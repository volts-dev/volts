package noop

import "github.com/volts-dev/volts/registry"

type (
	// NoopRegistry
	noopRegistry struct {
		config *registry.Config
	}
)

func New() *noopRegistry {
	return &noopRegistry{
		config: &registry.Config{},
	}
}

func (*noopRegistry) Init(...registry.Option) error {
	return nil
}

func (self *noopRegistry) Config() *registry.Config {
	return self.config
}

// 注册
func (*noopRegistry) Register(*registry.Service, ...registry.Option) error {
	return nil
}

// 注销
func (*noopRegistry) Deregister(*registry.Service, ...registry.Option) error {
	return nil
}

func (*noopRegistry) GetService(string) ([]*registry.Service, error) {
	return nil, nil
}

func (*noopRegistry) ListServices() ([]*registry.Service, error) {
	return nil, nil
}

func (*noopRegistry) Watcher(...registry.WatchOptions) (registry.Watcher, error) {
	return nil, nil
}

func (*noopRegistry) CurrentService() *registry.Service {
	return nil
}

func (*noopRegistry) String() string {
	return "noopRegistry"
}
