package server

import (
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/volts-dev/volts/broker"
	_ "github.com/volts-dev/volts/broker/http"
	_ "github.com/volts-dev/volts/broker/memory"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/internal/addr"
	"github.com/volts-dev/volts/internal/backoff"
	igoroutine "github.com/volts-dev/volts/internal/goroutine"
	"github.com/volts-dev/volts/internal/metadata"
	vnet "github.com/volts-dev/volts/internal/net"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	_ "github.com/volts-dev/volts/registry/mdns"
	_ "github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/router"
)

var (
	defaultServer   *TServer
	defaultServerMu sync.Mutex
	log             = logger.New("Server")
)

type (
	// Server is a simple volts server abstraction
	IServer interface {
		//Init(...Option) error // Initialise options
		Config() *Config // Retrieve the options

		Name() string
		// Register a handler
		//Handle(Handler) error
		// Create a new handler
		//NewHandler(interface{}, ...HandlerOption) Handler

		Start() error // Start the server
		Stop() error  // Stop the server
		Started() bool
		String() string // Server implementation
	}

	TServer struct {
		sync.RWMutex
		config *Config

		// server status
		started    atomic.Bool // marks the serve as started
		registered atomic.Bool // used for first registration
		exit       chan chan error
		wg         *sync.WaitGroup // graceful exit
		services   []*registry.Service
		subscribers map[router.ISubscriber][]broker.ISubscriber

		// registeredAddr 首次注册时**解析出来的**具体地址（如 192.168.1.5:35999），
		// 仅供注销时精确摘除那条注册项。
		//
		// 它不能写回 config.Advertise —— 那是**用户配置**。写回去之后每次续租都会
		// 复用这个具体地址，于是本机 IP 变了（换网、切 VPN、DHCP 续约）注册信息也
		// 永远不会自我纠正：调用方按旧地址去拨一个没人在听的 LAN 地址，在 macOS 上
		// 还会被网络扩展接受后丢弃，表现成 `EOF` 而不是 connection refused，极难定位。
		// 2026-07-28 一天内因此中断两次，每次只能靠重启恢复。
		registeredAddr string
	}
)

// new a server for the service node
func New(opts ...Option) *TServer {
	srv := &TServer{
		config: newConfig(opts...),
		exit:   make(chan chan error),
		wg:     &sync.WaitGroup{},
	}
	return srv
}

// 默认server实例
// NOTE 引入server时defaultServer必须是nil，防止某些场景不需要server实例
func Default(opts ...Option) *TServer {
	// 用互斥锁保护 lazy-init 与后续 opts 注入。原先 sync.Once 只保护首次创建，
	// 后续的 Config().Init(opts...) 与并发 Default() 调用之间无同步 → 数据竞态。
	defaultServerMu.Lock()
	defer defaultServerMu.Unlock()

	if defaultServer == nil {
		defaultServer = New(opts...)
		return defaultServer
	}

	if len(opts) > 0 {
		defaultServer.Config().Init(opts...)
	}
	return defaultServer
}

// setSubscribers 在写锁保护下替换 subscribers map。
// 必须用此 helper —— 直接赋值与 HandleEvent 的 RLock 读会形成
// Go memory model 数据竞态。
func (s *TServer) setSubscribers(m map[router.ISubscriber][]broker.ISubscriber) {
	s.Lock()
	s.subscribers = m
	s.Unlock()
}

func (s *TServer) HandleEvent(e broker.IEvent) error {
	s.RLock()
	subs := s.subscribers
	s.RUnlock()

	topic := e.Topic()
	ctx := &router.TSubscriberContext{Event: e}

	for sb := range subs {
		if sb.Topic() != topic {
			continue
		}
		for _, hd := range sb.Handlers() {
			if err := hd.InvokeSubscriber(ctx); err != nil {
				log.Errf("subscriber handler error on topic %s: %v", topic, err)
			}
		}
	}
	return nil
}

