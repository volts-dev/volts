package server

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/bus"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
	vrouter "github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/transport"
)

type (
	Option func(*Config)

	Config struct {
		*config.Config `field:"-"`
		Bus            bus.IBus `field:"-"` // 实例
		//Tracer    trace.Tracer
		Registry  registry.IRegistry   `field:"-"` // 实例
		Transport transport.ITransport `field:"-"` // 实例
		Router    vrouter.IRouter      `field:"-"` // 实例 The router for requests
		Logger    logger.ILogger       `field:"-"` // 实例
		TLSConfig *tls.Config          `field:"-"` // TLSConfig specifies tls.Config for secure serving

		// Other options for implementations of the interface
		// can be stored in a context
		Context       context.Context             `field:"-"`
		Metadata      map[string]string           `field:"-"`
		RegisterCheck func(context.Context) error `field:"-"` // RegisterCheck runs a check function before registering the service

		Name string
		//  host address ":35999"
		Address          string
		Advertise        string
		Uid              string
		Version          string
		RegisterTTL      time.Duration // The register expiry time
		RegisterInterval time.Duration // The interval on which to register
		AutoCreate       bool          //自动创建实例
	}
)

var (
	// App settings.
	AppVer      string                             // 程序版本
	AppName     string                             // 名称
	AppUrl      string                             //
	AppSubUrl   string                             //
	AppFilePath string = utils.AppFilePath()       // 可执行程序文件绝对路径
	AppPath     string = filepath.Dir(AppFilePath) // 可执行程序所在文件夹绝对路径
	AppDir      string = filepath.Base(AppPath)    // 文件夹名称
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Config:           config.New(config.DEFAULT_PREFIX),
		Uid:              uuid.New().String(),
		Name:             DefaultName,
		Logger:           log,
		Bus:              bus.DefaultBus,
		Registry:         registry.Default(),
		Metadata:         map[string]string{},
		Address:          DefaultAddress,
		RegisterInterval: DefaultRegisterInterval,
		RegisterTTL:      DefaultRegisterTTL,
		RegisterCheck:    DefaultRegisterCheck,
		Version:          DefaultVersion,
		AutoCreate:       true,
	}

	cfg.Init(opts...)
	config.Default().Register(cfg)
	return cfg
}

func (self *Config) String() string {
	return "server"
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}

	if self.Transport == nil {
		if self.AutoCreate {
			self.Transport = transport.NewHTTPTransport()
		} else {
			//log.Warn("Transport is NIL")
		}
	}

	// if not special router use create new
	if self.Router == nil {
		if self.AutoCreate {
			self.Router = vrouter.New()
		} else {
			//log.Warn("Router is NIL")
		}
	}

	if self.Registry == nil {
		if self.AutoCreate {
			self.Registry = registry.Default()
		} else {
			//log.Warn("Registry is NIL")
		}
	}
}

func (self *Config) Load() error {
	return self.LoadToModel(self)
}

func (self *Config) Save() error {
	return nil
}

// under debug mode the port will keep at 35999
func Debug() Option {
	return func(cfg *Config) {
		cfg.Router.Config().PrintRequest = true
		cfg.Router.Config().PrintRouterTree = true
		cfg.Address = ":35999"
		//...
	}
}

// Server name
func Name(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
	}
}

// Registry used for discovery
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		cfg.Registry = r
		cfg.Router.Config().Registry = r
		cfg.Router.Config().RegistryCacher = cacher.New(r)
	}
}

// Transport mechanism for communication e.g http, rabbitmq, etc
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		cfg.Transport = t
	}
}

// not accept other router
func Router(router vrouter.IRouter) Option {
	return func(cfg *Config) {
		if _, ok := router.(*vrouter.TRouter); ok {
			cfg.Router = router
		}
	}
}

// Address to bind to - host:port or :port
func Address(addr string) Option {
	return func(cfg *Config) {
		cfg.Address = addr
	}
}

// Context specifies a context for the service.
// Can be used to signal shutdown of the service
// Can be used for extra option values.
func Context(ctx context.Context) Option {
	return func(cfg *Config) {
		cfg.Context = ctx
	}
}

// RegisterCheck run func before registry service
func RegisterCheck(fn func(context.Context) error) Option {
	return func(cfg *Config) {
		cfg.RegisterCheck = fn
	}
}

// Register the service with a TTL
func RegisterTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterTTL = t
	}
}

// Register the service with at interval
func RegisterInterval(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterInterval = t
	}
}

// TLSConfig specifies a *tls.Config
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) {
		// set the internal tls
		cfg.TLSConfig = t

		// set the default transport if one is not
		// already set. Required for Init call below.
		if cfg.Transport == nil {
			cfg.Transport = transport.NewHTTPTransport()
		}

		// set the transport tls
		cfg.Transport.Init(
			transport.Secure(true),
			transport.TLSConfig(t),
		)
	}
}

// 如果为False服务器不会自动创建默认必要组件实例,开发者自己配置
func AutoCreate(open bool) Option {
	return func(cfg *Config) {
		cfg.AutoCreate = open
	}
}
