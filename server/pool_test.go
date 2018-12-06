package server

import (
	"reflect"
	"testing"
)

var pool *TPool = NewPool()

type (
	AA struct {
		N int
	}

	BB struct {
		s string
	}

	CC string
)

func TestPool(t *testing.T) {
	laa := &AA{N: 9999}
	pool.Put(reflect.ValueOf(laa))

	Debug(reflect.Zero(reflect.TypeOf(BB{})).IsValid())
	lbb := &BB{s: "web pool"}
	lbb2 := &BB{s: "web pool2"}
	pool.Put(lbb)
	pool.Put(lbb2)
	pool.Put(lbb2)
	pool.Put(lbb2)
	//pool.Put("fasdfa")
	Debug("1", pool.Get(reflect.TypeOf(laa)).(reflect.Value).Interface().(*AA).N)
	Debug("2", pool.Get(reflect.TypeOf(BB{})).(*BB).s)
	Debug("3", pool.Get(BB{}).(*BB).s)
	Debug("3", pool.Get(&BB{}).(*BB).s)
	Debug("4", pool.Get(reflect.TypeOf(BB{})).(*BB).s)
	//Debug(pool.Get(reflect.TypeOf("af").(*BB)))
}
