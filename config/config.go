package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/utils"
)

// 配置树。
//
// 一句话说清模型：**配置树就是结构体图。** 一个组件的配置是一个普通结构体；它拥有的
// 子组件是它的一个字段（值或指针），于是子组件的配置节自然挂在它下面
// （server.http.transport.read_timeout）；它引用而不拥有的东西（registry / broker /
// router 这类跨组件共享的实例）只留一个名字字段，那些实例的配置各自是根节。
//
// 每个根节调一次 Bind(path, ptr)：把结构体当前值灌成默认值、把文件里那一节解码进来、
// 登记到树上。此后热重载、校验、Lint、体检报告都沿结构体递归，不需要任何组件再
// 拼路径、转发前缀、或在 option 里偷偷注册。
//
// 曾经不是这样：每个模型内嵌 config.Config 并自己拼 String()（两种形状、一处写反），
// 父组件靴带式地把前缀转发给它认识的子组件，子组件自建的孙组件留在旧前缀下成为孤儿，
// 一个 etcd registry 的配置在真栈文件里出现了 20 多份。那一套（Register / LoadToModel /
// SaveFromModel / IConfig）已随下游迁完一并删除。

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

	// core 是一棵配置树的本体：一个文件、一份 viper 缓存、一组已绑定的节。
	core struct {
		name string
		fmt  *format

		// nodes 是树上所有已绑定的节：小写点分路径 -> *node。
		// 子结构体不单独成节 —— 它们随拥有者一起被走查。
		nodes sync.Map

		FileName string
		Mode     ModeType

		// errMu/errs 收集装载期错误。
		//
		// 刻意不 log.Fatal：配置层是库代码，一个坏 key 直接 os.Exit(1) 让调用方毫无补救
		// 余地，而 vectors 会在运行期动态注册租户/模块配置，退进程不可接受。错误累积，
		// 由 Errors() 一次取全。
		errMu sync.Mutex
		errs  []error

		// applyMu 串行化所有「把配置写进结构体」的路径：Bind、LoadFromFile、Reload。
		// Reload 跑在 WithWatcher 的监听协程上，另两条跑在调用方线程，写的是同一批对象。
		//
		// Bind 内部不会再回调任何可能再次 Lock 的东西（旧的 Register→Load→LoadToModel→
		// Register 自死锁正是这么来的，见 client/concurrent_new_client_test.go）。
		applyMu sync.Mutex
	}

	// node 是树上的一个绑定点：一个路径，一个结构体指针。
	node struct {
		path string
		ptr  any
	}

	// Config 是一棵配置树的句柄：Default() 是进程默认那棵（读 config.json），New(name)
	// 另起一棵。它不是给内嵌的：配置模型是纯结构体，见文件头。
	Config struct {
		AutoCreateFile bool
		core           *core
	}

	// IAfterLoad 由配置结构体**可选**实现：每次从文件装载（Bind、LoadFromFile）并解码
	// 成功之后被调用，给「配置变了要顺手做点事」留一个钩子 —— 比如数据库配置在装载后
	// 把已建好的 ORM 连接切到新的数据源上。根节和任意深度的子结构体都可以实现。
	//
	// 热重载**不**走它：那条路由 IReloadable 决定（见 reload.go）。
	IAfterLoad interface {
		AfterLoad() error
	}
)

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

// Default 返回进程默认的配置树。
func Default() *Config {
	return defaultConfig
}

// Init 应用选项至默认树，可用于更改文件名、开启文件监听等。
func Init(opts ...Option) {
	defaultConfig.Init(opts...)
}

// Bind 把一个配置结构体绑到默认树的 path 上。见 (*Config).Bind。
func Bind(path string, ptr any) error {
	return defaultConfig.Bind(path, ptr)
}

// Unbind 把 path 从默认树上摘下。
func Unbind(path string) {
	defaultConfig.Unbind(path)
}

// Load 重新读取默认树的配置文件并装载进所有已绑定的结构体。
func Load(fileName ...string) error {
	return defaultConfig.LoadFromFile(fileName...)
}

