package selector

import (
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

// FilterTrasport 只保留 metadata["transport"] 与给定传输一致的节点。
//
// 修了两个 bug：
//
//  1. nodes 变量声明在**外层循环之外**，于是节点会跨 service 累积：
//     第 2 个 service 拿到的是「第 1 个 service 的节点 + 自己的节点」。
//     registry.GetService(name) 是按版本各返回一个 *Service 的，所以实际后果是
//     **新版本会声称自己拥有旧版本的节点** —— 打给 v1.1 的请求可以落到 v1.0 的
//     节点上，且节点越多错得越离谱。本过滤器挂在每一次 RPC / HTTP 客户端调用上。
//
//  2. 没有节点匹配的 service 仍被保留（还带着别人的节点）。现与 FilterLabel
//     一致：过滤后为空的 service 直接剔除，让 Select 如实返回 ErrNoneAvailable，
//     而不是给出一个根本不在该传输上的节点。
//
// 另外改为返回副本而非就地改写 service.Nodes。当前 registry cacher 的
// GetService 返回的是 Copy() 深拷贝，就地改不会污染缓存，但这层依赖是隐式的
// ——过滤器不该假设上游一定给了它可写的数据。
func FilterTrasport(tp transport.ITransport) Filter {
	return func(services []*registry.Service) []*registry.Service {
		name := tp.String()
		out := make([]*registry.Service, 0, len(services))

		for _, service := range services {
			nodes := make([]*registry.Node, 0, len(service.Nodes))
			for _, node := range service.Nodes {
				if node.Metadata != nil && node.Metadata["transport"] == name {
					nodes = append(nodes, node)
				}
			}
			if len(nodes) == 0 {
				continue
			}

			cp := *service
			cp.Nodes = nodes
			out = append(out, &cp)
		}

		return out
	}
}

// FilterEndpoint is an endpoint based Select Filter which will
// only return services with the endpoint specified.
func FilterEndpoint(name string) Filter {
	return func(old []*registry.Service) []*registry.Service {
		var services []*registry.Service

		for _, service := range old {
			for _, ep := range service.Endpoints {
				if ep.Name == name {
					services = append(services, service)
					break
				}
			}
		}

		return services
	}
}

// FilterLabel is a label based Select Filter which will
// only return services with the label specified.
func FilterLabel(key string, vals ...string) Filter {
	return func(old []*registry.Service) []*registry.Service {
		var services []*registry.Service

		for _, service := range old {
			serv := new(registry.Service)
			var nodes []*registry.Node

			for _, node := range service.Nodes {
				if node.Metadata == nil {
					continue
				}

				for _, val := range vals {
					if node.Metadata[key] == val {
						nodes = append(nodes, node)
					}
				}
			}

			// only add service if there's some nodes
			if len(nodes) > 0 {
				// copy
				*serv = *service
				serv.Nodes = nodes
				services = append(services, serv)
			}
		}

		return services
	}
}

// FilterVersion is a version based Select Filter which will
// only return services with the version specified.
func FilterVersion(version string) Filter {
	return func(old []*registry.Service) []*registry.Service {
		var services []*registry.Service

		for _, service := range old {
			if service.Version == version {
				services = append(services, service)
			}
		}

		return services
	}
}
