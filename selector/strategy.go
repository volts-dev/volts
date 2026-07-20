package selector

import (
	"math/rand"
	"strings"
	"sync/atomic"

	"github.com/volts-dev/volts/registry"
)

var strategyMap = map[string]func(services []*registry.Service) Next{
	"random":     Random,
	"roundrobin": RoundRobin,
}

func Register(name string, fn func(services []*registry.Service) Next) {
	strategyMap[strings.ToLower(name)] = fn
}

func Use(name string) func(services []*registry.Service) Next {
	if fn, has := strategyMap[strings.ToLower(name)]; has {
		return fn
	}
	return nil
}

// Random is a random strategy algorithm for node selection
func Random(services []*registry.Service) Next {
	nodes := make([]*registry.Node, 0, len(services))

	for _, service := range services {
		nodes = append(nodes, service.Nodes...)
	}

	return func() (*registry.Node, error) {
		if len(nodes) == 0 {
			return nil, ErrNoneAvailable
		}

		i := rand.Int() % len(nodes)
		return nodes[i], nil
	}
}

// roundRobinSeq 是 RoundRobin 的轮询序号。
//
// 必须是包级的：Selector.Select 每次调用都会新建一份 Next 闭包，而调用方
// 通常只调一次 next() 就丢弃。此前计数器是闭包的局部变量、初值还是
// rand.Int()，于是永远停在初值上 —— RoundRobin 实际退化成了 Random，
// 名不副实且每次 Select 白白多分配一个 slice 和一个 mutex。
//
// 代价是序号跨服务共享：单服务场景即标准轮询；多服务并发场景下各服务看到的是
// 交错的序号，但对每个服务而言 i%len(nodes) 依然均匀覆盖全部节点，
// 负载分布不受影响。
var roundRobinSeq atomic.Uint64

// RoundRobin is a roundrobin strategy algorithm for node selection
func RoundRobin(services []*registry.Service) Next {
	nodes := make([]*registry.Node, 0, len(services))

	for _, service := range services {
		nodes = append(nodes, service.Nodes...)
	}

	return func() (*registry.Node, error) {
		if len(nodes) == 0 {
			return nil, ErrNoneAvailable
		}

		i := roundRobinSeq.Add(1) - 1
		return nodes[i%uint64(len(nodes))], nil
	}
}