// Errors 返回默认树上累积的装载期错误（多条已用 errors.Join 聚合）。
//
// 装载错误不 os.Exit：调用方（通常是 volts.Run 或各服务的 cmd）应在组件装配完毕后
// 查一次，自行决定是拒绝启动还是带着默认值继续跑。
func Errors() error {
	return defaultConfig.Errors()
}

// ClearErrors 清空默认树上累积的装载期错误。
func ClearErrors() {
	defaultConfig.ClearErrors()
}

// Dump 把默认树的全部配置（含代码默认值）写成一份可编辑的模板文件。
func Dump(fileName ...string) error {
	return defaultConfig.Dump(fileName...)
}

// Lint 报告默认树的配置文件里所有未知配置项（拼错的、过时的、没人认领的）。
// 须在全部组件装配完毕后调用。
func Lint() []LintIssue {
	return defaultConfig.Lint()
}

// Bound 返回默认树上所有已绑定的根路径。
func Bound() []string {
	return defaultConfig.Bound()
}

// New 创建并返回一棵命名的配置树。同名复用内存中已有的 core。
func New(name string, opts ...Option) *Config {
	var c *core
	if val, ok := cfgs.Load(name); ok {
		c = val.(*core)
	} else {
		c = &core{
			fmt:      newFormat(),
			FileName: CONFIG_FILE_NAME,
			Mode:     MODE_NORMAL,
			name:     name,
		}
		cfgs.Store(name, c)
	}

	cfg := &Config{
		core:           c,
		AutoCreateFile: true,
	}
	cfg.Init(opts...)

	// ★ 错误必须记下来。此前这里丢掉了返回值，而 `defaultConfig = New(DEFAULT_PREFIX)`
	// 就在包初始化里 —— 一份 JSON 写坏了的 config.json（漏个花括号是手改配置最常见的
	// 坏法）在进程启动时被完全忽略：所有配置停在代码默认值上，`Errors()` 是空的，
	// 日志里一个字都没有。这是"改了配置没反应"最极端的形态。
	if err := cfg.LoadFromFile(); err != nil {
		c.errorf("装载配置文件 %q 失败（本次启动将全部使用代码默认值）: %w", c.FileName, err)
	}

	return cfg
}

// Errors 返回本树上累积的装载期错误。
func (self *Config) Errors() error {
	return self.Core().Errors()
}

// ClearErrors 清空本树上累积的装载期错误。
func (self *Config) ClearErrors() {
	self.Core().ClearErrors()
}

func (self *Config) Core() *core {
	if self.core == nil {
		// 零值句柄回落到默认树
		if c, ok := cfgs.Load(DEFAULT_PREFIX); ok {
			self.core = c.(*core)
		}
	}
	return self.core
}

func (self *Config) Init(opts ...Option) {
	self.Core()
	for _, opt := range opts {
		opt(self)
	}
}

// Bind 把配置结构体 ptr 绑到树的 path 上，并立即完成装载：
//
//  1. 结构体当前的字段值作为**代码默认值**灌进 defaults 层（优先级最低，盖不过文件）；
//  2. 配置文件里若有这一节，解码进 ptr（含 ${VAR} 展开）；
//  3. 登记为一个根节，参与此后的 LoadFromFile / Reload / Validate / Lint。
//
// ptr 的子结构体字段（值或非 nil 指针）随之成为子节，路径按字段键名往下延伸。若某个
// 子指针此前已经作为别的根节绑过（典型：transport.NewHTTPTransport() 自己绑在
// transport.default，随后被 server.WithTransport 注入），那个根节会被**收编**——从树上
// 摘掉，配置从此只认 server.<name>.transport 这一处。归属由拥有者决定，被拥有的东西
// 不该再有第二个地址。
//
// 同一 path 被同类型对象再次绑定是多实例的常态（每个 HttpClient 都叫 client.default），
// 后来者接管该节，不告警；类型不同才是真撞车，会记一行日志。
//
// 装载错误既作为返回值，也累积进 Errors()。
func (self *Config) Bind(path string, ptr any) error {
	core := self.Core()

	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return core.errorf("Bind: 配置路径为空（%T）", ptr)
	}
	rv, ok := structValueOf(ptr)
	if !ok {
		return core.errorf("Bind %q: 需要非 nil 的结构体指针，得到 %T", path, ptr)
	}

	core.applyMu.Lock()
	defer core.applyMu.Unlock()

	core.adoptChildren(path, rv)

	if old, loaded := core.nodes.Load(path); loaded {
		if o := old.(*node); o.ptr != ptr && reflect.TypeOf(o.ptr) != reflect.TypeOf(ptr) {
			log.Printf("config: 配置节 %q 原由 %T 绑定，现被 %T 接管；两者共用同一节配置，先前对象不再参与装载与热重载",
				path, o.ptr, ptr)
		}
	}
	core.nodes.Store(path, &node{path: path, ptr: ptr})

	setLeaves(core.fmt, path, rv, true)
	return core.loadNode(path, ptr)
}

