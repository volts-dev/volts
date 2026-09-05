package router

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// bigJSON 是一段稳定超过压缩下限、且压缩率很高的正文。
func bigJSON() string {
	return `{"rows":[` + strings.TrimSuffix(strings.Repeat(`{"name":"volts","ok":true},`, 200), ",") + `]}`
}

// liveRouter 把 router 挂在真的 net/http 服务器后面，并返回发请求的闭包。
//
// 必须真服务器：压缩要经过 net/http 的头冻结、Content-Length 处理与连接写出，
// httptest.ResponseRecorder 三者都没有。
func liveRouter(t *testing.T, opts []Option, register func(*TGroup)) func(method, path string, hdr map[string]string) *http.Response {
	t.Helper()
	r := New(opts...)
	t.Cleanup(func() { close(r.exit) })
	grp := NewGroup()
	register(grp)
	r.RegisterGroup(grp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.ServeHTTP(w, transport.NewHttpRequest(req))
	}))
	t.Cleanup(srv.Close)

	return func(method, path string, hdr map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		// 关掉 transport 的自动 gzip：它会替我们解压并抹掉 Content-Encoding，
		// 于是"到底压没压"变得完全看不见。DisableCompression 只影响自动加的那条
		// Accept-Encoding，我们自己设的照发。
		cli := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		rsp, err := cli.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rsp.Body.Close() })
		return rsp
	}
}

func jsonRoute(payload string) func(*TGroup) {
	return func(grp *TGroup) {
		grp.Url([]string{"GET"}, "/data", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Respond([]byte(payload))
		})
	}
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCompress_GzipRoundTrip 锁定主路径：够大的 JSON 被 gzip 压出去，解开后与原文
// 逐字节相同，且 Content-Length 被删掉、Vary 带上 Accept-Encoding。
func TestCompress_GzipRoundTrip(t *testing.T) {
	payload := bigJSON()
	do := liveRouter(t, []Option{WithCompress()}, jsonRoute(payload))
	rsp := do("GET", "/data", map[string]string{"Accept-Encoding": "gzip"})

	if got := rsp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", got)
	}
	if v := rsp.Header.Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Errorf("Vary=%q，缺 Accept-Encoding：缓存会把压过的正文喂给不接受压缩的客户端", v)
	}

	raw := mustReadAll(t, rsp.Body)
	if len(raw) >= len(payload) {
		t.Errorf("压完 %d 字节 >= 原文 %d 字节，没起作用", len(raw), len(payload))
	}
	// 我们把原文的 Content-Length 删掉了；net/http 随后按**实际写出的字节**补一条
	// 准确的。留着原文长度的话客户端会一直等一段永远不来的正文（或把正文截断），
	// 所以判据是"若有长度，必须等于压缩后的字节数"，而不是"必须没有长度"。
	if cl := rsp.ContentLength; cl >= 0 && cl != int64(len(raw)) {
		t.Errorf("Content-Length=%d 与实际正文 %d 字节不符（原文是 %d 字节）", cl, len(raw), len(payload))
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("不是合法的 gzip 流: %v", err)
	}
	if got := string(mustReadAll(t, zr)); got != payload {
		t.Errorf("解开后与原文不符：%d vs %d 字节", len(got), len(payload))
	}
}

// TestCompress_DeflateWhenGzipRefused 锁定 q=0 被当真：客户端说"别给我 gzip"时
// 必须换 deflate，而不是照发一个它刚拒绝过的编码。
func TestCompress_DeflateWhenGzipRefused(t *testing.T) {
	payload := bigJSON()
	do := liveRouter(t, []Option{WithCompress()}, jsonRoute(payload))
	rsp := do("GET", "/data", map[string]string{"Accept-Encoding": "gzip;q=0, deflate"})

	if got := rsp.Header.Get("Content-Encoding"); got != "deflate" {
		t.Fatalf("Content-Encoding=%q, want deflate（gzip 被 q=0 拒绝）", got)
	}
	got := mustReadAll(t, flate.NewReader(bytes.NewReader(mustReadAll(t, rsp.Body))))
	if string(got) != payload {
		t.Errorf("deflate 解开后与原文不符")
	}
}

