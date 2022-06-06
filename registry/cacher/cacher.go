// Package cache provides a registry cache
package cacher

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	util "github.com/volts-dev/volts/util/registry"
	"golang.org/x/sync/singleflight"
)

// Cache is the registry cache interface
type (
	ICacher interface {
		// embed the registry interface
		registry.IRegistry
		// stop the cache watcher
		Stop()
	}

	cache struct {
		registry.IRegistry
		config *Config

		// registry cache
		sync.RWMutex
		cache   map[string][]*registry.Service
		ttls    map[string]time.Time
		watched map[string]bool

		// used to stop the cache
		exit chan bool

		// indicate whether its running
		running bool
		// status of the registry
		// used to hold onto the cache
		// in failure state
		status error
		// used to prevent cache breakdwon
		sg singleflight.Group
	}
)

var (
	DefaultTTL = time.Minute
)

func backoff(attempts int) time.Duration {
	if attempts == 0 {
		return time.Duration(0)
	}
	return time.Duration(math.Pow(10, float64(attempts))) * time.Millisecond
}

func (c *cache) getStatus() error {
	c.RLock()
	defer c.RUnlock()
	return c.status
}

func (c *cache) setStatus(err error) {
	c.Lock()
	c.status = err
	c.Unlock()
}

// isValid checks if the service is valid
func (c *cache) isValid(services []*registry.Service, ttl time.Time) bool {
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

func (c *cache) quit() bool {
	select {
	case <-c.exit:
		return true
	default:
		return false
	}
}

func (c *cache) del(service string) {
	// don't blow away cache in error state
	if err := c.status; err != nil {
		return
	}
	// otherwise delete entries
	delete(c.cache, service)
	delete(c.ttls, service)
}

func (c *cache) Contain(service string) bool {
	_, has := c.cache[service]
	return has
}

func (c *cache) get(service string) ([]*registry.Service, error) {
	// read lock
	c.RLock()

	// check the cache first
	services := c.cache[service]
	// get cache ttl
	ttl := c.ttls[service]
	// make a copy
	cp := util.Copy(services)

	// got services && within ttl so return cache
	if c.isValid(cp, ttl) {
		c.RUnlock()
		// return services
		return cp, nil
	}

	// get does the actual request for a service and cache it
	get := func(service string, cached []*registry.Service) ([]*registry.Service, error) {
		// ask the registry
		val, err, _ := c.sg.Do(service, func() (interface{}, error) {
			return c.IRegistry.GetService(service)
		})
		services, _ := val.([]*registry.Service)
		if err != nil {
			// check the cache
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

		// cache results
		c.Lock()
		c.set(service, util.Copy(services))
		c.Unlock()

		return services, nil
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
			go c.run()
		}

		c.Unlock()
	}

	// get and return services
	return get(service, cp)
}

func (c *cache) set(service string, services []*registry.Service) {
	c.cache[service] = services
	c.ttls[service] = time.Now().Add(c.config.TTL)
}

func (c *cache) update(res *registry.Result) {
	if res == nil || res.Service == nil {
		return
	}

	c.Lock()
	defer c.Unlock()

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
	var service *registry.Service
	var index int
	for i, s := range services {
		if s.Version == res.Service.Version {
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
				if cur.Uid == node.Uid {
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

		var nodes []*registry.Node

		// filter cur nodes to remove the dead one
		for _, cur := range service.Nodes {
			var seen bool
			for _, del := range res.Service.Nodes {
				if del.Uid == cur.Uid {
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
		var srvs []*registry.Service
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
func (c *cache) run() {
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
		w, err := c.IRegistry.Watch()
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
func (c *cache) watch(w registry.Watcher) error {
	// used to stop the watch
	stop := make(chan bool)

	// manage this loop
	go func() {
		defer w.Stop()

		select {
		// wait for exit
		case <-c.exit:
			return
		// we've been stopped
		case <-stop:
			return
		}
	}()

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

func (c *cache) GetService(service string) ([]*registry.Service, error) {
	// get the service
	services, err := c.get(service)
	if err != nil {
		return nil, err
	}

	// if there's nothing return err
	if len(services) == 0 {
		return nil, registry.ErrNotFound
	}

	// return services
	return services, nil
}

func (c *cache) Stop() {
	c.Lock()
	defer c.Unlock()

	select {
	case <-c.exit:
		return
	default:
		close(c.exit)
	}
}

func (c *cache) String() string {
	return "cache"
}

// New returns a new cache
func New(r registry.IRegistry, opts ...Option) ICacher {
	rand.Seed(time.Now().UnixNano())
	cfg := &Config{
		TTL: DefaultTTL,
	}

	for _, o := range opts {
		o(cfg)
	}

	return &cache{
		IRegistry: r,
		config:    cfg,
		watched:   make(map[string]bool),
		cache:     make(map[string][]*registry.Service),
		ttls:      make(map[string]time.Time),
		exit:      make(chan bool),
	}
}