// Unbind 把 path 从树上摘下。此后该节不再参与装载与热重载；已灌进 defaults 层的
// 默认值保留（viper 不支持删除），对 Get 的调用方无害。
func (self *Config) Unbind(path string) {
	self.Core().nodes.Delete(strings.ToLower(strings.TrimSpace(path)))
}

// Bound 返回树上所有已绑定的根路径，按字典序。
func (self *Config) Bound() []string {
	var out []string
	self.Core().nodes.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	sort.Strings(out)
	return out
}

// adoptChildren 收编 rv 的子结构体指针在本树上的独立根节。调用方须持有 applyMu。
func (self *core) adoptChildren(path string, rv reflect.Value) {
	walk(rv, path, func(p string, sv reflect.Value) {
		if p == path || !sv.CanAddr() {
			return
		}
		addr := sv.Addr().Interface()
		self.nodes.Range(func(k, v any) bool {
			if n := v.(*node); n.ptr == addr && n.path != p {
				self.nodes.Delete(k)
			}
			return true
		})
	}, nil)
}

// setLeaves 把结构体的全部叶子写进配置树：asDefault 为真写 defaults 层，否则写
// override 层（运行期设定值）。
func setLeaves(f *format, path string, rv reflect.Value, asDefault bool) {
	walk(rv, path, nil, func(p string, fv reflect.Value) {
		v, ok := leafValue(fv)
		if !ok {
			return
		}
		if asDefault {
			f.SetDefault(p, v)
		} else {
			f.SetValue(p, v)
		}
	})
}

// loadNode 把文件里 path 那一节解码进 ptr，并展开 ${VAR}。文件里没有这一节时什么都不做。
// 调用方须持有 applyMu。
func (self *core) loadNode(path string, ptr any) error {
	found, err := self.fmt.decodeSection(path, ptr)
	if err != nil {
		return self.errorf("装载配置 %q 失败: %w", path, err)
	}
	if !found {
		return nil
	}

	// 展开 ${VAR} 环境变量引用。
	//
	// 刻意只在**结构体**上展开，不回写配置树：树里保留原始的 ${VAR} 字面量，这样后续
	// 任何一次 SaveToFile 都不会把解析出来的密钥明文烤进配置文件。外置密钥的意义正在
	// 于此 —— 若展开后的值回流到配置树，第一次保存就前功尽弃。
	if errs := expandEnvRefs(ptr, path); len(errs) > 0 {
		for _, e := range errs {
			self.errorf("配置 %q 的环境变量引用无法解析: %w", path, e)
		}
		return errors.Join(errs...)
	}

	// 装载后钩子，子先父后
	var hookErrs []error
	rv, _ := structValueOf(ptr)
	afterLoad(rv, path, &hookErrs)
	for _, e := range hookErrs {
		self.errorf("配置 %q 的装载后钩子失败: %w", path, e)
	}
	return errors.Join(hookErrs...)
}

func afterLoad(v reflect.Value, path string, errs *[]error) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		if key == "" {
			continue
		}
		if sv, ok := isSection(v.Field(i)); ok && sv.IsValid() {
			afterLoad(sv, path+"."+key, errs)
		}
	}
	if v.CanAddr() {
		if h, ok := v.Addr().Interface().(IAfterLoad); ok {
			if err := h.AfterLoad(); err != nil {
				*errs = append(*errs, fmt.Errorf("%s: %w", path, err))
			}
		}
	}
}

