package server

import (
	"context"
	"time"

	"github.com/google/uuid"
)

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

	// Router handle serving messages
	IRouter interface {
		String() string
		Handler() interface{} // 连接入口 serveHTTP 等接口实现
	}

	___IModule interface {
		// 返回Module所有Routes 理论上只需被调用一次
		GetRoutes() *TTree
		GetPath() string
		GetFilePath() string
		GetModulePath() string
		GetTemplateVar() map[string]interface{}
	}
)

var (
	DefaultAddress                  = ":0"
	DefaultName                     = "volts.server"
	DefaultVersion                  = "latest"
	DefaultId                       = uuid.New().String()
	DefaultServer           IServer = NewServer()
	DefaultRouter                   = NewRouter()
	DefaultRegisterCheck            = func(context.Context) error { return nil }
	DefaultRegisterInterval         = time.Second * 30
	DefaultRegisterTTL              = time.Second * 90
)
