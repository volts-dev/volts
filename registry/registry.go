package registry

import (
	"errors"

	"github.com/volts-dev/volts/logger"
)

var log = logger.New("Registry")
var (
	defaultRegistry IRegistry

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
		CurrentService() *Service
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
		Uid      string            `json:"id"`
		Address  string            `json:"address"`
		Metadata map[string]string `json:"metadata"`
	}

	Endpoint struct {
		// RPC Method e.g. Greeter.Hello
		Name string `json:"name"`
		// HTTP Host e.g example.com
		Host []string `json:"host"`
		// HTTP Methods e.g GET, POST
		Method []string `json:"method"`
		// HTTP Path e.g /greeter. Expect POSIX regex
		Path string `json:"path"`
		// Description e.g what's this endpoint for
		Description string `json:"description"`
		// Stream flag
		Stream bool `json:"stream"`

		// 以下待确认
		Request  *Value            `json:"request"`
		Response *Value            `json:"response"`
		Metadata map[string]string `json:"metadata"`

		// API Handler e.g rpc, proxy
		Handler string
		// Body destination
		// "*" or "" - top level message value
		// "string" - inner message value
		Body string
	}

	Value struct {
		Name   string   `json:"name"`
		Type   string   `json:"type"`
		Values []*Value `json:"values"`
	}
)

func Default(new ...IRegistry) IRegistry {
	if new != nil {
		defaultRegistry = new[0]
	} else {
		if defaultRegistry == nil {
			defaultRegistry = New()
		}
	}
	return defaultRegistry
}

// 比对服务节点UID是否一致，
func (self Service) Equal(to *Service) bool {
	if len(self.Nodes) == len(to.Nodes) {
		var macth bool
		for _, node := range self.Nodes {
			macth = false
			for _, n := range to.Nodes {
				if node.Uid == n.Uid {
					macth = true
					break
				}
			}

			if !macth {
				return false
			}
		}
	}

	return true
}