// StopWatcher 停止 WithWatcher 启动的配置文件监听协程。可安全多次调用。
func (self *Config) StopWatcher() {
	if core := self.Core(); core != nil && core.fmt != nil {
		core.fmt.StopWatch()
	}
}

// InConfig 报告配置文件里是否有 path 这一节（或这一项）。
func (self *Config) InConfig(path string) bool {
	return self.Core().fmt.InConfig(path)
}

// resolveConfigPath 定位配置文件：先看可执行文件旁边，找不到再看**工作目录**。
//
// 绝对路径原样返回。
//
// 为什么需要回落：`AppPath` 是可执行文件所在目录，对部署好的服务是对的，但
// `go test` 把测试二进制编到临时目录里跑 —— 于是包目录下那份 config.json **一次都
// 没有被读过**。表现不是报错，是每个配置模型都停在代码默认值上：vectors 那边 storage
// 的测试因此拿到空的 bucket_name，报的却是 OSS SDK 的 "bucket name len is between
// [3-63], now is 0"，没有任何东西指回配置没加载。
//
// 回落只在「可执行文件旁边没有」时发生，所以对正常部署零影响。
func resolveConfigPath(fileName string) string {
	if filepath.IsAbs(fileName) {
		return fileName
	}
	if p := filepath.Join(AppPath, fileName); utils.FileExists(p) {
		return p
	}
	if cwd, err := os.Getwd(); err == nil {
		if p := filepath.Join(cwd, fileName); utils.FileExists(p) {
			return p
		}
	}
	// 两处都没有：仍返回 AppPath 下的路径，让后续的 AutoCreateFile / SaveToFile
	// 写在原来的位置。
	return filepath.Join(AppPath, fileName)
}

// LoadFromFile 读取配置文件，并把每个已绑定的节重新装载进对应的结构体。
//
// 文件里没有的节不被触碰（结构体保持当前值 —— 默认值在 Bind 时已经灌过了）。
// 此处刻意**不**回写磁盘，见 SaveToFile：配置文件是只读输入。
func (self *Config) LoadFromFile(fileName ...string) error {
	core := self.Core()

	if len(fileName) > 0 && fileName[0] != "" {
		core.FileName = fileName[0]
	}
	if core.FileName == "" {
		core.FileName = CONFIG_FILE_NAME
	}
	filePath := resolveConfigPath(core.FileName)
	core.fmt.SetConfigFile(filePath)

	if utils.FileExists(filePath) {
		if err := core.fmt.ReadInConfig(); err != nil {
			return err
		}
		// 建立「叶子路径 -> 行号」索引，供 Origin / Lint 定位用。
		core.fmt.indexFileLines(filePath)
	}

	core.applyMu.Lock()
	defer core.applyMu.Unlock()
	core.nodes.Range(func(_ any, value any) bool {
		n := value.(*node)
		core.loadNode(n.path, n.ptr) // 错误已累积，继续装下一个
		return true
	})
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

// SaveToFile 把配置落盘，但**只写差异**：运行期经 SetValue 显式设定过的值，以及本来
// 就在配置文件里的值。代码默认值不落盘（要全量模板用 Dump）。
//
// 默认值一旦落盘就成了「用户配置」，此后改代码里的默认值再也不生效 —— kylin 的
// config.json 曾因此膨胀到 453 个 key，其中 400+ 是默认值。
//
// AutoCreateFile 为 false 时，若目标文件尚不存在则跳过本次保存。
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

func (self *Config) GetBool(field string, defaultValue bool) bool {
	return self.Core().fmt.GetBool(field, defaultValue)
}

func (self *Config) GetString(field, defaultValue string) string {
	return self.Core().fmt.GetString(field, defaultValue)
}

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

// SetValue 写入一个运行期设定值（override 层，优先级高于配置文件），SaveToFile 时落盘。
func (self *Config) SetValue(field string, value interface{}) {
	self.Core().fmt.SetValue(field, value)
}
