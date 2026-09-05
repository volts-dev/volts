package router

import (
	"strings"

	"github.com/volts-dev/volts/registry"
)

// 代理路由的回收。
//
// # 为什么需要它
//
// `store()` 只增不减：它按注册中心的发现结果建反向代理路由，而没有任何地方把建过的
// 路由再拿下来。`TRouter.Deregister` 在整个仓里只有一个测试在调，生产代码一次都没调过。
// 于是：
//
//   - 一个服务的最后一个节点注销之后，它的代理路由**永远**留在树里。请求打过来选不到
//     节点，答的是 503 —— 而正确答案是 404，这条路径已经不存在了。两者的区别不只是
//     好看：503 是"稍后重试"，客户端和网关都会照做，于是一条永远不会回来的路径被反复
//     重试。
//   - 服务升级后砍掉一个端点时同理，旧路径继续以 503 存在。
//
// # 为什么可以安全地删
//
// `TTree.DelRoute` 不是把节点整个拿掉，而是从节点的路由上**按 handler id 剥离**
// （`route.stripHandler`），剥空了且没有子节点才移除节点。所以只要剥离的是我们自己
// 建那条代理 handler 的 id，同路径上任何别的 handler（尤其是本地 handler）都不受影响。
//
// 因此这里记的是"store() 实际建成了哪几条"，连 handler id 一起记 —— 不是事后按路径去
// 猜。安全性主要来自 id 本身：它是 `service:path:...` 的 crc32，别的服务、别的本地
// handler 都不可能撞上，剥不中就什么也不会发生。在此之上还有两道闸：
//
//   - **回查树**：删之前确认那条路由此刻确实还挂着我们记下的 id。`TTree.addNode` 对
//     同路径是后写覆盖，被本地路由或别的服务顶替过之后我们的 id 早已不在。这一道是
//     防御性的（剥不中本来也无害），它省掉一次持写锁的整树遍历，并且让 `TTree.Count`
//     那个用于打印的计数不至于被无谓地减。
//   - **别的服务还在广告同一条路径就不删**。这一道是**吃重**的：两个服务广告同一路径
//     时树里只有一条路由（后写者的），下线后写的那个会把整条路径删掉，而先写的那个
//     还活着 —— 要等下一轮 refresh（最多 60s）才补回来。
//     回归 `TestReclaim_KeepsPathClaimedByAnotherService` 必须下线**当前挂在树上的
//     那个**；反过来测什么也证明不了，闸一在回查树时就已经放弃了。

// proxyRouteKey 唯一标识一条被建出来的代理路由。
//
// 用「方法集 + 路径」而不是只用路径：注册中心里 GET /x 与 POST /x 是**两条**独立端点
// （见 server 的 dedupEndpoints 按 Method+Path 去重），各自建一条路由。
type proxyRouteKey struct {
	methods string // strings.Join(ep.Method, ",")
	path    string
}

func newProxyRouteKey(ep *registry.Endpoint) proxyRouteKey {
	return proxyRouteKey{methods: strings.Join(ep.Method, ","), path: ep.Path}
}

func (k proxyRouteKey) methodList() []string {
	if k.methods == "" {
		return nil
	}
	return strings.Split(k.methods, ",")
}

// trackProxyRoutes 记下某个服务本轮建成的全部代理路由，并回收上一轮有、这一轮没有的。
//
// created 为空表示这一轮一条都没建（全被护盾挡下、或服务只剩 RPC 端点）——那也是有效
// 信息，说明先前建的都该回收，不能当作"没数据所以跳过"。
func (self *TRouter) trackProxyRoutes(service string, created map[proxyRouteKey]int) {
	var prev map[proxyRouteKey]int
	if v, ok := self.proxyRoutes.Load(service); ok {
		prev, _ = v.(map[proxyRouteKey]int)
	}

	if len(created) == 0 {
		self.proxyRoutes.Delete(service)
	} else {
		self.proxyRoutes.Store(service, created)
	}

	for key, handlerId := range prev {
		if _, still := created[key]; still {
			continue
		}
		self.reclaimProxyRoute(service, key, handlerId)
	}
}

