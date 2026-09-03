package http

import (
	"fmt"
	"net/http"

	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/router"
)

// Handler 是订阅端的投递入口（POST DefaultSubPath?id=<node.Id>）。
//
// 回复的状态码是发布端唯一的反馈通道，语义分三档，Publish 依此决定重投还是丢弃：
//
//   - 4xx = **永久拒绝**。报文解不开、没有 topic、这个 id 的订阅者已经不在了
//     —— 换多少次重投结果都一样，发布端直接丢弃，不进积压队列。
//   - 5xx = 可重试。
//   - 2xx = 已投递。**handler 报错也算 2xx**：消息确实送到了，失败的是业务处理。
//     回 5xx 会让发布端把它塞回积压队列，于是一个必然失败的 handler 把"业务出错"
//     升级成"每条后继消息都多一次重复投递"，而本 broker 是 at-most-once 语义，
//     调用方并没有做幂等的义务。
func (h *httpBroker) Handler(ctx *router.THttpContext) {
	var m broker.Message
	if err := ctx.Body().Decode(&m); err != nil {
		// 此前这里是个空的 if 块：解码错误被完全吞掉，靠后面"topic 为空"
		// 的分支间接兜住，报出来的还是一句拼错的 "Error parsing request body: <nil>"。
		log.Errf("broker: 投递报文解码失败: %v", err)
		ctx.WriteHeader(http.StatusBadRequest)
		ctx.Write([]byte(fmt.Sprintf("broker: malformed message: %v", err)))
		return
	}

	topic := m.Header["v-topic"]
	delete(m.Header, "v-topic")

	if len(topic) == 0 {
		log.Errf("broker: 投递报文里没有 v-topic 头")
		ctx.WriteHeader(http.StatusBadRequest)
		ctx.Write([]byte("broker: topic not found"))
		return
	}

	id := ctx.MethodParams().FieldByName("id").AsString()

	//nolint:prealloc
	var subs []broker.Handler

	h.RLock()
	for _, subscriber := range h.subscribers[topic] {
		if id != subscriber.id {
			continue
		}
		subs = append(subs, subscriber.fn)
	}
	h.RUnlock()

	if len(subs) == 0 {
		// registry 里这个节点已经过期：订阅被取消了，或者进程重启换了 uuid 而
		// 旧记录的租约还没到期。此前照样回 200，发布端以为投成功了 —— 于是一条
		// 消息"发出去了、没人收到、两边日志都干净"，正是最难查的那种症状。
		log.Warnf("broker: 收到 topic %q 的消息，但本进程没有 id 为 %q 的订阅者（注册表里的节点已过期）", topic, id)
		ctx.WriteHeader(http.StatusNotFound)
		ctx.Write([]byte("broker: subscriber not found"))
		return
	}

	event := &event{
		m: &m,
		t: topic,
	}

	// dispatch 在子函数里调用，让 defer recover 捕获 handler 的 panic 并转成
	// error —— 与 broker/memory 同一约定：一个坏订阅者不能连累同一 topic 上
	// 其余订阅者收不到这条消息。
	dispatch := func(fn broker.Handler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("broker/http: handler panicked: %v", r)
			}
		}()
		return fn(event)
	}

	for _, fn := range subs {
		if err := dispatch(fn); err != nil {
			// 错误此前只写进 event.err，且被下一轮直接覆盖 —— 最后一个 handler
			// 成功就等于全都成功，没有任何出口。现在逐个上报：优先交给配置的
			// ErrorHandler（这个 Option 在 http 后端此前完全没人读），否则记日志。
			event.err = err
			if eh := h.config.ErrorHandler; eh != nil {
				eh(event)
			} else {
				log.Errf("broker: topic %q 的订阅处理失败: %v", topic, err)
			}
		}
	}
}
