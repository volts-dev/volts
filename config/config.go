package config

import (
	"log"
	"path/filepath"
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
	AppPath       = utils.AppPath()
	AppFilePath   = utils.AppFilePath()
	AppDir        = utils.AppDir()
	AppModuleDir  = "module"
	defaultConfig = New(DEFAULT_PREFIX)
	cfgs          sync.Map
)

type (
	Option func(*Config)

	core struct {
		name     string
		fmt      *format
		models   sync.Map
		FileName string
		Mode     ModeType
	}

	Config struct {
		//name string
		//fmt        *format
		//models     sync.Map
		AutoCreateFile bool `field:"-"`
		//Mode       ModeType
		//FileName   string
		Debug   bool
		core    *core // 配置文件核心 为了支持继承nil指针问题
		changed bool
	}

	IConfig interface {
		String() string // the Prefix name
		Load() error
		Save(immed ...bool) error // immed mean save to file immediately or not.
	}

	iConfig interface {
		checkSelf(c *Config, cfg IConfig) // 检测自己是否被赋值
	}
)

/*
	func init() {
		AppFilePath = utils.AppFilePath()
		AppPath = utils.AppPath()
		AppDir = utils.AppDir()
	}
*/
func Default() *Config {
	return defaultConfig
}

func Init(opts ...Option) {
	defaultConfig.Init(opts...)
}

func Register(cfg IConfig) {
	defaultConfig.Register(cfg)
}

func Unregister(cfg IConfig) {
	defaultConfig.Unregister(cfg)
}

func Load(fileName ...string) error {
	return defaultConfig.LoadFromFile(fileName...)
}

func New(name string, opts ...Option) *Config {
	// 取缓存
	var c *core
	if c, ok := cfgs.Load(name); ok {
		c = c.(*core)
	} else {
		c = &core{
			fmt:      newFormat(), // 配置主要文件格式读写实现,
			FileName: CONFIG_FILE_NAME,
			Mode:     MODE_NORMAL,
			name:     name,
		}
		cfgs.Store(name, c)
	}

	cfg := &Config{
		core:           c,
		AutoCreateFile: true,
		changed:        false,
	}
	// 初始化程序硬配置
	cfg.Init(opts...)
	// 加载文件配置
	cfg.LoadFromFile()

	return cfg
}

func (self *Config) Core() *core {
	if self.core == nil {
		c, ok := cfgs.Load(DEFAULT_PREFIX)
		if ok {
			self.core = c.(*core)
		}
	}

	return self.core
}

// 注册添加其他配置
func (self *Config) Register(cfg IConfig) {
	cfgStr := cfg.String()
	if cfgStr == "" {
		return
	}

	core := self.Core()

	if !core.fmt.v.InConfig(cfgStr) {
		if cfgStr == "registry." {
			log.Println(cfgStr)
		}

		// 保存配置到core
		err := cfg.Save()
		if err != nil {
			log.Fatal(err)
		}

		// 保存配置到文件
		err = self.SaveToFile()
		if err != nil {
			log.Fatal(err)
		}
	}

	// 直接覆盖
	core.models.Store(cfgStr, cfg)

	// 加载配置
	if err := cfg.Load(); err != nil {
		log.Fatalf("load %v config failed!", cfgStr)
	}
}

func (self *Config) Unregister(cfg IConfig) {
	self.Core().models.Delete(cfg.String())
}

// config: the config struct with binding the options
func (self *Config) Init(opts ...Option) {
	self.Core()

	for _, opt := range opts {
		opt(self)
	}
}

func (self *Config) Reload() {
	// 重新加载配置
	self.Core().models.Range(func(key any, value any) bool {
		v := value.(IConfig)
		err := v.Load()
		if err != nil {
			log.Fatalf("reload %v config failed!", key)
		}
		return true
	})
}

// 检测配置路径是否在Config里出现了
func (self *Config) InConfig(path string) bool {
	return self.Core().fmt.v.InConfig(path)
}

