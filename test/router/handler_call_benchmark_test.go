package router

import (
	"reflect"
	"testing"
)

func initCall(obj interface{}) (reflect.Value, func(...interface{}) string, iCtrl, func(...interface{}) string) {
	objVval := reflect.ValueOf(obj)
	funVal := objVval.MethodByName("String")
	var wrap iwrapper
	wrap = &wrapper{
		Func: funVal.Interface().(func(...interface{}) string),
	}

	v := reflect.ValueOf(wrap)
	vv := v.Interface().(iwrapper)
	return funVal, funVal.Interface().(func(s ...interface{}) string), objVval.Interface().(iCtrl), vv.String
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
	fnVal, _, _, _ := initCall(obj)
	for n := 0; n < b.N; n++ {
		fnVal.Call(nil)
	}
}

func BenchmarkHandlerReflectInterfaceStruct(b *testing.B) {
	obj := &ctrl{Name: "111"}
	_, _, o, _ := initCall(obj)
	for n := 0; n < b.N; n++ {
		o.String()
	}
}

func BenchmarkHandlerReflectInterfaceFunc(b *testing.B) {
	obj := &ctrl{Name: "111"}
	_, fn, _, _ := initCall(obj)
	for n := 0; n < b.N; n++ {
		fn()
	}
}

func BenchmarkHandlerReflectInterfaceFunc2(b *testing.B) {
	obj := &ctrl{Name: "111"}
	_, _, _, fn := initCall(obj)

	for n := 0; n < b.N; n++ {
		fn()
	}
}
