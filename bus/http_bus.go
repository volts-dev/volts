package bus

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	maddr "github.com/asim/go-micro/v3/util/addr"
	vnet "github.com/volts-dev/volts/util/net"
	vtls "github.com/volts-dev/volts/util/tls"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/registry"
	"golang.org/x/net/http2"
)

type (
	httpBus struct {
		sync.RWMutex
		id          string
		address     string
		subscribers map[string][]*httpSubscriber
		running     bool
		exit        chan chan error

		mux    *http.ServeMux
		config *Config
		c      *http.Client
		r      registry.IRegistry

		// offline message inbox

		mtx   sync.RWMutex
		inbox map[string][][]byte
	}

	httpSubscriber struct {
		opts  *SubscribeConfig
		id    string
		topic string
		fn    Handler
		svc   *registry.Service
		hb    *httpBus
	}
)

func newTransport(config *tls.Config) *http.Transport {
	if config == nil {
		config = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	dialTLS := func(network string, addr string) (net.Conn, error) {
		return tls.Dial(network, addr, config)
	}

	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		DialTLS:             dialTLS,
	}
	runtime.SetFinalizer(&t, func(tr **http.Transport) {
		(*tr).CloseIdleConnections()
	})

	// setup http2
	http2.ConfigureTransport(t)

	return t
}

func NewHttpBus(opts ...Option) *httpBus {
	cfg := &Config{
		Codec:    codec.IdentifyCodec(codec.JSON),
		Registry: registry.DefaultRegistry,
		Context:  context.TODO(),
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// set address
	addr := DefaultAddress
	if len(cfg.Addrs) > 0 && len(cfg.Addrs[0]) > 0 {
		addr = cfg.Addrs[0]
	}

	bus := &httpBus{
		id:          uuid.New().String(),
		config:      cfg,
		address:     addr,
		r:           cfg.Registry,
		c:           &http.Client{Transport: newTransport(cfg.TLSConfig)},
		subscribers: make(map[string][]*httpSubscriber),
		exit:        make(chan chan error),
		mux:         http.NewServeMux(),
		inbox:       make(map[string][][]byte),
	}

	return bus
}

func (self *httpBus) String() string {
	return "http bus"
}

func (self *httpBus) Init(opts ...Option) error {
	for _, opt := range opts {
		opt(self.config)
	}

	return nil
}

func (self *httpBus) Config() *Config {
	return self.config
}

func (self *httpBus) Address() string {
	self.RLock()
	defer self.RUnlock()
	return self.address
}

func (self *httpBus) Listen() error { // 开始阻塞监听
	self.RLock()
	if self.running {
		self.RUnlock()
		return nil
	}
	self.RUnlock()

	self.Lock()
	defer self.Unlock()

	var l net.Listener
	var err error

	if self.config.Secure || self.config.TLSConfig != nil {
		config := self.config.TLSConfig

		fn := func(addr string) (net.Listener, error) {
			if config == nil {
				hosts := []string{addr}

				// check if its a valid host:port
				if host, _, err := net.SplitHostPort(addr); err == nil {
					if len(host) == 0 {
						hosts = maddr.IPs()
					} else {
						hosts = []string{host}
					}
				}

				// generate a certificate
				cert, err := vtls.Certificate(hosts...)
				if err != nil {
					return nil, err
				}
				config = &tls.Config{Certificates: []tls.Certificate{cert}}
			}
			return tls.Listen("tcp", addr, config)
		}

		l, err = vnet.Listen(self.address, fn)
	} else {
		fn := func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		}

		l, err = vnet.Listen(self.address, fn)
	}

	if err != nil {
		return err
	}

	addr := self.address
	self.address = l.Addr().String()

	go http.Serve(l, self.mux)
	go func() {
		self.run(l)
		self.Lock()
		self.config.Addrs = []string{addr}
		self.address = addr
		self.Unlock()
	}()

	// get registry
	reg := self.config.Registry
	if reg == nil {
		reg = registry.DefaultRegistry
	}
	// set cache
	//self.r = cache.New(reg)

	// set running
	self.running = true
	return nil
}

func (self *httpBus) run(l net.Listener) {
	t := time.NewTicker(registerInterval)
	defer t.Stop()

	for {
		select {
		// heartbeat for each subscriber
		case <-t.C:
			self.RLock()
			for _, subs := range self.subscribers {
				for _, sub := range subs {
					_ = self.r.Register(sub.svc, registry.RegisterTTL(registerTTL))
				}
			}
			self.RUnlock()
		// received exit signal
		case ch := <-self.exit:
			ch <- l.Close()
			self.RLock()
			for _, subs := range self.subscribers {
				for _, sub := range subs {
					_ = self.r.Deregister(sub.svc)
				}
			}
			self.RUnlock()
			return
		}
	}
}

func (self *httpBus) Close() error {
	var err error
	return err
}

func (self *httpBus) Publish(topic string, m *TMessage, opts ...Option) error {
	var err error
	return err
}

func (self *httpBus) Subscribe(topic string, handler Handler, opts ...SubscribeOption) (ISubscriber, error) {
	var err error
	var host, port string
	scfg := NewSubscribeOptions(opts...)

	// parse address for host, port
	host, port, err = net.SplitHostPort(self.Address())
	if err != nil {
		return nil, err
	}

	addr, err := maddr.Extract(host)
	if err != nil {
		return nil, err
	}

	var secure bool

	if self.config.Secure || self.config.TLSConfig != nil {
		secure = true
	}

	// register service
	node := &registry.Node{
		Id:      topic + "-" + self.id,
		Address: vnet.HostPort(addr, port),
		Metadata: map[string]string{
			"secure": fmt.Sprintf("%t", secure),
			"broker": "http",
			"topic":  topic,
		},
	}

	// check for queue group or broadcast queue
	version := scfg.Queue
	if len(version) == 0 {
		version = broadcastVersion
	}

	service := &registry.Service{
		Name:    serviceName,
		Version: version,
		Nodes:   []*registry.Node{node},
	}

	// generate subscriber
	subscriber := &httpSubscriber{
		opts:  scfg,
		hb:    self,
		id:    node.Id,
		topic: topic,
		fn:    handler,
		svc:   service,
	}

	// subscribe now
	if err := self.subscribe(subscriber); err != nil {
		return nil, err
	}

	// return the subscriber
	return subscriber, nil
}

func (self *httpBus) subscribe(s *httpSubscriber) error {
	self.Lock()
	defer self.Unlock()

	if err := self.r.Register(s.svc, registry.RegisterTTL(registerTTL)); err != nil {
		return err
	}

	self.subscribers[s.topic] = append(self.subscribers[s.topic], s)
	return nil
}

func (self *httpSubscriber) Config() *SubscribeConfig {
	return nil
}

func (self *httpSubscriber) Topic() string {
	return ""
}

func (self *httpSubscriber) Unsubscribe() error {
	return nil
}
