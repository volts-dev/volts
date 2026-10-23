package consul

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	consul "github.com/hashicorp/consul/api"
	"github.com/volts-dev/volts/registry"
)

// fakeAgent 记下 agent 收到的注册与注销。
type fakeAgent struct {
	mu           sync.Mutex
	registered   []consul.AgentServiceRegistration
	deregistered []string
}

func newFakeAgentRegistry(t *testing.T) (*consulRegistry, *fakeAgent) {
	fa := &fakeAgent{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/agent/service/register":
			var asr consul.AgentServiceRegistration
			if err := json.NewDecoder(r.Body).Decode(&asr); err != nil {
				t.Errorf("decode register: %v", err)
			}
			fa.registered = append(fa.registered, asr)
		case strings.HasPrefix(r.URL.Path, "/v1/agent/service/deregister/"):
			fa.deregistered = append(fa.deregistered, strings.TrimPrefix(r.URL.Path, "/v1/agent/service/deregister/"))
		}
		w.WriteHeader(200)
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)

	cfg := consul.DefaultConfig()
	cfg.Address = strings.TrimPrefix(srv.URL, "http://")
	cr := &consulRegistry{
		config:       cfg,
		Address:      []string{cfg.Address},
		opts:         &registry.Config{},
		register:     make(map[string]uint64),
		lastChecked:  make(map[string]time.Time),
		queryOptions: &consul.QueryOptions{AllowStale: true},
	}
	cr.Client()
	return cr, fa
}

// 一个进程的所有服务共用一个节点 id。consul 的服务 id 必须按 (服务, 节点) 区分，否则后注册的
// 服务把先注册的覆盖掉、注销一个就把整个进程摘了。服务级 metadata（描述哈希在里面）要写进去。
func TestConsul_ServicesSharingANodeGetDistinctIDs(t *testing.T) {
	cr, fa := newFakeAgentRegistry(t)
	node := &registry.Node{Id: "rpc-1", Address: "127.0.0.1:4000"}
	for _, name := range []string{"sale", "stock"} {
		s := &registry.Service{
			Name:     name,
			Version:  "1",
			Metadata: map[string]string{registry.MetaDescriptor: "digest-" + name},
			Nodes:    []*registry.Node{node},
		}
		if err := cr.Register(s); err != nil {
			t.Fatal(err)
		}
	}

	fa.mu.Lock()
	if len(fa.registered) != 2 || fa.registered[0].ID == fa.registered[1].ID {
		t.Fatalf("两个服务的 consul 服务 id 应不同：%+v", fa.registered)
	}
	for _, asr := range fa.registered {
		if got := nodeIDOf(asr.Name, asr.ID); got != node.Id {
			t.Errorf("服务 id %q 还原不出节点 id：%q", asr.ID, got)
		}
		md := decodeServiceMetadata(asr.Tags)
		if md[registry.MetaDescriptor] != "digest-"+asr.Name {
			t.Errorf("%s 的服务级 metadata 没写进 consul：%v", asr.Name, md)
		}
	}
	fa.mu.Unlock()

	if err := cr.Deregister(&registry.Service{Name: "sale", Version: "1", Nodes: []*registry.Node{node}}); err != nil {
		t.Fatal(err)
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.deregistered) != 1 || fa.deregistered[0] != serviceID("sale", node.Id) {
		t.Fatalf("注销 sale 应只摘 %q，实际 %v", serviceID("sale", node.Id), fa.deregistered)
	}
}

// 读：服务级 metadata 读回来；同版本、描述哈希不同的两个实例分成两个 Service；节点 id 去掉服务名前缀。
func TestConsul_GetService_SplitsByDescriptorAndRestoresNodeID(t *testing.T) {
	entry := func(nodeID, digest string) *consul.ServiceEntry {
		e := newServiceEntry(nodeID, "127.0.0.1", "sale", "1", nil)
		e.Service.ID = serviceID("sale", nodeID)
		e.Service.Port = 4000
		e.Service.Tags = append(e.Service.Tags, encodeServiceMetadata(map[string]string{registry.MetaDescriptor: digest})...)
		return e
	}
	cr, cl := newConsulTestRegistry(&mockRegistry{
		status: 200,
		body:   newServiceList([]*consul.ServiceEntry{entry("a", "d1"), entry("b", "d1"), entry("c", "d2")}),
		url:    "/v1/health/service/sale",
	})
	defer cl()

	svcs, err := cr.GetService("sale")
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 {
		t.Fatalf("描述哈希不同应分成 2 个 Service，得到 %d 个", len(svcs))
	}
	nodes := map[string]string{}
	for _, s := range svcs {
		for _, n := range s.Nodes {
			nodes[n.Id] = s.Metadata[registry.MetaDescriptor]
		}
	}
	want := map[string]string{"a": "d1", "b": "d1", "c": "d2"}
	for id, d := range want {
		if nodes[id] != d {
			t.Errorf("节点 %s：哈希 %q，想要 %q（全部：%v）", id, nodes[id], d, nodes)
		}
	}
}
