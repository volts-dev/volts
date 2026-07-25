package broker

import "testing"

// 没有任何后端注册时，Use 必须返回 nil 而不是调用 nil 构造器。
//
// 后端靠 init() 注册（通常是一行空导入），一个只用 HTTP/RPC 的进程完全可能
// 一个 broker 后端都没引入。此前这里直接段错误，且崩在 init 阶段——栈里看不出
// "你没导入 broker 后端"这个真正原因，只看到 broker.Use 里一行 nil 调用。
func TestUse_NoBackendRegisteredReturnsNil(t *testing.T) {
	saved := brokerMap
	brokerMap = make(map[string]func(opts ...Option) IBroker)
	t.Cleanup(func() { brokerMap = saved })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("未注册后端时 Use 发生 panic: %v", r)
		}
	}()

	if got := Use("http"); got != nil {
		t.Errorf("Use = %v，期望 nil", got)
	}
	if got := Use("不存在的后端"); got != nil {
		t.Errorf("未知后端 Use = %v，期望 nil", got)
	}
}

// Default 同理：返回 nil，让调用方自己决定降级，而不是崩在启动阶段。
func TestDefault_NoBackendRegisteredReturnsNil(t *testing.T) {
	savedMap, savedDef := brokerMap, defaultBroker
	brokerMap = make(map[string]func(opts ...Option) IBroker)
	defaultBroker = nil
	t.Cleanup(func() { brokerMap, defaultBroker = savedMap, savedDef })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("未注册后端时 Default 发生 panic: %v", r)
		}
	}()

	if got := Default(); got != nil {
		t.Errorf("Default = %v，期望 nil", got)
	}
}
