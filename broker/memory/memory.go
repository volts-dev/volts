// Package memory provides a memory broker
package memory

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/broker"
	maddr "github.com/volts-dev/volts/internal/addr"
	mnet "github.com/volts-dev/volts/internal/net"
	"github.com/volts-dev/volts/logger"
)

var log = logger.New("broker")

type (
	memoryBroker struct {
		config *broker.Config

		addr string
		sync.RWMutex
		connected   bool
		Subscribers map[string][]*memorySubscriber
	}

	memoryEvent struct {
		config  broker.Config
		topic   string
		err     error
		message interface{}
	}

	memorySubscriber struct {
		id      string
		topic   string
		exit    chan bool
		handler broker.Handler
		opts    *broker.SubscribeConfig
	}
)

func init() {
	broker.Register("memory", New)
}

func New(opts ...broker.Option) broker.IBroker {
	rand.Seed(time.Now().UnixNano())
	var defaultOpts []broker.Option
	defaultOpts = append(defaultOpts,
		broker.WithName("memory"),
		broker.WithContext(context.Background()),
	)

	cfg := broker.NewConfig(append(defaultOpts, opts...)...)

	return &memoryBroker{
		config:      cfg,
		Subscribers: make(map[string][]*memorySubscriber),
	}
}

func (m *memoryBroker) Config() *broker.Config {
	return m.config
}

func (m *memoryBroker) Address() string {
	return m.addr
}

func (m *memoryBroker) Start() error {
	m.Lock()
	defer m.Unlock()

	if m.connected {
		return nil
	}

	// use 127.0.0.1 to avoid scan of all network interfaces
	addr, err := maddr.Extract("127.0.0.1")
	if err != nil {
		return err
	}
	i := rand.Intn(20000)
	// set addr with port
	addr = mnet.HostPort(addr, 10000+i)

	m.addr = addr
	m.connected = true

	return nil
}

func (m *memoryBroker) Close() error {
	m.Lock()
	defer m.Unlock()

	if !m.connected {
		return nil
	}

	m.connected = false

	return nil
}

func (m *memoryBroker) Init(opts ...broker.Option) error {
	for _, o := range opts {
		o(m.config)
	}
	return nil
}

func (m *memoryBroker) Publish(topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	m.RLock()
	if !m.connected {
		m.RUnlock()
		return errors.New("not connected")
	}

	subs, ok := m.Subscribers[topic]
	m.RUnlock()
	if !ok {
		return nil
	}

	var v interface{}
	if m.config.Codec != nil {
		buf, err := m.config.Codec.Encode(msg)
		if err != nil {
			return err
		}
		v = buf
	} else {
		v = msg
	}

	p := &memoryEvent{
		topic:   topic,
		message: v,
		config:  *m.config,
	}

	for _, sub := range subs {
		if err := sub.handler(p); err != nil {
			p.err = err
			if eh := m.config.ErrorHandler; eh != nil {
				eh(p)
				continue
			}
			return err
		}
	}

	return nil
}

func (m *memoryBroker) Subscribe(topic string, handler broker.Handler, opts ...broker.SubscribeOption) (broker.ISubscriber, error) {
	m.RLock()
	if !m.connected {
		m.RUnlock()
		return nil, errors.New("not connected")
	}
	m.RUnlock()

	var options broker.SubscribeConfig
	for _, o := range opts {
		o(&options)
	}

	sub := &memorySubscriber{
		exit:    make(chan bool, 1),
		id:      uuid.New().String(),
		topic:   topic,
		handler: handler,
		opts:    &options,
	}

	m.Lock()
	m.Subscribers[topic] = append(m.Subscribers[topic], sub)
	m.Unlock()

	go func() {
		<-sub.exit
		m.Lock()
		var newSubscribers []*memorySubscriber
		for _, sb := range m.Subscribers[topic] {
			if sb.id == sub.id {
				continue
			}
			newSubscribers = append(newSubscribers, sb)
		}
		m.Subscribers[topic] = newSubscribers
		m.Unlock()
	}()

	return sub, nil
}

func (m *memoryBroker) String() string {
	return "memory"
}

func (m *memoryEvent) Topic() string {
	return m.topic
}

func (m *memoryEvent) Message() *broker.Message {
	switch v := m.message.(type) {
	case *broker.Message:
		return v
	case []byte:
		msg := &broker.Message{}
		if err := m.config.Codec.Decode(v, msg); err != nil {
			log.Errf("[memory]: failed to unmarshal: %v\n", err)
			return nil
		}
		return msg
	}

	return nil
}

func (m *memoryEvent) Ack() error {
	return nil
}

func (m *memoryEvent) Error() error {
	return m.err
}

func (m *memorySubscriber) Config() *broker.SubscribeConfig {
	return m.opts
}

func (m *memorySubscriber) Topic() string {
	return m.topic
}

func (m *memorySubscriber) Unsubscribe() error {
	m.exit <- true
	return nil
}
