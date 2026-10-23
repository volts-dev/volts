package registry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetDescriptors 把进程级的描述状态清干净，并在测试结束时恢复。描述表、缓存、开关都是
// 进程级的（一个进程只有一份），测试之间不清就会互相看见。
func resetDescriptors(t *testing.T) {
	t.Helper()
	clear := func() {
		SetDescriptorServing(false)
		SetDescriptorSource(nil)
		localDescs.Range(func(k, _ any) bool { localDescs.Delete(k); return true })
		descCache.Range(func(k, _ any) bool { descCache.Delete(k); return true })
		localMu.Lock()
		localRefs, localUse = map[string]string{}, map[string]int{}
		localMu.Unlock()
		failedMu.Lock()
		failedAt = map[string]time.Time{}
		failedMu.Unlock()
	}
	prev := source.Load()
	clear()
	t.Cleanup(func() {
		clear()
		source.Store(prev)
	})
}

// storeRegistry 像 etcd 那样一条记录一个节点地存，原样交回 —— 解析层之下的「后端」。
type storeRegistry struct {
	IRegistry
	mu   sync.Mutex
	recs []*Service
	w    *chanWatcher
}

func (r *storeRegistry) Register(s *Service, _ ...Option) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, CopyService(s))
	return nil
}
func (r *storeRegistry) Deregister(*Service, ...Option) error { return nil }
func (r *storeRegistry) GetService(name string) ([]*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Service
	for _, s := range r.recs {
		if s.Name == name {
			out = append(out, CopyService(s))
		}
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}
func (r *storeRegistry) ListServices() ([]*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Copy(r.recs), nil
}
func (r *storeRegistry) Watcher(...WatchOptions) (Watcher, error) { return r.w, nil }
func (r *storeRegistry) String() string                           { return "store" }

func (r *storeRegistry) last() *Service {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recs[len(r.recs)-1]
}

type chanWatcher struct{ ch chan *Result }

func (w *chanWatcher) Next() (*Result, error) {
	res, ok := <-w.ch
	if !ok {
		return nil, ErrWatcherStopped
	}
	return res, nil
}
func (w *chanWatcher) Stop() {}

func sampleService(name, node string) *Service {
	return &Service{
		Name:     name,
		Version:  "latest",
		Metadata: map[string]string{"models": `{"sale.order":{}}`, "depends": "base"},
		Endpoints: []*Endpoint{
			{Name: "/" + name + "/a", Method: []string{"GET"}, Path: "/" + name + "/a"},
			{Name: "/" + name + ".read", Method: []string{"CONNECT"}, Path: "/" + name + ".read"},
		},
		Nodes: []*Node{{Id: node, Address: "192.0.2.1:46000", Metadata: map[string]string{"protocol": "TCP"}}},
	}
}

// stripped 造一条「别的进程」写的瘦身记录，返回它与描述的规范字节。
func stripped(t *testing.T, s *Service) (*Service, []byte) {
	t.Helper()
	raw, d, err := Describe(s)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Name: s.Name, Version: s.Version, Metadata: map[string]string{MetaDescriptor: d}, Nodes: s.Nodes}, raw
}

// countingSource 是注入的取法：按表回描述，并记被调了几次。
type countingSource struct {
	calls atomic.Int32
	descs map[string][]byte
	fail  error
}

func (c *countingSource) fn(_ context.Context, digests []string, _ []*Node) (map[string][]byte, error) {
	c.calls.Add(1)
	if c.fail != nil {
		return nil, c.fail
	}
	out := map[string][]byte{}
	for _, d := range digests {
		if raw, ok := c.descs[d]; ok {
			out[d] = raw
		}
	}
	return out, nil
}

