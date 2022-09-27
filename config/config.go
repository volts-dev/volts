package config

import (
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/logger"
)

var (
	// App settings.
	AppVer         string
	AppName        string
	AppUrl         string
	AppSubUrl      string
	AppPath        string
	AppFilePath    string
	AppDir         string
	cfgs           sync.Map
	log            = logger.New("Config")
	fmt            = newFormat() // 配置主要文件格式读写实现
	DEFAULT_PREFIX = "volts"
	defaultConfig  = New()
)

type (
	Option func(*Config)

	Config struct {
		fmt      *format
		Mode     ModeType
		Prefix   string `json:"prefix"`
		FileName string `json:"file_name"`
	}

	IConfig interface {
		Init(...Option)
		Load() error
		Save() error
	}
)

func init() {
	AppFilePath = utils.AppFilePath()
	AppPath = utils.AppPath()
	AppDir = utils.AppDir()
}

func Default() *Config {
	return defaultConfig
}

func New(opts ...Option) *Config {
	cfg := &Config{
		fmt:      fmt,
		Mode:     MODE_NORMAL,
		Prefix:   DEFAULT_PREFIX,
		FileName: CONFIG_FILE_NAME,
	}
	cfg.Init(opts...)

	// 取缓存
	if c, ok := cfgs.Load(cfg.Prefix); ok {
		cfg := c.(*Config)
		cfg.Init(opts...)
		return cfg
	}

	// 监视文件
	cfg.fmt.v.WatchConfig()
	cfg.fmt.v.OnConfigChange(func(e fsnotify.Event) {
		log.Info("config file changed:", e.Name)
	})

	cfgs.Store(cfg.Prefix, cfg)
	return cfg
}

// config: the config struct with binding the options
func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}
}

// default is CONFIG_FILE_NAME = "config.json"
func (self *Config) Load(fileName ...string) error {
	if self.FileName == "" {
		self.FileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
	}
	self.fmt.v.SetConfigFile(self.FileName)
	// 如果无文件则创建新的
	if !utils.FileExists(self.FileName) {
		return self.fmt.v.WriteConfig()
	}

	err := self.fmt.v.ReadInConfig() // Find and read the config file
	if err != nil {                  // Handle errors reading the config file
		return err
	}

	return nil
}

func (self *Config) Save(opts ...Option) error {
	for _, opt := range opts {
		opt(self)
	}

	if self.FileName == "" {
		self.FileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
	}

	self.fmt.v.SetConfigFile(self.FileName)
	if err := self.fmt.v.WriteConfig(); err != nil {
		return err
	}

	return nil
}

func (self *Config) GetBool(field string, defaultValue bool) bool {
	return self.fmt.GetBool(field, defaultValue)
}

// GetStringValue from default namespace
func (self *Config) GetString(field, defaultValue string) string {
	return self.fmt.GetString(field, defaultValue)
}

// GetIntValue from default namespace
func (self *Config) GetInt(field string, defaultValue int) int {
	return self.fmt.GetInt(field, defaultValue)
}

func (self *Config) GetInt32(field string, defaultValue int32) int32 {
	return self.fmt.GetInt32(field, defaultValue)
}

func (self *Config) GetInt64(field string, defaultValue int64) int64 {
	return self.fmt.GetInt64(field, defaultValue)
}

func (self *Config) GetIntSlice(field string, defaultValue []int) []int {
	return self.fmt.GetIntSlice(field, defaultValue)
}

func (self *Config) GetTime(field string, defaultValue time.Time) time.Time {
	return self.fmt.GetTime(field, defaultValue)
}

func (self *Config) GetDuration(field string, defaultValue time.Duration) time.Duration {
	return self.fmt.GetDuration(field, defaultValue)
}

func (self *Config) GetFloat64(field string, defaultValue float64) float64 {
	return self.fmt.GetFloat64(field, defaultValue)
}

func (self *Config) SetValue(field string, value interface{}) {
	self.fmt.SetValue(field, value)
}

func (self *Config) Unmarshal(rawVal interface{}) error {
	return self.fmt.Unmarshal(rawVal)
}

// 反序列字段映射到数据类型
func (self *Config) UnmarshalField(field string, rawVal interface{}) error {
	return self.fmt.UnmarshalKey(field, rawVal)
}
