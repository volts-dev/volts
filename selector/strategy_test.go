package selector

import (
	"sync"
	"testing"

	"github.com/volts-dev/volts/registry"
)

// RoundRobin 必须真的轮询。
//
// 此前计数器是 Next 闭包的局部变量、初值还是 rand.Int()，而 Selector.Select
// 每次调用都新建一份闭包、调用方通常只调一次 next() 就丢弃 —— 计数器永远停在
// 初值，RoundRobin 实际退化成了 Random。
//
// 这里模拟真实调用形态：每次请求都重新 Select 再调一次 next()。
func TestRoundRobinRotatesAcrossSelects(t *testing.T) {
	services := []*registry.Service{svc("svc", "a", "b", "c")}

	seen := map[string]int{}
	for i := 0; i < 30; i++ {
		next := RoundRobin(services) // 每次请求都新建，等价于 Select 的行为
		node, err := next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		seen[node.Id]++
	}

	if len(seen) != 3 {
		t.Fatalf("30 次选取只覆盖了 %d 个节点 %v，未在轮询", len(seen), seen)
	}
	for id, n := range seen {
		if n != 10 {
			t.Errorf("节点 %s 被选中 %d 次，轮询应为 10 次（%v）", id, n, seen)
		}
	}
}

// 同一个 Next 闭包内连续调用也应依次推进。
func TestRoundRobinRotatesWithinNext(t *testing.T) {
	next := RoundRobin([]*registry.Service{svc("svc", "a", "b")})

	first, _ := next()
	second, _ := next()

	if first.Id == second.Id {
		t.Fatalf("连续两次选到同一节点 %s，未轮询", first.Id)
	}
}

// 空节点集返回 ErrNoneAvailable 而非 panic（取模会除零）。
func TestRoundRobinEmpty(t *testing.T) {
	next := RoundRobin(nil)
	if _, err := next(); err != ErrNoneAvailable {
		t.Fatalf("err = %v, want ErrNoneAvailable", err)
	}
}

func TestRandomEmpty(t *testing.T) {
	next := Random(nil)
	if _, err := next(); err != ErrNoneAvailable {
		t.Fatalf("err = %v, want ErrNoneAvailable", err)
	}
}

// 并发选取不得数据竞争（配合 -race）。
func TestRoundRobinConcurrent(t *testing.T) {
	services := []*registry.Service{svc("svc", "a", "b", "c")}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := RoundRobin(services)
			for j := 0; j < 20; j++ {
				if _, err := next(); err != nil {
					t.Errorf("next: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
