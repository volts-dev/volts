package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/transport"
)

func bound(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// TestConfigTree_ServerOwnsItsTransport 锁定服务器配置在配置树上的形状：
//
//   - server 是根节 server.<name>；
//   - 注入的传输层被**收编**：它自己先绑在 transport.default 并读到那一节的值，交给
//     server 后只认 server.<name>.transport —— 之后 transport.default 怎么改都影响不到它；
//   - 传输层实例用的正是 server 持有的那份配置结构体，文件里 server.<name>.transport.*
//     的值就落在它身上。
//
// 判据刻意不写成「transport.default 不在 Bound() 里」：server 装配时 broker 会借用进程
// 共享的 transport.Default()，那个实例合法地绑在 transport.default。要看的是注入的这个
// 实例还认不认那一节。
func TestConfigTree_ServerOwnsItsTransport(t *testing.T) {
	// 用一份独立文件驱动默认树，测完换回一份空文件，不把状态留给别的测试。
	dir := t.TempDir()
	file := filepath.Join(dir, "tree.json")
	load := func(content string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := config.Load(file); err != nil {
			t.Fatal(err)
		}
	}
	load(`{"transport":{"default":{"read_timeout":5000}},
	       "server":{"cfgtree":{"address":":0","transport":{"read_timeout":30000}}}}`)
	t.Cleanup(func() { load(`{}`) })

	tr := transport.NewHTTPTransport()
	if got := tr.Config().ReadTimeout; got != 5*time.Second {
		t.Fatalf("前置条件：独立创建的传输层应先读到 transport.default: %v", got)
	}

	srv := New(Name("cfgtree"), WithTransport(tr))
	cfg := srv.Config()

	paths := config.Bound()
	if !bound(paths, "server.cfgtree") {
		t.Errorf("server 应绑在 server.cfgtree: %v", paths)
	}
	if cfg.TransportConfig != tr.Config() {
		t.Error("server 持有的传输层配置应就是实例自己那份")
	}
	if got := tr.Config().ReadTimeout; got != 30*time.Second {
		t.Errorf("文件里 server.cfgtree.transport.read_timeout 没有落到注入的传输层上: %v", got)
	}
	if o := config.Default().Origin("server.cfgtree.transport.read_timeout"); o.Source != config.SourceFile {
		t.Errorf("来源应为文件: %v", o)
	}

	// 收编之后，transport.default 那一节再怎么改都与这个实例无关
	load(`{"transport":{"default":{"read_timeout":7000}}}`)
	if got := tr.Config().ReadTimeout; got != 30*time.Second {
		t.Errorf("被收编的传输层仍在响应 transport.default（旧根节没有摘掉）: %v", got)
	}
	// 共享的 router 是自己的根节，不在 server 下面
	if !bound(paths, "router.default") {
		t.Errorf("router 应是独立根节 router.default: %v", paths)
	}
	for _, p := range paths {
		if p == "server.cfgtree.router" || p == "server.cfgtree.registry" || p == "server.cfgtree.broker" {
			t.Errorf("共享实例不该挂在 server 下: %s", p)
		}
	}
}

// TestConfigTree_DefaultTransportIsOwnedToo 锁定：没注入传输层时，server 自建的那份同样
// 挂在 server.<name>.transport，且实例用的是 server 持有的配置。
func TestConfigTree_DefaultTransportIsOwnedToo(t *testing.T) {
	srv := New(Name("cfgtree_default"))
	cfg := srv.Config()
	if cfg.Transport == nil || cfg.TransportConfig != cfg.Transport.Config() {
		t.Fatal("自建传输层应使用 server 持有的配置结构体")
	}
	if o := config.Default().Origin("server.cfgtree_default.transport.read_timeout"); o.Source != config.SourceDefault {
		t.Errorf("传输层默认值应落在 server.cfgtree_default.transport.*: %v", o)
	}
	if bound(config.Bound(), "transport.default") {
		// 只有别的测试留下的 transport.Default() 才会绑在这里；本 server 不该新增它
		t.Logf("transport.default 存在（来自进程共享的 Default()），与本测试无关")
	}
}
