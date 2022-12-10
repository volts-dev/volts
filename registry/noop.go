package registry

type (
	// NoopRegistry
	noopRegistry struct {
		config *Config
	}
)

func newNoopRegistry() *noopRegistry {
	reg := &noopRegistry{
		config: NewConfig(),
	}
	reg.config.Name = reg.String()
	return reg
}

func (*noopRegistry) Init(...Option) error {
	return nil
}

func (self *noopRegistry) Config() *Config {
	return self.config
}

// 注册
func (*noopRegistry) Register(*Service, ...Option) error {
	return nil
}

// 注销
func (*noopRegistry) Deregister(*Service, ...Option) error {
	return nil
}

func (*noopRegistry) GetService(string) ([]*Service, error) {
	return nil, nil
}

func (*noopRegistry) ListServices() ([]*Service, error) {
	return nil, nil
}

func (*noopRegistry) Watcher(...WatchOptions) (Watcher, error) {
	return nil, nil
}

func (*noopRegistry) LocalServices() []*Service {
	return nil
}

func (*noopRegistry) String() string {
	return "noopRegistry"
}
