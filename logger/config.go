package logger

import "sync"

type (
	Option func(*TConfig)

	TConfig struct {
		Level     Level  `json:"Level"`
		Prefix    string `json:"Prefix"`
		WriteName string
	}
)

var (
	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
	defualt  = New("LOG")
	loggers  sync.Map
)

func newConfig(opts ...Option) *TConfig {
	config := &TConfig{
		Level:  LevelDebug,
		Prefix: "LOG",
	}
	config.Init(opts...)
	return config
}

// init options
func (self *TConfig) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func WithWrite(name string) Option {
	return func(cfg *TConfig) {
		cfg.WriteName = name
	}
}

func WithPrefix(name string) Option {
	return func(cfg *TConfig) {
		cfg.Prefix = name
	}
}

func WithLevel(lvl Level) Option {
	return func(cfg *TConfig) {
		cfg.Level = lvl
	}
}
