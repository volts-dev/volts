package selector

import (
	"testing"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

// fakeTransport 只需要 String() 参与过滤。内嵌 ITransport（nil）以满足接口，
// 其余方法不会被调用 —— 真被调到会 nil panic，正好暴露"过滤器不该碰的东西"。
type fakeTransport struct {
	transport.ITransport
	name string
}

func (f fakeTransport) String() string { return f.name }

func svcWithTransport(name, version string, nodes map[string]string) *registry.Service {
	s := &registry.Service{Name: name, Version: version}
	for id, tp := range nodes {
		s.Nodes = append(s.Nodes, &registry.Node{
			Id:       id,
			Metadata: map[string]string{"transport": tp},
		})
	}
	return s
}

// 节点不得跨 service 累积。
//
// 原实现把 `var nodes []*registry.Node` 声明在外层循环之外，于是第 2 个
// service 拿到的是「第 1 个 service 的节点 + 自己的节点」。
// registry.GetService(name) 是按版本各返回一个 *Service 的，实际后果就是
// **新版本声称自己拥有旧版本的节点** —— 打给 v1.1 的请求会落到 v1.0 上。
// 本过滤器挂在每一次 RPC / HTTP 客户端调用上。
func TestFilterTrasportDoesNotLeakNodesAcrossServices(t *testing.T) {
	tp := fakeTransport{name: "TcpTransport"}

	services := []*registry.Service{
		svcWithTransport("svc", "1.0", map[string]string{"v1-a": "TcpTransport"}),
		svcWithTransport("svc", "1.1", map[string]string{"v11-a": "TcpTransport"}),
	}

	out := FilterTrasport(tp)(services)

	if len(out) != 2 {
		t.Fatalf("service 数 = %d, want 2", len(out))
	}

	for _, s := range out {
		if len(s.Nodes) != 1 {
			t.Errorf("service %s 有 %d 个节点，want 1 —— 节点跨 service 泄漏了: %v",
				s.Version, len(s.Nodes), nodeIds([]*registry.Service{s}))
		}
	}

	// 精确断言归属
	byVersion := map[string][]string{}
	for _, s := range out {
		byVersion[s.Version] = nodeIds([]*registry.Service{s})
	}
	if got := byVersion["1.1"]; len(got) != 1 || got[0] != "v11-a" {
		t.Errorf("v1.1 的节点 = %v, want [v11-a]", got)
	}
}

// 只保留匹配当前传输的节点。
func TestFilterTrasportKeepsOnlyMatching(t *testing.T) {
	tp := fakeTransport{name: "TcpTransport"}

	services := []*registry.Service{
		svcWithTransport("svc", "1.0", map[string]string{
			"tcp-1":  "TcpTransport",
			"http-1": "HttpTransport",
		}),
	}

	out := FilterTrasport(tp)(services)

	ids := nodeIds(out)
	if len(ids) != 1 || ids[0] != "tcp-1" {
		t.Fatalf("剩余节点 = %v, want [tcp-1]", ids)
	}
}

// 过滤后无节点的 service 必须被剔除，让 Select 如实报 ErrNoneAvailable，
// 而不是给出一个根本不在该传输上的节点。
func TestFilterTrasportDropsEmptyServices(t *testing.T) {
	tp := fakeTransport{name: "TcpTransport"}

	services := []*registry.Service{
		svcWithTransport("svc", "1.0", map[string]string{"http-1": "HttpTransport"}),
	}

	out := FilterTrasport(tp)(services)

	if len(out) != 0 {
		t.Fatalf("无匹配节点的 service 应被剔除，得到 %d 个: %v", len(out), nodeIds(out))
	}
}

// 不得就地改写入参 —— 过滤器不该假设上游给了它可写的数据。
func TestFilterTrasportDoesNotMutateInput(t *testing.T) {
	tp := fakeTransport{name: "TcpTransport"}

	in := []*registry.Service{
		svcWithTransport("svc", "1.0", map[string]string{
			"tcp-1":  "TcpTransport",
			"http-1": "HttpTransport",
		}),
	}

	FilterTrasport(tp)(in)

	if len(in[0].Nodes) != 2 {
		t.Fatalf("入参被就地改写：节点数 = %d, want 2", len(in[0].Nodes))
	}
}

// 节点没有 metadata 时不得 panic。
func TestFilterTrasportNilMetadata(t *testing.T) {
	tp := fakeTransport{name: "TcpTransport"}

	services := []*registry.Service{
		{Name: "svc", Nodes: []*registry.Node{{Id: "no-meta"}}},
	}

	out := FilterTrasport(tp)(services)
	if len(out) != 0 {
		t.Fatalf("无 metadata 的节点不应匹配任何传输，得到 %v", nodeIds(out))
	}
}
