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
		//
		// **内置的两个后端都不读它。** http 后端里唯一的确认通道就是那次 POST
		// 的响应，消息不落盘也没有重投窗口（投递是 at-most-once，见
		// broker/http/handler.go 对状态码语义的说明）；memory 后端是同进程直接
		// 调用。DisableAutoAck() 因此是个不生效的开关，别据它设计需要重投的流程。
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

	// DefaultBroadcastVersion 是「广播订阅」在 registry 里的版本号标记。
	//
	// 订阅方把它写进 registry.Service.Version，发布方靠比对这个字符串决定
	// 「投给全部节点」还是「按 queue group 随机投一个」。因此它是**跨进程契约**：
	// 集群里所有进程必须用同一个值，改它等于分裂集群（改了的进程发的消息，
	// 没改的进程会当成 queue 组员只收到一部分）。
	DefaultBroadcastVersion = "broadcast"
)

func NewConfig(opts ...Option) *Config {
	cfg := &Config{
		//Config: config.Default(),
		Name:             "default",
		BroadcastVersion: DefaultBroadcastVersion,
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

	return self.Name
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
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
//
// 内置后端都不实现它，见 SubscribeConfig.AutoAck。
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

func WithRegisterTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterTTL = t
	}
}

func WithRegisterInterval(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterInterval = t
	}
}

func WithRegistry(r registry.IRegistry) Option {
	return func(o *Config) {
		o.Registry = r
	}
}

// Secure communication with the broker.
func WithSecure(b bool) Option {
	return func(o *Config) {
		o.Secure = b
	}
}

// Specify TLS Config.
func WithTLSConfig(t *tls.Config) Option {
	return func(o *Config) {
		o.TLSConfig = t
	}
}

func WithContext(ctx context.Context) Option {
	return func(cfg *Config) {
		cfg.Context = ctx
	}
}

// Codec sets the codec used for encoding/decoding used where
// a broker does not support headers.
func WithCodec(c codec.SerializeType) Option {
	return func(cfg *Config) {
		cfg.Codec = codec.IdentifyCodec(c)
	}
}

func WithPublishCodec(c codec.SerializeType) PublishOption {
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
