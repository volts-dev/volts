package server

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/errors"
	"github.com/volts-dev/volts/internal/metadata"
)

type (
	// PublisherOption defines the method to customize a Publisher.
	PublisherOption func(*Publisher)
	// PublishOption used by Publish.
	PublishOption func(*PublishConfig)

	PublishConfig struct {
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
		// Exchange is the routing exchange for the message
		Exchange string
	}

	// Message is the interface for publishing asynchronously.
	IMessage interface {
		Topic() string
		Payload() interface{}
		ContentType() string // codec 类型 用于指定序列或者加密
	}

	Publisher struct {
		id     string
		once   atomic.Value
		Broker broker.IBroker
		Client client.IClient
	}
)

// NewPublisher returns a Publisher.
func NewPublisher(opts ...PublisherOption) *Publisher {
	pub := &Publisher{
		id: uuid.New().String(),
	}
	pub.Init(opts...)
	return pub
}

func (self *Publisher) Init(opts ...PublisherOption) {
	for _, opt := range opts {
		opt(self)
	}
}

// 发布订阅消息
func (self *Publisher) Publish(ctx context.Context, msg IMessage, opts ...PublishOption) error {
	cfg := PublishConfig{
		Context: context.Background(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	// set the topic
	topic := msg.Topic()

	metadata, ok := metadata.FromContext(ctx)
	if !ok {
		metadata = make(map[string]string)
	}

	metadata["Content-Type"] = msg.ContentType()
	metadata["Micro-Topic"] = topic
	metadata["Micro-ID"] = self.id

	// get the exchange
	if len(cfg.Exchange) > 0 {
		topic = cfg.Exchange
	}

	// encode message body
	cf := codec.Use(msg.ContentType())
	if cf == 0 {
		return nil
		//return merrors.InternalServerError(packageID, err.Error())
	}

	// 验证解码器
	msgCodece := codec.IdentifyCodec(cf)
	if msgCodece == nil { // no codec specified
		return errors.UnsupportedCodec("volts.client", cf)
	}

	body, err := msgCodece.Encode(msg.Payload())
	if err != nil {
		return err
	}

	l, ok := self.once.Load().(bool)
	if !ok {
		return fmt.Errorf("failed to cast to bool")
	}

	if !l {
		if err = self.Broker.Start(); err != nil {
			return err
			//return merrors.InternalServerError(packageID, err.Error())
		}

		self.once.Store(true)
	}

	return self.Broker.Publish(topic, &broker.Message{
		Header: metadata,
		Body:   body,
	}, broker.PublishContext(cfg.Context))
}

// WithId customizes a Publisher with the id.
func WithId(id string) PublisherOption {
	return func(publisher *Publisher) {
		publisher.id = id
	}
}

func WithClient(cli client.IClient) PublisherOption {
	return func(publisher *Publisher) {
		publisher.Client = cli
	}
}

func WithPublisherBroker(brok broker.IBroker) PublisherOption {
	return func(publisher *Publisher) {
		publisher.Broker = brok
	}
}
