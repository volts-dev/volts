package logger

import (
	"github.com/volts-dev/volts/config"
)

type (
	Option func(*Config)

	// Config 是一个日志器的配置，配置节 logger.<name>（name 即 New(prefix) 的 prefix）。
	Config struct {
		Name  string `field:"-"`
		Debug bool   `field:"-"`
		Level Level
	}
)

var (
	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Level: LevelDebug,
		Name:  "logger",
	}
	cfg.Init(opts...)
	config.Bind("logger."+cfg.Name, cfg)
	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

// WithName 设定日志器名，也是配置路径 logger.<name>。
func WithName(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
	}
}

func WithLevel(lvl Level) Option {
	return func(cfg *Config) {
		cfg.Level = lvl
	}
}
