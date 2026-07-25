package router

import (
	"net"
	"net/http"
	"strconv"

	"github.com/volts-dev/volts/internal/addr"
)

// 反向代理自环 / 转发环防护。
//
// 背景：注册中心（尤其 etcd 跨进程发现）里，某个被代理服务的节点地址完全可能
// 解析成网关自身的监听地址 —— 同进程托管该服务、多进程用 server.Debug() 撞同
// 一端口、或注册树抖动导致 store() 的 filteSelf 一时认不出自己。一旦发生，反代
// 会把请求拨回自己的监听端口，再次进入 ServeHTTP，如此无限自环；每一跳新建一条
// loopback 连接，几秒钟打光临时端口，报 "connect: can't assign requested
// address"(EADDRNOTAVAIL)，整个进程随之瘫痪。
//
// 这里放两道与注册中心状态无关的护盾，任一单独即可阻止端口耗尽：
//
//  1. 跳数上限（proxyDepthHeader）：每经过一次 volts 反代，请求头里的深度 +1；
//     超过 maxProxyDepth 直接拒绝。它不依赖“识别自身地址”，因此连跨机器的
//     A→B→A 环也一并封顶，把任何转发环的连接数限制在常数级。
//  2. 自身地址短路（isSelfTarget）：拨号前若目标就是本进程监听端口（同端口的
//     loopback），直接拒绝，连一跳都不浪费。自身地址取自 stdlib 通过
//     http.LocalAddrContextKey 注入的本连接监听地址 —— 即请求真正到达的那个
//     socket，永远正确，与注册中心状态无关。
const (
	proxyDepthHeader = "X-Volts-Proxy-Depth"

	// maxProxyDepth 是单个请求允许经过的 volts 反代跳数上限。正常拓扑至多 1~2
	// 跳，给到 8 是宽裕冗余；触顶只可能是环。
	maxProxyDepth = 8
)

// proxyDepthOf 读取并解析请求已累计的反代跳数；缺失或非法一律按 0。
func proxyDepthOf(r *http.Request) int {
	if r == nil {
		return 0
	}
	n, err := strconv.Atoi(r.Header.Get(proxyDepthHeader))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// localListenAddr 返回本请求所到达的本机监听地址。
//
// 来源是 stdlib http.Server 注入的 http.LocalAddrContextKey —— 即接受该连接的
// 那个 socket 的本地地址。它是“本进程自身监听地址”的权威且注册中心无关来源：
// 请求实际就是从这个 socket 进来的。取不到时返回 nil（自身地址短路随之失效，
// 但跳数护盾仍然兜底）。
func localListenAddr(r *http.Request) net.Addr {
	if r == nil {
		return nil
	}
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return la
	}
	return nil
}

// isSelfTarget 判断“拨号 targetHostPort 是否会回到本进程自己的监听端口”。
//
// 判据：端口等于本连接的监听端口，且目标主机指向本机 —— 二者同时成立即可断定是
// 自身。一台机器上一个端口只能被一个进程绑定，所以“本机 + 同端口”就是本进程。
//
// ★ 本机的判定必须覆盖 loopback **和** 本机网卡 IP。此前只认 loopback，而注册中心
// 里存的恰恰是网卡地址（如 192.168.x.x:35999）：正常情况上游 addr.LocalFormat 会把
// 它折成 127.0.0.1 从而命中，可一旦折叠失效 —— IP 缓存 10s TTL 内网络刚切换、或
// 注册的是已失效的旧 IP（进程被 SIGKILL 后 lease 未撤销，僵尸节点带着旧地址留在
// 注册中心里）—— 目标就以 192.168.x.x 原样进来，isLoopbackHost 判否，护盾整个被
// 绕过，请求真的拨回自己，只能靠跳数上限在第 8 跳兜底。而 filteSelf 归一化两侧
// 地址时同样会因这个原因错过，于是自环路由从源头就建了出来。
//
// 先比端口再判本机：端口不同必然不是自身，可以直接短路，把 IsLocal 的网卡枚举
// 开销挡在绝大多数请求之外。listen 为 nil（拿不到监听地址）时保守返回 false，
// 交给跳数护盾。
func isSelfTarget(targetHostPort string, listen net.Addr) bool {
	if listen == nil {
		return false
	}
	th, tp, err := net.SplitHostPort(targetHostPort)
	if err != nil {
		return false
	}
	_, lp, err := net.SplitHostPort(listen.String())
	if err != nil {
		return false
	}

	// 端口不同 → 必非本进程，短路，不做代价较高的本机 IP 枚举。
	if tp != lp {
		return false
	}

	return isLoopbackHost(th) || addr.IsLocal(th)
}

// isLoopbackHost 报告 host 是否为环回地址（localhost / 127.0.0.0/8 / ::1）。
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
