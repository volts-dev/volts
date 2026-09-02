package transport

// ss/ssr 的**真节点**实测。默认不跑。
//
//	VOLTS_SS_LIVE_NODES=/path/to/nodes.txt go test ./transport/ -run TestSsLive -v
//
// 文件里一行一个 ss:// 或 ssr:// URL。**别把它提交进仓库** —— 那一行就是凭据。
//
// 为什么必须有这么一条：上面那些进程内往返测的是"我写的客户端和我写的服务端能
// 互相说话"，而这套协议真正的风险在**对面不是我写的**：段序解错、口令派生差一个
// 字节、obfs 少一个头，本地全绿而真节点一个都连不上。同 core/ja3 的
// boundary_live_test.go —— "指纹能不能用"只有真服务器答得了。

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestSsLive(t *testing.T) {
	path := os.Getenv("VOLTS_SS_LIVE_NODES")
	if path == "" {
		t.Skip("设置 VOLTS_SS_LIVE_NODES=<节点清单文件> 才跑这条")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var nodes []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			nodes = append(nodes, line)
		}
	}
	if len(nodes) == 0 {
		t.Fatal("清单是空的")
	}

	ok := 0
	for i, node := range nodes {
		name := fmt.Sprintf("node%02d", i)
		if _, tag, found := strings.Cut(node, "#"); found && tag != "" {
			name = tag
		}
		t.Run(name, func(t *testing.T) {
			d, err := NewProxyDialer(node, "")
			if err != nil {
				t.Fatalf("建拨号器失败: %v", err)
			}
			cli := &http.Client{
				Timeout: 20 * time.Second,
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						if cd, is := d.(proxy.ContextDialer); is {
							return cd.DialContext(ctx, network, addr)
						}
						return d.Dial(network, addr)
					},
					TLSClientConfig:   &tls.Config{},
					ForceAttemptHTTP2: true,
				},
			}

			// 出口 IP 才是判据。**只看 204 是不够的** —— 代理没生效时直连一样 204，
			// 那正是"配了代理却在裸奔"的样子。
			rsp, err := cli.Get("https://api.ipify.org")
			if err != nil {
				t.Fatalf("经代理请求失败: %v", err)
			}
			defer rsp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(rsp.Body, 128))
			if rsp.StatusCode != 200 {
				t.Fatalf("HTTP %d", rsp.StatusCode)
			}
			t.Logf("出口 IP = %s", strings.TrimSpace(string(body)))
			ok++
		})
	}
	t.Logf("%d/%d 个节点可用", ok, len(nodes))
}
