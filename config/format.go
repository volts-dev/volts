package config

import (
	"path/filepath"
	"reflect"
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

		// watchOnce / watchStop 管理配置文件监听协程。
		watchOnce sync.Once
		watchStop chan struct{}
	}
)

func newFormat() *format {
	file := viper.New()
	//file.SetConfigFile(CONFIG_FILE_NAME)
	return &format{
		v:         file,
		watchStop: make(chan struct{}),
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

func (self *format) Unset(key string) {
	self.Lock()
	defer self.Unlock()
	newViper := viper.New()
	for k, v := range self.v.AllSettings() {
		if k != key {
			newViper.Set(k, v)
		}
	}
	self.v = newViper
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

func (self *format) WriteConfig() error {
	self.Lock()
	defer self.Unlock()
	return self.v.WriteConfig()
}

func (self *format) GetBool(key string, defaultValue bool) bool {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetBool(key)
}

// GetString from default namespace
func (self *format) GetString(key, defaultValue string) string {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	return self.v.GetString(key)
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

func (self *format) SetValue(key string, value interface{}) {
	self.Lock()
	self.v.Set(key, value)
	self.Unlock()
}

func (self *format) SetDefault(key string, value interface{}) {
	self.Lock()
	self.v.SetDefault(key, value)
	self.Unlock()
}

func (self *format) Delete(key string) {
	self.Lock()
	self.v.Set(key, nil)
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
