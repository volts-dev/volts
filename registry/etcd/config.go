package etcd

import (
	"context"

	"github.com/volts-dev/volts/registry"
	"go.uber.org/zap"
)

type authKey struct{}

type logConfigKey struct{}

type authCreds struct {
	Username string
	Password string
}

// Auth allows you to specify username/password
func Auth(username, password string) registry.Option {
	return func(cfg *registry.Config) {
		if cfg.Context == nil {
			cfg.Context = context.Background()
		}
		cfg.Context = context.WithValue(cfg.Context, authKey{}, &authCreds{Username: username, Password: password})
	}
}

// LogConfig allows you to set etcd log config
func LogConfig(config *zap.Config) registry.Option {
	return func(cfg *registry.Config) {
		if cfg.Context == nil {
			cfg.Context = context.Background()
		}
		cfg.Context = context.WithValue(cfg.Context, logConfigKey{}, config)
	}
}
