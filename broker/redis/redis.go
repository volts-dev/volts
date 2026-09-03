// Package redis 提供基于 Redis Streams 的 broker 后端。
//
// 与内置的两个后端相比，它换掉的是最根本的那件事：**消息落在 redis 里，不在
// 发布进程的内存里**。由此带来三个内置后端做不到的性质：
//
//   - **at-least-once**。消费组的 PEL 记着"投出去但还没确认"的消息，handler
//     报错、或者消费者进程被 SIGKILL，消息都会在 ClaimMinIdle 之后被重新认领
//     投递。IEvent.Ack() 在这里是真的 XACK，broker.DisableAutoAck() 也真的生效。
//     代价是调用方必须做幂等 —— 这与 http/memory 后端的 at-most-once 相反。
//   - **发布方和订阅方不必同时在线**。http 后端的积压队列只活在发布进程的内存里
//     （64 条，进程一退就没），这里的积压是 stream 本身，受 MaxLen 约束。
//   - **不依赖 registry**。http 后端把每个 (topic × 进程) 编码成一条 registry
//     记录，广播还是 queue 靠比对 Service.Version 这个跨进程字符串契约决定；
//     redis 后端直接用消费组表达同一件事，registry 相关的配置在这里全部无效
//     （WithRegistry 是空操作），"两个互不相识的 broker 实例"那一类故障也就
//     不存在了。
//
// 用法是一行空导入，然后把后端名配成 redis：
//
//	import _ "github.com/volts-dev/volts/broker/redis"
//
//	volts.New(volts.Server(server.New(
//	    server.WithBrokerType("redis"),
//	    server.WithBrokerHost("redis://:${REDIS_PASSWORD}@10.0.0.5:6379/1"),
//	)))
//
// 语义映射：一个 topic 一条 stream；带 queue 的订阅共用一个消费组（组内分摊，
// 即 queue group），不带 queue 的订阅各占一个自己的消费组（组间各收一份，即广播）。
//
// 最低 redis 版本 5.0（stream 与消费组是这个版本引入的）。
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/logger"
)

var log = logger.New("broker")

const (
	fieldHeader = "v-header"
	fieldBody   = "v-body"

	// queueGroupPrefix / broadcastGroupPrefix 给消费组名分类。
	//
	// 清理僵尸组只认 broadcastGroupPrefix：广播组属于某一个进程，本进程能从
	// "所有消费者都闲置很久且没有待确认消息"判断它已经死了；queue 组是跨进程
	// 共享的，同样的迹象只说明"最近没人发消息"。
	queueGroupPrefix     = "q:"
	broadcastGroupPrefix = "b:"
)

type redisBroker struct {
	sync.RWMutex
	config *broker.Config
	opts   *options

	client    goredis.UniversalClient
	addr      string
	connected bool

	// broadcastID 是本进程在广播组名里的身份段，见 options.BroadcastID。
	broadcastID string

	subs map[string][]*redisSubscriber

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func init() {
	broker.Register("redis", New)
}

func New(opts ...broker.Option) broker.IBroker {
	var defaultOpts []broker.Option
	defaultOpts = append(defaultOpts,
		broker.WithName("redis"),
		// 必须排在 opts 之前：redis 专属配置挂在 Context 上，WithContext 会整个
		// 换掉它。放在后面等于把调用方传的 redis 选项一起抹掉。
		broker.WithContext(context.Background()),
	)

	cfg := broker.NewConfig(append(defaultOpts, opts...)...)

	return &redisBroker{
		config: cfg,
		opts:   optionsOf(cfg),
		subs:   make(map[string][]*redisSubscriber),
	}
}

func (b *redisBroker) String() string { return "redis" }

func (b *redisBroker) Config() *broker.Config { return b.config }

// Address 返回实际连上的 redis 地址。
//
// server 的 WithBroker 会把它回填进 cfg.BrokerHost，所以这里返回的必须是能
// 再次连上的东西 —— 给的是原始地址（可能带 redis:// 前缀），不是解析后的形态。
func (b *redisBroker) Address() string {
	b.RLock()
	defer b.RUnlock()
	if b.addr != "" {
		return b.addr
	}
	if len(b.config.Addrs) > 0 && b.config.Addrs[0] != "" {
		return b.config.Addrs[0]
	}
	return DefaultAddr
}

// Init 应用配置。
//
// 已经连上之后再改地址不会重连 —— 与 http 后端一致：实例是按 name-addr 缓存
// 复用的，后来的持有者只是覆盖配置，不该把别人正在用的连接换掉。
func (b *redisBroker) Init(opts ...broker.Option) error {
	b.Lock()
	defer b.Unlock()
	for _, o := range opts {
		if o != nil {
			o(b.config)
		}
	}
	// 选项对象是挂在 Context 上的指针，上面这轮可能刚把它换成新的一份。
	b.opts = optionsOf(b.config)
	return nil
}

func (b *redisBroker) Start() error { return b.connect() }

func (b *redisBroker) conn() goredis.UniversalClient {
	b.RLock()
	defer b.RUnlock()
	return b.client
}

// connect 建立连接，幂等。
//
// Publish 和 Subscribe 都会先调它：调用方常常忘了 Start（http 后端的
// Subscribe 也是自己先 Start 的），而"没 Start 所以静默什么都没发生"是本仓
// 反复踩过的那一类故障。
func (b *redisBroker) connect() error {
	b.Lock()
	defer b.Unlock()

	if b.connected {
		return nil
	}

	cli, addr, err := buildClient(b.config, b.opts)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), b.opts.DialTimeout)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close()
		return fmt.Errorf("broker/redis: 连不上 %s: %w", addr, err)
	}

	b.client = cli
	b.addr = addr
	b.broadcastID = b.opts.BroadcastID
	if b.broadcastID == "" {
		b.broadcastID = uuid.New().String()
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.connected = true
	return nil
}

