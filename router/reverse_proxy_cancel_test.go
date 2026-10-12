package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// 浏览器跳页取消的请求不是上游故障：不许记 ERROR，更不许上报熔断（连着三次就把健康节点
// 踢出 10s）。只有「入站 context 已结束 + 错误是 canceled」才算；调用方还在等而上游报了
// canceled，仍是故障。
func TestCanceledByClient(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	req := func(ctx context.Context) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "GET", "http://upstream/", nil)
		return r
	}
	wrapped := fmt.Errorf("proxy: %w", context.Canceled)

	cases := []struct {
		name string
		r    *http.Request
		err  error
		want bool
	}{
		{"调用方走了", req(gone), context.Canceled, true},
		{"调用方走了（包了一层）", req(gone), wrapped, true},
		{"调用方还在，上游报 canceled", req(live), context.Canceled, false},
		{"调用方走了但错误是连不上", req(gone), errors.New("dial tcp: connection refused"), false},
		{"普通故障", req(live), errors.New("EOF"), false},
	}
	for _, c := range cases {
		if got := canceledByClient(c.r, c.err); got != c.want {
			t.Errorf("%s: canceledByClient = %v, want %v", c.name, got, c.want)
		}
	}
}
