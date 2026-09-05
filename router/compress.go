package router

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// 响应压缩。
//
// # 为什么框架要管这件事
//
// 文本资源（HTML / JSON / JS / CSS / SVG）压缩比普遍在 3~4 倍。此前 volts 的 HTTP
// 出口没有任何压缩，于是每个页面、每次 call_kw 都按原始体积出站；vectors 那边
// connect_components 因为要把上游压好的内容转发出去，只能在自己的控制器里手写一份
// serveBytes 来补 —— 一个模块补一份，别的模块继续裸奔。
//
// # 默认关闭，且必须由使用方明示开启
//
// 压缩不是纯赚：把**攻击者可影响的输入**与**同一响应里的机密**（CSRF token、会话
// 标识）一起压，压缩率本身就是一条侧信道，攻击者可据此逐字节猜出机密 —— 这就是
// BREACH。HTTPS 不能防它，因为泄漏的是密文长度。所以：
//
//   - 默认 **不开**（Compress=false），开启是 `router.WithCompress()` 的显式动作，
//     由部署方在了解自己响应内容的前提下决定；
//   - 真正在意的服务应当同时给敏感响应加长度掩码或对 token 做每请求随机化，
//     那是应用层的事，框架无从代劳。
//
// # 为什么不是中间件
//
// 中间件在 `ctx.Next()` 返回时就结束了，而正文是**之后**由 `ctx.Apply()` 写出去的
// （Result 在 Apply 里才落笔）—— 中间件想在 Next 之后关掉压缩流，关的是一条还没写
// 过一个字节的流，Apply 的正文随后写进已关闭的 writer。压缩流的生命周期必须覆盖
// 「整条响应」，而唯一知道响应何时结束的地方是 TRouter.ServeHTTP。
//
// 关闭时的代价是每请求一次 nil 判断（`self.compressor == nil`），开启时才有真正的
// 协商开销。这与「分流放注册期」的规矩同向：**开不开**在装配期定死，请求期只剩
// 「这一条请求接不接受压缩」这个本来就无法提前知道的问题。

// DefaultCompressMinSize 是默认的压缩下限（字节）。
//
// 比这更小的正文压了通常反而变大（gzip 头尾固定 ~20 字节，且小报文熵高），
// 还白搭一次 CPU 与一次内存拷贝。1KB 是 nginx 等一贯的取值量级。
const DefaultCompressMinSize = 1024

// DefaultCompressTypes 是默认参与压缩的 Content-Type 前缀。
//
// 只列文本族。图片 / 音视频 / zip / gz 本身已是压缩格式，再压一遍是纯亏；
// application/octet-stream 语义未知，不猜。
var DefaultCompressTypes = []string{
	"text/",
	"application/json",
	"application/javascript",
	"application/x-javascript",
	"application/xml",
	"application/xhtml+xml",
	"application/rss+xml",
	"application/atom+xml",
	"application/manifest+json",
	"application/wasm",
	"image/svg+xml",
}

// compressor 是装配期编译好的压缩策略，请求期只读。
type compressor struct {
	minSize int
	// exact 与 prefix 分开：绝大多数判断是精确命中（application/json），
	// 走 map 一次哈希；带 "/" 结尾的（text/）才需要逐个前缀比。
	exact  map[string]struct{}
	prefix []string
}

func newCompressor(minSize int, types []string) *compressor {
	if minSize <= 0 {
		minSize = DefaultCompressMinSize
	}
	if len(types) == 0 {
		types = DefaultCompressTypes
	}
	c := &compressor{minSize: minSize, exact: make(map[string]struct{}, len(types))}
	for _, t := range types {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if strings.HasSuffix(t, "/") || strings.HasSuffix(t, "*") {
			c.prefix = append(c.prefix, strings.TrimSuffix(t, "*"))
			continue
		}
		c.exact[t] = struct{}{}
	}
	return c
}

// accepts 报告该 Content-Type 是否参与压缩。
func (self *compressor) accepts(contentType string) bool {
	// "text/html; charset=utf-8" → "text/html"
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		return false
	}
	if _, ok := self.exact[contentType]; ok {
		return true
	}
	for _, p := range self.prefix {
		if strings.HasPrefix(contentType, p) {
			return true
		}
	}
	return false
}

// wrap 按请求决定是否套上压缩 writer；返回 nil 表示本次请求不压。
func (self *compressor) wrap(w http.ResponseWriter, r *http.Request) *compressWriter {
	if r == nil {
		return nil
	}
	// HEAD 没有正文；升级请求（WebSocket）之后会被劫持，套 writer 只会碍事。
	if r.Method == http.MethodHead {
		return nil
	}
	if strings.EqualFold(r.Header.Get("Connection"), "upgrade") || r.Header.Get("Upgrade") != "" {
		return nil
	}
	enc := negotiateEncoding(r.Header.Get("Accept-Encoding"))
	if enc == "" {
		return nil
	}
	return &compressWriter{ResponseWriter: w, cfg: self, encoding: enc}
}