// Close 停掉所有订阅并断开连接。
//
// 注意 broker 实例是全进程共享的（broker.Use 的缓存），**第一个 Close 会把
// 所有持有者的 broker 一起关掉** —— 这一点与 http 后端相同。
func (b *redisBroker) Close() error {
	b.Lock()
	if !b.connected {
		b.Unlock()
		return nil
	}
	b.connected = false

	cancel := b.cancel
	client := b.client
	subs := make([]*redisSubscriber, 0, len(b.subs))
	for _, list := range b.subs {
		subs = append(subs, list...)
	}
	b.subs = make(map[string][]*redisSubscriber)
	b.Unlock()

	if cancel != nil {
		cancel()
	}
	b.wg.Wait()

	// 读循环都退出之后再清理消费组：反过来的话，刚 destroy 掉的组会被还在
	// 跑的读循环的 NOGROUP 分支重新建起来。
	var err error
	for _, s := range subs {
		if e := s.stop(); e != nil {
			err = e
		}
	}

	b.Lock()
	b.client = nil
	b.Unlock()

	if client != nil {
		if e := client.Close(); e != nil {
			err = e
		}
	}
	return err
}

func (b *redisBroker) stream(topic string) string {
	prefix := b.opts.KeyPrefix
	if prefix == "" {
		return topic
	}
	return prefix + ":" + topic
}

// Publish 把消息写进 topic 对应的 stream。
//
// 没有任何订阅者时它照样写入（并按 MaxLen 滚动淘汰）—— 这正是与 http 后端的
// 分水岭：那边"没订阅者"等于消息就地蒸发，这边晚一点起来的订阅者只要配了
// WithReadFromOldest 就还读得到。
//
// PublishConfig 的 Codec/SerializeType 在这里不生效：Body 已经是调用方编好的
// 字节，stream 原样存取，不再套一层编码。
func (b *redisBroker) Publish(topic string, msg *broker.Message, opts ...broker.PublishOption) error {
	if err := b.connect(); err != nil {
		return err
	}

	b.RLock()
	cli, ctx := b.client, b.ctx
	b.RUnlock()
	if cli == nil {
		return errors.New("broker/redis: 连接已关闭")
	}

	header := "{}"
	var body []byte
	if msg != nil {
		if len(msg.Header) > 0 {
			buf, err := json.Marshal(msg.Header)
			if err != nil {
				return fmt.Errorf("broker/redis: 编码 header 失败: %w", err)
			}
			header = string(buf)
		}
		body = msg.Body
	}
	if body == nil {
		body = []byte{}
	}

	return cli.XAdd(ctx, &goredis.XAddArgs{
		Stream: b.stream(topic),
		MaxLen: b.opts.MaxLen,
		Approx: true,
		Values: []any{fieldHeader, header, fieldBody, body},
	}).Err()
}

