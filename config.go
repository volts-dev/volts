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
		Client    client.IClient
		Server    server.IServer
		Registry  registry.IRegistry
		Transport transport.ITransport

		// Before and After funcs
		BeforeStart []func() error
		BeforeStop  []func() error
		AfterStart  []func() error
		AfterStop   []func() error
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
		Signal  bool
	}
)

func NewConfig(opts ...Option) *Config {
	cfg := &Config{}

	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.Transport == nil {
		cfg.Transport = transport.NewHTTPTransport()
	}

	if cfg.Client == nil {
		cfg.Client = client.Default(client.Transport(cfg.Transport))
	}

	if cfg.Server == nil {
		cfg.Server = server.New(server.Transport(cfg.Transport))
	}

	if cfg.Registry == nil {
		cfg.Registry = registry.New()
	}

	if cfg.Context == nil {
		cfg.Context = context.Background()
	}

	return cfg
}

// Name of the service
func Name(name string) Option {
	return func(cfg *Config) {
		cfg.Server.Init(server.Name(name))
	}
}

// Client to be used for service
func Debug() Option {
	return func(cfg *Config) {
		srvCfg := cfg.Server.Config()
		srvCfg.Router.Config().PrintRequest = true
		srvCfg.Router.Config().PrintRouterTree = true
		srvCfg.Address = ":35999"
		//...
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
		cfg.Registry = r
		// Update Client and Server

		if cfg.Client != nil {
			cfg.Client.Init(client.Registry(r))
		}

		if cfg.Server != nil {
			cfg.Server.Init(server.Registry(r))
		}
		// Update Broker
		//cfg.Broker.Init(broker.Registry(r))
	}
}

// Transport sets the transport for the Server and client
// and the underlying components
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		cfg.Transport = t

		// Update Client and Server
		if cfg.Client != nil {
			cfg.Client.Init(client.Transport(t))
		}

		if cfg.Server != nil {
			cfg.Server.Init(server.Transport(t))
		}

	}
}
