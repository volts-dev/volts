package client

import (
	"context"
	"errors"
	"testing"

	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

// fakeRequest 只实现 IRequest 里钩子会碰到的部分，其余返回零值。
type fakeRequest struct {
	header header.Header
}

func newFakeRequest() *fakeRequest {
	return &fakeRequest{header: make(header.Header)}
}

func (r *fakeRequest) Service() string        { return "svc" }
func (r *fakeRequest) Method() string         { return "m" }
func (r *fakeRequest) ContentType() string    { return "application/json" }
func (r *fakeRequest) Header() header.Header  { return r.header }
func (r *fakeRequest) Body() *body.TBody      { return nil }
func (r *fakeRequest) Stream() bool           { return false }
func (r *fakeRequest) Options() RequestOptions { return RequestOptions{} }

func TestApplyRequestHooksOrderGlobalBeforeOwn(t *testing.T) {
	ResetGlobalRequestHooks()
	defer ResetGlobalRequestHooks()

	var order []string
	RegisterGlobalRequestHook(func(ctx context.Context, req IRequest) (context.Context, error) {
		order = append(order, "global")
		return ctx, nil
	})
	own := []RequestHook{func(ctx context.Context, req IRequest) (context.Context, error) {
		order = append(order, "own")
		return ctx, nil
	}}

	if _, err := applyRequestHooks(context.Background(), newFakeRequest(), own); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(order) != 2 || order[0] != "global" || order[1] != "own" {
		t.Fatalf("进程级钩子必须先于客户端级执行，实际顺序 %v", order)
	}
}

// 身份盖不上就不该把请求发出去——错误必须中止，不能吞掉继续发匿名请求。
func TestApplyRequestHooksErrorAborts(t *testing.T) {
	ResetGlobalRequestHooks()
	defer ResetGlobalRequestHooks()

	want := errors.New("no identity")
	ran := false
	RegisterGlobalRequestHook(func(ctx context.Context, req IRequest) (context.Context, error) {
		return nil, want
	})
	own := []RequestHook{func(ctx context.Context, req IRequest) (context.Context, error) {
		ran = true
		return ctx, nil
	}}

	_, err := applyRequestHooks(context.Background(), newFakeRequest(), own)
	if !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}
	if ran {
		t.Fatal("前一个钩子报错后，后续钩子不该再执行")
	}
}

func TestApplyRequestHooksCanSetHeaderAndContext(t *testing.T) {
	ResetGlobalRequestHooks()
	defer ResetGlobalRequestHooks()

	type ctxKey struct{}
	RegisterGlobalRequestHook(func(ctx context.Context, req IRequest) (context.Context, error) {
		req.Header().Set("Caller-Identity", "websvc")
		return context.WithValue(ctx, ctxKey{}, "v"), nil
	})

	req := newFakeRequest()
	ctx, err := applyRequestHooks(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := req.Header().Get("Caller-Identity"); got != "websvc" {
		t.Fatalf("钩子对请求头的修改没生效，got %q", got)
	}
	if ctx.Value(ctxKey{}) != "v" {
		t.Fatal("钩子返回的 ctx 没有被采用")
	}
}

// 没有任何钩子时必须是零成本、且原样返回 ctx（这是绝大多数调用的路径）。
func TestApplyRequestHooksNoopWhenEmpty(t *testing.T) {
	ResetGlobalRequestHooks()
	defer ResetGlobalRequestHooks()

	base := context.Background()
	got, err := applyRequestHooks(base, newFakeRequest(), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != base {
		t.Fatal("无钩子时应原样返回同一个 ctx")
	}
}

// nil 钩子（配置疏漏）不该 panic。
func TestApplyRequestHooksSkipsNil(t *testing.T) {
	ResetGlobalRequestHooks()
	defer ResetGlobalRequestHooks()

	RegisterGlobalRequestHook(nil)
	if _, err := applyRequestHooks(context.Background(), newFakeRequest(), []RequestHook{nil}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}
