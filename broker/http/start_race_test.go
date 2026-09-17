package http

import (
	"strings"
	"sync"
	"testing"

	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/registry/memory"
)

// 一个进程里两台 server（HTTP + RPC）共用**同一个** broker 实例（broker.Use 的裸名
// 缓存），它们的 Start 是并发的。`httpBroker.Start` 的 started 判定曾经只在**锁外**
// 做一次，于是两条 goroutine 能同时看到 false：先拿到锁的那条把 `:0` 绑成一个真端口
// 并写回 self.address，后拿到锁的那条不再判，去 Listen 同一个端口，撞 EADDRINUSE。
//
// 那个错误会让**那台 server 整个退出**，而进程还活着、另一半协议面还在：
//
//	[service][ERR] RpcServer Exit with Error:listen tcp 0.0.0.0:55464: address already in use
//
// 表现是"跨进程读这个服务的模型一律 service none available"或"页面 502"，
// 而两边的业务日志都干净，`run.sh status` 也是绿的。谁输是随机的。
//
// 判据是**并发 Start 全部返回 nil**，不是"第二次 Start 返回 nil" —— 后者走的是锁外
// 那条短路，修之前也照样通过。所以这条测试必须真的并发，且要跑够多轮：单轮命中率
// 不高，2026-09-08 真栈上是几次重启里撞一次。
func TestStart_ConcurrentIsIdempotent(t *testing.T) {
	const rounds = 60
	const goroutines = 8

	for r := 0; r < rounds; r++ {
		b := New(broker.WithRegistry(memory.New()))
		var wg sync.WaitGroup
		errs := make([]error, goroutines)
		start := make(chan struct{})
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = b.Start()
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				b.Close()
				t.Fatalf("第 %d 轮：并发 Start 的第 %d 条返回了错误 %v\n"+
					"（address already in use = 锁内没有再判一次 started，"+
					"真栈上的后果是那台 server 整个退出而进程还活着）", r, i, err)
			}
		}
		// 绑上的地址必须是**一个真端口**：所有人看到的是同一份状态。
		if addr := b.Address(); addr == "" || strings.HasSuffix(addr, ":0") {
			b.Close()
			t.Fatalf("第 %d 轮：Start 之后地址仍是 %q", r, addr)
		}
		if err := b.Close(); err != nil {
			t.Fatalf("第 %d 轮：Close 失败: %v", r, err)
		}
	}
}
