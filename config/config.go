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
	AppPath       = utils.AppPath()
	AppFilePath   = utils.AppFilePath()
	AppDir        = utils.AppDir()
	AppModuleDir  = "module"
	defaultConfig = New(DEFAULT_PREFIX)
	cfgs          sync.Map
)

type (
	Option func(*Config)

	// core结构体定义了处理格式化数据的核心功能。
	// 它包含了格式化器的名称、格式化选项、模型缓存、文件名和操作模式。
	// 这些字段共同作用，以支持不同格式的数据处理和缓存机制。
	core struct {
		// name是格式化器的唯一标识符。
		name string

		// fmt是指向format类型的指针，用于根据特定格式处理数据。
		fmt *format

		// models是一个同步Map，用于存储和快速访问已经加载的模型。
		// 这对于性能优化和避免重复加载相同模型尤为重要。
		models sync.Map

		// FileName指定了当前处理的文件名。
		// 这对于跟踪和记录处理过程中的文件信息非常有用。
		FileName string

		// Mode定义了操作模式，决定了如何处理和格式化数据。
		// 操作模式通过ModeType类型定义，可能包括不同的处理策略和选项。
		Mode ModeType
	}

	Config struct {
		Debug          bool
		AutoCreateFile bool    `field:"-"`
		core           *core   `field:"-"` // 配置文件核心 为了支持继承nil指针问题
		changed        bool    `field:"-"`
		model          IConfig `field:"-"`
	}

	IConfig interface {
		String() string // the Prefix name
		Load() error
		Save(immed ...bool) error // immed mean save to file immediately or not.
	}

	iConfig interface {
		checkSelf(cfg IConfig) // 检测自己是否被赋值
	}
)

// Default 返回系统初始化时的基础配置单例 (singleton)。
func Default() *Config {
	return defaultConfig
}

// Init 应用选项至默认单例配置，可用于更改应用路径模式、文件驱动等。
func Init(opts ...Option) {
	defaultConfig.Init(opts...)
}

// Register 使用默认单例载入额外的各个业务模块配置。
// 并执行默认合并去重逻辑。
func Register(cfg IConfig) {
	defaultConfig.Register(cfg)
}

// Unregister 从内存和驱动中移除不再需要的业务配置模版。
func Unregister(cfg IConfig) {
	defaultConfig.Unregister(cfg)
}

// Load 指定重载配置文件入口并立即刷新至缓存和底层组件模型中。
func Load(fileName ...string) error {
	return defaultConfig.LoadFromFile(fileName...)
}

