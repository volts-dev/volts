package etcd

import (
	"testing"
	"time"

	"github.com/volts-dev/volts/registry"
)

// 亚秒级超时必须被抬到 defaultTimeout。
//
// 这不是防御性编程，是修一个真实故障：本机单节点 etcd 拉一次全量服务列表实测
// 77~130ms，而历史默认 100ms 正好压在这条线上。踩线时 GetService/ListServices
// 返回 context deadline exceeded，registryCacher 在没有旧缓存可回退时把这个原始
// 错误原样透出（它不是 *errors.Error，带不出状态码），上层网关一律压成 HTTP 500 ——
// 现象就是接口间歇性 500，报文里写着 "context deadline exceeded"。
//
// 兜底此前只认 Timeout==0，那个哨兵假设「没配过就是 0」；但 registry.NewConfig
// 的默认值会被 config.Register 持久化进配置文件，且每个内嵌 registry 的组件都留
// 一份自己前缀下的副本，于是 Timeout 恒非 0，兜底永不触发。改按下限判定后，存量
// 配置也能自愈。
func TestConfigureClampsSubSecondTimeout(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"未设置(0)走默认", 0, defaultTimeout},
		{"★ 历史默认 100ms 必须被抬起", 100 * time.Millisecond, defaultTimeout},
		{"差一毫秒到下限", 999 * time.Millisecond, defaultTimeout},
		{"负值按未设置处理", -1 * time.Second, defaultTimeout},
		{"正好等于下限则保留", minTimeout, minTimeout},
		{"合理的显式配置不被改写", 30 * time.Second, 30 * time.Second},
	}

	for _, c := range cases {
		r, ok := New(
			registry.Addrs("127.0.0.1:2379"),
			registry.Timeout(c.in),
		).(*etcdRegistry)
		if !ok {
			t.Fatal("New 未返回 *etcdRegistry")
		}

		if got := r.config.Timeout; got != c.want {
			t.Errorf("%s: Timeout(%v) → %v，期望 %v", c.name, c.in, got, c.want)
		}
	}
}
