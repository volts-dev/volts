package config

import (
	"os"
	"path/filepath"
	"testing"
)

type (
	fakeSelector interface{ Pick() string }

	// liveInstance 是"活着的实例"：非 nil 的接口字段。mapstructure 对它走
	// decodeBasic 的另一支，会把配置节的键写进这个正在被使用的对象里。
	liveInstance struct{ Addrs string }


	// ownedChildConfig 是「拥有的子组件」的配置，父结构体里以 field:"selector" 挂着。
	ownedChildConfig struct {
		Strategy string `field:"strategy"`
		Retries  int    `field:"retries"`
	}

	// ownerConfig 复刻 router.Config / server.Config 的真实形状：**同一个配置键**
	// 上既有一个子配置字段（field:"selector"），又有一个同名的实例字段
	// （Selector fakeSelector，蛇形名也是 selector）。设计上后者不是配置
	// （walk.go 的 isConfigType 排除接口），所以它不需要 field:"-"。
	ownerConfig struct {
		Name     string            `field:"-"`
		Addrs    string            `field:"addrs"`
		Selector fakeSelector      // 实例引用，不是配置
		Registry fakeSelector      // 同上；文件里若留着孤儿节 registry.* 也不许喂给它
		Child    *ownedChildConfig `field:"selector"`
	}
)

func (*liveInstance) Pick() string { return "" }

// 解码只能看 walk.go 认定的配置字段。
//
// decodeSection 曾直接把整节交给 mapstructure，而 mapstructure 只认标签/字段名、
// 不认「接口不是配置」这条规则：`selector` 这个键于是**同时**命中 Child
// （field:"selector"）和 Selector（蛇形名 selector），后者是接口，报
// "'Selector' expected type ... got unconvertible type 'map[string]interface {}'"。
//
// 后果不是少一个字段：整节解码返回 error，volts.New 把它记成「装载配置失败」。
// 2026-09-08 各服务启动时 router.default / server.http / server.rpc 三节全中，
// 而这三节恰好是唯一有 selector/registry/broker 子节的三节。
func TestDecodeSection_InstanceFieldDoesNotStealChildKey(t *testing.T) {
	fn := "instance_field_decode_test.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
		"owner": {
			"addrs": ":8080",
			"selector": {
				"strategy": "random",
				"retries": 5
			},
			"registry": {
				"addrs": "127.0.0.1:2379"
			}
		}
	}`)

	cfg := newIsolated(t, fn)
	// Registry 是**非 nil** 的实例引用 —— 这是生产里的常态（server.WithRegistry
	// 已经注入过了）。报 unconvertible 的只是 nil 的那一半；非 nil 的这一半此前是
	// 静默把 registry 那节写进活对象。
	live := &liveInstance{Addrs: "live"}
	owner := &ownerConfig{Name: "http", Addrs: ":80", Registry: live,
		Child: &ownedChildConfig{Strategy: "rr", Retries: 1}}
	cfg.Bind("owner", owner)

	if err := cfg.Errors(); err != nil {
		t.Fatalf("装载报错: %v", err)
	}
	if owner.Addrs != ":8080" {
		t.Errorf("Addrs = %q, want :8080", owner.Addrs)
	}
	if owner.Child.Strategy != "random" || owner.Child.Retries != 5 {
		t.Errorf("子节没解进来: %+v", owner.Child)
	}
	if owner.Selector != nil {
		t.Errorf("实例字段被解码写过: %v", owner.Selector)
	}
	if live.Addrs != "live" {
		t.Errorf("活着的实例被配置节改写了: Addrs=%q，want live", live.Addrs)
	}
}

// 同一个形状还把热重载整节堵死了。
//
// ReloadWithChanges 解码到 cloneConfig 造的副本，副本的实例字段一律是 nil ——
// 恰好落在旧解码器报 unconvertible 的那一支，而那一支是「保持原配置」。于是
// router.default 这一节的热重载**从来没有生效过**：文件改了，日志里多一条
// 「重载配置失败，保持原配置」，Recover / RequestPrinter 纹丝不动。
func TestReload_InstanceFieldDoesNotBlockTheWholeSection(t *testing.T) {
	fn := "instance_field_reload_test.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"owner2":{"addrs":":80","selector":{"strategy":"rr","retries":1},"registry":{"addrs":"a"}}}`)
	cfg := newIsolated(t, fn)

	owner := &ownerConfig{Name: "http", Registry: &liveInstance{Addrs: "live"},
		Child: &ownedChildConfig{}}
	cfg.Bind("owner2", owner)

	rewriteAndWatch(t, cfg, path, `{"owner2":{"addrs":":9090","selector":{"strategy":"rr","retries":1},"registry":{"addrs":"a"}}}`)
	changes := cfg.ReloadWithChanges()

	if len(changes) != 1 || owner.Addrs != ":9090" {
		t.Fatalf("热重载没落地: changes=%+v Addrs=%q", changes, owner.Addrs)
	}
}
