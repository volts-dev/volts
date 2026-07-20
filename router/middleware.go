package router

import (
	"sync"
)

type (
	// 中间件接口
	IMiddleware interface {
		Handler(ctx IContext)
	}

	// 中间件名称接口
	IMiddlewareName interface {
		Name() string
	}

	// 中间件重置接口
	// 重置接口用于实现特定成员手动清空
	// NOTE:改函数被调用有当Handler执行完毕并回收中
	IMiddlewareReset interface {
		Reset()
	}

	// 中间件传输类型声明接口（可选）
	//
	// 本框架同时承载 HTTP 与 RPC，两者的 handler 链是各自独立装配的
	// （handler id 即含 TransportType）。IMiddleware.Handler 的签名收 IContext，
	// 天然协议无关，因此**不实现本接口的中间件默认两条链都装**。
	//
	// 只对单侧有意义的中间件（CORS 只碰 Header/Cookie，仅 HTTP 有语义）实现本接口
	// 声明适用范围，装配期即被筛掉 —— 不适用的那一侧 funcs 里根本不存在该条目，
	// 请求路径上零开销，而不是运行时再判断一次。
	IMiddlewareTransport interface {
		Transports() []TransportType
	}

	TMiddlewareManager struct {
		middlewares sync.Map
		names       []string     //
		lock        sync.RWMutex // 同步性不重要暂时不加锁
	}
)

func newMiddlewareManager() *TMiddlewareManager {
	return &TMiddlewareManager{
		//middlewares: make(map[string]func(IRouter) IMiddleware),
	}

}

// 有序返回所有中间件名称 顺序依据注册顺序
func (self *TMiddlewareManager) Names() []string {
	return self.names
}

func (self *TMiddlewareManager) Contain(key string) bool {
	_, exsit := self.middlewares.Load(key)
	return exsit
}

func (self *TMiddlewareManager) Add(key string, creator func(IRouter) IMiddleware) {
	if _, exsit := self.middlewares.Load(key); exsit {
		return
	}

	self.middlewares.Store(key, creator)
	self.names = append(self.names, key) // # 保存添加顺序
}

func (self *TMiddlewareManager) Set(key string, creator func(IRouter) IMiddleware) {
	self.middlewares.Store(key, creator)
}

func (self *TMiddlewareManager) Get(key string) func(IRouter) IMiddleware {
	creator, exsit := self.middlewares.Load(key)
	if exsit {
		return creator.(func(IRouter) IMiddleware)
	}

	return nil
}

func (self *TMiddlewareManager) Del(key string) {
	self.middlewares.Delete(key)
	for i, n := range self.names {
		if n == key {
			self.names = append(self.names[:i], self.names[i+1:]...)
			break
		}
	}
}