// 注册到服务发现
func (self *TServer) Register() error {
	self.RLock()
	regSrv := self.services
	config := self.config
	self.RUnlock()

	regFunc := func(service *registry.Service) error {
		// create registry options
		regOpts := []registry.Option{registry.RegisterTTL(config.RegisterTTL)}

		var regErr error

		for i := 0; i < 3; i++ {
			// attempt to register
			if err := config.Registry.Register(service, regOpts...); err != nil {
				// set the error
				regErr = err
				// backoff then retry
				time.Sleep(backoff.Do(i + 1))
				continue
			}
			// success so nil error
			regErr = nil
			break
		}

		return regErr
	}

	// have we registered before?
	if len(regSrv) > 0 {
		for _, srv := range regSrv {
			if err := regFunc(srv); err != nil {
				return err
			}
		}

		return nil
	}

	var err error
	var advt, host, port string
	//var cacheService bool

	// check the advertise address first
	// if it exists then use it, otherwise
	// use the address
	if len(config.Advertise) > 0 {
		advt = config.Advertise
	} else {
		advt = config.Address
	}

	if cnt := strings.Count(advt, ":"); cnt >= 1 {
		// ipv6 address in format [host]:port or ipv4 host:port
		host, port, err = net.SplitHostPort(advt)
		if err != nil {
			return err
		}
	} else {
		host = advt
	}

	// cacheService 决定「本次算出的 service 对象能不能一直复用」。只有当运维**显式配了
	// 一个具体 IP** 时才成立 —— 那种情况下地址确实是静态的。
	//
	// **通配地址不算**。监听成功后 cfg.Address 会被改写成 listener 的地址
	// （server.go 里的 `cfg.Address = ts.Addr().String()`），对 `:35999` 这种配置来说
	// 就是 `[::]:35999`，host 取出来是 `::`。而 net.ParseIP("::") 是成功的 —— 于是
	// 通配地址被误判成"字面 IP"，service 被缓存，此后每轮续租都走早返回、复用首次
	// 算出来的那个具体 IP。
	//
	// 后果：本机 IP 一变（换网、切 VPN、DHCP 续约），注册信息**永远不会自我纠正**，
	// 30 秒一轮的周期性重注册形同虚设，只能重启进程。2026-07-28 一天之内因此中断三次，
	// 每次都表现为"所有走 RPC 的端点 500 EOF、纯 HTTP 端点正常"（macOS 上拨一个没人听
	// 的旧 LAN 地址会被网络扩展接受后丢弃，所以是 EOF 而不是 connection refused），
	// 三次里有两次一度被怀疑成代码回归。
	//
	// IsUnspecified 同时覆盖 `::` 与 `0.0.0.0`。
	var cacheService bool
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		cacheService = true
	}

	addr, err := addr.Extract(host)
	if err != nil {
		return err
	}

	// make copy of metadata
	md := metadata.Copy(config.Metadata)

	// mq-rpc(eg. nats) doesn't need the port. its addr is queue name.
	if port != "" {
		addr = vnet.HostPort(addr, port)
	}

	// register service node
	node := &registry.Node{
		Id:       config.Name + "-" + config.Uid,
		Address:  addr,
		Metadata: md,
	}

	node.Metadata["transport"] = config.Transport.String()
	node.Metadata["broker"] = config.Broker.String()
	node.Metadata["server"] = self.String()
	node.Metadata["registry"] = config.Registry.String()
	node.Metadata["protocol"] = config.Transport.Protocol()

	// **按服务名合并后再注册**，不能按 group 直接注册。
	//
	// Endpoints() 是按 *TGroup 指针分组的，而同一个服务名可能对应**多个 group 对象**：
	// vectors 的 module.NewModule 写的是 `TGroup: *router.NewGroup(...)`（值拷贝），
	// 于是原始堆对象与模块内嵌的副本共享同一个 *GroupConfig —— 名字、路径全一样。
	// 随后 WithStatic() 走 `cfg.Group.SetStatic(...)` 把静态路由加到**原始**那个上，
	// 而模块自己的 Url() 加到**副本**上，两边各持一部分路由。
	//
	// 此前按 group 逐个 regFunc，同名服务写的是同一个 etcd 键（服务名 + 节点 id 都相同），
	// **后写的整份覆盖先写的**；再叠加 Go map 迭代顺序随机，于是同一份二进制每次启动
	// 注册出的端点集都可能不同 —— 真栈实测 website 有时 204 条、有时只剩 2 条静态路由。
	//
	// 后果不是"少几条元数据"：网关按对方注册的端点建反向代理路由（见 router.store），
	// 端点丢了代理路由就不建，该服务的 HTTP 流量不再被转发。表现为「前台/预览有时
	// 404、重启又好了」这类最难查的间歇故障。2026-07-28 的 /website/preview 404 即此。
	byName := make(map[string]*registry.Service, 8)
	var order []string // 保持稳定的注册顺序，便于日志比对
	for grp, endpoints := range config.Router.Endpoints() {
		if len(endpoints) == 0 {
			continue
		}

		name := grp.Name()
		if name == "" {
			name = config.Name // default registry service of this server
		}

		svc, ok := byName[name]
		if ok {
			// 同一个服务名对应多个 group 对象。合并逻辑保证端点不丢，但这件事本身
			// 值得知道 —— 它多半意味着某处把 TGroup 按值拷贝了（vectors 的
			// module.NewModule 曾如此，见那里的注释），而共享 GroupConfig 的两份副本
			// 还会在别的地方分叉（各自的 subscribers map、各自的锁）。
			log.Warnf("Registry 服务 %q 由多个 group 提供（本次 +%d 个端点）——"+
				"端点已合并不会丢，但请检查是否有 TGroup 值拷贝", name, len(endpoints))
		}
		if !ok {
			svc = &registry.Service{
				Name:     name,
				Version:  config.Version,
				Metadata: grp.Metadata,
				Nodes:    []*registry.Node{node},
			}
			byName[name] = svc
			order = append(order, name)
		} else if len(svc.Metadata) == 0 {
			// 同名组的 Metadata 通常是同一个 map（共享 GroupConfig），
			// 但先到的那个若为空，用后来者补上。
			svc.Metadata = grp.Metadata
		}
		svc.Endpoints = append(svc.Endpoints, endpoints...)
	}

	var servics []*registry.Service
	for _, name := range order {
		svc := byName[name]
		svc.Endpoints = dedupEndpoints(svc.Endpoints)

		if err := regFunc(svc); err != nil {
			return err
		}
		servics = append(servics, svc)
	}

	registered := self.registered.Load()

	if !registered {
		log.Infof("Registry [%s] Registering node: %s", config.Registry.String(), node.Id)
	}

	// already registered? don't need to register subscribers
	if registered {
		return nil
	}

	// 记下本次实际注册的地址（供注销用）。**不写回 config.Advertise**，
	// 否则续租时会复用旧地址，换网后再也纠正不回来（见字段注释）。
	self.Lock()
	self.registeredAddr = addr
	self.Unlock()

	if cacheService {
		self.Lock()
		self.services = servics
		self.Unlock()
	}

	self.registered.Store(true)

	// 注册订阅
	// Router can exchange messages on broker
	// Subscribe to the topic with its own name
	if self.config.Router != nil {
		sub, err := self.config.Broker.Subscribe(config.Name, self.HandleEvent)
		if err != nil {
			return err
			//		return errors.Wrap(err, "failed to subscribe to service name topic")
		}

		// Save the subscriber
		self.config.Subscriber = sub
	}

	// Subscribe for all of the subscribers.
	// 持写锁保护 subscribers map：Deregister 可能在心跳轮询中并发 range+delete 此 map，
	// 没有锁的话 Go runtime 直接 panic（concurrent map iteration and write）。
	// 锁的范围覆盖整个循环，期间外部 Broker.Subscribe IO 也持锁 —— 接受这点开销，
	// 因为 Register 在生命周期内仅在 Start 和心跳 deregister-after-failure 路径触发。
	self.Lock()
	defer self.Unlock()
	for sb := range self.subscribers {
		var opts []broker.SubscribeOption
		if queue := sb.Config().Queue; len(queue) > 0 {
			opts = append(opts, broker.Queue(queue))
		}

		if ctx := sb.Config().Context; ctx != nil {
			opts = append(opts, broker.SubscribeContext(ctx))
		}

		if !sb.Config().AutoAck {
			opts = append(opts, broker.DisableAutoAck())
		}

		log.Infof("Subscribing to topic: %s", sb.Topic())
		sub, err := self.config.Broker.Subscribe(sb.Topic(), self.HandleEvent, opts...)
		if err != nil {
			return err
			//		return errors.Wrap(err, "failed to resubscribe")
		}

		self.subscribers[sb] = []broker.ISubscriber{sub}
	}

	return nil
}

