package logger

import "github.com/volts-dev/volts/config"

type (
	Option func(*Config)

	Config struct {
		*config.Config `field:"-"`
		Level          Level
		Prefix         string
		WriteName      string
	}
)

var (
	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
	defualt  = New("volts")
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Level:  LevelDebug,
		Prefix: "LOG",
	}
	cfg.Init(opts...)
	config.Default().Register(cfg)
	return cfg
}

// init options
func (self *Config) String() string {
	return "logger"
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

func (self *Config) Save() error {
	return self.Config.Save(config.WithConfig(self))
}

func WithWrite(name string) Option {
	return func(cfg *Config) {
		cfg.WriteName = name
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
