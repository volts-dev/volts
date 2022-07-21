package router

import (
	"reflect"
	"testing"
)

func initCall(obj interface{}) (reflect.Value, func(...interface{}) string, iCtrl) {
	objVval := reflect.ValueOf(obj)
	funVal := objVval.MethodByName("String")
	return funVal, funVal.Interface().(func(s ...interface{}) string), objVval.Interface().(iCtrl)
}

func BenchmarkHandlerDirectCall(b *testing.B) {
	obj := &ctrl{Name: "111"}
	initCall(obj)
	for n := 0; n < b.N; n++ {
		obj.String()
	}
}

func BenchmarkHandlerReflectCall(b *testing.B) {
	obj := &ctrl{Name: "111"}
	fnVal, _, _ := initCall(obj)
	for n := 0; n < b.N; n++ {
		fnVal.Call(nil)
	}
}

func BenchmarkHandlerReflectInterfaceStruct(b *testing.B) {
	obj := &ctrl{Name: "111"}
	_, _, o := initCall(obj)
	for n := 0; n < b.N; n++ {
		o.String()
	}
}

func BenchmarkHandlerReflectInterfaceFunc(b *testing.B) {
	obj := &ctrl{Name: "111"}
	_, fn, _ := initCall(obj)
	for n := 0; n < b.N; n++ {
		fn()
	}
}
