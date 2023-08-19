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
		Client client.IClient
		Server server.IServer

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
	cfg := &Config{}
	cfg.Init(opts...)

	if cfg.Context == nil {
		cfg.Context = context.Background()
	}

	if cfg.Server == nil {
		cfg.Server = server.New(
			//server.Name(self.Name),
			server.WithTransport(
				transport.NewHTTPTransport(),
			),
		)
	}

	if cfg.Client == nil {
		cfg.Client = client.NewRpcClient(
			/*client.WithTransport(self.Transport)*/
			client.WithConfigPrefixName(cfg.Server.Config().Name),
		)
	}
	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}

	if self.Debug {
		//self.Transport.Init(transport.Debug())
		if self.Client != nil {
			self.Client.Init(client.Debug())
		}
		if self.Server != nil {
			self.Server.Config().Init(server.Debug())
		}
	}
}

// Name of the service
func Name(name string) Option {
	return func(cfg *Config) {
		if cfg.Server != nil {
			cfg.Server.Config().Init(server.Name(name))
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
	}
}

// Registry sets the registry for the Server
// and the underlying components
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		// Update Client and Server
		if cfg.Client != nil {
			cfg.Client.Init(client.WithRegistry(r))
		}

		if cfg.Server != nil {
			cfg.Server.Config().Init(server.WithRegistry(r))
		}
	}
}

// Transport sets the transport for the Server and client
// and the underlying components
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		// Update Client and Server
		if cfg.Client != nil {
			cfg.Client.Init(client.WithTransport(t))
		}

		if cfg.Server != nil {
			cfg.Server.Config().Init(server.WithTransport(t))
		}

	}
}
