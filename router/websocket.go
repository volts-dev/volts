package router

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/volts-dev/volts/transport"
)

// RFC 6455 §1.3 规定的握手魔术串。
const wsAcceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var (
	ErrWsNotUpgrade       = errors.New("websocket: not a websocket upgrade request")
	ErrWsBadVersion       = errors.New("websocket: unsupported version, only 13 is supported")
	ErrWsBadKey           = errors.New("websocket: invalid Sec-WebSocket-Key")
	ErrWsOriginNotAllowed = errors.New("websocket: origin not allowed")
	ErrWsNotHijackable    = errors.New("websocket: ResponseWriter does not support hijacking")
	ErrWsAlreadyWritten   = errors.New("websocket: response already written, cannot upgrade")
)

// TWebsocketUpgrader 把一个 HTTP 请求升级为 WebSocket 连接。
type TWebsocketUpgrader struct {
	// HandshakeTimeout 限制握手写出 101 响应的耗时，零值表示不限制。
	HandshakeTimeout time.Duration

	// ReadLimit 单条消息字节上限，零值使用 transport 层默认值（32MB）。
	ReadLimit int64

	// Subprotocols 是服务端支持的子协议，按顺序与客户端请求求交集取第一个命中项。
	Subprotocols []string

	// CheckOrigin 校验跨站请求。**nil 表示同源策略**，不是放行。
	// 浏览器不对 WebSocket 施加同源限制，也不发预检——握手就是一个普通 GET，
	// 任何站点都能对着你的地址发起。放行所有 Origin 等于把这个端点上的
	// 全部能力交给任意第三方页面（CSWSH）。要跨域必须显式写出白名单。
	CheckOrigin func(r *transport.THttpRequest) bool

	// Error 自定义握手失败时的响应，nil 时返回纯文本错误。
	Error func(ctx *THttpContext, status int, reason error)
}

func (self *TWebsocketUpgrader) String() string {
	return "WebsocketUpgrader"
}

// checkSameOrigin 是 CheckOrigin 为 nil 时的默认实现：无 Origin 头放行
// （非浏览器客户端不发这个头），有则要求与 Host 同源。
func checkSameOrigin(r *transport.THttpRequest) bool {
	origin := r.Header().Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// headerContainsToken 按逗号切分做 token 级比较。
// 不能用 strings.Contains：`Connection: keep-alive` 里并没有 upgrade，
// 而 `Upgrade: websocketx` 也不该被当成 websocket。
func headerContainsToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// selectSubprotocol 求客户端请求与服务端支持的交集，取服务端顺序里的第一个。
func (self *TWebsocketUpgrader) selectSubprotocol(r *transport.THttpRequest) string {
	if len(self.Subprotocols) == 0 {
		return ""
	}
	var requested []string
	for _, v := range r.Header().Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(v, ",") {
			requested = append(requested, strings.TrimSpace(part))
		}
	}
	for _, server := range self.Subprotocols {
		for _, want := range requested {
			if strings.EqualFold(server, want) {
				return server
			}
		}
	}
	return ""
}

