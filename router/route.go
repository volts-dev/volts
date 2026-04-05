package router

import (
	"sync"
	"sync/atomic"

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
	IRoute interface {
		Id() int
		Path() string
		PathDelimitChar() byte
		FilePath() string
		Position() RoutePosition
		Methods() []string
		Host() []string
		URL() *TUrl
		Action() string
		Group() *TGroup
		Handlers() []int
		Clone() IRoute
		//CombineHandler(from IRoute)
		//StripHandler(target IRoute)
	}

	// route 结构体定义了路由的相关属性和方法
	// 用于匹配和处理特定的HTTP请求
	route struct {
		sync.RWMutex                  // 并发保护锁
		group           *TGroup       // 所属的路由组
		id              int           // 路由的唯一标识符，用于定位和调试
		path            string        // 路径，存储路由绑定的URL路径
		pathDelimitChar byte          // 路径分隔符，用于区分路径中的不同部分，通常是'/'或'.'
		filePath        string        // 文件路径，存储路由对应的文件路径
		position        RoutePosition // 路由位置信息，可能包括命名组、子路由等
		handlers        []int         // 处理器列表，包含主处理器和次处理器，用于处理匹配到的请求
		methods         []string      // 支持的HTTP方法列表，如GET、POST等
		host            []string      // 允许的主机名列表，用于限制路由仅对特定主机生效
		url             *TUrl         // URL对象，提供更复杂的URL匹配和参数提取功能
		action          string        // 动作名称，包含模块名和动作名，用于标识要执行的具体操作
	}
)

var idQueue int32 = 0 //id 自动递增值

func RouteToEndpiont(r *route) *registry.Endpoint {
	ep := &registry.Endpoint{
		//Name: r.
		Method: r.Methods(),
		Path:   r.Path(),
		Host:   r.Host(),
		//Metadata: make(map[string]string),
	}
	//ep.Metadata["Path"] = r.Path()
	//ep.Metadata["FilePath"] = r.FilePath()
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
		id:              int(atomic.AddInt32(&idQueue, 1)),
		url:             url,
		path:            path,
		pathDelimitChar: '/',
		filePath:        filePath,
		action:          action, //
		methods:         methods,
		//handlers:        make([]*handler, 0),
	}

	if url != nil {
		r.path = url.Path
	}

	return r
}

func (self *route) Id() int {
	return self.id
}

func (self *route) Path() string {
	return self.path
}

func (self *route) PathDelimitChar() byte {
	return self.pathDelimitChar
}

func (self *route) FilePath() string {
	return self.filePath
}

func (self *route) Position() RoutePosition {
	return self.position
}

func (self *route) Methods() []string {
	return self.methods
}

func (self *route) Host() []string {
	return self.host
}

func (self *route) URL() *TUrl {
	return self.url
}

func (self *route) Action() string {
	return self.action
}

func (self *route) Group() *TGroup {
	return self.group
}

func (self *route) Clone() IRoute {
	self.RLock()
	defer self.RUnlock()

	r := &route{
		group:           self.group,
		id:              self.id,
		path:            self.path,
		pathDelimitChar: self.pathDelimitChar,
		filePath:        self.filePath,
		position:        self.position,
		action:          self.action,
		url:             self.url,
	}

	// 拷贝切片
	if self.methods != nil {
		r.methods = make([]string, len(self.methods))
		copy(r.methods, self.methods)
	}

	if self.host != nil {
		r.host = make([]string, len(self.host))
		copy(r.host, self.host)
	}

	if self.handlers != nil {
		r.handlers = make([]int, len(self.handlers))
		copy(r.handlers, self.handlers)
	}

	return r
}

// TODO 管理Ctrl 顺序 before center after
// 根据不同Action 名称合并Ctrls
func (self *route) CombineHandler(from *route) {
	self.Lock()
	defer self.Unlock()

	//! NOTE 由于 Clone 了 Route 所以这里不能直接使用 from.handlers, 也需要 Clone
	fromHandlers := from.Handlers()

	switch from.position {
	case Before:
		self.handlers = append(fromHandlers, self.handlers...)
	case After:
		self.handlers = append(self.handlers, fromHandlers...)
	default:
		// 替换路由会直接替换 主控制器 但不会影响其他Hook 进来的控制器
		self.handlers = fromHandlers
	}
}

func (self *route) Handlers() []int {
	return self.handlers
}

// StripHandler 从当前路由中剔除目标路由包含的所有处理器（Handler）。
// 
// 该操作实质上是处理器 ID 列表的“减法”运算（self.handlers = self.handlers - target.handlers）。
// 常用场景包括：在服务发现更新时，从本地路由中移除已下线的服务节点对应的处理器。
//
// 参数:
//   - target: 目标路由，其内部包含的处理器 ID 列表将作为“剔除名单”。
func (self *route) stripHandler(target *route) {
	if target == nil {
		return
	}

	// 先获取目标处理器的副本（内部会处理读锁并释放）
	// 这样可以避免在持有 self.Lock 的情况下再去获取 target.Handlers() 导致的死锁（尤其是 self == target 时）
	targetHandlers := target.Handlers()

	// 1. 将剔除列表 b 放入 map，利用其 O(1) 的查找特性
	m := make(map[int]struct{}, len(targetHandlers))
	for _, x := range targetHandlers {
		m[x] = struct{}{}
	}

	self.Lock()
	defer self.Unlock()
	// 2. 预分配结果切片容量，减少内存重分配开销
	// 假设结果长度可能接近 a
	res := make([]int, 0, len(self.handlers))
	// 3. 遍历 a，剔除在 map 中存在的元素
	for _, x := range self.handlers {
		if _, ok := m[x]; !ok {
			res = append(res, x)
		}
	}

	self.handlers = res
}
