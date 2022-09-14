package router

import (
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
)

const (
	MODULE_DIR   = "module" // # 模块文件夹名称
	STATIC_DIR   = "static"
	TEMPLATE_DIR = "template"
)

type (
	Option func(*Config)

	Config struct {
		*config.Config
		Registry       registry.IRegistry
		RegistryCacher cacher.ICacher // registry cache
		StaticDir      []string       `field:"-"` // the static dir allow to visit
		StaticExt      []string       `field:"-"` // the static file format allow to visit

		// mapping to config file
		Recover         bool
		RecoverHandler  func(IContext)
		PrintRouterTree bool `field:"enabled_print_router_tree"`
		PrintRequest    bool
	}
)

func newConfig(opts ...config.Option) *Config {
	cfg := &Config{
		Config:         config.New(),
		Recover:        true,
		RecoverHandler: recoverHandler,
	}
	cfg.Init(opts...)

	if cfg.Registry == nil {
		cfg.Registry = registry.Default()
		cfg.RegistryCacher = cacher.New(cfg.Registry)
	}

	return cfg
}

func (self *Config) Init(opts ...config.Option) {
	for _, opt := range opts {
		opt(self.Config)
	}

	self.Config.Init(opts...)
}

func (self *Config) Load() error {
	self.Config.UnmarshalField("router", &self)
	return nil
}

func (self *Config) Save() error {
	return self.Config.Save(
		config.WithConfig(self),
	)
}

// Register the service with a TTL
func PrintRoutesTree() config.Option {
	return func(cfg *config.Config) {
		//cfg.PrintRouterTree = true
		cfg.SetValue("print_router_tree", true)
	}
}

// Register the service with a TTL
func PrintRequest() config.Option {
	return func(cfg *config.Config) {
		//cfg.PrintRequest = true
		cfg.SetValue("print_request", true)
	}
}

func Recover(on bool) Option {
	return func(cfg *Config) {
		cfg.Recover = on
	}
}

// Register the service with a TTL
func RecoverHandler(handler func(IContext)) Option {
	return func(cfg *Config) {
		cfg.RecoverHandler = handler
	}
}
