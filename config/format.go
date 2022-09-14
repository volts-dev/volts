package config

import (
	"time"

	"github.com/spf13/viper"
)

type (
	format struct {
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
	self.SetDefault(key, defaultValue)
	return self.v.GetBool(key)
}

// GetStringValue from default namespace
func (self *format) GetString(key, defaultValue string) string {
	val := self.v.GetString(key)
	if val == "" {
		return defaultValue
	}
	return val
}

// GetIntValue from default namespace
func (self *format) GetInt(key string, defaultValue int) int {
	val := self.v.GetInt(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetInt32(key string, defaultValue int32) int32 {
	val := self.v.GetInt32(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetInt64(key string, defaultValue int64) int64 {
	val := self.v.GetInt64(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetIntSlice(key string, defaultValue []int) []int {
	val := self.v.GetIntSlice(key)
	if len(val) == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetTime(key string, defaultValue time.Time) time.Time {
	self.SetDefault(key, defaultValue)
	return self.v.GetTime(key)
}

func (self *format) GetDuration(key string, defaultValue time.Duration) time.Duration {
	val := self.v.GetDuration(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) GetFloat64(key string, defaultValue float64) float64 {
	val := self.v.GetFloat64(key)
	if val == 0 {
		return defaultValue
	}
	return val
}

func (self *format) Unmarshal(rawVal interface{}) error {
	return self.v.Unmarshal(rawVal)
}

// key 可以整个config结构
func (self *format) UnmarshalKey(key string, rawVal interface{}) error {
	return self.v.UnmarshalKey(key, rawVal)
}

func (self *format) SetValue(key string, value interface{}) {
	self.v.Set(key, value)
}

func (self *format) SetDefault(key string, value interface{}) {
	self.v.SetDefault(key, value)
}