func (b *redisBroker) Subscribe(topic string, handler broker.Handler, opts ...broker.SubscribeOption) (broker.ISubscriber, error) {
	if handler == nil {
		return nil, errors.New("broker/redis: handler 不能为空")
	}
	if err := b.connect(); err != nil {
		return nil, err
	}

	// 用 NewSubscribeOptions 而不是自己 for-range 一个零值 SubscribeConfig：
	// AutoAck 的默认值是 **true**，零值是 false。broker/memory 就是自己遍历的，
	// 只因为它压根不读这个字段才没出事；在 redis 后端那会变成"每条消息都不确认，
	// 30 秒后全部重投一遍"，而且看起来只是"消息偶尔重复"。
	sc := broker.NewSubscribeOptions(opts...)

	b.Lock()
	if !b.connected {
		b.Unlock()
		return nil, errors.New("broker/redis: 连接已关闭")
	}

	sub := &redisSubscriber{
		id:      uuid.New().String(),
		topic:   topic,
		stream:  b.stream(topic),
		opts:    sc,
		handler: handler,
		b:       b,
		done:    make(chan struct{}),
		slot:    -1,
	}
	sub.consumer = "c:" + sub.id

	if sc.Queue != "" {
		sub.group = queueGroupPrefix + sc.Queue
	} else {
		sub.slot = b.freeSlotLocked(topic)
		sub.group = fmt.Sprintf("%s%s:%d", broadcastGroupPrefix, b.broadcastID, sub.slot)
		// 只有随机身份的广播组才该销毁：配了 BroadcastID 的进程是要靠这个组
		// 在重启后接回读取位置的，退订就删等于把这个能力删掉。
		sub.ephemeral = b.opts.BroadcastID == ""
	}
	sub.ctx, sub.cancel = context.WithCancel(b.ctx)

	b.subs[topic] = append(b.subs[topic], sub)
	cli := b.client
	b.Unlock()

	if err := ensureGroup(sub.ctx, cli, sub.stream, sub.group, sub.startID()); err != nil {
		b.removeSub(sub)
		sub.cancel()
		close(sub.done)
		return nil, fmt.Errorf("broker/redis: 创建消费组 %q 失败: %w", sub.group, err)
	}

	// 先落心跳再扫：反过来的话，本次刚建好的组还没有心跳键，会被自己扫掉。
	if sub.slot >= 0 {
		b.beat(sub.ctx, cli, sub.stream, sub.group, sub.consumer)
	}
	b.sweepStaleGroups(sub.ctx, cli, sub.stream)

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		sub.run(cli)
	}()

	return sub, nil
}

// freeSlotLocked 找出这个 topic 上最小的空闲广播槽位。调用方须持写锁。
func (b *redisBroker) freeSlotLocked(topic string) int {
	used := make(map[int]bool, len(b.subs[topic]))
	for _, s := range b.subs[topic] {
		if s.slot >= 0 {
			used[s.slot] = true
		}
	}
	slot := 0
	for used[slot] {
		slot++
	}
	return slot
}

// removeSub 把订阅从表里摘掉，返回它是否真的还在表里（用于 Unsubscribe 的幂等）。
func (b *redisBroker) removeSub(s *redisSubscriber) bool {
	b.Lock()
	defer b.Unlock()

	list := b.subs[s.topic]
	var kept []*redisSubscriber
	var removed bool
	for _, sub := range list {
		if !removed && sub == s {
			removed = true
			continue
		}
		kept = append(kept, sub)
	}
	if !removed {
		return false
	}
	if len(kept) == 0 {
		delete(b.subs, s.topic)
	} else {
		b.subs[s.topic] = kept
	}
	return true
}

// heartbeatKey 是某个广播组"持有它的进程还活着"的凭据。
func heartbeatKey(stream, group string) string {
	return stream + ":hb:" + group
}

// beat 续一次广播组的心跳。
func (b *redisBroker) beat(ctx context.Context, cli goredis.UniversalClient, stream, group, consumer string) {
	ttl := b.opts.GroupTTL
	if ttl <= 0 || cli == nil {
		return
	}
	if err := cli.Set(ctx, heartbeatKey(stream, group), consumer, ttl).Err(); err != nil && ctx.Err() == nil {
		log.Dbgf("broker/redis: 续广播组 %q 的心跳失败: %v", group, err)
	}
}

