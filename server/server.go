package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/asim/go-micro/v3/metadata"
	"github.com/google/uuid"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/util/addr"
	"github.com/volts-dev/volts/util/backoff"
	vnet "github.com/volts-dev/volts/util/net"
)

var (
	DefaultAddress         = ":0"
	DefaultName            = "volts.server"
	DefaultVersion         = "latest"
	DefaultUid             = uuid.New().String()
	DefaultServer  IServer = NewServer()

	DefaultRegisterCheck    = func(context.Context) error { return nil }
	DefaultRegisterInterval = time.Second * 30
	DefaultRegisterTTL      = time.Second * 90
)
var lastStreamResponseError = errors.New("EOS")

type (
	// Server is a simple micro server abstraction
	IServer interface {
		Init(...Option) error // Initialise options
		Config() *Config      // Retrieve the options

		Name() string
		// Register a handler
		//Handle(Handler) error
		// Create a new handler
		//NewHandler(interface{}, ...HandlerOption) Handler
		// Create a new subscriber
		//NewSubscriber(string, interface{}, ...SubscriberOption) Subscriber
		// Register a subscriber
		//Subscribe(Subscriber) error

		Start() error   // Start the server
		Stop() error    // Stop the server
		String() string // Server implementation
	}

	___IModule interface {
		// 返回Module所有Routes 理论上只需被调用一次
		//GetRoutes() *TTree
		GetPath() string
		GetFilePath() string
		GetModulePath() string
		GetTemplateVar() map[string]interface{}
	}

	TServer struct {
		//*router
		sync.RWMutex
		config      *Config
		httpRspPool sync.Pool

		// server status
		started    bool // marks the serve as started
		registered bool // used for first registration
		exit       chan chan error
		wg         *sync.WaitGroup // graceful exit
		rsvc       *registry.Service
	}
)

// new a server for the service node
func NewServer(opts ...Option) *TServer {
	cfg := newConfig(opts...)

	// if not special router use the default
	if cfg.Router == nil {
		cfg.Router = router.DefaultRouter
	}
	//router.hdlrWrappers = options.HdlrWrappers
	//router.subWrappers = options.SubWrappers
	// inite HandlerPool New function

	srv := &TServer{
		config: cfg,
		//router:  cfg.Router.(*router),
		RWMutex: sync.RWMutex{},
		//handlers:   map[string]Handler{},
		started:    false,
		registered: false,
		exit:       make(chan chan error),
		wg:         &sync.WaitGroup{},
	}

	//cfg.Router.(*router).server = srv // 传递服务器指针

	//srv.httpRspPool.New = func() interface{} {
	//	return &httpResponse{}
	//}

	return srv
}

func (self *TServer) Init(opts ...Option) error {
	self.Lock()
	defer self.Unlock()

	for _, opt := range opts {
		opt(self.config)
	}

	return nil
}