// New 创建并返回一个新的特定命名配置实体。
// 通过加载同名空间配置以及支持绑定一系列 Option 以控制加载及缓存策略。
func New(name string, opts ...Option) *Config {
	// 复用内存中旧有的 core 实例，若无则新建
	var c *core
	if val, ok := cfgs.Load(name); ok {
		c = val.(*core)
	} else {
		c = &core{
			fmt:      newFormat(), // 配置底层基于 Viper 等库实现
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

// Register 注册一个配置模型（实现 IConfig 接口）。
// 注册时如果指定的配置文件中尚未包含该部分配置，将使用模型默认值保存并持久化到文件（受 AutoCreateFile 控制）。
func (self *Config) Register(cfg IConfig) {
	if c, ok := cfg.(iConfig); ok {
		c.checkSelf(cfg)
	}

	cfgStr := cfg.String()
	if cfgStr == "" {
		log.Fatal("config name is empty")
		return
	}

	core := self.Core()

	// 缓存到 core
	core.models.Store(cfgStr, cfg)

	if !self.InConfig(cfgStr) {
		// 尚未存储该配置：使用默认值写入到内存（core）
		if err := cfg.Save(); err != nil {
			log.Fatal(err)
		}
	}

	// 从 core 中装载真实的配置到模型实体
	if err := cfg.Load(); err != nil {
		log.Fatalf("load %v config failed!", cfgStr)
	}
}

// Unregister 移除配置模型并保存持久化状态。
func (self *Config) Unregister(cfg IConfig) {
	cfgStr := cfg.String()
	self.Core().models.Delete(cfgStr)

	if self.InConfig(cfgStr) {
		//self.Core().fmt.Unset(cfgStr)
		self.Core().fmt.v.Set(cfgStr, nil)

		// 保存配置到文件
		err := self.SaveToFile()
		if err != nil {
			log.Fatal(err)
		}
	}

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

// LoadFromFile 尝试从文件系统加载配置。
// 并会将已注册的配置模型与加载的数据文件进行校对。
// 缺失的配置模型（未存在于文件）将被补全并根据 AutoCreateFile 开关写回。
func (self *Config) LoadFromFile(fileName ...string) error {
	core := self.Core()

	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}
	filePath := filepath.Join(AppPath, core.FileName)
	core.fmt.v.SetConfigFile(filePath)

	// 如果文件存在，先载入文件所有内容
	fileExists := utils.FileExists(filePath)
	if fileExists {
		if err := core.fmt.v.ReadInConfig(); err != nil {
			return err
		}
	}

	// 校对已在 core.models 里的配置模型。如果名字修改过，重新进行绑定。
	core.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			if key != v.String() {
				core.models.Delete(key)
				core.models.Store(v.String(), v)
			}
		}
		return true
	})

	changed := false
	core.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			// 如果没有文件提供原始配置，或者该节配置不在文件里
			if !fileExists || !core.fmt.v.InConfig(v.String()) {
				if err := v.Save(false); err != nil {
					log.Fatalf("save %v config failed!", v.String())
					return false
				}
				changed = true
			} else {
				// 配置存在，立即反序列化加载到对应的内存模型实例
				if err := v.Load(); err != nil {
					log.Fatalf("save %v config failed!", key)
				}
			}
		}
		return true
	})

	// 有变更且允许创建及修改文件，则刷回磁盘
	if changed && self.AutoCreateFile {
		return self.SaveToFile()
	}

	return nil
}

func (self *Config) checkSelf(cfg IConfig) {
	if self.model == nil {
		self.model = cfg
	}
}

func (self *Config) Load() error {
	if self.model != nil {
		return self.LoadToModel(self.model)
	}
	return nil
}

func (self *Config) Save(immed ...bool) error {
	if self.model != nil {
		return self.SaveFromModel(self.model, immed...)
	}
	return nil
}

// LoadToModel 提供将 viper 内存缓存序列化装载到指定配置模型的接口。
func (self *Config) LoadToModel(model IConfig) error {
	core := self.Core()

	// 若该模型不在配置树的范围内（初次使用或未注册）
	if !core.fmt.v.InConfig(model.String()) {
		err := model.Save() // 持久化其默认值到核里
		if err != nil {
			return err
		}
	} else {
		// 解码装载
		err := core.fmt.UnmarshalKey(model.String(), model)
		if err != nil {
			return err
		}
	}

	// 保存时如果尚未注册该模型则连带注册
	if _, has := core.models.Load(model.String()); !has {
		self.Register(model)
	}
	return nil
}

// SaveToFile 原地或切换后保存 core 缓存层配置到文件驱动。
func (self *Config) SaveToFile(opts ...Option) error {
	core := self.Core()

	for _, opt := range opts {
		opt(self)
	}

	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}

	core.fmt.RWMutex.Lock()
	defer core.fmt.RWMutex.Unlock()
	core.fmt.v.SetConfigFile(filepath.Join(AppPath, core.FileName))
	return core.fmt.v.WriteConfig()
}

// SaveFromModel 提取模型对象的所有可见属性并转储至配置底层的 KV 缓存中。
// 当前支持解析的类型为 struct 以及 map[string]any。
func (self *Config) SaveFromModel(model IConfig, immed ...bool) error {
	opts := utils.Struct2ItfMap(model)

	for k, v := range opts {
		// 避免模型相互嵌套解析 config 引发死循环
		if strings.EqualFold("config", k) {
			continue
		}

		// 忽略零值或空值，防止其覆盖现有合法配置从而提高配置干净度
		/*if isZeroValue(v) {
			 continue
		}*/

		// 针对时间间隔 (time.Duration) 将其扁平化为毫无精度的毫秒级以增强向外输出配置的可读性
		if d, ok := v.(time.Duration); ok {
			v = d.Milliseconds()
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

// isZeroValue 判断值是否为零值/空值
func isZeroValue(v interface{}) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	}

	return false
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