// 同样的内容在任何进程里都算出同一个哈希（map 是新建的、遍历顺序随机）；任何一个字段变了，
// 哈希都要变 —— 漏一个就是「描述变了、读的一方还用旧的」，而且不报任何错。
func TestDescribe_DeterministicAndCoversEveryField(t *testing.T) {
	_, want, _ := Describe(sampleService("sale", "n1"))
	for i := 0; i < 20; i++ {
		if _, got, _ := Describe(sampleService("sale", "n1")); got != want {
			t.Fatal("同样的描述算出了不同的哈希")
		}
	}
	if _, got, _ := Describe(sampleService("sale", "other-node")); got != want {
		t.Fatal("节点不属于描述，换节点不该改哈希 —— 否则每次重启都是一份新描述")
	}

	cases := map[string]func(s *Service){
		"服务名":         func(s *Service) { s.Name = "sale2" },
		"版本":          func(s *Service) { s.Version = "2" },
		"metadata 值":  func(s *Service) { s.Metadata["depends"] = "base,mail" },
		"metadata 键":  func(s *Service) { s.Metadata["extra"] = "" },
		"端点路径":        func(s *Service) { s.Endpoints[0].Path = "/sale/b" },
		"端点方法":        func(s *Service) { s.Endpoints[0].Method = []string{"POST"} },
		"端点名":         func(s *Service) { s.Endpoints[1].Name = "/sale.write" },
		"端点顺序":        func(s *Service) { s.Endpoints[0], s.Endpoints[1] = s.Endpoints[1], s.Endpoints[0] },
		"多一个端点":       func(s *Service) { s.Endpoints = append(s.Endpoints, &Endpoint{Path: "/x"}) },
		"端点 metadata": func(s *Service) { s.Endpoints[0].Metadata = map[string]string{"site_app": "1"} },
	}
	for name, mutate := range cases {
		s := sampleService("sale", "n1")
		mutate(s)
		if _, got, _ := Describe(s); got == want {
			t.Errorf("%s 变了，哈希没变", name)
		}
	}
}

