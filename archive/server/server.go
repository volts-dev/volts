package server

import (
	"context"
	"crypto/tls"
	"net"

	//	"errors"
	//	"fmt"
	///	"log"
	"reflect"
	//	"runtime"

	rpcsrv "github.com/volts-dev/volts/server/net"
	//log "github.com/volts-dev/logger"
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
		Listener *rpcsrv.TServer
		Router   *TRouter // 路由类
		// BlockCrypt for kcp.BlockCrypt
		options map[string]interface{}

		// TLSConfig for creating tls tcp connection.
		tlsConfig *tls.Config
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

	//self.Listener, err = rpcsrv.ListenAndServe(network, address, self.Router)
	//if err != nil {
	//	log.Panic("qwerqe", err.Error())
	//}
	self.Listener = rpcsrv.NewServer(network, address, self.Router)
	return self.Listener.ListenAndServe()
}

// Address returns listened address.
func (self *TServer) Address() net.Addr {
	if self.Listener == nil {
		return nil
	}

	return self.Listener.Address()
}

// 关闭服务器
func (self *TServer) Close() error {
	return nil
}
