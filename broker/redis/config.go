package redis

import (
	"context"
	"time"

	"github.com/volts-dev/volts/broker"
)

const (
	// DefaultKeyPrefix 是 stream 键名的前缀。一个 topic 对应一个 stream：
	// <prefix>:<topic>。给它加前缀是为了让 broker 的键在共用的 redis 实例里
	// 能被 SCAN 出来、也能被 ACL 按 key pattern 授权。
	DefaultKeyPrefix = "volts:broker"

	// DefaultAddr 是没有配置地址时的回落目标。
	DefaultAddr = "127.0.0.1:6379"
)

type (
	optionsKey struct{}

	// options 是 redis 后端独有的配置。
	//
	// 它挂在 broker.Config.Context 上（与 registry/etcd 的 auth/log 配置同一套做法），
	// 因为 broker.Config 是所有后端共用的结构，不该长出某一个后端专有的字段。
	//
	// **注意顺序**：broker.WithContext() 会整个换掉 Context，把这里的配置一起换走。
	// 要同时用两者，WithContext 必须排在所有 redis 选项之前。
	options struct {
		KeyPrefix string
		Username  string
		Password  string
		DB        int

		// MasterName 非空时按 sentinel 模式连接。
		MasterName string

		// MaxLen 是每个 stream 保留的消息条数上限（XADD MAXLEN ~ N）。
		//
		// **这是个有界缓冲，不是永久队列。** 一个停机时间内积压超过 MaxLen 条的
		// 订阅者，落在窗口外的消息就是丢了 —— 想要"绝不丢"得配合更大的窗口和
		// 对 redis 本身的持久化配置，那已经超出 broker 的职责。
		MaxLen int64

		// Block 是 XREADGROUP 的阻塞时长，Count 是单次最多取几条。
		Block time.Duration
		Count int64

		// ClaimMinIdle 是"这条消息的持有者大概已经死了"的判定阈值：
		// 投出去这么久还没被确认，就允许别的消费者认领过来重投。
		//
		// 它同时是 handler 报错后的重投间隔 —— 报错的消息不确认，就留在 PEL 里
		// 等这个时间过去被认领。设得太短会把"handler 正在慢慢处理"误判成死亡。
		ClaimMinIdle time.Duration

		// ClaimInterval 是本订阅者多久扫一次待认领消息。
		ClaimInterval time.Duration

		// MaxDeliveries 是一条消息最多被投递几次。超过就确认掉并出声。
		//
		// 没有这个上限，一条 handler 永远处理不了的"毒消息"会占着 PEL 的队头
		// 无限重投，把它后面所有真正该重投的消息一起拖住。0 表示不限（不建议）。
		MaxDeliveries int64

		// GroupTTL 是广播组心跳键的存活时长，也就是"进程失联多久之后，它的
		// 广播组可以被别人清掉"。活着的订阅者每读一轮就续一次期。
		//
		// 只清广播组（组名带 b: 前缀）。queue 组是跨进程共享的，本进程无从判断
		// 别的进程还在不在用它。0 表示不清理。
		//
		// 配了 BroadcastID 的进程停机超过这个时长，组同样会被清掉，重启后从
		// "$" 重新开始 —— 想让停机窗口更长就把它调大。
		GroupTTL time.Duration

		// FromOldest 决定新建的消费组从哪里开始读：false（默认）只收订阅之后
		// 发布的消息，与 pub/sub 的直觉一致；true 则从 stream 里现存的最早一条读起。
		FromOldest bool

		// BroadcastID 是本进程在广播订阅里的稳定身份。
		//
		// 留空时每次启动随机生成，进程重启即换新组 —— 旧组会残留在 redis 里
		// （靠 GroupIdleTTL 清理），且重启期间发布的消息收不到。填一个稳定值
		// （比如部署名）则重启后沿用同一个组，从断点续读；代价是这个进程如果
		// 永久下线，组就得手工清。
		BroadcastID string

		DialTimeout time.Duration
	}
)

