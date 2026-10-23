// Package registry provides a registry implementation with caching capabilities
package registry

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	igoroutine "github.com/volts-dev/volts/internal/goroutine"
	"github.com/volts-dev/volts/logger"
	"golang.org/x/sync/singleflight"
)

// IRegistryCacher is an interface that extends IRegistry with caching capabilities.
// It provides methods for efficient service lookup and management of local services.
type (
	IRegistryCacher interface {
		IRegistry // embed the registry interface

		// Match finds services that match the given endpoint path.
		Match(endpoint string) ([]*Service, error)

		// LocalServices returns a list of services registered by the local instance.
		LocalServices() []*Service

		// Stop terminates the background registry watcher and cleans up resources.
		Stop()
	}

	// registryCacher is the default implementation of IRegistryCacher.
	registryCacher struct {
		IRegistry
		config *Config

		// registry cache state
		sync.RWMutex
		cache         map[string][]*Service
		endpointMap   map[string]string // cached endpoint to service name mapping
		ttls          map[string]time.Time
		watched       map[string]bool
		localServices []*Service
		// readBack 记着每个本地服务（name+version）上一次**成功回读**时的身份指纹
		// （localIdentityKey）。续租时身份没变就不再回读，见 Register。
		readBack map[string]string

		// 全量服务清单的缓存。ListServices 此前**完全没有缓存**（结构体只是嵌入
		// IRegistry，方法直接穿透到后端），而它是按模型找属主服务这类调用的入口，
		// 每个请求都要走一趟：真栈实测网关每处理一次 call_kw 就打约 3 次 etcd
		// Range，每次全量扫 800KB+ 的注册数据。
		listCache []*Service
		listTTL   time.Time
		// listGen 每次失效加一。回源前记下、回来时比对：变了说明拉取途中有注册 / 注销事件，
		// 拿回来的清单可能早于它，不能缓存（见 ListServices）。
		listGen uint64

		// exit is used to stop the background watcher
		exit chan bool
		// wg waits for run() + every watch() goroutine to fully exit on Stop()
		wg sync.WaitGroup

		// running indicates whether the background watcher is active
		running bool
		// status holds the last error encountered by the registry
		// and is used to preserve the cache in failure state
		status error
		// sg is used to prevent cache breakdown (thundering herd effect)
		sg singleflight.Group
	}
)

var (
	DefaultTTL = time.Minute
)

// NewCacher returns a new registry cacher instance with the given registry and options.
func NewCacher(r IRegistry, opts ...Option) IRegistryCacher {
	var defaultOpts []Option
	defaultOpts = append(defaultOpts,
		//WithName(""),
		RegisterTTL(DefaultTTL))

	return &registryCacher{
		IRegistry:   r,
		config:      newCacheConfig(append(defaultOpts, opts...)...),
		watched:     make(map[string]bool),
		cache:       make(map[string][]*Service),
		endpointMap: make(map[string]string),
		ttls:        make(map[string]time.Time),
		readBack:    make(map[string]string),
		exit:        make(chan bool),
	}
}

func backoff(attempts int) time.Duration {
	if attempts == 0 {
		return time.Duration(0)
	}
	return time.Duration(math.Pow(10, float64(attempts))) * time.Millisecond
}

func (c *registryCacher) getStatus() error {
	c.RLock()
	defer c.RUnlock()
	return c.status
}

func (c *registryCacher) setStatus(err error) {
	c.Lock()
	c.status = err
	c.Unlock()
}

// isValid checks if the service is valid
func (c *registryCacher) isValid(services []*Service, ttl time.Time) bool {
	// no services exist
	if len(services) == 0 {
		return false
	}

	// ttl is invalid
	if ttl.IsZero() {
		return false
	}

	// time since ttl is longer than timeout
	if time.Since(ttl) > 0 {
		return false
	}

	// ok
	return true
}

func (c *registryCacher) quit() bool {
	select {
	case <-c.exit:
		return true
	default:
		return false
	}
}

func (c *registryCacher) del(service string) {
	// don't blow away cache in error state
	if err := c.status; err != nil {
		return
	}
	// otherwise delete entries
	delete(c.cache, service)
	delete(c.ttls, service)
}

