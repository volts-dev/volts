package registry

import (
	"hash/fnv"
	"sort"
	"strconv"
)

// EndpointsKey 是一组端点的稳定指纹，用作合并注册项时的**分组键之一**。
//
// # 它挡的是什么
//
// 注册中心里一个服务名下可以有好几个注册项（= 好几个进程）：一个模块被别的进程
// blank-import 带起来，那个进程就也注册了这个服务名。合并时若只按 `Version` 分组，
// 结果是 **nodes 全并、endpoints 只留第一个注册项的** —— "这个服务有哪些端点"与
// "这些节点各自有哪些端点"就此脱钩。
//
// 后果不是报错，是**一半的请求 404**：客户端按合并后的节点列表轮询，打到那个不
// 实现该端点的进程上，volts 的路由树找不到路径，回一句
// `rpc status NotFound: server returned no detail` —— 它与"这个部署根本没装那个
// 模块"长得一模一样，于是上层往往把瞬时故障当成永久缺失处理掉。
//
// 真栈实测（2026-09-03，vectors）：`calendar` 服务名下两个进程，一个是 calendar
// 本体（223 个端点，含 `/mail.activity.systray_get_activities`），另一个是
// blank-import 了 calendar 的 hr_svc（222 个端点，编译于该端点加入之前）。顶栏那个
// 活动菜单于是**一次成一次不成**，而两边日志都是干净的。滚动升级期间同一个服务名
// 下新旧二进制并存，这个形状是必然会出现的，不是本机特例。
//
// # 为什么是"指纹"而不是"每个注册项一条"
//
// 端点集相同的进程（同一份二进制的多个副本，即绝大多数情况）仍然合并成一个
// Service，与改动前完全一致；只有端点集**确实不同**时才分开。这样上层看到的形状
// 只在真出问题时才变。
//
// 拆开不影响负载均衡：selector 的策略把所有 Service 的 Nodes 合起来再挑
// （见 selector/strategy.go）。
//
// # 只在按节点派发的路径上用
//
// GetService（选节点的入口）与 registryCacher 的 watch 合并用它。ListServices 不用
// ——那里回答的是"集群里有哪些服务"，同名出现两条只会让调用方重复计数。
func EndpointsKey(eps []*Endpoint) string {
	if len(eps) == 0 {
		return "0"
	}
	names := make([]string, 0, len(eps))
	for _, ep := range eps {
		if ep == nil {
			continue
		}
		names = append(names, ep.Name)
	}
	// 排序是必须的：注册项里端点的顺序来自各自的路由注册序，同一份二进制两次启动
	// 都可能不同（map 遍历），不排的话同一个端点集会算出不同的指纹，于是每个进程
	// 各自成一组 —— 过滤器照样能工作，但白白多出几份缓存。
	sort.Strings(names)

	h := fnv.New64a()
	for _, n := range names {
		_, _ = h.Write([]byte(n))
		_, _ = h.Write([]byte{0}) // 分隔符：否则 "ab"+"c" 与 "a"+"bc" 同值
	}
	return strconv.FormatUint(h.Sum64(), 16)
}
