package client

import (
	"crypto/tls"
	"net"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/protocol"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
)

type (
	// Option contains all options for creating clients.
	Option func(*Config) error
	Config struct {
		Registry registry.IRegistry
		Selector selector.ISelector

		conn     net.Conn
		protocol string

		// Group is used to select the services in the same group. Services set group info in their meta.
		// If it is empty, clients will ignore group.
		Group string

		// Retries retries to send
		Retries int

		// TLSConfig for tcp and quic
		TLSConfig *tls.Config
		// kcp.BlockCrypt
		Block interface{}
		// RPCPath for http connection
		RPCPath string
		//ConnectTimeout sets timeout for dialing
		ConnectTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// BackupLatency is used for Failbackup mode. rpc will sends another request if the first response doesn't return in BackupLatency time.
		BackupLatency time.Duration

		// Breaker is used to config CircuitBreaker
		///Breaker Breaker

		SerializeType codec.SerializeType
		CompressType  protocol.CompressType

		Heartbeat         bool
		HeartbeatInterval time.Duration
	}
)

func newConfig(fileName ...string) *Config {
	return &Config{
		Retries: 3,
		//RPCPath:        share.DefaultRPCPath,
		ConnectTimeout: 10 * time.Second,
		SerializeType:  codec.JSON,
		CompressType:   protocol.None,
		BackupLatency:  10 * time.Millisecond,
	}
}
