// Package etcd provides an etcd service registry
package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	vtls "github.com/volts-dev/volts/internal/tls"
	"github.com/volts-dev/volts/registry"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

var log = registry.Logger()

var (
	prefix = "/volts/registry/"
)

const (
	// defaultTimeout 是 etcd 各操作（Get/Put/Lease/Watch）的默认超时。
	defaultTimeout = 5 * time.Second

	// minTimeout 是允许的最小超时。低于它的配置一律视为配置错误并抬到
	// defaultTimeout —— 对 etcd 来说亚秒级超时不存在合理用途：本机单节点
	// 拉一次全量服务列表实测就要 77~130ms，跨网络更高，而超时窗口还要覆盖
	// 传输 + 反序列化。踩线的后果不是慢，是 GetService/ListServices 直接
	// 报 context deadline exceeded；registryCacher 在没有旧缓存可回退时会把
	// 这个原始错误原样透出，上层网关按「无状态码错误」压成 HTTP 500。
	minTimeout = time.Second
)

type etcdRegistry struct {
	client *clientv3.Client
	config *registry.Config
	sync.RWMutex
	register map[string]uint64
	leases   map[string]clientv3.LeaseID

	// records 缓存每个键**解码并验过签**的注册项，按 ModRevision 认版本，见 record。
	recMu   sync.Mutex
	records map[string]*cachedRecord

	// ranges 缓存每个前缀上一次整段读回来的键值，见 rangePrefix。
	rangeMu sync.Mutex
	ranges  map[string]*cachedRange
}

// cachedRange 是一个前缀在某个库版本上的整段读结果。
type cachedRange struct {
	rev int64 // 读的那一刻的库版本（Header.Revision）
	kvs []*mvccpb.KeyValue
}

// cachedRecord 是一条注册项在某个 ModRevision 上的解码结果与验签结论。
type cachedRecord struct {
	rev     int64
	svc     *registry.Service // nil = 解不出来
	ok      bool              // registry.AcceptRecord 的结论
	stamp   registry.VerdictStamp
	checked time.Time // ok 是什么时候判的
}

// recordRecheck 是应用没给信任表代号（RecordSecurity.Generation）时验签结论的有效期。
// 字节没变（ModRevision 相同）结论也可能变：信任表 30 秒热更新一次，补上 provides
// 之后此前被丢的注册项要能自己回来，不能等它被重写（稳态下注册项不再重写）。给了
// 代号就不靠时间：结论一直复用到代号变。
var recordRecheck = 30 * time.Second

func init() {
	registry.Register("etcd", New)
}

func New(opts ...registry.Option) registry.IRegistry {
	var defaultOpts []registry.Option
	defaultOpts = append(defaultOpts,
		registry.WithName("etcd"),
		// 不设默认 Timeout：交给 configure() 按 minTimeout 下限兜底。此前这里硬塞
		// 100ms，对 etcd（哪怕本机单节点）都太紧——真栈实测大量注册漏注、
		// 客户端解析服务 "not found" / "context deadline exceeded"。调用方仍可用
		// registry.Timeout(...) 覆盖，只要不低于 minTimeout。
	)

	reg := &etcdRegistry{
		config:   registry.NewConfig(append(defaultOpts, opts...)...),
		register: make(map[string]uint64),
		leases:   make(map[string]clientv3.LeaseID),
		records:  make(map[string]*cachedRecord),
	}

	configure(reg)
	return reg
}

