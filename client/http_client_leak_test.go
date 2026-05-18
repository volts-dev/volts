package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type closeTrackingRT struct {
	base       http.RoundTripper
	closeCount int64
	doCount    int64
}

type trackingBody struct {
	rc       interface{ Read([]byte) (int, error) }
	closer   func() error
	closeCnt *int64
	closed   bool
}

func (b *trackingBody) Read(p []byte) (int, error) { return b.rc.Read(p) }
func (b *trackingBody) Close() error {
	if !b.closed {
		b.closed = true
		atomic.AddInt64(b.closeCnt, 1)
	}
	return b.closer()
}

func (rt *closeTrackingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(&rt.doCount, 1)
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	original := resp.Body
	resp.Body = &trackingBody{rc: original, closer: original.Close, closeCnt: &rt.closeCount}
	return resp, nil
}

func newTestHTTPClient(t *testing.T, srv *httptest.Server, rt *closeTrackingRT) *HttpClient {
	t.Helper()
	c, err := NewHttpClient()
	if err != nil {
		t.Fatalf("NewHttpClient: %v", err)
	}
	rt.base = c.client.Transport
	c.client.Transport = rt
	return c
}

func TestHTTPClient_ResponseBodyClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "hello")
	}))
	defer srv.Close()

	rt := &closeTrackingRT{}
	c := newTestHTTPClient(t, srv, rt)

	req, err := c.NewRequest("get", srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := c.Call(req); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if got := atomic.LoadInt64(&rt.closeCount); got != 1 {
		t.Fatalf("expected response Body.Close to be called exactly once, got %d (Do called %d times)",
			got, atomic.LoadInt64(&rt.doCount))
	}
}

func TestHTTPClient_RetryDoesNotRaceResponse(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	}))
	defer srv.Close()

	rt := &closeTrackingRT{}
	c := newTestHTTPClient(t, srv, rt)

	req, err := c.NewRequest("get", srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Call(req, WithContext(ctx))
	_ = err

	do := atomic.LoadInt64(&rt.doCount)
	cl := atomic.LoadInt64(&rt.closeCount)
	if do == 0 {
		t.Fatalf("expected at least one HTTP request, got 0")
	}
	if cl != do {
		t.Fatalf("expected Body.Close calls (%d) == Do calls (%d) — leak detected", cl, do)
	}
}
