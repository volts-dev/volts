package client

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/registry"
)

// 向别的进程按哈希取服务描述（registry/descriptor.go，设计见 docs/registry_descriptor_design.md）。
//
// registry 不能依赖 client（client 依赖 registry），所以取法由这里在 init 里注入。凡是带服务器
// 或客户端的进程都链接了本包。

func init() { registry.SetDescriptorSource(fetchDescriptors) }

// tcpProtocol 是 TCP 节点的 metadata["protocol"]（server 按 Transport.Protocol() 写）。
const tcpProtocol = "TCP"

var (
	describeOnce sync.Once
	describeCli  *RpcClient
)

// describeClient 是取描述专用的客户端：不走服务发现（逐个节点按地址打），所以给它一个
// 空注册中心，免得为了取描述反过来去解析注册中心。
func describeClient() *RpcClient {
	describeOnce.Do(func() {
		describeCli = NewRpcClient(
			WithName("registry_describe"),
			WithRegistry(registry.NewNopRegistry()),
			WithSerializeType(codec.JSON),
		)
	})
	return describeCli
}

// fetchDescriptors 依次问能答描述的节点（TCP），直到要的都拿到或节点问完。可以只回一部分。
func fetchDescriptors(ctx context.Context, digests []string, nodes []*registry.Node) (map[string][]byte, error) {
	need := make(map[string]bool, len(digests))
	for _, d := range digests {
		need[d] = true
	}
	out := make(map[string][]byte, len(digests))
	cli := describeClient()

	var lastErr error
	asked := 0
	for _, n := range nodes {
		if len(need) == 0 {
			break
		}
		if n == nil || n.Address == "" || n.Metadata["protocol"] != tcpProtocol {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		asked++
		want := make([]string, 0, len(need))
		for d := range need {
			want = append(want, d)
		}
		sort.Strings(want)

		req, err := cli.NewRequest("volts.registry", registry.DescribeEndpoint, &registry.DescribeRequest{Digests: want}, WithCodec(codec.JSON))
		if err != nil {
			return out, err
		}
		rsp, err := cli.Call(req, WithAddress(n.Address), WithContext(ctx))
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", n.Address, err)
			continue
		}
		var body registry.DescribeResponse
		if err := rsp.Body().Decode(&body); err != nil {
			lastErr = fmt.Errorf("%s: decode: %w", n.Address, err)
			continue
		}
		for d, raw := range body.Descs {
			if need[d] {
				out[d] = []byte(raw)
				delete(need, d)
			}
		}
	}

	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		if asked == 0 {
			return nil, fmt.Errorf("没有能答描述的节点（TCP）")
		}
	}
	return out, nil
}
