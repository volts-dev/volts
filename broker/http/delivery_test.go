package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/registry"
)

// postSub 直接向订阅端点投一条报文，返回状态码。
//
// 状态码是发布端唯一的反馈通道，所以它本身就是契约的一部分，必须能被断言。
func postSub(t *testing.T, b broker.IBroker, id, contentType, body string) int {
	t.Helper()
	uri := fmt.Sprintf("http://%s%s?id=%s", b.Address(), DefaultSubPath, url.QueryEscape(id))
	rsp, err := http.Post(uri, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", uri, err)
	}
	defer rsp.Body.Close()
	io.Copy(io.Discard, rsp.Body)
	return rsp.StatusCode
}

func testMessageBody(t *testing.T, topic string) string {
	t.Helper()
	b, err := json.Marshal(broker.Message{
		Header: map[string]string{"v-topic": topic},
		Body:   []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// 解不开的报文必须回 4xx，而不是被吞掉后照样回 200。
//
// 此前 `err := ctx.Body().Decode(&m)` 后面跟着一个**空的 if 块**：解码错误被完全
// 丢弃，只靠后面"topic 为空"的分支间接兜住，回的还是 500（可重试）。于是一条永远
// 解不开的报文会被发布端一直留在积压队列里反复重投。
func TestHandler_MalformedBodyIsRejectedNotSwallowed(t *testing.T) {
	b := startBroker(t)

	if code := postSub(t, b, "任意 id", "application/json", "{这不是 json"); code != http.StatusBadRequest {
		t.Errorf("畸形报文返回 %d，期望 %d", code, http.StatusBadRequest)
	}
	if code := postSub(t, b, "任意 id", "application/json", `{"Header":{}}`); code != http.StatusBadRequest {
		t.Errorf("缺 v-topic 的报文返回 %d，期望 %d", code, http.StatusBadRequest)
	}
}

// 注册表里的节点已过期时必须回 4xx。
//
// 此前照样回 200：发布端以为投递成功，消息被丢弃且两端日志都干净 —— 这正是
// "发出去了、没人收到、查不出原因"那类症状的来源。4xx 还告诉发布端这是**永久**
// 失败，不要塞回积压队列每次 Publish 都重试一遍。
func TestHandler_UnknownSubscriberReturns4xx(t *testing.T) {
	b := startBroker(t)
	topic := uuid.New().String()

	if _, err := b.Subscribe(topic, func(broker.IEvent) error { return nil }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	code := postSub(t, b, "早已消失的订阅者", "application/json", testMessageBody(t, topic))
	if code != http.StatusNotFound {
		t.Errorf("未知订阅者返回 %d，期望 %d", code, http.StatusNotFound)
	}
}

func TestSubscriber_TopicAndConfigAreReal(t *testing.T) {
	b := startBroker(t)
	topic := uuid.New().String()

	sub, err := b.Subscribe(topic, func(broker.IEvent) error { return nil }, broker.Queue("shared"))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if got := sub.Topic(); got != topic {
		t.Errorf("Topic() = %q，期望 %q", got, topic)
	}
	cfg := sub.Config()
	if cfg == nil {
		t.Fatal("Config() 返回 nil —— 调用方读 sub.Config().Queue 就是空指针崩溃")
	}
	if cfg.Queue != "shared" {
		t.Errorf("Config().Queue = %q，期望 %q", cfg.Queue, "shared")
	}
}

// Unsubscribe 必须真的停止投递（此前是空实现，handler 会一直被调用到进程退出）。
//
// 发布方必须是**另一个** broker 实例：同一个实例内部的扇出不经过 registry，
// 看不见节点是否已注销。
func TestUnsubscribe_StopsDelivery(t *testing.T) {
	topic := uuid.New().String()
	sink := startBroker(t)
	pubr := startBroker(t)

	hit := make(chan struct{}, 8)
	sub, err := sink.Subscribe(topic, func(broker.IEvent) error {
		hit <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	msg := &broker.Message{Body: []byte(`{"m":1}`)}

	// 先确认这条链路本来是通的，否则"退订后没收到"什么都证明不了。
	if err := pubr.Publish(topic, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		t.Fatal("退订前就投不到，本用例失去意义")
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	if err := pubr.Publish(topic, msg); err != nil {
		t.Fatalf("退订后 Publish: %v", err)
	}
	select {
	case <-hit:
		t.Error("退订后 handler 仍被调用")
	case <-time.After(700 * time.Millisecond):
	}
}

// 退订一个订阅者，不能把同 topic 上其余订阅者一起摘掉。
//
// 一个 topic 的所有订阅共用同一个 node.Id（topic + "-" + broker uuid），在 registry
// 里只有一条记录。"退订即 Deregister"会把整个进程从该 topic 的投递列表里删掉，
// 剩下的 handler 一起收不到消息，且没有任何报错。
func TestUnsubscribe_OneOfTwoKeepsTheOtherAlive(t *testing.T) {
	topic := uuid.New().String()
	sink := startBroker(t)
	pubr := startBroker(t)

	gone := make(chan struct{}, 8)
	kept := make(chan struct{}, 8)

	subGone, err := sink.Subscribe(topic, func(broker.IEvent) error {
		gone <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := sink.Subscribe(topic, func(broker.IEvent) error {
		kept <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := subGone.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	if err := pubr.Publish(topic, &broker.Message{Body: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case <-kept:
	case <-time.After(10 * time.Second):
		t.Fatal("退订一个订阅者后，同 topic 上剩下的那个也收不到了（节点被整条注销）")
	}
	select {
	case <-gone:
		t.Error("已退订的 handler 仍被调用")
	case <-time.After(500 * time.Millisecond):
	}
}

// handler 报错不得触发重投。
//
// 消息已经送到了，失败的是业务处理；把它当投递失败塞回积压队列，等于让一个必然
// 失败的 handler 对每条后继消息都多消费一次 —— 而本 broker 是 at-most-once，
// 调用方没有做幂等的义务。
func TestHandlerError_DoesNotCauseRedelivery(t *testing.T) {
	topic := uuid.New().String()
	sink := startBroker(t)
	pubr := startBroker(t)

	var mu sync.Mutex
	var calls int
	beat := make(chan struct{}, 16)

	if _, err := sink.Subscribe(topic, func(broker.IEvent) error {
		mu.Lock()
		calls++
		mu.Unlock()
		beat <- struct{}{}
		return fmt.Errorf("业务处理故意失败")
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := pubr.Publish(topic, &broker.Message{Body: []byte(`{}`)}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
		select {
		case <-beat:
		case <-time.After(10 * time.Second):
			t.Fatalf("第 %d 条消息没送到", i+1)
		}
		// beat 是在 handler 里发的，此刻发布端那次 POST 还没返回。必须等它走完，
		// 否则"这条被判为投递失败并入队"发生在下一次 Publish 取积压之后，
		// 重投要到第三条消息才现形，本用例就成了看运气。
		time.Sleep(500 * time.Millisecond)
	}

	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("handler 被调用 %d 次，期望 2 次（报错不应触发重投）", calls)
	}
}

// 积压超限时丢**最旧**的那条，而不是刚进来的那条。
//
// 此前是 c = c[:64]：保住最旧的 64 条、把刚 append 的新消息立刻扔掉。于是积压一满，
// 此后每条新消息都发不出去，进程却在原地反复重投一批早已过时的旧消息，全程无日志。
func TestSaveMessage_DropsOldestNotNewest(t *testing.T) {
	h := New().(*httpBroker)
	topic := "inbox"

	total := maxInboxSize + 10
	for i := 0; i < total; i++ {
		h.saveMessage(topic, []byte(fmt.Sprintf("%d", i)))
	}

	got := h.getMessage(topic, total)
	if len(got) != maxInboxSize {
		t.Fatalf("积压 %d 条，期望上限 %d", len(got), maxInboxSize)
	}

	// 保留的应该是最后 64 条：10..73
	if first, want := string(got[0]), fmt.Sprintf("%d", total-maxInboxSize); first != want {
		t.Errorf("最旧的一条是 %q，期望 %q —— 说明丢的是新消息而不是旧消息", first, want)
	}
	if last, want := string(got[len(got)-1]), fmt.Sprintf("%d", total-1); last != want {
		t.Errorf("最新的一条是 %q，期望 %q —— 最后写入的消息被丢掉了", last, want)
	}
}

// 投递响应的状态码决定这条消息是重投还是丢弃。
//
// 此前状态码根本没读：对端回什么都算投递成功。两种失败都被吞掉 ——
// 5xx（对端暂时出问题，本该重投）和 4xx（对端明确拒绝，本该丢弃）。
func TestPublish_ClassifiesResponseStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		backlog int
	}{
		{"5xx 可重试：留在积压里等下次带出去", http.StatusInternalServerError, 1},
		{"4xx 永久拒绝：直接丢弃，不占积压", http.StatusNotFound, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
			}))
			t.Cleanup(peer.Close)

			hb := startBroker(t).(*httpBroker)
			topic := uuid.New().String()

			// 手工注册一个指向上面那台假订阅者的节点。
			svc := &registry.Service{
				Name:    hb.config.Name,
				Version: hb.broadcastVersion(),
				Nodes: []*registry.Node{{
					Id:      topic + "-fake",
					Address: strings.TrimPrefix(peer.URL, "http://"),
					Metadata: map[string]string{
						"secure": "false",
						"broker": "http",
						"topic":  topic,
					},
				}},
			}
			if err := hb.registry.Register(svc); err != nil {
				t.Fatalf("Register: %v", err)
			}
			t.Cleanup(func() { _ = hb.registry.Deregister(svc) })

			if err := hb.Publish(topic, &broker.Message{Body: []byte(`{}`)}); err != nil {
				t.Fatalf("Publish: %v", err)
			}

			// 投递是异步的，等它走完一轮。
			time.Sleep(800 * time.Millisecond)

			hb.mtx.RLock()
			got := len(hb.inbox[topic])
			hb.mtx.RUnlock()
			if got != c.backlog {
				t.Errorf("积压 %d 条，期望 %d 条", got, c.backlog)
			}
		})
	}
}
