package router

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

// stateMid 是一个**带每请求状态**的路由级中间件，形状同 vectors 的 TenantSession：
// Handler 把这次请求的身份写进自己的字段，下游（以及它自己后面几行）再读回来。
type stateMid struct {
	who     string
	arrived chan<- *stateMid
	release <-chan struct{}
}

func (m *stateMid) Name() string { return "stateMid" }
func (m *stateMid) Reset()       { m.who = "" }
func (m *stateMid) Handler(ctx IContext) {
	c := ctx.(*THttpContext)
	m.who = c.Request().Header().Get("Who")
	m.arrived <- m
	<-m.release
	c.Response().Write([]byte(m.who)) // 读回自己写的那一位：共享实例下这里读到的是别人的
	ctx.Next()
}

// 路由级中间件（`Url(methods, path, h, func(IRouter) IMiddleware)`）必须**每个在途请求
// 一份实例**。
//
// 此前 MiddlewareCreator 在 init 里把造出来的实例写回 handle 结构，而 clone 只拷
// handle 指针——第一个克隆写进去的那份，被之后所有克隆（并发请求池里没有空闲实例时
// 现克隆的）原样拿来用。于是同一路由上的并发请求共享一个中间件实例，谁的字段都可能被
// 另一个请求改掉。vectors 的 TenantSession 就是这样挂在每个模型的 RPC 路由上的
// （module.UseRpc）：压测时 `self.Token` 被撕裂（WithToken SIGSEGV / "That's not even
// a token"），或被另一个服务身份请求清空后把用户请求当成服务调用处理。
func TestRouteMiddleware_OneInstancePerInflightRequest(t *testing.T) {
	r := New()
	defer close(r.exit)

	arrived := make(chan *stateMid, 2)
	release := make(chan struct{})
	creator := func(IRouter) IMiddleware { return &stateMid{arrived: arrived, release: release} }

	grp := NewGroup()
	grp.Url([]string{"GET"}, "/who", func(ctx *THttpContext) {}, creator)
	r.RegisterGroup(grp)

	bodies := make([]string, 2)
	var wg sync.WaitGroup
	for i, who := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(i int, who string) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/who", nil)
			req.Header.Set("Who", who)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, transport.NewHttpRequest(req))
			bodies[i] = w.Body.String()
		}(i, who)
	}

	// 两个请求都停在中间件里之后才放行——这样第二个请求从池里拿不到空闲 handler，
	// 必然走现克隆那条路。
	var seen [2]*stateMid
	for i := range seen {
		select {
		case seen[i] = <-arrived:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("两个并发请求没有都进到中间件里")
		}
	}
	close(release)
	wg.Wait()

	if seen[0] == seen[1] {
		t.Fatalf("两个在途请求拿到的是同一个路由级中间件实例 %p；各自读回的身份 %q", seen[0], bodies)
	}
	if bodies[0] != "alice" || bodies[1] != "bob" {
		t.Fatalf("请求读回的身份串了：%q", bodies)
	}
}