func configure(e *etcdRegistry) error {
	config := clientv3.Config{
		Endpoints: []string{"127.0.0.1:2379"}, // etcd 默认地址
	}

	e.config.Name = e.String()

	// 兜底不能只认 Timeout==0：那个哨兵假设「没配过就是 0」，可 registry.NewConfig
	// 的历史默认是 100ms，且该值早已被 config.Register 持久化进各家配置文件
	// （server.*.registry.timeout / router.*.registry.timeout / client.*… 每个使用
	// registry 的组件都有一份自己前缀下的副本）。于是 Timeout 恒非 0，兜底永不
	// 触发，而 100ms 对 etcd 是必然踩线的值。这里按下限判定，存量配置也能自愈。
	if e.config.Timeout < minTimeout {
		if e.config.Timeout > 0 {
			log.Warnf("etcd: timeout %v 过短（下限 %v），已抬到 %v —— 亚秒级超时会让 GetService/ListServices 报 context deadline exceeded，上层通常表现为 HTTP 500；请检查配置文件里的 registry.timeout",
				e.config.Timeout, minTimeout, defaultTimeout)
		}
		e.config.Timeout = defaultTimeout
	}

	if e.config.Secure || e.config.TlsConfig != nil {
		// Secure(true) 但未给 TlsConfig 时，使用「开启证书校验 + TLS>=1.2」的安全
		// 默认。此前这里默认 InsecureSkipVerify:true —— 中间人可篡改服务发现应答，
		// 把整个集群的 RPC 流量导向攻击者节点。要跳过校验必须显式传入
		// TlsConfig(&tls.Config{InsecureSkipVerify: true})。
		config.TLS = vtls.Secure(e.config.TlsConfig)
	}

	if e.config.Context != nil {
		u, ok := e.config.Context.Value(authKey{}).(*authCreds)
		if ok {
			config.Username = u.Username
			config.Password = u.Password
		}
		cfg, ok := e.config.Context.Value(logConfigKey{}).(*zap.Config)
		if ok && cfg != nil {
			config.LogConfig = cfg
		}
	}

	var cAddrs []string
	for _, address := range e.config.Addrs {
		if len(address) == 0 {
			continue
		}
		addr, port, err := net.SplitHostPort(address)
		if ae, ok := err.(*net.AddrError); ok && ae.Err == "missing port in address" {
			port = "2379"
			addr = address
			cAddrs = append(cAddrs, net.JoinHostPort(addr, port))
		} else if err == nil {
			cAddrs = append(cAddrs, net.JoinHostPort(addr, port))
		}
	}

	// if we got addrs then we'll update
	if len(cAddrs) > 0 {
		config.Endpoints = cAddrs
	}

	cli, err := clientv3.New(config)
	if err != nil {
		return err
	}
	e.client = cli
	return nil
}

func encode(s *registry.Service) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decode(ds []byte) *registry.Service {
	var s *registry.Service
	err := json.Unmarshal(ds, &s)
	if err != nil {
		log.Errf("decode service failed with error: %s - %s", err.Error(), string(ds))
	}
	return s
}

func nodePath(s, id string) string {
	service := strings.Replace(s, "/", "-", -1)
	node := strings.Replace(id, "/", "-", -1)
	return path.Join(prefix, service, node)
}

func servicePath(s string) string {
	return path.Join(prefix, strings.Replace(s, "/", "-", -1))
}

func (e *etcdRegistry) Init(opts ...registry.Option) error {
	e.config.Init(opts...)
	return configure(e)
}

func (e *etcdRegistry) Config() *registry.Config {
	return e.config
}

