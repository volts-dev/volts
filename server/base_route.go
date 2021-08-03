package server

import (
	"net/url"
	"reflect"
)

const (
	// TODO 重新命名
	CommomRoute RouteType = iota // extenion route
	HookBeforeRoute
	HookAfterRoute
	ReplaceRoute // the route replace orgin
	ProxyRoute
)

type (
	RouteType byte

	// 路由节点绑定的控制器 func(Handler)
	TMethodType struct {
		Name       string        // 名称
		Func       reflect.Value // 方法本体
		FuncType   reflect.Type  // 方法类型
		ArgType    reflect.Type  // 参数组类型
		ReplyType  reflect.Type  // TODO 返回多结果
		Controller reflect.Value // TODO 待定 控制器
		//ArgType   []reflect.Type // 参数组类型
		//ReplyType []reflect.Type //TODO 返回多结果
	}

	// TRoute 路,表示一个Link 连接地址"../webgo/"
	// 提供基础数据参数供Handler处理
	TRoute struct {
		group          *TGroup
		Id             int       // 用于定位
		Path           string    // !NOTE! Path存储路由绑定的URL 网络路径
		FilePath       string    // 短存储路径
		Model          string    // 模型/对象/模块名称 Tmodule/Tmodel, "Model.Action", "404"
		Action         string    // 动作名称[包含模块名，动作名] "Model.Action", "/index.html","/filename.png"
		FileName       string    //
		Type           RouteType // Route 类型 决定合并的形式
		Host           *url.URL
		Url            *TUrl
		isReverseProxy bool          //# 是反向代理
		isDynRoute     bool          // 是否*动态路由   /base/*.html
		MainCtrl       TMethodType   // 主控制器 每个Route都会有一个主要的Ctrl,其他为Hook的Ctrl
		Ctrls          []TMethodType // 最终控制器 合并主控制器+次控制器
		//HookCtrl map[string][]TMethodType // 次控制器 map[*][]TMethodType 匹配所有  Hook的Ctrl会在主的Ctrl执行完后执行
		//Ctrls    map[string][]TMethodType // 最终控制器 合并主控制器+次控制器
	}
)

var idQueue int = 0 //id 自动递增值

func newRoute(group *TGroup, url *TUrl, path, filePath, name, action string, rt RouteType) *TRoute {
	return &TRoute{
		group:    group,
		Id:       idQueue + 1,
		Url:      url,
		Path:     url.Path,
		FilePath: filePath,
		Model:    name,
		Action:   action, //
		Type:     rt,
		Ctrls:    make([]TMethodType, 0),
		//HookCtrl: make([]TMethodType, 0),
		//Host:     host,
		//Scheme:   scheme,
	}
}

func (self *TRoute) Group() *TGroup {
	return self.group
}

// TODO 管理Ctrl 顺序 before center after
// 根据不同Action 名称合并Ctrls
func (self *TRoute) CombineController(from *TRoute) {
	switch from.Type {
	/*	case CommomRoute:
		// 普通Tree合并
		{
			self.MainCtrl = append(self.MainCtrl, from.MainCtrl...)
		}*/
	case HookBeforeRoute:
		/*
			// 静态：Url xxx\xxx\action 将直接插入
			// 动态：根据Action 插入到Map 叠加
			if from.isDynRoute {
				self.HookCtrl[from.Action] = append(self.HookCtrl[from.Action], from.MainCtrl)
			} else {
				self.MainCtrl = append(self.MainCtrl, from.MainCtrl...)
			}
		*/

		from.Ctrls = []TMethodType{from.MainCtrl}
		self.Ctrls = append(from.Ctrls, self.Ctrls...)
	case HookAfterRoute:
		self.Ctrls = append(self.Ctrls, from.MainCtrl)
	default:
		// 替换路由会直接替换 主控制器 但不会影响其他Hook 进来的控制器
		self.MainCtrl = from.MainCtrl
		self.Ctrls = []TMethodType{self.MainCtrl}
		//logger.Dbg("CombineController", self.MainCtrl, from.MainCtrl)
	}
}
