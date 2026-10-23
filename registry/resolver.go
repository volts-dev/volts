package registry

import (
	"sync"
	"time"
)

// resolver 包在 Use() 返回的每个后端外面：注册时把描述剥成一个哈希，读出时按哈希补全。
// 调用方看到的仍是完整的 Service；后端只存瘦身后的记录。设计见 descriptor.go 与
// docs/registry_descriptor_design.md。
type resolver struct {
	IRegistry
}

// wrapResolver 给后端套上描述解析层。已经套过的原样返回。
func wrapResolver(r IRegistry) IRegistry {
	if r == nil {
		return nil
	}
	if _, ok := r.(*resolver); ok {
		return r
	}
	return &resolver{IRegistry: r}
}

// Unwrap 返回被包着的后端（诊断与测试用）。
func (r *resolver) Unwrap() IRegistry { return r.IRegistry }

// Register 剥离描述后交给后端。三种情况照旧写完整记录：
//   - 本进程不在答描述（只跑 HTTP 服务器）—— 别人取不到；
//   - 记录本来就没有描述（broker 订阅记录：只有版本与节点 metadata）—— 剥了反而多一次取；
//   - 已经是瘦身记录。
func (r *resolver) Register(s *Service, opts ...Option) error {
	if s == nil || !DescriptorServing() || s.Metadata[MetaDescriptor] != "" ||
		(len(s.Metadata) == 0 && len(s.Endpoints) == 0) {
		return r.IRegistry.Register(s, opts...)
	}
	raw, digest, err := Describe(s)
	if err != nil {
		return err
	}
	// 先登记、后写后端：别人一看到记录就可能来取。
	for _, n := range s.Nodes {
		if n != nil {
			publishLocal(s.Name, n.Id, digest, raw)
		}
	}
	rememberLocal(s.Name, digest, raw)

	return r.IRegistry.Register(&Service{
		Name:     s.Name,
		Version:  s.Version,
		Metadata: map[string]string{MetaDescriptor: digest},
		Nodes:    s.Nodes,
	}, opts...)
}

// Deregister 放掉这些节点占着的描述，再交给后端。
func (r *resolver) Deregister(s *Service, opts ...Option) error {
	if s != nil {
		for _, n := range s.Nodes {
			if n != nil {
				unpublishLocal(s.Name, n.Id)
			}
		}
	}
	return r.IRegistry.Deregister(s, opts...)
}

func (r *resolver) GetService(name string) ([]*Service, error) {
	list, err := r.IRegistry.GetService(name)
	if err != nil {
		return list, err
	}
	out := resolveAll(list)
	if len(out) == 0 && len(list) > 0 {
		// 记录在、描述一份都取不到：与「节点还没起来」同义。
		return nil, ErrNotFound
	}
	return out, nil
}

func (r *resolver) ListServices() ([]*Service, error) {
	list, err := r.IRegistry.ListServices()
	if err != nil {
		return list, err
	}
	return resolveAll(list), nil
}

func (r *resolver) Watcher(opts ...WatchOptions) (Watcher, error) {
	w, err := r.IRegistry.Watcher(opts...)
	if err != nil || w == nil {
		return w, err
	}
	return &resolvingWatcher{Watcher: w}, nil
}

// resolveAll 补全一批记录。缓存里没有的哈希先批量取一次；还是没有的，引用它的记录不出现
// 在结果里。没有哈希的记录原样放行。
func resolveAll(in []*Service) []*Service {
	if len(in) == 0 {
		return in
	}
	sweepIdle()

	var want map[string]string
	var nodes map[string][]*Node
	for _, s := range in {
		d := DigestOf(s)
		if d == "" || cached(d) != nil {
			continue
		}
		if want == nil {
			want, nodes = map[string]string{}, map[string][]*Node{}
		}
		want[d] = s.Name
		nodes[d] = append(nodes[d], s.Nodes...)
	}
	fetchDescriptors(want, nodes)

	out := make([]*Service, 0, len(in))
	for _, s := range in {
		if s == nil {
			continue
		}
		d := DigestOf(s)
		if d == "" {
			out = append(out, s)
			continue
		}
		dd := cached(d)
		if dd == nil {
			unresolvedWarn(s.Name, d)
			continue
		}
		out = append(out, assemble(s, d, dd))
	}
	return out
}

func assemble(s *Service, digest string, dd *decodedDesc) *Service {
	return &Service{
		Name:      s.Name,
		Version:   s.Version,
		Metadata:  dd.metadata,
		Endpoints: dd.endpoints,
		Nodes:     s.Nodes,
		Digest:    digest,
	}
}

// resolvingWatcher 补全 watch 事件里的记录。create / update 补不全就跳过这条事件 ——
// 下一轮全量刷新会补上，交出一条没有描述的记录反倒会让下游以为端点全没了。delete 不需要
// 描述：带上 Digest 交出去，分组键对得上就够了。
type resolvingWatcher struct {
	Watcher
}

func (w *resolvingWatcher) Next() (*Result, error) {
	for {
		res, err := w.Watcher.Next()
		if err != nil || res == nil || res.Service == nil {
			return res, err
		}
		d := DigestOf(res.Service)
		if d == "" {
			return res, nil
		}
		if res.Action == "delete" {
			if dd := cached(d); dd != nil {
				return &Result{Action: res.Action, Service: assemble(res.Service, d, dd)}, nil
			}
			cp := *res.Service
			cp.Digest = d
			return &Result{Action: res.Action, Service: &cp}, nil
		}
		got := resolveAll([]*Service{res.Service})
		if len(got) == 0 {
			continue
		}
		return &Result{Action: res.Action, Service: got[0]}, nil
	}
}

var (
	unresolvedMu   sync.Mutex
	unresolvedLast = map[string]time.Time{}
)

// unresolvedWarn 限频地记一条「这条记录的描述取不到」：同一 (服务, 哈希) 每分钟一条。
func unresolvedWarn(name, digest string) {
	key := name + "\x00" + digest
	now := time.Now()
	unresolvedMu.Lock()
	defer unresolvedMu.Unlock()
	if t, ok := unresolvedLast[key]; ok && now.Sub(t) < time.Minute {
		return
	}
	if len(unresolvedLast) > 4096 {
		unresolvedLast = map[string]time.Time{}
	}
	unresolvedLast[key] = now
	log.Warnf("registry: 服务 %q 的描述 %s 取不到，这条记录暂不可见", name, short(digest))
}
