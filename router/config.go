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

var (
	NameMapper = func(name string) string { return name }
)

type (
	Option func(*Config)

	Config struct {
		*config.Config `field:"-"`
		Registry       registry.IRegistry `field:"-"`
		RegistryCacher cacher.ICacher     `field:"-"` // registry cache
		StaticDir      []string           `field:"-"` // the static dir allow to visit
		StaticExt      []string           `field:"-"` // the static file format allow to visit

		// mapping to config file
		RecoverHandler  func(IContext) `field:"-"`
		Recover         bool           `field:"recover"`
		PrintRouterTree bool           `field:"enabled_print_router_tree"`
		PrintRequest    bool           `field:"print_request"`
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Recover:        true,
		RecoverHandler: recoverHandler,
	}

	cfg.Init(opts...)

	if cfg.Registry == nil {
		cfg.Registry = registry.Default()
		cfg.RegistryCacher = cacher.New(cfg.Registry)
	}

	config.Default().Register(cfg)
	return cfg
}

func (self *Config) String() string {
	return "router"
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func (self *Config) Load() error {
	return self.LoadToModel(self)
}

func (self *Config) Save() error {
	return self.Config.Save(
		config.WithConfig(self),
	)
}

func WithNameMapper(fn func(string) string) config.Option {
	return func(cfg *config.Config) {
		NameMapper = fn
	}
}

// Register the service with a TTL
func PrintRoutesTree() Option {
	return func(cfg *Config) {
		cfg.PrintRouterTree = true
		//cfg.SetValue("print_router_tree", true)
	}
}

// Register the service with a TTL
func PrintRequest() Option {
	return func(cfg *Config) {
		cfg.PrintRequest = true
		//cfg.SetValue("print_request", true)
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
