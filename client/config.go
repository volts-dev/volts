package client

import (
	"crypto/tls"
	"time"
	"vectors/volts/codec"
	"vectors/volts/protocol"
)

// Option contains all options for creating clients.
type Option struct {
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

	// BackupLatency is used for Failbackup mode. rpcx will sends another request if the first response doesn't return in BackupLatency time.
	BackupLatency time.Duration

	// Breaker is used to config CircuitBreaker
	///Breaker Breaker

	SerializeType codec.SerializeType
	CompressType  protocol.CompressType

	Heartbeat         bool
	HeartbeatInterval time.Duration
}

// DefaultOption is a common option configuration for client.
var DefaultOption = Option{
	Retries: 3,
	//RPCPath:        share.DefaultRPCPath,
	ConnectTimeout: 10 * time.Second,
	SerializeType:  codec.JSON,
	CompressType:   protocol.None,
	BackupLatency:  10 * time.Millisecond,
}