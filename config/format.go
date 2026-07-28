package config

import (
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"github.com/volts-dev/utils"
)

type (
	format struct {
		sync.RWMutex
		v *viper.Viper

		// overrideKeys 记录经 SetValue 显式设定过的 key（点分全路径）。
		//
		// 落盘时用它把「代码默认值」与「真的有人设定过的值」区分开：只有本集合里的
		// key、以及本来就存在于配置文件里的 key 才会被写回。没有这份记账，
		// viper 的 AllSettings() 会把 defaults 层一并吐出来，配置文件于是膨胀成
		// 全量快照 —— kylin 那份 453 个 key 里 400+ 是这么来的，其中 registry.timeout
		// 这一个默认值就复制了 15 份。副本一旦落盘就成了「用户配置」，此后改代码
		// 里的默认值再也不生效。
		overrideKeys map[string]struct{}

		// lines 记录配置文件里每个叶子路径的行号，由 indexFileLines 建立。
		// 错误消息靠它给出「config.json:47」这样的位置 —— kylin 的配置里
		// read_timeout 出现 9 次，没有行号的错误消息定位不了任何东西。
		lines map[string]int

		// watchOnce / watchStop 管理配置文件监听协程。
		watchOnce sync.Once
		watchStop chan struct{}
	}
)

func newFormat() *format {
	file := viper.New()
	//file.SetConfigFile(CONFIG_FILE_NAME)
	return &format{
		v:            file,
		overrideKeys: make(map[string]struct{}),
		lines:        make(map[string]int),
		watchStop:    make(chan struct{}),
	}
}

// Watch 监听配置文件变动，变动时先重载再回调 onChange。
//
// 这里自建 fsnotify 监听，而不用 viper.WatchConfig —— 后者会启动一条**不受
// 本结构体的锁保护**的协程去调 v.ReadInConfig()，与 SetConfigFile / 各类
// Get 形成数据竞态（-race 下 config 包必炸，viper 的 Viper 对这组操作不是
// 并发安全的）。本实现所有 viper 访问都走加锁的方法。
//
// 监听的是文件所在**目录**而非文件本身：多数编辑器保存时走"写临时文件 + 原子
// rename"，直接监听文件会在第一次保存后就丢掉 inode，再不触发。
//
// 多次调用只会启动一条协程。
func (self *format) Watch(path string, onChange func()) error {
	var err error

	self.watchOnce.Do(func() {
		var watcher *fsnotify.Watcher
		watcher, err = fsnotify.NewWatcher()
		if err != nil {
			return
		}

		dir := filepath.Dir(path)
		if err = watcher.Add(dir); err != nil {
			watcher.Close()
			return
		}

		target := filepath.Clean(path)

		go func() {
			defer watcher.Close()
			for {
				select {
				case <-self.watchStop:
					return

				case ev, ok := <-watcher.Events:
					if !ok {
						return
					}
					if filepath.Clean(ev.Name) != target {
						continue
					}
					if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) {
						continue
					}
					// 走加锁的 ReadInConfig，与其他访问互斥
					if rerr := self.ReadInConfig(); rerr != nil {
						continue
					}
					if onChange != nil {
						onChange()
					}

				case _, ok := <-watcher.Errors:
					if !ok {
						return
					}
				}
			}
		}()
	})

	return err
}

// StopWatch 停止配置文件监听。可安全多次调用。
func (self *format) StopWatch() {
	select {
	case <-self.watchStop:
		// 已关闭
	default:
		close(self.watchStop)
	}
}

func (self *format) InConfig(key string) bool {
	self.RLock()
	defer self.RUnlock()
	return self.v.InConfig(key)
}

func (self *format) SetConfigFile(in string) {
	self.Lock()
	defer self.Unlock()
	self.v.SetConfigFile(in)
}

func (self *format) ReadInConfig() error {
	self.Lock()
	defer self.Unlock()
	return self.v.ReadInConfig()
}

