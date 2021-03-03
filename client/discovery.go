package client

// 服务发现

type (
	// KVPair contains a key and a string.
	KVPair struct {
		Key   string
		Value string
	}

	// ServiceDiscovery defines ServiceDiscovery of zookeeper, etcd and consul
	IDiscovery interface {
		GetServices() []*KVPair
		WatchService() chan []*KVPair
		RemoveWatcher(ch chan []*KVPair)
		Clone(servicePath string) IDiscovery
		Close()
	}
)
