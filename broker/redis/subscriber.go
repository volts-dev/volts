package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/volts-dev/volts/broker"
)

type (
	redisSubscriber struct {
		id       string
		topic    string
		stream   string
		group    string
		consumer string

		// slot 是本进程在这个 topic 上的第几个广播订阅（queue 订阅为 -1）。
		//
		// 广播语义是"每个订阅都收到全部消息"，而一个消费组内部是分摊的，所以
		// 本进程每一个广播订阅都得占一个**自己的**消费组，组名靠 slot 区分。
		// 用最小空闲槽位而不是自增计数：这样退订再订阅会复用同一个组名，配了
		// 稳定 BroadcastID 的进程重启后才接得回原来的读取位置。
		slot int

		// ephemeral 表示这个消费组是本次运行临时建的，退订时应当销毁。
		// queue 组是跨进程共享的，任何时候都不能销毁。
		ephemeral bool

		opts    *broker.SubscribeConfig
		handler broker.Handler
		b       *redisBroker

		ctx    context.Context
		cancel context.CancelFunc
		done   chan struct{}
		once   sync.Once
	}
)

func (s *redisSubscriber) Config() *broker.SubscribeConfig { return s.opts }

func (s *redisSubscriber) Topic() string { return s.topic }

// Unsubscribe 停掉这个订阅：读循环退出，消费者从组里摘掉，临时组销毁。
//
// **PEL 里没确认完的消息不会丢**：queue 组里它们会被同组的其他消费者认领重投；
// 临时广播组则随组一起销毁 —— 那个组本来就只服务这一个订阅者。
func (s *redisSubscriber) Unsubscribe() error {
	if !s.b.removeSub(s) {
		return nil // 幂等：已经退过了
	}
	return s.stop()
}

func (s *redisSubscriber) stop() error {
	var err error
	s.once.Do(func() {
		s.cancel()
		<-s.done

		// 用独立的 context：s.ctx 已经取消了，拿它发命令一条也发不出去，
		// 消费者和临时组就会留在 redis 里。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cli := s.b.conn()
		if cli == nil {
			return
		}
		if e := cli.XGroupDelConsumer(ctx, s.stream, s.group, s.consumer).Err(); e != nil && !isNoGroup(e) {
			err = e
		}
		if s.ephemeral {
			if e := cli.XGroupDestroy(ctx, s.stream, s.group).Err(); e != nil && !isNoGroup(e) {
				err = e
			}
		}
	})
	return err
}

