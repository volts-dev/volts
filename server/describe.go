package server

import (
	"sync"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/router"
)

// 按哈希交出本进程注册出去的服务描述（registry/descriptor.go，设计见
// docs/registry_descriptor_design.md）。
//
// 只挂在 TCP（RPC）服务器上：描述里有端点表、设置字段、模块钩子，不该出现在对外的 HTTP
// 端口上。路由放在 IsService=false 的组里，所以它不会被当成业务端点再写进描述。

// tcpProtocol 是 TCP 传输层的 Protocol()，见 transport/tcp.go。
const tcpProtocol = "TCP"

// describeRouters 记已经挂过描述端点的路由器：http 与 rpc 两个服务器常共用一个路由器。
var describeRouters sync.Map

// enableDescriptorServing 在 TCP 服务器上声明本进程答描述，并挂上端点。
//
// 构造时就置位（不等 Start）：同一个进程的 http 服务器与 rpc 服务器并发启动，http 那边先注册的话，
// 置位晚了就会先写一轮完整记录、下一个注册周期再改写成瘦身的。
func enableDescriptorServing(cfg *Config) {
	if cfg == nil || cfg.Transport == nil || cfg.Transport.Protocol() != tcpProtocol {
		return
	}
	registry.SetDescriptorServing(true)
	if cfg.Router == nil {
		return
	}
	if _, loaded := describeRouters.LoadOrStore(cfg.Router, struct{}{}); loaded {
		return
	}
	g := router.NewGroup()
	g.Url([]string{"CONNECT"}, registry.DescribeEndpoint, describeHandler)
	cfg.Router.RegisterGroup(g)
}

func describeHandler(ctx *router.TRpcContext) {
	var req registry.DescribeRequest
	if err := ctx.Request().Body().Decode(&req); err != nil {
		log.Warnf("registry describe: bad request: %v", err)
		_ = ctx.Response().WriteStream(&registry.DescribeResponse{})
		return
	}
	_ = ctx.Response().WriteStream(&registry.DescribeResponse{Descs: registry.LocalDescriptors(req.Digests)})
}
