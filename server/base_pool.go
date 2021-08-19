package server

import (
	"reflect"
	"sync"
)

/*
	本池不会自动生成任何东西只暂存保管
*/

type (
	pool struct {
		active bool
		pools  map[reflect.Type]*sync.Pool
		New    func(t reflect.Type) reflect.Value
	}
)

// TODO 使用字符串名称
// TODO 测试效率
func newPool() *pool {
	return &pool{
		active: true,
		pools:  make(map[reflect.Type]*sync.Pool), //@@@ 改进改为接口 String
		New: func(t reflect.Type) reflect.Value {
			var argv reflect.Value

			if t.Kind() == reflect.Ptr {
				argv = reflect.New(t.Elem())
			} else {
				argv = reflect.New(t)
			}

			return argv
		},
	}
}

func (self *pool) Active(open bool) {
	self.active = open
}

//@@@ 改进改为接口
// Resul:nil 当取不到时直接返回Nil 方便外部判断
// TODO:优化速度
func (self *pool) Get(object reflect.Type) reflect.Value {
	if object == nil {
		return reflect.ValueOf(nil)
	}

	if !self.active {
		return self.New(object)
	}

	if pool, ok := self.pools[object]; ok {
		itf := pool.Get()
		if itf == nil {
			//return reflect.New(object).Elem()
			return self.New(object)
		}

		return itf.(reflect.Value)
	} else {
		//return reflect.New(object).Elem()
		return self.New(object)
	}
}

func (self *pool) Put(typ reflect.Type, val reflect.Value) {
	if !self.active {
		return
	}

	if pool, ok := self.pools[typ]; ok {
		pool.Put(val)
	} else {
		pool := new(sync.Pool)

		pool.Put(val)
		self.pools[typ] = pool
	}
}
