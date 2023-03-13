package config

import (
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cast"
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

	Config struct {
		fmt        *format
		models     sync.Map
		CreateFile bool `field:"-"`
		Mode       ModeType
		FileName   string
		Debug      bool
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
	return defaultConfig.Load(fileName...)
}

func New(prefix string, opts ...Option) *Config {
	// 取缓存
	var cfg *Config
	if c, ok := cfgs.Load(prefix); ok {
		cfg = c.(*Config)
	} else {
		cfg = &Config{
			fmt:        newFormat(), // 配置主要文件格式读写实现,
			CreateFile: true,
			Mode:       MODE_NORMAL,
			//Prefix:     prefix,
			FileName: CONFIG_FILE_NAME,
		}
	}

	// 初始化程序硬配置
	cfg.Init(opts...)
	// 加载文件配置
	cfg.Load()

	//cfgs.Store(cfg.Prefix, cfg)
	return cfg
}

func (self *Config) checkSelf(c *Config, cfg IConfig) {
	if self == nil {
		val := reflect.ValueOf(cfg).Elem()
		val.FieldByName("Config").Set(reflect.ValueOf(c))
	}
}

// 注册添加其他配置
func (self *Config) Register(cfg IConfig) {
	if c, ok := cfg.(iConfig); ok {
		c.checkSelf(self, cfg)
	}

	self.models.Store(cfg.String(), cfg)
}

func (self *Config) Unregister(cfg IConfig) {
	self.models.Delete(cfg.String())
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

func (self *Config) Reload() {
	// 重新加载配置
	defaultConfig.models.Range(func(key any, value any) bool {
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
	return defaultConfig.fmt.v.InConfig(path)
}

// 校正
func (self *Config) Trim(fileName ...string) {
	// 保存默认配置
	self.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			if key != v.String() {
				// 配置路径被更改过
				self.models.Delete(key)
				self.models.Store(v.String(), v)
			}
		}
		return true
	})
}

func (self *Config) Load(fileName ...string) error {
	if self.FileName == "" {
		self.FileName = CONFIG_FILE_NAME
	}
	filePath := filepath.Join(AppPath, self.FileName)
	self.fmt.v.SetConfigFile(filePath)

	self.Trim()

	if self.CreateFile && !utils.FileExists(filePath) {
		// 保存默认配置
		self.models.Range(func(key any, value any) bool {
			if v, ok := value.(IConfig); ok {
				if err := v.Save(false); err != nil {
					log.Fatalf("reload %v config failed!", v.String())
					return false
				}
			}
			return true
		})
		return self.fmt.v.WriteConfig()
	}
	// Find and read the config file
	// Handle errors reading the config file
	err := self.fmt.v.ReadInConfig()
	if err != nil {
		return err
	}

	changed := 0
	self.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			if !self.fmt.v.InConfig(v.String()) {
				if err := v.Save(false); err != nil {
					log.Fatalf("save %v config failed!", key)
					return false
				}
				changed++
			} else if err := v.Load(); err != nil {
				log.Fatalf("reload %v config failed!", key)
			}
		}
		return true
	})
	if changed > 0 {
		// 保存新的配置
		return self.fmt.v.WriteConfig()
	}

	return nil
}

func (self *Config) LoadToModel(model IConfig) error {
	err := defaultConfig.fmt.UnmarshalKey(model.String(), model)
	if err != nil {
		return err
	}

	// 保存但不覆盖注册的Model
	if _, ok := defaultConfig.models.Load(model.String()); !ok {
		defaultConfig.models.Store(model.String(), model)
	}
	return nil
}

// TODO 支持字段结构
// save settings data from the config model
func (self *Config) ___LoadToModel(model IConfig) error {
	mapper := utils.NewStructMapper(model)
	for _, field := range mapper.Fields() {
		// 过滤自己
		if strings.ToLower("config") == strings.ToLower(field.Name()) {
			continue
		}

		// 字段赋值
		key := strings.Join([]string{model.String(), utils.SnakeCasedName(field.Name())}, ".")
		if val := self.fmt.v.Get(key); val != nil {
			log.Println(field.Value())
			// 初始化值以供接下来转换
			if field.Value() == nil {
				if err := field.Zero(); err != nil {
					log.Fatal(err)
				}
			}

			// 转换类型
			switch field.Kind() {
			case reflect.Bool:
				val = cast.ToBool(val)
			case reflect.String:
				val = cast.ToString(val)
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				field.SetInt(cast.ToInt64(val))
				continue
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				field.SetUint(cast.ToUint64(val))
				continue
			case reflect.Float64, reflect.Float32:
				val = cast.ToFloat64(val)
			default:
				switch field.Value().(type) {
				case time.Time:
					val = cast.ToTime(val)
				case []string:
					val = cast.ToStringSlice(val)
				case []int:
					val = cast.ToIntSlice(val)
				}
			}

			err := field.Set(val)
			if err != nil {
				log.Fatalf("load to model failed! Key:%s Value:%v Err:%s", key, self.fmt.v.Get(key), err.Error())
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

	if self.FileName == "" {
		self.FileName = CONFIG_FILE_NAME
	}

	self.fmt.v.SetConfigFile(filepath.Join(AppPath, self.FileName))
	return self.fmt.v.WriteConfig()
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
			return self.Save()
		}
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
