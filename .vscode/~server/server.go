package server

import (
	"context"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/volts-dev/logger"
	listener "github.com/volts-dev/volts/server/listener"
	rpc "github.com/volts-dev/volts/server/listener/rpc"
)

const (
	// ReaderBuffsize is used for bufio reader.
	ReaderBuffsize = 1024
	// WriterBuffsize is used for bufio writer.
	WriterBuffsize = 1024
)

var (
	// RemoteConnContextKey is a context key. It can be used in
	// services with context.WithValue to access the connection arrived on.
	// The associated value will be of type net.Conn.
	RemoteConnContextKey = &contextKey{"remote-conn"}
	// StartRequestContextKey records the start time
	StartRequestContextKey = &contextKey{"start-parse-request"}
	// StartSendRequestContextKey records the start time
	StartSendRequestContextKey = &contextKey{"start-send-request"}
)

type ()

// contextKey is a value for use with context.WithValue. It's used as
// a pointer so it fits in an interface{} without allocation.
type contextKey struct {
	name string
}

func (k *contextKey) String() string { return "rpc context value " + k.name }

// ContextKey defines key type in context.
type ContextKey string

// ReqMetaDataKey is used to set metatdata in context of requests.
var ReqMetaDataKey = ContextKey("__req_metadata")

// ResMetaDataKey is used to set metatdata in context of responses.
var ResMetaDataKey = ContextKey("__res_metadata")

// Precompute the reflect type for error. Can't use error directly
// because Typeof takes an empty interface value. This is annoying.
var typeOfError = reflect.TypeOf((*error)(nil)).Elem()

// Precompute the reflect type for context.
var typeOfContext = reflect.TypeOf((*context.Context)(nil)).Elem()

type (
	// Server is a simple micro server abstraction
	IServer interface {
		Name() string
		// Initialise options
		Init(...Option) error
		// Retrieve the options
		Config() *Config
		// Register a handler
		//Handle(Handler) error
		// Create a new handler
		//NewHandler(interface{}, ...HandlerOption) Handler
		// Create a new subscriber
		//NewSubscriber(string, interface{}, ...SubscriberOption) Subscriber
		// Register a subscriber
		//Subscribe(Subscriber) error
		// Start the server
		Listen() error
		Close() error
		// Stop the server
		Stop() error
		// Server implementation
		//String() string
	}

	// Server is rpc server that use TCP or UDP.
	TServer struct {
		TModule
		config *Config // 配置类
	}
)

// NewServer returns a server. default is Http server
func NewServer(opts ...Option) *TServer {
	srv := &TServer{
		TModule: *NewModule(),
		config:  NewConfig(),
	}

	srv.config.Router.server = srv

	// init options
	for _, opt := range opts {
		if opt != nil {
			opt(srv.config)
		}
	}

	return srv
}

func (self *TServer) Init(opts ...Option) error {
	// init options
	for _, opt := range opts {
		if opt != nil {
			opt(self.config)
		}
	}
	return nil
}

func (self *TServer) RegisterModule(obj IModule) {
	self.config.Router.RegisterModule(obj)
}

// 注册中间件
// 中间件可以使用在Conntroller，全局Object 上
func (self *TServer) RegisterMiddleware(obj ...IMiddleware) {
	self.config.Router.RegisterMiddleware(obj...)
}

func (self *TServer) Config() *Config {
	return self.config
}

// Serve starts and listens network requests.
// newwork:tcp,http,rpc
// It is blocked until receiving connectings from clients.
func (self *TServer) Listen() (err error) {
	// load config file
	self.config.LoadFromFile(self.config.configFileName)

	// register server routes
	self.config.Router.RegisterModule(self)
	self.config.Router.init()

	// new a listener
	ln, err := listener.NewListener(self.config.tlsConfig, self.config.protocol, self.config.address)
	if err != nil {
		return err
	}

	self.config.logger.Infof("%s listening and serving %s on %s\n", self.config.name, self.config.protocol, self.config.address)
	self.config.ln = ln

	// TODO 实现同端口不同协议 超时，加密
	switch self.config.protocol {
	case "http": // serve as a http server
		// register dispatcher
		http_srv := &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
			//TLSConfig:    s.Config.tlsConfig,
			Handler: self.config.Router,
		}
		self.config.listener = http_srv
		return http_srv.Serve(ln)

	default: // serve as a RPC server
		// register dispatcher
		rpc_srv := &rpc.TServer{Dispatcher: self.config.Router}
		self.config.listener = rpc_srv
		return rpc_srv.Serve(ln)
	}
}

// return the name of server
func (self *TServer) Name() string {
	return self.config.name
}

// Address returns listened address.
func (self *TServer) Address() net.Addr {
	if self.config.ln == nil {
		return nil
	}

	return self.config.ln.Addr()
}

// close the server gracefully
func (self *TServer) Close() error {
	return self.config.listener.Close()
}

// shutdown the server forcedly
func (self *TServer) Stop() error {
	return self.config.listener.Shutdown(nil)
}

func (self *TServer) Logger() logger.ILogger {
	return self.config.logger
}
