package server

import (
	"fmt"
	"sync"
	"vectors/logger"
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

var (
	Middleware = TMiddlewareManager{}
)

type (
	IMiddleware interface{}

	IMiddlewareInit interface {
		Init(*TRouter)
	}

	/*
		this will call before current ruote
		@controller: the action interface which middleware bindding
		@hd: the Handler interface for controller
	*/
	IMiddlewareRequest interface {
		Request(controller interface{}, route *TController)
	}

	/*
		this will call after current ruote
		@controller: the action interface which middleware bindding
		@hd: the Handler interface for controller
	*/
	IMiddlewareResponse interface {
		Response(controller interface{}, route *TController)
	}

	IMiddlewarePanic interface {
		Panic(controller interface{}, route *TController)
	}

	TMiddlewareManager struct {
		middlewares map[string]IMiddleware
		Names       []string     //
		lock        sync.RWMutex // 同步性不重要暂时不加锁
	}
)

func NewMiddlewareManager() *TMiddlewareManager {
	return &TMiddlewareManager{
		middlewares: make(map[string]IMiddleware),
	}

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
		self.Names = append(self.Names, key) // # 保存添加顺序
	} else {
		logger.Err("key:" + key + " already exists")
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
	for i, n := range self.Names {
		if n == key {
			self.Names = append(self.Names[:i], self.Names[i+1:]...)
			break
		}

	}
}
