package router

import (
	"github.com/volts-dev/volts/registry"
)

const (
	Normal RoutePosition = iota
	Before
	After
	Replace // the route replace origin
)

type RoutePosition byte

func (self RoutePosition) String() string {
	return [...]string{"Normal", "Before", "After", "Replace"}[self]
}

type (
	// route 结构体定义了路由的相关属性和方法
	// 用于匹配和处理特定的HTTP请求
	route struct {
		group           *TGroup       // 所属的路由组
		Id              int           // 路由的唯一标识符，用于定位和调试
		Path            string        // 路径，存储路由绑定的URL路径
		PathDelimitChar byte          // 路径分隔符，用于区分路径中的不同部分，通常是'/'或'.'
		FilePath        string        // 文件路径，存储路由对应的文件路径
		Position        RoutePosition // 路由位置信息，可能包括命名组、子路由等
		handlers        []*handler    // 处理器列表，包含主处理器和次处理器，用于处理匹配到的请求
		Methods         []string      // 支持的HTTP方法列表，如GET、POST等
		Host            []string      // 允许的主机名列表，用于限制路由仅对特定主机生效
		Url             *TUrl         // URL对象，提供更复杂的URL匹配和参数提取功能
		Action          string        // 动作名称，包含模块名和动作名，用于标识要执行的具体操作
	}
)

var idQueue int = 0 //id 自动递增值

func RouteToEndpiont(r *route) *registry.Endpoint {
	ep := &registry.Endpoint{
		//Name: r.
		Method: r.Methods,
		Path:   r.Path,
		Host:   r.Host,
		//Metadata: make(map[string]string),
	}
	//ep.Metadata["Path"] = r.Path
	//ep.Metadata["FilePath"] = r.FilePath
	//ep.Metadata["Type"] = r.Type.String()

	return ep
}

func EndpiontToRoute(ep *registry.Endpoint) *route {
	return newRoute(
		nil,
		ep.Method,
		nil,
		ep.Path,
		"",
		"",
		"",
	)
}

func newRoute(group *TGroup, methods []string, url *TUrl, path, filePath, name, action string) *route {
	r := &route{
		group:           group,
		Id:              idQueue + 1,
		Url:             url,
		Path:            path,
		PathDelimitChar: '/',
		FilePath:        filePath,
		Action:          action, //
		Methods:         methods,
		handlers:        make([]*handler, 0),
	}

	if url != nil {
		r.Path = url.Path
	}

	return r
}

func (self *route) Group() *TGroup {
	return self.group
}

// TODO 管理Ctrl 顺序 before center after
// 根据不同Action 名称合并Ctrls
func (self *route) CombineHandler(from *route) {
	switch from.Position {
	case Before:
		self.handlers = append(from.handlers, self.handlers...)
	case After:
		self.handlers = append(self.handlers, from.handlers...)
	default:
		// 替换路由会直接替换 主控制器 但不会影响其他Hook 进来的控制器
		self.handlers = from.handlers
	}
}

// StripHandler 从当前路由中移除与目标路由匹配的服务。
// 这个方法主要用于在路由中排除某些服务，通常是在负载均衡或服务发现场景中使用。
// 参数:
//   - target: 目标路由，用于与当前路由的手动处理器中的服务进行匹配。
func (self *route) StripHandler(target *route) {
	// 初始化一个空的服务切片，用于存储不与目标路由匹配的服务。
	srvs := make([]*registry.Service, 0)
	// 用于标记服务是否匹配的变量。
	var match bool

	// 遍历当前路由的手动处理器。
	for _, ctr := range self.handlers {
		// 如果处理器类型不是本地处理器，则进一步处理。
		if ctr.Type != LocalHandler {
			// 遍历处理器中的服务。
			for _, srv := range ctr.Services {
				// 默认情况下，假设没有匹配的服务。
				match = false

				// 遍历目标路由的手动处理器。
				for _, hd := range target.handlers {
					// 遍历目标处理器中的服务。
					for _, s := range hd.Services {
						// 如果当前服务与目标服务不相等，则标记为匹配，并跳出循环。
						if !srv.Equal(s) {
							match = true
							break
						}
					}
				}

				// 如果当前服务没有与目标服务匹配，则将其添加到srvs切片中。
				if !match {
					srvs = append(srvs, srv)
				}
			}

			// 更新处理器的服务列表为那些不与目标路由匹配的服务。
			ctr.Services = srvs
		}
	}
}