// WriteConfig 把配置落盘，但**只写差异**：运行期显式设定过的值，以及本来就在
// 配置文件里的值。纯粹的代码默认值不落盘。
//
// 这样配置文件里的每一行都是「有人真的写过」，可读、可 diff、可手改；也让「改代码
// 默认值」这件事重新生效 —— 默认值一旦被回写成文件内容，就再也不是默认值了。
func (self *format) WriteConfig() error {
	self.Lock()
	defer self.Unlock()

	out := viper.New()
	out.SetConfigFile(self.v.ConfigFileUsed())
	for path, val := range self.persistableLeaves() {
		out.Set(path, val)
	}
	return out.WriteConfig()
}

// WriteConfigFull 落盘**全量**配置（含所有代码默认值），用于生成给人编辑的模板。
//
// 凭据字段会被替换成 ${环境变量名} 占位符而不是原值：模板的用途就是提交进版本库、
// 发给同事、当部署样例，把明文密钥烤进去等于把它散布得更广。占位符本身就是可用的
// 写法（见 config/env.go），拿到模板的人设好环境变量即可直接跑。
func (self *format) WriteConfigFull(file string) error {
	self.Lock()
	defer self.Unlock()

	out := viper.New()
	out.SetConfigFile(file)
	collectLeaves(self.v.AllSettings(), "", func(path string, val any) {
		if name, ok := secretPlaceholder(path, val); ok {
			out.Set(path, name)
			return
		}
		out.Set(path, val)
	})
	return out.WriteConfig()
}

// secretPlaceholder 为疑似凭据的叶子生成 ${环境变量名} 占位符。
//
// 变量名由配置路径推导（db.db_password -> ${DB_DB_PASSWORD}），这样模板既隐去了
// 原值，又直接告诉拿到它的人该设哪个环境变量。
func secretPlaceholder(path string, val any) (string, bool) {
	if !isLikelySecret(lastSegment(path), val) {
		return "", false
	}
	name := strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(path))
	return "${" + name + "}", true
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// persistableLeaves 返回应当落盘的叶子集合，键为点分全路径。调用方须持有锁。
func (self *format) persistableLeaves() map[string]any {
	out := make(map[string]any)
	collectLeaves(self.v.AllSettings(), "", func(path string, val any) {
		// Delete 走的是 v.Set(key, nil)（viper 无法真正删除 key），若把 nil 写回去，
		// 文件里会留下一个 null 节点，下次加载时该 key「存在但为空」。
		if val == nil {
			return
		}
		if _, ok := self.overrideKeys[path]; ok {
			out[path] = val
			return
		}
		if self.v.InConfig(path) {
			out[path] = val
		}
	})
	return out
}

// collectLeaves 深度遍历嵌套 map，对每个叶子回调一次点分全路径。
func collectLeaves(m map[string]any, prefix string, fn func(path string, val any)) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok && len(sub) > 0 {
			collectLeaves(sub, path, fn)
			continue
		}
		fn(path, v)
	}
}

func (self *format) GetBool(key string, defaultValue bool) bool {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetBool(key)
}

// GetString 读取字符串配置，并展开其中的 ${VAR} 环境变量引用。
//
// 与模型装载路径（LoadToModel -> expandEnvRefs）保持一致：两条读路径对同一份数据
// 必须给出同样的结果，否则把密钥外置成 ${VAR} 之后，走 GetString 的调用方会拿到
// 字面量 "${VS_DB_PASSWORD}" 并把它当成密码用。
//
// 展开失败（变量未设置且未声明缺省）时返回 defaultValue 并告警，而不是把 ${VAR}
// 字面量交出去 —— 后者会以一个看起来像密码的字符串继续往下跑。
func (self *format) GetString(key, defaultValue string) string {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	raw := self.v.GetString(key)
	out, err := expandString(raw)
	if err != nil {
		log.Printf("config: %s 的环境变量引用无法解析，回落默认值: %v", key, err)
		return defaultValue
	}
	return out
}

// GetInt from default namespace
func (self *format) GetInt(key string, defaultValue int) int {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetInt(key)
}

func (self *format) GetInt32(key string, defaultValue int32) int32 {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetInt32(key)
}

func (self *format) GetInt64(key string, defaultValue int64) int64 {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetInt64(key)
}

func (self *format) GetIntSlice(key string, defaultValue []int) []int {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetIntSlice(key)
}

