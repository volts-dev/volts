package consul

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	consul "github.com/hashicorp/consul/api"
	hash "github.com/mitchellh/hashstructure/v2"
	mnet "github.com/volts-dev/volts/internal/net"
	vtls "github.com/volts-dev/volts/internal/tls"
	"github.com/volts-dev/volts/registry"
)

type consulRegistry struct {
	Address []string
	opts    *registry.Config
	client  *consul.Client
	config  *consul.Config

	// connect enabled
	connect bool

	queryOptions *consul.QueryOptions

	sync.Mutex
	register map[string]uint64
	// lastChecked tracks when a node was last checked as existing in Consul
	lastChecked map[string]time.Time
}

func init() {
	registry.Register("consul", New)
}

func New(opts ...registry.Option) registry.IRegistry {
	var defaultOpts []registry.Option
	defaultOpts = append(defaultOpts,
		registry.WithName("consul"),
		registry.Timeout(time.Millisecond*100),
	)

	cr := &consulRegistry{
		opts:        registry.NewConfig(append(defaultOpts, opts...)...),
		register:    make(map[string]uint64),
		lastChecked: make(map[string]time.Time),
		queryOptions: &consul.QueryOptions{
			AllowStale: true,
		},
	}
	configure(cr)
	return cr
}

func getDeregisterTTL(t time.Duration) time.Duration {
	// splay slightly for the watcher?
	splay := time.Second * 5
	deregTTL := t + splay

	// consul has a minimum timeout on deregistration of 1 minute.
	if t < time.Minute {
		deregTTL = time.Minute + splay
	}

	return deregTTL
}

// newTransport 构造访问 Consul 的 HTTP transport。
//
// config 为 nil 时使用「开启证书校验 + TLS>=1.2」的安全默认。此前默认
// InsecureSkipVerify:true —— 中间人可篡改服务发现应答，把整个集群的 RPC 流量
// 导向攻击者节点，危害远大于劫持单条业务连接。要跳过校验必须显式传入
// &tls.Config{InsecureSkipVerify: true}。
func newTransport(config *tls.Config) *http.Transport {
	config = vtls.Secure(config)

	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     config,
	}
	runtime.SetFinalizer(&t, func(tr **http.Transport) {
		(*tr).CloseIdleConnections()
	})
	return t
}

func configure(c *consulRegistry) {
	// use default non pooled config
	config := consul.DefaultNonPooledConfig()
	c.opts.Name = c.String()
	if c.opts.Context != nil {
		// Use the consul config passed in the options, if available
		if co, ok := c.opts.Context.Value("consul_config").(*consul.Config); ok {
			config = co
		}
		if cn, ok := c.opts.Context.Value("consul_connect").(bool); ok {
			c.connect = cn
		}

		// Use the consul query options passed in the options, if available
		if qo, ok := c.opts.Context.Value("consul_query_options").(*consul.QueryOptions); ok && qo != nil {
			c.queryOptions = qo
		}
		if as, ok := c.opts.Context.Value("consul_allow_stale").(bool); ok {
			c.queryOptions.AllowStale = as
		}
	}

	// check if there are any addrs
	var addrs []string

	// iterate the options addresses
	for _, address := range c.opts.Addrs {
		// check we have a port
		addr, port, err := net.SplitHostPort(address)
		if ae, ok := err.(*net.AddrError); ok && ae.Err == "missing port in address" {
			port = "8500"
			addr = address
			addrs = append(addrs, net.JoinHostPort(addr, port))
		} else if err == nil {
			addrs = append(addrs, net.JoinHostPort(addr, port))
		}
	}

	// set the addrs
	if len(addrs) > 0 {
		c.Address = addrs
		config.Address = c.Address[0]
	}

	if config.HttpClient == nil {
		config.HttpClient = new(http.Client)
	}

	// requires secure connection?
	if c.opts.Secure || c.opts.TlsConfig != nil {
		config.Scheme = "https"
		// We're going to support InsecureSkipVerify
		config.HttpClient.Transport = newTransport(c.opts.TlsConfig)
	}

	// set timeout
	if c.opts.Timeout > 0 {
		config.HttpClient.Timeout = c.opts.Timeout
	}

	// set the config
	c.config = config

	// remove client
	c.client = nil

	// setup the client
	c.Client()
}

func (c *consulRegistry) Init(opts ...registry.Option) error {
	c.opts.Init(opts...)
	configure(c)
	return nil
}

