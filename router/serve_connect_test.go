package router

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// errHijacker 是实现了 http.Hijacker 但 Hijack() 必定失败的 ResponseWriter。
type errHijacker struct {
	*httptest.ResponseRecorder
}

func (e *errHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack failed for test")
}

// CONNECT 请求 Hijack 失败时不得 nil deref panic。
//
// 此前：Hijack 返回 err 只记日志不 return，紧接着 io.WriteString(conn,...) 里
// conn 为 nil → 直接 panic，拖垮当前服务 goroutine。
func TestServeHTTP_ConnectHijackErrorNoPanic(t *testing.T) {
	r := New()
	defer r.Close()

	req := httptest.NewRequest("CONNECT", "/whatever.rpc", nil)
	w := &errHijacker{httptest.NewRecorder()}

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Hijack 失败路径发生 panic（nil conn 未防护）: %v", p)
		}
	}()

	// 不 panic、正常返回即通过。
	r.ServeHTTP(w, transport.NewHttpRequest(req))
}

// 非 Hijacker 的 ResponseWriter 走 CONNECT 时应干净返回（已有防护，作回归基线）。
func TestServeHTTP_ConnectNonHijackerReturns(t *testing.T) {
	r := New()
	defer r.Close()

	req := httptest.NewRequest("CONNECT", "/whatever.rpc", nil)
	w := httptest.NewRecorder() // 不实现 http.Hijacker

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("非 Hijacker 的 CONNECT 不应 panic: %v", p)
		}
	}()

	r.ServeHTTP(w, transport.NewHttpRequest(req))
}

// 404 仍返回 404（recycle rsp 的改动不得影响响应正确性）。
func TestServeHTTP_NotFoundStillReturns404(t *testing.T) {
	r := New()
	defer r.Close()

	req := httptest.NewRequest("GET", "/no-such-route", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, transport.NewHttpRequest(req))

	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d", w.Code)
	}
}
