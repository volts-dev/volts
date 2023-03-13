package volts

import (
	"context"

	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

type (
	Option func(*Config)

	Config struct {
		Name   string
		Client client.IClient
		Server server.IServer
		//Transport transport.ITransport
		//Registry  registry.IRegistry

		// Before and After funcs
		BeforeStart []func() error // TODO 处理服务器启动后的事物
		BeforeStop  []func() error
		AfterStart  []func() error
		AfterStop   []func() error
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
		Signal  bool
		Debug   bool
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Name: "service",
		//Registry: registry.Default(),
	}

	cfg.Init(opts...)
	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}

	if self.Context == nil {
		self.Context = context.Background()
	}
	/* 废弃
	if self.Transport == nil {
		self.Transport = transport.NewHTTPTransport()
	}
	*/
	if self.Server == nil {
		self.Server = server.New(
			server.Name(self.Name),
			server.Transport(
				transport.NewHTTPTransport(transport.WithConfigPrefixName(self.Name)),
			),
		)
	}

	if self.Client == nil {
		self.Client = client.NewRpcClient(
			/*client.WithTransport(self.Transport)*/
			client.WithConfigPrefixName(self.Server.Config().Name),
		)
	}

	if self.Debug {
		//self.Transport.Init(transport.Debug())
		self.Client.Init(client.Debug())
		self.Server.Config().Init(server.Debug())
	}
}

// Name of the service
func Name(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
		if cfg.Server != nil {
			//cfg.Server.Config().Init(server.Name(name))
			cfg.Server.Config().Init(server.WithConfigPrefixName(cfg.Name))
		}
	}
}

// Client to be used for service
// under debug mode the port will keep at 35999
func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

// Client to be used for service
func Client(cli client.IClient) Option {
	return func(cfg *Config) {
		cfg.Client = cli
	}
}

// Server to be used for service
func Server(srv server.IServer) Option {
	return func(cfg *Config) {
		cfg.Server = srv
		cfg.Server.Config().Init(server.WithConfigPrefixName(cfg.Name))
	}
}

// Registry sets the registry for the Server
// and the underlying components
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		//cfg.Registry = r
		// Update Client and Server

		if cfg.Client != nil {
			cfg.Client.Init(client.WithRegistry(r))
		}

		if cfg.Server != nil {
			cfg.Server.Config().Init(server.Registry(r))
		}
		// Update Broker
		//cfg.Broker.Init(broker.Registry(r))
	}
}

// Transport sets the transport for the Server and client
// and the underlying components
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		//cfg.Transport = t

		// Update Client and Server
		if cfg.Client != nil {
			cfg.Client.Init(client.WithTransport(t))
		}

		if cfg.Server != nil {
			cfg.Server.Config().Init(server.Transport(t))
		}

	}
}
