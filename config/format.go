package config

import (
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
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
	self.SetDefault(key, defaultValue)
	return self.v.GetBool(key)
}

// GetStringValue from default namespace
func (self *format) GetString(key, defaultValue string) string {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetString(key)
	if val == "" {
		return defaultValue
	}
	return val
}

// GetIntValue from default namespace
func (self *format) GetInt(key string, defaultValue int) int {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetInt(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetInt32(key string, defaultValue int32) int32 {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetInt32(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetInt64(key string, defaultValue int64) int64 {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetInt64(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetIntSlice(key string, defaultValue []int) []int {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetIntSlice(key)
	if len(val) == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetTime(key string, defaultValue time.Time) time.Time {
	self.RLock()
	defer self.RUnlock()
	self.SetDefault(key, defaultValue)
	return self.v.GetTime(key)
}

func (self *format) GetDuration(key string, defaultValue time.Duration) time.Duration {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetDuration(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetFloat64(key string, defaultValue float64) float64 {
	self.RLock()
	defer self.RUnlock()
	val := self.v.GetFloat64(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) Unmarshal(rawVal interface{}) error {
	self.Lock()
	defer self.Unlock()
	return self.v.Unmarshal(rawVal, withTagName("field"), withMatchName())
}

// key 可以整个config结构
func (self *format) UnmarshalKey(key string, rawVal interface{}) error {
	self.Lock()
	defer self.Unlock()
	return self.v.UnmarshalKey(key, rawVal, withTagName("field"), withMatchName())
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