// negotiateEncoding 从 Accept-Encoding 里挑一种支持的编码，都不支持返回 ""。
//
// 只认 gzip 与 deflate —— 两者都在标准库里。br / zstd 压得更好，但要引入第三方
// 依赖，而这个库会被编进每一个用 volts 的二进制里。
//
// q=0 是**明确拒绝**，必须认：`Accept-Encoding: gzip;q=0, deflate` 的意思是"别给我
// gzip"，忽略 q 值会给出一个客户端刚说过不要的编码，表现为整页乱码。
func negotiateEncoding(accept string) string {
	if accept == "" {
		return ""
	}
	var gzipOK, deflateOK bool
	for _, part := range strings.Split(accept, ",") {
		name, q := part, 1.0
		if i := strings.IndexByte(part, ';'); i >= 0 {
			name = part[:i]
			if v, ok := parseQValue(part[i+1:]); ok {
				q = v
			}
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if q <= 0 {
			continue
		}
		switch name {
		case "gzip":
			gzipOK = true
		case "deflate":
			deflateOK = true
		}
	}
	// gzip 优先：兼容面更广，且 deflate 历史上有 raw/zlib 两种实现分歧。
	if gzipOK {
		return "gzip"
	}
	if deflateOK {
		return "deflate"
	}
	return ""
}

func parseQValue(params string) (float64, bool) {
	for _, p := range strings.Split(params, ";") {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(strings.ToLower(p), "q=") {
			continue
		}
		v := strings.TrimSpace(p[2:])
		switch {
		case v == "0" || strings.HasPrefix(v, "0.0") || v == "0.":
			return 0, true
		case v == "":
			return 1, false
		}
		// 只需要分辨"是不是 0"，不必精确解析权重：本实现不做多编码排序。
		return 1, true
	}
	return 1, false
}

var (
	gzipWriterPool  = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}
	flateWriterPool = sync.Pool{New: func() any {
		w, _ := flate.NewWriter(io.Discard, flate.DefaultCompression)
		return w
	}}
)

// compressWriter 是套在真实 ResponseWriter 外面的一层，按需把正文压缩后写出。
//
// 关键在于**推迟决定**：压不压取决于 Content-Type 与正文大小，而这两者在
// WriteHeader 的那一刻未必都知道（Content-Length 常常缺席）。所以：
//
//	WriteHeader → 只记状态码，判断"有没有资格压"
//	Write       → 有资格就先攒够 minSize 再决定；无资格直接直通
//	Flush/Close → 逼出决定（攒的还不够就直通发原文）
//
// 这也意味着 Content-Length 只有在"决定直通"时才保得住；一旦压缩，它必须删掉。
type compressWriter struct {
	http.ResponseWriter
	cfg      *compressor
	encoding string

	status      int
	wroteHeader bool
	decided     bool // 压不压已经定了
	compressing bool
	buf         []byte
	enc         io.WriteCloser
	closed      bool
}

func (self *compressWriter) WriteHeader(code int) {
	if self.wroteHeader {
		return
	}
	self.wroteHeader = true
	self.status = code

	// Vary 无论压不压都要发：中间缓存必须知道这条响应随 Accept-Encoding 而变，
	// 否则它会把压过的正文喂给一个不接受压缩的客户端（表现为整页二进制乱码）。
	addVaryAcceptEncoding(self.Header())

	if !self.eligible(code) {
		self.decided = true
		self.ResponseWriter.WriteHeader(code)
		return
	}
	// 有资格 —— 状态行**先不发**，等知道正文够不够大再连同 Content-Encoding 一起发。
}

// eligible 判断这条响应是否有资格参与压缩。
func (self *compressWriter) eligible(code int) bool {
	switch code {
	case http.StatusNoContent, http.StatusNotModified, http.StatusPartialContent:
		// 无正文，或正文是字节区间 —— 压了会让 Content-Range 对不上。
		return false
	}
	if code < 200 || code >= 300 {
		// 只压成功响应。错误页通常很小，而 3xx 根本没有正文。
		return false
	}
	h := self.Header()
	if h.Get("Content-Encoding") != "" {
		// 上游（反向代理透传的响应）已经压过了，再压一层等于把 gzip 再 gzip：
		// 体积不降，客户端还解不开。
		return false
	}
	if h.Get("Content-Range") != "" {
		return false
	}
	// Content-Length 已知且小于下限时立刻放弃，省掉一次缓冲。
	if cl := h.Get("Content-Length"); cl != "" {
		n, err := parseContentLength(cl)
		if err == nil && n < int64(self.cfg.minSize) {
			return false
		}
	}
	return self.cfg.accepts(h.Get("Content-Type"))
}

