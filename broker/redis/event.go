package redis

import (
	"context"
	"sync/atomic"

	"github.com/volts-dev/volts/broker"
)

type (
	// event 是投给 handler 的一条消息。
	//
	// 与内置的 http / memory 后端不同，这里的 Ack() **是真的**：它对应一次
	// XACK，把消息从消费组的 PEL（待确认列表）里摘掉。没被确认的消息会在
	// ClaimMinIdle 之后被重新认领投递 —— 也就是说 redis 后端是 at-least-once，
	// 调用方需要自己保证幂等，这一点与另外两个后端相反。
	event struct {
		topic string
		msg   *broker.Message
		err   error

		id    string
		sub   *redisSubscriber
		ctx   context.Context
		acked atomic.Bool
	}
)

func (e *event) Topic() string { return e.topic }

func (e *event) Message() *broker.Message { return e.msg }

func (e *event) Error() error { return e.err }

// Ack 确认这条消息已被处理完毕。
//
// 重复调用是安全的（只有第一次会真的发 XACK）：AutoAck 打开时框架会替调用方
// 确认一次，而 handler 里往往也会顺手调一下。
func (e *event) Ack() error {
	if e.acked.Swap(true) {
		return nil
	}
	return e.sub.ack(e.ctx, e.id)
}
