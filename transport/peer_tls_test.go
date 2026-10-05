package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
)

// peerRecordingHandler 回显载荷，并在后面标出 PeerOf 认出的对端：证书 CN / inproc / plain。
type peerRecordingHandler struct{}

func (h *peerRecordingHandler) ServeRPC(rsp *RpcResponse, req *RpcRequest) {
	peer := "plain"
	if c, ok := req.Context.Value(RemoteConnContextKey).(net.Conn); ok {
		certs, inproc := PeerOf(c)
		switch {
		case inproc:
			peer = "inproc"
		case len(certs) > 0:
			peer = "cert:" + certs[0].Subject.CommonName
		}
	}
	rsp.WriteHeader(StatusOK)
	rsp.Write(append(append([]byte{}, req.Message.Payload...), []byte("|"+peer)...))
}
func (h *peerRecordingHandler) String() string       { return "peerRecordingHandler" }
func (h *peerRecordingHandler) Handler() interface{} { return h }

func testPeerCert(t *testing.T, cn string) (tls.Certificate, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pub
}

// pinned 只认 trusted 里的公钥 —— 与 vectors 按信任表认人同一个形状。
func pinned(trusted ...ed25519.PublicKey) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("no peer certificate")
		}
		c, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		for _, k := range trusted {
			if pub, ok := c.PublicKey.(ed25519.PublicKey); ok && pub.Equal(k) {
				return nil
			}
		}
		return errors.New("peer not trusted")
	}
}

func testPeerTLS(t *testing.T, mode PeerTLSMode, own tls.Certificate, trusted ...ed25519.PublicKey) *PeerTLS {
	return &PeerTLS{
		Mode: mode,
		Server: &tls.Config{
			Certificates:          []tls.Certificate{own},
			ClientAuth:            tls.RequireAnyClientCert,
			VerifyPeerCertificate: pinned(trusted...),
		},
		Client: &tls.Config{
			Certificates:          []tls.Certificate{own},
			InsecureSkipVerify:    true, // 没有 CA：身份由 VerifyPeerCertificate 按公钥认
			VerifyPeerCertificate: pinned(trusted...),
		},
	}
}

func setPeerTLSForTest(t *testing.T, p *PeerTLS) {
	t.Helper()
	if err := SetPeerTLS(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetPeerTLS(nil) })
}

func servePeerForTest(t *testing.T, opts ...Option) IListener {
	t.Helper()
	tr := NewTCPTransport(append([]Option{ReadTimeout(5 * time.Second), WriteTimeout(5 * time.Second)}, opts...)...)
	ln, err := tr.Listen(":0")
	if err != nil {
		t.Fatal("Listen:", err)
	}
	go ln.Serve(&peerRecordingHandler{})
	t.Cleanup(func() { ln.Close() })
	return ln
}

func dialForTest(t *testing.T, addr string, opts ...Option) (IClient, error) {
	t.Helper()
	tr := NewTCPTransport(append([]Option{ReadTimeout(5 * time.Second), WriteTimeout(5 * time.Second)}, opts...)...)
	return tr.Dial(addr, WithTimeout(5*time.Second, 5*time.Second, 5*time.Second))
}

func peerRoundTrip(t *testing.T, c IClient) string {
	t.Helper()
	got := string(roundTrip(t, c, []byte("x")))
	return strings.TrimPrefix(got, "x|")
}

// 滚动切换的每一步，新旧两档的进程都要能互通。监听处在 dual（中间那一档，明文与 TLS
// 都收），拨号方分别是 off / accept（明文）与 dual（TLS）。
func TestPeerTLS_RollingModesInteroperate(t *testing.T) {
	cert, pub := testPeerCert(t, "kylin")
	p := testPeerTLS(t, PeerTLSDual, cert, pub)
	setPeerTLSForTest(t, p)
	ln := servePeerForTest(t, DisableInproc())

	for _, c := range []struct {
		dialer PeerTLSMode
		want   string
	}{
		{PeerTLSOff, "plain"},
		{PeerTLSAccept, "plain"},
		{PeerTLSDual, "cert:kylin"},
		{PeerTLSOn, "cert:kylin"},
	} {
		dp := *p
		dp.Mode = c.dialer
		setPeerTLSForTest(t, &dp)
		cl, err := dialForTest(t, ln.Addr().String(), DisableInproc())
		if err != nil {
			t.Fatalf("拨号方 %v: %v", c.dialer, err)
		}
		for i := 0; i < 3; i++ { // 同一条连接上连发：握手一次，帧照常复用
			if got := peerRoundTrip(t, cl); got != c.want {
				t.Fatalf("拨号方 %v：服务端看到 %q，期望 %q", c.dialer, got, c.want)
			}
		}
		cl.Close()
	}
}

