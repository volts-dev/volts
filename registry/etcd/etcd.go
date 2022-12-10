// Package etcd provides an etcd service registry
package etcd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	hash "github.com/mitchellh/hashstructure"
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

type etcdRegistry struct {
	client *clientv3.Client
	config *registry.Config

	sync.RWMutex
	register map[string]uint64
	leases   map[string]clientv3.LeaseID
}

func New(opts ...registry.Option) registry.IRegistry {
	opts = append(opts, registry.Timeout(time.Millisecond*100))

	reg := &etcdRegistry{
		config:   registry.NewConfig(opts...),
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

	if e.config.Timeout == 0 {
		e.config.Timeout = 5 * time.Second
	}

	if e.config.Secure || e.config.TlsConfig != nil {
		tlsConfig := e.config.TlsConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{
				InsecureSkipVerify: true,
			}
		}

		config.TLS = tlsConfig
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
	leaseID, ok := e.leases[s.Name+node.Uid]
	e.RUnlock()

	if !ok {
		// missing lease, check if the key exists
		ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
		defer cancel()

		// look for the existing key
		rsp, err := e.client.Get(ctx, nodePath(s.Name, node.Uid), clientv3.WithSerializable())
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
				h, err := hash.Hash(srv.Nodes[0], nil)
				if err != nil {
					continue
				}

				// save the info
				e.Lock()
				e.leases[s.Name+node.Uid] = leaseID
				e.register[s.Name+node.Uid] = h
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
	h, err := hash.Hash(node, nil)
	if err != nil {
		return err
	}

	// get existing hash for the service node
	e.Lock()
	v, ok := e.register[s.Name+node.Uid]
	e.Unlock()

	// the service is unchanged, skip registering
	if ok && v == h && !leaseNotFound {
		if log.GetLevel() == logger.LevelTrace {
			log.Tracef("Service %s node %s unchanged skipping registration", s.Name, node.Uid)
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
	if options.Ttl.Seconds() > 0 {
		// get a lease used to expire keys since we have a ttl
		lgr, err = e.client.Grant(ctx, int64(options.Ttl.Seconds()))
		if err != nil {
			return err
		}
	}

	//if logger.V(logger.TraceLevel, logger.DefaultLogger) {
	log.Dbgf("Registering %s id %s with lease %v and leaseID %v and ttl %v", service.Name, node.Uid, lgr, lgr.ID, options.Ttl)
	//}
	// create an entry for the node
	if lgr != nil {
		_, err = e.client.Put(ctx, nodePath(service.Name, node.Uid), encode(service), clientv3.WithLease(lgr.ID))
	} else {
		_, err = e.client.Put(ctx, nodePath(service.Name, node.Uid), encode(service))
	}
	if err != nil {
		return err
	}

	e.Lock()
	// save our hash of the service
	e.register[s.Name+node.Uid] = h
	// save our leaseID of the service
	if lgr != nil {
		e.leases[s.Name+node.Uid] = lgr.ID
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
		delete(e.register, s.Name+node.Uid)
		// delete our lease of the service
		delete(e.leases, s.Name+node.Uid)
		e.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), e.config.Timeout)
		defer cancel()

		//if logger.V(logger.TraceLevel, logger.DefaultLogger) {
		log.Dbgf("Deregistering %s id %s", s.Name, node.Uid)
		//}
		_, err := e.client.Delete(ctx, nodePath(s.Name, node.Uid))
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
	if gerr == nil {
		e.config.LocalServices = append(e.config.LocalServices, s)
	}

	return gerr
}

func (m *etcdRegistry) LocalServices() []*registry.Service {
	return m.config.LocalServices
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
	return "Etcd"
}