// 本进程答描述时，写进后端的是瘦身记录；不答时（只跑 HTTP 服务器）照旧写完整记录 ——
// 否则别人取不到它的描述。
func TestResolver_RegisterStripsOnlyWhenServing(t *testing.T) {
	resetDescriptors(t)
	back := &storeRegistry{}
	r := wrapResolver(back)

	if err := r.Register(sampleService("sale", "n1")); err != nil {
		t.Fatal(err)
	}
	if got := back.last(); len(got.Endpoints) != 2 || got.Metadata[MetaDescriptor] != "" {
		t.Fatalf("没答描述的进程把记录剥了：%+v", got)
	}

	SetDescriptorServing(true)
	if err := r.Register(sampleService("sale", "n1")); err != nil {
		t.Fatal(err)
	}
	got := back.last()
	_, d, _ := Describe(sampleService("sale", "n1"))
	if len(got.Endpoints) != 0 || len(got.Metadata) != 1 || got.Metadata[MetaDescriptor] != d {
		t.Fatalf("瘦身记录应只剩描述哈希，得到 metadata=%v endpoints=%d", got.Metadata, len(got.Endpoints))
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Id != "n1" {
		t.Fatalf("节点丢了：%+v", got.Nodes)
	}
}

// 没有描述的记录（broker 订阅：只有版本与节点 metadata）不剥 —— 剥了反而多一次取。
func TestResolver_RegisterLeavesRecordsWithoutDescriptor(t *testing.T) {
	resetDescriptors(t)
	SetDescriptorServing(true)
	back := &storeRegistry{}
	broker := &Service{Name: "volts.http.broker", Version: "broadcast",
		Nodes: []*Node{{Id: "b1", Address: "192.0.2.1:1", Metadata: map[string]string{"topic": "x"}}}}
	if err := wrapResolver(back).Register(broker); err != nil {
		t.Fatal(err)
	}
	if got := back.last(); got.Metadata[MetaDescriptor] != "" {
		t.Fatalf("没有描述的记录被剥了：%+v", got.Metadata)
	}
}

// 读自己注册的记录不走网络。
func TestResolver_ResolvesOwnRecordsLocally(t *testing.T) {
	resetDescriptors(t)
	SetDescriptorServing(true)
	src := &countingSource{}
	SetDescriptorSource(src.fn)
	r := wrapResolver(&storeRegistry{})

	if err := r.Register(sampleService("sale", "n1")); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetService("sale")
	if err != nil || len(got) != 1 {
		t.Fatalf("GetService: %v, %v", got, err)
	}
	if len(got[0].Endpoints) != 2 || got[0].Metadata["depends"] != "base" || got[0].Digest == "" {
		t.Fatalf("自己的记录没补全：%+v", got[0])
	}
	if n := src.calls.Load(); n != 0 {
		t.Fatalf("读自己的描述走了 %d 次网络", n)
	}
}

// 别人的描述：取一次、校验、缓存；之后再读不再取。节点用记录里的，不用描述里的。
func TestResolver_FetchesForeignDescriptorOnce(t *testing.T) {
	resetDescriptors(t)
	foreign := sampleService("stock", "remote-1")
	rec, raw := stripped(t, foreign)
	back := &storeRegistry{recs: []*Service{rec}}
	src := &countingSource{descs: map[string][]byte{rec.Metadata[MetaDescriptor]: raw}}
	SetDescriptorSource(src.fn)
	r := wrapResolver(back)

	for i := 0; i < 3; i++ {
		got, err := r.GetService("stock")
		if err != nil || len(got) != 1 {
			t.Fatalf("第 %d 次 GetService: %v, %v", i, got, err)
		}
		if len(got[0].Endpoints) != 2 || got[0].Nodes[0].Id != "remote-1" {
			t.Fatalf("补全不对：%+v", got[0])
		}
	}
	if n := src.calls.Load(); n != 1 {
		t.Fatalf("同一份描述取了 %d 次，应只取一次", n)
	}

	list, err := r.ListServices()
	if err != nil || len(list) != 1 || len(list[0].Endpoints) != 2 {
		t.Fatalf("ListServices 没补全：%v, %v", list, err)
	}
}

// 取回的描述对不上哈希、或者是别的服务的，一律不收：引用它的记录不可见。
func TestResolver_RejectsTamperedDescriptors(t *testing.T) {
	cases := map[string]func(raw []byte) []byte{
		"内容被改": func(raw []byte) []byte {
			out := append([]byte(nil), raw...)
			out[len(out)-3] ^= 1
			return out
		},
		"别的服务的描述": func([]byte) []byte {
			_, other := stripped(t, sampleService("account", "x"))
			return other
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			resetDescriptors(t)
			rec, raw := stripped(t, sampleService("stock", "remote-1"))
			d := rec.Metadata[MetaDescriptor]
			SetDescriptorSource((&countingSource{descs: map[string][]byte{d: tamper(raw)}}).fn)
			r := wrapResolver(&storeRegistry{recs: []*Service{rec}})

			if got, err := r.GetService("stock"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("篡改过的描述被收下了：%v, %v", got, err)
			}
			if cached(d) != nil {
				t.Fatal("篡改过的描述进了缓存")
			}
		})
	}
}

// 取不到时，同一哈希短时间内不重取 —— 否则每个读注册中心的请求都会去打一遍对端。
func TestResolver_FailedFetchIsThrottled(t *testing.T) {
	resetDescriptors(t)
	rec, _ := stripped(t, sampleService("stock", "remote-1"))
	src := &countingSource{fail: errors.New("down")}
	SetDescriptorSource(src.fn)
	r := wrapResolver(&storeRegistry{recs: []*Service{rec}})

	for i := 0; i < 5; i++ {
		if _, err := r.GetService("stock"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("取不到描述时应回 ErrNotFound，得到 %v", err)
		}
	}
	if n := src.calls.Load(); n != 1 {
		t.Fatalf("取失败后 %v 内重取了 %d 次", descRetry, n)
	}
}