func (e *etcdRegistry) registerNode(s *registry.Service, node *registry.Node, opts ...registry.Option) error {
	// check existing lease cache
	e.RLock()
	leaseID, ok := e.leases[s.Name+node.Id]
	e.RUnlock()

	if !ok {
		// missing lease, check if the key exists
		ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
		defer cancel()

		// look for the existing key
		rsp, err := e.client.Get(ctx, nodePath(s.Name, node.Id), clientv3.WithSerializable())
		if err != nil {
			return err
		}

		// get the existing lease
		for _, kv := range rsp.Kvs {
			if kv.Lease > 0 {
				leaseID = clientv3.LeaseID(kv.Lease)

				// 与下面同一个判据：库里那条的字节
				e.Lock()
				e.leases[s.Name+node.Id] = leaseID
				e.register[s.Name+node.Id] = recordHash(kv.Value)
				e.Unlock()

				break
			}
		}
	}

	// 签名（registry.SignedNode）要在算哈希之前：签名随 metadata / 端点变，哈希跟着变，
	// 声明变了才会真的重写这一条（此前哈希只看节点，服务 metadata 变了也被当成"没变"跳过）。
	node, err := registry.SignedNode(s, node)
	if err != nil {
		return err
	}

	var leaseNotFound bool

	// renew the lease if it exists
	//
	// 稳态下每个服务每轮都走这里，**不打日志**：此前这一行是 Trace，而 logger 默认等级
	// 放行 Trace，一个 aio 进程几十个服务、每 30 秒一轮，云上 syslog 被它和 SQL 日志
	// 一起灌到 8GB。只在状态变了（租约丢了、内容变了）时才值得记一笔。
	if leaseID > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
		_, err := e.client.KeepAliveOnce(ctx, leaseID)
		cancel()
		if err != nil {
			if err != rpctypes.ErrLeaseNotFound {
				return err
			}

			log.Warnf("Lease %x for %s node %s has expired, registering again", int64(leaseID), s.Name, node.Id)
			// lease not found do register
			leaseNotFound = true
		}
	}

	service := &registry.Service{
		Name:      s.Name,
		Version:   s.Version,
		Metadata:  s.Metadata,
		Endpoints: s.Endpoints,
		Nodes:     []*registry.Node{node},
	}
	value := encode(service)

	// 「没变就跳过」比的是**将要写进去的整条字节**，不只是节点：此前只哈希节点，端点 /
	// 服务 metadata 变了全靠签名间接带进来，关了签名就会漏写。字节相同 ⇔ 内容相同，
	// 前提是编码确定 —— json 对 map 排序，端点顺序由 server.sortEndpoints 保证。
	h := recordHash([]byte(value))

	// get existing hash for the service node
	e.Lock()
	v, ok := e.register[s.Name+node.Id]
	e.Unlock()

	// the service is unchanged, skip registering
	if ok && v == h && !leaseNotFound {
		return nil
	}

	var options registry.Config
	for _, o := range opts {
		o(&options)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
	defer cancel()

	// 租约还活着就沿用，内容变了只重写这一条。此前一律 Grant 新租约：旧的刚续过、
	// 还要空挂一个 TTL，每次重写都多一个租约（真栈 44 个键挂着 279 个租约）。
	useLease := leaseID
	if leaseNotFound || options.TTL.Seconds() <= 0 {
		useLease = 0
	}
	if useLease == 0 && options.TTL.Seconds() > 0 {
		// get a lease used to expire keys since we have a ttl
		lgr, err := e.client.Grant(ctx, int64(options.TTL.Seconds()))
		if err != nil {
			return err
		}
		useLease = lgr.ID
	}

	log.Dbgf("Registering %s id %s with lease %x and ttl %v", service.Name, node.Id, int64(useLease), options.TTL)
	// create an entry for the node
	if useLease > 0 {
		_, err = e.client.Put(ctx, nodePath(service.Name, node.Id), value, clientv3.WithLease(useLease))
	} else {
		_, err = e.client.Put(ctx, nodePath(service.Name, node.Id), value)
	}
	if err != nil {
		return err
	}

	e.Lock()
	// save our hash of the service
	e.register[s.Name+node.Id] = h
	// save our leaseID of the service
	if useLease > 0 {
		e.leases[s.Name+node.Id] = useLease
	} else {
		delete(e.leases, s.Name+node.Id)
	}
	e.Unlock()

	return nil
}

// recordHash 是一条注册项字节的指纹，用来判「这一轮要不要重写」。
func recordHash(b []byte) uint64 {
	f := fnv.New64a()
	f.Write(b)
	return f.Sum64()
}

func (e *etcdRegistry) Deregister(s *registry.Service, opts ...registry.Option) error {
	if len(s.Nodes) == 0 {
		return errors.New("Require at least one node")
	}

	for _, node := range s.Nodes {
		e.Lock()
		// delete our hash of the service
		delete(e.register, s.Name+node.Id)
		// delete our lease of the service
		delete(e.leases, s.Name+node.Id)
		e.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
		defer cancel()

		//if logger.V(logger.TraceLevel, logger.DefaultLogger) {
		log.Dbgf("Deregistering %s id %s", s.Name, node.Id)
		//}
		_, err := e.client.Delete(ctx, nodePath(s.Name, node.Id))
		if err != nil {
			return err
		}
	}

	return nil
}

func (e *etcdRegistry) Register(s *registry.Service, opts ...registry.Option) error {
	if len(s.Nodes) == 0 {
		return errors.New("Require at least one node")
	}
	var gerr error

	// register each node individually
	for _, node := range s.Nodes {
		if err := e.registerNode(s, node, opts...); err != nil {
			gerr = err
		}
	}

	return gerr
}

func (e *etcdRegistry) GetService(name string) ([]*registry.Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
	defer cancel()

	kvs, err := e.rangePrefix(ctx, servicePath(name)+"/")
	if err != nil {
		return nil, err
	}

	if len(kvs) == 0 {
		return nil, registry.ErrNotFound
	}

	serviceMap := map[string]*registry.Service{}

	for _, n := range kvs {
		if sn := e.record(n); sn != nil {
			// ★ 分组键是 版本 + **端点集指纹**，不只是版本。
			//
			// 一个服务名下可以有好几个注册项（别的进程 blank-import 了这个模块，
			// 于是它也注册了这个服务名）。只按版本合并的话，nodes 全并进来而
			// endpoints 只留第一个注册项的 —— 节点与端点的对应关系就此丢失，
			// 客户端会把请求轮询到不实现该端点的进程上，得到一句
			// NotFound，而它与"没装那个模块"长得一模一样。详见
			// registry.EndpointsKey 的注释（含真栈实测）。
			//
			// 端点集相同的注册项照旧合并成一个 Service，所以常态下形状不变。
			key := sn.Version + "\x00" + registry.EndpointsKey(sn.Endpoints)
			s, ok := serviceMap[key]
			if !ok {
				s = &registry.Service{
					Name:      sn.Name,
					Version:   sn.Version,
					Metadata:  sn.Metadata,
					Endpoints: sn.Endpoints,
				}
				serviceMap[key] = s
			}

			s.Nodes = append(s.Nodes, sn.Nodes...)
		}
	}

	services := make([]*registry.Service, 0, len(serviceMap))
	for _, service := range serviceMap {
		services = append(services, service)
	}

	return services, nil
}

