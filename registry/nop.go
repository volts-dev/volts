package registry

type (
	// NopRegistry
	nopRegistry struct {
		config *Config
	}
)

func NewNopRegistry() *nopRegistry {
	reg := &nopRegistry{
		//	config: NewConfig(
		//		WithName(""),
		//	),
		config: &Config{
			Name: "",
		},
	}
	return reg
}

func (self *nopRegistry) Init(opts ...Option) error {
	//self.config.Init(opts...)
	return nil
}

func (self *nopRegistry) Config() *Config {
	return self.config
}

// 注册
func (*nopRegistry) Register(*Service, ...Option) error {
	return nil
}

// 注销
func (*nopRegistry) Deregister(*Service, ...Option) error {
	return nil
}

func (*nopRegistry) GetService(string) ([]*Service, error) {
	return nil, nil
}

func (*nopRegistry) ListServices() ([]*Service, error) {
	return nil, nil
}

func (*nopRegistry) Watcher(...WatchOptions) (Watcher, error) {
	return nil, nil
}


func (self *nopRegistry) String() string {
	return "" // self.config.Name
}