func (c *registryCacher) Contain(service string) bool {
	_, has := c.cache[service]
	return has
}

func (c *registryCacher) get(service string) ([]*Service, error) {
	// read lock
	c.RLock()

	// check the cache first
	services := c.cache[service]
	// get cache ttl
	ttl := c.ttls[service]

	// got services && within ttl so return cache
	//
	// ★ 先判有效再复制。此前进门就 Copy，过期时这份副本只在回源失败时才用得上 ——
	// 而 TTL 与 router 的刷新周期同为 60s，几乎每轮都过期，于是每次读都白拷一整份
	// （connect_components 实测：这一处一天约 10GB）。
	// ★★ 复制必须在锁里：update() 会**原地**改缓存（services[i] = …、service.Nodes = …）。
	if c.isValid(services, ttl) {
		cp := Copy(services)
		c.RUnlock()
		return cp, nil
	}

	// get does the actual request for a service and cache it
	get := func(service string) ([]*Service, error) {
		// ask the registry
		val, err, _ := c.sg.Do(service, func() (interface{}, error) {
			return c.IRegistry.GetService(service)
		})
		services, _ := val.([]*Service)
		if err != nil {
			// check the cache（在锁里重取重拷，理由同上）
			c.RLock()
			cached := Copy(c.cache[service])
			c.RUnlock()
			if len(cached) > 0 {
				// set the error status
				c.setStatus(err)

				// return the stale cache
				return cached, nil
			}
			// otherwise return error
			return nil, err
		}

		// reset the status
		if err := c.getStatus(); err != nil {
			c.setStatus(nil)
		}

		// 缓存直接收下后端这份，交出去的永远是副本：一次回源一次复制（此前两次 ——
		// 进门一次、入缓存一次），而且每个调用方各拿一份。此前这里把回源结果原样交出，
		// singleflight 合并的几个并发调用方拿到的是**同一个**切片。缓存里的对象从不
		// 交给任何人，所以它是不是后端新建的都无所谓；但 update() 会原地改它，
		// 交出去的副本要在同一把锁里拷。
		c.Lock()
		c.set(service, services)
		cp := Copy(services)
		c.Unlock()

		return cp, nil
	}

	// watch service if not watched
	_, ok := c.watched[service]

	// unlock the read lock
	c.RUnlock()

	// check if its being watched
	if !ok {
		c.Lock()

		// set to watched
		c.watched[service] = true

		// only kick it off if not running
		if !c.running {
			c.wg.Add(1)
			go c.run()
		}

		c.Unlock()
	}

	// get and return services
	return get(service)
}

func (c *registryCacher) set(service string, services []*Service) {
	c.cache[service] = services
	c.ttls[service] = time.Now().Add(c.config.TTL)
}

