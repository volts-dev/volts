package transport

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// 服务间 TLS（peer TLS）：本集群进程之间的 RPC 线路加密并互认身份。
//
// # 与 Config.Secure / TlsConfig 的区别
//
// 那两个是**这一个传输实例**的设置，谁建传输谁配。服务间的 RPC 客户端却散在各处
// （每个服务名一个、还有控制器自己 NewRpcClient 的），逐个配一定会漏，而漏掉的那一个
// 在 PeerTLSOn 下的表现只是"某几个调用连不上"。所以它是**进程级**的：SetPeerTLS 一次，
// 本进程每个没显式配 TLS 的 TCP 传输都按它拨号与监听。显式配了 Secure / TlsConfig 的
// 传输照旧按自己的来。
//
// # 四档，为了滚动切换不断线
//
//	           监听收明文   监听收 TLS   拨号用
//	Off        收           —           明文
//	Accept     收           收          明文
//	Dual       收           收          TLS
//	On         拒           收          TLS
//
// 同一个端口两种都收靠**首字节**区分：TLS 握手第一个字节恒为 0x16（handshake 记录），
// RPC 帧恒为 MagicNumber（0x08）。全体先切 Accept，再全体切 Dual，最后全体切 On ——
// 每一步里新旧两档的进程都能互通，任何一步都可以按进程逐个重启。
//
// # 身份
//
// 证书怎么来、对端怎么认，由调用方放进两份 tls.Config（Server 要带 ClientAuth 与
// VerifyPeerCertificate，Client 要带客户端证书）。传输层只负责把握手做完，并让服务端
// 拿得到对端：PeerOf。
//
// # 进程内直连照走
//
// 管道两端是同一个进程，身份就是自己，没什么可加密、可互认的 —— 这正是"同进程调用
// 不付 TLS 的代价"的落点。PeerOf 对它报 inproc=true，由上层按"对端是本进程"处理。

// PeerTLSMode 服务间 TLS 的档位，见文件头。
type PeerTLSMode int32

const (
	PeerTLSOff PeerTLSMode = iota
	PeerTLSAccept
	PeerTLSDual
	PeerTLSOn
)

func (m PeerTLSMode) String() string {
	switch m {
	case PeerTLSAccept:
		return "accept"
	case PeerTLSDual:
		return "dual"
	case PeerTLSOn:
		return "on"
	}
	return "off"
}

// PeerTLS 是进程级的服务间 TLS 设置。
type PeerTLS struct {
	Mode PeerTLSMode
	// Server 监听侧。要互认就得带 ClientAuth（RequireAnyClientCert + 自己的
	// VerifyPeerCertificate，或 RequireAndVerifyClientCert + ClientCAs）。
	Server *tls.Config
	// Client 拨号侧，带本进程的客户端证书。
	Client *tls.Config
}

var peerTLS atomic.Pointer[PeerTLS]

// SetPeerTLS 装上本进程的服务间 TLS；nil 等于 Off。
//
// Mode 不是 Off 时 Server 必须给（Accept 起就要能收 TLS）；Mode 是 Dual / On 时
// Client 也必须给。缺了返回错误 —— 运维意图是"要加密"，静默退回明文正是要消除的状态。
func SetPeerTLS(p *PeerTLS) error {
	if p == nil || p.Mode == PeerTLSOff {
		peerTLS.Store(nil)
		return nil
	}
	if p.Server == nil {
		return errors.New("transport: peer TLS 档位 " + p.Mode.String() + " 需要 Server 配置")
	}
	if p.Mode >= PeerTLSDual && p.Client == nil {
		return errors.New("transport: peer TLS 档位 " + p.Mode.String() + " 需要 Client 配置")
	}
	cp := *p
	cp.Server = withMinTLS12(p.Server)
	if p.Client != nil {
		cp.Client = withMinTLS12(p.Client)
	}
	peerTLS.Store(&cp)
	return nil
}

// CurrentPeerTLS 返回本进程的服务间 TLS；没开时为 nil。
func CurrentPeerTLS() *PeerTLS {
	return peerTLS.Load()
}

func withMinTLS12(c *tls.Config) *tls.Config {
	if c.MinVersion >= tls.VersionTLS12 {
		return c
	}
	c = c.Clone()
	c.MinVersion = tls.VersionTLS12
	return c
}