// dropProxyRoutes 回收某个服务的全部代理路由。用于服务从注册中心整个消失时。
func (self *TRouter) dropProxyRoutes(service string) {
	v, ok := self.proxyRoutes.LoadAndDelete(service)
	if !ok {
		return
	}
	routes, _ := v.(map[proxyRouteKey]int)
	if len(routes) == 0 {
		return
	}
	for key, handlerId := range routes {
		self.reclaimProxyRoute(service, key, handlerId)
	}
	log.Infof("router: 服务 %q 已从注册中心消失，回收它的 %d 条代理路由（此后这些路径回 404 而不是 503）",
		service, len(routes))
}

// reclaimProxyRoute 从路由树上剥掉一条代理路由。
func (self *TRouter) reclaimProxyRoute(service string, key proxyRouteKey, handlerId int) {
	methods := key.methodList()
	if len(methods) == 0 || key.path == "" {
		return
	}

	// 别的服务还在广告同一条路径就不动 —— 树里同路径只有一条路由，删了会把
	// 还活着的那个服务一起打掉，要等下一轮 refresh（最多 60s）才补回来。
	if self.pathClaimedByOtherService(service, key) {
		return
	}

	// 确认树上那条路由此刻确实还挂着我们记的这个 handler id。被本地路由或别的服务
	// 顶替过就不动 —— 那时我们的 id 早已不在，DelRoute 什么也剥不掉（这是安全的），
	// 但会白跑一次持写锁的整树遍历，并把用于打印的 TTree.Count 无谓地减一。
	var stale []string
	var routeId int
	for _, m := range methods {
		r := self.tree.RouteOf(m, key.path)
		if r == nil {
			continue
		}
		for _, id := range r.Handlers() {
			if id == handlerId {
				stale = append(stale, m)
				routeId = r.Id()
				break
			}
		}
	}
	if len(stale) == 0 {
		return
	}

	victim := EndpiontToRoute(&registry.Endpoint{Method: stale, Path: key.path})
	victim.handlers = []int{handlerId}
	if err := self.tree.DelRoute(key.path, victim); err != nil {
		log.Errf("router: 回收服务 %q 的代理路由 %v %s 失败: %v", service, stale, key.path, err)
		return
	}

	// 路由整条没了才清上下文池：还挂着别的 handler 时那个池仍然在服务它。
	if routeId != 0 && self.tree.RouteOf(stale[0], key.path) == nil {
		self.httpCtxPool.Delete(routeId)
		self.rpcCtxPool.Delete(routeId)
	}
}

// pathClaimedByOtherService 报告除 self 之外是否还有别的服务在广告同一条路径。
func (self *TRouter) pathClaimedByOtherService(service string, key proxyRouteKey) bool {
	var claimed bool
	self.proxyRoutes.Range(func(k, v any) bool {
		name, _ := k.(string)
		if name == service {
			return true
		}
		routes, _ := v.(map[proxyRouteKey]int)
		if _, ok := routes[key]; ok {
			claimed = true
			return false
		}
		return true
	})
	return claimed
}

// reconcileProxyRoutes 回收所有"仍在记账、但注册中心已经没有"的服务。
//
// 只在 ListServices **成功**之后调用：读失败时列表是空的，那不是"服务都没了"。
//
// 误删是自愈的：watch 事件或下一轮 refresh（≤60s）会把路由重新建起来。而不删是永久的
// —— 这正是 store() 只增不减留下的问题。所以这里的取舍方向与 isLocalService 那里一致：
// 宁可多跑一轮，也不留下一条永远不会被纠正的坏路由。
func (self *TRouter) reconcileProxyRoutes(list []*registry.Service) {
	present := make(map[string]struct{}, len(list))
	for _, s := range list {
		if s != nil {
			present[s.Name] = struct{}{}
		}
	}

	var gone []string
	self.proxyRoutes.Range(func(k, _ any) bool {
		if name, ok := k.(string); ok {
			if _, alive := present[name]; !alive {
				gone = append(gone, name)
			}
		}
		return true
	})
	for _, name := range gone {
		self.dropProxyRoutes(name)
	}
}