// serviceID 是一个 (服务, 节点) 在 consul 里的服务实例 id。
//
// consul 的服务 id 在一个 agent 上唯一，而一个进程注册的所有服务共用一个节点 id（vectors 每个
// 模块一个服务名）。曾直接拿节点 id 当服务 id：后注册的服务把先注册的整个覆盖掉，注销一个就
// 把整个进程摘了。
func serviceID(name, nodeID string) string {
	return name + idSep + nodeID
}

// nodeIDOf 从服务实例 id 还原节点 id。不带服务名前缀的（旧二进制注册的）原样返回。
func nodeIDOf(name, id string) string {
	return strings.TrimPrefix(id, name+idSep)
}

const idSep = "~"

// groupKey 是同名服务下哪些实例并成一个 registry.Service：版本 + 描述（registry.GroupKey）。
func groupKey(s *registry.Service) string {
	return s.Version + "\x00" + registry.GroupKey(s)
}

func (c *consulRegistry) Deregister(s *registry.Service, opts ...registry.Option) error {
	if len(s.Nodes) == 0 {
		return errors.New("Require at least one node")
	}

	var gerr error
	for _, node := range s.Nodes {
		id := serviceID(s.Name, node.Id)

		// delete our hash and time check of the service
		c.Lock()
		delete(c.register, id)
		delete(c.lastChecked, id)
		c.Unlock()

		if err := c.Client().Agent().ServiceDeregister(id); err != nil {
			gerr = err
		}
	}
	return gerr
}

func (c *consulRegistry) Register(s *registry.Service, opts ...registry.Option) error {
	if len(s.Nodes) == 0 {
		return errors.New("Require at least one node")
	}

	var regTCPCheck bool
	var regInterval time.Duration

	var options registry.Config
	for _, o := range opts {
		o(&options)
	}

	if c.opts.Context != nil {
		if tcpCheckInterval, ok := c.opts.Context.Value("consul_tcp_check").(time.Duration); ok {
			regTCPCheck = true
			regInterval = tcpCheckInterval
		}
	}

	// create hash of service; uint64
	//
	// NOTE 用 FormatV2。hashstructure v1 的 FormatV1 存在结构碰撞（不同结构可能
	// 算出同一个哈希），而这个哈希正是用来判断"服务是否变更、要不要重新注册"的
	// —— 碰撞意味着**变更被漏掉**，节点会带着过期的元数据继续留在注册中心。
	// 哈希只存在进程内存里（c.register），升级后至多多做一次重复注册。
	h, err := hash.Hash(s, hash.FormatV2, nil)
	if err != nil {
		return err
	}

	// use first node
	node := s.Nodes[0]
	id := serviceID(s.Name, node.Id)

	// get existing hash and last checked time
	//
	// 按服务实例 id 记，不按服务名：同一进程的 http 与 rpc 两个服务器用两个节点注册同一个服务名，
	// 按名字记的话两边的哈希来回顶替，每一轮都当成「变了」整条重新注册。
	c.Lock()
	v, ok := c.register[id]
	lastChecked := c.lastChecked[id]
	c.Unlock()

	// if it's already registered and matches then just pass the check
	if ok && v == h {
		if options.TTL == time.Duration(0) {
			// ensure that our service hasn't been deregistered by Consul
			if time.Since(lastChecked) <= getDeregisterTTL(regInterval) {
				return nil
			}
			services, _, err := c.Client().Health().Checks(s.Name, c.queryOptions)
			if err == nil {
				for _, v := range services {
					if v.ServiceID == id {
						return nil
					}
				}
			}
		} else {
			// if the err is nil we're all good, bail out
			// if not, we don't know what the state is, so full re-register
			if err := c.Client().Agent().PassTTL("service:"+id, ""); err == nil {
				return nil
			}
		}
	}

	// encode the tags
	tags := encodeMetadata(node.Metadata)
	tags = append(tags, encodeServiceMetadata(s.Metadata)...)
	tags = append(tags, encodeEndpoints(s.Endpoints)...)
	tags = append(tags, encodeVersion(s.Version)...)

	var check *consul.AgentServiceCheck

	if regTCPCheck {
		deregTTL := getDeregisterTTL(regInterval)

		check = &consul.AgentServiceCheck{
			TCP:                            node.Address,
			Interval:                       fmt.Sprintf("%v", regInterval),
			DeregisterCriticalServiceAfter: fmt.Sprintf("%v", deregTTL),
		}

		// if the TTL is greater than 0 create an associated check
	} else if options.TTL > time.Duration(0) {
		deregTTL := getDeregisterTTL(options.TTL)

		check = &consul.AgentServiceCheck{
			TTL:                            fmt.Sprintf("%v", options.TTL),
			DeregisterCriticalServiceAfter: fmt.Sprintf("%v", deregTTL),
		}
	}

	host, pt, _ := net.SplitHostPort(node.Address)
	if host == "" {
		host = node.Address
	}
	port, _ := strconv.Atoi(pt)

	// register the service
	asr := &consul.AgentServiceRegistration{
		ID:      id,
		Name:    s.Name,
		Tags:    tags,
		Port:    port,
		Address: host,
		Check:   check,
	}

	// Specify consul connect
	if c.connect {
		asr.Connect = &consul.AgentServiceConnect{
			Native: true,
		}
	}

	if err := c.Client().Agent().ServiceRegister(asr); err != nil {
		return err
	}

	// save our hash and time check of the service
	c.Lock()
	c.register[id] = h
	c.lastChecked[id] = time.Now()
	c.Unlock()

	// if the TTL is 0 we don't mess with the checks
	if options.TTL == time.Duration(0) {
		return nil
	}

	// pass the healthcheck
	return c.Client().Agent().PassTTL("service:"+id, "")
}