func (s *redisSubscriber) ack(ctx context.Context, id string) error {
	cli := s.b.conn()
	if cli == nil {
		return errors.New("broker/redis: 连接已关闭，无法确认消息")
	}
	if ctx == nil || ctx.Err() != nil {
		// 订阅已经在停了，但这条消息确实处理完了 —— 用独立 context 把确认发出去，
		// 否则它会留在 PEL 里，等 ClaimMinIdle 过后被别人重投一次。
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	return cli.XAck(ctx, s.stream, s.group, id).Err()
}

// run 是这个订阅的唯一读循环。
//
// 读和重投扫描放在同一条 goroutine 里轮流做，而不是各开一条：handler 是串行
// 调用的（保序），两条 goroutine 都要调它就得再加一把锁，而 XREADGROUP 本来
// 就每 Block 秒返回一次，顺手扫一遍待认领消息不需要额外的定时器。
func (s *redisSubscriber) run(cli goredis.UniversalClient) {
	defer close(s.done)

	o := s.b.opts
	var lastClaim, lastBeat time.Time

	for {
		if s.ctx.Err() != nil {
			return
		}

		if o.ClaimInterval > 0 && time.Since(lastClaim) >= o.ClaimInterval {
			s.reclaim(cli)
			lastClaim = time.Now()
		}

		// 广播组的心跳：本进程还在读，别的进程就不该清掉这个组。
		//
		// 顺着读循环续期，不另开定时器 —— XREADGROUP 每 Block 秒就返回一次。
		// 按 GroupTTL/4 限流：有消息流动时这个循环每批就转一圈，不限流会变成
		// 每批消息一次多余的 SET。
		if s.slot >= 0 && o.GroupTTL > 0 && time.Since(lastBeat) >= o.GroupTTL/4 {
			s.b.beat(s.ctx, cli, s.stream, s.group, s.consumer)
			lastBeat = time.Now()
		}

		res, err := cli.XReadGroup(s.ctx, &goredis.XReadGroupArgs{
			Group:    s.group,
			Consumer: s.consumer,
			Streams:  []string{s.stream, ">"},
			Count:    o.Count,
			Block:    o.Block,
		}).Result()

		switch {
		case err == nil:
		case errors.Is(err, goredis.Nil):
			continue // Block 到期没有新消息，正常
		case s.ctx.Err() != nil:
			return
		case isNoGroup(err):
			// 消费组没了：运维手工 XGROUP DESTROY 过，或者本进程停得太久被别的
			// 进程的清理逻辑判成僵尸。重建后继续 —— 不重建的话这条订阅从此静默
			// 失聪，日志里只会每秒刷一行 NOGROUP，没人会把它读成"订阅已失效"。
			log.Warnf("broker/redis: topic %q 的消费组 %q 不存在，重建后继续", s.topic, s.group)
			if e := ensureGroup(s.ctx, cli, s.stream, s.group, s.startID()); e != nil {
				log.Errf("broker/redis: 重建消费组 %q 失败: %v", s.group, e)
				s.pause()
			}
			continue
		default:
			log.Errf("broker/redis: 读 topic %q 失败: %v", s.topic, err)
			s.pause()
			continue
		}

		for _, st := range res {
			for _, m := range st.Messages {
				s.handle(cli, m)
			}
		}
	}
}

func (s *redisSubscriber) startID() string {
	if s.b.opts.FromOldest {
		return "0"
	}
	return "$"
}

// pause 在出错后歇一秒，避免 redis 不可用时把日志和 CPU 一起打满。
func (s *redisSubscriber) pause() {
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-s.ctx.Done():
	case <-t.C:
	}
}

// handle 处理一条消息。
//
// handler 报错时**不确认**：消息留在 PEL 里，ClaimMinIdle 之后被重新认领投递。
// 这与 http 后端相反（那边 handler 报错也回 2xx，因为它没有重投窗口，回 5xx
// 只会让"业务出错"变成"每条后继消息都被消费两次"）。redis 后端有真正的重投，
// 重投才是这里的正确答案 —— 代价是调用方必须做幂等。
func (s *redisSubscriber) handle(cli goredis.UniversalClient, m goredis.XMessage) {
	msg, err := decodeMessage(m)
	if err != nil {
		// 解不开的报文重投多少次结果都一样。直接确认掉并出声，否则它会永远
		// 占着 PEL 的队头，把后面每一条真该重投的消息拖在身后。
		log.Errf("broker/redis: topic %q 的消息 %s 解码失败，已丢弃: %v", s.topic, m.ID, err)
		if e := cli.XAck(s.ctx, s.stream, s.group, m.ID).Err(); e != nil {
			log.Errf("broker/redis: 丢弃 %s 时确认失败: %v", m.ID, e)
		}
		return
	}

	ev := &event{topic: s.topic, msg: msg, id: m.ID, sub: s, ctx: s.ctx}
	if err := s.dispatch(ev); err != nil {
		ev.err = err
		if eh := s.b.config.ErrorHandler; eh != nil {
			eh(ev)
		} else {
			log.Errf("broker/redis: topic %q 的订阅处理失败，消息 %s 待重投: %v", s.topic, m.ID, err)
		}
		return
	}

	if s.opts.AutoAck {
		if err := ev.Ack(); err != nil {
			log.Errf("broker/redis: 确认消息 %s 失败（将被重投）: %v", m.ID, err)
		}
	}
}

// dispatch 单独成函数，好让 defer recover 把 handler 的 panic 转成 error ——
// 与 broker/memory、broker/http 一致：一个坏订阅者不该拖垮整条读循环。
func (s *redisSubscriber) dispatch(ev *event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("broker/redis: handler panicked: %v", r)
		}
	}()
	return s.handler(ev)
}

