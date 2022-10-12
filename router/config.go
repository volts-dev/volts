package router

import (
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
	"github.com/volts-dev/volts/registry/noop"
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
		Logger         logger.ILogger     `field:"-"` // 实例
		Router         *TRouter           `field:"-"`
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

	GroupOption func(*GroupConfig)
	GroupConfig struct {
		Name       string
		PrefixPath string
		FilePath   string // 当前文件夹名称
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Logger:         log,
		Recover:        true,
		RecoverHandler: recoverHandler,
	}

	cfg.Init(opts...)

	if cfg.Registry == nil {
		cfg.Registry = noop.New()
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

func WithPprof() Option {
	return func(cfg *Config) {
		cfg.Router.RegisterGroup(pprofGroup())
	}
}

// Register the service with a TTL
func WithRoutesTreePrinter() Option {
	return func(cfg *Config) {
		cfg.PrintRouterTree = true
		//cfg.SetValue("print_router_tree", true)
	}
}

// Register the service with a TTL
func WithRequestPrinter() Option {
	return func(cfg *Config) {
		cfg.PrintRequest = true
		//cfg.SetValue("print_request", true)
	}
}

func WithRecover(on bool) Option {
	return func(cfg *Config) {
		cfg.Recover = on
	}
}

// Register the service with a TTL
func WithRecoverHandler(handler func(IContext)) Option {
	return func(cfg *Config) {
		cfg.RecoverHandler = handler
	}
}

func WithCurrentModulePath() GroupOption {
	return func(cfg *GroupConfig) {
		cfg.FilePath, cfg.Name = curFilePath(4)
	}
}

func WithGroupName(name string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.Name = name
	}
}

// default url"/abc"
// PrefixPath url "/PrefixPath/abc"
func WithGroupPathPrefix(prefixPath string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.PrefixPath = prefixPath
	}
}
