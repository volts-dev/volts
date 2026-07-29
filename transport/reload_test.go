package transport

import (
	"errors"
	"testing"

	"github.com/volts-dev/volts/config"
)

// TestConfig_OptsOutOfHotReload 锁定：传输层配置退出热重载。
//
// 这一节里几乎每项改了都要重启才生效（Addrs / TlsConfig / Secure / EnableACME 决定
// listener 怎么建，而 listener 启动时就建好了）。ReadTimeout / WriteTimeout 理论上
// 可热更，但它们是全框架被读得最多的配置字段（生产代码 19 处），热更收益是「改超时
// 不用重启」，代价是那 19 个读点每个都与重载写入竞态。整节退出后竞态归零。
//
// 若哪天决定让这一节重新参与热重载，必须先把那些读点改成快照/accessor，
// 否则就是把已经消除的竞态又请回来。
func TestConfig_OptsOutOfHotReload(t *testing.T) {
	var cfg any = &Config{}

	r, ok := cfg.(config.IReloadable)
	if !ok {
		t.Fatal("transport.Config 应实现 config.IReloadable")
	}
	if err := r.OnReload(&Config{}); !errors.Is(err, config.ErrReloadUnsupported) {
		t.Errorf("应返回 ErrReloadUnsupported, got %v", err)
	}
}
