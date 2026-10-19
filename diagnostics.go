package volts

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/volts-dev/volts/internal/memlimit"
)

// EnvPprofAddr 设了就在这个地址上另起一个只服务 /debug/pprof/ 的监听，例如
// VOLTS_PPROF_ADDR=127.0.0.1:6060。同机多个进程各配一个端口。
//
// 与 router 的 use_pprof 不同：那个把 pprof 挂在服务自己的端口上，一台公网机器上
// 开了就是把堆转储、命令行参数和 CPU 采样交给任何人。这里只接受环回地址，要看就
// 先 ssh 上去再 curl —— 线上内存出事时，能抓 heap 与不能抓是两回事。
const EnvPprofAddr = "VOLTS_PPROF_ADDR"

var processRuntimeOnce sync.Once

// setupProcessRuntime 一个进程只做一次的运行时设置。kylin 一个进程起 HttpServer
// 与 RpcServer 两个 service，都会走到这里。放在 New 而不是 Start：冷启动（加载
// 模块、反查表结构）本身就是内存尖峰，上限要在那之前就位。
func setupProcessRuntime() {
	switch r := memlimit.Apply(); r.Source {
	case "cgroup":
		if b := r.Budget; b.Shared != 0 {
			log.Infof("memory limit: shared budget %s on %s (own cap %s) → GC soft limit %s, rebalanced every %s",
				formatBytes(int64(b.Shared)), b.SharedName, formatBytes(int64(b.Own)), formatBytes(r.Limit), memlimit.Interval)
		} else {
			log.Infof("memory limit: cgroup %s → GC soft limit %s", formatBytes(int64(b.Own)), formatBytes(r.Limit))
		}
	case "GOMEMLIMIT":
		log.Infof("memory limit: GOMEMLIMIT %s", formatBytes(r.Limit))
	}

	if addr := os.Getenv(EnvPprofAddr); addr != "" {
		if bound, err := startPprof(addr); err != nil {
			log.Errf("%s=%s ignored: %v", EnvPprofAddr, addr, err)
		} else {
			log.Infof("pprof listening on http://%s/debug/pprof/ (loopback only)", bound)
		}
	}
}

// startPprof 在环回地址上起 pprof 监听，返回实际绑定的地址。
//
// ★ 直接用 net.Listen，不能走 internal/net.Listen：后者会把 127.0.0.1:PORT 规整成
// :PORT（全接口），正好把这里要守住的东西放掉。
func startPprof(addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("must be a loopback address (127.0.0.1 / ::1 / localhost), got %q", host)
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return ln.Addr().String(), nil
}

func formatBytes(n int64) string {
	const mib = 1 << 20
	if n >= 1<<30 {
		return fmt.Sprintf("%.2fGiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%dMiB", n/mib)
}
