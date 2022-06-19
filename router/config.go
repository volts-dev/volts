package router

import (
	log "github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
)

var logger log.ILogger = log.New("Router")

const (
	MODULE_DIR   = "module" // # 模块文件夹名称
	STATIC_DIR   = "static"
	TEMPLATE_DIR = "template"
)

type (
	Option func(*Config)

	Config struct {
		Registry       registry.IRegistry
		RegistryCacher cacher.ICacher // registry cache
		StaticDir      []string       `ini:"-"` // the static dir allow to visit
		StaticExt      []string       `ini:"-"` // the static file format allow to visit

		RecoverPanic    bool
		RecoverHandler  func(IContext)
		PrintRouterTree bool `ini:"enabled_print_router_tree"`
		PrintRequest    bool
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		RecoverPanic: true,
	}
	cfg.Init(opts...)

	if cfg.Registry == nil {
		cfg.Registry = registry.Default()
		cfg.RegistryCacher = cacher.New(cfg.Registry)
	}

	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}
}

// Register the service with a TTL
func PrintRoutesTree() Option {
	return func(cfg *Config) {
		cfg.PrintRouterTree = true
	}
}

// Register the service with a TTL
func PrintRequest() Option {
	return func(cfg *Config) {
		cfg.PrintRequest = true
	}
}

// Register the service with a TTL
func RecoverHandler(handler func(IContext)) Option {
	return func(cfg *Config) {
		cfg.RecoverHandler = handler
	}
}
