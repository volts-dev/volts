package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
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

const (
	// maxInboxSize 是每个 topic 在发布方内存里保留的重投积压上限。
	//
	// 这份积压只活在发布进程的内存里，进程一退就没了 —— 它是"对端刚好在重启"
	// 这类短暂失败的缓冲，不是持久队列。
	maxInboxSize = 64

	// maxDrainPerPublish 是每次 Publish 顺带重投的积压条数。
	//
	// 积压只在下一次 **同 topic** 的 Publish 时才被带出去重投，没有独立的定时器：
	// 一个此后再无人发布的 topic，它积压的消息就一直躺在那里。
	maxDrainPerPublish = 8
)

// permanentError 标记「对端明确拒绝了这条消息」——重投不会有不同结果。
//
// 区分它的意义在于积压队列：把永久失败也存起来，等于每次 Publish 都先把一批
// 注定失败的旧消息重发一遍（还各带 100ms 的间隔），真正的新消息被排到后面，
// 而失败原因从头到尾不会变。
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func isPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

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
	self.RUnlock()
	if err != nil {
		// 「没有任何订阅者」不是错误，是 pub/sub 的正常状态，各后端在这里却不一致：
		// NopRegistry 回 (nil, nil)，memory/etcd 回 ErrNotFound。照原样往上抛会让
		// 同一段业务代码换个 registry 就开始报"service not found"。统一按空列表处理。
		if !errors.Is(err, registry.ErrNotFound) {
			return err
		}
		s = nil
	}

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

		// 状态码此前完全没看：对端回 400/404/500 与回 200 一样算投递成功。于是
		// 「注册表里的节点已经过期」「报文对端解不开」都表现为消息发出去了、
		// 没人收到、两边日志都干净。
		if r.StatusCode >= 200 && r.StatusCode < 300 {
			return nil
		}
		rerr := fmt.Errorf("投递到 %s 被拒绝: %s", node.Address, r.Status)
		if r.StatusCode >= 400 && r.StatusCode < 500 {
			return &permanentError{err: rerr}
		}
		return rerr
	}

	// srv 把一条消息投给所有匹配 topic 的节点，返回实际尝试投递的节点数。
	// 返回 0 表示这条消息哪儿都没去（没有订阅者）。
	srv := func(s []*registry.Service, b []byte) int {
		var attempted int
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
				var success, retryable bool

				// publish to all nodes
				for _, node := range nodes {
					attempted++
					err := pub(node, topic, b)
					switch {
					case err == nil:
						success = true
					case isPermanent(err):
						log.Warnf("broker: topic %q 广播到 %s 被永久拒绝，丢弃: %v", topic, node.Address, err)
					default:
						retryable = true
						log.Warnf("broker: topic %q 广播到 %s 失败，将重投: %v", topic, node.Address, err)
					}
				}

				// 一个都没成功、且失败是可重试的，才留着下次再发。
				if !success && retryable {
					self.saveMessage(topic, b)
				}
			default:
				// select node to publish to
				node := nodes[rand.Int()%len(nodes)]
				attempted++

				// publish async to one node
				if err := pub(node, topic, b); err != nil {
					if isPermanent(err) {
						log.Warnf("broker: topic %q 投递到 %s 被永久拒绝，丢弃: %v", topic, node.Address, err)
					} else {
						log.Warnf("broker: topic %q 投递到 %s 失败，将重投: %v", topic, node.Address, err)
						// if failed save it
						self.saveMessage(topic, b)
					}
				}
			}
		}
		return attempted
	}

	// do the rest async
	igoroutine.Go(func() {
		// 连同积压一起发。注意这不是"只发刚才那条"：Publish 一进来就 saveMessage，
		// 这里再整批取出，所以新消息和之前投失败的一起排队出去。
		messages := self.getMessage(topic, maxDrainPerPublish)
		delay := (len(messages) > 1)

		// publish all the messages
		for _, msg := range messages {
			// serialize here
			if srv(s, msg) == 0 {
				// 没有任何订阅者节点，消息直接落地。这是 pub/sub 的正常状态
				// （没人订阅就是没人收），但 Publish 早已返回 nil，不留一行的话
				// 症状就只剩"对面没反应"，与"消息发失败了"无从区分。
				log.Dbgf("broker: topic %q 没有任何订阅者节点，消息未投递", topic)
			}

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

// unsubscribe 摘掉一个订阅者，必要时把节点从 registry 注销。
//
// **同一个 topic 的所有订阅共用同一个 node.Id**（topic + "-" + broker uuid），
// 在 registry 里也只有一条记录。所以只有当这个 (topic, version) 上再没有别的
// 订阅者时才能 Deregister —— 否则退订一个 handler 会把整个进程从该 topic 的
// 投递列表里摘掉，同进程其余 handler 一起收不到消息，且没有任何报错。
// 回归：TestUnsubscribe_OneOfTwoKeepsTheOtherAlive。
func (self *httpBroker) unsubscribe(s *httpSubscriber) error {
	self.Lock()

	var kept []*httpSubscriber
	var removed, stillRegistered bool
	for _, sub := range self.subscribers[s.topic] {
		if !removed && sub == s {
			removed = true
			continue
		}
		kept = append(kept, sub)
		// 同 id 同 version 才是同一条 registry 记录（version 由 queue 决定）。
		if sub.id == s.id && sub.svc.Version == s.svc.Version {
			stillRegistered = true
		}
	}

	if !removed {
		// 已经退订过。幂等返回，不重复 Deregister —— server.Stop() 会在
		// 调用方自己退订之后再兜底退一次。
		self.Unlock()
		return nil
	}

	if len(kept) == 0 {
		delete(self.subscribers, s.topic)
	} else {
		self.subscribers[s.topic] = kept
	}
	self.Unlock()

	if stillRegistered {
		return nil
	}

	// 在锁外注销：subscribe 持写锁调 Register，而退出 goroutine 持读锁调
	// Deregister，registry 实现里可能有自己的锁和网络调用，不要叠在本对象的锁里。
	return self.registry.Deregister(s.svc)
}

func (h *httpBroker) saveMessage(topic string, msg []byte) {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	// get messages
	c := h.inbox[topic]

	// save message
	c = append(c, msg)

	// 超限时丢**最旧**的。此前是 c = c[:64]：保住最旧的 64 条，把刚 append 进来的
	// 那条立刻扔掉 —— 于是积压一满，此后每一条新消息都发不出去，而进程还在原地
	// 反复重投一批早已过时的旧消息，且全程没有任何日志。
	//
	// 这里复制一份而不是直接切片，避免 append/重切在同一块底层数组上把已经丢弃的
	// 头部一直留在内存里。
	if len(c) > maxInboxSize {
		c = append(make([][]byte, 0, maxInboxSize), c[len(c)-maxInboxSize:]...)
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
