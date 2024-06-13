package server

import (
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
	"github.com/volts-dev/volts/internal/metadata"
	vnet "github.com/volts-dev/volts/internal/net"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	_ "github.com/volts-dev/volts/registry/mdns"
	_ "github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/router"
)

var defaultServer *TServer
var log = logger.New("Server")

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
		//*router
		sync.RWMutex
		config      *Config
		httpRspPool sync.Pool

		// server status
		started     atomic.Value // marks the serve as started
		registered  bool         // used for first registration
		exit        chan chan error
		wg          *sync.WaitGroup // graceful exit
		services    []*registry.Service
		subscribers map[router.ISubscriber][]broker.ISubscriber
	}
)

// new a server for the service node
func New(opts ...Option) *TServer {
	srv := &TServer{
		config:     newConfig(opts...),
		registered: false,
		exit:       make(chan chan error),
		wg:         &sync.WaitGroup{},
	}
	srv.started.Store(false)
	return srv
}

// 默认server实例
// NOTE 引入server时defaultServer必须是nil，防止某些场景不需要server实例
func Default(opts ...Option) *TServer {
	if defaultServer == nil {
		defaultServer = New(opts...)
	} else {
		defaultServer.Config().Init(opts...)
	}

	return defaultServer
}

func (s *TServer) HandleEvent(e broker.IEvent) error {
	return nil
}

// 注册到服务发现
func (self *TServer) Register() error {
	regSrv := self.services
	config := self.config

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
	var cacheService bool

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

	if ip := net.ParseIP(host); ip != nil {
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
	/*
		// 注册订阅列表
		var subscriberList []ISubscriber

		for e := range self.subscribers {
			// Only advertise non internal subscribers
			if !e.Config().Internal {
				subscriberList = append(subscriberList, e)
			}
		}
		sort.Slice(subscriberList, func(i, j int) bool {
			return subscriberList[i].Topic() > subscriberList[j].Topic()
		})
	*/
	var servics []*registry.Service
	for grp, endpoints := range config.Router.Endpoints() {
		if len(endpoints) == 0 {
			continue
		}

		name := grp.Name()
		if name == "" {
			name = config.Name // default registry service of this server
		}
		/*
			for _, e := range subscriberList {
				endpoints = append(endpoints, e.Endpoints()...)
			}
		*/
		// default registry service of this server
		service := &registry.Service{
			Name:      name,
			Version:   config.Version,
			Metadata:  grp.Metadata,
			Nodes:     []*registry.Node{node},
			Endpoints: endpoints,
		}

		// register the service
		if err := regFunc(service); err != nil {
			return err
		}

		servics = append(servics, service)
	}

	// get registered value
	registered := self.registered

	if !registered {
		log.Infof("Registry [%s] Registering node: %s", config.Registry.String(), node.Id)
	}

	// already registered? don't need to register subscribers
	if registered {
		return nil
	}

	// set what we're advertising
	self.config.Advertise = addr

	if cacheService {
		self.services = servics
	}
	self.registered = true

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

	// Subscribe for all of the subscribers
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

	//self.RLock()
	config := self.config
	//self.RUnlock()

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

	//self.Lock()
	self.services = nil

	if !self.registered {
		return nil
	}

	self.registered = false

	// 订阅事宜
	// close the subscriber
	if self.config.Subscriber != nil {
		self.config.Subscriber.Unsubscribe()
		self.config.Subscriber = nil
	}

	for sb, subs := range self.subscribers {
		for _, sub := range subs {
			log.Infof("Unsubscribing %s from topic: %s", node.Id, sub.Topic())
			if err = sub.Unsubscribe(); err != nil {
				log.Err(err)
			}
		}
		self.subscribers[sb] = nil
	}

	return nil
}

func (self *TServer) Start() error {
	if self.started.Load().(bool) {
		return nil
	}

	cfg := self.config
	//config.Register(cfg) // 注册服务器配置
	err := config.Load() // 加载所有配置
	if err != nil {
		return err
	}

	// 打印
	cfg.Router.PrintRoutes()

	self.subscribers = self.config.Router.Config().Router.GetSubscribers()

	// start listening on the transport
	ts, err := cfg.Transport.Listen(cfg.Address)
	if err != nil {
		return err
	}

	// swap address
	addr := cfg.Address
	cfg.Address = ts.Addr().String()
	bname := cfg.Broker.String()

	// connect to the broker
	if err := cfg.Broker.Start(); err != nil {
		log.Errf("Broker [%s] connect error: %v", bname, err)
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
	go func() {
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
	}()
	// mark the server as started
	self.started.Store(true)

	log.Infof("Listening on %s - %s", ts.Addr(), cfg.Transport.String())

	// 监听退出
	go func() {
		t := new(time.Ticker)

		// only process if it exists
		if cfg.RegisterInterval > time.Duration(0) {
			// new ticker
			t = time.NewTicker(cfg.RegisterInterval)
		}

		// return error chan
		var ch chan error

	Loop:
		for {
			select {
			// register self on interval
			case <-t.C:
				//self.RLock()
				registered := self.registered
				//self.RUnlock()
				rerr := cfg.RegisterCheck(self.config.Context)
				if rerr != nil {
					if !registered {
						log.Errf("Server %s-%s register check error: %s", cfg.Name, cfg.Uid, err)
						continue
					}

					log.Errf("Server %s-%s register check error: %s, deregister it", cfg.Name, cfg.Uid, err)

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
				t.Stop()
				close(exit)
				break Loop
			}
		}

		//self.RLock()
		registered := self.registered
		//self.RUnlock()
		if registered {
			// deregister self
			if err := self.Deregister(); err != nil {
				log.Errf("Server %s-%s deregister error: %s", cfg.Name, cfg.Uid, err)
			}
		}

		//self.Lock()
		swg := self.wg
		//self.Unlock()

		// wait for requests to finish
		if swg != nil {
			swg.Wait()
		}

		// close transport listener
		ch <- ts.Close()

		// disconnect the broker
		log.Infof("Broker [%s] Disconnected from %s", bname, cfg.Broker.Address())
		if err := cfg.Broker.Close(); err != nil {
			log.Errf("Broker [%s] Disconnect error: %v", bname, err)
		}

		// swap back address
		//self.Lock()
		cfg.Address = addr
		//self.Unlock()
	}()

	return nil
}

func (self *TServer) Started() bool {
	return self.started.Load().(bool)
}

func (self *TServer) Stop() error {
	if !self.started.Load().(bool) {
		return nil
	}

	ch := make(chan error)
	self.exit <- ch

	err := <-ch
	self.started.Store(false)
	return err
}

func (self *TServer) Name() string {
	return self.config.Name
}

func (self *TServer) String() string {
	return self.config.Router.String() + self.config.Transport.String() + "+" + " Server"
}

func (self *TServer) Config() *Config {
	//self.RLock()
	cfg := self.config
	//self.RUnlock()
	return cfg
}
