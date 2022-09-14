package config

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/logger"
)

var log = logger.New("Config")
var fmt = newFormat() // 配置主要文件格式读写实现
var cfgs sync.Map

var (
	// App settings.
	AppVer        string
	AppName       string
	AppUrl        string
	AppSubUrl     string
	AppPath       string
	AppFilePath   string
	AppDir        string
	DefaultPrefix = "volts"
	defaultConfig = New()
)

type (
	Option func(*Config)

	Config struct {
		fmt      *format
		Mode     ModeType
		Prefix   string `json:"Prefix"`
		FileName string
		//AppFilePath string
		//AppPath     string
		//AppDir      string
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
		fmt:    fmt,
		Mode:   MODE_NORMAL,
		Prefix: DefaultPrefix,
		//AppFilePath: AppFilePath,
		//AppPath:     AppPath,
		//AppDir:      AppDir,
	}
	cfg.Init(opts...)

	// 如果无文件则创建新的
	if !utils.FileExists(cfg.FileName) {
		cfg.fmt.v.WriteConfig()
	}

	// 取缓存
	if c, ok := cfgs.Load(cfg.Prefix); ok {
		return c.(*Config)
	}

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
	if self.FileName != "" {
		self.fmt.v.SetConfigFile(filepath.Join(AppPath, self.FileName))
	} else {
		self.fmt.v.SetConfigFile(filepath.Join(AppPath, CONFIG_FILE_NAME))
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

	if self.FileName != "" {
		self.fmt.v.SetConfigFile(filepath.Join(AppPath, self.FileName))
	} else {
		self.fmt.v.SetConfigFile(filepath.Join(AppPath, CONFIG_FILE_NAME))
	}

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
