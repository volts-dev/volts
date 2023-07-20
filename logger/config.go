package logger

import (
	"strings"

	"github.com/volts-dev/volts/config"
)

type (
	Option func(*Config)

	Config struct {
		config.Config `field:"-"`
		Name          string `field:"-"` // config name/path in config file
		PrefixName    string `field:"-"` // config prefix name
		Level         Level
		//Prefix         string
		WriterName string
	}
)

var (
	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		//Config: config.Default(),
		Level: LevelDebug,
		Name:  "logger",
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.Name, self.PrefixName}, ".")
	}
	return self.Name
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

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)
		// 重新加载
		//cfg.Load()
	}
}

func WithLevel(lvl Level) Option {
	return func(cfg *Config) {
		cfg.Level = lvl
	}
}