func (self *TServer) Deregister() error {
	var err error
	var advt, host, port string

	self.RLock()
	config := self.config
	self.RUnlock()

	// check the advertise address first
	// if it exists then use it, otherwise
	// use the address
	// 注销要摘的是**当初注册进去的那条**，所以优先用 registeredAddr；
	// 它为空（还没注册过）时才回退到配置。
	self.RLock()
	regAddr := self.registeredAddr
	self.RUnlock()
	switch {
	case regAddr != "":
		advt = regAddr
	case len(config.Advertise) > 0:
		advt = config.Advertise
	default:
		advt = config.Transport.Config().Addrs
	}

	if cnt := strings.Count(advt, ":"); cnt >= 1 {
		// ipv6 address in format [host]:port or ipv4 host:port
		host, port, err = net.SplitHostPort(advt)
		if err != nil {
			return err
		}
	} else {
		host = advt
	}

	addr, err := addr.Extract(host)
	if err != nil {
		return err
	}

	// mq-rpc(eg. nats) doesn't need the port. its addr is queue name.
	if port != "" {
		addr = vnet.HostPort(addr, port)
	}

	node := &registry.Node{
		Id:      config.Name + "-" + config.Uid,
		Address: addr,
	}

	service := &registry.Service{
		Name:    config.Name,
		Version: config.Version,
		Nodes:   []*registry.Node{node},
	}

	log.Infof("Registry [%s] Deregistering node: %s", config.Registry.String(), node.Id)
	if err := config.Registry.Deregister(service); err != nil {
		return err
	}

	self.Lock()
	self.services = nil
	self.Unlock()

	if !self.registered.Load() {
		return nil
	}

	self.registered.Store(false)

	// 订阅事宜
	// close the subscriber
	if self.config.Subscriber != nil {
		self.config.Subscriber.Unsubscribe()
		self.config.Subscriber = nil
	}

	// 持写锁：与 Register 中的 subscribers map 写入序列化，避免 concurrent
	// map iteration and write panic。
	self.Lock()
	for sb, subs := range self.subscribers {
		for _, sub := range subs {
			log.Infof("Unsubscribing %s from topic: %s", node.Id, sub.Topic())
			if err = sub.Unsubscribe(); err != nil {
				log.Err(err)
			}
		}
		delete(self.subscribers, sb)
	}
	self.Unlock()

	return nil
}

