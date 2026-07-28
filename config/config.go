package config

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

		// errMu/errs 收集装载期错误。
		//
		// 这里刻意不再 log.Fatal：配置层是库代码，一个坏 key 直接 os.Exit(1)
		// 让调用方毫无补救余地，而 vectors 会在运行期动态注册租户/模块配置，
		// 那种场景下退进程是不可接受的。错误改为累积，由 Errors() 一次取全。
		errMu sync.Mutex
		errs  []error

		// applyMu 串行化**配置层内部**所有「把配置写进模型」的路径：Register、
		// LoadFromFile、Reload。
		//
		// 这三条路径分属不同 goroutine —— Reload 跑在 WithWatcher 的文件监听协程上，
		// 另两条跑在调用方线程 —— 却写同一批模型对象，-race 实测可复现（watcher 的
		// Reload 读模型字段 vs LoadFromFile 里 model.Load() 写同一字段）。
		//
		// 这把锁**不解决**业务代码直接读模型字段与热重载写入之间的竞态：那些读点
		// 遍布请求路径（router.Config.Recover 每请求一次…），配置层拦不住，得靠
		// copy-on-write + accessor 改造读侧。但配置层自己制造的那一半没有理由留着。
		applyMu sync.Mutex

		// savingDefaults 标记当前这次 Save 灌的是「代码默认值」而非「运行期设定值」。
		//
		// 两者必须落在配置系统的不同层：默认值优先级要**低于**配置文件，否则启动时
		// 灌进去的默认值会永久压住用户在文件里写的真实配置 —— 用户改了配置没反应，
		// 且毫无提示。
		//
		// 放在 core 而不是 Config 上：core 始终以指针传递，Config 却会被按值复制
		// （如 broker/memory/memory.go 从 *m.config 取字面量），把 atomic.Bool 放
		// Config 里会让 go vet 的 copylocks 对既有代码报警。
		savingDefaults atomic.Bool
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
		checkSelf(cfg IConfig)  // 检测自己是否被赋值
		bindCore(c *core) *core // 把注册方的 core 注入模型内嵌的 Config
	}
)

// saveDefaults 以「灌默认值」的模式执行 model 的 Save。
//
// 走 model.Save() 而不是直接 SaveFromModel，是因为各包普遍覆写了 Save 并在其中做
// 额外动作（见 client/config.go 的 Init→Save、测试里的 testConfig.Save）；绕开覆写
// 会静默改变这些包的行为。
func saveDefaults(c *core, model IConfig) error {
	if c == nil {
		return model.Save()
	}
	c.savingDefaults.Store(true)
	defer c.savingDefaults.Store(false)
	return model.Save()
}

// errorf 累积一条装载期错误。
func (self *core) errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	self.errMu.Lock()
	self.errs = append(self.errs, err)
	self.errMu.Unlock()
	log.Println("config:", err)
	return err
}

// Errors 返回自上次 ClearErrors 以来累积的全部装载期错误。
//
// 刻意做成「聚合后一次取全」而不是「遇错即停」：一份 config.json 动辄数百个 key，
// 报第一个就退出意味着改一次重启一次，一个个撞。
func (self *core) Errors() error {
	self.errMu.Lock()
	defer self.errMu.Unlock()
	if len(self.errs) == 0 {
		return nil
	}
	return errors.Join(self.errs...)
}

func (self *core) ClearErrors() {
	self.errMu.Lock()
	self.errs = nil
	self.errMu.Unlock()
}

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

// Errors 返回默认单例上累积的装载期错误（多条已用 errors.Join 聚合）。
//
// 装载错误不再直接 os.Exit：调用方（通常是 volts.Run 或各服务的 cmd）应在组件
// 装配完毕后查一次，自行决定是拒绝启动还是带着默认值继续跑。
func Errors() error {
	return defaultConfig.Errors()
}

// ClearErrors 清空默认单例上累积的装载期错误。
func ClearErrors() {
	defaultConfig.ClearErrors()
}

