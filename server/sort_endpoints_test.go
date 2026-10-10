package server

import (
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/volts-dev/volts/registry"
)

// 同一份端点集不论以什么顺序收集，排完之后的字节必须相同。
//
// 注册项签名按切片顺序算摘要；顺序一变签名就变，etcd 后端的「内容未变则跳过」随之
// 失效，每个服务每 30 秒整条重写一次（见 sortEndpoints 的注释）。收集顺序来自
// TTree.Endpoints() 的按方法分桶 map 和同名多 group 的 map，两处都是随机的。
func TestSortEndpoints_OrderIndependentOfCollection(t *testing.T) {
	base := []*registry.Endpoint{
		{Name: "/web/login", Path: "/web/login", Method: []string{"GET"}},
		{Name: "/web/login", Path: "/web/login", Method: []string{"POST"}},
		{Name: "/shop/confirm_order", Path: "/shop/confirm_order", Method: []string{"POST"}},
		{Name: "/sale.order.read", Path: "/sale.order.read", Method: []string{"CONNECT"}},
		{Name: "/a", Path: "/a", Method: []string{"GET"}, Host: []string{"b.example"}},
		{Name: "/a", Path: "/a", Method: []string{"GET"}, Host: []string{"a.example"}},
		{Name: "Sub.Topic", Path: ""},
		{Name: "Another.Topic", Path: ""},
	}

	want := ""
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		eps := append([]*registry.Endpoint(nil), base...)
		rng.Shuffle(len(eps), func(a, b int) { eps[a], eps[b] = eps[b], eps[a] })
		b, _ := json.Marshal(sortEndpoints(eps))
		if i == 0 {
			want = string(b)
			continue
		}
		if string(b) != want {
			t.Fatalf("第 %d 次打乱后排出的顺序不同:\n%s\nvs\n%s", i, b, want)
		}
	}
}
