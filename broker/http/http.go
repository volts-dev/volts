package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/codec"
	maddr "github.com/volts-dev/volts/internal/addr"
	igoroutine "github.com/volts-dev/volts/internal/goroutine"
	vnet "github.com/volts-dev/volts/internal/net"
	vtls "github.com/volts-dev/volts/internal/tls"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/transport"
	"golang.org/x/net/http2"
)

var log = logger.New("broker")

type (
	httpBroker struct {
		sync.RWMutex
		id          string
		address     string
		subscribers map[string][]*httpSubscriber
		running     bool
		exit        chan chan error

		config    *broker.Config
		router    *router.TRouter
		transport transport.ITransport
		started   atomic.Value // marks the serve as started

		_mux     *http.ServeMux
		client   *http.Client
		registry registry.IRegistry

		// offline message inbox

		mtx   sync.RWMutex
		inbox map[string][][]byte
	}
)

var (
	DefaultSubPath   = "/_sub"
	serviceName      = "volts.http.broker"
	registerTTL      = time.Minute
	registerInterval = time.Second * 30
)

func init() {
	broker.Register("http", New)
}

// newTransport 构造 broker 的出站 HTTP transport。
//
// config 为 nil 时使用「开启证书校验 + TLS>=1.2」的安全默认，与 transport/http.go
// 的数据面策略保持一致。此前这里默认 InsecureSkipVerify:true —— 中间人可劫持
// broker 的 Publish 目标并伪造/篡改消息，且调用方无从察觉。要跳过校验必须显式传入
// &tls.Config{InsecureSkipVerify: true}。
func newTransport(config *tls.Config) *http.Transport {
	config = vtls.Secure(config)

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

func New(opts ...broker.Option) broker.IBroker {
	var defaultOpts []broker.Option
	defaultOpts = append(defaultOpts,
		broker.WithName("http"),
		broker.WithCodec(codec.JSON),
		broker.WithRegistry(registry.NewNopRegistry()),
		broker.WithContext(context.TODO()),
	)

	cfg := broker.NewConfig(append(defaultOpts, opts...)...)

	// set address
	addr := broker.DefaultAddress
	if len(cfg.Addrs) > 0 && len(cfg.Addrs[0]) > 0 {
		addr = cfg.Addrs[0]
	}

	b := &httpBroker{
		id:       uuid.New().String(),
		config:   cfg,
		address:  addr,
		registry: cfg.Registry,
		client: &http.Client{
			Transport: newTransport(cfg.TLSConfig),
			Timeout:   30 * time.Second,
		},
		subscribers: make(map[string][]*httpSubscriber),
		exit:        make(chan chan error),
		//mux:         http.NewServeMux(),
		inbox: make(map[string][][]byte),
	}
	b.started.Store(false)

	if b.transport == nil {
		b.transport = transport.Default()
		b.transport.Init(
			transport.WithConfigPrefixName(cfg.String()),
		)
	}

	if b.router == nil {
		b.router = router.New(router.WithConfigPrefixName(cfg.String()))

		// 注册路由
		b.router.Url([]string{"POST"}, DefaultSubPath, b.Handler)
	}

	// specify the message handler
	//b.mux.Handle(DefaultSubPath, b)

	// get optional handlers
	if b.config.Context != nil {
		handlers, ok := b.config.Context.Value("http_handlers").(map[string]http.Handler)
		if ok {
			for pattern, handler := range handlers {
				b.router.Url([]string{"POST"}, pattern, handler)
			}
		}
	}

	return b
}

func (self *httpBroker) String() string {
	return self.config.Name
}

// broadcastVersion 返回「广播」在 registry 里的版本号标记。
//
// 发布端（Publish 里的 switch）和订阅端（Subscribe 写 Service.Version）**必须走
// 同一个取值口**。此前两边各有一份：订阅端写 config.BroadcastVersion（默认
// "broadcast"），发布端却比对包变量 broadcastVersion（"volts.http.broadcast"）。
// 两者永不相等，于是每一条广播订阅都掉进 default 分支被当成 queue group，
// **随机投给一个节点** —— 默认配置下 Publish 从来没有真正广播过，且没有任何报错。
// 回归：TestPublish_BroadcastReachesAllNodes。
func (self *httpBroker) broadcastVersion() string {
	if v := self.config.BroadcastVersion; v != "" {
		return v
	}
	return broker.DefaultBroadcastVersion
}

func (self *httpBroker) Init(opts ...broker.Option) error {
	for _, opt := range opts {
		opt(self.config)
	}

	return nil
}

func (self *httpBroker) Config() *broker.Config {
	return self.config
}

func (self *httpBroker) Address() string {
	self.RLock()
	defer self.RUnlock()
	return self.address
}

func (self *httpBroker) Start() error { // 开始阻塞监听
	if self.started.Load().(bool) {
		return nil
	}

	self.Lock()
	defer self.Unlock()

	/*
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

		go http.Serve(l, self.router)
		go func() {
			self.run(l)
			self.Lock()
			self.config.Addrs = []string{addr}
			self.address = addr
			self.Unlock()
		}()
	*/
	// start listening on the transport

	ts, err := self.transport.Listen(self.address)
	if err != nil {
		return err
	}

	addr := self.address
	self.address = ts.Addr().String()

	exit := make(chan bool)

	// 监听链接
	igoroutine.Go(func() {
		for {
			// listen for connections
			err := ts.Serve(self.router)

			// TODO: listen for messages
			// msg := broker.Exchange(service).Consume()

			select {
			// check if we're supposed to exit
			case <-exit:
				return
			// check the error and backoff
			default:
				if err != nil {
					log.Errf("Accept error: %v", err)

					time.Sleep(time.Second)
					continue
				}
			}

			// no error just exit
			return
		}
	}, func(err error) { log.Err(err.Error()) })

	// mark the server as started
	self.started.Store(true)

	log.Infof("Listening on %s - %s", ts.Addr(), self.transport.String())

	// 监听退出
	igoroutine.Go(func() {
		t := new(time.Ticker)

		// only process if it exists
		if self.config.RegisterInterval > time.Duration(0) {
			// new ticker
			t = time.NewTicker(self.config.RegisterInterval)
		}

		// return error chan
		var ch chan error

	Loop:
		for {
			select {
			// register self on interval
			case <-t.C:
				self.RLock()
				for _, subs := range self.subscribers {
					for _, sub := range subs {
						_ = self.registry.Register(sub.svc, registry.RegisterTTL(self.config.RegisterTTL))
					}
				}
				self.RUnlock()
				// received exit signal
			// wait for exit
			case ch = <-self.exit: // 监听来自self.Stop()信号
				self.RLock()
				for _, subs := range self.subscribers {
					for _, sub := range subs {
						_ = self.registry.Deregister(sub.svc)
					}
				}
				self.RUnlock()

				t.Stop()
				close(exit)
				break Loop
			}
		}

		//self.Lock()
		//swg := self.wg
		//self.Unlock()

		// wait for requests to finish
		//if swg != nil {
		//	swg.Wait()
		//}

		// close transport listener
		cerr := ts.Close()

		// 还原为配置里的地址（Start 时被换成了实际绑定到的那个），好让下一次
		// Start 重新绑定。两件事都要在 ch 之前做完：
		//   1. **必须持锁**。Address() 在 RLock 下读它，而 broker 现在是进程级
		//      共享实例（broker.Use 的裸名缓存），Close 之后仍会被下一台 server
		//      装配时读到 —— 锁外写就是 -race 里那条 WARNING。
		//   2. **必须早于 ch 的握手**。Close() 阻塞在 <-ch，收到就返回；若还原
		//      发生在其后，一次 Close→Start 的重启序列会把新绑定的地址覆盖回
		//      "127.0.0.1:0"，之后 Subscribe 就往 registry 里注册一个 :0 的节点。
		self.Lock()
		self.address = addr
		self.Unlock()

		ch <- cerr
	}, func(err error) { log.Err(err.Error()) })

	// set cache
	//self.r = cache.New(self.config.Registry)

	// set running
	self.running = true
	return nil
}

func (self *httpBroker) Close() error {
	// 先在锁内判定并标记 running=false，随即释放锁再做退出握手：
	// exit 处理 goroutine 在注销订阅时要拿 RLock，若持写锁等待 <-ch
	// 会与之死锁（TestBroker 曾卡死在此，长期被投递失败掩盖）。
	// running=false 同时保证并发/重入 Close 只有一个真正执行握手。
	self.Lock()
	if !self.running {
		self.Unlock()
		return nil
	}
	self.running = false
	self.started.Store(false)
	self.Unlock()

	// stop cache
	rc, ok := self.registry.(registry.IRegistryCacher)
	if ok {
		rc.Stop()
	}

	// exit and return err
	ch := make(chan error)
	self.exit <- ch
	return <-ch
}

func (self *httpBroker) Publish(topic string, message *broker.Message, opts ...broker.PublishOption) error {
	var cfg broker.PublishConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.Codec == nil {
		// 验证解码器
		cfg.Codec = codec.IdentifyCodec(cfg.SerializeType)
		if cfg.Codec == nil { // no codec specified
			cfg.Codec = codec.IdentifyCodec(codec.JSON)
			//return fmt.Errorf("no codec specified") // errors.UnsupportedCodec("volts.client", cf)
		}
	}

	// create the message first
	m := &broker.Message{
		Header: make(map[string]string),
		Body:   message.Body,
	}

	for k, v := range message.Header {
		m.Header[k] = v
	}

	m.Header["v-topic"] = topic

	// encode the message
	body, err := cfg.Codec.Encode(m)
	if err != nil {
		return err
	}

	// save the message
	self.saveMessage(topic, body)

	// now attempt to get the service
	self.RLock()
	s, err := self.registry.GetService(self.config.Name)
	if err != nil {
		self.RUnlock()
		return err
	}
	self.RUnlock()

	pub := func(node *registry.Node, t string, b []byte) error {
		scheme := "http"

		// check if secure is added in metadata
		if node.Metadata["secure"] == "true" {
			scheme = "https"
		}

		vals := url.Values{}
		vals.Add("id", node.Id)

		// 本机地址转回环后再发（与 client/rpc_client、http_client、reverse_proxy
		// 同一约定）：Subscribe 经 maddr.Extract 注册的是物理网卡 IP，macOS 的
		// 网络扩展（VPN/防火墙）会在内核层拦截发往物理接口的本机流量导致投递
		// 挂死——TestBroker 卡在 <-done 即此因。回环不受拦截。
		uri := fmt.Sprintf("%s://%s%s?%s", scheme, maddr.LocalFormat(node.Address), DefaultSubPath, vals.Encode())
		r, err := self.client.Post(uri, "application/json", bytes.NewReader(b))
		if err != nil {
			return err
		}

		// discard response body
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		return nil
	}

	srv := func(s []*registry.Service, b []byte) {
		for _, service := range s {
			var nodes []*registry.Node

			for _, node := range service.Nodes {
				// only use nodes tagged with broker http
				if node.Metadata["broker"] != "http" {
					continue
				}

				// look for nodes for the topic
				if node.Metadata["topic"] != topic {
					continue
				}

				nodes = append(nodes, node)
			}

			// only process if we have nodes
			if len(nodes) == 0 {
				continue
			}

			switch service.Version {
			// broadcast version means broadcast to all nodes
			case self.broadcastVersion():
				var success bool

				// publish to all nodes
				for _, node := range nodes {
					// publish async
					if err := pub(node, topic, b); err == nil {
						success = true
					}
				}

				// save if it failed to publish at least once
				if !success {
					self.saveMessage(topic, b)
				}
			default:
				// select node to publish to
				node := nodes[rand.Int()%len(nodes)]

				// publish async to one node
				if err := pub(node, topic, b); err != nil {
					// if failed save it
					self.saveMessage(topic, b)
				}
			}
		}
	}

	// do the rest async
	igoroutine.Go(func() {
		// get a third of the backlog
		messages := self.getMessage(topic, 8)
		delay := (len(messages) > 1)

		// publish all the messages
		for _, msg := range messages {
			// serialize here
			srv(s, msg)

			// sending a backlog of messages
			if delay {
				time.Sleep(time.Millisecond * 100)
			}
		}
	}, func(err error) { log.Err(err.Error()) })

	return nil
}

func (self *httpBroker) Subscribe(topic string, handler broker.Handler, opts ...broker.SubscribeOption) (broker.ISubscriber, error) {
	// 订阅前必须先监听。未 Start 时 self.address 还是 DefaultAddress（端口 0），
	// 注册进 registry 的就是一个 ":0" 的节点：发布方照常 POST 过去、照常失败，
	// 而 Publish 是 fire-and-forget，两端都不会有任何日志。Start 幂等，已启动直接返回。
	if err := self.Start(); err != nil {
		return nil, err
	}

	var err error
	var host, port string
	scfg := broker.NewSubscribeOptions(opts...)

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
		version = self.broadcastVersion()
	}

	service := &registry.Service{
		Name:    self.config.Name,
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

func (self *httpBroker) subscribe(s *httpSubscriber) error {
	self.Lock()
	defer self.Unlock()

	if err := self.registry.Register(s.svc, registry.RegisterTTL(self.config.RegisterTTL)); err != nil {
		return err
	}

	self.subscribers[s.topic] = append(self.subscribers[s.topic], s)
	return nil
}

func (h *httpBroker) saveMessage(topic string, msg []byte) {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	// get messages
	c := h.inbox[topic]

	// save message
	c = append(c, msg)

	// max length 64
	if len(c) > 64 {
		c = c[:64]
	}

	// save inbox
	h.inbox[topic] = c
}

func (h *httpBroker) getMessage(topic string, num int) [][]byte {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	// get messages
	c, ok := h.inbox[topic]
	if !ok {
		return nil
	}

	// more message than requests
	if len(c) >= num {
		msg := c[:num]
		h.inbox[topic] = c[num:]
		return msg
	}

	// reset inbox
	h.inbox[topic] = nil

	// return all messages
	return c
}
