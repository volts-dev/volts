// Package etcd provides an etcd service registry
package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	hash "github.com/mitchellh/hashstructure/v2"
	vtls "github.com/volts-dev/volts/internal/tls"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
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
}

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

				// decode the existing node
				srv := decode(kv.Value)
				if srv == nil || len(srv.Nodes) == 0 {
					continue
				}

				// create hash of service; uint64
				h, err := hash.Hash(srv.Nodes[0], hash.FormatV2, nil)
				if err != nil {
					continue
				}

				// save the info
				e.Lock()
				e.leases[s.Name+node.Id] = leaseID
				e.register[s.Name+node.Id] = h
				e.Unlock()

				break
			}
		}
	}

	var leaseNotFound bool

	// renew the lease if it exists
	if leaseID > 0 {
		log.Tracef("Renewing existing lease for %s %d", s.Name, leaseID)
		if _, err := e.client.KeepAliveOnce(context.TODO(), leaseID); err != nil {
			if err != rpctypes.ErrLeaseNotFound {
				return err
			}

			log.Tracef("Lease not found for %s %d", s.Name, leaseID)
			// lease not found do register
			leaseNotFound = true
		}
	}

	// create hash of service; uint64
	h, err := hash.Hash(node, hash.FormatV2, nil)
	if err != nil {
		return err
	}

	// get existing hash for the service node
	e.Lock()
	v, ok := e.register[s.Name+node.Id]
	e.Unlock()

	// the service is unchanged, skip registering
	if ok && v == h && !leaseNotFound {
		if log.GetLevel() == logger.LevelTrace {
			log.Tracef("Service %s node %s unchanged skipping registration", s.Name, node.Id)
		}
		return nil
	}

	service := &registry.Service{
		Name:      s.Name,
		Version:   s.Version,
		Metadata:  s.Metadata,
		Endpoints: s.Endpoints,
		Nodes:     []*registry.Node{node},
	}

	var options registry.Config
	for _, o := range opts {
		o(&options)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
	defer cancel()

	var lgr *clientv3.LeaseGrantResponse
	if options.TTL.Seconds() > 0 {
		// get a lease used to expire keys since we have a ttl
		lgr, err = e.client.Grant(ctx, int64(options.TTL.Seconds()))
		if err != nil {
			return err
		}
	}

	//if logger.V(logger.TraceLevel, logger.DefaultLogger) {
	log.Dbgf("Registering %s id %s with lease %v and leaseID %v and ttl %v", service.Name, node.Id, lgr, lgr.ID, options.TTL)
	//}
	// create an entry for the node
	if lgr != nil {
		_, err = e.client.Put(ctx, nodePath(service.Name, node.Id), encode(service), clientv3.WithLease(lgr.ID))
	} else {
		_, err = e.client.Put(ctx, nodePath(service.Name, node.Id), encode(service))
	}
	if err != nil {
		return err
	}

	e.Lock()
	// save our hash of the service
	e.register[s.Name+node.Id] = h
	// save our leaseID of the service
	if lgr != nil {
		e.leases[s.Name+node.Id] = lgr.ID
	}
	e.Unlock()

	return nil
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

	rsp, err := e.client.Get(ctx, servicePath(name)+"/", clientv3.WithPrefix(), clientv3.WithSerializable())
	if err != nil {
		return nil, err
	}

	if len(rsp.Kvs) == 0 {
		return nil, registry.ErrNotFound
	}

	serviceMap := map[string]*registry.Service{}

	for _, n := range rsp.Kvs {
		if sn := decode(n.Value); sn != nil {
			s, ok := serviceMap[sn.Version]
			if !ok {
				s = &registry.Service{
					Name:      sn.Name,
					Version:   sn.Version,
					Metadata:  sn.Metadata,
					Endpoints: sn.Endpoints,
				}
				serviceMap[s.Version] = s
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
	versions := make(map[string]*registry.Service)

	ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
	defer cancel()

	rsp, err := e.client.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithSerializable())
	if err != nil {
		return nil, err
	}

	if len(rsp.Kvs) == 0 {
		return []*registry.Service{}, nil
	}

	for _, n := range rsp.Kvs {
		sn := decode(n.Value)
		if sn == nil {
			continue
		}
		v, ok := versions[sn.Name+sn.Version]
		if !ok {
			versions[sn.Name+sn.Version] = sn
			continue
		}
		// append to service:version nodes
		v.Nodes = append(v.Nodes, sn.Nodes...)
	}

	services := make([]*registry.Service, 0, len(versions))
	for _, service := range versions {
		services = append(services, service)
	}

	// sort the services
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

	return services, nil
}

func (e *etcdRegistry) Watcher(opts ...registry.WatchOptions) (registry.Watcher, error) {
	return newEtcdWatcher(e, e.config.Timeout, opts...)
}

func (e *etcdRegistry) String() string {
	return e.config.Name
}
