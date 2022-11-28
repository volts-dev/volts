package logger

import "github.com/volts-dev/volts/config"

type (
	Option func(*Config)

	Config struct {
		*config.Config `field:"-"`
		Level          Level
		Prefix         string
		WriterName     string
	}
)

var (
	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Level:  LevelDebug,
		Prefix: "LOG",
	}
	config.Default().Register(cfg)
	cfg.Init(opts...)
	return cfg
}

// init options
func (self *Config) String() string {
	return "logger." + self.Prefix
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}
func (self *Config) Load() error {
	return self.LoadToModel(self)
}

func (self *Config) Save(immed ...bool) error {
	return self.SaveFromModel(self, immed...)
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

func WithWrite(name string) Option {
	return func(cfg *Config) {
		cfg.WriterName = name
	}
}

func WithPrefix(name string) Option {
	return func(cfg *Config) {
		cfg.Prefix = name
	}
}

func WithLevel(lvl Level) Option {
	return func(cfg *Config) {
		cfg.Level = lvl
	}
}
