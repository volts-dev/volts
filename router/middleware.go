package router

import (
	"fmt"
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

	// 中间件初始化接口
	IMiddlewareInit interface {
		Init()
	}

	TMiddlewareManager struct {
		middlewares map[string]func() IMiddleware
		names       []string     //
		lock        sync.RWMutex // 同步性不重要暂时不加锁
	}
)

func newMiddlewareManager() *TMiddlewareManager {
	return &TMiddlewareManager{
		middlewares: make(map[string]func() IMiddleware),
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

func (self *TMiddlewareManager) Add(key string, value func() IMiddleware) {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, exsit := self.middlewares[key]; !exsit {
		self.middlewares[key] = value
		self.names = append(self.names, key) // # 保存添加顺序
	} else {
		log.Err("key:" + key + " already exists")
	}
}

func (self *TMiddlewareManager) Set(key string, value func() IMiddleware) {
	self.lock.Lock()
	defer self.lock.Unlock()

	if _, ok := self.middlewares[key]; ok {
		self.middlewares[key] = value
	} else {
		fmt.Println("key:" + key + " does not exists")
	}
}

func (self *TMiddlewareManager) Get(key string) func() IMiddleware {
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
