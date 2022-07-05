package config

import (
	"path/filepath"
	"time"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/logger"
)

var log = logger.New("Config")

var (
	// App settings.
	AppVer        string
	AppName       string
	AppUrl        string
	AppSubUrl     string
	AppPath       string
	AppFilePath   string
	AppDir        string
	defaultConfig = New()
)

type (
	Config struct {
		fmt         *format
		Mode        ModeType
		AppFilePath string
		AppPath     string
		AppDir      string
	}
)

func init() {
	AppFilePath = utils.AppFilePath()
	AppPath = utils.AppPath()
	AppDir = utils.AppDir()
}

func New() *Config {
	return &Config{
		fmt:         newFormat(),
		Mode:        MODE_NORMAL,
		AppFilePath: AppFilePath,
		AppPath:     AppPath,
		AppDir:      AppDir,
	}
}

func Default() *Config {
	return defaultConfig
}

// default is CONFIG_FILE_NAME = "config.json"
func (self *Config) Load(fileName ...string) error {
	if fileName != nil {
		self.fmt.v.SetConfigFile(filepath.Join(AppPath, fileName[0]))
	} else {
		self.fmt.v.SetConfigFile(filepath.Join(AppPath, CONFIG_FILE_NAME))
	}
	err := self.fmt.v.ReadInConfig() // Find and read the config file
	if err != nil {                  // Handle errors reading the config file
		return err
	}

	return nil
}

func (self *Config) Save() error {
	if err := self.fmt.v.WriteConfig(); err != nil {
		return err
	}

	return nil
}

func (self *Config) GetBool(field string, defaultValue bool) bool {
	return self.fmt.GetBool(field, defaultValue)
}

// GetStringValue from default namespace
func (self *Config) GetString(field, defaultValue string) string {
	return self.fmt.GetString(field, defaultValue)
}

// GetIntValue from default namespace
func (self *Config) GetInt(field string, defaultValue int) int {
	return self.fmt.GetInt(field, defaultValue)
}

func (self *Config) GetInt32(field string, defaultValue int32) int32 {
	return self.fmt.GetInt32(field, defaultValue)
}

func (self *Config) GetInt64(field string, defaultValue int64) int64 {
	return self.fmt.GetInt64(field, defaultValue)
}

func (self *Config) GetIntSlice(field string, defaultValue []int) []int {
	return self.fmt.GetIntSlice(field, defaultValue)
}

func (self *Config) GetTime(field string, defaultValue time.Time) time.Time {
	return self.fmt.GetTime(field, defaultValue)
}

func (self *Config) GetDuration(field string, defaultValue time.Duration) time.Duration {
	return self.fmt.GetDuration(field, defaultValue)
}

func (self *Config) GetFloat64(field string, defaultValue float64) float64 {
	return self.fmt.GetFloat64(field, defaultValue)
}

func (self *Config) SetValue(field string, value interface{}) {
	self.fmt.SetValue(field, value)
}

func (self *Config) Unmarshal(rawVal interface{}) error {
	return self.fmt.Unmarshal(rawVal)
}

// 反序列字段映射到数据类型
func (self *Config) UnmarshalField(field string, rawVal interface{}) error {
	return self.fmt.UnmarshalKey(field, rawVal)
}
