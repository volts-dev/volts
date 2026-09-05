package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordingWriter 记下每一次真正落到连接上的 WriteHeader，用来分辨"记下了"与"发出去了"。
type recordingWriter struct {
	header http.Header
	codes  []int
	body   []byte
}

func (w *recordingWriter) Header() http.Header  { return w.header }
func (w *recordingWriter) WriteHeader(code int) { w.codes = append(w.codes, code) }
func (w *recordingWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}

func newLazyRsp(t *testing.T) (*THttpResponse, *recordingWriter) {
	t.Helper()
	req := NewHttpRequest(httptest.NewRequest("GET", "/", nil))
	rsp := NewHttpResponse(req.Context(), req)
	w := &recordingWriter{header: http.Header{}}
	rsp.Connect(w)
	return rsp, w
}

// TestHttpResponse_WriteHeaderIsDeferredUntilBody 锁定：WriteHeader 只记状态码，头在
// 第一次 Write 时才发出 —— 于是 WriteHeader 之后再设的头照样出得去。
func TestHttpResponse_WriteHeaderIsDeferredUntilBody(t *testing.T) {
	rsp, w := newLazyRsp(t)

	rsp.WriteHeader(422)
	if len(w.codes) != 0 {
		t.Fatalf("WriteHeader 不该立刻发出状态行，实际发了 %v", w.codes)
	}
	if !rsp.Written() || rsp.Committed() || rsp.Status() != 422 {
		t.Fatalf("Written=%v Committed=%v Status=%d，期望 记过/未发出/422", rsp.Written(), rsp.Committed(), rsp.Status())
	}

	rsp.Header().Set("Content-Type", "application/json")
	if _, err := rsp.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := w.codes; len(got) != 1 || got[0] != 422 {
		t.Fatalf("状态行应在第一次 Write 时以 422 发出一次，实际 %v", got)
	}
	if string(w.body) != `{}` || w.header.Get("Content-Type") != "application/json" {
		t.Fatalf("正文/头丢了: body=%q ct=%q", w.body, w.header.Get("Content-Type"))
	}
	if !rsp.Committed() || rsp.Size() != 2 {
		t.Fatalf("Committed=%v Size=%d", rsp.Committed(), rsp.Size())
	}
}

// TestHttpResponse_CommitSendsHeaderOnlyResponse 锁定：只调了 WriteHeader 没写正文的
// 响应（304/404/302）靠 Commit 发出，且 Commit 幂等。
func TestHttpResponse_CommitSendsHeaderOnlyResponse(t *testing.T) {
	rsp, w := newLazyRsp(t)

	rsp.WriteHeader(304)
	rsp.Commit()
	rsp.Commit()
	if got := w.codes; len(got) != 1 || got[0] != 304 {
		t.Fatalf("期望恰好发出一次 304，实际 %v", got)
	}
	if len(w.body) != 0 {
		t.Fatalf("不该有正文: %q", w.body)
	}
}

// TestHttpResponse_CommitWithoutStatusDoesNothing 锁定：没人决定过状态码时 Commit 不动 ——
// 留给 net/http 的隐式 200，而不是抢先发一个空响应。
func TestHttpResponse_CommitWithoutStatusDoesNothing(t *testing.T) {
	rsp, w := newLazyRsp(t)
	rsp.Commit()
	if len(w.codes) != 0 || rsp.Committed() {
		t.Fatalf("没记过状态码不该发出任何东西: %v", w.codes)
	}
}

// TestHttpResponse_WriteHeaderAfterCommitIsIgnored 锁定：头发出后再改状态码无效，
// 且不会再次透传给底层（否则 net/http 记 superfluous WriteHeader）。
func TestHttpResponse_WriteHeaderAfterCommitIsIgnored(t *testing.T) {
	rsp, w := newLazyRsp(t)
	rsp.Write([]byte("a"))
	rsp.WriteHeader(500)
	rsp.Commit()
	if got := w.codes; len(got) != 1 || got[0] != 200 {
		t.Fatalf("期望只有隐式 200，实际 %v", got)
	}
	if rsp.Status() != 200 {
		t.Fatalf("Status 应保持 200，实际 %d", rsp.Status())
	}
}

// TestHttpResponse_InformationalPassesThrough 锁定：1xx 是可多次发送的中间响应，必须
// 立刻透传，且不算"决定过这条响应"。
func TestHttpResponse_InformationalPassesThrough(t *testing.T) {
	rsp, w := newLazyRsp(t)
	rsp.WriteHeader(http.StatusEarlyHints)
	rsp.WriteHeader(http.StatusEarlyHints)
	if got := w.codes; len(got) != 2 || got[0] != 103 || got[1] != 103 {
		t.Fatalf("103 应逐次透传，实际 %v", got)
	}
	if rsp.Written() {
		t.Fatal("1xx 不该把响应标成已决定")
	}
	rsp.Write([]byte("x"))
	if got := w.codes; got[len(got)-1] != 200 {
		t.Fatalf("最终响应应为 200，实际 %v", got)
	}
}

// TestHttpResponse_FlushCommitsHeader 锁定：Flush 的语义是"头也一并发出"（SSE/流式）。
func TestHttpResponse_FlushCommitsHeader(t *testing.T) {
	req := NewHttpRequest(httptest.NewRequest("GET", "/", nil))
	rsp := NewHttpResponse(req.Context(), req)
	rec := httptest.NewRecorder()
	rsp.Connect(rec)
	rsp.WriteHeader(202)
	rsp.Flush()
	if rec.Code != 202 || !rec.Flushed {
		t.Fatalf("Flush 后应已发出 202 并刷新，实际 code=%d flushed=%v", rec.Code, rec.Flushed)
	}
}

// TestHttpResponse_ConnectResetsCommitState 锁定：池化复用时 Connect 必须清掉上一轮的
// 提交状态，否则复用的对象上第一次 WriteHeader 会被当成"已发出"而丢掉。
func TestHttpResponse_ConnectResetsCommitState(t *testing.T) {
	rsp, _ := newLazyRsp(t)
	rsp.Write([]byte("first"))

	w2 := &recordingWriter{header: http.Header{}}
	rsp.Connect(w2)
	if rsp.Written() || rsp.Committed() || rsp.Size() != 0 {
		t.Fatalf("Connect 后应回到初始态: written=%v committed=%v size=%d", rsp.Written(), rsp.Committed(), rsp.Size())
	}
	rsp.WriteHeader(201)
	rsp.Commit()
	if got := w2.codes; len(got) != 1 || got[0] != 201 {
		t.Fatalf("复用后的对象应能正常发出 201，实际 %v", got)
	}
}
