package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"

	//	"errors"
	//	"fmt"
	///	"log"
	"reflect"
	//	"runtime"
	listener "vectors/rpc/server/listener"
	rpc "vectors/rpc/server/listener/rpc"
	//log "github.com/VectorsOrigin/logger"
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

func (k *contextKey) String() string { return "rpcx context value " + k.name }

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
		Listener net.Listener
		listener listener.IListeners
		Router   *TRouter // 路由类
		// BlockCrypt for kcp.BlockCrypt
		options map[string]interface{}

		// TLSConfig for creating tls tcp connection.
		tlsConfig *tls.Config

		address string
		network string
		//dispatcher IDispatcher // dispatcher to invoke, http.DefaultServeMux if nil
		//ln net.Listener
	}
)

// NewServer returns a server.
func NewServer(config ...FConfig) *TServer {
	s := &TServer{
		TModule: *NewModule(),
		Router:  NewRouter(),
		//Plugins: &pluginContainer{},
		//options: make(map[string]interface{}),
	}

	for _, fn := range config {
		fn(s)
	}

	return s
}

func (self *TServer) RegisterModule(obj IModule) {
	self.Router.RegisterModule(obj)
}

// Serve starts and listens RPC requests.
// It is blocked until receiving connectings from clients.
func (self *TServer) Listen(network, address string) (err error) {
	//注册主Route
	self.Router.RegisterModule(self)
	self.Router.init()

	self.address = address
	self.network = strings.ToLower(network)
	//self.Dispatcher = self.Router

	// new a listener
	ln, err := listener.NewListener(self.tlsConfig, self.network, self.address)
	if err != nil {
		return err
	}
	self.Listener = ln
	return self.serve(ln)
}

// TODO 实现同端口不同协议
func (s *TServer) serve(ln net.Listener) error {
	switch s.network {
	case "http": // serve as a http server
		// register dispatcher
		http_srv := &http.Server{Handler: s.Router}
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

// Address returns listened address.
func (self *TServer) Address() net.Addr {
	if self.Listener == nil {
		return nil
	}

	return self.Listener.Addr()
}

// 关闭服务器
func (self *TServer) Close() error {
	return self.listener.Close()
}

func (self *TServer) Shutdown() error {
	return self.listener.Shutdown(nil)
}

func (self *TServer) LoadConfigFile(filepath string) {

}