// wsAcceptKey 计算 Sec-WebSocket-Accept。
func wsAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(wsAcceptGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (self *TWebsocketUpgrader) fail(ctx *THttpContext, status int, err error) error {
	if self.Error != nil {
		self.Error(ctx, status, err)
	} else {
		ctx.SetHeader(true, "Sec-WebSocket-Version", "13")
		http.Error(ctx.Response(), err.Error(), status)
		ctx.isDone = true
	}
	return err
}

// Upgrade 执行握手并接管连接。成功后 ctx 会被标记为已劫持，router 不再对它做
// 任何写入或池回收，连接的生命周期完全交给调用方——用完必须 Close。
func (self *TWebsocketUpgrader) Upgrade(ctx *THttpContext) (*transport.TWebsocketConn, error) {
	r := ctx.Request()

	if !strings.EqualFold(r.Method, "GET") {
		return nil, self.fail(ctx, http.StatusMethodNotAllowed,
			fmt.Errorf("%w: method %s, must be GET", ErrWsNotUpgrade, r.Method))
	}
	if !headerContainsToken(r.Header(), "Connection", "upgrade") {
		return nil, self.fail(ctx, http.StatusBadRequest,
			fmt.Errorf("%w: missing 'Connection: Upgrade'", ErrWsNotUpgrade))
	}
	if !headerContainsToken(r.Header(), "Upgrade", "websocket") {
		return nil, self.fail(ctx, http.StatusBadRequest,
			fmt.Errorf("%w: missing 'Upgrade: websocket'", ErrWsNotUpgrade))
	}
	if r.Header().Get("Sec-WebSocket-Version") != "13" {
		return nil, self.fail(ctx, http.StatusUpgradeRequired, ErrWsBadVersion)
	}

	key := r.Header().Get("Sec-WebSocket-Key")
	// 必须是 16 字节的 base64，长度不对就不是合法握手。
	if decoded, err := base64.StdEncoding.DecodeString(key); err != nil || len(decoded) != 16 {
		return nil, self.fail(ctx, http.StatusBadRequest, ErrWsBadKey)
	}

	check := self.CheckOrigin
	if check == nil {
		check = checkSameOrigin
	}
	if !check(r) {
		return nil, self.fail(ctx, http.StatusForbidden, ErrWsOriginNotAllowed)
	}

	rsp := ctx.Response()
	if rsp.Written() {
		// 已经写过响应就不可能再发 101，此时劫持只会产出一条协议混乱的连接。
		return nil, ErrWsAlreadyWritten
	}

	subproto := self.selectSubprotocol(r)

	conn, brw, err := rsp.Hijack()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWsNotHijackable, err.Error())
	}

	// 从这里开始连接归我们管，任何失败路径都必须自己 Close，
	// http.Server 已经不会再碰它了。
	if self.HandshakeTimeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(self.HandshakeTimeout))
	}

	var b strings.Builder
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Accept: ")
	b.WriteString(wsAcceptKey(key))
	b.WriteString("\r\n")
	if subproto != "" {
		b.WriteString("Sec-WebSocket-Protocol: ")
		b.WriteString(subproto)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")

	if _, err := conn.Write([]byte(b.String())); err != nil {
		conn.Close()
		return nil, err
	}
	if self.HandshakeTimeout > 0 {
		conn.SetWriteDeadline(time.Time{})
	}

	// 标记劫持：router 据此跳过 Apply() 与池回收。必须在握手成功后才置位，
	// 失败路径仍要让 ctx 正常走完 404/400 的响应流程。
	ctx.hijacked = true

	ws := transport.NewWebsocketConn(conn, wsReader(brw), true, subproto)
	if self.ReadLimit > 0 {
		ws.SetReadLimit(self.ReadLimit)
	}
	return ws, nil
}

// wsReader 取出握手时缓冲区里的读端。客户端常在 101 还没读到就把第一帧发出来，
// 那些字节此刻已经躺在 bufio 里；丢掉这个 reader 就会丢掉它们。
func wsReader(brw *bufio.ReadWriter) *bufio.Reader {
	if brw == nil {
		return nil
	}
	return brw.Reader
}

// Upgrade 是 THttpContext 上的便捷入口，用默认 Upgrader（同源校验）完成升级。
// 需要自定义 Origin 白名单或子协议时，直接构造 TWebsocketUpgrader 调用其 Upgrade。
func (self *THttpContext) Upgrade() (*transport.TWebsocketConn, error) {
	up := &TWebsocketUpgrader{}
	return up.Upgrade(self)
}

// IsWebsocket 判断当前请求是否为 WebSocket 升级握手。
func (self *THttpContext) IsWebsocket() bool {
	r := self.Request()
	return headerContainsToken(r.Header(), "Connection", "upgrade") &&
		headerContainsToken(r.Header(), "Upgrade", "websocket")
}