func (self *TServer) Start() error {
	if self.started.Load() {
		return nil
	}

	cfg := self.config
	//config.Register(cfg) // 注册服务器配置
	err := config.Load() // 加载所有配置
	if err != nil {
		return err
	}

	if cfg.Router.Config().UsePprof {
		cfg.Router.RegisterGroup(router.PprofGroup())
	}
	if cfg.Router.Config().UseOpenAPI {
		cfg.Router.RegisterGroup(router.OpenAPIGroup(cfg.Router))
	}
	// 打印
	cfg.Router.PrintRoutes()

	self.setSubscribers(self.config.Router.Config().Router.GetSubscribers())

	// start listening on the transport
	ts, err := cfg.Transport.Listen(cfg.Address) // TODO Transport 无需地址
	if err != nil {
		return err
	}

	cfg.Address = ts.Addr().String()
	bname := cfg.Broker.String()

	// connect to the broker
	if err := cfg.Broker.Start(); err != nil {
		log.Errf("Broker [%s] connect error: %v", bname, err)
		// 回滚：transport 已 Listen，broker 启动失败必须关闭它，否则资源泄漏
		if cerr := ts.Close(); cerr != nil {
			log.Errf("Transport [%s] close after broker failure: %v", cfg.Transport.String(), cerr)
		}
		return err
	}

	// use RegisterCheck func before register
	if err = self.config.RegisterCheck(self.config.Context); err != nil {
		log.Errf("Server %s-%s register check error: %s", cfg.Name, cfg.Uid, err)
	} else {
		// announce self to the world
		if err = self.Register(); err != nil {
			log.Errf("Server %s-%s register error: %s", cfg.Name, cfg.Uid, err)
		}
	}

	exit := make(chan bool)

	// 监听链接
	igoroutine.Go(func() {
		for {
			// listen for connections
			err := ts.Serve(self.config.Router)

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

	log.Infof("Listening on %s - %s", ts.Addr().String(), cfg.Transport.String())

	// 监听退出
	igoroutine.Go(func() {
		var t *time.Ticker
		var tickerC <-chan time.Time
		if cfg.RegisterInterval > 0 {
			t = time.NewTicker(cfg.RegisterInterval)
			defer t.Stop()
			tickerC = t.C
		}

		// return error chan
		var ch chan error

	Loop:
		for {
			select {
			// register self on interval
			case <-tickerC:
				registered := self.registered.Load()
				rerr := cfg.RegisterCheck(self.config.Context)
				if rerr != nil {
					if !registered {
						log.Errf("Server %s-%s register check error: %s", cfg.Name, cfg.Uid, rerr)
						continue
					}

					log.Errf("Server %s-%s register check error: %s, deregister it", cfg.Name, cfg.Uid, rerr)

					// deregister self in case of error
					if err := self.Deregister(); err != nil {
						log.Errf("Server %s-%s deregister error: %s", cfg.Name, cfg.Uid, err)
					}
				}

				if err := self.Register(); err != nil {
					log.Errf("Server %s-%s register error: %s", cfg.Name, cfg.Uid, err)
				}
			// wait for exit
			case ch = <-self.exit: // 监听来自self.Stop()信号
				close(exit)
				break Loop
			}
		}

		var deregErr error
		if self.registered.Load() {
			// deregister self
			if deregErr = self.Deregister(); deregErr != nil {
				log.Errf("Server %s-%s deregister error: %s", cfg.Name, cfg.Uid, deregErr)
			}
		}

		// 停掉路由器后台任务（registry watch/refresh、静态存储清理）。
		// router 的 exit 通道此前无人触发，这些 goroutine 在服务器停止后
		// 仍永久运行并继续轮询 registry。
		if c, ok := cfg.Router.(interface{ Close() }); ok {
			c.Close()
		}

		swg := self.wg

		// wait for requests to finish
		if swg != nil {
			swg.Wait()
		}

		// 关闭传输层并聚合 broker 错误，确保所有关闭错误均被返回
		tsErr := ts.Close()

		log.Infof("Broker [%s] Disconnected from %s", bname, cfg.Broker.Address())
		brokerErr := cfg.Broker.Close()
		if brokerErr != nil {
			log.Errf("Broker [%s] Disconnect error: %v", bname, brokerErr)
		}

		// 聚合全部关闭错误（deregister/transport/broker），任何一步失败都不被吞掉
		ch <- errors.Join(deregErr, tsErr, brokerErr)
	}, func(err error) { log.Err(err.Error()) })

	return nil
}

func (self *TServer) Started() bool {
	return self.started.Load()
}

func (self *TServer) Stop() error {
	// CAS 保证只有一个调用真正执行 Stop —— 其余并发/重入调用立即返回 nil，
	// 不会去抢只读一次的 self.exit 通道而死锁。
	if !self.started.CompareAndSwap(true, false) {
		return nil
	}

	ch := make(chan error)
	self.exit <- ch
	return <-ch
}

func (self *TServer) Name() string {
	return self.config.Name
}

func (self *TServer) String() string {
	return self.config.Router.String() + self.config.Transport.String() + "+" + " Server"
}

func (self *TServer) Config() *Config {
	return self.config
}

// dedupEndpoints 按 **方法+路径** 去重，保留首次出现的顺序。
//
// 同名服务的多个 group 合并后可能带来重复项（同一条路由被两处登记）。注册中心对
// 重复端点没有硬约束，但它会让载荷凭空变大、也让"这个服务到底暴露了什么"难以核对。
//
// **不能按 Endpoint.Name 去重**：`RouteToEndpiont` 在 Name 为空时填的是路径，而
// 同一条路径上不同方法是**两个独立的端点、两个不同的 handler**——
//
//	reg.Url([]string{"GET"},  "/web/login", …WebLoginForm)   // 渲染登录页
//	reg.Url([]string{"POST"}, "/web/login", …WebLoginSubmit) // 提交认证
//
// 按 Name 去重会把其中一个整个丢掉，别的进程于是只拿到半条路由，`store()` 据此建出
// 的代理路由也只有那半边。表现是**登录页打得开、点提交 404**（或反过来，页面本身
// 404），而且 `TTree.Endpoints()` 走的是按方法分桶的 map，**哪一半活下来取决于 map
// 的随机迭代序**——同一份代码换次重启症状就变。2026-08-02 真栈实测：kylin 直连
// `POST /web/login` 是 303，经 websvc / salesvc 转发是 404。
//
// 路径为空的端点（RPC 侧有此形态）回落到 Name。
func dedupEndpoints(eps []*registry.Endpoint) []*registry.Endpoint {
	if len(eps) < 2 {
		return eps
	}
	seen := make(map[string]bool, len(eps))
	out := eps[:0]
	for _, e := range eps {
		if e == nil {
			continue
		}
		key := strings.Join(e.Method, ",") + " " + e.Path
		if e.Path == "" {
			key = e.Name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}
