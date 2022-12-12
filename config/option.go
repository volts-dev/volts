package config

import (
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

func WithPrefix(name string) Option {
	return func(cfg *Config) {
		cfg.Prefix = name
	}
}

func WithFileName(fileName string) Option {
	return func(cfg *Config) {
		cfg.FileName = fileName
	}
}

// 保存新配置数据
func WithConfig(model IConfig) Option {
	return func(cfg *Config) {
		err := cfg.SaveFromModel(model)
		if err != nil {
			log.Fatal(err)
		}
	}
}

// 监听配置文件变动
func WithWatcher() Option {
	return func(cfg *Config) {
		// 监视文件
		if cfg.FileName == "" {
			cfg.FileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
		}
		cfg.fmt.v.SetConfigFile(filepath.Join(AppPath, cfg.FileName))
		cfg.fmt.v.WatchConfig()
		cfg.fmt.v.OnConfigChange(func(e fsnotify.Event) {
			if e.Op == fsnotify.Write {
				cfg.Reload() // 重新加载配置
			}
		})
	}
}

// 当无配置文件时不自动创建配置文件
func WithNoAutoCreateFile() Option {
	return func(cfg *Config) {
		cfg.CreateFile = false
	}
}
