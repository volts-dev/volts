package selector

import (
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/volts/registry"
)

var strategyMap = map[string]func(services []*registry.Service) Next{
	"random":     Random,
	"roundrobin": RoundRobin,
}

func init() {
	rand.Seed(time.Now().UnixNano())
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

// RoundRobin is a roundrobin strategy algorithm for node selection
func RoundRobin(services []*registry.Service) Next {
	nodes := make([]*registry.Node, 0, len(services))

	for _, service := range services {
		nodes = append(nodes, service.Nodes...)
	}

	var i = rand.Int()
	var mtx sync.Mutex

	return func() (*registry.Node, error) {
		if len(nodes) == 0 {
			return nil, ErrNoneAvailable
		}

		mtx.Lock()
		node := nodes[i%len(nodes)]
		i++
		mtx.Unlock()

		return node, nil
	}
}