func newOptions() *options {
	return &options{
		KeyPrefix:     DefaultKeyPrefix,
		MaxLen:        10000,
		Block:         5 * time.Second,
		Count:         16,
		ClaimMinIdle:  30 * time.Second,
		ClaimInterval: 15 * time.Second,
		MaxDeliveries: 5,
		GroupTTL:      time.Hour,
		DialTimeout:   5 * time.Second,
	}
}

// optionsOf 取出挂在 cfg.Context 上的 redis 配置，没有就创建一份。
//
// 返回的是**指针**，后续 Init 传进来的选项直接改这一份 —— 复用实例时
// （broker.Use 的 name-addr 缓存）新配置才能落到已经在跑的那个 broker 上。
func optionsOf(cfg *broker.Config) *options {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if o, ok := cfg.Context.Value(optionsKey{}).(*options); ok && o != nil {
		return o
	}
	o := newOptions()
	cfg.Context = context.WithValue(cfg.Context, optionsKey{}, o)
	return o
}

func withOption(fn func(*options)) broker.Option {
	return func(cfg *broker.Config) { fn(optionsOf(cfg)) }
}

// WithKeyPrefix 设置 stream 键名前缀。
func WithKeyPrefix(prefix string) broker.Option {
	return withOption(func(o *options) { o.KeyPrefix = prefix })
}

// WithAuth 设置 redis 的用户名/密码。
//
// 也可以直接写进地址：broker.Addrs("redis://user:pass@host:6379/2")，
// 这样密码能走 config 的 ${VAR} 环境变量展开而不落进 config.json。
// 两者都给时以本选项为准。
func WithAuth(username, password string) broker.Option {
	return withOption(func(o *options) { o.Username, o.Password = username, password })
}

// WithDB 选择 redis 的库号。
func WithDB(db int) broker.Option {
	return withOption(func(o *options) { o.DB = db })
}

// WithMasterName 以 sentinel 模式连接，name 是主节点名。
func WithMasterName(name string) broker.Option {
	return withOption(func(o *options) { o.MasterName = name })
}

// WithMaxLen 设置每个 topic 的 stream 保留条数上限。
func WithMaxLen(n int64) broker.Option {
	return withOption(func(o *options) { o.MaxLen = n })
}

// WithReadBlock 设置 XREADGROUP 的阻塞时长。
func WithReadBlock(d time.Duration) broker.Option {
	return withOption(func(o *options) { o.Block = d })
}

// WithReadCount 设置单次最多取几条消息。
func WithReadCount(n int64) broker.Option {
	return withOption(func(o *options) { o.Count = n })
}

// WithClaim 设置重投的判定阈值与扫描间隔。
func WithClaim(minIdle, interval time.Duration) broker.Option {
	return withOption(func(o *options) { o.ClaimMinIdle, o.ClaimInterval = minIdle, interval })
}

// WithMaxDeliveries 设置一条消息最多投递几次，超过即丢弃并出声。
func WithMaxDeliveries(n int64) broker.Option {
	return withOption(func(o *options) { o.MaxDeliveries = n })
}

// WithGroupTTL 设置广播组心跳键的存活时长（即失联多久算死），0 表示不清理。
func WithGroupTTL(d time.Duration) broker.Option {
	return withOption(func(o *options) { o.GroupTTL = d })
}

// WithReadFromOldest 让新建的消费组从 stream 现存的最早一条读起。
func WithReadFromOldest() broker.Option {
	return withOption(func(o *options) { o.FromOldest = true })
}

// WithBroadcastID 给本进程的广播订阅一个稳定身份，重启后沿用同一个消费组。
func WithBroadcastID(id string) broker.Option {
	return withOption(func(o *options) { o.BroadcastID = id })
}

// WithDialTimeout 设置连接 redis 的超时。
func WithDialTimeout(d time.Duration) broker.Option {
	return withOption(func(o *options) { o.DialTimeout = d })
}
