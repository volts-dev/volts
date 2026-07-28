package config

import (
	"log"
	"path/filepath"
)

func WithFileName(fileName string) Option {
	return func(cfg *Config) {
		cfg.Core().FileName = fileName
	}
}

// 保存新配置数据
func WithConfig(model IConfig) Option {
	return func(cfg *Config) {
		if err := cfg.SaveFromModel(model); err != nil {
			cfg.Core().errorf("保存配置 %q 失败: %w", model.String(), err)
		}
	}
}

// 监听配置文件变动
//
// NOTE 此前直接用 core.fmt.v.WatchConfig()，绕过了 format 的读写锁：viper 会
// 起一条自己的协程去调 v.ReadInConfig()，与 SetConfigFile / 各类 Get 并发
// （-race 下 config 包必炸）。现改走 format.Watch，所有 viper 访问统一加锁。
func WithWatcher() Option {
	return func(cfg *Config) {
		core := cfg.Core()
		// 监视文件
		if core.FileName == "" {
			core.FileName = CONFIG_FILE_NAME //filepath.Join(AppPath, CONFIG_FILE_NAME)
		}

		path := filepath.Join(AppPath, core.FileName)
		core.fmt.SetConfigFile(path)

		if err := core.fmt.Watch(path, cfg.Reload); err != nil {
			log.Printf("config: 启动配置文件监听失败: %v", err)
		}
	}
}

// 当无配置文件时不自动创建配置文件
func WithNoAutoCreateFile() Option {
	return func(cfg *Config) {
		cfg.AutoCreateFile = false
	}
}
