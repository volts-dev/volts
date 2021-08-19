package registry

import "errors"

var (
	DefaultRegistry = NewMdnsRegistry()

	// Not found error when GetService is called
	ErrNotFound = errors.New("service not found")
	// Watcher stopped error when watcher is stopped
	ErrWatcherStopped = errors.New("watcher stopped")
)

type (

	// 注册中心接口
	IRegistry interface {
		Init(...Option) error
		Config() *Config
		Register(*Service, ...Option) error   // 注册
		Deregister(*Service, ...Option) error // 注销
		GetService(string) ([]*Service, error)
		ListServices() ([]*Service, error)
		Watch(...WatchOptions) (Watcher, error)
		String() string
	}

	Service struct {
		Name      string            `json:"name"`
		Version   string            `json:"version"`
		Metadata  map[string]string `json:"metadata"`
		Endpoints []*Endpoint       `json:"endpoints"`
		Nodes     []*Node           `json:"nodes"`
	}

	Node struct {
		Id       string            `json:"id"`
		Address  string            `json:"address"`
		Metadata map[string]string `json:"metadata"`
	}

	Endpoint struct {
		Name     string            `json:"name"`
		Request  *Value            `json:"request"`
		Response *Value            `json:"response"`
		Metadata map[string]string `json:"metadata"`
	}

	Value struct {
		Name   string   `json:"name"`
		Type   string   `json:"type"`
		Values []*Value `json:"values"`
	}
)
