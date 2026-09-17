package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/transport"
)

// warnSink 收下 Warn 及以上的日志，供断言"那条告警到底有没有发出去"。
type warnSink struct {
	mu      sync.Mutex
	entries []logger.Entry
}

func (s *warnSink) Wants(l logger.Level) bool {
	return l.Severity() >= logger.LevelWarn.Severity()
}

func (s *warnSink) Write(e logger.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func (s *warnSink) find(sub string) (logger.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if strings.Contains(e.Message, sub) {
			return e, true
		}
	}
	return logger.Entry{}, false
}

// TestHttpReverseProxy_NamesTheHopThatRejectedOversizedHeaders 锁定：上游因请求头过大
// 回 431 时，网关必须在日志里点名**是哪一跳**拒的、请求头有多大。
//
// 为什么值得一条测试：431 是标准库在上游**任何 handler 之前**回的 —— 上游不进路由、
// 不进中间件、一行日志都没有；转一手之后调用方只看到一个不知道出自哪里的 431。
// 2026-09-09 那次故障（工作台主机名下 .ti.com 的 cookie 撑爆上限）就是因此只能逐跳
// 手工灌 cookie 去二分，查了一上午。缺了这条告警，下次还是一样贵。
//
// ★ 上游必须是**真服务器**并且真的把 MaxHeaderBytes 调小：431 是读连接时施加的，
// 让 handler 自己 WriteHeader(431) 测不到"标准库在 handler 之前拒掉"这件事。
func TestHttpReverseProxy_NamesTheHopThatRejectedOversizedHeaders(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("上游的 handler 不该被调用 —— 请求头超限时标准库在这之前就拒了")
	}))
	upstream.Config.MaxHeaderBytes = 1 << 10 // 1KiB：比下面那条 cookie 小得多
	upstream.Start()
	defer upstream.Close()

	up, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	// 服务名带一次性后缀：memory 注册中心是**进程级**的，同名服务在 -count=2 下会把
	// 上一轮那个已经关掉的节点留在表里，选点选中它就是 502（既有的两条 e2e 正栽在这里）。
	svcName := fmt.Sprintf("gwsvc431_%d", time.Now().UnixNano())

	reg := memory.New()
	if err := reg.Register(&registry.Service{
		Name: svcName,
		Nodes: []*registry.Node{{
			Id:       "n1",
			Address:  up.Host,
			Metadata: map[string]string{"protocol": "http"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	r := New(WithRegistry(reg))
	defer r.Close()

	grp := NewGroup(WithAloneService(true, svcName))
	grp.Url([]string{"GET"}, "/shop", HttpReverseProxy)
	r.RegisterGroup(grp)

	sink := &warnSink{}
	defer logger.AddSink(sink)()

	req := httptest.NewRequest("GET", "/shop", nil)
	req.Header.Set("Cookie", "bloat="+strings.Repeat("a", 8<<10)) // 本进程放行，上游放不下
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))

	if w.Code != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("状态 %d，want 431 —— 上游拒了但网关没把它转回来，这条测试没测到东西", w.Code)
	}

	e, ok := sink.find("431")
	if !ok {
		t.Fatal("上游回了 431，网关一声不吭 —— 调用方拿到的 431 就无从判断是哪一跳拒的（这正是要修的那件事）")
	}
	if !strings.Contains(e.Message, up.Host) {
		t.Errorf("告警里没点名上游地址 %q: %s", up.Host, e.Message)
	}
	if !strings.Contains(e.Message, "InboundHeaderBytes") {
		t.Errorf("告警里没说请求头有多大，排查时还得自己去量: %s", e.Message)
	}
}

// TestRequestHeaderBytes 锁定字节数是**按线路格式**算的（请求行 + 每个头 + 空行），
// 而不是随手把值长度加一加 —— 告警里那个数要能跟 max_header_bytes 直接比大小，
// 差一个量级就失去意义。
func TestRequestHeaderBytes(t *testing.T) {
	if got := requestHeaderBytes(nil); got != 0 {
		t.Fatalf("nil 请求应为 0，得到 %d", got)
	}

	req := httptest.NewRequest("GET", "http://example.com/a", nil)
	req.Header = http.Header{} // 只留 Host，逐项可数
	// "GET /a HTTP/1.1\r\n" = 17；"Host: example.com\r\n" = 19；末尾 "\r\n" = 2
	if got, want := requestHeaderBytes(req), 17+19+2; got != want {
		t.Errorf("requestHeaderBytes=%d，want %d", got, want)
	}

	req.Header.Set("Cookie", strings.Repeat("a", 1000))
	// 多出 "Cookie: " (8) + 1000 + "\r\n" (2)
	if got, want := requestHeaderBytes(req), 17+19+2+8+1000+2; got != want {
		t.Errorf("加一个 1000B 的头之后 =%d，want %d", got, want)
	}
}
