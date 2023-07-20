package broker

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
)

var log = logger.New("broker")

type (
	Option          func(*Config)
	PublishOption   func(*PublishConfig)
	SubscribeOption func(*SubscribeConfig)

	Config struct {
		config.Config `field:"-"`
		ErrorHandler  Handler            `field:"-"`
		Codec         codec.ICodec       `field:"-"` // Codec
		Registry      registry.IRegistry `field:"-"` // Registry used for clustering
		Context       context.Context    `field:"-"`
		TLSConfig     *tls.Config        `field:"-"`
		PrefixName    string             `field:"-"`
		Name          string             `field:"-"`
		Addrs         []string

		// Handler executed when error happens in broker mesage
		// processing
		Secure bool

		BroadcastVersion string
		RegisterTTL      time.Duration `field:"register_ttl"`
		RegisterInterval time.Duration

		// Other options for implementations of the interface
		// can be stored in a context
	}

	PublishConfig struct {
		Codec         codec.ICodec
		SerializeType codec.SerializeType
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	SubscribeConfig struct {
		// AutoAck defaults to true. When a handler returns
		// with a nil error the message is acked.
		AutoAck bool
		// Subscribers with the same queue name
		// will create a shared subscription where each
		// receives a subset of messages.
		Queue string

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

var (
	DefaultAddress = "127.0.0.1:0"
)

func NewConfig(opts ...Option) *Config {
	cfg := &Config{
		//Config: config.Default(),
		//Name:             "broker",
		BroadcastVersion: "broadcast",
		RegisterTTL:      time.Minute,
		RegisterInterval: time.Second * 30,
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, "broker"}, ".")
	}

	if len(self.Name) > 0 {
		return strings.Join([]string{"broker", self.Name}, ".")
	}

	return ""
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

func Logger() logger.ILogger {
	return log
}

func NewSubscribeOptions(opts ...SubscribeOption) *SubscribeConfig {
	cfg := &SubscribeConfig{
		AutoAck: true,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// Addrs sets the host addresses to be used by the broker.
func Addrs(addrs ...string) Option {
	return func(o *Config) {
		o.Addrs = addrs
	}
}

// DisableAutoAck will disable auto acking of messages
// after they have been handled.
func DisableAutoAck() SubscribeOption {
	return func(o *SubscribeConfig) {
		o.AutoAck = false
	}
}

// ErrorHandler will catch all broker errors that cant be handled
// in normal way, for example Codec errors.
func ErrorHandler(h Handler) Option {
	return func(o *Config) {
		o.ErrorHandler = h
	}
}

// Queue sets the name of the queue to share messages on.
func Queue(name string) SubscribeOption {
	return func(o *SubscribeConfig) {
		o.Queue = name
	}
}

// 修改Config.json的路径
func WithName(name string) Option {
	return func(cfg *Config) {
		//cfg.Unregister(cfg)
		cfg.Name = name
		//cfg.Register(cfg)
	}
}

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)
	}
}

func RegisterTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterTTL = t
	}
}

func RegisterInterval(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterInterval = t
	}
}

func Registry(r registry.IRegistry) Option {
	return func(o *Config) {
		o.Registry = r
	}
}

// Secure communication with the broker.
func Secure(b bool) Option {
	return func(o *Config) {
		o.Secure = b
	}
}

// Specify TLS Config.
func TLSConfig(t *tls.Config) Option {
	return func(o *Config) {
		o.TLSConfig = t
	}
}

func Context(ctx context.Context) Option {
	return func(cfg *Config) {
		cfg.Context = ctx
	}
}

// Codec sets the codec used for encoding/decoding used where
// a broker does not support headers.
func Codec(c codec.SerializeType) Option {
	return func(cfg *Config) {
		cfg.Codec = codec.IdentifyCodec(c)
	}
}

func WithCodec(c codec.SerializeType) PublishOption {
	return func(cfg *PublishConfig) {
		cfg.SerializeType = c
		cfg.Codec = codec.IdentifyCodec(c)
	}
}

// PublishContext set context.
func PublishContext(ctx context.Context) PublishOption {
	return func(o *PublishConfig) {
		o.Context = ctx
	}
}

// SubscribeContext set context.
func SubscribeContext(ctx context.Context) SubscribeOption {
	return func(o *SubscribeConfig) {
		o.Context = ctx
	}
}
