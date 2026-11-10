package transport

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	vaddr "github.com/volts-dev/volts/internal/addr"
)

// 进程内直连：拨号目标就是本进程自己的 TCP 监听时，不走内核网络栈，改用一对
// net.Pipe —— 一端交给调用方，另一端交给那个监听**原有的**逐连接服务循环。
//
// # 为什么切在拨号这一层
//
// 一个进程里常装着几十个模块（服务名不同、监听同一个端口）。它们之间的调用照样经
// 注册中心选点、再拨到自己的端口上：一次本机 TCP 往返，外加这台机器上网络扩展的
// 拦截。要省掉的只是这一段。
//
// 切在 Dial 上，**除了 net.Conn 以外全部是同一份代码**：连接池、消息编解码、帧头
// 元数据、超时与 deadline、服务端的 context（不含调用方的任何值）、panic 兜底、
// 路由与整条中间件链（鉴权、租户作用域、ACL）。上层看不出区别，也不应该看得出 ——
// 同一个调用在单体部署和拆分部署下必须是同一个语义。更靠上的短路（直接调 handler
// 函数）省得更多，但每跳过一层就多一处"同进程与跨进程行为不同"，那正是要消除的东西。
//
// # 什么时候不走
//
//   - 拨号方开了 TLS（Secure / TlsConfig）：对端身份可能正由证书承担，管道没有证书。
//     进程级的服务间 TLS（peer_tls.go）**照走**：那张证书证明的是"我是本集群的哪个
//     进程"，而管道两端是同一个进程。服务端经 PeerOf 认得出这是本进程。
//   - 监听方开了 TLS：不登记，理由同上。
//   - Config.DisableInproc：显式要求真网络（压测、抓包、对照测试）。
//   - 目标地址不是本机：本表只登记本进程的监听，别的主机的地址查不到。

type (
	inprocServeFunc func(net.Conn)

	// inprocEntry 包一层是为了注销时能认出"是不是我登记的那一条"（闭包不可比较）。
	inprocEntry struct{ serve inprocServeFunc }
)

var inproc = struct {
	sync.RWMutex
	byPort map[int]*inprocEntry
}{byPort: make(map[int]*inprocEntry)}

// registerInproc 登记本进程的一个监听；返回注销函数。
//
// 按端口登记：本机上一个端口只可能被一个进程监听，所以"地址指向本机 + 端口在表里"
// 就足以断定目标是本进程。
func registerInproc(addr net.Addr, serve inprocServeFunc) func() {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.Port == 0 {
		return func() {}
	}
	port := tcp.Port
	e := &inprocEntry{serve: serve}
	inproc.Lock()
	inproc.byPort[port] = e
	inproc.Unlock()
	return func() {
		inproc.Lock()
		// 只删自己登记的那一条：同一端口可能已被重新 Serve 覆盖。
		if inproc.byPort[port] == e {
			delete(inproc.byPort, port)
		}
		inproc.Unlock()
	}
}

// inprocFor 返回拨号地址对应的本进程监听；不是本进程的返回 nil。
func inprocFor(addr string) inprocServeFunc {
	host, portStr, err := net.SplitHostPort(vaddr.LocalFormat(addr))
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil // LocalFormat 已把本机网卡 IP / 通配地址折成 loopback；折不了就不是本机
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil
	}
	inproc.RLock()
	e := inproc.byPort[port]
	inproc.RUnlock()
	if e == nil {
		return nil
	}
	return e.serve
}

// dialInproc 建一条进程内连接：返回调用方那一端，另一端交给监听的服务循环。
func dialInproc(serve inprocServeFunc) net.Conn {
	client, server := net.Pipe()
	closed := new(atomic.Bool)
	go serve(&inprocConn{Conn: server, closed: closed})
	return &inprocConn{Conn: client, closed: closed}
}

// Both ends share their close state so pool reuse does not need a timed Read.
// The pipe is private to our RPC stack; unlike a TCP peer it cannot inject
// unsolicited bytes from outside this process. PeerOf also recognizes this type.
type inprocConn struct {
	net.Conn
	closed *atomic.Bool
}

func (c *inprocConn) IsClosed() bool { return c.closed.Load() }

func (c *inprocConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}
