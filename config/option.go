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
		cfg.fileName = fileName
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

func WithWatcher() Option {
	return func(cfg *Config) {
		// 监视文件
		if cfg.fileName == "" {
			cfg.fileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
		}
		cfg.fmt.v.SetConfigFile(filepath.Join(AppPath, cfg.fileName))
		cfg.fmt.v.WatchConfig()
		cfg.fmt.v.OnConfigChange(func(e fsnotify.Event) {
			if e.Op == fsnotify.Write {
				// 重新加载配置
				cfg.models.Range(func(key any, value any) bool {
					v := value.(IConfig)
					err := v.Load()
					if err != nil {
						log.Fatalf("reload %v config failed!", key)
					}
					return true
				})
			}
		})
	}
}
