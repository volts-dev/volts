package router

import (
	"io/fs"
	"path"
	"strings"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
)

var (
	ControllerMethodNameMapper = func(name string) string {
		name = strings.ToLower(name)
		// 为特殊控制器提供固定的接口命名 比如Model.Read被占用的情款下改用Model.Api_Read
		name = strings.Replace(name, "api_", "", 1)
		return name
	}
)

type (
	Option func(*Config)

	// PathRewriter 在**路由匹配之前**改写请求路径,返回改写后的路径与被摘掉的前缀。
	// 不改写时返回 ("", "") 或让 stripped 为空。
	//
	// 存在的理由是多语言 URL 前缀(/fr/shop → /shop + "/fr"):路由匹配发生在
	// middleware 之前(middleware 是装配期挂到 handler 链上的),所以中间件根本没
	// 机会在匹配前动路径。若不在这里摘掉前缀,每个前台模块都得把自己的路由再注册
	// 一份 /{lang}/... —— 路由表和注册到 registry 的端点表双倍膨胀,而且根部的
	// 单段通配会抢走所有二级路径(/company/team 被读成 lang=company + /team)。
	//
	// 约束:
	//   - 必须是纯函数且够快,它在每个 HTTP 请求的最前面执行,不允许查库/加锁;
	//   - 不要在这里做重定向决策,只摘前缀。前缀会经 THttpContext.PathPrefix()
	//     交给 handler,重定向/链接生成由上层拼回去。
	PathRewriter func(path string) (newPath, stripped string)

	// Config 是路由器的配置。纯结构体：导出字段就是配置文件里的项，接口 / 函数类型的
	// 字段（Logger、Selector、Registry、RecoverHandler、PathRewriter）是实例引用，天然
	// 不进配置。配置节是 router.<name>（默认 router.default）。
	//
	// router 是跨 server **共享**的（vectors 把同一个路由器交给 HTTP 与 RPC 两台 server），
	// 所以它是根节，不挂在 server 下面；它**拥有**自己的 selector，那份配置是它的
	// SelectorConfig 字段，配置节因此是 router.<name>.selector.*。
	Config struct {
		Name  string `field:"-"` // 配置路径 router.<name>
		Debug bool   `field:"-"`

		SelectorStrategy string           `field:"selector_strategy"`
		SelectorConfig   *selector.Config `field:"selector"` // 拥有的选择器的配置

		// ---- 实例，不进配置文件 ----
		Logger         logger.ILogger
		Router         *TRouter `field:"-"`
		Selector       selector.ISelector
		Registry       registry.IRegistry
		RegistryCacher registry.IRegistryCacher // registry cache
		RecoverHandler func(IContext)
		PathRewriter   PathRewriter // 路由匹配前的路径改写(多语言 URL 前缀),见类型注释

		// mapping to config file
		Recover                   bool   `field:"recover"`
		RouterTreePrinter         bool   `field:"router_tree_printer"`
		RouterTreeRefreshInterval int    `field:"router_tree_refresh_interval"`
		RequestPrinter            bool   `field:"request_printer"`
		UsePprof                  bool   `field:"use_pprof"`
		UseOpenAPI                bool   `field:"use_openapi"`      // 配置开关：开启后挂载 /openapi.json 与 /docs
		OpenAPITitle              string `field:"openapi_title"`    // 文档标题（空则默认 "Volts API"）
		OpenAPIVersion            string `field:"openapi_version"`  // 文档版本（空则默认 "1.0.0"）
		OpenAPIGateway            bool   `field:"openapi_gateway"`  // 是否聚合 registry 中的远程服务端点
		UploadBuf                 int    `field:"upload_buf"`       // 上传文件大小MB
		StaticCacheTTL            int    `field:"static_cache_ttl"` // 缓存秒数，默认 60；0 = 永不过期（仅靠 watcher 失效）

		// 响应压缩，见 compress.go。默认关闭 —— 开启是部署方的明示动作（BREACH）。
		Compress        bool     `field:"compress"`
		CompressMinSize int      `field:"compress_min_size"` // 小于此字节数不压，0 = DefaultCompressMinSize
		CompressTypes   []string `field:"compress_types"`    // 参与压缩的 Content-Type，空 = DefaultCompressTypes

		use *Config // WithConfig
	}

	GroupOption func(*GroupConfig)
	GroupConfig struct {
		Group          *TGroup
		Name           string // 当前源代码文件夹名称
		_PrefixPath    string
		Path           string // URL 路径
		FilePath       string // 当前源代码文件夹路径
		IsService      bool   // 是否在registry注册为独立的服务
		Service        *registry.Service
		PathPrefix     string
		EmbedFS        fs.FS `field:"-"` // module 层注入的 embed.FS，供 SetStatic 使用
		StaticCacheTTL int   `field:"-"` // 0 = use default (60s); negative = never expire
	}
)

