package router

import (
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/registry"
)

// 既有的回收用例都按"当场回收"写；宽限期的行为由本文件的用例单独锁定（各自临时调回非零）。
func init() { proxyReclaimGrace = 0 }

func withGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := proxyReclaimGrace
	proxyReclaimGrace = d
	t.Cleanup(func() { proxyReclaimGrace = old })
}

// 服务消失后路由先留着（期间代理选不到节点回 503），宽限期满才回收（之后 404）。
// 当场回收时，websvc 一次重启让 /、/store 经网关回 0 字节 404 共 66 秒（10-23 真栈）。
func TestReclaim_GraceKeepsRouteUntilExpiry(t *testing.T) {
	withGrace(t, 80*time.Millisecond)
	r := newReclaimRouter(t)
	r.store([]*registry.Service{remoteService("website", "/w/a")})
	r.dropProxyRoutes("website")
	if !hasRoute(r, "/w/a") {
		t.Fatal("宽限期内路由应还在（选不到节点回 503，而不是 404）")
	}
	time.Sleep(250 * time.Millisecond)
	if hasRoute(r, "/w/a") {
		t.Error("宽限期满服务没回来，路由应已回收")
	}
}

// 宽限期内服务回来（重启的常态）：到期时这条路由已被重新登记，不能回收。
func TestReclaim_GraceServiceComesBack(t *testing.T) {
	withGrace(t, 80*time.Millisecond)
	r := newReclaimRouter(t)
	svc := remoteService("website", "/w/a")
	r.store([]*registry.Service{svc})
	r.dropProxyRoutes("website")
	r.store([]*registry.Service{svc}) // 新进程注册回来
	time.Sleep(250 * time.Millisecond)
	if !hasRoute(r, "/w/a") {
		t.Error("服务在宽限期内回来了，到期时不该回收它的路由")
	}
}

// staleCacher：GetService 回事件之前的那份（缓存落后），Fresh 回后端的新状态。
type staleCacher struct {
	registry.IRegistryCacher
	mu           sync.Mutex
	stale, fresh []*registry.Service
	freshCalls   int
}

func (c *staleCacher) GetService(string) ([]*registry.Service, error) { return c.stale, nil }
func (c *staleCacher) Fresh(string) ([]*registry.Service, error) {
	c.mu.Lock()
	c.freshCalls++
	c.mu.Unlock()
	return c.fresh, nil
}

func svcWithNodes(ids ...string) []*registry.Service {
	s := &registry.Service{Name: "website", Version: "latest"}
	for _, id := range ids {
		s.Nodes = append(s.Nodes, &registry.Node{Id: id})
	}
	return []*registry.Service{s}
}

// watch 事件比缓存新（两条独立的 watch 流）：读到的缓存没反映这条事件，就绕过缓存读后端。
// 不这样做，store() 按只有 rpc 节点的旧数据建出零条 HTTP 路由并记下指纹，要等下一轮 refresh。
func TestServicesAfterEvent_StaleCacheReadsFresh(t *testing.T) {
	c := &staleCacher{stale: svcWithNodes("rpc-1"), fresh: svcWithNodes("rpc-1", "http-1")}
	r := &TRouter{config: &Config{RegistryCacher: c}}

	got, err := r.servicesAfterEvent(&registry.Result{Action: "create", Service: svcWithNodes("http-1")[0]})
	if err != nil || len(got) != 1 || len(got[0].Nodes) != 2 || c.freshCalls != 1 {
		t.Fatalf("缓存落后时应改读后端：got=%v err=%v fresh=%d", got, err, c.freshCalls)
	}
	// 缓存已经反映了事件：不多读。
	got, _ = r.servicesAfterEvent(&registry.Result{Action: "create", Service: svcWithNodes("rpc-1")[0]})
	if len(got[0].Nodes) != 1 || c.freshCalls != 1 {
		t.Errorf("缓存一致时不该绕过它：nodes=%d fresh=%d", len(got[0].Nodes), c.freshCalls)
	}
	// delete 的节点还在缓存里：同样落后。
	if _, _ = r.servicesAfterEvent(&registry.Result{Action: "delete", Service: svcWithNodes("rpc-1")[0]}); c.freshCalls != 2 {
		t.Errorf("delete 的节点还在缓存里时应改读后端，fresh=%d", c.freshCalls)
	}
}
