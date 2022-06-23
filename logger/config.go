package logger

import "sync"

type (
	Option func(*TConfig)

	TConfig struct {
		Level  Level  `json:"Level"`
		Prefix string `json:"Prefix"`
	}
)

var (
	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
	defualt  = newLogger()
	all      sync.Map
)

// the output like: 2035/01/01 00:00:00 [Prefix][Action] message...
func New(Prefix string, opts ...Option) *TLogger {
	opts = append(opts, WithPrefix(Prefix)) //
	log := newLogger(opts...)
	all.Store(Prefix, log)
	return log
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