func (self *format) GetTime(key string, defaultValue time.Time) time.Time {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetTime(key)
}

// GetDuration 读取时长配置。
//
// 单位约定必须与写入端一致：SaveFromModel 把 time.Duration 落盘为
// d.Milliseconds()（毫秒整数），withDurationHook 也按毫秒把整数解回 Duration。
// 此前这里却是 time.Duration(GetInt64(key)) —— **按纳秒解释**，同一份数据经
// UnmarshalKey 读出 5s、经 GetDuration 读出 5µs，差 100 万倍。
//
// 三种形态都接受：带单位字符串（"5s"，人手写配置的首选）、time.Duration、
// 裸数字（按毫秒，兼容既有配置文件）。
func (self *format) GetDuration(key string, defaultValue time.Duration) time.Duration {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}

	switch v := self.v.Get(key).(type) {
	case string:
		d, err := time.ParseDuration(v)
		if err != nil {
			return defaultValue
		}
		return d
	case time.Duration:
		return v
	default:
		return time.Duration(self.v.GetInt64(key)) * time.Millisecond
	}
}

func (self *format) GetFloat64(key string, defaultValue float64) float64 {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetFloat64(key)
}

func (self *format) Unmarshal(rawVal interface{}) error {
	self.Lock()
	defer self.Unlock()
	return self.v.Unmarshal(rawVal, withTagName("field"), withMatchName(), withDurationHook())
}

// key 可以整个config结构
func (self *format) UnmarshalKey(key string, rawVal interface{}) error {
	self.Lock()
	defer self.Unlock()
	return self.v.UnmarshalKey(key, rawVal, withTagName("field"), withMatchName(), withDurationHook())
}

// SetValue 写入一个运行期显式设定值（override 层，优先级高于配置文件），并记账
// 以便落盘时能与代码默认值区分开。
func (self *format) SetValue(key string, value interface{}) {
	self.Lock()
	self.v.Set(key, value)
	self.overrideKeys[strings.ToLower(key)] = struct{}{}
	self.Unlock()
}

// SetDefault 写入一个代码默认值（defaults 层，优先级低于配置文件）。
// 刻意不记账：默认值不该落盘，否则它就不再是默认值了。
func (self *format) SetDefault(key string, value interface{}) {
	self.Lock()
	self.v.SetDefault(key, value)
	self.Unlock()
}

func (self *format) Delete(key string) {
	self.Lock()
	self.v.Set(key, nil)
	// 同时撤销记账：这个 key 不再是「有人设定过的值」，落盘时不应带上它。
	// persistableLeaves 另外还会跳过 nil 值，两处配合才能真正把 key 从文件里去掉。
	prefix := strings.ToLower(key) + "."
	for k := range self.overrideKeys {
		if k == strings.ToLower(key) || strings.HasPrefix(k, prefix) {
			delete(self.overrideKeys, k)
		}
	}
	self.Unlock()
}

func withTagName(tag string) viper.DecoderConfigOption {
	return func(c *mapstructure.DecoderConfig) {
		c.TagName = tag
	}
}

func withMatchName() viper.DecoderConfigOption {
	return func(c *mapstructure.DecoderConfig) {
		c.MatchName = func(mapKey, fieldName string) bool {
			return mapKey == utils.SnakeCasedName(fieldName)
		}
	}
}

func withDurationHook() viper.DecoderConfigOption {
	return func(c *mapstructure.DecoderConfig) {
		customHook := func(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
			if t != reflect.TypeOf(time.Duration(0)) {
				return data, nil
			}
			switch v := data.(type) {
			case int:
				return time.Duration(v) * time.Millisecond, nil
			case int32:
				return time.Duration(v) * time.Millisecond, nil
			case int64:
				return time.Duration(v) * time.Millisecond, nil
			case float64:
				return time.Duration(v) * time.Millisecond, nil
			}
			return data, nil
		}

		if c.DecodeHook == nil {
			c.DecodeHook = mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
				customHook,
			)
		} else {
			c.DecodeHook = mapstructure.ComposeDecodeHookFunc(
				c.DecodeHook,
				customHook,
			)
		}
	}
}