func (self *Config) LoadFromFile(fileName ...string) error {
	core := self.Core()

	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}
	filePath := filepath.Join(AppPath, core.FileName)
	core.fmt.v.SetConfigFile(filePath)

	fileExists := utils.FileExists(filePath) && self.AutoCreateFile

	// Find and read the config file
	// Handle errors reading the config file
	if fileExists {
		err := core.fmt.v.ReadInConfig()
		if err != nil {
			return err
		}
	}

	// 校正
	// 保存默认配置
	core.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			if key != v.String() {
				// 配置路径被更改过
				core.models.Delete(key)
				core.models.Store(v.String(), v)
			}
		}
		return true
	})

	changed := 0
	core.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			if !fileExists || !core.fmt.v.InConfig(v.String()) {
				// 没有配置文件则保存默认配置
				if err := v.Save(false); err != nil {
					log.Fatalf("save %v config failed!", v.String())
					return false
				}
				changed++
			} else {
				if err := v.Load(); err != nil {
					log.Fatalf("save %v config failed!", key)
				}
			}

		}
		return true
	})

	if changed > 0 {
		// 保存新的配置
		return core.fmt.v.WriteConfig()
	}

	return nil
}

func (self *Config) LoadToModel(model IConfig) error {
	core := self.Core()

	// 如果配置不存于V
	if !core.fmt.v.InConfig(model.String()) {
		err := model.Save()
		if err != nil {
			return err
		}
	} else {
		err := core.fmt.UnmarshalKey(model.String(), model)
		if err != nil {
			return err
		}
	}

	// 保存但不覆盖注册的Model
	if _, has := core.models.Load(model.String()); !has {
		self.Register(model)
	}
	return nil
}

func (self *Config) SaveToFile(opts ...Option) error {
	core := self.Core()

	for _, opt := range opts {
		opt(self)
	}

	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}

	core.fmt.v.SetConfigFile(filepath.Join(AppPath, core.FileName))
	return core.fmt.v.WriteConfig()
}

// 从数据类型加载数据
// 只支持map[string]any 和struct
func (self *Config) SaveFromModel(model IConfig, immed ...bool) error {
	opts := utils.Struct2ItfMap(model)

	for k, v := range opts {
		// 过滤自己
		if strings.ToLower("config") == strings.ToLower(k) {
			continue
		}

		self.SetValue(strings.Join([]string{model.String(), k}, "."), v)
	}

	if len(immed) > 0 {
		// 立即保存
		on := immed[0]
		if on {
			return self.SaveToFile()
		}
	}

	return nil
}

func (self *Config) GetBool(field string, defaultValue bool) bool {
	return self.Core().fmt.GetBool(field, defaultValue)
}

// GetStringValue from default namespace
func (self *Config) GetString(field, defaultValue string) string {
	return self.Core().fmt.GetString(field, defaultValue)
}

// GetIntValue from default namespace
func (self *Config) GetInt(field string, defaultValue int) int {
	return self.Core().fmt.GetInt(field, defaultValue)
}

func (self *Config) GetInt32(field string, defaultValue int32) int32 {
	return self.Core().fmt.GetInt32(field, defaultValue)
}

func (self *Config) GetInt64(field string, defaultValue int64) int64 {
	return self.Core().fmt.GetInt64(field, defaultValue)
}

func (self *Config) GetIntSlice(field string, defaultValue []int) []int {
	return self.Core().fmt.GetIntSlice(field, defaultValue)
}

func (self *Config) GetTime(field string, defaultValue time.Time) time.Time {
	return self.Core().fmt.GetTime(field, defaultValue)
}

func (self *Config) GetDuration(field string, defaultValue time.Duration) time.Duration {
	return self.Core().fmt.GetDuration(field, defaultValue)
}

func (self *Config) GetFloat64(field string, defaultValue float64) float64 {
	return self.Core().fmt.GetFloat64(field, defaultValue)
}

func (self *Config) SetValue(field string, value interface{}) {
	self.Core().fmt.SetValue(field, value)
}

func (self *Config) xUnmarshal(rawVal interface{}) error {
	return self.Core().fmt.Unmarshal(rawVal)
}

// 反序列字段映射到数据类型
func (self *Config) xUnmarshalField(field string, rawVal interface{}) error {
	return self.Core().fmt.UnmarshalKey(field, rawVal)
}
