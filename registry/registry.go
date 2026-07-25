package registry

import (
	"errors"
	"fmt"
	"sync"

	"github.com/volts-dev/volts/logger"
)

var (
	log               = logger.New("Registry")
	defaultRegistry   IRegistry
	ErrNotFound       = errors.New("service not found") // Not found error when GetService is called
	ErrWatcherStopped = errors.New("watcher stopped")   // Watcher stopped error when watcher is stopped
	registryMap       = make(map[string]func(opts ...Option) IRegistry)
	registryHost      sync.Map
)

type (
	// 注册中心接口
	IRegistry interface {
		Init(...Option) error
		Config() *Config
		Register(*Service, ...Option) error   // 注册
		Deregister(*Service, ...Option) error // 注销
		GetService(string) ([]*Service, error)
		ListServices() ([]*Service, error)
		Watcher(...WatchOptions) (Watcher, error)
		String() string
	}

	// 微服务
	Service struct {
		Name      string            `json:"name"`
		Version   string            `json:"version"`
		Metadata  map[string]string `json:"metadata"`
		Endpoints []*Endpoint       `json:"endpoints"`
		Nodes     []*Node           `json:"nodes"`
	}

	// 微服务节点 相当于每个程序/Port一个节点
	Node struct {
		Id       string            `json:"id"`
		Address  string            `json:"address"`
		Metadata map[string]string `json:"metadata"`
	}

	Endpoint struct {
		// RPC Method e.g. Greeter.Hello
		Name string `json:"name"`
		// HTTP Host e.g example.com
		Host []string `json:"host"`
		// HTTP Methods e.g GET, POST
		Method []string `json:"method"`
		// HTTP Path e.g /greeter. Expect POSIX regex
		Path string `json:"path"`
		// Description e.g what's this endpoint for
		Description string `json:"description"`
		// Tags 用于 OpenAPI 分组（omitempty 保持向后兼容）
		Tags []string `json:"tags,omitempty"`
		// Stream flag
		Stream bool `json:"stream"`

		// 以下待确认
		Request  *Value            `json:"request"`
		Response *Value            `json:"response"`
		Metadata map[string]string `json:"metadata"`

		// API Handler e.g rpc, proxy
		Handler string
		// Body destination
		// "*" or "" - top level message value
		// "string" - inner message value
		Body string
	}

	Value struct {
		Name   string   `json:"name"`
		Type   string   `json:"type"`
		Values []*Value `json:"values"`

		// OpenAPI 扩展字段（向后兼容：omitempty）
		Required    bool     `json:"required,omitempty"`
		Format      string   `json:"format,omitempty"`
		Enum        []string `json:"enum,omitempty"`
		Description string   `json:"description,omitempty"`
		In          string   `json:"in,omitempty"` // path|query|header|body
	}
)

func Register(name string, creator func(opts ...Option) IRegistry) {
	registryMap[name] = creator
}

func Use(name string, opts ...Option) IRegistry {
	cfg := &Config{Name: name}
	cfg.Init(opts...)

	// 加载存在的服务
	for _, addr := range cfg.Addrs {
		if addr == "" {
			continue
		}

		regName := fmt.Sprintf("%s-%s", cfg.Name, addr)
		if reg, has := registryHost.Load(regName); has {
			return reg.(IRegistry)
		}
	}

	if fn, has := registryMap[name]; has {
		reg := fn(opts...)
		for _, addr := range reg.Config().Addrs {
			if addr == "" {
				continue
			}

			regName := fmt.Sprintf("%s-%s", cfg.Name, addr)
			registryHost.Store(regName, reg)
		}
		return reg
	}

	return NewNopRegistry()
}

// NopRegistry as default registry
func Default(new ...IRegistry) IRegistry {
	if new != nil {
		defaultRegistry = new[0]
	} else if defaultRegistry == nil {
		defaultRegistry = NewNopRegistry()
	}

	return defaultRegistry
}

// Equal 报告两个服务是否为「同名同版本、且节点集合完全一致」的同一个服务。
//
// ★ 历史 bug（跨进程调用时好时坏的根因之一）：节点数不等时，判断整个被 if 跳过，
// 直接落到结尾的 `return true` —— 判定为「相等」；名称也完全不参与比较。
// TRouter.isLocalService 用它把「本进程自己托管的服务」排除在建代理路由之外，于是
// 只要某个远程服务的节点数与任意一个本地服务不同，它就被误判成本地服务，
// refresh() 随即 continue 跳过，永远不为它建代理路由 —— 调用方拿不到路由，
// 表现为 404 / 500 / 超时。
//
// 注册中心里存在僵尸节点时必然触发：进程被 SIGKILL（调试停止、kill -9）后 lease
// 不会主动撤销，同名服务下新旧节点并存（节点数翻倍），与本地快照长度不等。
// lease 过期后又恢复正常 —— 这正是「时好时坏」的由来。
//
// 判定方向刻意取严：宁可返回 false 多走一次 store()，那里的 filteSelf 会按
// node.Id + 归一化地址精确剔除自身节点；返回 true 则是直接跳过，一旦误判就永久
// 缺路由，代价不对等。
func (self *Service) Equal(to *Service) bool {
	// noop registry 会返回 nil，这里需要判断
	if self == nil || to == nil {
		return self == to
	}

	if self.Name != to.Name || self.Version != to.Version {
		return false
	}

	if len(self.Nodes) != len(to.Nodes) {
		return false
	}

	for _, node := range self.Nodes {
		match := false
		for _, n := range to.Nodes {
			if node.Id == n.Id {
				match = true
				break
			}
		}

		if !match {
			return false
		}
	}

	return true
}