// TestCompress_SkipCases 锁定所有"不该压"的情形。每一条都对应一种真实故障：
// 不接受压缩却压了 = 整页乱码；已压过再压 = 客户端解不开；太小压了 = 变大还费 CPU。
func TestCompress_SkipCases(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		hd     func(*THttpContext)
		reason string
	}{
		{"客户端不接受", "", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Respond([]byte(bigJSON()))
		}, "没有 Accept-Encoding"},
		{"正文太小", "gzip", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Respond([]byte(`{"ok":true}`))
		}, "小于 minSize"},
		{"类型不在表内", "gzip", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "image/png")
			ctx.Respond(bytes.Repeat([]byte("x"), 8192))
		}, "image/png 本身已压缩"},
		{"上游已压过", "gzip", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Response().Header().Set("Content-Encoding", "gzip")
			ctx.Respond(bytes.Repeat([]byte("x"), 8192))
		}, "反向代理透传的响应"},
		{"错误响应", "gzip", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.WriteHeader(http.StatusInternalServerError)
			ctx.Respond([]byte(bigJSON()))
		}, "只压 2xx"},
		{"部分内容", "gzip", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "text/plain")
			ctx.Response().Header().Set("Content-Range", "bytes 0-99/9999")
			ctx.WriteHeader(http.StatusPartialContent)
			ctx.Respond(bytes.Repeat([]byte("x"), 8192))
		}, "压了 Content-Range 就对不上"},
		{"已知长度且很小", "gzip", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Response().Header().Set("Content-Length", "11")
			ctx.Respond([]byte(`{"ok":true}`))
		}, "Content-Length < minSize"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hd := c.hd
			do := liveRouter(t, []Option{WithCompress()}, func(grp *TGroup) {
				grp.Url([]string{"GET"}, "/data", hd)
			})
			h := map[string]string{}
			if c.accept != "" {
				h["Accept-Encoding"] = c.accept
			}
			rsp := do("GET", "/data", h)
			if got := rsp.Header.Get("Content-Encoding"); got == "gzip" && c.name != "上游已压过" {
				t.Fatalf("不该压却压了（%s）", c.reason)
			}
			if c.name == "上游已压过" {
				// 头保持上游那一条，且正文没有被再压一层。
				body := mustReadAll(t, rsp.Body)
				if len(body) != 8192 {
					t.Fatalf("正文被改写了：%d 字节，want 8192（不该在上游的 gzip 外面再压一层）", len(body))
				}
			}
		})
	}
}

// TestCompress_DisabledByDefault 锁定默认关闭：不写 WithCompress 就一个字节都不压。
// 这是安全默认（BREACH），也是"不改变既有部署行为"的前提。
func TestCompress_DisabledByDefault(t *testing.T) {
	payload := bigJSON()
	do := liveRouter(t, nil, jsonRoute(payload))
	rsp := do("GET", "/data", map[string]string{"Accept-Encoding": "gzip"})

	if got := rsp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("默认不该压，实际 Content-Encoding=%q", got)
	}
	if got := string(mustReadAll(t, rsp.Body)); got != payload {
		t.Fatalf("正文被动过")
	}
}

// TestCompress_HeadHasNoBody 锁定 HEAD 不套压缩层：它没有正文，套上只会平白多出
// 一个 Content-Encoding 头，让客户端以为有一段压缩正文。
func TestCompress_HeadHasNoBody(t *testing.T) {
	do := liveRouter(t, []Option{WithCompress()}, func(grp *TGroup) {
		grp.Url([]string{"GET", "HEAD"}, "/data", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Respond([]byte(bigJSON()))
		})
	})
	rsp := do("HEAD", "/data", map[string]string{"Accept-Encoding": "gzip"})
	if got := rsp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("HEAD 不该带 Content-Encoding，实际 %q", got)
	}
}

