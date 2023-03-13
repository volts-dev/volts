package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/util/addr"
	"github.com/volts-dev/volts/util/backoff"
	"github.com/volts-dev/volts/util/metadata"
	vnet "github.com/volts-dev/volts/util/net"
)

var defaultServer *TServer
var log = logger.New("Server")

var (
	DefaultAddress          = ":0"
	DefaultName             = "server"
	DefaultVersion          = "latest"
	DefaultRegisterCheck    = func(context.Context) error { return nil }
	DefaultRegisterInterval = time.Second * 30
	DefaultRegisterTTL      = time.Second * 90
	lastStreamResponseError = errors.New("EOS")
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
		// Create a new subscriber
		//NewSubscriber(string, interface{}, ...SubscriberOption) Subscriber
		// Register a subscriber
		//Subscribe(Subscriber) error

		Start() error // Start the server
		Stop() error  // Stop the server
		Started() bool
		String() string // Server implementation
	}

	TServer struct {
		//*router
		//sync.RWMutex
		config      *Config
		httpRspPool sync.Pool

		// server status
		started    atomic.Value // marks the serve as started
		registered bool         // used for first registration
		exit       chan chan error
		wg         *sync.WaitGroup // graceful exit
		services   []*registry.Service
	}
)

// new a server for the service node
func New(opts ...Option) *TServer {
	// inite HandlerPool New function
	srv := &TServer{
		config: newConfig(opts...),
		//RWMutex: sync.RWMutex{},
		//started:    atomic.Value,
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

// 注册到服务发现
func (self *TServer) Register() error {
	//self.RLock()
	regSrv := self.services
	config := self.config
	//self.RUnlock()

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
		Uid:      config.Name + "-" + config.Uid,
		Address:  addr,
		Metadata: md,
	}

	node.Metadata["transport"] = config.Transport.String()
	//node.Metadata["broker"] = config.Broker.String()
	node.Metadata["server"] = self.String()
	node.Metadata["registry"] = config.Registry.String()
	node.Metadata["protocol"] = "mucp"

	//self.RLock()
	/* 是否需要
	// Maps are ordered randomly, sort the keys for consistency
	var handlerList []string
	for n, e := range self.handlers {
		// Only advertise non internal handlers
		if !e.Options().Internal {
			handlerList = append(handlerList, n)
		}
	}

	sort.Strings(handlerList)

	var subscriberList []Subscriber
	for e := range self.subscribers {
		// Only advertise non internal subscribers
		if !e.Options().Internal {
			subscriberList = append(subscriberList, e)
		}
	}

	sort.Slice(subscriberList, func(i, j int) bool {
		return subscriberList[i].Topic() > subscriberList[j].Topic()
	})

	endpoints := make([]*registry.Endpoint, 0, len(handlerList)+len(subscriberList))

	for _, n := range handlerList {
		endpoints = append(endpoints, self.handlers[n].Endpoints()...)
	}

	for _, e := range subscriberList {
		endpoints = append(endpoints, e.Endpoints()...)
	}
	*/
	var servics []*registry.Service
	for name, endpoints := range config.Router.Endpoints() {
		if len(endpoints) == 0 {
			continue
		}

		if name == "" {
			name = config.Name // default registry service of this server
		}
		// default registry service of this server
		service := &registry.Service{
			Name:      name,
			Version:   config.Version,
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

	//self.RUnlock()

	if !registered {
		//if log.V(log.InfoLevel, log.DefaultLogger) {
		//	、	log.Infof("Registry [%s] Registering node: %s", config.Registry.String(), node.Uid)
		//	}
	}

	// already registered? don't need to register subscribers
	if registered {
		return nil
	}

	//self.Lock()
	//defer self.Unlock()

	// set what we're advertising
	self.config.Advertise = addr

	// router can exchange messages
	if self.config.Router != nil {
		// subscribe to the topic with own name
		///sub, err := self.config.Broker.Subscribe(config.Name, self.HandleEvent)
		///if err != nil {
		///	return err
		///}

		// save the subscriber
		///self.subscriber = sub
	}
	/*
		// subscribe for all of the subscribers
		for sb := range self.subscribers {
			var opts []broker.SubscribeOption
			if queue := sb.Options().Queue; len(queue) > 0 {
				opts = append(opts, broker.Queue(queue))
			}

			if cx := sb.Options().Context; cx != nil {
				opts = append(opts, broker.SubscribeContext(cx))
			}

			if !sb.Options().AutoAck {
				opts = append(opts, broker.DisableAutoAck())
			}

			sub, err := config.Broker.Subscribe(sb.Topic(), self.HandleEvent, opts...)
			if err != nil {
				return err
			}
			if log.V(log.InfoLevel, log.DefaultLogger) {
				log.Infof("Subscribing to topic: %s", sub.Topic())
			}
			self.subscribers[sb] = []broker.Subscriber{sub}
		}
	*/
	if cacheService {
		self.services = servics
	}
	self.registered = true

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
		Uid:     config.Name + "-" + config.Uid,
		Address: addr,
	}

	service := &registry.Service{
		Name:    config.Name,
		Version: config.Version,
		Nodes:   []*registry.Node{node},
	}

	//if log.V(log.InfoLevel, log.DefaultLogger) {
	log.Infof("Registry [%s] Deregistering node: %s", config.Registry.String(), node.Uid)
	//}
	if err := config.Registry.Deregister(service); err != nil {
		return err
	}

	//self.Lock()
	self.services = nil

	if !self.registered {
		//self.Unlock()
		return nil
	}

	self.registered = false

	/*
		// close the subscriber
		if self.subscriber != nil {
			self.subscriber.Unsubscribe()
			self.subscriber = nil
		}

		for sb, subs := range self.subscribers {
			for _, sub := range subs {
				//if log.V(log.InfoLevel, log.DefaultLogger) {
				//log.Infof("Unsubscribing %s from topic: %s", node.Uid, sub.Topic())
				//}
				sub.Unsubscribe()
			}
			self.subscribers[sb] = nil
		}
	*/
	//self.Unlock()
	return nil
}

func (self *TServer) Start() error {
	if self.started.Load().(bool) {
		return nil
	}

	cfg := self.config
	//config.Register(cfg) // 注册服务器配置
	config.Load() // 加载所有配置

	// 打印
	cfg.Router.PrintRoutes()

	// start listening on the transport
	ts, err := cfg.Transport.Listen(cfg.Address)
	if err != nil {
		return err
	}

	// swap address
	addr := cfg.Address
	cfg.Address = ts.Addr().String()
	/*
		bname := config.Broker.String()

		// connect to the broker
		if err := config.Broker.Connect(); err != nil {
			if log.V(log.ErrorLevel, log.DefaultLogger) {
				log.Errorf("Broker [%s] connect error: %v", bname, err)
			}
			return err
		}

		if log.V(log.InfoLevel, log.DefaultLogger) {
			log.Infof("Broker [%s] Connected to %s", bname, config.Broker.Address())
		}
	*/
	// use RegisterCheck func before register
	if err = self.config.RegisterCheck(self.config.Context); err != nil {
		//if log.V(log.ErrorLevel, log.DefaultLogger) {
		log.Errf("Server %s-%s register check error: %s", cfg.Name, cfg.Uid, err)
		//}
	} else {
		// announce self to the world
		if err = self.Register(); err != nil {
			//if log.V(log.ErrorLevel, log.DefaultLogger) {
			log.Errf("Server %s-%s register error: %s", cfg.Name, cfg.Uid, err)
			//}
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
				if rerr != nil && registered {
					log.Errf("Server %s-%s register check error: %s, deregister it", cfg.Name, cfg.Uid, err)

					// deregister self in case of error
					if err := self.Deregister(); err != nil {
						log.Errf("Server %s-%s deregister error: %s", cfg.Name, cfg.Uid, err)

					}
				} else if rerr != nil && !registered {
					log.Errf("Server %s-%s register check error: %s", cfg.Name, cfg.Uid, err)

					continue
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

		////if log.Lvl(log.LevelError) {
		////	log.Infof("Broker [%s] Disconnected from %s", bname, config.Broker.Address())
		//}
		// disconnect the broker
		//if err := config.Broker.Disconnect(); err != nil {
		//	if log.Lvl(log.LevelError) {
		//		log.Errf("Broker [%s] Disconnect error: %v", bname, err)
		//	}
		//}

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
	return self.config.Transport.String() + "+" + self.config.Router.String() + " Server"
}

func (self *TServer) Config() *Config {
	//self.RLock()
	cfg := self.config
	//self.RUnlock()
	return cfg
}