// sweepStaleGroups 清掉这条 stream 上属于已死进程的广播组。
//
// 广播组按进程身份命名，随机身份的进程每重启一次就留下一个组。组本身很小
// （XADD MAXLEN 是按 stream 裁的，与组无关），但一年下来会攒出几百条 XINFO
// GROUPS 记录，把真正在用的那几个淹掉。
//
// 判活靠**心跳键**，不靠消费者的 idle。后者看起来更省事，实测却是错的：
// redis 7.0.4 上，一次没读到数据的 XREADGROUP **不会**刷新消费者的 seen-time
// （连续两次 XINFO CONSUMERS 量到 151ms -> 209ms，中间隔着一次空读，idle 照涨）。
// 也就是说 idle 是"距上次真收到消息"，不是"距上次有动静"，于是一个低流量 topic
// 上活得好好的订阅者，看起来和死了一模一样 —— 按 idle 清就会把它的组销毁。
// 这类误判还特别隐蔽：读循环的 NOGROUP 分支会把组悄悄重建回来，只是读取位置
// 被重置成 "$"，表现为"偶尔丢一段消息"。
//
// 只清广播组。queue 组是跨进程共享的，本进程无从判断别的进程还在不在用它。
func (b *redisBroker) sweepStaleGroups(ctx context.Context, cli goredis.UniversalClient, stream string) {
	if b.opts.GroupTTL <= 0 {
		return
	}

	groups, err := cli.XInfoGroups(ctx, stream).Result()
	if err != nil {
		return
	}

	for _, g := range groups {
		if !strings.HasPrefix(g.Name, broadcastGroupPrefix) || g.Pending > 0 {
			continue
		}
		n, err := cli.Exists(ctx, heartbeatKey(stream, g.Name)).Result()
		if err != nil || n > 0 {
			continue
		}
		if err := cli.XGroupDestroy(ctx, stream, g.Name).Err(); err != nil {
			continue
		}
		log.Warnf("broker/redis: 清理僵尸广播组 %q（stream %s，心跳已过期超过 %v）", g.Name, stream, b.opts.GroupTTL)
	}
}

// buildClient 按配置造一个 redis 客户端，同时返回用于日志和 Address() 的地址。
//
// 地址可以是裸 host:port，也可以是 redis:// rediss:// unix:// 形式的 URL ——
// 后者能把密码写进地址，从而借 config 的 ${VAR} 环境变量展开把它挡在
// config.json 之外。显式的 WithAuth/WithDB 覆盖 URL 里的同名部分。
//
// 单地址走 redis.NewClient（它支持 Network:"unix"，UniversalOptions 没有这个
// 字段）；多地址或配了 MasterName 才走 UniversalClient（cluster / sentinel）。
func buildClient(cfg *broker.Config, o *options) (goredis.UniversalClient, string, error) {
	var addrs []string
	for _, a := range cfg.Addrs {
		if a = strings.TrimSpace(a); a != "" && !strings.HasSuffix(a, ":0") {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		addrs = []string{DefaultAddr}
	}

	if len(addrs) == 1 && o.MasterName == "" {
		opt := &goredis.Options{Addr: addrs[0]}
		if strings.Contains(addrs[0], "://") {
			parsed, err := goredis.ParseURL(addrs[0])
			if err != nil {
				return nil, addrs[0], fmt.Errorf("broker/redis: 地址 %q 解析失败: %w", addrs[0], err)
			}
			opt = parsed
		}
		if o.Username != "" {
			opt.Username = o.Username
		}
		if o.Password != "" {
			opt.Password = o.Password
		}
		if o.DB != 0 {
			opt.DB = o.DB
		}
		if cfg.TLSConfig != nil {
			opt.TLSConfig = cfg.TLSConfig
		}
		if o.DialTimeout > 0 {
			opt.DialTimeout = o.DialTimeout
		}
		return goredis.NewClient(opt), addrs[0], nil
	}

	uopt := &goredis.UniversalOptions{
		Addrs:      addrs,
		Username:   o.Username,
		Password:   o.Password,
		DB:         o.DB,
		MasterName: o.MasterName,
		TLSConfig:  cfg.TLSConfig,
	}
	if o.DialTimeout > 0 {
		uopt.DialTimeout = o.DialTimeout
	}
	return goredis.NewUniversalClient(uopt), strings.Join(addrs, ","), nil
}
