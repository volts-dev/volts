package router

import (
	"strings"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
)

const (
	MODULE_DIR   = "module" // # 模块文件夹名称
	STATIC_DIR   = "static"
	TEMPLATE_DIR = "template"
)

var (
	NameMapper = func(name string) string {
		name = strings.ToLower(name)
		// 为特殊控制器提供固定的接口命名 比如Model.Read被占用的情款下改用Model.Api_Read
		name = strings.Replace(name, "api_", "", 1)
		return name
	}
)

type (
	Option func(*Config)
	Config struct {
		*config.Config `field:"-"`
		Logger         logger.ILogger     `field:"-"` // 实例
		Router         *TRouter           `field:"-"`
		Registry       registry.IRegistry `field:"-"`
		RegistryCacher cacher.ICacher     `field:"-"` // registry cache

		// mapping to config file
		RecoverHandler    func(IContext) `field:"-"`
		Recover           bool           `field:"recover"`
		RouterTreePrinter bool           `field:"router_tree_printer"`
		RequestPrinter    bool           `field:"request_printer"`
		StaticDir         []string       `field:"static_dir"` // the static dir allow to visit
		StaticExt         []string       `field:"static_ext"` // the static file format allow to visit
	}

	GroupOption func(*GroupConfig)
	GroupConfig struct {
		Name        string // 当前源代码文件夹名称
		_PrefixPath string
		FilePath    string // 当前源代码文件夹路径
		IsService   bool   // 是否在registry注册为独立的服务
		PathPrefix  string
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Logger:         log,
		Recover:        true,
		RecoverHandler: recoverHandler,
	}

	config.Default().Register(cfg)
	cfg.Init(opts...)

	if cfg.Registry == nil {
		cfg.Registry = registry.Default()
		cfg.RegistryCacher = cacher.New(cfg.Registry)
	}

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

func (self *Config) Save(immed ...bool) error {
	return self.SaveFromModel(self, immed...)
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
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
		cfg.RouterTreePrinter = true
		//cfg.SetValue("print_router_tree", true)
	}
}

// Register the service with a TTL
func WithRequestPrinter() Option {
	return func(cfg *Config) {
		cfg.RequestPrinter = true
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
		cfg.PathPrefix = prefixPath
	}
}

// 独立出来注册为单一微服务在registry里
func WithAloneService(asService bool, servicsName string, prefix ...string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.Name = servicsName
		cfg.IsService = asService
		if len(prefix) > 0 {
			cfg.PathPrefix = prefix[0]
		}
	}
}
