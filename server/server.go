package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts"
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
		//Listener *rpcsrv.TServer
		ln       net.Listener
		listener listener.IListeners
		Router   *TRouter // 路由类
		Config   *TConfig // 配置类
		logger   volts.ILogger

		// BlockCrypt for kcp.BlockCrypt
		options map[string]interface{}

		// TLSConfig for creating tls tcp connection.
		tlsConfig *tls.Config

		name            string // server name
		address         string
		network         string
		config_filename string
	}
)

// NewServer returns a server.
func NewServer(config ...FConfig) *TServer {
	srv := &TServer{
		TModule: *NewModule(),
		Router:  NewRouter(),
		Config:  NewConfig(),
		logger:  logger.NewLogger(""), // TODO 添加配置
		//Plugins: &pluginContainer{},
		//options: make(map[string]interface{}),
	}
	// 传递
	srv.Router.server = srv // 传递服务器指针

	for _, fn := range config {
		if fn != nil {
			fn(srv)
		}
	}

	return srv
}

func (self *TServer) RegisterModule(obj IModule) {
	self.Router.RegisterModule(obj)
}

// 注册中间件
// 中间件可以使用在Conntroller，全局Object 上
func (self *TServer) RegisterMiddleware(obj ...IMiddleware) {
	self.Router.RegisterMiddleware(obj...)

}

// Serve starts and listens network requests.
// newwork:tcp,http,rpc
// It is blocked until receiving connectings from clients.
func (self *TServer) Listen(network string, address ...string) (err error) {
	// 解析地址
	host, port := self.parse_addr(address)

	// 加载配置文件
	self.Config.LoadFromFile(self.config_filename)
	// 确认配置已经被加载加载
	// 配置最终处理
	sec, err := self.Config.GetSection(self.name)
	if err != nil {

		// 存储默认
		sec, err = self.Config.NewSection(self.name)
		if err != nil {
			self.logger.Panicf("creating ini' section faild! Name:%s Error:%s", self.name, err.Error())
		}
		if host != "" {
			self.Config.Host = host
		}
		self.Config.Port = port
		sec.ReflectFrom(self.Config)
	}
	// 映射到服务器配置结构里
	sec.MapTo(self.Config) // 加载
	self.Config.Save()     // 保存文件

	// 显示系统信息
	new_addr := fmt.Sprintf("%s:%d", self.Config.Host, self.Config.Port)
	self.address = new_addr
	self.network = strings.ToLower(network)

	//注册主Route
	self.Router.RegisterModule(self)
	self.Router.init()

	// new a listener
	ln, err := listener.NewListener(self.tlsConfig, self.network, self.address)
	if err != nil {
		return err
	}
	self.ln = ln

	self.logger.Infof("server %s type: %s ", self.name, network)
	self.logger.Infof("server %s address: %s", self.name, new_addr)
	return self.serve(ln)
}

// TODO 实现同端口不同协议 超时，加密
func (s *TServer) serve(ln net.Listener) error {
	switch s.network {
	case "http": // serve as a http server

		// register dispatcher
		http_srv := &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
			//TLSConfig:    s.Config.tlsConfig,
			Handler: s.Router,
		}
		s.listener = http_srv
		return http_srv.Serve(ln)
	default: // serve as a RPC server
		// register dispatcher
		rpc_srv := &rpc.TServer{Dispatcher: s.Router}
		s.listener = rpc_srv
		return rpc_srv.Serve(ln)
	}

	return nil
}

func (self *TServer) parse_addr(addr []string) (host string, port int) {
	// 如果已经配置了端口则不使用
	if len(addr) != 0 {
		lAddrSplitter := strings.Split(addr[0], ":")
		if len(lAddrSplitter) != 2 {
			logger.Err("Address %s of server %s is unavailable!", addr[0], self.name)
		} else {
			host = lAddrSplitter[0]
			port = utils.StrToInt(lAddrSplitter[1])
		}
	}

	return
}

// return the name of server
func (self *TServer) Name() string {
	return self.name
}

// Address returns listened address.
func (self *TServer) Address() net.Addr {
	if self.ln == nil {
		return nil
	}

	return self.ln.Addr()
}

// close the server gracefully
func (self *TServer) Close() error {
	return self.listener.Close()
}

// shutdown the server forcedly
func (self *TServer) Shutdown() error {
	return self.listener.Shutdown(nil)
}

// Set the new logger for server
func (self *TServer) SetLogger(log volts.ILogger) {
	self.logger = log
}

func (self *TServer) Logger() volts.ILogger {
	return self.logger
}

func (self *TServer) LoadConfigFile(filepath string) {
	self.config_filename = filepath
}