func (c *consulRegistry) GetService(name string) ([]*registry.Service, error) {
	var rsp []*consul.ServiceEntry
	var err error

	// if we're connect enabled only get connect services
	if c.connect {
		rsp, _, err = c.Client().Health().Connect(name, "", false, c.queryOptions)
	} else {
		rsp, _, err = c.Client().Health().Service(name, "", false, c.queryOptions)
	}
	if err != nil {
		return nil, err
	}

	serviceMap := map[string]*registry.Service{}

	for _, s := range rsp {
		if s.Service.Service != name {
			continue
		}

		// version is now a tag
		version, _ := decodeVersion(s.Service.Tags)
		id := nodeIDOf(name, s.Service.ID)

		// address is service address
		address := s.Service.Address

		// use node address
		if len(address) == 0 {
			address = s.Node.Address
		}

		cand := &registry.Service{
			Endpoints: decodeEndpoints(s.Service.Tags),
			Name:      s.Service.Service,
			Version:   version,
			Metadata:  decodeServiceMetadata(s.Service.Tags),
		}
		key := groupKey(cand)
		svc, ok := serviceMap[key]
		if !ok {
			svc = cand
			serviceMap[key] = svc
		}

		var del bool

		for _, check := range s.Checks {
			// delete the node if the status is critical
			if check.Status == "critical" {
				del = true
				break
			}
		}

		// if delete then skip the node
		if del {
			continue
		}

		svc.Nodes = append(svc.Nodes, &registry.Node{
			Id:       id,
			Address:  mnet.HostPort(address, s.Service.Port),
			Metadata: decodeMetadata(s.Service.Tags),
		})
	}

	var services []*registry.Service
	for _, service := range serviceMap {
		services = append(services, service)
	}
	return services, nil
}

func (c *consulRegistry) ListServices() ([]*registry.Service, error) {
	rsp, _, err := c.Client().Catalog().Services(c.queryOptions)
	if err != nil {
		return nil, err
	}

	var services []*registry.Service

	for service := range rsp {
		services = append(services, &registry.Service{Name: service})
	}

	return services, nil
}

func (c *consulRegistry) Watcher(opts ...registry.WatchOptions) (registry.Watcher, error) {
	return newConsulWatcher(c, opts...)
}

func (c *consulRegistry) String() string {
	return c.opts.Name
}

func (c *consulRegistry) Config() *registry.Config {
	return c.opts
}

func (c *consulRegistry) Client() *consul.Client {
	// 持锁串行化首次创建。原代码并发调用会创建多个 client 并随意覆盖 c.client，
	// 上一个 client 持有的连接不会被关闭 → 连接泄漏。
	c.Lock()
	defer c.Unlock()

	if c.client != nil {
		return c.client
	}

	for _, addr := range c.Address {
		// set the address
		c.config.Address = addr

		// create a new client
		tmpClient, _ := consul.NewClient(c.config)

		// test the client
		_, err := tmpClient.Agent().Host()
		if err != nil {
			continue
		}

		// set the client
		c.client = tmpClient
		return c.client
	}

	// set the default
	c.client, _ = consul.NewClient(c.config)

	// return the client
	return c.client
}
