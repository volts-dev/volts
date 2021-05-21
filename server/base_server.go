package server

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/asim/go-micro/v3/metadata"
	"github.com/asim/go-micro/v3/util/addr"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/util/backoff"
	vnet "github.com/volts-dev/volts/util/net"
)

var lastStreamResponseError = errors.New("EOS")

type (

	// Handler interface represents a request handler. It's generated
	// by passing any type of public concrete object with endpoints into server.NewHandler.
	// Most will pass in a struct.
	//
	// Example:
	//
	//      type Greeter struct {}
	//
	//      func (g *Greeter) Hello(context, request, response) error {
	//              return nil
	//      }
	//
	//Handler interface {
	//	Name() string
	//	Handler() interface{}
	//	Endpoints() []*registry.Endpoint
	//	Options() HandlerOptions
	//}

	server struct {
		sync.RWMutex
		config *Config
		//router *router

		//handlers    map[string]Handler
		httpRspPool sync.Pool

		// server status
		started    bool // marks the serve as started
		registered bool // used for first registration
		exit       chan chan error
		wg         *sync.WaitGroup // graceful exit
		rsvc       *registry.Service
	}
)

func newServer(opts ...Option) IServer {
	cfg := newConfig(opts...)

	// if not special router use the default
	if cfg.Router == nil {
		cfg.Router = newRouter()
	}
	//router.hdlrWrappers = options.HdlrWrappers
	//router.subWrappers = options.SubWrappers
	// inite HandlerPool New function

	srv := &server{
		RWMutex: sync.RWMutex{},
		config:  cfg,
		//handlers:   map[string]Handler{},
		started:    false,
		registered: false,
		exit:       make(chan chan error),
		wg:         &sync.WaitGroup{},
	}

	srv.httpRspPool.New = func() interface{} {
		return &httpResponse{}
	}

	return srv
}

func (self *server) Init(opts ...Option) error {
	self.Lock()
	defer self.Unlock()

	for _, opt := range opts {
		opt(self.config)
	}
	// update router if its the default
	if self.config.Router == nil {
		//r := newServer()
		//r.hdlrWrappers = self.config.HdlrWrappers
		//r.serviceMap = self.router.serviceMap
		//r.subWrappers = self.config.SubWrappers
		//self.router = r
	}

	//self.rsvc = nil

	return nil
}

// 注册到服务发现
func (self *server) Register() error {
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
		Id:       config.Name + "-" + config.Id,
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
	service := &registry.Service{
		Name:    config.Name,
		Version: config.Version,
		Nodes:   []*registry.Node{node},
		//Endpoints: endpoints,
	}

	// get registered value
	registered := self.registered

	self.RUnlock()

	if !registered {
		//if logger.V(logger.InfoLevel, logger.DefaultLogger) {
		//	、	logger.Infof("Registry [%s] Registering node: %s", config.Registry.String(), node.Id)
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
func (self *server) Deregister() error {
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
		Id:      config.Name + "-" + config.Id,
		Address: addr,
	}

	service := &registry.Service{
		Name:    config.Name,
		Version: config.Version,
		Nodes:   []*registry.Node{node},
	}

	//if logger.V(logger.InfoLevel, logger.DefaultLogger) {
	//log.Infof("Registry [%s] Deregistering node: %s", config.Registry.String(), node.Id)
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
				//log.Infof("Unsubscribing %s from topic: %s", node.Id, sub.Topic())
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
func (self *server) serve() error {
	config := self.config

	self.RLock()
	if self.started {
		self.RUnlock()
		return nil
	}
	self.RUnlock()

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

		// use RegisterCheck func before register
		if err = s.opts.RegisterCheck(s.opts.Context); err != nil {
			if logger.V(logger.ErrorLevel, logger.DefaultLogger) {
				logger.Errorf("Server %s-%s register check error: %s", config.Name, config.Id, err)
			}
		} else {
			// announce self to the world
			if err = s.Register(); err != nil {
				if logger.V(logger.ErrorLevel, logger.DefaultLogger) {
					logger.Errorf("Server %s-%s register error: %s", config.Name, config.Id, err)
				}
			}
		}
	*/
	exit := make(chan bool)

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
					logger.Errf("Server %s-%s register check error: %s, deregister it", config.Name, config.Id, err)

					// deregister self in case of error
					if err := self.Deregister(); err != nil {
						logger.Errf("Server %s-%s deregister error: %s", config.Name, config.Id, err)

					}
				} else if rerr != nil && !registered {
					logger.Errf("Server %s-%s register check error: %s", config.Name, config.Id, err)

					continue
				}

				if err := self.Register(); err != nil {
					logger.Errf("Server %s-%s register error: %s", config.Name, config.Id, err)

				}
			// wait for exit
			case ch = <-self.exit:
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
				logger.Errf("Server %s-%s deregister error: %s", config.Name, config.Id, err)

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

func (self *server) Start() error {

	return self.serve()
}

func (self *server) Stop() error {
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

func (self *server) Name() string {
	return self.config.Name
}

func (self *server) String() string {
	return "mucp"
}

func (self *server) Config() *Config {
	self.RLock()
	opts := self.config
	self.RUnlock()
	return opts
}
