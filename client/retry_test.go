package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/volts-dev/volts/errors"
)

// TestRetryOnError_ReadsCodeThroughWrapping 锁定这条曾经完全失效的判定。
//
// 旧实现是 `errors.Parse(err.Error())`：Error() 只返回 Detail，Parse 对一句普通
// 错误文案做 JSON 解码必然失败，于是 Code 恒为 0，switch 一档都不匹配 ——
// **永远返回 false**。调用方把 Retries 调大也不会有第二次尝试，且没有任何提示。
func TestRetryOnError_ReadsCodeThroughWrapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"500 裸的", errors.New("svc", 500, "boom"), true},
		{"408 裸的", errors.New("svc", 408, "timeout"), true},
		{"404 不重试", errors.New("svc", 404, "nope"), false},
		{"500 被 fmt.Errorf 包过", fmt.Errorf("call failed: %w", errors.New("svc", 500, "boom")), true},
		{"500 被 errors.Wrap 包过", errors.Wrap(errors.New("svc", 500, "boom"), "outer", 500, ""), true},
		{"不带码的普通错误", fmt.Errorf("plain failure"), false},
	}
	for _, c := range cases {
		got, err := RetryOnError(context.Background(), nil, 0, c.err)
		if err != nil {
			t.Fatalf("%s: 意外错误 %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: RetryOnError=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestRetryOnConnectError_OnlyWhenNothingWasSent 锁定安全边界：只有"连接没建成"
// 才重试。发出去之后失败的一律不重试 —— 那会让一次下单/扣款执行两次。
func TestRetryOnConnectError_OnlyWhenNothingWasSent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"连接没建成", ErrConnectFailed, true},
		{"连接没建成（带节点地址包装）", errors.Wrap(ErrConnectFailed, "volts.client", 500, "dial 1.2.3.4:1: refused"), true},
		{"服务端回了 500", errors.New("svc", 500, "boom"), false},
		{"超时", errors.New("svc", 408, "deadline"), false},
		{"裸 EOF（发出去了，不知道处理没有）", fmt.Errorf("EOF"), false},
	}
	for _, c := range cases {
		got, err := RetryOnConnectError(context.Background(), nil, 0, c.err)
		if err != nil {
			t.Fatalf("%s: 意外错误 %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: RetryOnConnectError=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestDefaultRetryNeverRetries 锁定默认不重试。框架不替调用方决定一个非幂等调用
// 能不能执行两次；要重试必须由知道自己幂等性的那一方明说。
func TestDefaultRetryNeverRetries(t *testing.T) {
	for _, err := range []error{
		errors.New("svc", 500, "boom"),
		errors.New("svc", 408, "timeout"),
		ErrConnectFailed,
	} {
		if got, _ := DefaultRetry(context.Background(), nil, 0, err); got {
			t.Errorf("默认策略对 %v 返回了重试", err)
		}
	}
}
