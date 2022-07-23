package router

import (
	"github.com/volts-dev/volts/registry"
)

const (
	Normal RoutePosition = iota
	Before
	After
	Replace // the route replace orgin
)

type RoutePosition byte

func (self RoutePosition) String() string {
	return [...]string{"Normal", "Before", "After", "Replace"}[self]
}

type (

	// route 路,表示一个Link 连接地址"../webgo/"
	// 提供基础数据参数供Handler处理
	route struct {
		group           *TGroup
		Id              int           // 用于定位
		Path            string        // !NOTE! Path存储路由绑定的URL 网络路径
		PathDelimitChar byte          // URL分割符 "/"或者"."
		FilePath        string        // 短存储路径
		Position        RoutePosition //
		handlers        []*handler    // 最终处理器 合并主处理器+次处理器 代理处理器
		Methods         []string      // 方法
		Host            []string      //
		Url             *TUrl         // 提供Restful 等Controller.Action
		Action          string        // 动作名称[包含模块名，动作名] "Model.Action", "/index.html","/filename.png"
		___isDynRoute   bool          // 是否*动态路由   /base/*.html
		// 废弃
		//HookCtrl map[string][]handler // 次控制器 map[*][]handler 匹配所有  Hook的Ctrl会在主的Ctrl执行完后执行
		//handlers    map[string][]handler // 最终控制器 合并主控制器+次控制器
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
	r := newRoute(
		nil,
		ep.Method,
		nil,
		ep.Path,
		"",
		"",
		"",
	)

	return r
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

// 剥离目标路由
// TODO 优化
func (self *route) StripController(target *route) {
	srvs := make([]*registry.Service, 0)
	var match bool
	for _, ctr := range self.handlers {
		if ctr.Type != LocalHandler {
			for _, srv := range ctr.Services {
				match = false
				for _, hd := range target.handlers {
					for _, s := range hd.Services {
						if !srv.Equal(s) {
							match = true
							break
						}
					}
				}

				if !match {
					srvs = append(srvs, srv)
				}
			}

			ctr.Services = srvs
		}
	}
}
