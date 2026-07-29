package server

import (
	"errors"
	"testing"

	"github.com/volts-dev/volts/config"
)

// TestConfig_OptsOutOfHotReload 锁定：服务器配置退出热重载。
//
// Address 与 listener 同理，进程启动时就已绑好端口；RegisterTTL / RegisterInterval
// 由后台注册协程按自己的节奏读取，热更等于在跑着的 ticker 底下换参数。
func TestConfig_OptsOutOfHotReload(t *testing.T) {
	var cfg any = &Config{}

	r, ok := cfg.(config.IReloadable)
	if !ok {
		t.Fatal("server.Config 应实现 config.IReloadable")
	}
	if err := r.OnReload(&Config{}); !errors.Is(err, config.ErrReloadUnsupported) {
		t.Errorf("应返回 ErrReloadUnsupported, got %v", err)
	}
}
