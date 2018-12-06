package server

import (
	"crypto/tls"
	"time"
)

// OptionFn configures options of server.
type FConfig func(*TServer)

// WithTLSConfig sets tls.Config.
func WithTLSConfig(cfg *tls.Config) FConfig {
	return func(s *TServer) {
		s.tlsConfig = cfg
	}
}

// WithReadTimeout sets readTimeout.
func WithReadTimeout(readTimeout time.Duration) FConfig {
	return func(s *TServer) {
		s.Router.readTimeout = readTimeout
	}
}

// WithWriteTimeout sets writeTimeout.
func WithWriteTimeout(writeTimeout time.Duration) FConfig {
	return func(s *TServer) {
		s.Router.writeTimeout = writeTimeout
	}
}
