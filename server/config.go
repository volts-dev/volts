package server

import (
	"context"
	"crypto/tls"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/broker"
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
		config.Config `field:"-"`
		Name          string `field:"-"` // config name/path in config file
		PrefixName    string `field:"-"` // config prefix name
		Uid           string `field:"-"` // 实例
		//Tracer    trace.Tracer
		Subscriber broker.ISubscriber   `field:"-"` // Subscribe to service name
		Broker     broker.IBroker       `field:"-"`
		Registry   registry.IRegistry   `field:"-"` // 实例
		Transport  transport.ITransport `field:"-"` // 实例
		Router     vrouter.IRouter      `field:"-"` // 实例 The router for requests
		Logger     logger.ILogger       `field:"-"` // 实例
		TLSConfig  *tls.Config          `field:"-"` // TLSConfig specifies tls.Config for secure serving

		// Other options for implementations of the interface
		// can be stored in a context
		Context       context.Context             `field:"-"`
		Metadata      map[string]string           `field:"-"`
		RegisterCheck func(context.Context) error `field:"-"` // RegisterCheck runs a check function before registering the service

		Address    string
		Advertise  string
		Version    string
		AutoCreate bool //自动创建实例

		// broker
		BrokerType string
		BrokerHost string

		// Registry
		RegistryType     string
		RegistryHost     string
		RegisterTTL      time.Duration `field:"register_ttl"` // The register expiry time
		RegisterInterval time.Duration // The interval on which to register
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

	DefaultAddress          = ":0"
	DefaultName             = "default"
	DefaultVersion          = "latest"
	DefaultRegisterCheck    = func(context.Context) error { return nil }
	DefaultRegisterInterval = time.Second * 30
	DefaultRegisterTTL      = time.Second * 90
	lastStreamResponseError = errors.New("EOS")
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		PrefixName:       "server",
		Name:             DefaultName,
		Uid:              uuid.New().String(),
		Logger:           log,
		Metadata:         map[string]string{},
		Address:          DefaultAddress,
		RegisterInterval: DefaultRegisterInterval,
		RegisterTTL:      DefaultRegisterTTL,
		RegisterCheck:    DefaultRegisterCheck,
		Version:          DefaultVersion,
		AutoCreate:       true,
	}
	cfg.Init(opts...)
	config.Register(cfg)

	// 同过截取配置初始化对象
	if cfg.Broker == nil {
		if bk := broker.Use(cfg.BrokerType, broker.WithConfigPrefixName(cfg.String()), broker.Addrs(cfg.BrokerHost)); bk != nil {
			cfg.Broker = bk
		}
	}

	// 初始化 registry
	if cfg.Registry == nil {
		if reg := registry.Use(cfg.RegistryType, registry.WithConfigPrefixName(cfg.String()), registry.Addrs(cfg.RegistryHost)); reg != nil {
			cfg.Registry = reg
		}
	}

	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, self.Name}, ".")
	}
	return self.Name
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}

	if self.AutoCreate {
		if self.Transport == nil {
			self.Transport = transport.Default()
			self.Transport.Init(
				transport.WithConfigPrefixName(self.String()),
			)
		}
		// if not special router use create new
		if self.Router == nil {
			self.Router = vrouter.New(
				vrouter.WithConfigPrefixName(self.String()),
			)
		}
	}

	if self.Debug {
		self.Transport.Init(transport.Debug())
		self.Router.Config().Init(vrouter.Debug())
		if self.Registry == nil {
			self.Registry.Config().Init(registry.Debug())
		}
	}
}

func (self *Config) Load() error {
	return self.LoadToModel(self)

}

func (self *Config) Save(immed ...bool) error {
	return self.SaveFromModel(self, immed...)
}

// under debug mode the port will keep at 35999
func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
		cfg.Router.Config().RequestPrinter = true
		cfg.Router.Config().RouterTreePrinter = true
		if cfg.Transport.String() == "Http Transport" {
			cfg.Address = ":35999"
		} else {
			cfg.Address = ":45999"
		}
		//...
	}
}

func Logger() logger.ILogger {
	return log
}

// Server name
func Name(name string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.Name = name
		cfg.Register(cfg)

		prefixName := cfg.String()
		if cfg.Broker != nil {
			cfg.Broker.Init(broker.WithConfigPrefixName(prefixName))
		}

		if cfg.Transport != nil {
			cfg.Transport.Init(transport.WithConfigPrefixName(prefixName))
		}

		if cfg.Router != nil {
			cfg.Router.Config().Init(vrouter.WithConfigPrefixName(prefixName))
		}
	}
}

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)

		prefixName := cfg.String()
		if cfg.Broker != nil {
			cfg.Broker.Init(broker.WithConfigPrefixName(prefixName))
		}

		if cfg.Transport != nil {
			cfg.Transport.Init(transport.WithConfigPrefixName(prefixName))
		}

		if cfg.Router != nil {
			cfg.Router.Config().Init(vrouter.WithConfigPrefixName(prefixName))
		}
	}
}

func Broker(bk broker.IBroker) Option {
	return func(cfg *Config) {
		bk.Init(broker.WithConfigPrefixName(cfg.String()))
		cfg.Broker = bk
		cfg.BrokerType = bk.Config().Name
		cfg.BrokerHost = bk.Address() // FIXME
	}
}

// Registry used for discovery
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		r.Init(registry.WithConfigPrefixName(cfg.String()))
		cfg.Registry = r
		cfg.Router.Config().Registry = r
		cfg.Router.Config().RegistryCacher = cacher.New(r)
		cfg.RegistryType = r.Config().Name
		///cfg.RegistryHost     = r.Config(). // FIXME

		// Update Broker
		if cfg.Broker != nil {
			cfg.Broker.Init(broker.Registry(r))
		}
	}
}

// Transport mechanism for communication e.g http, rabbitmq, etc
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		t.Init(transport.WithConfigPrefixName(cfg.String()))
		cfg.Transport = t
	}
}

// not accept other router
func Router(router vrouter.IRouter) Option {
	return func(cfg *Config) {
		if _, ok := router.(*vrouter.TRouter); ok {
			router.Config().Init(vrouter.WithConfigPrefixName(cfg.String()))
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
