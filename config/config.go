package config

import (
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/utils"
)

var (
	// App settings.
	AppVer        string
	AppName       string
	AppUrl        string
	AppSubUrl     string
	AppPath       string
	AppFilePath   string
	AppDir        string
	cfgs          sync.Map
	defaultConfig = New(DEFAULT_PREFIX)
)

type (
	Option func(*Config)

	Config struct {
		fmt      *format
		models   sync.Map
		Mode     ModeType
		Prefix   string
		fileName string
	}

	IConfig interface {
		String() string // the Prefix name
		Load() error
		Save() error
	}

	iConfig interface {
		checkSelf(c *Config, cfg IConfig) // 检测自己是否被赋值
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

func New(prefix string, opts ...Option) *Config {
	// 取缓存
	if c, ok := cfgs.Load(prefix); ok {
		cfg := c.(*Config)
		cfg.Init(opts...)
		return cfg
	}

	cfg := &Config{
		fmt:      newFormat(), // 配置主要文件格式读写实现,
		Mode:     MODE_NORMAL,
		Prefix:   prefix,
		fileName: CONFIG_FILE_NAME,
	}
	cfg.Init(opts...)

	cfgs.Store(cfg.Prefix, cfg)
	return cfg
}

func (self *Config) checkSelf(c *Config, cfg IConfig) {
	if self == nil {
		val := reflect.ValueOf(cfg).Elem()
		val.FieldByName("Config").Set(reflect.ValueOf(c))
	}
}

// 添加其他配置
func (self *Config) Register(cfg IConfig) {
	if c, ok := cfg.(iConfig); ok {
		c.checkSelf(self, cfg)
	}

	self.models.Store(cfg.String(), cfg)
}

// config: the config struct with binding the options
func (self *Config) Init(opts ...Option) {
	if self == nil {
		*self = *New(DEFAULT_PREFIX)
	}

	for _, opt := range opts {
		opt(self)
	}
}

// default is CONFIG_FILE_NAME = "config.json"
func (self *Config) Load(fileName ...string) error {
	if self.fileName == "" {
		self.fileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
	}

	self.fmt.v.SetConfigFile(filepath.Join(AppPath, self.fileName))
	// Find and read the config file
	// Handle errors reading the config file
	return self.fmt.v.ReadInConfig()
}

// save settings data from the config model
func (self *Config) LoadToModel(model IConfig) error {
	mapper := utils.NewStructMapper(model)
	for _, field := range mapper.Fields() {
		// 过滤自己
		if strings.ToLower("config") == strings.ToLower(field.Name()) {
			continue
		}

		// 字段赋值
		key := strings.Join([]string{model.String(), utils.SnakeCasedName(field.Name())}, ".")
		if val := self.fmt.v.Get(key); val != nil {
			err := field.Set(val)
			if err != nil {
				log.Fatalf("load to model failed! %s %v", key, self.fmt.v.Get(key))
			}
		}

	}

	// 保存但不覆盖注册的Model
	if _, ok := self.models.Load(model.String()); !ok {
		self.models.Store(model.String(), model)
	}
	return nil
}

func (self *Config) Save(opts ...Option) error {
	for _, opt := range opts {
		opt(self)
	}

	if self.fileName == "" {
		self.fileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
	}

	self.fmt.v.SetConfigFile(filepath.Join(AppPath, self.fileName))
	return self.fmt.v.WriteConfig()
}

// 从数据类型加载数据
// 只支持map[string]any 和struct
func (self *Config) SaveFromModel(model IConfig) error {
	opts := utils.Struct2ItfMap(model)

	for k, v := range opts {
		// 过滤自己
		if strings.ToLower("config") == strings.ToLower(k) {
			continue
		}

		self.SetValue(strings.Join([]string{model.String(), k}, "."), v)
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

func (self *Config) xUnmarshal(rawVal interface{}) error {
	return self.fmt.Unmarshal(rawVal)
}

// 反序列字段映射到数据类型
func (self *Config) xUnmarshalField(field string, rawVal interface{}) error {
	return self.fmt.UnmarshalKey(field, rawVal)
}
