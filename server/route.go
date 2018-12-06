package server

import (
	"net/url"
	"reflect"
)

const (
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
		Name     string        // 名称
		Func     reflect.Value // 方法本体
		FuncType reflect.Type  // 方法类型
		//ArgType   []reflect.Type // 参数组类型
		//ReplyType []reflect.Type //TODO 返回多结果
		ArgType    reflect.Type  // 参数组类型
		ReplyType  reflect.Type  //TODO 返回多结果
		Controller reflect.Value // TODO 待定 控制器
	}

	// TRoute 路,表示一个Link 连接地址"../webgo/"
	// 提供基础数据参数供Handler处理
	TRoute struct {
		ID             int64  // 在Tree里的Id号
		Path           string // 网络路径
		FilePath       string // 短存储路径
		Model          string // 模型/对象/模块名称 Tmodule/Tmodel, "Model.Action", "404"
		Action         string // 动作名称[包含模块名，动作名] "Model.Action", "/index.html","/filename.png"
		FileName       string
		Type           RouteType // Route 类型 决定合并的形式
		Host           *url.URL
		isReverseProxy bool //# 是反向代理
		isDynRoute     bool // 是否*动态路由   /base/*.html

		MainCtrl TMethodType   // 主控制器 每个Route都会有一个主要的Ctrl,其他为Hook的Ctrl
		Ctrls    []TMethodType // 最终控制器 合并主控制器+次控制器
		//HookCtrl map[string][]TMethodType // 次控制器 map[*][]TMethodType 匹配所有  Hook的Ctrl会在主的Ctrl执行完后执行
		//Ctrls    map[string][]TMethodType // 最终控制器 合并主控制器+次控制器

		rpcHandler *TRpcHandler
		webHandler *TWebHandler
	}
)

// TODO 管理Ctrl 顺序 before center after
// 根据不同Action 名称合并Ctrls
func (self *TRoute) CombineController(aFrom *TRoute) {
	switch aFrom.Type {
	/*	case CommomRoute:
		// 普通Tree合并
		{
			self.MainCtrl = append(self.MainCtrl, aFrom.MainCtrl...)
		}*/
	case HookBeforeRoute:
		/*
			// 静态：Url xxx\xxx\action 将直接插入
			// 动态：根据Action 插入到Map 叠加
			if aFrom.isDynRoute {
				self.HookCtrl[aFrom.Action] = append(self.HookCtrl[aFrom.Action], aFrom.MainCtrl)
			} else {
				self.MainCtrl = append(self.MainCtrl, aFrom.MainCtrl...)
			}
		*/

		self.Ctrls = []TMethodType{self.MainCtrl}
		self.Ctrls = append(self.Ctrls, self.Ctrls...)
	case HookAfterRoute:
		self.Ctrls = append(self.Ctrls, aFrom.MainCtrl)
	default:
		// 替换路由会直接替换 主控制器 但不会影响其他Hook 进来的控制器
		self.MainCtrl = aFrom.MainCtrl
		self.Ctrls = []TMethodType{self.MainCtrl}
		//logger.Dbg("CombineController", self.MainCtrl, aFrom.MainCtrl)
	}
}

func (self *TRoute) GetRpcHandler() *TRpcHandler {
	return self.rpcHandler
}

func (self *TRoute) GetHttpHandler() *TWebHandler {
	return self.webHandler
}
