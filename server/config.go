package server

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	log "github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/bus"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
	vrouter "github.com/volts-dev/volts/router"

	"github.com/volts-dev/volts/transport"
)

var logger log.ILogger = log.NewLogger(log.WithPrefix("Server"))

type (
	Option func(*Config)

	Config struct {
		Codecs map[string]codec.ICodec
		Bus    bus.IBus
		//Tracer    trace.Tracer
		Registry  registry.IRegistry
		Transport transport.ITransport
		Router    vrouter.IRouter // The router for requests
		Logger    log.ILogger     //
		TLSConfig *tls.Config     // TLSConfig specifies tls.Config for secure serving

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context

		Metadata  map[string]string
		Name      string
		Address   string
		Advertise string
		Uid       string
		Version   string
		//HdlrWrappers []HandlerWrapper
		//SubWrappers  []SubscriberWrapper

		// RegisterCheck runs a check function before registering the service
		RegisterCheck func(context.Context) error
		// The register expiry time
		RegisterTTL time.Duration
		// The interval on which to register
		RegisterInterval time.Duration
	}
)

const (
	CONFIG_FILE_NAME = "config.ini"
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
		Uid:              uuid.New().String(),
		Name:             DefaultName,
		Logger:           logger,
		Bus:              bus.DefaultBus,
		Registry:         registry.DefaultRegistry,
		Codecs:           make(map[string]codec.ICodec),
		Metadata:         map[string]string{},
		Address:          DefaultAddress,
		RegisterInterval: DefaultRegisterInterval,
		RegisterTTL:      DefaultRegisterTTL,
		RegisterCheck:    DefaultRegisterCheck,
		Version:          DefaultVersion,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// if not special router use the default
	if cfg.Router == nil {
		cfg.Router = vrouter.NewRouter()
	}

	if cfg.Transport == nil {
		cfg.Transport = transport.NewHTTPTransport()
	}

	return cfg
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

// Codec to use to encode/decode requests for a given content type
func Codec(contentType string, c codec.ICodec) Option {
	return func(cfg *Config) {
		cfg.Codecs[contentType] = c
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