// Dump 把默认单例的全部配置（含代码默认值）写成一份可编辑的模板文件。
func Dump(fileName ...string) error {
	return defaultConfig.Dump(fileName...)
}

// Lint 报告默认单例的配置文件里所有未知配置项（拼错的、过时的、没人认领的）。
// 须在全部组件装配完毕后调用。
func Lint() []LintIssue {
	return defaultConfig.Lint()
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

// Detach 让该 Config 挂到一个**与全局隔离**的一次性 core 上。
//
// 用于「只想 apply 一遍 option、把某几个字段解析出来，不想产生任何注册副作用」的
// 临时对象。典型场景是各包的 Use 工厂：它们建一个临时 Config 来求出 Addrs 以便
// 查实例缓存，但 option 里往往含 WithConfigPrefixName —— 那个 option 内部直接
// Register，于是每调一次 Use 就把一个随即被丢弃的临时对象注册进全局 models。
// 命中缓存时更糟：后面没有第二次注册去覆盖它，该配置路径便永久指向一个没人持有的
// 孤儿对象，改它不影响真实例，表现为「配置改了不生效」。
//
// 隔离 core 不进 cfgs 注册表、不读文件，随临时对象一起被回收。
func Detach(cfg *Config) {
	cfg.core = &core{
		fmt:      newFormat(),
		FileName: CONFIG_FILE_NAME,
		Mode:     MODE_NORMAL,
		name:     "detached",
	}
}

// Errors 返回本配置实例所属 core 上累积的装载期错误。
func (self *Config) Errors() error {
	return self.Core().Errors()
}

// ClearErrors 清空本配置实例所属 core 上累积的装载期错误。
func (self *Config) ClearErrors() {
	self.Core().ClearErrors()
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
//
// 若配置文件中尚未包含该节，模型的当前值会作为**默认值**灌进 core（defaults 层，
// 优先级低于配置文件），随后立即把 core 里的实际配置装载回模型。注册不写磁盘。
func (self *Config) Register(cfg IConfig) {
	core := self.Core()

	if c, ok := cfg.(iConfig); ok {
		c.checkSelf(cfg)
		// ★ 把注册方的 core 注入模型内嵌的 Config。
		//
		// 此前这一步不存在：Register 只把模型指针存进 core.models，而模型内嵌的
		// Config.core 始终是 nil，于是模型自己的 Save/Load 走 Core() 时回落到全局
		// DEFAULT_PREFIX 那个 core。后果是 New(name) 建出来的命名配置实体对
		// Register 完全无效 —— 注册两个模型再 SaveToFile()，写出来的是 `{}`，
		// 值全跑进了全局单例。
		c.bindCore(core)
	}

	cfgStr := cfg.String()
	if cfgStr == "" {
		core.errorf("配置模型 %T 的 String() 返回空名称，已跳过注册", cfg)
		return
	}

	// 缓存到 core
	core.models.Store(cfgStr, cfg)

	core.applyMu.Lock()
	defer core.applyMu.Unlock()

	// 把模型当前值作为**默认值**灌进 core（defaults 层）。
	//
	// 无条件灌，不再只在「整节不存在时」才灌：配置文件通常只写少数几项，此前
	// 只要该节存在，模型其余字段的默认值就一个都不进 core —— 于是 GetXxx 查不到
	// 它们，Origin 也说不出它们的来源。defaults 层优先级最低，灌了也不会盖掉
	// 文件里的值。
	if err := saveDefaults(core, cfg); err != nil {
		core.errorf("保存配置 %q 的默认值失败: %w", cfgStr, err)
	}

	// 从 core 中装载真实的配置到模型实体
	if err := cfg.Load(); err != nil {
		core.errorf("装载配置 %q 失败: %w", cfgStr, err)
	}
}

// Unregister 移除配置模型。
//
// 不再顺手写盘：Unregister 的主要调用方是 WithConfigPrefixName / Name 这类改名
// option（见 server/client/router 各自的 config.go），它们在**装配期**每改一次名就
// 触发一次全量落盘。多进程同时启动时并发写同一个 config.json 会把文件写截断，
// 服务器随后静默退出而进程还活着 —— 症状与配置无关，极难回溯到这里。
// 落盘统一由 SaveToFile 显式触发。
func (self *Config) Unregister(cfg IConfig) {
	cfgStr := cfg.String()
	self.Core().models.Delete(cfgStr)

	if self.InConfig(cfgStr) {
		self.Core().fmt.Delete(cfgStr)
	}
}

// config: the config struct with binding the options
func (self *Config) Init(opts ...Option) {
	self.Core()

	for _, opt := range opts {
		opt(self)
	}
}

// StopWatcher 停止 WithWatcher 启动的配置文件监听协程。可安全多次调用。
//
// 该协程默认活到进程结束（与替换掉的 viper.WatchConfig 行为一致），对长驻服务
// 无所谓；提供本方法是给测试与嵌入式场景收口用的 —— 否则 format.StopWatch 因为
// 类型未导出而根本无法从包外触达，等于一段看着有用其实够不着的死代码。
func (self *Config) StopWatcher() {
	if core := self.Core(); core != nil && core.fmt != nil {
		core.fmt.StopWatch()
	}
}

// 检测配置路径是否在Config里出现了
func (self *Config) InConfig(path string) bool {
	return self.Core().fmt.InConfig(path)
}

// LoadFromFile 尝试从文件系统加载配置。
// 并会将已注册的配置模型与加载的数据文件进行校对。
// 未出现在文件里的配置模型，以其自身当前值作为默认值补全（只进内存，不写磁盘）。
func (self *Config) LoadFromFile(fileName ...string) error {
	core := self.Core()

	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}
	filePath := filepath.Join(AppPath, core.FileName)
	core.fmt.SetConfigFile(filePath)

	// 如果文件存在，先载入文件所有内容
	fileExists := utils.FileExists(filePath)
	if fileExists {
		if err := core.fmt.ReadInConfig(); err != nil {
			return err
		}
		// 建立「叶子路径 -> 行号」索引，供 Origin / Lint 定位用。
		// viper 把文件解成 map 之后行号就没了，只能自己再走一遍。
		core.fmt.indexFileLines(filePath)
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
	core.applyMu.Lock()
	core.models.Range(func(key any, value any) bool {
		if v, ok := value.(IConfig); ok {
			// 如果没有文件提供原始配置，或者该节配置不在文件里
			if !fileExists || !core.fmt.InConfig(v.String()) {
				if err := saveDefaults(core, v); err != nil {
					core.errorf("保存配置 %q 的默认值失败: %w", v.String(), err)
					return true
				}
				changed = true
			} else {
				// 配置存在，立即反序列化加载到对应的内存模型实例
				if err := v.Load(); err != nil {
					core.errorf("装载配置 %v 失败: %w", key, err)
				}
			}
		}
		return true
	})
	core.applyMu.Unlock()

	// 此处刻意**不再**自动回写磁盘。
	//
	// 原先是 `if changed && self.AutoCreateFile { return self.SaveToFile() }`：
	// 只要有任何一个模型的配置节不在文件里，就把它的默认值刷回磁盘。后果是
	// 配置文件必然膨胀成全量快照（kylin：453 个 key，400+ 是默认值），运行期状态
	// 也会被写回去（Debug 端口、空字符串地址），而多进程同时启动时并发写同一个
	// config.json 会把文件写截断 —— 服务器随后静默退出而进程还活着。
	//
	// 更要命的是语义：默认值一旦落盘就成了「用户配置」，此后改代码里的默认值
	// 再也不生效（etcd 超时从 100ms 调到 5s 那次完全没起作用，就是因为各家配置
	// 文件里早已存着 28 份旧值）。
	//
	// 配置文件现在是只读输入。要生成一份可编辑的模板，用 Dump()。
	_ = changed

	return nil
}

// Dump 把当前全部配置（含所有代码默认值）写到指定文件，用于生成一份给人编辑的
// 模板。这是唯一会输出默认值的出口 —— 常规的 SaveToFile 只写差异。
//
// 不传 fileName 时写到 core.FileName 指向的路径。
func (self *Config) Dump(fileName ...string) error {
	core := self.Core()

	name := core.FileName
	if len(fileName) > 0 && fileName[0] != "" {
		name = fileName[0]
	}
	if name == "" {
		name = CONFIG_FILE_NAME
	}

	return core.fmt.WriteConfigFull(filepath.Join(AppPath, name))
}

func (self *Config) checkSelf(cfg IConfig) {
	if self.model == nil {
		self.model = cfg
	}
}

// bindCore 把注册方的 core 绑到本模型上，并返回最终生效的 core。
//
// 首次绑定生效；已绑定则保持不变（同一个模型被重复注册到不同的 Config 实例时，
// 以第一次为准而不是来回改绑 —— 改绑会让模型的历史值留在旧 core 里成为孤儿）。
func (self *Config) bindCore(c *core) *core {
	if self.core == nil {
		self.core = c
	}
	return self.core
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
	if !core.fmt.InConfig(model.String()) {
		// 「文件里没有这一节，用模型当前值兜底」—— 兜的就是默认值，必须落在
		// defaults 层。此前这里是裸的 model.Save()，走 SetValue 写进 override 层：
		// Register 前脚刚把默认值规规矩矩灌进 defaults，后脚这一行又把同一份值
		// 灌进优先级最高的 override，于是配置文件里的真实值**永远**盖不过启动时
		// 的默认值。用户改配置没反应且零提示，其隐蔽性正来自这两行的顺序。
		if err := saveDefaults(core, model); err != nil {
			return err
		}
	} else {
		// 解码装载
		err := core.fmt.UnmarshalKey(model.String(), model)
		if err != nil {
			return err
		}

		// 展开 ${VAR} 环境变量引用。
		//
		// 刻意只在**模型**上展开，不回写配置树：配置树里保留原始的 ${VAR} 字面量，
		// 这样后续任何一次 SaveToFile 都不会把解析出来的密钥明文烤进配置文件。
		// 外置密钥的意义正在于此 —— 若展开后的值回流到配置树，第一次保存就前功尽弃。
		if errs := expandEnvRefs(model); len(errs) > 0 {
			for _, e := range errs {
				core.errorf("配置 %q 的环境变量引用无法解析: %w", model.String(), e)
			}
		}
	}

	// 保存时如果尚未注册该模型则连带注册
	if _, has := core.models.Load(model.String()); !has {
		self.Register(model)
	}
	return nil
}

// SaveToFile 原地或切换后保存 core 缓存层配置到文件驱动。
//
// 只写差异：运行期显式设定过的值，以及本来就在配置文件里的值。代码默认值不落盘
// （要全量模板用 Dump）。
//
// AutoCreateFile 为 false 时，若目标文件尚不存在则跳过本次保存 —— 这是
// WithNoAutoCreateFile 的语义所在。它此前挂在 LoadFromFile 的自动回写上，
// 那条路径已随「配置文件是只读输入」一并取消。
func (self *Config) SaveToFile(opts ...Option) error {
	core := self.Core()

	for _, opt := range opts {
		opt(self)
	}

	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}

	path := filepath.Join(AppPath, core.FileName)
	if !self.AutoCreateFile && !utils.FileExists(path) {
		return nil
	}

	core.fmt.SetConfigFile(path)
	return core.fmt.WriteConfig()
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

		// 针对时间间隔 (time.Duration) 将其扁平化为毫无精度的毫秒级以增强向外输出配置的可读性
		if d, ok := v.(time.Duration); ok {
			v = d.Milliseconds()
		}

		key := strings.Join([]string{model.String(), k}, ".")
		if self.Core().savingDefaults.Load() {
			// 代码默认值 —— 落在 defaults 层，优先级低于配置文件
			self.Core().fmt.SetDefault(key, v)
		} else {
			// 运行期设定值 —— 落在 override 层，优先级高于配置文件
			self.SetValue(key, v)
		}
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