// 注册到服务发现
func (self *TServer) Register() error {
	self.RLock()
	rsvc := self.rsvc
	config := self.config
	self.RUnlock()

	regFunc := func(service *registry.Service) error {
		// create registry options
		rOpts := []registry.Option{registry.RegisterTTL(config.RegisterTTL)}

		var regErr error

		for i := 0; i < 3; i++ {
			// attempt to register
			if err := config.Registry.Register(service, rOpts...); err != nil {
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
	if rsvc != nil {
		if err := regFunc(rsvc); err != nil {
			return err
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

	// register service
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

	self.RLock()
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
	// default registry service of this server
	service := &registry.Service{
		Name:      config.Name,
		Version:   config.Version,
		Nodes:     []*registry.Node{node},
		Endpoints: config.Router.Endpoints(),
	}

	// get registered value
	registered := self.registered

	self.RUnlock()

	if !registered {
		//if logger.V(logger.InfoLevel, logger.DefaultLogger) {
		//	、	logger.Infof("Registry [%s] Registering node: %s", config.Registry.String(), node.Uid)
		//	}
	}

	// register the service
	if err := regFunc(service); err != nil {
		return err
	}

	// already registered? don't need to register subscribers
	if registered {
		return nil
	}

	self.Lock()
	defer self.Unlock()

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
			if logger.V(logger.InfoLevel, logger.DefaultLogger) {
				log.Infof("Subscribing to topic: %s", sub.Topic())
			}
			self.subscribers[sb] = []broker.Subscriber{sub}
		}
	*/
	if cacheService {
		self.rsvc = service
	}
	self.registered = true

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

	//if logger.V(logger.InfoLevel, logger.DefaultLogger) {
	logger.Infof("Registry [%s] Deregistering node: %s", config.Registry.String(), node.Uid)
	//}
	if err := config.Registry.Deregister(service); err != nil {
		return err
	}

	self.Lock()
	self.rsvc = nil

	if !self.registered {
		self.Unlock()
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
				//if logger.V(logger.InfoLevel, logger.DefaultLogger) {
				//log.Infof("Unsubscribing %s from topic: %s", node.Uid, sub.Topic())
				//}
				sub.Unsubscribe()
			}
			self.subscribers[sb] = nil
		}
	*/
	self.Unlock()
	return nil

}

// serve connection
func (self *TServer) serve() error {
	self.RLock()
	if self.started {
		self.RUnlock()
		return nil
	}
	self.RUnlock()

	config := self.config

	if config.Router.Config().PrintRouterTree {
		config.Router.PrintRoutes()
	}

	// start listening on the transport
	ts, err := config.Transport.Listen(config.Address)
	if err != nil {
		return err
	}

	logger.Infof("Transport [%s] Listening on %s", config.Transport.String(), ts.Addr())

	// swap address
	self.Lock()
	addr := config.Address
	config.Address = ts.Addr().String()
	self.Unlock()
	/*
		bname := config.Broker.String()

		// connect to the broker
		if err := config.Broker.Connect(); err != nil {
			if logger.V(logger.ErrorLevel, logger.DefaultLogger) {
				logger.Errorf("Broker [%s] connect error: %v", bname, err)
			}
			return err
		}

		if logger.V(logger.InfoLevel, logger.DefaultLogger) {
			logger.Infof("Broker [%s] Connected to %s", bname, config.Broker.Address())
		}
	*/
	// use RegisterCheck func before register
	if err = self.config.RegisterCheck(self.config.Context); err != nil {
		//if logger.V(logger.ErrorLevel, logger.DefaultLogger) {
		logger.Errf("Server %s-%s register check error: %s", config.Name, config.Uid, err)
		//}
	} else {
		// announce self to the world
		if err = self.Register(); err != nil {
			//if logger.V(logger.ErrorLevel, logger.DefaultLogger) {
			logger.Errf("Server %s-%s register error: %s", config.Name, config.Uid, err)
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
					logger.Errf("Accept error: %v", err)

					time.Sleep(time.Second)
					continue
				}
			}

			// no error just exit
			return
		}
	}()

	// 监听退出
	go func() {
		t := new(time.Ticker)

		// only process if it exists
		if config.RegisterInterval > time.Duration(0) {
			// new ticker
			t = time.NewTicker(config.RegisterInterval)
		}

		// return error chan
		var ch chan error

	Loop:
		for {
			select {
			// register self on interval
			case <-t.C:
				self.RLock()
				registered := self.registered
				self.RUnlock()
				rerr := config.RegisterCheck(self.config.Context)
				if rerr != nil && registered {
					logger.Errf("Server %s-%s register check error: %s, deregister it", config.Name, config.Uid, err)

					// deregister self in case of error
					if err := self.Deregister(); err != nil {
						logger.Errf("Server %s-%s deregister error: %s", config.Name, config.Uid, err)

					}
				} else if rerr != nil && !registered {
					logger.Errf("Server %s-%s register check error: %s", config.Name, config.Uid, err)

					continue
				}

				if err := self.Register(); err != nil {
					logger.Errf("Server %s-%s register error: %s", config.Name, config.Uid, err)

				}
			// wait for exit
			case ch = <-self.exit: // 监听来自self.Stop()信号
				t.Stop()
				close(exit)
				break Loop
			}
		}

		self.RLock()
		registered := self.registered
		self.RUnlock()
		if registered {
			// deregister self
			if err := self.Deregister(); err != nil {
				logger.Errf("Server %s-%s deregister error: %s", config.Name, config.Uid, err)

			}
		}

		self.Lock()
		swg := self.wg
		self.Unlock()

		// wait for requests to finish
		if swg != nil {
			swg.Wait()
		}

		// close transport listener
		ch <- ts.Close()

		////if logger.Lvl(logger.LevelError) {
		////	logger.Infof("Broker [%s] Disconnected from %s", bname, config.Broker.Address())
		//}
		// disconnect the broker
		//if err := config.Broker.Disconnect(); err != nil {
		//	if logger.Lvl(logger.LevelError) {
		//		logger.Errf("Broker [%s] Disconnect error: %v", bname, err)
		//	}
		//}

		// swap back address
		self.Lock()
		config.Address = addr
		self.Unlock()
	}()

	// mark the server as started
	self.Lock()
	self.started = true
	self.Unlock()

	return nil
}

func (self *TServer) Start() error {
	return self.serve()
}

func (self *TServer) Stop() error {
	self.RLock()
	if !self.started {
		self.RUnlock()
		return nil
	}
	self.RUnlock()

	ch := make(chan error)
	self.exit <- ch

	err := <-ch
	self.Lock()
	self.started = false
	self.Unlock()

	return err
}

func (self *TServer) Name() string {
	return self.config.Name
}

func (self *TServer) String() string {
	return self.config.Transport.String() + "+" + self.config.Router.String() + " Server"
}

func (self *TServer) Config() *Config {
	self.RLock()
	cfg := self.config
	self.RUnlock()
	return cfg
}