func (c *registryCacher) update(res *Result) {
	if res == nil || res.Service == nil {
		return
	}

	c.Lock()
	defer c.Unlock()

	// 任何注册/注销事件都让全量清单缓存立即失效——**必须在下面按 watched
	// 过滤之前**：新上线的服务本来就还没被 watch，若放在过滤之后，新服务要等
	// 满一个 TTL 才在清单里出现。
	c.listTTL = time.Time{}
	c.listGen++

	// only save watched services
	if _, ok := c.watched[res.Service.Name]; !ok {
		return
	}

	services, ok := c.cache[res.Service.Name]
	if !ok {
		// we're not going to cache anything
		// unless there was already a lookup
		return
	}

	if len(res.Service.Nodes) == 0 {
		switch res.Action {
		case "delete":
			c.del(res.Service.Name)
		}
		return
	}

	// existing service found
	//
	// ★ 认的是 版本 + **端点集指纹**，不只是版本。一个服务名下可以有好几个进程
	// （别的进程 blank-import 了这个模块），只按版本认的话，两个端点集不同的进程
	// 会被并成一条，并且 endpoints 被**最后一次事件**覆盖 —— 节点与端点的对应
	// 关系丢失，请求轮询到不实现该端点的进程上就是一句 NotFound。
	// 详见 EndpointsKey 的注释。
	//
	// 有描述哈希时按哈希认（GroupKey）：delete 事件里的记录可能没补全描述、端点表为空，
	// 只看 EndpointsKey 就对不上缓存里补全过的那份。
	key := GroupKey(res.Service)
	var service *Service
	var index int
	for i, s := range services {
		if s.Version == res.Service.Version && GroupKey(s) == key {
			service = s
			index = i
		}
	}

	switch res.Action {
	case "create", "update":
		if service == nil {
			c.set(res.Service.Name, append(services, res.Service))
			return
		}

		// append old nodes to new service
		for _, cur := range service.Nodes {
			var seen bool
			for _, node := range res.Service.Nodes {
				if cur.Id == node.Id {
					seen = true
					break
				}
			}
			if !seen {
				res.Service.Nodes = append(res.Service.Nodes, cur)
			}
		}

		services[index] = res.Service
		c.set(res.Service.Name, services)
	case "delete":
		if service == nil {
			return
		}

		var nodes []*Node

		// filter cur nodes to remove the dead one
		for _, cur := range service.Nodes {
			var seen bool
			for _, del := range res.Service.Nodes {
				if del.Id == cur.Id {
					seen = true
					break
				}
			}
			if !seen {
				nodes = append(nodes, cur)
			}
		}

		// still got nodes, save and return
		if len(nodes) > 0 {
			service.Nodes = nodes
			services[index] = service
			c.set(service.Name, services)
			return
		}

		// zero nodes left

		// only have one thing to delete
		// nuke the thing
		if len(services) == 1 {
			c.del(service.Name)
			return
		}

		// still have more than 1 service
		// check the version and keep what we know
		var srvs []*Service
		for _, s := range services {
			if s.Version != service.Version {
				srvs = append(srvs, s)
			}
		}

		// save
		c.set(service.Name, srvs)
	case "override":
		if service == nil {
			return
		}

		c.del(service.Name)
	}
}

// run starts the cache watcher loop
// it creates a new watcher if there's a problem
func (c *registryCacher) run() {
	defer c.wg.Done()

	c.Lock()
	c.running = true
	c.Unlock()

	// reset watcher on exit
	defer func() {
		c.Lock()
		c.watched = make(map[string]bool)
		c.running = false
		c.Unlock()
	}()

	var a, b int

	for {
		// exit early if already dead
		if c.quit() {
			return
		}

		// jitter before starting
		j := rand.Int63n(100)
		time.Sleep(time.Duration(j) * time.Millisecond)

		// create new watcher
		w, err := c.IRegistry.Watcher()
		if err != nil {
			if c.quit() {
				return
			}

			d := backoff(a)
			c.setStatus(err)

			if a > 3 {
				if log.GetLevel() == logger.LevelDebug {
					log.Dbg("rcache: ", err, " backing off ", d)
				}
				a = 0
			}

			time.Sleep(d)
			a++

			continue
		}

		if w == nil {
			break
		}

		// reset a
		a = 0

		// watch for events
		if err := c.watch(w); err != nil {
			if c.quit() {
				return
			}

			d := backoff(b)
			c.setStatus(err)

			if b > 3 {
				if log.GetLevel() == logger.LevelDebug {
					log.Dbg("rcache: ", err, " backing off ", d)
				}
				b = 0
			}

			time.Sleep(d)
			b++

			continue
		}

		// reset b
		b = 0
	}
}

// watch loops the next event and calls update
// it returns if there's an error
func (c *registryCacher) watch(w Watcher) error {
	// not need watch
	if w == nil {
		return nil
	}

	// used to stop the watch
	stop := make(chan bool)

	// manage this loop —— wg.Add 必须在 go 启动前完成，确保 Stop().Wait() 能正确同步
	c.wg.Add(1)
	igoroutine.Go(func() {
		defer c.wg.Done()
		defer w.Stop()

		select {
		// wait for exit
		case <-c.exit:
			return
		// we've been stopped
		case <-stop:
			return
		}
	}, func(err error) { log.Err(err.Error()) })

	for {
		res, err := w.Next()
		if err != nil {
			close(stop)
			return err
		}

		// reset the error status since we succeeded
		if err := c.getStatus(); err != nil {
			// reset status
			c.setStatus(nil)
		}

		c.update(res)
	}
}

