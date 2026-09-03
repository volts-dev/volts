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
		// Ack 在内置的 http / memory 后端里都是空实现（恒返回 nil）：
		// 两者都没有重投窗口，投递是 at-most-once。它留在接口上是给需要确认
		// 语义的后端（如 NATS/Kafka）用的。
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

var defaultMu sync.Mutex
var defaultBroker IBroker

// fallbackName 是后端名解析不出来时的回落目标。
const fallbackName = "http"

// Default 返回本进程的默认 broker。
//
// **没有任何后端被注册时返回 nil**，调用方必须判空。后端靠 init() 注册（通常是
// 一行空导入），一个只用 HTTP/RPC 的进程完全可能没引入任何 broker —— 此前这里
// 会拿到 nil 构造器直接调用而 panic，把"没配 pub/sub"变成启动崩溃。
//
// 它必须与 server 装配的那个 broker 是**同一个实例**：只有 server 那份被注入了
// 真 registry（否则停在 NopRegistry，GetService 恒返回 (nil,nil)，Publish 遍历
// 空切片、一条都发不出去还返回 nil），也只有 server 那份被 Start 过（否则连
// 监听端口都没有，别人也投不进来）。同一性由 Use 的裸名缓存 + SetDefault 保证。
func Default(opts ...Option) IBroker {
	defaultMu.Lock()
	b := defaultBroker
	defaultMu.Unlock()

	if b == nil {
		// Use 内部会 adoptDefault，这里不再直接赋值，避免与并发的 Use 抢写。
		b = Use(fallbackName)
	}
	if b == nil {
		return nil
	}
	b.Init(opts...)
	return b
}

// SetDefault 指定本进程的默认 broker，供 Default() 返回。
//
// server 装配完自己的 broker 后调用它：server 那份才是拿到真 registry、被
// Start 过的那个。调用方若要自带 broker，在 volts.New 之后再调一次即可。
func SetDefault(b IBroker) {
	defaultMu.Lock()
	defaultBroker = b
	defaultMu.Unlock()
}

// adoptDefault 让第一个被创建/复用的实例自动成为默认 broker。
func adoptDefault(b IBroker) {
	if b == nil {
		return
	}
	defaultMu.Lock()
	if defaultBroker == nil {
		defaultBroker = b
	}
	defaultMu.Unlock()
}

func Register(name string, creator func(opts ...Option) IBroker) {
	brokerMap[name] = creator
}

// usableAddr 判断一个地址能不能当实例缓存的键。
//
// 端口 0 不算：DefaultAddress 就是 "127.0.0.1:0"，而 server 的 WithBroker 会把
// broker.Address() 回填进 cfg.BrokerHost —— 一旦这个值被写进 config.json，下次
// 启动就变成"带地址"的调用，各建各的实例。:0 是"随便挑个端口"，不是身份。
func usableAddr(addr string) bool {
	if addr == "" {
		return false
	}
	return !strings.HasSuffix(addr, ":0")
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
	// 配置路径也算分歧：WithConfigPrefixName 会把实例的配置节整段搬走
	// （Unregister + Register），一个进程里两台 server 共用 broker 时，
	// 它在 config.json 里的位置取决于谁最后装配。
	if want.PrefixName != "" && want.PrefixName != got.PrefixName {
		diffs = append(diffs, fmt.Sprintf("config_prefix: %q -> %q", got.PrefixName, want.PrefixName))
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

	// 解析后端名。空名或未注册的名字回落到 http（与此前行为一致），但**缓存键
	// 用回落后的真名** —— 否则 Use("") 与 Use("http") 各建一个实例。server 的
	// BrokerType 默认就是空串，所以这正是生产默认路径：此前 server 走回落分支
	// 拿到一个从不入缓存的实例，broker.Default() 又建了另一个，两者互不相识；
	// registry 只注入了 server 那份，Default() 那份停在 NopRegistry，
	// Publish 遍历空节点列表，静默丢掉每一条消息。
	realName := name
	if _, has := brokerMap[realName]; !has {
		if name != "" && name != fallbackName {
			log.Warnf("broker 后端 %q 未注册，回落到 %q", name, fallbackName)
		}
		realName = fallbackName
	}

	fn, has := brokerMap[realName]
	if !has {
		// 未注册任何后端。返回 nil 而不是调用 nil 构造器 —— 后者是段错误，
		// 且崩在 init 阶段，栈里看不出"你没导入 broker 后端"这个真正原因。
		return nil
	}

	// 查实例缓存。给了地址的按 name-addr 找；没给地址的按**裸 name** 找 ——
	// 裸名那一格代表"本进程的这个后端"，broker.Default() 和未配 BrokerHost 的
	// server 都落在这格上，从而拿到同一个实例。
	var keys []string
	for _, addr := range cfg.Addrs {
		if usableAddr(addr) {
			keys = append(keys, fmt.Sprintf("%s-%s", realName, addr))
		}
	}
	addressed := len(keys) > 0
	if !addressed {
		keys = append(keys, realName)
	}

	for _, key := range keys {
		if reg, has := brokerHost.Load(key); has {
			b := reg.(IBroker)
			// 复用已有实例时仍 apply 一次 opts：与 registry 的「先创建者为准」相反，
			// broker 这里是**后来者覆盖先来者**。两种都让「哪份配置生效」取决于
			// 装配顺序，所以同样要出声 —— 只是这次被静默改掉的是先来者。
			warnConfigDivergence(key, cfg, b.Config())
			b.Init(opts...)
			adoptDefault(b)
			return b
		}
	}

	b := fn(opts...)
	if b == nil {
		return nil
	}

	if !addressed {
		// 并发创建时只留一个赢家，输的那份直接丢弃（尚未 Start，只剩一份孤儿
		// 配置模型；两个活着的 broker 要贵得多）。
		if actual, loaded := brokerHost.LoadOrStore(realName, b); loaded {
			b = actual.(IBroker)
			b.Init(opts...)
		}
	} else {
		// 带地址的实例也占一格裸名（仅当空着），好让之后的 Default() 找得到它；
		// 已被占用则不抢，保住"不同地址各自独立"这条既有语义。
		brokerHost.LoadOrStore(realName, b)
	}

	for _, addr := range b.Config().Addrs {
		if !usableAddr(addr) {
			continue
		}
		brokerHost.Store(fmt.Sprintf("%s-%s", realName, addr), b)
	}

	adoptDefault(b)
	return b
}