// on 档拒收明文：连接被关，对端读到 EOF，而不是被当成一个没身份的调用放进来。
func TestPeerTLS_OnRejectsPlaintext(t *testing.T) {
	cert, pub := testPeerCert(t, "kylin")
	setPeerTLSForTest(t, testPeerTLS(t, PeerTLSOn, cert, pub))
	ln := servePeerForTest(t, DisableInproc())

	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	msg := newMessage()
	msg.SetMessageType(MT_REQUEST)
	msg.SetSerializeType(codec.JSON)
	msg.Path = "/test.echo"
	msg.Payload = []byte("x")
	raw.Write(msg.Encode())
	raw.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := raw.Read(make([]byte, 64)); err == nil || n > 0 {
		t.Fatalf("on 档的明文连接得到了 %d 字节回应（err=%v）", n, err)
	}

	c, err := dialForTest(t, ln.Addr().String(), DisableInproc())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := peerRoundTrip(t, c); got != "cert:kylin" {
		t.Fatalf("on 档的 TLS 连接应通，看到 %q", got)
	}
}

// 不在信任里的证书握手就失败 —— 两个方向都是。
func TestPeerTLS_UntrustedPeerFailsHandshake(t *testing.T) {
	srvCert, srvPub := testPeerCert(t, "kylin")
	setPeerTLSForTest(t, testPeerTLS(t, PeerTLSOn, srvCert, srvPub))
	ln := servePeerForTest(t, DisableInproc())

	// 客户端拿一张服务端不认的证书。
	strangerCert, strangerPub := testPeerCert(t, "stranger")
	stranger := testPeerTLS(t, PeerTLSOn, strangerCert, srvPub) // 它认服务端，服务端不认它
	setPeerTLSForTest(t, stranger)
	c, err := dialForTest(t, ln.Addr().String(), DisableInproc())
	if err == nil {
		// TLS 1.3 客户端证书在客户端握手完成之后才被服务端判，拒绝体现在第一次读上。
		defer c.Close()
		msg := newMessage()
		msg.SetMessageType(MT_REQUEST)
		msg.SetSerializeType(codec.JSON)
		msg.Path = "/test.echo"
		msg.Payload = []byte("x")
		if err := c.Send(msg); err == nil {
			if err := c.Recv(newMessage()); err == nil {
				t.Fatal("服务端不认的客户端证书也拿到了回应")
			}
		}
	}

	// 服务端不在客户端的信任里：客户端拒。
	setPeerTLSForTest(t, testPeerTLS(t, PeerTLSOn, strangerCert, strangerPub))
	if c, err := dialForTest(t, ln.Addr().String(), DisableInproc()); err == nil {
		c.Close()
		t.Fatal("客户端连上了一个它不认的服务端")
	}
}

// 进程内直连在 on 档照走：管道两端是同一个进程，服务端经 PeerOf 认出 inproc。
func TestPeerTLS_InprocStillUsedAndRecognised(t *testing.T) {
	cert, pub := testPeerCert(t, "kylin")
	setPeerTLSForTest(t, testPeerTLS(t, PeerTLSOn, cert, pub))
	ln := servePeerForTest(t)
	waitFor(t, func() bool { return inprocFor(ln.Addr().String()) != nil })

	c, err := dialForTest(t, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := peerRoundTrip(t, c); got != "inproc" {
		t.Fatalf("同进程拨号应走管道并被认作本进程，看到 %q", got)
	}
}

// 显式配了 TlsConfig 的传输不归进程级设置管：它照旧按自己的配置拨号。
func TestPeerTLS_ExplicitTransportTLSWins(t *testing.T) {
	cert, pub := testPeerCert(t, "kylin")
	setPeerTLSForTest(t, testPeerTLS(t, PeerTLSOn, cert, pub))
	tr := NewTCPTransport(TLSConfig(&tls.Config{InsecureSkipVerify: true})).(*tcpTransport)
	if tr.peerTLSFor() != nil {
		t.Fatal("显式配了 TlsConfig 的传输被进程级服务间 TLS 接管了")
	}
}

func TestPeerTLS_ModeNeedsConfigs(t *testing.T) {
	defer SetPeerTLS(nil)
	if err := SetPeerTLS(&PeerTLS{Mode: PeerTLSAccept}); err == nil {
		t.Fatal("accept 档没有 Server 配置也装上了")
	}
	if err := SetPeerTLS(&PeerTLS{Mode: PeerTLSDual, Server: &tls.Config{}}); err == nil {
		t.Fatal("dual 档没有 Client 配置也装上了")
	}
	if err := SetPeerTLS(&PeerTLS{Mode: PeerTLSOff}); err != nil || CurrentPeerTLS() != nil {
		t.Fatal("off 档应等于没装")
	}
}

// LoopbackOnly：只绑 127.0.0.1，主机部分写成通配也一样。
func TestTCP_LoopbackOnlyBindsLoopback(t *testing.T) {
	tr := NewTCPTransport(LoopbackOnly())
	ln, err := tr.Listen(":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if a, ok := ln.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() {
		t.Fatalf("LoopbackOnly 的监听绑在了 %v", ln.Addr())
	}

	tr2 := NewTCPTransport()
	ln2, err := tr2.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if a := ln2.Addr().(*net.TCPAddr); !a.IP.IsUnspecified() {
		t.Fatalf("默认监听仍应把环回扩成全接口（normalizeBindAddr），得到 %v", ln2.Addr())
	}
}
