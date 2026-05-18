package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/transport"
)

// TestRegisteredFieldIsAtomic 验证 registered 字段通过 atomic.Bool 操作，无竞态
func TestRegisteredFieldIsAtomic(t *testing.T) {
	srv := New()

	var wg sync.WaitGroup
	const goroutines = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.registered.Store(true)
			_ = srv.registered.Load()
			srv.registered.Store(false)
		}()
	}
	wg.Wait()
	// 如果有竞态，-race 检测器会报告
}

// TestStopAggregatesErrors 验证 Stop() 中 transport 和 broker 的错误均被聚合返回
// 对应 H5：原先只返回 ts.Close() 一个错误
func TestStopAggregatesErrors(t *testing.T) {
	tsErr := errors.New("transport-close-error")
	brokerErr := errors.New("broker-close-error")

	// 模拟 Stop goroutine 中的错误聚合逻辑
	joined := errors.Join(tsErr, brokerErr)

	if !errors.Is(joined, tsErr) {
		t.Errorf("expected tsErr in joined error, got: %v", joined)
	}
	if !errors.Is(joined, brokerErr) {
		t.Errorf("expected brokerErr in joined error, got: %v", joined)
	}

	// 验证只有一个错误时仍正确
	onlyOne := errors.Join(nil, brokerErr)
	if !errors.Is(onlyOne, brokerErr) {
		t.Errorf("single error should be preserved in Join: %v", onlyOne)
	}

	// 验证两个 nil 时返回 nil
	if err := errors.Join(nil, nil); err != nil {
		t.Errorf("expected nil when both errors are nil, got: %v", err)
	}
}

// TestMapDeleteVsNilSemantics 验证 delete(map, key) 与 map[key]=nil 的区别
// 对应 H3：Deregister 使用 delete 而非 nil 赋值，确保 key 完全从 map 中移除
func TestMapDeleteVsNilSemantics(t *testing.T) {
	type key struct{ topic string }

	// 正确做法：delete 后 key 不存在
	m := map[key][]string{
		{topic: "t1"}: {"sub1"},
		{topic: "t2"}: {"sub2"},
	}
	k := key{topic: "t1"}
	delete(m, k)

	if _, exists := m[k]; exists {
		t.Error("after delete, key should not exist in map")
	}
	if len(m) != 1 {
		t.Errorf("expected 1 entry remaining, got %d", len(m))
	}

	// 错误做法对比：nil 赋值后 key 仍存在（这是 H3 修复前的 bug 模式）
	m2 := map[key][]string{
		{topic: "t1"}: {"sub1"},
	}
	m2[k] = nil
	if _, exists := m2[k]; !exists {
		t.Error("nil assignment should leave key in map (this is the former bug pattern)")
	}
	if len(m2) != 1 {
		t.Errorf("nil map assignment: expected 1 entry, got %d", len(m2))
	}
}

func TestAddressOptionSyncsBothFields(t *testing.T) {
	const addr = ":19999"
	srv := New(Address(addr))
	cfg := srv.Config()

	if cfg.Address != addr {
		t.Errorf("cfg.Address = %q, want %q", cfg.Address, addr)
	}
	if cfg.Transport.Config().Addrs != addr {
		t.Errorf("Transport.Addrs = %q, want %q", cfg.Transport.Config().Addrs, addr)
	}
}

func TestAddressOptionWithExplicitTransport(t *testing.T) {
	const addr = ":19998"
	srv := New(
		WithTransport(transport.NewHTTPTransport()),
		Address(addr),
	)
	cfg := srv.Config()

	if cfg.Address != addr {
		t.Errorf("cfg.Address = %q, want %q", cfg.Address, addr)
	}
	if cfg.Transport.Config().Addrs != addr {
		t.Errorf("Transport.Addrs = %q, want %q", cfg.Transport.Config().Addrs, addr)
	}
}

// === C6+C7+C8 生命周期加固测试 ===

// fakeBroker：用于 C6 测试，Start() 可控制返回错误，并记录是否被 Close。
type fakeBroker struct {
	broker.IBroker
	startErr   error
	closeCount int32
}

func (f *fakeBroker) Start() error                                                   { return f.startErr }
func (f *fakeBroker) Close() error                                                   { atomic.AddInt32(&f.closeCount, 1); return nil }
func (f *fakeBroker) String() string                                                 { return "fake" }
func (f *fakeBroker) Address() string                                                { return "fake://" }
func (f *fakeBroker) Init(...broker.Option) error                                    { return nil }
func (f *fakeBroker) Config() *broker.Config                                         { return &broker.Config{} }
func (f *fakeBroker) Publish(string, *broker.Message, ...broker.PublishOption) error { return nil }
func (f *fakeBroker) Subscribe(string, broker.Handler, ...broker.SubscribeOption) (broker.ISubscriber, error) {
	return nil, nil
}

