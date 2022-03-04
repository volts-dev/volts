package config

import (
	"time"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
)

var log logger.ILogger = logger.NewLogger(logger.WithPrefix("Config"))

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
		fmt  *format
		Mode ModeType
	}
)

func init() {
	AppFilePath = utils.AppFilePath()
	AppPath = utils.AppPath()
	AppDir = utils.AppDir()
}

func New() *Config {
	return &Config{
		fmt:  newFormat(),
		Mode: MODE_NORMAL,
	}
}

func Default() *Config {
	return defaultConfig
}

func (self *Config) Load(fileName ...string) error {
	if fileName != nil {
		self.fmt.v.SetConfigFile(fileName[0])
	} else {
		self.fmt.v.SetConfigFile(CONFIG_FILE_NAME)
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

func (self *Config) GetBool(key string) bool {
	return self.fmt.GetBool(key)
}

// GetStringValue from default namespace
func (self *Config) GetString(key, defaultValue string) string {
	return self.fmt.GetString(key, defaultValue)
}

// GetIntValue from default namespace
func (self *Config) GetInt(key string, defaultValue int) int {
	return self.fmt.GetInt(key, defaultValue)
}

func (self *Config) GetInt32(key string, defaultValue int32) int32 {
	return self.fmt.GetInt32(key, defaultValue)
}

func (self *Config) GetInt64(key string, defaultValue int64) int64 {
	return self.fmt.GetInt64(key, defaultValue)
}

func (self *Config) GetIntSlice(key string, defaultValue []int) []int {
	return self.fmt.GetIntSlice(key, defaultValue)
}

func (self *Config) GetTime(key string) time.Time {
	return self.fmt.GetTime(key)
}

func (self *Config) GetFloat64(key string) float64 {
	return self.fmt.GetFloat64(key)
}

func (self *Config) SetValue(key string, value interface{}) {
	self.fmt.SetValue(key, value)
}
