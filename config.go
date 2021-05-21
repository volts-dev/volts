package volts

import (
	"context"

	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/server"
)

type (
	Option func(*Config) error
	Config struct {
		Client   client.IClient
		Server   server.IServer
		Registry registry.IRegistry

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

	return cfg
}

// Name of the service
func Name(name string) Option {
	return func(cfg *Config) error {
		cfg.Server.Init(server.Name(name))
		return nil
	}
}

// Client to be used for service
func Client(cli client.IClient) Option {
	return func(cfg *Config) error {
		cfg.Client = cli
		return nil
	}
}

// Server to be used for service
func Server(srv server.IServer) Option {
	return func(cfg *Config) error {
		cfg.Server = srv
		return nil
	}
}

// Registry sets the registry for the service
// and the underlying components
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) error {
		cfg.Registry = r
		// Update Client and Server
		//cfg.Client.Init(client.Registry(r))
		//cfg.Server.Init(server.Registry(r))
		// Update Broker
		//cfg.Broker.Init(broker.Registry(r))
		return nil
	}
}
