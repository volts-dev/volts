package router

import (
	"fmt"
	"sync"
)

/**
中间件控制器

Sample:

		TWebCtrl struct {
			event.TEvent
		}

		// 传递的必须为非指针的值(self TWebCtrl)
		func (self TWebCtrl) Before(hd *web.THandler) {

		}

*/

type (
	IMiddleware interface{}

	// 中间件名称接口
	IMiddlewareName interface {
		Name() string
	}

	IMiddlewareInit interface {
		Init(*TRouter)
	}

	/*
		this will call before current ruote
		@controller: the action interface which middleware bindding
		@hd: the Handler interface for controller
	*/
	IMiddlewareRequest interface {
		Request(controller interface{}, ctx IContext)
	}

	/*
		this will call after current ruote
		@controller: the action interface which middleware bindding
		@hd: the Handler interface for controller
	*/
	IMiddlewareResponse interface {
		Response(controller interface{}, ctx IContext)
	}

	TMiddlewareManager struct {
		middlewares map[string]IMiddleware
		names       []string     //
		lock        sync.RWMutex // 同步性不重要暂时不加锁
	}

	Middleware struct {
	}
)

// middleware 中间件对象
// urls 不执行中间件名称
func (self *Middleware) BlackList(middleware interface{}, urls ...string) {

}

func newMiddlewareManager() *TMiddlewareManager {
	return &TMiddlewareManager{
		middlewares: make(map[string]IMiddleware),
	}

}

// 有序返回所有中间件名称 顺序依据注册顺序
func (self *TMiddlewareManager) Names() []string {
	return self.names
}

func (self *TMiddlewareManager) Contain(key string) bool {
	self.lock.RLock()
	_, ok := self.middlewares[key]
	self.lock.RUnlock()
	return ok
}

func (self *TMiddlewareManager) Add(key string, value IMiddleware) {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, exsit := self.middlewares[key]; !exsit {
		self.middlewares[key] = value
		self.names = append(self.names, key) // # 保存添加顺序
	} else {
		log.Err("key:" + key + " already exists")
	}
}

func (self *TMiddlewareManager) Set(key string, value IMiddleware) {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, ok := self.middlewares[key]; ok {
		self.middlewares[key] = value
	} else {
		fmt.Println("key:" + key + " does not exists")
	}
}

func (self *TMiddlewareManager) Get(key string) IMiddleware {
	self.lock.RLock()
	defer self.lock.RUnlock()
	return self.middlewares[key]
}

func (self *TMiddlewareManager) Del(key string) {
	self.lock.Lock()
	defer self.lock.Unlock()

	delete(self.middlewares, key)
	for i, n := range self.names {
		if n == key {
			self.names = append(self.names[:i], self.names[i+1:]...)
			break
		}

	}
}
