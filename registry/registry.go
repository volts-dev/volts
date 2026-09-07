package registry

import (
	"errors"
	"fmt"
	"strings"
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

	// Endpoint 描述一个对外暴露的接口。整个 Endpoint 列表会随服务注册写进注册
	// 中心，是「各微服务之间共享接口」的载体：网关据此为远端服务的 HTTP 路径建
	// 反向代理路由（router.store），registryCacher 据此做「路径 → 服务」反查，
	// OpenAPI 网关据此聚合全景文档。
	//
	// **所有可空字段一律 omitempty**：真栈实测 4284 条端点里，
	// description/metadata/stream/Handler/host/Body 这六个字段**无一例外全是空值**
	// （""/null/false），却占掉整个 endpoints 载荷的 28%——每条都原样搬一遍
	// `"description":"","metadata":null,"stream":false,...`。注册中心的每次
	// 注册、每个 watch 事件、每次全量 ListServices 都要搬运它们。
	// omitempty 只影响编码：读侧缺键即零值，与此前收到空值完全等价，跨版本双向兼容。
	Endpoint struct {
		// RPC Method e.g. Greeter.Hello
		Name string `json:"name"`
		// HTTP Host e.g example.com
		Host []string `json:"host,omitempty"`
		// HTTP Methods e.g GET, POST
		Method []string `json:"method,omitempty"`
		// HTTP Path e.g /greeter. Expect POSIX regex
		Path string `json:"path"`
		// Description e.g what's this endpoint for
		Description string `json:"description,omitempty"`
		// Tags 用于 OpenAPI 分组（omitempty 保持向后兼容）
		Tags []string `json:"tags,omitempty"`
		// Stream flag
		Stream bool `json:"stream,omitempty"`

		// 以下待确认
		Request  *Value            `json:"request,omitempty"`
		Response *Value            `json:"response,omitempty"`
		Metadata map[string]string `json:"metadata,omitempty"`

		// API Handler e.g rpc, proxy
		//
		// 补 json tag 是为了带上 omitempty。改成小写键不影响兼容：Go 的
		// json 解码匹配字段名不区分大小写，旧数据里的 "Handler"/"Body"
		// 仍能解进来。
		Handler string `json:"handler,omitempty"`
		// Body destination
		// "*" or "" - top level message value
		// "string" - inner message value
		Body string `json:"body,omitempty"`
	}

	Value struct {
		Name string `json:"name"`
		Type string `json:"type"`
		// 叶子类型（string/bool/int…）没有子字段，而请求/响应的 schema 里
		// 绝大多数节点都是叶子：不加 omitempty 每个叶子都要多带 `"values":null`。
		Values []*Value `json:"values,omitempty"`

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
	// 这个 cfg 只用来把 opts 解析出 Addrs 以便查实例缓存，随后丢弃；它不绑定到配置树。
	cfg := newCacheConfig(append([]Option{WithName(name)}, opts...)...)

	// 加载存在的服务
	for _, addr := range cfg.Addrs {
		if addr == "" {
			continue
		}

		regName := fmt.Sprintf("%s-%s", cfg.Name, addr)
		if reg, has := registryHost.Load(regName); has {
			existing := reg.(IRegistry)
			warnConfigDivergence(regName, cfg, existing.Config())
			return existing
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

// warnConfigDivergence 在复用已有实例时，比对调用方期望的配置与实例实际生效的配置。
//
// registry 实例按 name-addr 复用，但每个持有者（server / client / router / selector）
// 都会带着自己那份配置来调 Use。实例只有一份运行期状态，所以**先创建者的配置生效，
// 后来者的被静默丢弃** —— 后来者的 option 全应用在上面那个随即被丢弃的临时 cfg 上。
//
// 于是配置文件里会出现同一个实例的多份配置且取值不一致（kylin 实测 ttl 在 16 个
// registry 节里有 0 和 60000 两种），而改哪一份生效取决于谁先创建。这类「改了配置
// 不生效」改对了地方也可能不生效，是最难排查的一种，所以这里必须出声。
func warnConfigDivergence(regName string, want, got *Config) {
	diffs := divergence(want, got)
	if len(diffs) == 0 {
		return
	}

	log.Warnf("registry %q 已被复用（同名同地址即同一实例），配置以先创建者为准；"+
		"本次调用（registry.%s）传入的下列设置不会生效：\n  %s",
		regName, want.Name, strings.Join(diffs, "\n  "))
}

// divergence 列出调用方期望的配置与实例现有配置之间的差异。
//
// 只比对**调用方显式设过**的字段（非零值）：零值意味着调用方没表态，不是「要求设成 0」。
// 把未表态当成分歧会让每个不传 option 的持有者都触发一次告警，噪音淹没真问题。
func divergence(want, got *Config) []string {
	if want == nil || got == nil {
		return nil
	}

	var diffs []string
	if want.Timeout != 0 && want.Timeout != got.Timeout {
		diffs = append(diffs, fmt.Sprintf("timeout: 期望 %v，实际 %v", want.Timeout, got.Timeout))
	}
	if want.TTL != 0 && want.TTL != got.TTL {
		diffs = append(diffs, fmt.Sprintf("ttl: 期望 %v，实际 %v", want.TTL, got.TTL))
	}
	if want.Secure && want.Secure != got.Secure {
		diffs = append(diffs, fmt.Sprintf("secure: 期望 %v，实际 %v", want.Secure, got.Secure))
	}
	return diffs
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
