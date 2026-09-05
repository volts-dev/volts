package router

import (
	"strings"
	"testing"
)

type fakeEvent struct {
	topic string
}

func (e *fakeEvent) Topic() string { return e.topic }

type structSub struct {
	hit *int
}

// OnOrder 是值接收者、签名正确 —— 应当被登记并真的执行。
func (s structSub) OnOrder(ctx *TSubscriberContext) { *s.hit++ }

// Helper 签名对不上，应被忽略（结构体上有别的方法是常态）。
func (s structSub) Helper() string { return "not a subscriber" }

type ptrOnlySub struct{ hit *int }

// OnlyOnPointer 是**指针**接收者，不在值的方法集里 —— 与路由层的通则一致，不会被登记。
func (s *ptrOnlySub) OnlyOnPointer(ctx *TSubscriberContext) { *s.hit++ }

// TestSubscriber_StructMethodsActuallyFire 锁定结构体订阅者真的会被调用。
//
// 此前 parseSubscriber 的两条分支不对称：`func(*TSubscriberContext)` 那条往
// `hd.funcs` 里放执行体，走反射的那条只建 handler 就返回、funcs 是空的。而
// `handler.InvokeSubscriber` 遍历的正是 funcs —— 于是结构体订阅者**静默地一次都不
// 触发**：Subscribe 返回 nil、注册中心里有这条订阅、消息也投递到了本进程，就是没人处理。
//
// 根因是取方法的方式：`Type().Method(i).Func` 是**未绑定**的，第一个入参是接收者，
// 签名成了 func(T, *TSubscriberContext)，既对不上 SubFunc 的类型，也没有接收者可传。
func TestSubscriber_StructMethodsActuallyFire(t *testing.T) {
	hit := 0
	sub := newSubscriber("orders", structSub{hit: &hit})

	if err := validateSubscriber(sub); err != nil {
		t.Fatalf("校验不该失败: %v", err)
	}
	if len(sub.Handlers()) != 1 {
		t.Fatalf("应只登记 1 个处理器（Helper 签名对不上要忽略），实际 %d", len(sub.Handlers()))
	}

	ctx := &TSubscriberContext{}
	for _, hd := range sub.Handlers() {
		if err := hd.InvokeSubscriber(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if hit != 1 {
		t.Fatalf("处理器被调用了 %d 次，期望 1 —— 结构体订阅者又变回静默失效了", hit)
	}
}

// TestSubscriber_PlainFuncStillFires 反向：原本就能工作的那条路径不能被改坏。
func TestSubscriber_PlainFuncStillFires(t *testing.T) {
	hit := 0
	sub := newSubscriber("orders", func(ctx *TSubscriberContext) { hit++ })

	if err := validateSubscriber(sub); err != nil {
		t.Fatalf("校验不该失败: %v", err)
	}
	for _, hd := range sub.Handlers() {
		hd.InvokeSubscriber(&TSubscriberContext{})
	}
	if hit != 1 {
		t.Fatalf("被调用了 %d 次，期望 1", hit)
	}
}

// TestSubscriber_PointerReceiverIsNotRegistered 锁定指针接收者不被登记 —— 与本框架
// 路由层"值接收者"的通则一致。关键是它必须**报得出来**，而不是登记一个永不触发的桩。
func TestSubscriber_PointerReceiverIsNotRegistered(t *testing.T) {
	hit := 0
	sub := newSubscriber("orders", ptrOnlySub{hit: &hit})
	if len(sub.Handlers()) != 0 {
		t.Fatalf("指针接收者的方法不该被登记，实际登记了 %d 个", len(sub.Handlers()))
	}
}

// TestValidateSubscriber_RejectsHandlerWithoutBody 锁定校验函数真的会拒。
//
// 它此前的函数体就是 `return nil` —— 什么都不校验，于是"登记得上、永远不触发"这件事
// 一路畅通到线上，表现为"这个事件没反应"。
func TestValidateSubscriber_RejectsHandlerWithoutBody(t *testing.T) {
	sub := &subscriber{
		topic:    "orders",
		handlers: []*handler{{Name: "orders-empty"}},
	}
	err := validateSubscriber(sub)
	if err == nil {
		t.Fatal("没有可执行函数体的处理器必须被拒")
	}
	for _, want := range []string{"orders", "TSubscriberContext"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息里应指出 %q，实际: %v", want, err)
		}
	}

	if err := validateSubscriber(nil); err == nil {
		t.Error("nil 订阅者应被拒")
	}
}