// watch：create 补不全就跳过（交出一条没有端点的记录，下游会以为端点全没了）；delete 不需要描述，
// 带着 Digest 交出去。
func TestResolver_WatchSkipsUnresolvedCreatesButPassesDeletes(t *testing.T) {
	resetDescriptors(t)
	lost, _ := stripped(t, sampleService("lost", "remote-1"))
	ok, raw := stripped(t, sampleService("stock", "remote-2"))
	SetDescriptorSource((&countingSource{descs: map[string][]byte{ok.Metadata[MetaDescriptor]: raw}}).fn)

	w := &chanWatcher{ch: make(chan *Result, 3)}
	w.ch <- &Result{Action: "create", Service: lost}
	w.ch <- &Result{Action: "delete", Service: lost}
	w.ch <- &Result{Action: "create", Service: ok}
	close(w.ch)

	rw, _ := wrapResolver(&storeRegistry{w: w}).Watcher()
	first, err := rw.Next()
	if err != nil || first.Action != "delete" || first.Service.Digest != lost.Metadata[MetaDescriptor] {
		t.Fatalf("第一条应是带 Digest 的 delete（补不全的 create 跳过），得到 %+v, %v", first, err)
	}
	second, err := rw.Next()
	if err != nil || second.Action != "create" || len(second.Service.Endpoints) != 2 {
		t.Fatalf("能补全的 create 应带着端点交出，得到 %+v, %v", second, err)
	}
}

// 同名同版本、描述不同的两条记录不能并成一份；没补全的 delete 事件要对得上缓存里补全过的那份。
func TestGroupKey_FollowsDigest(t *testing.T) {
	a, _ := stripped(t, sampleService("sale", "n1"))
	other := sampleService("sale", "n2")
	other.Endpoints = other.Endpoints[:1]
	b, _ := stripped(t, other)
	if GroupKey(a) == GroupKey(b) {
		t.Fatal("描述不同的两条记录分组键相同 —— 会被并成一份，节点与端点对不上")
	}
	assembled := &Service{Name: "sale", Version: "latest", Digest: a.Metadata[MetaDescriptor], Endpoints: sampleService("sale", "x").Endpoints}
	if GroupKey(a) != GroupKey(assembled) {
		t.Fatal("瘦身记录与补全后的同一份描述分组键不同 —— delete 事件找不到缓存里的那条")
	}
}

// 缓存层收到没补全的 delete（只有 Digest），要从补全过的那条里摘掉节点。
func TestCacherUpdate_DeleteMatchesByDigest(t *testing.T) {
	c, ok := NewCacher(&storeRegistry{}).(*registryCacher)
	if !ok {
		t.Fatal("NewCacher 没有返回 *registryCacher")
	}
	rec, _ := stripped(t, sampleService("sale", "n1"))
	d := rec.Metadata[MetaDescriptor]
	full := sampleService("sale", "n1")
	full.Digest = d
	full.Nodes = append(full.Nodes, &Node{Id: "n2", Address: "192.0.2.2:1"})

	c.Lock()
	c.watched["sale"] = true
	c.cache["sale"] = []*Service{full}
	c.Unlock()

	del := &Service{Name: "sale", Version: "latest", Digest: d, Metadata: rec.Metadata, Nodes: []*Node{{Id: "n1"}}}
	c.update(&Result{Action: "delete", Service: del})

	c.RLock()
	got := c.cache["sale"]
	c.RUnlock()
	if len(got) != 1 || len(got[0].Nodes) != 1 || got[0].Nodes[0].Id != "n2" {
		t.Fatalf("没补全的 delete 没摘掉节点：%+v", got)
	}
}

// 节点换了描述（同进程里端点变了）或注销后，本进程不再交出它不再用的那份。
func TestLocalDescriptors_ReleasedWhenNoLongerReferenced(t *testing.T) {
	resetDescriptors(t)
	SetDescriptorServing(true)
	r := wrapResolver(&storeRegistry{})

	v1 := sampleService("sale", "n1")
	_, d1, _ := Describe(v1)
	_ = r.Register(v1)
	v2 := sampleService("sale", "n1")
	v2.Endpoints = v2.Endpoints[:1]
	_, d2, _ := Describe(v2)
	_ = r.Register(v2)

	if got := LocalDescriptors([]string{d1, d2}); len(got) != 1 || got[d2] == nil {
		t.Fatalf("换描述后应只交出新的那份，得到 %d 份", len(got))
	}
	_ = r.Deregister(&Service{Name: "sale", Nodes: []*Node{{Id: "n1"}}})
	if got := LocalDescriptors([]string{d2}); len(got) != 0 {
		t.Fatal("节点注销后描述还在交出")
	}
}