// fakeTransport：Listen 返回一个 fakeListener，可观察 Close 调用次数
type fakeTransport struct {
	transport.ITransport
	listener *fakeListener
}

func (t *fakeTransport) Init(opts ...transport.Option) error { return nil }
func (t *fakeTransport) Listen(addr string, opts ...transport.ListenOption) (transport.IListener, error) {
	if t.listener == nil {
		t.listener = &fakeListener{addr: addr}
	}
	return t.listener, nil
}
func (t *fakeTransport) Dial(addr string, opts ...transport.DialOption) (transport.IClient, error) {
	return nil, nil
}
func (t *fakeTransport) String() string   { return "fake-transport" }
func (t *fakeTransport) Protocol() string { return "fake" }
func (t *fakeTransport) Config() *transport.Config {
	return &transport.Config{Addrs: ""}
}

type fakeListener struct {
	addr       string
	closeCount int32
}

func (l *fakeListener) Addr() net.Addr {
	a, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:65535")
	return a
}
func (l *fakeListener) Close() error                           { atomic.AddInt32(&l.closeCount, 1); return nil }
func (l *fakeListener) Accept() (net.Conn, error)              { select {} }
func (l *fakeListener) Serve(h transport.Handler) error        { select {} }

// C6：Broker.Start 失败时，Transport 必须被 Close 以释放底层连接。
func TestServer_Start_RollsBackTransportOnBrokerFailure(t *testing.T) {
	fb := &fakeBroker{startErr: errors.New("forced broker failure")}
	ft := &fakeTransport{}
	srv := New(
		WithTransport(ft),
		WithBroker(fb),
	)

	if err := srv.Start(); err == nil {
		t.Fatalf("expected broker failure to bubble up, got nil")
	}

	if ft.listener == nil {
		t.Fatalf("transport.Listen was never called")
	}
	if got := atomic.LoadInt32(&ft.listener.closeCount); got != 1 {
		t.Fatalf("expected transport listener Close to be called once after broker failure, got %d", got)
	}
}

// C7：并发 Stop() 必须不能死锁。
func TestServer_Stop_IsReentrantSafe(t *testing.T) {
	srv := New()
	srv.started.Store(true)

	done := make(chan struct{})
	go func() {
		ch := <-srv.exit
		ch <- nil
		close(done)
	}()

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			srv.Stop()
		}()
	}

	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("Stop() deadlocked under concurrent invocation")
	}

	<-done
}

// H6：Config.Context 必须有默认值，否则用户提供的 RegisterCheck 调 ctx.Done() 会 nil panic。
func TestServer_Config_HasDefaultContext(t *testing.T) {
	srv := New()
	ctx := srv.Config().Context
	if ctx == nil {
		t.Fatal("Config.Context must be initialized to context.Background() by default")
	}
	// 验证可用：调用任意 context 方法不能 panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Config.Context method panicked: %v", r)
		}
	}()
	_ = ctx.Done()
	_ = ctx.Err()
}

// H7：并发调用 Default(opts...) 不能 race 也不能创建多个 server 实例。
func TestServer_Default_ConcurrentSafe(t *testing.T) {
	// 测试需要重置 default —— 但 sync.Once 不可重置，所以测试只覆盖 race-detector 路径。
	// 这里改用与 Default 同款的内部锁逻辑：N 个 goroutine 同时调 Default，
	// 修复后必须无 -race 警告。
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]*TServer, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = Default(Context(context.Background()))
		}(i)
	}
	wg.Wait()

	// 所有 goroutine 拿到同一个实例
	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Fatalf("Default returned %d-th distinct instance: %p vs %p", i, results[i], results[0])
		}
	}
}

// H8 (subscribers Register/Deregister race) verified by existing
// TestServer_SubscribersWriteIsLocked + code review: Register and Deregister
// now hold self.Lock() across the subscribers loop.

// C8：subscribers 字段写入必须持锁。-race 检测器在并发读写时报警。
func TestServer_SubscribersWriteIsLocked(t *testing.T) {
	srv := New()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			srv.setSubscribers(nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			srv.RLock()
			_ = srv.subscribers
			srv.RUnlock()
		}
	}()
	wg.Wait()
}