// TestCompress_StreamingFlushIsNotStalled 锁定流式响应不被缓冲卡住：SSE 的第一帧
// 只有几十字节，够不到 minSize —— Flush 必须逼出"直通"的决定并立刻发出去，而不是
// 攒着等一个永远到不了的下限（那表现为"进度条一直不动"）。
func TestCompress_StreamingFlushIsNotStalled(t *testing.T) {
	released := make(chan struct{})
	do := liveRouter(t, []Option{WithCompress()}, func(grp *TGroup) {
		grp.Url([]string{"GET"}, "/data", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "text/event-stream")
			ctx.Response().Write([]byte(": connected\n\n"))
			ctx.Response().Flush()
			<-released
			ctx.Response().Write([]byte("data: done\n\n"))
			ctx.Response().Flush()
		})
	})

	rsp := do("GET", "/data", map[string]string{"Accept-Encoding": "gzip"})
	if got := rsp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("小于下限的流式首帧不该被压：Content-Encoding=%q", got)
	}
	buf := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(rsp.Body, buf); err != nil {
		t.Fatalf("首帧没能在 handler 返回前到达（被缓冲卡住了）: %v", err)
	}
	if string(buf) != ": connected\n\n" {
		t.Fatalf("首帧内容不对: %q", buf)
	}
	close(released)
}

// TestCompress_ETagWeakened 锁定：正文被压过之后强 ETag 必须弱化 —— 强 ETag 标识的
// 是字节序列，而压缩恰好改了字节序列；留着强 ETag 会让缓存把两份不同的字节当成同一个。
func TestCompress_ETagWeakened(t *testing.T) {
	do := liveRouter(t, []Option{WithCompress()}, func(grp *TGroup) {
		grp.Url([]string{"GET"}, "/data", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/json")
			ctx.Response().Header().Set("ETag", `"abc123"`)
			ctx.Respond([]byte(bigJSON()))
		})
	})
	rsp := do("GET", "/data", map[string]string{"Accept-Encoding": "gzip"})
	if got := rsp.Header.Get("ETag"); got != `W/"abc123"` {
		t.Fatalf("ETag=%q, want W/\"abc123\"", got)
	}
}

// TestCompress_NegotiateEncoding 单测协商本身，含几个容易写错的形态。
func TestCompress_NegotiateEncoding(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"identity", ""},
		{"gzip", "gzip"},
		{"GZIP", "gzip"},
		{"deflate", "deflate"},
		{"gzip, deflate, br", "gzip"},
		{"br, zstd", ""},
		{"gzip;q=0", ""},
		{"gzip;q=0, deflate", "deflate"},
		{"gzip;q=0.0,deflate;q=0", ""},
		{" gzip ; q=0.5 , deflate ", "gzip"},
	}
	for _, c := range cases {
		if got := negotiateEncoding(c.in); got != c.want {
			t.Errorf("negotiateEncoding(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

// TestCompress_ExplicitContentLengthIsCorrected 锁定：handler 自己声明了
// Content-Length 时，压缩必须把它去掉（由 net/http 按实际写出的字节重新计算）。
//
// 留着原文长度的后果是客户端按那个数等正文：少了的那部分永远不来，表现为请求挂住
// 或正文被截断 —— 而这两种都不像"压缩配错了"。上一条用例里 handler 不设长度，
// net/http 自己算的那条恰好是对的，所以抓不到这个 bug；必须由 handler 显式声明。
func TestCompress_ExplicitContentLengthIsCorrected(t *testing.T) {
	payload := bigJSON()
	do := liveRouter(t, []Option{WithCompress()}, func(grp *TGroup) {
		grp.Url([]string{"GET"}, "/data", func(ctx *THttpContext) {
			h := ctx.Response().Header()
			h.Set("Content-Type", "application/json")
			h.Set("Content-Length", strconv.Itoa(len(payload)))
			ctx.Respond([]byte(payload))
		})
	})
	rsp := do("GET", "/data", map[string]string{"Accept-Encoding": "gzip"})

	if got := rsp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", got)
	}
	if cl := rsp.Header.Get("Content-Length"); cl == strconv.Itoa(len(payload)) {
		t.Fatalf("Content-Length 还是原文的 %s，客户端会一直等一段永远不来的正文", cl)
	}
	raw := mustReadAll(t, rsp.Body)
	if cl := rsp.ContentLength; cl >= 0 && cl != int64(len(raw)) {
		t.Fatalf("Content-Length=%d 与实际正文 %d 字节不符", cl, len(raw))
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("不是合法 gzip: %v", err)
	}
	if string(mustReadAll(t, zr)) != payload {
		t.Fatal("解开后与原文不符")
	}
}
