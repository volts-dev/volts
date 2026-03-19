package config

import (
	"reflect"
	"sync"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"github.com/volts-dev/utils"
)

type (
	format struct {
		sync.RWMutex
		v *viper.Viper
	}
)

func newFormat() *format {
	file := viper.New()
	//file.SetConfigFile(CONFIG_FILE_NAME)
	return &format{
		v: file,
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

func (self *format) GetDuration(key string, defaultValue time.Duration) time.Duration {
	self.RLock()
	defer self.RUnlock()
	if !self.v.IsSet(key) {
		return defaultValue
	}
	val := self.v.GetInt64(key)
	return time.Duration(val)
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