// RecoverEnabled 与 PrintsRequest 是**每请求都要读、又会被热重载改写**的两个字段的
// 访问器，读锁的理由见 config.HotRLock。
//
// 直接读 self.config.Recover 与热重载的写入之间没有任何同步，而 vectors 的
// cmd/service.go 是开着 WithWatcher 的 —— 这条竞态是线上活的。
// 装配期的读不用走这里：那时候还没有请求在跑。
func (self *Config) RecoverEnabled() bool {
	config.HotRLock()
	defer config.HotRUnlock()
	return self.Recover
}

// PrintsRequest 见 RecoverEnabled。
func (self *Config) PrintsRequest() bool {
	config.HotRLock()
	defer config.HotRUnlock()
	return self.RequestPrinter
}

// NewConfig 返回一份带代码默认值、**尚未绑定**到配置树的路由器配置。
//
// 配合 WithConfig 用：New(WithConfig(NewConfig())) 得到一个不在配置文件里出现的
// 私有路由器（broker/http 的内部投递路由就是这么建的）。
func NewConfig() *Config {
	return &Config{
		Name:                      "default",
		Logger:                    log,
		Recover:                   true,
		RecoverHandler:            recoverHandler,
		UploadBuf:                 25,
		StaticCacheTTL:            60,
		RouterTreeRefreshInterval: 60,
		SelectorStrategy:          "random",
		SelectorConfig:            selector.NewConfig(),
	}
}

func newConfig(opts ...Option) *Config {
	cfg := NewConfig()
	cfg.Init(opts...)
	if ext := cfg.use; ext != nil {
		cfg.use = nil
		if ext.Logger == nil {
			ext.Logger = log
		}
		if ext.RecoverHandler == nil {
			ext.RecoverHandler = recoverHandler
		}
		if ext.SelectorConfig == nil {
			ext.SelectorConfig = selector.NewConfig()
		}
		ext.Init(opts...)
		ext.use = nil
		cfg = ext
	} else {
		// 一次绑定，整棵子树（含 selector）随之装载；子组件在此之后才建，拿的已经是
		// 文件里的值。
		config.Bind("router."+cfg.Name, cfg)
	}

	if cfg.Registry == nil {
		cfg.Registry = registry.NewNopRegistry()
		cfg.RegistryCacher = registry.NewCacher(cfg.Registry)
	} else if rc, ok := cfg.Registry.(registry.IRegistryCacher); ok {
		cfg.RegistryCacher = rc
	} else {
		cfg.RegistryCacher = registry.NewCacher(cfg.Registry)
	}

	if cfg.Selector == nil {
		cfg.Selector = selector.New(
			selector.WithConfig(cfg.SelectorConfig), // 拥有：配置节 router.<name>.selector
			selector.Registry(cfg.Registry),
			selector.WithStrategy(cfg.SelectorStrategy),
		)
	}

	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func (self *GroupConfig) Init(opts ...GroupOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

func Logger() logger.ILogger {
	return log
}

func WithNameMapper(fn func(string) string) config.Option {
	return func(cfg *config.Config) {
		ControllerMethodNameMapper = fn
	}
}

func WithPprof() Option {
	return func(cfg *Config) {
		cfg.UsePprof = true
		//cfg.Router.RegisterGroup(pprofGroup())
	}
}

// WithOpenAPI 以编程方式开启 OpenAPI 文档（等价于配置文件里 use_openapi: true）。
func WithOpenAPI() Option {
	return func(cfg *Config) {
		cfg.UseOpenAPI = true
	}
}

// Register the service with a TTL
func WithRoutesTreePrinter() Option {
	return func(cfg *Config) {
		cfg.RouterTreePrinter = true
		//cfg.SetValue("print_router_tree", true)
	}
}

// Register the service with a TTL
func WithRequestPrinter() Option {
	return func(cfg *Config) {
		cfg.RequestPrinter = true
		//cfg.SetValue("print_request", true)
	}
}

func WithRecover(on bool) Option {
	return func(cfg *Config) {
		cfg.Recover = on
	}
}

func WithRecoverHandler(handler func(IContext)) Option {
	return func(cfg *Config) {
		cfg.RecoverHandler = handler
	}
}

// WithPathRewriter 装上路由匹配前的路径改写钩子(多语言 URL 前缀),见 PathRewriter。
func WithPathRewriter(fn PathRewriter) Option {
	return func(cfg *Config) {
		cfg.PathRewriter = fn
	}
}

func WithRegistry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		cfg.Registry = r
		if rc, ok := r.(registry.IRegistryCacher); ok {
			cfg.RegistryCacher = rc
		} else {
			cfg.RegistryCacher = registry.NewCacher(r)
		}
		if cfg.Selector != nil {
			cfg.Selector.Init(selector.Registry(r))
		}
	}
}

