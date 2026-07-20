package consul

import (
	"encoding/json"
	"testing"

	"github.com/volts-dev/volts/registry"
)

func TestEncodingEndpoints(t *testing.T) {
	eps := []*registry.Endpoint{
		{
			Name: "endpoint1",
			Request: &registry.Value{
				Name: "request",
				Type: "request",
			},
			Response: &registry.Value{
				Name: "response",
				Type: "response",
			},
			Metadata: map[string]string{
				"foo1": "bar1",
			},
		},
		{
			Name: "endpoint2",
			Request: &registry.Value{
				Name: "request",
				Type: "request",
			},
			Response: &registry.Value{
				Name: "response",
				Type: "response",
			},
			Metadata: map[string]string{
				"foo2": "bar2",
			},
		},
		{
			Name: "endpoint3",
			Request: &registry.Value{
				Name: "request",
				Type: "request",
			},
			Response: &registry.Value{
				Name: "response",
				Type: "response",
			},
			Metadata: map[string]string{
				"foo3": "bar3",
			},
		},
	}

	testEp := func(ep *registry.Endpoint, enc string) {
		// encode endpoint
		e := encodeEndpoints([]*registry.Endpoint{ep})

		// check there are two tags; old and new
		if len(e) != 1 {
			t.Fatalf("Expected 1 encoded tags, got %v", e)
		}

		// check old encoding
		var seen bool

		for _, en := range e {
			if en == enc {
				seen = true
				break
			}
		}

		if !seen {
			t.Fatalf("Expected %s but not found", enc)
		}

		// decode
		d := decodeEndpoints([]string{enc})
		if len(d) == 0 {
			t.Fatalf("Expected %v got %v", ep, d)
		}

		// check name
		if d[0].Name != ep.Name {
			t.Fatalf("Expected ep %s got %s", ep.Name, d[0].Name)
		}

		// check all the metadata exists
		for k, v := range ep.Metadata {
			if gv := d[0].Metadata[k]; gv != v {
				t.Fatalf("Expected key %s val %s got val %s", k, v, gv)
			}
		}
	}

	for _, ep := range eps {
		// JSON encoded
		jencoded, err := json.Marshal(ep)
		if err != nil {
			t.Fatal(err)
		}

		// HEX encoded
		hencoded := encode(jencoded)
		// endpoint tag
		hepTag := "e-" + hencoded
		testEp(ep, hepTag)
	}
}

// 版本标签编解码。
//
// NOTE 这里**不断言精确的编码字节**。原测试把 zlib 输出写死成
// "v-789c32d433d03300040000ffff02ce00ee"，而 Go 的 flate 实现对极短输入改用了
// stored block（实际输出变成 "v-789c000500faff312e302e30030002ce00ee"），
// 于是测试随 Go 版本升级而失败 —— 但编码本身并没有坏，往返完全正确。
//
// 真正需要钉死的性质有两条，下面分别断言：
//  1. 往返一致：encode→decode 拿回原值
//  2. 向后兼容：**旧版本写进 Consul 的标签必须仍能解出来**。这条才是关键 ——
//     标签持久化在注册中心里，滚动升级期间新旧版本会同时读到对方写的标签。
func TestEncodingVersion(t *testing.T) {
	testData := []struct {
		decoded string
		// legacyEncoded 是历史版本产生的编码，用于验证向后兼容
		legacyEncoded string
	}{
		{"1.0.0", "v-789c32d433d03300040000ffff02ce00ee"},
		{"latest", "v-789cca492c492d2e01040000ffff08cc028e"},
	}

	for _, data := range testData {
		// 1) 往返一致
		e := encodeVersion(data.decoded)

		d, ok := decodeVersion(e)
		if !ok {
			t.Fatalf("decodeVersion(%v) 返回 ok=false", e)
		}
		if d != data.decoded {
			t.Fatalf("往返后 = %q, want %q", d, data.decoded)
		}

		// 2) 旧编码仍可解出
		d, ok = decodeVersion([]string{data.legacyEncoded})
		if !ok {
			t.Fatalf("旧编码 %s 解不出来了 —— 滚动升级时会读不懂存量标签", data.legacyEncoded)
		}
		if d != data.decoded {
			t.Fatalf("旧编码解出 %q, want %q", d, data.decoded)
		}
	}
}

// 明文的更老格式（v=1.0.0）也必须继续兼容。
func TestDecodeVersionLegacyPlain(t *testing.T) {
	d, ok := decodeVersion([]string{"v=1.0.0"})
	if !ok || d != "1.0.0" {
		t.Fatalf("明文旧格式解析失败: d=%q ok=%v", d, ok)
	}
}
