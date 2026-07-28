package broker

import (
	"fmt"
	"strings"
	"sync"

	"github.com/volts-dev/volts/config"
)

type (
	// IBroker 提供微服务之间的消息交换总线
	IBroker interface {
		String() string // 返回对象名称
		Init(...Option) error
		Config() *Config
		Address() string
		Start() error // 开始阻塞监听
		Close() error
		Publish(topic string, m *Message, opts ...PublishOption) error
		Subscribe(topic string, h Handler, opts ...SubscribeOption) (ISubscriber, error)
	}

	// Subscriber is a convenience return type for the Subscribe method
	ISubscriber interface {
		Config() *SubscribeConfig
		Topic() string
		Unsubscribe() error
	}

	// Event is given to a subscription handler for processing
	IEvent interface {
		Topic() string
		Message() *Message
		Ack() error
		Error() error
	}

	// Handler is used to process messages via a subscription of a topic.
	// The handler is passed a publication interface which contains the
	// message and optional Ack method to acknowledge receipt of the message.
	Handler func(IEvent) error
)

var brokerMap = make(map[string]func(opts ...Option) IBroker)
var brokerHost sync.Map
var defaultBroker IBroker

// Default 返回默认 broker。
//
// **没有任何后端被注册时返回 nil**，调用方必须判空。后端靠 init() 注册（通常是
// 一行空导入），一个只用 HTTP/RPC 的进程完全可能没引入任何 broker —— 此前这里
// 会拿到 nil 构造器直接调用而 panic，把"没配 pub/sub"变成启动崩溃。
func Default(opts ...Option) IBroker {
	if defaultBroker == nil {
		defaultBroker = Use("http")
	}
	if defaultBroker == nil {
		return nil
	}
	defaultBroker.Init(opts...)
	return defaultBroker
}

func Register(name string, creator func(opts ...Option) IBroker) {
	brokerMap[name] = creator
}

// warnConfigDivergence 在复用已有 broker 实例时，报告调用方配置与实例现有配置的分歧。
//
// broker 实例按 name-addr 复用，一个实例只有一份运行期状态；每个持有者却都带着
// 自己那份配置来调 Use。这里的 b.Init(opts...) 会让**后来者覆盖先来者**，于是
// 先装配的组件以为自己设的 RegisterTTL 生效了，实际早被后面的覆盖掉。
func warnConfigDivergence(altName string, want, got *Config) {
	if want == nil || got == nil {
		return
	}

	var diffs []string
	if want.RegisterTTL != 0 && want.RegisterTTL != got.RegisterTTL {
		diffs = append(diffs, fmt.Sprintf("register_ttl: %v -> %v", got.RegisterTTL, want.RegisterTTL))
	}
	if want.RegisterInterval != 0 && want.RegisterInterval != got.RegisterInterval {
		diffs = append(diffs, fmt.Sprintf("register_interval: %v -> %v", got.RegisterInterval, want.RegisterInterval))
	}
	if want.BroadcastVersion != "" && want.BroadcastVersion != got.BroadcastVersion {
		diffs = append(diffs, fmt.Sprintf("broadcast_version: %q -> %q", got.BroadcastVersion, want.BroadcastVersion))
	}
	if len(diffs) == 0 {
		return
	}

	log.Warnf("broker %q 已被复用（同名同地址即同一实例），来自 %q 的配置将**覆盖**"+
		"先前 %q 的设置：\n  %s", altName, want.String(), got.String(), strings.Join(diffs, "\n  "))
}

func Use(name string, opts ...Option) IBroker {
	// 与全局隔离：这个 cfg 只用来解析 Addrs 以查实例缓存，随后丢弃。opts 里常含
	// WithConfigPrefixName，而它内部直接 Register —— 不隔离就会把临时对象注册进
	// 全局 models，该配置路径此后指向一个没人持有的孤儿对象。详见 config.Detach。
	cfg := &Config{Name: name}
	config.Detach(&cfg.Config)
	cfg.Init(opts...)

	// 加载存在的服务
	for _, addr := range cfg.Addrs {
		if addr == "" {
			continue
		}

		altName := fmt.Sprintf("%s-%s", cfg.Name, addr)
		if reg, has := brokerHost.Load(altName); has {
			b := reg.(IBroker)
			// 复用已有实例时仍 apply 一次 opts：与 registry 的「先创建者为准」相反，
			// broker 这里是**后来者覆盖先来者**。两种都让「哪份配置生效」取决于
			// 装配顺序，所以同样要出声 —— 只是这次被静默改掉的是先来者。
			warnConfigDivergence(altName, cfg, b.Config())
			b.Init(opts...)
			return b
		}
	}

	if fn, has := brokerMap[name]; has {
		reg := fn(opts...)
		for _, addr := range reg.Config().Addrs {
			if addr == "" {
				continue
			}

			regName := fmt.Sprintf("%s-%s", cfg.Name, addr)
			brokerHost.Store(regName, reg)
		}
		return reg
	}

	// 默认
	def := brokerMap["http"]
	if def == nil {
		// 未注册任何后端。返回 nil 而不是调用 nil 构造器 —— 后者是段错误，
		// 且崩在 init 阶段，栈里看不出"你没导入 broker 后端"这个真正原因。
		return nil
	}
	return def(opts...)
}
