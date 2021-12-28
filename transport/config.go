package transport

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/asim/go-micro/v3/codec"
)

type (
	Option       func(*Config)
	DialOption   func(*DialConfig)
	ListenOption func(*ListenConfig)

	Config struct {
		Listener IListener
		// Addrs is the list of intermediary addresses to connect to
		Addrs []string
		// Codec is the codec interface to use where headers are not supported
		// by the transport and the entire payload must be encoded
		Codec codec.Marshaler
		// Secure tells the transport to secure the connection.
		// In the case TLSConfig is not specified best effort self-signed
		// certs should be used
		Secure bool
		// TLSConfig to secure the connection. The assumption is that this
		// is mTLS keypair
		TLSConfig *tls.Config

		//ConnectTimeout sets timeout for dialing
		ConnectTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	DialConfig struct {
		// Tells the transport this is a streaming connection with
		// multiple calls to send/recv and that send may not even be called
		Stream bool
		// Timeout for dialing
		//Timeout time.Duration

		// TODO: add tls options when dialling
		// Currently set in global options
		Ja3      Ja3
		ProxyURL string
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	ListenConfig struct {
		// TODO: add tls options when listening
		// Currently set in global options

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

func newConfig() *Config {
	return &Config{
		ConnectTimeout: DefaultTimeout,
		ReadTimeout:    DefaultTimeout,
		WriteTimeout:   DefaultTimeout,
	}
}

// Addrs to use for transport
func Addrs(addrs ...string) Option {
	return func(o *Config) {
		o.Addrs = addrs
	}
}

// Codec sets the codec used for encoding where the transport
// does not support message headers
func Codec(c codec.Marshaler) Option {
	return func(o *Config) {
		o.Codec = c
	}
}

// Timeout sets the timeout for Send/Recv execution
func Timeout(t time.Duration) Option {
	return func(o *Config) {
		o.ReadTimeout = t
		o.WriteTimeout = t
		o.ConnectTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func ReadTimeout(t time.Duration) Option {
	return func(o *Config) {
		o.ReadTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func WriteTimeout(t time.Duration) Option {
	return func(o *Config) {
		o.WriteTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func ConnectTimeout(t time.Duration) Option {
	return func(o *Config) {
		o.ConnectTimeout = t
	}
}

// Use secure communication. If TLSConfig is not specified we
// use InsecureSkipVerify and generate a self signed cert
func Secure(b bool) Option {
	return func(o *Config) {
		o.Secure = b
	}
}

// TLSConfig to be used for the transport.
func TLSConfig(t *tls.Config) Option {
	return func(o *Config) {
		o.TLSConfig = t
	}
}

// Indicates whether this is a streaming connection
func WithStream() DialOption {
	return func(o *DialConfig) {
		o.Stream = true
	}
}

func WithJa3(ja3, userAgent string) DialOption {
	return func(o *DialConfig) {
		o.Ja3.Ja3 = ja3
		o.Ja3.UserAgent = userAgent
	}
}

func WithProxyURL(proxyURL string) DialOption {
	return func(o *DialConfig) {
		o.ProxyURL = proxyURL
	}
}