func (e *etcdRegistry) ListServices() ([]*registry.Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
	defer cancel()

	kvs, err := e.rangePrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}

	if len(kvs) == 0 {
		return []*registry.Service{}, nil
	}

	entries := make([]revisioned, 0, len(kvs))
	seen := make(map[string]struct{}, len(kvs))
	for _, n := range kvs {
		seen[string(n.Key)] = struct{}{}
		if sn := e.record(n); sn != nil {
			entries = append(entries, revisioned{svc: sn, rev: n.CreateRevision})
		}
	}
	e.pruneRecords(seen)
	return mergeByNameVersion(entries), nil
}

// rangePrefix 读前缀 p 下的全部键值；自上次整段读之后什么都没变时，只花两次只计数的
// 查询，返回上次的结果。
//
// 「没变」的判据（同一个库版本上的两次读）：键数与上次相同，且没有任何键的 ModRevision 晚于
// 上次读的库版本（只取键的过滤查询回空）。新建 / 修改必然产生更晚的 ModRevision；删除（含租约到期）不留键，
// 但会让键数变少 —— 删一建一时，建的那条照样被第二个条件抓到。
//
// 此前每次 ListServices / GetService 都整段拉值：全量 7MB，路由刷新、健康监控、
// Apps 目录、admin 探测各自在拉，空闲时 etcd 每分钟发出 150MB（2026-10-10 实测）。
func (e *etcdRegistry) rangePrefix(ctx context.Context, p string) ([]*mvccpb.KeyValue, error) {
	e.rangeMu.Lock()
	c := e.ranges[p]
	e.rangeMu.Unlock()

	if c != nil {
		// ★ 两次 Get 钉在同一个库版本上，**不能**合成一个只读 Txn。
		//
		// etcd 3.5.4（本机 brew 装的就是它）的只读 Txn 走请求路径、用的是客户端的 ctx：
		// 应用到一半客户端超时或取消，applyTxn 拿到 context canceled / deadline exceeded，
		// 直接 panic「unexpected error during txn」（apply.go:638），整个 etcd 进程退出。
		// 负载一高客户端就超时，etcd 一崩全栈注册失败，客户端重试又压上来 —— 2026-10-14
		// 一天崩了十四次（11:59 开了日志之后 40 分钟里就 11 次），全是这个栈。普通的 Range 被取消只回错误，不会 panic。
		//
		// 第二次 Get 带 WithRev(第一次的库版本)，读的是同一个快照：与原来同一事务里两次读等价。
		cnt, err := e.client.Get(ctx, p, clientv3.WithPrefix(), clientv3.WithCountOnly(), clientv3.WithSerializable())
		if err != nil {
			return nil, err
		}
		// 只取键、不计数：带 MinModRev 过滤时 etcd 回的 Count 是**过滤前**的键数，
		// 判不出「有没有更新过的」。没变时这一项回空，照样只有几十字节。
		upd, err := e.client.Get(ctx, p, clientv3.WithPrefix(), clientv3.WithKeysOnly(), clientv3.WithSerializable(),
			clientv3.WithMinModRev(c.rev+1), clientv3.WithRev(cnt.Header.Revision))
		if err != nil {
			return nil, err
		}
		if cnt.Count == int64(len(c.kvs)) && len(upd.Kvs) == 0 {
			return c.kvs, nil
		}
	}

	rsp, err := e.client.Get(ctx, p, clientv3.WithPrefix(), clientv3.WithSerializable())
	if err != nil {
		return nil, err
	}

	e.rangeMu.Lock()
	if e.ranges == nil {
		e.ranges = make(map[string]*cachedRange)
	}
	e.ranges[p] = &cachedRange{rev: rsp.Header.Revision, kvs: rsp.Kvs}
	e.rangeMu.Unlock()
	return rsp.Kvs, nil
}