func WithStatic(path ...string) GroupOption {
	return func(cfg *GroupConfig) {
		if len(path) > 0 {
			cfg.Group.SetStatic(path[0])
			return
		}

		// init router tree
		cfg.Group.SetStatic("/static")
	}
}

func WithModuleFilePath(path string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.FilePath = path
	}
}

// offset 标记希望返回调用该函数文件偏移,当前文件无需填写！其他代码文件调用到当前文件+1
func WithCurrentModulePath(offset ...int) GroupOption {
	return func(cfg *GroupConfig) {
		var line = 4
		if len(offset) > 0 {
			line += offset[0]
		}
		cfg.FilePath, cfg.Name = curFilePath(line)
	}
}

func WithGroupName(name string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.Name = name
		cfg.Path = path.Join("/", cfg.Name)
	}
}

// TODO 更名
// default url"/abc"
// PrefixPath url "/PrefixPath/abc"
func WithGroupPathPrefix(prefixPath string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.PathPrefix = prefixPath
	}
}

// 独立出来注册为单一微服务在registry里
func WithAloneService(asService bool, servicsName string, prefix ...string) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.Name = servicsName
		cfg.Path = path.Join("/", cfg.Name)
		cfg.IsService = asService
		if len(prefix) > 0 {
			cfg.PathPrefix = prefix[0]
		}
	}
}

func WithEmbedFS(fsys fs.FS) GroupOption {
	return func(cfg *GroupConfig) {
		cfg.EmbedFS = fsys
	}
}

// WithCompress 开启 HTTP 响应压缩（gzip / deflate）。
//
// 默认关闭。开启前请确认本服务的响应里不会把**攻击者可影响的输入**与**同一响应中的
// 机密**（CSRF token、会话标识）放在一起 —— 压缩率本身是一条侧信道，那正是 BREACH
// 的前提，且 HTTPS 挡不住（泄漏的是密文长度）。纯静态资源与不含机密的 API 响应没有
// 这个问题。详见 compress.go 顶部。
//
// minSize 省略时用 DefaultCompressMinSize（1KB）；types 省略时用 DefaultCompressTypes。
func WithCompress(minSize ...int) Option {
	return func(cfg *Config) {
		cfg.Compress = true
		if len(minSize) > 0 {
			cfg.CompressMinSize = minSize[0]
		}
	}
}

// WithCompressTypes 指定参与压缩的 Content-Type（前缀以 "/" 或 "*" 结尾，如 "text/"）。
// 不自动开启压缩，需与 WithCompress 同用。
func WithCompressTypes(types ...string) Option {
	return func(cfg *Config) {
		cfg.CompressTypes = types
	}
}

// 修改Config.json的路径
// WithName 设定配置路径 router.<name>。
func WithName(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
	}
}

// WithConfig 让路由器使用调用方持有的这份配置，不再自建、不再绑定到配置树。
func WithConfig(c *Config) Option {
	return func(cfg *Config) {
		cfg.use = c
	}
}
