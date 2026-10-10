package logger

import (
	"fmt"
	"os"
	"sync"

	"github.com/volts-dev/volts/config"
)

type (
	Option func(*Config)

	// Config 是一个日志器的配置，配置节 logger.<name>（name 即 New(prefix) 的 prefix）。
	Config struct {
		Name  string `field:"-"`
		Debug bool   `field:"-"`
		Level Level

		// apply 把等级落到热路径读的原子镜像上，由 New 填。配置文件装载 / 热重载改了
		// Level 之后经它生效 —— 此前只在 New 那一刻读一次。它**只动镜像不动 Level**：
		// 环境变量的覆盖不该被 SaveToFile 当成配置差异落进文件。
		apply func(Level)
	}
)

// EnvLevel 是覆盖**全部**日志器等级的环境变量，取 ParseLevel 认得的名字（info / warn /
// debug …）。它压过配置文件：部署出去的 config.json 里每个日志器都带着一份早年持久化
// 下来的 "level": 9（debug），只改代码默认值动不了它们；运维要的是一个不用逐节改文件
// 的开关。
const EnvLevel = "VOLTS_LOG_LEVEL"

// envLevel 读 EnvLevel。没设返回 false；设了但认不出，报一次并当没设。
func envLevel() (Level, bool) {
	v := os.Getenv(EnvLevel)
	if v == "" {
		return 0, false
	}
	lv, ok := ParseLevel(v)
	if !ok {
		envWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "logger: %s=%q 认不出，忽略（可用 debug / trace / info / warn / error）\n", EnvLevel, v)
		})
		return 0, false
	}
	return lv, true
}

// DefaultLevel 是没有任何配置时的等级：Info。
//
// 此前默认 Debug（数值最大，Trace 也一并放行），一个 aio 进程每 30 秒为几十个服务各打
// 一行续租日志、每条 SQL 一行，云上 syslog 一天 8GB。调试时用 EnvLevel 或配置文件抬上去。
const DefaultLevel = LevelInfo

// effective 是这一节最终生效的等级：环境变量 > 配置（文件或代码默认）。
func (self *Config) effective() Level {
	if lv, ok := envLevel(); ok {
		return lv
	}
	return self.Level
}

// AfterLoad 在每次从文件装载之后把等级落到热路径上（config.IAfterLoad）。
func (self *Config) AfterLoad() error {
	if self.apply != nil {
		self.apply(self.effective())
	}
	return nil
}

// OnReload 接管热重载：Level 经 SetLevel 加锁写回并同步原子镜像（config.IReloadable）。
func (self *Config) OnReload(next any) error {
	n, ok := next.(*Config)
	if !ok {
		return fmt.Errorf("logger: 热重载类型不符: %T", next)
	}
	if self.apply == nil {
		return config.ErrReloadUnsupported
	}
	self.Level = n.Level
	self.apply(self.effective())
	return nil
}

var (
	envWarnOnce sync.Once

	creators = make(map[string]IWriterType) // 注册的Writer类型函数接口
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Level: DefaultLevel,
		Name:  "logger",
	}
	cfg.Init(opts...)
	config.Bind("logger."+cfg.Name, cfg)
	return cfg
}

func (self *Config) Init(opts ...Option) {
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

// WithName 设定日志器名，也是配置路径 logger.<name>。
func WithName(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
	}
}

func WithLevel(lvl Level) Option {
	return func(cfg *Config) {
		cfg.Level = lvl
	}
}
