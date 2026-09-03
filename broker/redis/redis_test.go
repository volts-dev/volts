package redis

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/volts-dev/volts/broker"
)

// startRedis 起一个只监听 unix socket 的 redis-server 供测试用。
//
// 不绑 TCP 端口：本仓的测试不许占固定端口（server.Debug() 的 35999/45999 撞过
// 本机在跑的 kylin），而 unix socket 连"随机端口也可能撞"的问题都没有。
// socket 路径要短 —— macOS 的 sun_path 只有 104 字节，t.TempDir() 会把测试名
// 拼进路径，长测试名能把它顶爆。
func startRedis(t *testing.T) string {
	t.Helper()

	bin, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("没有 redis-server，跳过 redis broker 测试")
	}

	dir, err := os.MkdirTemp("", "vb")
	if err != nil {
		t.Fatalf("建临时目录失败: %v", err)
	}
	sock := filepath.Join(dir, "r.sock")

	cmd := exec.Command(bin,
		"--port", "0",
		"--unixsocket", sock,
		"--save", "",
		"--appendonly", "no",
		"--daemonize", "no",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 redis-server 失败: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.RemoveAll(dir)
	})

	// 等它把 socket 建出来并能应答。必须带超时：本仓吃过"测试挂满 300s 超时、
	// 一条结果都报不出来"的亏。
	probe := goredis.NewClient(&goredis.Options{Network: "unix", Addr: sock})
	defer probe.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := probe.Ping(ctx).Err()
		cancel()
		if err == nil {
			return sock
		}
		if time.Now().After(deadline) {
			t.Fatalf("redis-server 10s 内没起来: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var prefixSeq atomic.Int64

// newBroker 造一个连到 sock 的 broker，键前缀每次不同以免用例互相看到对方的消息。
func newBroker(t *testing.T, sock string, opts ...broker.Option) broker.IBroker {
	t.Helper()

	base := []broker.Option{
		broker.Addrs("unix://" + sock),
		WithKeyPrefix(fmt.Sprintf("test:%s:%d", t.Name(), prefixSeq.Add(1))),
		// 把节奏压快，否则每个重投用例都要等 30 秒
		WithReadBlock(100 * time.Millisecond),
		WithClaim(300*time.Millisecond, 100*time.Millisecond),
	}

	b := New(append(base, opts...)...)
	if err := b.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// sharedPrefix 让两个 broker（模拟两个进程）指向同一批 stream。
func sharedPrefix(t *testing.T) broker.Option {
	return WithKeyPrefix(fmt.Sprintf("shared:%s:%d", t.Name(), prefixSeq.Add(1)))
}

type collector struct {
	sync.Mutex
	got []string
}

func (c *collector) add(s string) {
	c.Lock()
	c.got = append(c.got, s)
	c.Unlock()
}

func (c *collector) len() int {
	c.Lock()
	defer c.Unlock()
	return len(c.got)
}

func (c *collector) snapshot() []string {
	c.Lock()
	defer c.Unlock()
	return append([]string(nil), c.got...)
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func publish(t *testing.T, b broker.IBroker, topic, body string, header map[string]string) {
	t.Helper()
	if err := b.Publish(topic, &broker.Message{Header: header, Body: []byte(body)}); err != nil {
		t.Fatalf("Publish 失败: %v", err)
	}
}

func TestPublishSubscribe_Roundtrip(t *testing.T) {
	b := newBroker(t, startRedis(t))

	type got struct {
		header map[string]string
		body   string
	}
	ch := make(chan got, 1)

	if _, err := b.Subscribe("t.round", func(e broker.IEvent) error {
		ch <- got{header: e.Message().Header, body: string(e.Message().Body)}
		return nil
	}); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	publish(t, b, "t.round", "hello", map[string]string{"k": "v", "空": "值"})

	select {
	case g := <-ch:
		if g.body != "hello" {
			t.Fatalf("body = %q，期望 %q", g.body, "hello")
		}
		if g.header["k"] != "v" || g.header["空"] != "值" {
			t.Fatalf("header = %v，期望 k=v 且 空=值", g.header)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5s 内没收到消息")
	}
}

// 广播：同一条消息，每个订阅者各收一份 —— 跨进程如此，同进程的两个订阅也如此。
//
// 后者是消费组语义最容易搞反的地方：一个组内部是**分摊**的，所以本进程的每个
// 广播订阅必须各占一个自己的组，不能共用。
func TestBroadcast_EverySubscriberGetsOwnCopy(t *testing.T) {
	sock := startRedis(t)
	prefix := sharedPrefix(t)

	pub := newBroker(t, sock, prefix)
	sub1 := newBroker(t, sock, prefix) // 另一个"进程"
	sub2 := newBroker(t, sock, prefix)

	var a, b1, b2 collector
	mk := func(c *collector) broker.Handler {
		return func(e broker.IEvent) error { c.add(string(e.Message().Body)); return nil }
	}

	// pub 自己也订两条：同进程的两个广播订阅同样各收一份
	for _, s := range []struct {
		b broker.IBroker
		c *collector
	}{{pub, &a}, {pub, &a}, {sub1, &b1}, {sub2, &b2}} {
		if _, err := s.b.Subscribe("t.bcast", mk(s.c)); err != nil {
			t.Fatalf("Subscribe 失败: %v", err)
		}
	}

	publish(t, pub, "t.bcast", "m1", nil)

	if !waitFor(t, 5*time.Second, func() bool {
		return a.len() == 2 && b1.len() == 1 && b2.len() == 1
	}) {
		t.Fatalf("收到的份数 = 本进程两个订阅 %d / 进程2 %d / 进程3 %d，期望 2/1/1",
			a.len(), b1.len(), b2.len())
	}
}

// queue 组：同名 queue 的订阅者分摊消息，每条只被处理一次。
func TestQueue_MessagesAreSplitNotDuplicated(t *testing.T) {
	sock := startRedis(t)
	prefix := sharedPrefix(t)

	pub := newBroker(t, sock, prefix)
	one := newBroker(t, sock, prefix)
	two := newBroker(t, sock, prefix)

	var c collector
	h := func(e broker.IEvent) error { c.add(string(e.Message().Body)); return nil }

	for _, b := range []broker.IBroker{one, two} {
		if _, err := b.Subscribe("t.queue", h, broker.Queue("workers")); err != nil {
			t.Fatalf("Subscribe 失败: %v", err)
		}
	}

	const n = 20
	for i := 0; i < n; i++ {
		publish(t, pub, "t.queue", fmt.Sprintf("m%02d", i), nil)
	}

	if !waitFor(t, 8*time.Second, func() bool { return c.len() >= n }) {
		t.Fatalf("只收到 %d 条，期望 %d 条", c.len(), n)
	}

	// 再等一会儿，确认没有多出来的重复投递
	time.Sleep(700 * time.Millisecond)
	seen := map[string]int{}
	for _, s := range c.snapshot() {
		seen[s]++
	}
	if len(seen) != n {
		t.Fatalf("去重后 %d 条，期望 %d 条", len(seen), n)
	}
	for body, cnt := range seen {
		if cnt != 1 {
			t.Fatalf("%s 被处理了 %d 次，queue 组里每条只该处理一次", body, cnt)
		}
	}
}

// handler 报错 ⇒ 消息不确认 ⇒ 被重投。
//
// 这是 redis 后端与 http 后端的分水岭：http 那边 handler 报错也回 2xx（没有重投
// 窗口，回 5xx 只会把"业务出错"变成"每条后继消息都被消费两次"），这边有真正的
// PEL，报错就该重投。
func TestHandlerError_MessageIsRedelivered(t *testing.T) {
	b := newBroker(t, startRedis(t))

	var calls atomic.Int32
	done := make(chan struct{})
	var once sync.Once

	if _, err := b.Subscribe("t.retry", func(e broker.IEvent) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("第一次故意失败")
		}
		once.Do(func() { close(done) })
		return nil
	}); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	publish(t, b, "t.retry", "m", nil)

	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("handler 只被调用了 %d 次，报错的消息没有被重投", calls.Load())
	}
}

// 关掉 AutoAck 之后，handler 返回 nil 也不算确认 —— 必须自己调 Ack()。
//
// 这个开关在 http/memory 后端是不生效的装饰，在这里是真的。
func TestManualAck_RedeliversUntilAcked(t *testing.T) {
	b := newBroker(t, startRedis(t))

	var calls atomic.Int32
	acked := make(chan struct{})
	var once sync.Once

	if _, err := b.Subscribe("t.manual", func(e broker.IEvent) error {
		// 前两次故意不确认，第三次才确认
		if calls.Add(1) < 3 {
			return nil
		}
		if err := e.Ack(); err != nil {
			t.Errorf("Ack 失败: %v", err)
		}
		once.Do(func() { close(acked) })
		return nil
	}, broker.DisableAutoAck()); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	publish(t, b, "t.manual", "m", nil)

	select {
	case <-acked:
	case <-time.After(10 * time.Second):
		t.Fatalf("handler 被调用 %d 次，未确认的消息没有被重投", calls.Load())
	}

	// 确认之后就不该再重投了
	n := calls.Load()
	time.Sleep(1200 * time.Millisecond)
	if got := calls.Load(); got != n {
		t.Fatalf("确认后又被投了 %d 次（%d -> %d）", got-n, n, got)
	}
}

// AutoAck 默认是 true。
//
// 守的是一个很容易犯的错：自己 for-range 一个零值 SubscribeConfig（broker/memory
// 就是这么写的）会得到 AutoAck=false，在这里就变成"每条消息都不确认、
// ClaimMinIdle 之后全部重投一遍"，症状只是"消息偶尔重复"，很难往这上面想。
//
// 判据是**调用次数**而不是 PEL 是否清空：没确认的消息最终也会因为撞上
// MaxDeliveries 被丢弃并确认掉，只看 PEL 会在几秒后一样变空，用例就成了摆设。
func TestAutoAck_DefaultsToTrue(t *testing.T) {
	sock := startRedis(t)
	b := newBroker(t, sock)

	var calls atomic.Int32
	first := make(chan struct{}, 1)
	sub, err := b.Subscribe("t.autoack", func(e broker.IEvent) error {
		calls.Add(1)
		select {
		case first <- struct{}{}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	publish(t, b, "t.autoack", "m", nil)
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("5s 内没收到消息")
	}

	// 等过一整个重投窗口（ClaimMinIdle 300ms + ClaimInterval 100ms）
	time.Sleep(1500 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler 被调用 %d 次，期望 1 次 —— 消息没有被自动确认", n)
	}

	rs := sub.(*redisSubscriber)
	cli := goredis.NewClient(&goredis.Options{Network: "unix", Addr: sock})
	defer cli.Close()
	n, err := cli.XPending(context.Background(), rs.stream, rs.group).Result()
	if err != nil {
		t.Fatalf("XPending 失败: %v", err)
	}
	if n.Count != 0 {
		t.Fatalf("还有 %d 条待确认", n.Count)
	}
}

// 心跳要**持续**续期，不只是订阅时写一次。
//
// 不续期的话，广播组的心跳键到期即失效，下一个订阅这个 topic 的进程一扫就把它
// 清了；读循环随后从 NOGROUP 分支把组重建成 "$"，表现为"偶尔丢一段消息"。
func TestHeartbeat_IsRenewed(t *testing.T) {
	sock := startRedis(t)
	b := newBroker(t, sock, WithGroupTTL(400*time.Millisecond))

	sub, err := b.Subscribe("t.hb.renew", func(e broker.IEvent) error { return nil })
	if err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}
	rs := sub.(*redisSubscriber)

	cli := goredis.NewClient(&goredis.Options{Network: "unix", Addr: sock})
	defer cli.Close()
	key := heartbeatKey(rs.stream, rs.group)

	// 跨过好几个 TTL 周期都得还在
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		n, err := cli.Exists(context.Background(), key).Result()
		if err != nil {
			t.Fatalf("Exists 失败: %v", err)
		}
		if n == 0 {
			t.Fatalf("心跳键 %q 过期了 —— 读循环没有续期", key)
		}
	}
}

// 毒消息：投够 MaxDeliveries 次还没被确认就丢掉并出声。
//
// 关键不在"别再投它"，而在于它不能一直占着 PEL 的队头 —— 后面每一条真该重投的
// 消息都排在它身后。所以这个用例断言的是"毒消息停了，而且好消息照样过得去"。
func TestPoisonMessage_DroppedAndDoesNotBlockOthers(t *testing.T) {
	b := newBroker(t, startRedis(t), WithMaxDeliveries(2))

	var poison, good atomic.Int32
	okCh := make(chan struct{}, 1)

	if _, err := b.Subscribe("t.poison", func(e broker.IEvent) error {
		if string(e.Message().Body) == "poison" {
			poison.Add(1)
			return fmt.Errorf("永远处理不了")
		}
		good.Add(1)
		select {
		case okCh <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	publish(t, b, "t.poison", "poison", nil)
	if !waitFor(t, 8*time.Second, func() bool { return poison.Load() >= 2 }) {
		t.Fatalf("毒消息只被投了 %d 次，期望至少 2 次", poison.Load())
	}

	// 到上限之后应当被丢弃，不再增长
	time.Sleep(1500 * time.Millisecond)
	n := poison.Load()
	time.Sleep(1200 * time.Millisecond)
	if got := poison.Load(); got != n {
		t.Fatalf("毒消息在 MaxDeliveries=2 之后还在重投（%d -> %d）", n, got)
	}

	publish(t, b, "t.poison", "ok", nil)
	select {
	case <-okCh:
	case <-time.After(5 * time.Second):
		t.Fatal("毒消息把后面的好消息挡住了")
	}
}

// 退订之后不再收到消息。
//
// 故意用 **queue** 订阅：广播订阅退订时会把自己那个临时组销毁，而销毁会让阻塞中的
// XREADGROUP 立刻拿到 NOGROUP，"读循环有没有真的停下来"就被这一下掩盖过去了
// —— 读循环重建的组从 "$" 开始，正好也收不到后发的消息。queue 组不销毁，
// 于是这个用例真正测的是"读循环停了没有"。
func TestUnsubscribe_StopsDelivery(t *testing.T) {
	sock := startRedis(t)
	prefix := sharedPrefix(t)
	pub := newBroker(t, sock, prefix)
	b := newBroker(t, sock, prefix)

	var c collector
	sub, err := b.Subscribe("t.unsub", func(e broker.IEvent) error {
		c.add(string(e.Message().Body))
		return nil
	}, broker.Queue("unsub-workers"))
	if err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	publish(t, pub, "t.unsub", "before", nil)
	if !waitFor(t, 5*time.Second, func() bool { return c.len() == 1 }) {
		t.Fatal("退订前就收不到消息，用例前提不成立")
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe 失败: %v", err)
	}
	// 幂等
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("重复 Unsubscribe 报错: %v", err)
	}

	publish(t, pub, "t.unsub", "after", nil)
	time.Sleep(1 * time.Second)
	if got := c.snapshot(); len(got) != 1 {
		t.Fatalf("退订后仍收到消息: %v", got)
	}
}

// 退掉一个广播订阅，同 topic 上剩下的那个必须照常收消息。
//
// 每个广播订阅各有自己的消费组，所以退订销毁的必须是**自己那个组**。
func TestUnsubscribe_OneOfTwoKeepsTheOtherAlive(t *testing.T) {
	sock := startRedis(t)
	prefix := sharedPrefix(t)
	pub := newBroker(t, sock, prefix)
	b := newBroker(t, sock, prefix)

	var first, second collector
	sub1, err := b.Subscribe("t.two", func(e broker.IEvent) error {
		first.add(string(e.Message().Body))
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}
	if _, err := b.Subscribe("t.two", func(e broker.IEvent) error {
		second.add(string(e.Message().Body))
		return nil
	}); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	if err := sub1.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe 失败: %v", err)
	}

	publish(t, pub, "t.two", "m", nil)
	if !waitFor(t, 5*time.Second, func() bool { return second.len() == 1 }) {
		t.Fatal("退订一个订阅者后，同 topic 上剩下的那个也收不到了")
	}
	if first.len() != 0 {
		t.Fatalf("已退订的订阅者还收到了 %d 条", first.len())
	}
}

// 默认只收订阅之后发布的消息；WithReadFromOldest 才回放历史。
func TestReadFromOldest(t *testing.T) {
	sock := startRedis(t)
	prefix := sharedPrefix(t)
	pub := newBroker(t, sock, prefix)

	publish(t, pub, "t.history", "old", nil)

	fresh := newBroker(t, sock, prefix)
	var skipped collector
	if _, err := fresh.Subscribe("t.history", func(e broker.IEvent) error {
		skipped.add(string(e.Message().Body))
		return nil
	}); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	replay := newBroker(t, sock, prefix, WithReadFromOldest())
	var replayed collector
	if _, err := replay.Subscribe("t.history", func(e broker.IEvent) error {
		replayed.add(string(e.Message().Body))
		return nil
	}); err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}

	if !waitFor(t, 5*time.Second, func() bool { return replayed.len() == 1 }) {
		t.Fatal("WithReadFromOldest 没有回放订阅之前的消息")
	}
	if got := skipped.snapshot(); len(got) != 0 {
		t.Fatalf("默认订阅收到了订阅之前的消息: %v", got)
	}
}

// 没有订阅者时 Publish 不算失败，消息留在 stream 里。
func TestPublish_WithoutSubscribersIsNotAnError(t *testing.T) {
	b := newBroker(t, startRedis(t))
	if err := b.Publish("t.nobody", &broker.Message{Body: []byte("m")}); err != nil {
		t.Fatalf("没有订阅者时 Publish 报错: %v", err)
	}
}

// 僵尸广播组清理：靠心跳键判活。
//
// 三个组都**没有消费者**——这正是低流量订阅者的常态：redis 7.0.4 上，一次没读到
// 数据的 XREADGROUP 既不注册消费者、也不刷新 seen-time（实测 idle 151ms -> 209ms
// 照涨）。所以"没有消费者/闲置很久"完全区分不出活人和死人，只有心跳键能。
//
// 用例里不放真的订阅者，是因为它会把故障掩盖掉：XGROUP DESTROY 会让阻塞中的
// XREADGROUP 立刻拿到 NOGROUP，读循环随即把组重建回来，事后去数组的数量就什么
// 都看不出 —— 真实故障表现为读取位置被悄悄重置成 "$"，也就是"偶尔丢一段消息"。
func TestSweepStaleGroups(t *testing.T) {
	sock := startRedis(t)
	b := newBroker(t, sock, WithGroupTTL(30*time.Second)).(*redisBroker)
	ctx := context.Background()
	cli := b.conn()
	stream := b.stream("t.sweep")

	alive := broadcastGroupPrefix + "alive:0" // 有心跳
	dead := broadcastGroupPrefix + "dead:0"   // 没心跳
	queue := queueGroupPrefix + "workers"     // 没心跳，但跨进程共享，不该动

	for _, g := range []string{alive, dead, queue} {
		if err := ensureGroup(ctx, cli, stream, g, "$"); err != nil {
			t.Fatalf("建组 %s 失败: %v", g, err)
		}
	}
	if err := cli.Set(ctx, heartbeatKey(stream, alive), "c1", 30*time.Second).Err(); err != nil {
		t.Fatalf("写心跳键失败: %v", err)
	}

	b.sweepStaleGroups(ctx, cli, stream)

	groups, err := cli.XInfoGroups(ctx, stream).Result()
	if err != nil {
		t.Fatalf("XInfoGroups 失败: %v", err)
	}
	remaining := map[string]bool{}
	for _, g := range groups {
		remaining[g.Name] = true
	}
	if remaining[dead] {
		t.Errorf("没有心跳的广播组 %q 没有被清理", dead)
	}
	if !remaining[alive] {
		t.Errorf("有心跳的广播组 %q 被清掉了 —— 它只是没收到过消息，不是死了", alive)
	}
	if !remaining[queue] {
		t.Errorf("queue 组 %q 被清理了 —— 它是跨进程共享的，本进程无从判断别人还在不在用", queue)
	}
}

// 真的订阅必须落下心跳键，否则别的进程一扫就把它的组清了。
func TestSubscribe_WritesHeartbeat(t *testing.T) {
	sock := startRedis(t)
	b := newBroker(t, sock, WithGroupTTL(30*time.Second))

	sub, err := b.Subscribe("t.hb", func(e broker.IEvent) error { return nil })
	if err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}
	rs := sub.(*redisSubscriber)

	cli := goredis.NewClient(&goredis.Options{Network: "unix", Addr: sock})
	defer cli.Close()

	if !waitFor(t, 5*time.Second, func() bool {
		n, err := cli.Exists(context.Background(), heartbeatKey(rs.stream, rs.group)).Result()
		return err == nil && n > 0
	}) {
		t.Fatalf("订阅之后没有心跳键 %q", heartbeatKey(rs.stream, rs.group))
	}

	// queue 订阅不写心跳：它的组是跨进程共享的，本来就不参与清理
	qsub, err := b.Subscribe("t.hb.q", func(e broker.IEvent) error { return nil }, broker.Queue("w"))
	if err != nil {
		t.Fatalf("Subscribe 失败: %v", err)
	}
	qs := qsub.(*redisSubscriber)
	time.Sleep(400 * time.Millisecond)
	if n, _ := cli.Exists(context.Background(), heartbeatKey(qs.stream, qs.group)).Result(); n > 0 {
		t.Errorf("queue 订阅不该写心跳键")
	}
}

func TestDecodeMessage(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
		ok     bool
		body   string
		header map[string]string
	}{
		{
			name:   "正常",
			values: map[string]any{fieldHeader: `{"a":"b"}`, fieldBody: "x"},
			ok:     true, body: "x", header: map[string]string{"a": "b"},
		},
		{
			name:   "空 header",
			values: map[string]any{fieldHeader: "{}", fieldBody: ""},
			ok:     true, body: "",
		},
		{
			name:   "缺 body 字段",
			values: map[string]any{fieldHeader: "{}"},
		},
		{
			name:   "header 不是 JSON",
			values: map[string]any{fieldHeader: "不是json", fieldBody: "x"},
		},
		{
			name:   "header 类型不对",
			values: map[string]any{fieldHeader: 12, fieldBody: "x"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, err := decodeMessage(goredis.XMessage{ID: "1-1", Values: c.values})
			if c.ok != (err == nil) {
				t.Fatalf("err = %v，期望 ok=%v", err, c.ok)
			}
			if !c.ok {
				return
			}
			if string(msg.Body) != c.body {
				t.Fatalf("body = %q，期望 %q", msg.Body, c.body)
			}
			for k, v := range c.header {
				if msg.Header[k] != v {
					t.Fatalf("header[%s] = %q，期望 %q", k, msg.Header[k], v)
				}
			}
		})
	}
}

func TestBuildClient(t *testing.T) {
	t.Run("裸地址", func(t *testing.T) {
		cfg := &broker.Config{Addrs: []string{"10.0.0.5:6380"}}
		_, addr, err := buildClient(cfg, newOptions())
		if err != nil || addr != "10.0.0.5:6380" {
			t.Fatalf("addr = %q, err = %v", addr, err)
		}
	})

	t.Run("URL 里的密码和库号", func(t *testing.T) {
		cfg := &broker.Config{Addrs: []string{"redis://user:pw@10.0.0.5:6379/3"}}
		cli, _, err := buildClient(cfg, newOptions())
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		o := cli.(*goredis.Client).Options()
		if o.Password != "pw" || o.Username != "user" || o.DB != 3 {
			t.Fatalf("user=%q pass=%q db=%d，期望 user/pw/3", o.Username, o.Password, o.DB)
		}
	})

	t.Run("显式选项覆盖 URL", func(t *testing.T) {
		cfg := &broker.Config{Addrs: []string{"redis://user:pw@10.0.0.5:6379/3"}}
		opts := newOptions()
		opts.Password, opts.DB = "override", 7
		cli, _, err := buildClient(cfg, opts)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		o := cli.(*goredis.Client).Options()
		if o.Password != "override" || o.DB != 7 {
			t.Fatalf("pass=%q db=%d，期望 override/7", o.Password, o.DB)
		}
	})

	t.Run("端口 0 不是地址", func(t *testing.T) {
		// DefaultAddress 就是 "127.0.0.1:0"，server 的 WithBroker 会把它回填进
		// config.json。当成真地址去连只会连不上，回落到默认端口才是对的。
		cfg := &broker.Config{Addrs: []string{"127.0.0.1:0"}}
		_, addr, err := buildClient(cfg, newOptions())
		if err != nil || addr != DefaultAddr {
			t.Fatalf("addr = %q, err = %v，期望回落到 %s", addr, err, DefaultAddr)
		}
	})

	t.Run("坏 URL 要报错", func(t *testing.T) {
		cfg := &broker.Config{Addrs: []string{"http://10.0.0.5:6379"}}
		if _, _, err := buildClient(cfg, newOptions()); err == nil {
			t.Fatal("非 redis 协议的地址没有报错")
		}
	})
}
