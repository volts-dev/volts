package http

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/broker"
)

// startBroker 起一个已监听的 http broker，测试结束自动 Close。
func startBroker(t *testing.T) broker.IBroker {
	t.Helper()
	b := New(broker.WithRegistry(newTestRegistry()))
	if err := b.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// 广播订阅（不带 queue）必须投给**每一个**节点。
//
// 此前投不到：订阅端把 config.BroadcastVersion（默认 "broadcast"）写进
// registry.Service.Version，发布端 switch 比对的却是包变量 broadcastVersion
// （"volts.http.broadcast"）。两者永不相等，于是每条广播都掉进 default 分支，
// 被当成 queue group **随机投给一个节点**，没有任何报错。
//
// 单进程内复现不出来：同一个 broker 的所有订阅共用一个 node.Id
// （topic + "-" + broker uuid），一次 POST 到达后 Handler 会扇出给本地全部
// handler —— 所以 TestConcurrentSubBroker 一直是绿的。必须两个实例才能看见。
func TestPublish_BroadcastReachesAllNodes(t *testing.T) {
	topic := uuid.New().String()

	var mu sync.Mutex
	seen := make(map[string]bool)
	done := make(chan string, 2)

	var brokers []broker.IBroker
	for _, name := range []string{"a", "b"} {
		b := startBroker(t)
		brokers = append(brokers, b)

		if _, err := b.Subscribe(topic, func(broker.IEvent) error {
			mu.Lock()
			if !seen[name] {
				seen[name] = true
				done <- name
			}
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("%s Subscribe: %v", name, err)
		}
	}

	msg := &broker.Message{Body: []byte(`{"message":"hello"}`)}
	if err := brokers[0].Publish(topic, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// 等待两侧各收到一次。必须带超时：投递是 fire-and-forget 的异步 goroutine，
	// 收不到时这里会永远阻塞，把一次失败变成整包测试 300s 超时。
	deadline := time.After(10 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-deadline:
			mu.Lock()
			t.Fatalf("广播只到达 %d/2 个节点，已收到: %v", len(seen), seen)
			mu.Unlock()
		}
	}
}

// 带 queue 的订阅仍然只投给一个节点 —— 上面的修复不能退化成"一律广播"。
func TestPublish_QueueGroupReachesOneNode(t *testing.T) {
	topic := uuid.New().String()

	var mu sync.Mutex
	var count int
	first := make(chan struct{}, 4)

	var brokers []broker.IBroker
	for i := 0; i < 2; i++ {
		b := startBroker(t)
		brokers = append(brokers, b)

		if _, err := b.Subscribe(topic, func(broker.IEvent) error {
			mu.Lock()
			count++
			mu.Unlock()
			first <- struct{}{}
			return nil
		}, broker.Queue("shared")); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	if err := brokers[0].Publish(topic, &broker.Message{Body: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("queue 订阅一个节点都没收到")
	}

	// 再给一段时间，确认没有第二个节点也收到。
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("queue group 收到 %d 次，期望 1 次（不应广播）", count)
	}
}