// reclaim 认领并重投那些"投出去很久还没被确认"的消息。
//
// 这是 crash 恢复路径：消费者进程被 SIGKILL 时，它已取走但未确认的消息会永远
// 留在 PEL 里，除非有人来认领。同组的任何一个存活消费者都会做这件事。
func (s *redisSubscriber) reclaim(cli goredis.UniversalClient) {
	o := s.b.opts
	if o.ClaimMinIdle <= 0 {
		return
	}

	// 不用 XPENDING 的 IDLE 过滤参数（redis 6.2 才有），改在客户端按返回的
	// idle 字段筛 —— 扩展形式的 XPENDING 从 5.0 起就返回它，而 stream 本身
	// 也是 5.0 才有的，这样最低要求就与 stream 齐平。
	pend, err := cli.XPendingExt(s.ctx, &goredis.XPendingExtArgs{
		Stream: s.stream,
		Group:  s.group,
		Start:  "-",
		End:    "+",
		Count:  o.Count,
	}).Result()
	if err != nil {
		if !errors.Is(err, goredis.Nil) && s.ctx.Err() == nil && !isNoGroup(err) {
			log.Errf("broker/redis: 扫描 topic %q 的待确认消息失败: %v", s.topic, err)
		}
		return
	}

	var ids []string
	for _, p := range pend {
		if p.Idle < o.ClaimMinIdle {
			continue
		}
		if o.MaxDeliveries > 0 && p.RetryCount >= o.MaxDeliveries {
			log.Errf("broker/redis: topic %q 的消息 %s 已投递 %d 次仍未确认，丢弃",
				s.topic, p.ID, p.RetryCount)
			if e := cli.XAck(s.ctx, s.stream, s.group, p.ID).Err(); e != nil {
				log.Errf("broker/redis: 丢弃 %s 时确认失败: %v", p.ID, e)
			}
			continue
		}
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return
	}

	msgs, err := cli.XClaim(s.ctx, &goredis.XClaimArgs{
		Stream:   s.stream,
		Group:    s.group,
		Consumer: s.consumer,
		MinIdle:  o.ClaimMinIdle,
		Messages: ids,
	}).Result()
	if err != nil {
		if s.ctx.Err() == nil && !errors.Is(err, goredis.Nil) {
			log.Errf("broker/redis: 认领 topic %q 的消息失败: %v", s.topic, err)
		}
		return
	}

	for _, m := range msgs {
		s.handle(cli, m)
	}
}

// decodeMessage 把一条 stream 记录还原成 broker.Message。
//
// 落库形状是两个字段：v-header 是 header 的 JSON，v-body 是原始字节。没有把
// header 的键直接摊成 stream 字段，是因为业务的 header 键随时可能撞上我们自己
// 的字段名；分成两格则 redis-cli 里 XRANGE 一眼就能读，运维排查时用得上。
func decodeMessage(m goredis.XMessage) (*broker.Message, error) {
	msg := &broker.Message{}

	if raw, has := m.Values[fieldHeader]; has {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("%s 字段不是字符串（%T）", fieldHeader, raw)
		}
		if s != "" && s != "{}" {
			if err := json.Unmarshal([]byte(s), &msg.Header); err != nil {
				return nil, fmt.Errorf("%s 不是合法 JSON: %w", fieldHeader, err)
			}
		}
	}

	raw, has := m.Values[fieldBody]
	if !has {
		return nil, errors.New("记录里没有 " + fieldBody + " 字段")
	}
	switch v := raw.(type) {
	case string:
		msg.Body = []byte(v)
	case []byte:
		msg.Body = v
	default:
		return nil, fmt.Errorf("%s 字段不是字符串（%T）", fieldBody, raw)
	}

	return msg, nil
}

// ensureGroup 建消费组，组已存在不算错误。
func ensureGroup(ctx context.Context, cli goredis.UniversalClient, stream, group, start string) error {
	err := cli.XGroupCreateMkStream(ctx, stream, group, start).Err()
	if err == nil || strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

func isNoGroup(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "NOGROUP")
}