// record 返回 kv 解码并验过签的注册项；解不出或验不过返回 nil。
//
// 同一个键的 ModRevision 没变，字节就没变：解码直接复用，验签结论在 recordRecheck
// 内复用。此前每次 GetService / ListServices 都把每条注册项整条 JSON 解码、再整条
// 重新编码算摘要验签（stock 一条 400KB、全量 7MB），空闲时 kylin 每分钟 22 秒 CPU
// 里一大半花在这上面（2026-10-10 采样）。
//
// 返回的对象在调用方之间共享（上面的 registryCacher 本来就这么做），切片的容量
// 收紧到长度，调用方 append 时一定另起底层数组，碰不到这里的缓存。
func (e *etcdRegistry) record(kv *mvccpb.KeyValue) *registry.Service {
	key := string(kv.Key)

	stamp, exact := registry.CurrentVerdictStamp()

	e.recMu.Lock()
	c := e.records[key]
	e.recMu.Unlock()

	if c == nil || c.rev != kv.ModRevision {
		svc := decode(kv.Value)
		if svc != nil {
			svc.Endpoints = svc.Endpoints[:len(svc.Endpoints):len(svc.Endpoints)]
			svc.Nodes = svc.Nodes[:len(svc.Nodes):len(svc.Nodes)]
		}
		c = &cachedRecord{rev: kv.ModRevision, svc: svc}
	} else if c.stamp == stamp && (exact || time.Since(c.checked) < recordRecheck) {
		if c.ok {
			return c.svc
		}
		return nil
	} else {
		cp := *c // 别在别人可能正读着的那份上改结论
		c = &cp
	}

	c.ok = c.svc != nil && registry.AcceptRecord(c.svc)
	c.stamp = stamp
	c.checked = time.Now()

	e.recMu.Lock()
	if e.records == nil {
		e.records = make(map[string]*cachedRecord)
	}
	e.records[key] = c
	e.recMu.Unlock()

	if c.ok {
		return c.svc
	}
	return nil
}

// pruneRecords 丢掉全量清单里已经不在的键（进程重启后旧节点的键）。
func (e *etcdRegistry) pruneRecords(live map[string]struct{}) {
	e.recMu.Lock()
	defer e.recMu.Unlock()
	for k := range e.records {
		if _, ok := live[k]; !ok {
			delete(e.records, k)
		}
	}
}

// revisioned 是一条注册项连同它在 etcd 里的创建版本号（越大越晚注册）。
type revisioned struct {
	svc *registry.Service
	rev int64
}

// mergeByNameVersion 把同名同版本的注册项合并成一个 Service：节点全部并进来，
// **metadata 与端点取最后注册的那一条**（CreateRevision 最大）。
//
// 原来取的是 etcd 返回顺序里的第一条，而键里带随机 uuid，于是取到谁全凭运气。平时同一
// 服务的几个节点 metadata 相同，无所谓；**滚动重启的那几分钟**旧进程的注册项还没过租约，
// 新旧两份同名同版本、metadata 不同 —— 取到旧的那份，新版本声明的东西（扩展字段、设置
// 字段、方法贡献）就在这段时间里看不见。2026-09-29 真栈：salesvc 重启后 sale 的设置字段
// 有几分钟不出现，等旧租约过期才出来。最后注册的那一条就是正在运行的最新版本。
func mergeByNameVersion(entries []revisioned) []*registry.Service {
	type merged struct {
		svc *registry.Service
		rev int64
	}
	byKey := make(map[string]*merged)
	for _, e := range entries {
		key := e.svc.Name + "\x00" + e.svc.Version
		m, ok := byKey[key]
		if !ok {
			cp := *e.svc
			cp.Nodes = append([]*registry.Node(nil), e.svc.Nodes...)
			byKey[key] = &merged{svc: &cp, rev: e.rev}
			continue
		}
		m.svc.Nodes = append(m.svc.Nodes, e.svc.Nodes...)
		if e.rev > m.rev {
			m.svc.Metadata = e.svc.Metadata
			m.svc.Endpoints = e.svc.Endpoints
			m.rev = e.rev
		}
	}

	services := make([]*registry.Service, 0, len(byKey))
	for _, m := range byKey {
		services = append(services, m.svc)
	}
	sort.Slice(services, func(i, j int) bool {
		if services[i].Name != services[j].Name {
			return services[i].Name < services[j].Name
		}
		return services[i].Version < services[j].Version
	})
	return services
}

func (e *etcdRegistry) Watcher(opts ...registry.WatchOptions) (registry.Watcher, error) {
	return newEtcdWatcher(e, e.config.Timeout, opts...)
}

func (e *etcdRegistry) String() string {
	return e.config.Name
}
