package config

import "github.com/volts-dev/utils"

func WithPrefix(name string) Option {
	return func(cfg *Config) {
		cfg.Prefix = name
	}
}

func WithFileName(fileName string) Option {
	return func(cfg *Config) {
		cfg.FileName = fileName
	}
}

func WithConfig(model any) Option {
	return func(cfg *Config) {
		opts := utils.Struct2ItfMap(model)
		for k, v := range opts {
			cfg.SetValue(k, v)
		}
	}
}