// peerTLSFor 返回这个传输该用的服务间 TLS：显式配了 Secure / TlsConfig 的传输不归它管。
func (self *tcpTransport) peerTLSFor() *PeerTLS {
	if self.config.Secure || self.config.TlsConfig != nil {
		return nil
	}
	return peerTLS.Load()
}

// tlsRecordHandshake 是 TLS 记录层 handshake 类型，ClientHello 的第一个字节。
const tlsRecordHandshake = 0x16

// peerHandshakeTimeout 是认首字节 + 握手的时限。读超时没配时也得有一个 ——
// 否则一条只连不发的连接能一直占着一个 goroutine（Slowloris）。
const peerHandshakeTimeout = 10 * time.Second

// acceptPeer 按档位处理一条刚接进来的连接：认首字节，TLS 就握手，明文看档位收不收。
// ok=false 时连接已关掉。
func acceptPeer(conn net.Conn, p *PeerTLS, readTimeout time.Duration) (net.Conn, bool) {
	limit := peerHandshakeTimeout
	if readTimeout > 0 && readTimeout < limit {
		limit = readTimeout
	}
	conn.SetDeadline(time.Now().Add(limit))

	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil {
		conn.Close()
		return nil, false
	}
	pc := &prefixConn{Conn: conn, r: io.MultiReader(bytes.NewReader(first[:]), conn)}

	if first[0] != tlsRecordHandshake {
		if p.Mode >= PeerTLSOn {
			plainRejectedLog(conn.RemoteAddr())
			conn.Close()
			return nil, false
		}
		conn.SetDeadline(time.Time{})
		return pc, true
	}

	tc := tls.Server(pc, p.Server)
	if err := tc.Handshake(); err != nil {
		handshakeFailedLog(conn.RemoteAddr(), err)
		conn.Close()
		return nil, false
	}
	conn.SetDeadline(time.Time{})
	return tc, true
}

// prefixConn 把认首字节时读掉的那一个字节还回去。
type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// inprocConn 标记进程内直连的服务端那一端，供 PeerOf 认出来。
type inprocConn struct{ net.Conn }

// PeerOf 报告一条服务端连接的对端。
//
//   - TLS 连接：返回握手时对端出示的证书链（已经过 tls.Config 里的校验）；
//   - 进程内直连：inproc=true，对端就是本进程；
//   - 明文连接：两者皆空。
//
// conn 一般取自请求 context 上的 RemoteConnContextKey。
func PeerOf(conn net.Conn) (certs []*x509.Certificate, inproc bool) {
	switch c := conn.(type) {
	case *tls.Conn:
		return c.ConnectionState().PeerCertificates, false
	case *inprocConn:
		return nil, true
	}
	return nil, false
}

// 两类拒绝各自按对端地址节流（十分钟一条）：被拒的那一方只会看到连接被关，原因只在这里。
var (
	peerLogMu sync.Mutex
	peerLogAt = map[string]time.Time{}
)

const peerLogEvery = 10 * time.Minute

func peerLogDue(key string) bool {
	peerLogMu.Lock()
	defer peerLogMu.Unlock()
	if at, ok := peerLogAt[key]; ok && time.Since(at) < peerLogEvery {
		return false
	}
	if len(peerLogAt) > 1024 {
		peerLogAt = map[string]time.Time{}
	}
	peerLogAt[key] = time.Now()
	return true
}

func remoteHost(a net.Addr) string {
	if a == nil {
		return ""
	}
	if h, _, err := net.SplitHostPort(a.String()); err == nil {
		return h
	}
	return a.String()
}

func plainRejectedLog(a net.Addr) {
	if peerLogDue("plain|" + remoteHost(a)) {
		log.Warnf("rpc: 拒绝来自 %s 的明文连接（服务间 TLS 档位 on）—— 对端还没切到 dual，或不是本集群的进程", a)
	}
}

func handshakeFailedLog(a net.Addr, err error) {
	if peerLogDue("tls|" + remoteHost(a) + "|" + err.Error()) {
		log.Warnf("rpc: 来自 %s 的服务间 TLS 握手失败: %v", a, err)
	}
}
