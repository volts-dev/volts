package memory

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/registry"
)

var log = registry.Logger()

var (
	sendEventTime = 10 * time.Millisecond
	ttlPruneTime  = time.Second
)

type (
	node struct {
		LastSeen time.Time
		*registry.Node
		TTL time.Duration
	}

	record struct {
		Name      string
		Version   string
		Metadata  map[string]string
		Nodes     map[string]*node
		Endpoints []*registry.Endpoint
	}

	memRegistry struct {
		config *registry.Config

		records  map[string]map[string]*record
		watchers map[string]*memWatcher

		sync.RWMutex
	}
)

func init() {
	registry.Register("memory", New)
}

func New(opts ...registry.Option) registry.IRegistry {
	var defaultOpts []registry.Option
	defaultOpts = append(defaultOpts,
		registry.WithName("memory"),
		registry.Timeout(time.Millisecond*100),
	)

	cfg := registry.NewConfig(append(defaultOpts, opts...)...)

	records := getServiceRecords(cfg.Context)
	if records == nil {
		records = make(map[string]map[string]*record)
	}

	reg := &memRegistry{
		config:   cfg,
		records:  records,
		watchers: make(map[string]*memWatcher),
	}

	go reg.ttlPrune()

	return reg
}

func (m *memRegistry) ttlPrune() {
	prune := time.NewTicker(ttlPruneTime)
	defer prune.Stop()

	for {
		select {
		case <-prune.C:
			m.Lock()
			for name, records := range m.records {
				for version, record := range records {
					for id, n := range record.Nodes {
						if n.TTL != 0 && time.Since(n.LastSeen) > n.TTL {
							log.Dbgf("Registry TTL expired for node %s of service %s", n.Id, name)
							delete(m.records[name][version].Nodes, id)
						}
					}
				}
			}
			m.Unlock()
		}
	}
}

func (m *memRegistry) sendEvent(r *registry.Result) {
	m.RLock()
	watchers := make([]*memWatcher, 0, len(m.watchers))
	for _, w := range m.watchers {
		watchers = append(watchers, w)
	}
	m.RUnlock()

	for _, w := range watchers {
		select {
		case <-w.exit:
			m.Lock()
			delete(m.watchers, w.id)
			m.Unlock()
		default:
			select {
			case w.res <- r:
			case <-time.After(sendEventTime):
			}
		}
	}
}

func (m *memRegistry) Init(opts ...registry.Option) error {
	for _, o := range opts {
		o(m.config)
	}

	// add services
	m.Lock()
	defer m.Unlock()

	records := getServiceRecords(m.config.Context)
	for name, record := range records {
		// add a whole new service including all of its versions
		if _, ok := m.records[name]; !ok {
			m.records[name] = record
			continue
		}
		// add the versions of the service we dont track yet
		for version, r := range record {
			if _, ok := m.records[name][version]; !ok {
				m.records[name][version] = r
				continue
			}
		}
	}

	return nil
}

func (m *memRegistry) Config() *registry.Config {
	return m.config
}

func (m *memRegistry) Register(s *registry.Service, opts ...registry.Option) error {
	m.Lock()
	defer m.Unlock()
	cfg := m.config
	for _, o := range opts {
		o(cfg)
	}

	r := serviceToRecord(s, cfg.TTL)

	if _, ok := m.records[s.Name]; !ok {
		m.records[s.Name] = make(map[string]*record)
	}

	if _, ok := m.records[s.Name][s.Version]; !ok {
		m.records[s.Name][s.Version] = r
		log.Dbgf("Registry added new service: %s, version: %s", s.Name, s.Version)
		go m.sendEvent(&registry.Result{Action: "update", Service: s})
		return nil
	}

	addedNodes := false
	for _, n := range s.Nodes {
		if _, ok := m.records[s.Name][s.Version].Nodes[n.Id]; !ok {
			addedNodes = true
			metadata := make(map[string]string)
			for k, v := range n.Metadata {
				metadata[k] = v
			}
			m.records[s.Name][s.Version].Nodes[n.Id] = &node{
				Node: &registry.Node{
					Id:       n.Id,
					Address:  n.Address,
					Metadata: metadata,
				},
				TTL:      cfg.TTL,
				LastSeen: time.Now(),
			}
		}
	}

	cfg.LocalServices = append(cfg.LocalServices, s)

	if addedNodes {
		log.Dbgf("Registry added new node to service: %s, version: %s", s.Name, s.Version)
		go m.sendEvent(&registry.Result{Action: "update", Service: s})
		return nil
	}

	// refresh TTL and timestamp
	for _, n := range s.Nodes {
		log.Dbgf("Updated registration for service: %s, version: %s", s.Name, s.Version)
		m.records[s.Name][s.Version].Nodes[n.Id].TTL = cfg.TTL
		m.records[s.Name][s.Version].Nodes[n.Id].LastSeen = time.Now()
	}

	return nil
}

func (m *memRegistry) Deregister(s *registry.Service, opts ...registry.Option) error {
	m.Lock()
	defer m.Unlock()

	if _, ok := m.records[s.Name]; ok {
		if _, ok := m.records[s.Name][s.Version]; ok {
			for _, n := range s.Nodes {
				if _, ok := m.records[s.Name][s.Version].Nodes[n.Id]; ok {
					log.Dbgf("Registry removed node from service: %s, version: %s", s.Name, s.Version)
					delete(m.records[s.Name][s.Version].Nodes, n.Id)
				}
			}
			if len(m.records[s.Name][s.Version].Nodes) == 0 {
				delete(m.records[s.Name], s.Version)
				log.Dbgf("Registry removed service: %s, version: %s", s.Name, s.Version)
			}
		}
		if len(m.records[s.Name]) == 0 {
			delete(m.records, s.Name)
			log.Dbgf("Registry removed service: %s", s.Name)
		}
		go m.sendEvent(&registry.Result{Action: "delete", Service: s})
	}

	return nil
}

func (m *memRegistry) GetService(name string) ([]*registry.Service, error) {
	m.RLock()
	defer m.RUnlock()

	records, ok := m.records[name]
	if !ok {
		return nil, registry.ErrNotFound
	}

	services := make([]*registry.Service, len(m.records[name]))
	i := 0
	for _, record := range records {
		services[i] = recordToService(record)
		i++
	}

	return services, nil
}

func (m *memRegistry) LocalServices() []*registry.Service {
	return m.config.LocalServices
}

func (m *memRegistry) ListServices() ([]*registry.Service, error) {
	m.RLock()
	defer m.RUnlock()

	var services []*registry.Service
	for _, records := range m.records {
		for _, record := range records {
			services = append(services, recordToService(record))
		}
	}

	return services, nil
}

func (m *memRegistry) Watcher(opts ...registry.WatchOptions) (registry.Watcher, error) {
	var wo registry.WatchConfig
	for _, o := range opts {
		o(&wo)
	}

	w := &memWatcher{
		exit: make(chan bool),
		res:  make(chan *registry.Result),
		id:   uuid.New().String(),
		wo:   wo,
	}

	m.Lock()
	m.watchers[w.id] = w
	m.Unlock()

	return w, nil
}

func (m *memRegistry) String() string {
	return m.config.Name
}
