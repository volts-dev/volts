package server

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"time"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

type (
	Option        func(*Config)
	HandlerOption func(*HandlerOptions)
	Config        struct {
		Codecs map[string]codec.ICodec
		//Broker broker.Broker
		//Tracer    trace.Tracer
		Registry  registry.IRegistry
		Transport transport.ITransport
		Router    IRouter        // The router for requests
		Logger    logger.ILogger //
		TLSConfig *tls.Config    // TLSConfig specifies tls.Config for secure serving

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context

		Metadata  map[string]string
		Name      string
		Address   string
		Advertise string
		Id        string
		Version   string
		//HdlrWrappers []HandlerWrapper
		//SubWrappers  []SubscriberWrapper

		// RegisterCheck runs a check function before registering the service
		RegisterCheck func(context.Context) error
		// The register expiry time
		RegisterTTL time.Duration
		// The interval on which to register
		RegisterInterval time.Duration

		PrintRouterTree bool `ini:"enabled_print_router_tree"`

		StaticDir []string `ini:"-"` // the static dir allow to visit
		StaticExt []string `ini:"-"` // the static file format allow to visit
	}

	HandlerOptions struct {
		Internal bool
		Metadata map[string]map[string]string
	}
)

const (
	CONFIG_FILE_NAME = "config.ini"
	MODULE_DIR       = "module" // # 模块文件夹名称
	DATA_DIR         = "data"
	STATIC_DIR       = "static"
	TEMPLATE_DIR     = "template"
	CSS_DIR          = "css"
	JS_DIR           = "js"
	IMG_DIR          = "img"
)

var (
	// App settings.
	AppVer      string // #程序版本
	AppName     string // #名称
	AppUrl      string //
	AppSubUrl   string //
	AppPath     string // #程序文件夹
	AppFilePath string // #程序绝对路径
	AppDir      string // # 文件夹名称
)

func init() {
	AppFilePath = utils.AppFilePath()
	AppPath = filepath.Dir(AppFilePath)
	AppDir = filepath.Base(AppPath)
}

func newConfig(opt ...Option) *Config {
	cfg := &Config{
		Codecs:   make(map[string]codec.ICodec),
		Metadata: map[string]string{},
		//RegisterInterval: DefaultRegisterInterval,
		//RegisterTTL:      DefaultRegisterTTL,
	}

	for _, o := range opt {
		o(cfg)
	}

	//if cfg.Broker == nil {
	///	cfg.Broker = broker.DefaultBroker
	//}

	if cfg.Registry == nil {
		cfg.Registry = registry.DefaultRegistry
	}

	if cfg.Transport == nil {
		cfg.Transport = transport.DefaultTransport
	}

	if cfg.RegisterCheck == nil {
		//cfg.RegisterCheck = DefaultRegisterCheck
	}

	if len(cfg.Address) == 0 {
		//cfg.Address = DefaultAddress
	}

	if len(cfg.Name) == 0 {
		//cfg.Name = DefaultName
	}

	if len(cfg.Id) == 0 {
		//cfg.Id = DefaultId
	}

	if len(cfg.Version) == 0 {
		//cfg.Version = DefaultVersion
	}

	return cfg
}