func (self *compressWriter) Write(b []byte) (int, error) {
	if !self.wroteHeader {
		self.WriteHeader(http.StatusOK)
	}
	if self.decided {
		if self.compressing {
			return self.enc.Write(b)
		}
		return self.ResponseWriter.Write(b)
	}

	self.buf = append(self.buf, b...)
	if len(self.buf) >= self.cfg.minSize {
		if err := self.decide(true); err != nil {
			return 0, err
		}
	}
	// 缓冲住的字节对调用方而言就是"已写入"：它只关心有没有全部收下。
	return len(b), nil
}

// decide 落定压不压，并把缓冲的字节吐出去。compress 为 false 时按原文直通。
func (self *compressWriter) decide(compress bool) error {
	if self.decided {
		return nil
	}
	self.decided = true
	self.compressing = compress

	if compress {
		h := self.Header()
		h.Set("Content-Encoding", self.encoding)
		// 压缩后长度必然与原文不同；留着就是一个错的长度，客户端会截断或挂住。
		h.Del("Content-Length")
		// 强校验的 ETag 标识的是**字节序列**，正文变了就不再是同一个实体。
		// 弱化它（W/ 前缀）而不是删掉：删掉会连协商缓存一起废掉。
		weakenETag(h)
		self.enc = newEncoder(self.encoding, self.ResponseWriter)
	}

	self.ResponseWriter.WriteHeader(self.status)

	if len(self.buf) == 0 {
		return nil
	}
	var err error
	if compress {
		_, err = self.enc.Write(self.buf)
	} else {
		_, err = self.ResponseWriter.Write(self.buf)
	}
	self.buf = nil
	return err
}

// Flush 逼出决定并把已写入的字节推到连接上。
//
// 流式响应（SSE、进度推送）的第一帧通常只有几十字节，够不到 minSize —— 那就按
// 直通发出去。这正是想要的：流式响应求的是**尽快到达**，不是省几十字节，而攒着
// 不发会让客户端一直看不到第一帧。
func (self *compressWriter) Flush() {
	if !self.wroteHeader {
		self.WriteHeader(http.StatusOK)
	}
	if !self.decided {
		_ = self.decide(false)
	}
	if self.compressing {
		if f, ok := self.enc.(interface{ Flush() error }); ok {
			_ = f.Flush()
		}
	}
	if f, ok := self.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 透传给底层。WebSocket 升级要接管连接，压缩层不能挡在中间；
// 走到这里时本响应一个字节都还没写过（Upgrade 已在 wrap 处排除，这里是兜底）。
func (self *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := self.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("router: the ResponseWriter doesn't support the Hijacker interface")
}

// Unwrap 让 http.NewResponseController 能穿过本层拿到真正的 writer
// （SetWriteDeadline 之类的能力否则会丢失 —— vectors 的 SSE 就靠它关写超时）。
func (self *compressWriter) Unwrap() http.ResponseWriter { return self.ResponseWriter }

// Close 收尾：还没决定的按直通发出，压缩流写出尾块并归还池。幂等。
//
// **一个字节都没写过、也没人定过状态码时什么都不做** —— 否则会凭空造出一条空的
// gzip 流（20 字节），把"没有正文"变成"有一段谁也不需要的正文"。
func (self *compressWriter) Close() {
	if self.closed {
		return
	}
	self.closed = true
	if !self.wroteHeader {
		return
	}
	if !self.decided {
		_ = self.decide(false)
	}
	if self.compressing && self.enc != nil {
		_ = self.enc.Close()
		releaseEncoder(self.encoding, self.enc)
		self.enc = nil
	}
}

func newEncoder(encoding string, w io.Writer) io.WriteCloser {
	switch encoding {
	case "deflate":
		fw := flateWriterPool.Get().(*flate.Writer)
		fw.Reset(w)
		return fw
	default:
		gw := gzipWriterPool.Get().(*gzip.Writer)
		gw.Reset(w)
		return gw
	}
}

func releaseEncoder(encoding string, enc io.WriteCloser) {
	switch encoding {
	case "deflate":
		if fw, ok := enc.(*flate.Writer); ok {
			// 归还前解除对本次 ResponseWriter 的引用，否则池会把一条已结束的
			// 连接一直拽着不放（下一次 Reset 之前都不会释放）。
			fw.Reset(io.Discard)
			flateWriterPool.Put(fw)
		}
	default:
		if gw, ok := enc.(*gzip.Writer); ok {
			gw.Reset(io.Discard)
			gzipWriterPool.Put(gw)
		}
	}
}

func addVaryAcceptEncoding(h http.Header) {
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

// weakenETag 把强 ETag 转成弱 ETag。压缩改变了字节序列，强 ETag 不再成立，
// 而弱 ETag 表达的正是"语义相同、字节未必相同"。
func weakenETag(h http.Header) {
	et := h.Get("ETag")
	if et == "" || strings.HasPrefix(et, "W/") {
		return
	}
	h.Set("ETag", "W/"+et)
}

func parseContentLength(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, errors.New("empty")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}
