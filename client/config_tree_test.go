package client

import (
	"testing"

	"github.com/volts-dev/volts/config"
)

// TestConfigTree_ClientOwnsTransportAndSelector 锁定客户端配置在配置树上的形状：
// client.<name> 是根节，它拥有的传输层与选择器分别是 client.<name>.transport 与
// client.<name>.selector，两者都不再是独立根节。
func TestConfigTree_ClientOwnsTransportAndSelector(t *testing.T) {
	cli, err := NewHttpClient(WithName("cfgtree"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := cli.Config()

	var hasClient bool
	for _, p := range config.Bound() {
		switch p {
		case "client.cfgtree":
			hasClient = true
		case "selector.default":
			t.Errorf("被拥有的选择器不该是独立根节: %v", config.Bound())
		}
	}
	if !hasClient {
		t.Fatalf("客户端应绑在 client.cfgtree: %v", config.Bound())
	}
	if cfg.TransportConfig == nil || cfg.TransportConfig != cfg.Transport.Config() {
		t.Error("客户端持有的传输层配置应就是实例自己那份")
	}
	if cfg.SelectorConfig == nil || cfg.Selector == nil {
		t.Fatal("选择器及其配置都应就位")
	}
	for _, path := range []string{
		"client.cfgtree.transport.read_timeout",
		"client.cfgtree.selector.breaker_failures",
		"client.cfgtree.pool_size",
	} {
		if o := config.Default().Origin(path); o.Source != config.SourceDefault {
			t.Errorf("%s 的默认值没有沿路径落进树里: %v", path, o.Source)
		}
	}
}