// match 按 endpoint 路径反查服务名。
//
// 这里曾有三个叠在一起的并发缺陷，都已修掉：
//  1. 出错时 `return nil, err` **没有 RUnlock**，读锁永久泄漏。之后任何写者
//     （watch 事件的 update）会永远阻塞，而 Go 的 RWMutex 一旦有写者在等，
//     后续 RLock 也全部排在它后面——**一次瞬时的注册中心抖动会把整个缓存器
//     永久卡死**，症状是此后所有服务发现都超时。
//  2. `c.endpointMap[...] = ...` 是**在读锁下写 map**：并发调用会触发
//     "concurrent map writes" 直接崩进程，也与 update 的写入互相竞争。
//  3. 持锁做网络调用（ListServices），把后端的慢直接传导成锁竞争。
func (c *registryCacher) match(endpoint string) ([]*Service, error) {
	c.RLock()
	service, has := c.endpointMap[endpoint]
	c.RUnlock()

	if has {
		return c.get(service)
	}

	// 不持锁做网络调用；并发的同名查询由 singleflight 合并成一次。
	val, err, _ := c.sg.Do(endpoint, func() (interface{}, error) {
		return c.ListServices()
	})
	if err != nil {
		return nil, err
	}

	services, _ := val.([]*Service)
	// reset the status
	if err := c.getStatus(); err != nil {
		c.setStatus(nil)
	}

	c.Lock()
	for _, srv := range services {
		for _, ep := range srv.Endpoints {
			// 获取服务名称
			if ep.Path == endpoint {
				service = srv.Name
			}

			if srvName, exists := c.endpointMap[ep.Path]; exists {
				if srvName != srv.Name {
					log.Errf("same endpiont in diff service %s-%s", srvName, srv.Name)
				}

				continue
			}
			c.endpointMap[ep.Path] = srv.Name
		}
	}
	c.Unlock()

	return c.get(service)
}

// ListServices 返回全量服务清单，带 TTL 缓存。
//
// 必须覆写：registryCacher 只是嵌入 IRegistry，不覆写就直接穿透到后端，
// 「缓存器」这个名字对它完全不成立。而这个方法是「按模型/按路径找属主服务」
// 的入口，处在每个网关请求的必经路径上——真栈实测 20 次 call_kw 打出 61 次
// etcd Range，每次全量扫 800KB+ 注册数据，间歇性把 5s 超时耗尽，表现为
// 跨服务调用约一成随机失败。
//
// 与 get() 同款约定：singleflight 合并并发回源、出错时**退回陈旧缓存**而不是
// 把错误抛给调用方（服务清单短暂过期，远好过整条链路报错），返回副本防止
// 调用方改到缓存里的对象。watch 事件会让缓存立即失效（见 update），所以
// 新注册/注销的可见延迟不受 TTL 限制。
func (c *registryCacher) ListServices() ([]*Service, error) {
	// 与 get() 同理：先判有效再复制，过期时不白拷。listCache 只会被整体替换。
	c.RLock()
	cached := c.listCache
	ttl := c.listTTL
	c.RUnlock()

	if c.isValid(cached, ttl) {
		return Copy(cached), nil
	}

	// ★ 拉取途中来了失效（watch 事件）：拿回来的清单可能早于那次注册，缓存它就等于把失效
	// 吃掉 —— 旧清单再顶一整个 TTL，而触发这次拉取的往往正是那个事件（vectors 的服务目录
	// 收到事件就重建快照，拉到的却是缓存器还没来得及作废、或者途中被作废的旧清单）。
	// 所以回源前后比对 listGen：变了就再拉一次（有限次），拉不稳就只返回、不缓存。
	val, err, _ := c.sg.Do("*list*", func() (interface{}, error) {
		var services []*Service
		for try := 0; try < 3; try++ {
			c.RLock()
			gen := c.listGen
			c.RUnlock()
			got, err := c.IRegistry.ListServices()
			if err != nil {
				return nil, err
			}
			services = got
			c.Lock()
			stable := c.listGen == gen
			if stable {
				c.listCache = got // 交出去的是副本（见下），缓存收下这份即可
				c.listTTL = time.Now().Add(c.config.TTL)
			}
			c.Unlock()
			if stable {
				break
			}
		}
		return services, nil
	})
	if err != nil {
		if len(cached) > 0 {
			c.setStatus(err)
			return Copy(cached), nil
		}
		return nil, err
	}

	services, _ := val.([]*Service)

	if err := c.getStatus(); err != nil {
		c.setStatus(nil)
	}

	return Copy(services), nil
}

