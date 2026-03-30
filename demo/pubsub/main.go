// demo/pubsub/main.go
//
// 演示 volts 完整订阅发布流程：
//   1. 用内存 broker 启动服务
//   2. 通过 router.Subscribe 注册订阅处理器
//   3. 用 server.Publisher 向同一 topic 发布消息
//   4. 验证处理器收到消息后退出
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/volts-dev/volts/broker"
	_ "github.com/volts-dev/volts/broker/memory"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

const topic = "order.created"

// OrderEvent 是发布到 topic 的消息载体，实现 server.IMessage
type OrderEvent struct {
	OrderID string
	Amount  float64
}

func (e *OrderEvent) Topic() string       { return topic }
func (e *OrderEvent) ContentType() string { return "application/json" }
func (e *OrderEvent) Payload() interface{} {
	return map[string]interface{}{
		"order_id": e.OrderID,
		"amount":   e.Amount,
	}
}

// orderHandler 是订阅处理器，签名必须是 func(*router.TSubscriberContext)
func orderHandler(ctx *router.TSubscriberContext) {
	msg := ctx.Event.Message()
	logger.Infof("[subscriber] topic=%s body=%s", ctx.Event.Topic(), string(msg.Body))
}

func main() {
	log := logger.New("pubsub-demo")

	// 1. 创建 router 并注册订阅
	r := router.New()
	if err := r.Subscribe(r.NewSubscriber(topic, orderHandler)); err != nil {
		log.Errf("subscribe: %v", err)
		os.Exit(1)
	}

	// 2. 启动 server（使用 memory broker）
	memBroker := broker.Use("memory")
	srv := server.New(
		server.WithRouter(r),
		server.WithBroker(memBroker),
	)
	if err := srv.Start(); err != nil {
		log.Errf("server start: %v", err)
		os.Exit(1)
	}
	defer srv.Stop()

	// 3. 创建 Publisher，共用 server 的 broker
	pub := server.NewPublisher(
		server.WithPublisherBroker(srv.Config().Broker),
	)

	// 稍等 broker 完全就绪
	time.Sleep(50 * time.Millisecond)

	// 4. 发布 3 条消息
	for i := 1; i <= 3; i++ {
		evt := &OrderEvent{
			OrderID: fmt.Sprintf("ORD-%04d", i),
			Amount:  float64(i) * 99.9,
		}
		if err := pub.Publish(context.Background(), evt); err != nil {
			log.Errf("publish #%d: %v", i, err)
		} else {
			log.Infof("[publisher] sent order %s", evt.OrderID)
		}
	}

	// 给 handler 处理时间后退出
	time.Sleep(100 * time.Millisecond)
	log.Info("done")
}
