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
	// Server is rpc server that use TCP or UDP.
	TServer struct {
		TModule
		Config *TConfig // 配置类
	}
)

// NewServer returns a server. default is Http server
func NewServer(opts ...Options) *TServer {
	srv := &TServer{
		TModule: *NewModule(),
		Config:  NewConfig(),
	}

	srv.Config.Router.server = srv

	// init options
	for _, opt := range opts {
		if opt != nil {
			opt(srv.Config)
		}
	}

	return srv
}

func (self *TServer) RegisterModule(obj IModule) {
	self.Config.Router.RegisterModule(obj)
}

// 注册中间件
// 中间件可以使用在Conntroller，全局Object 上
func (self *TServer) RegisterMiddleware(obj ...IMiddleware) {
	self.Config.Router.RegisterMiddleware(obj...)
}

// Serve starts and listens network requests.
// newwork:tcp,http,rpc
// It is blocked until receiving connectings from clients.
func (self *TServer) Listen() (err error) {
	// load config file
	self.Config.LoadFromFile(self.Config.configFileName)

	// register server routes
	self.Config.Router.RegisterModule(self)
	self.Config.Router.init()

	// new a listener
	ln, err := listener.NewListener(self.Config.tlsConfig, self.Config.protocol, self.Config.address)
	if err != nil {
		return err
	}

	self.Config.logger.Infof("%s listening and serving %s on %s\n", self.Config.name, self.Config.protocol, self.Config.address)
	self.Config.ln = ln

	// TODO 实现同端口不同协议 超时，加密
	switch self.Config.protocol {
	case "http": // serve as a http server
		// register dispatcher
		http_srv := &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
			//TLSConfig:    s.Config.tlsConfig,
			Handler: self.Config.Router,
		}
		self.Config.listener = http_srv
		return http_srv.Serve(ln)

	default: // serve as a RPC server
		// register dispatcher
		rpc_srv := &rpc.TServer{Dispatcher: self.Config.Router}
		self.Config.listener = rpc_srv
		return rpc_srv.Serve(ln)
	}
}

// return the name of server
func (self *TServer) Name() string {
	return self.Config.name
}

// Address returns listened address.
func (self *TServer) Address() net.Addr {
	if self.Config.ln == nil {
		return nil
	}

	return self.Config.ln.Addr()
}

// close the server gracefully
func (self *TServer) Close() error {
	return self.Config.listener.Close()
}

// shutdown the server forcedly
func (self *TServer) Shutdown() error {
	return self.Config.listener.Shutdown(nil)
}

func (self *TServer) Logger() logger.ILogger {
	return self.Config.logger
}