func (c *registryCacher) Match(endpoint string) ([]*Service, error) {
	services, err := c.match(endpoint)
	if err != nil {
		return nil, err
	}

	// if there's nothing return err
	if len(services) == 0 {
		return nil, ErrNotFound
	}

	// return services
	return services, nil
}

func (c *registryCacher) GetService(service string) ([]*Service, error) {
	// get the service
	services, err := c.get(service)
	if err != nil {
		return nil, err
	}

	// if there's nothing return err
	if len(services) == 0 {
		return nil, ErrNotFound
	}

	// return services
	return services, nil
}

// Fresh 绕过缓存读一次后端，并把结果写回缓存。
//
// 给"手里的事件比缓存新"的调用方用：缓存靠自己的 watch 增量更新，调用方若另有一条 watch（router），
// 两条流谁先收到同一个事件没有保证，读缓存可能拿到事件之前的样子（见 router.servicesAfterEvent）。
// 后端这一读发生在事件提交之后，至少与事件一样新；写回之后缓存自己的 watch 再应用同一事件是幂等的
// （create 按节点 id 合并、delete 按节点 id 剔除）。
func (c *registryCacher) Fresh(service string) ([]*Service, error) {
	services, err := c.IRegistry.GetService(service)
	if err != nil || len(services) == 0 {
		if err == nil || errors.Is(err, ErrNotFound) {
			c.Lock()
			if _, ok := c.cache[service]; ok {
				c.del(service)
			}
			c.Unlock()
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Lock()
	c.set(service, services)
	cp := Copy(services)
	c.Unlock()
	return cp, nil
}

func (c *registryCacher) LocalServices() []*Service {
	c.RLock()
	defer c.RUnlock()
	return c.localServices
}

func (c *registryCacher) Register(s *Service, opts ...Option) error {
	// ★ 先登记本地身份，**再**写注册中心。顺序是关键，不是风格问题。
	//
	// IRegistry.Register 一落盘，本进程的 watcher 立刻会收到自己这条创建事件并驱动
	// router.store()；那里靠 LocalServices() 认"哪些节点是我自己"。若此时本条注册
	// 还没进 localServices，自己的节点就被当成远端，按它的端点建出一整批指向本进程
	// 监听端口的反向代理路由 —— 而 TTree.addNode 同路径后写覆盖，本地 handler 当场
	// 被顶掉，refresh() 之后又把该服务判成 local 直接跳过，坏路由**再没人纠正**。
	// 真栈症状：kylin 自己托管的模块 `/{service}/static/*` 恒 508（自环护盾），
	// 装/升级模块取不到数据文件而整体失败；每次重启命中的模块还不一样（是竞态）。
	//
	// 提前登记的代价只有一种：注册失败时曾短暂多认了一个"自己"，下面立即回滚。
	//
	// 续租（server 每 30 秒一轮）时身份没变就只续租、不回读：localServices 里那份
	// 已经是上次回读出来的，而它的消费者（filteSelf / isLocalService）只看名字、版本、
	// 节点 id 与地址。此前每轮每个服务都 GetService 一次 —— 一条前缀读 + 整条 JSON
	// 解码 + 逐条验签（stock 一条 400KB），kylin 约 120 个服务，空闲时 etcd 每分钟
	// 千余次 Range、发出 150MB，这一处占 kylin 空闲 CPU 的三分之一（2026-10-10 实测）。
	key, fp := s.Name+"\x00"+s.Version, localIdentityKey(s)
	c.RLock()
	known := c.readBack[key] == fp
	c.RUnlock()
	if known {
		return c.IRegistry.Register(s, opts...)
	}

	restore := c.trackLocalService(s)

	if err := c.IRegistry.Register(s, opts...); err != nil {
		restore()
		return err
	}

	services, err := c.IRegistry.GetService(s.Name)
	if err != nil {
		// 注册本身已成功，本地身份必须留着 —— 回读只是为了补全规范化后的信息。
		return err
	}

	for _, ss := range services {
		if ss.Name != s.Name || ss.Version != s.Version {
			continue
		}
		// 只取回读结果里**属于本进程**的节点。注册中心是按服务名合并的，
		// 同名服务的其它进程节点也在里面；整份存进 localServices 会让
		// filteSelf 把真正的远端节点当成自己剔掉，那个服务就永远没有代理路由。
		//
		// ★ 一个 (name, version) 现在可能对应**好几条** Service：端点集不同的
		// 进程各自成一条（见 EndpointsKey）。挑不出本进程节点的那些是兄弟进程的，
		// 跳过继续找 —— 早年这里是 `break`，遇到兄弟进程排在前面就会把一个空节点
		// 集登记成"我自己"，filteSelf 从此认不出自己的节点。
		if local := withNodesOf(ss, s); len(local.Nodes) > 0 {
			c.trackLocalService(local)
			c.Lock()
			if c.readBack == nil {
				c.readBack = make(map[string]string)
			}
			c.readBack[key] = fp
			c.Unlock()
			break
		}
	}

	return nil
}

// localIdentityKey 是一个本地服务在 filteSelf / isLocalService 眼里的身份：
// 名字、版本、每个节点的 id 与地址（排序后拼接，与节点顺序无关）。
func localIdentityKey(s *Service) string {
	parts := make([]string, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		if n != nil {
			parts = append(parts, n.Id+"@"+n.Address)
		}
	}
	sort.Strings(parts)
	return s.Name + "\x00" + s.Version + "\x00" + strings.Join(parts, ",")
}

// trackLocalService 把 s 记入/替换本进程的注册记录（按 name+version），返回一个
// 把 localServices 还原到调用前状态的函数，供注册失败时回滚。
func (c *registryCacher) trackLocalService(s *Service) (restore func()) {
	c.Lock()
	defer c.Unlock()

	for i, ls := range c.localServices {
		if ls.Name == s.Name && ls.Version == s.Version {
			old := ls
			c.localServices[i] = s
			return func() {
				c.Lock()
				defer c.Unlock()
				if i < len(c.localServices) && c.localServices[i] == s {
					c.localServices[i] = old
				}
			}
		}
	}

	c.localServices = append(c.localServices, s)
	return func() {
		c.Lock()
		defer c.Unlock()
		for i, ls := range c.localServices {
			if ls == s {
				c.localServices = append(c.localServices[:i], c.localServices[i+1:]...)
				return
			}
		}
	}
}

// withNodesOf 返回 srv 的浅拷贝，节点收窄为 mine 里出现过的那些（按 node.Id 匹配）。
// 一个都没匹配上时退回 mine 本身 —— 宁可用自己手上的那份，也不要带上别人的节点。
func withNodesOf(srv, mine *Service) *Service {
	ids := make(map[string]struct{}, len(mine.Nodes))
	for _, n := range mine.Nodes {
		if n != nil {
			ids[n.Id] = struct{}{}
		}
	}

	cp := *srv
	nodes := make([]*Node, 0, len(ids))
	for _, n := range srv.Nodes {
		if n == nil {
			continue
		}
		if _, hit := ids[n.Id]; hit {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return mine
	}

	cp.Nodes = nodes
	return &cp
}

func (c *registryCacher) Deregister(s *Service, opts ...Option) error {
	c.Lock()
	var services []*Service
	for _, ls := range c.localServices {
		if ls.Name == s.Name && ls.Version == s.Version {
			continue
		}
		services = append(services, ls)
	}
	c.localServices = services
	delete(c.readBack, s.Name+"\x00"+s.Version)
	c.Unlock()

	return c.IRegistry.Deregister(s, opts...)
}

func (c *registryCacher) Stop() {
	c.Lock()
	select {
	case <-c.exit:
		c.Unlock()
		return
	default:
		close(c.exit)
	}
	c.Unlock()

	// 持锁阶段已结束，wg.Wait 必须在锁外 —— 否则 run() 的 defer 想取写锁会死锁。
	c.wg.Wait()
}

func (c *registryCacher) String() string {
	return c.config.Name
}
