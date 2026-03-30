package memory

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/volts-dev/volts/broker"
)

func TestMemoryBroker(t *testing.T) {
	b := New()

	if err := b.Start(); err != nil {
		t.Fatalf("unexpected connect error: %v", err)
	}

	topic := "test"
	count := 10
	var received int32

	sub, err := b.Subscribe(topic, func(p broker.IEvent) error {
		atomic.AddInt32(&received, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error subscribing: %v", err)
	}

	for i := 0; i < count; i++ {
		if err := b.Publish(topic, &broker.Message{
			Header: map[string]string{"id": fmt.Sprintf("%d", i)},
			Body:   []byte(`hello world`),
		}); err != nil {
			t.Fatalf("unexpected error publishing %d: %v", i, err)
		}
	}

	if int(atomic.LoadInt32(&received)) != count {
		t.Fatalf("expected %d messages, got %d", count, received)
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unexpected error unsubscribing: %v", err)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
}

// 验证消息体内容正确传递
func TestMemoryBrokerMessageContent(t *testing.T) {
	b := New()
	if err := b.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Close()

	var got *broker.Message
	sub, err := b.Subscribe("content", func(e broker.IEvent) error {
		got = e.Message()
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	want := &broker.Message{
		Header: map[string]string{"k": "v"},
		Body:   []byte("payload"),
	}
	if err := b.Publish("content", want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if got == nil {
		t.Fatal("handler never called")
	}
	if string(got.Body) != string(want.Body) {
		t.Errorf("body: got %q, want %q", got.Body, want.Body)
	}
	if got.Header["k"] != "v" {
		t.Errorf("header: got %q, want %q", got.Header["k"], "v")
	}
}

// 发布到无订阅的 topic 不报错
func TestMemoryBrokerPublishNoSubscribers(t *testing.T) {
	b := New()
	if err := b.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Close()

	if err := b.Publish("empty-topic", &broker.Message{Body: []byte("x")}); err != nil {
		t.Fatalf("publish to empty topic should not error, got: %v", err)
	}
}

// 未连接时 Publish/Subscribe 应返回错误
func TestMemoryBrokerNotConnected(t *testing.T) {
	b := New()

	if err := b.Publish("t", &broker.Message{}); err == nil {
		t.Fatal("expected error when not connected")
	}
	if _, err := b.Subscribe("t", func(broker.IEvent) error { return nil }); err == nil {
		t.Fatal("expected error when not connected")
	}
}

// 取消订阅后不再收到消息
func TestMemoryBrokerUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	if err := b.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Close()

	var count int32
	sub, err := b.Subscribe("unsub", func(broker.IEvent) error {
		atomic.AddInt32(&count, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	_ = b.Publish("unsub", &broker.Message{Body: []byte("1")})
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	_ = b.Publish("unsub", &broker.Message{Body: []byte("2")})

	// 给 unsubscribe goroutine 一个 yield 机会
	// （channel 已接收，无需 sleep）
	runtime_Gosched()

	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected 1 message after unsubscribe, got %d", count)
	}
}

// 多订阅者并发订阅同一 topic
func TestMemoryBrokerConcurrentSubscribers(t *testing.T) {
	b := New()
	if err := b.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Close()

	const n = 20
	var wg sync.WaitGroup
	var total int32

	subs := make([]broker.ISubscriber, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sub, err := b.Subscribe("concurrent", func(broker.IEvent) error {
				atomic.AddInt32(&total, 1)
				return nil
			})
			if err != nil {
				t.Errorf("subscribe %d: %v", idx, err)
				return
			}
			subs[idx] = sub
		}(i)
	}
	wg.Wait()

	if err := b.Publish("concurrent", &broker.Message{Body: []byte("ping")}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if int(atomic.LoadInt32(&total)) != n {
		t.Fatalf("expected %d deliveries, got %d", n, total)
	}

	for _, sub := range subs {
		if sub != nil {
			sub.Unsubscribe()
		}
	}
}

// handler 返回错误时 Publish 应透传该错误
func TestMemoryBrokerHandlerError(t *testing.T) {
	b := New()
	if err := b.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Close()

	sub, err := b.Subscribe("errtopic", func(broker.IEvent) error {
		return fmt.Errorf("handler failed")
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	if err := b.Publish("errtopic", &broker.Message{Body: []byte("x")}); err == nil {
		t.Fatal("expected error from handler to propagate")
	}
}

// 并发 Publish 不应 race
func TestMemoryBrokerConcurrentPublish(t *testing.T) {
	b := New()
	if err := b.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Close()

	var received int32
	sub, _ := b.Subscribe("pubrace", func(broker.IEvent) error {
		atomic.AddInt32(&received, 1)
		return nil
	})
	defer sub.Unsubscribe()

	const publishers = 10
	const msgs = 50
	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < msgs; j++ {
				b.Publish("pubrace", &broker.Message{Body: []byte("x")})
			}
		}()
	}
	wg.Wait()

	if int(atomic.LoadInt32(&received)) != publishers*msgs {
		t.Fatalf("expected %d, got %d", publishers*msgs, received)
	}
}

// Gosched 让 unsubscribe 的 goroutine 有机会执行
func runtime_Gosched() { var wg sync.WaitGroup; wg.Add(1); go func() { wg.Done() }(); wg.Wait() }
