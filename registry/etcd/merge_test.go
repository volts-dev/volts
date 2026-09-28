package etcd

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// 滚动重启时同名同版本有新旧两条注册项：节点都要，metadata / 端点取最后注册的那条，
// 与 etcd 返回的顺序无关。
func TestMergeByNameVersion_NewestRegistrationWins(t *testing.T) {
	old := &registry.Service{Name: "sale", Version: "1.45",
		Metadata: map[string]string{"settings_fields": ""},
		Nodes:    []*registry.Node{{Id: "old", Address: "127.0.0.1:1"}}}
	cur := &registry.Service{Name: "sale", Version: "1.45",
		Metadata: map[string]string{"settings_fields": "[...]"},
		Nodes:    []*registry.Node{{Id: "new", Address: "127.0.0.1:2"}}}
	other := &registry.Service{Name: "account", Version: "1.0", Nodes: []*registry.Node{{Id: "a"}}}

	for _, order := range [][]revisioned{
		{{old, 10}, {cur, 20}, {other, 5}},
		{{cur, 20}, {other, 5}, {old, 10}},
	} {
		got := mergeByNameVersion(order)
		if len(got) != 2 || got[0].Name != "account" || got[1].Name != "sale" {
			t.Fatalf("合并结果: %+v", got)
		}
		sale := got[1]
		if sale.Metadata["settings_fields"] != "[...]" {
			t.Fatalf("应取最后注册那条的 metadata，得 %q", sale.Metadata["settings_fields"])
		}
		if len(sale.Nodes) != 2 {
			t.Fatalf("两条注册项的节点都要保留，得 %d 个", len(sale.Nodes))
		}
	}
	// 合并不能改到输入对象（它可能还在缓存里被别人读）。
	if len(old.Nodes) != 1 || len(cur.Nodes) != 1 {
		t.Fatal("合并改写了输入的注册项")
	}
}
