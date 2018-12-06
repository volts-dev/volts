package test

import (
	"reflect"
	"testing"
)

type myint int64

type Inccer interface {
	Inc()
}

func (i myint) Inc2() {
	i = 123 + 231 + 23
}
func (i *myint) Inc() {
	*i = *i + 1
}

var (
	func_v = reflect.ValueOf(myint.Inc2)

	//incnReflectCall2(func_v, b.N)

	rec = func_v.Type().In(0)
	v   = reflect.Zero(rec)
	f   = v.MethodByName("Inc2")
)

func BenchmarkReflectMethodCall2(b *testing.B) {
	//func_type := reflect.TypeOf(myint.Inc2)
	func_v := reflect.ValueOf(myint.Inc2)

	incnReflectCall2(func_v, b.N)
	/*
		rec := func_v.Type().In(0)
		v := reflect.Zero(rec)
		f := v.MethodByName("Inc2")
		incnReflectCall3(f, b.N)*/
}

func incnReflectCall2(fn reflect.Value, n int) {
	rec := fn.Type().In(0)
	v := reflect.Zero(rec)

	for k := 0; k < n; k++ {
		fn.Call([]reflect.Value{v})
	}
}

func incnReflectCall3(v reflect.Value, n int) {
	//	fn := v.Interface().(func())

	for k := 0; k < n; k++ {
		//fn()
		func() int { return 1 + 1231231 }()
		//v.Call(nil)
	}
}
func BenchmarkReflectMethodCall(b *testing.B) {
	i := new(myint)
	incnReflectCall(i.Inc, b.N)
}

func _BenchmarkReflectOnceMethodCall(b *testing.B) {
	i := new(myint)
	incnReflectOnceCall(i.Inc, b.N)
}

func BenchmarkStructMethodCall(b *testing.B) {
	i := new(myint)
	incnIntmethod(i, b.N)
}

func _BenchmarkInterfaceMethodCall(b *testing.B) {
	i := new(myint)
	incnInterface(i, b.N)
}

func _BenchmarkTypeSwitchMethodCall(b *testing.B) {
	i := new(myint)
	incnSwitch(i, b.N)
}

func _BenchmarkTypeAssertionMethodCall(b *testing.B) {
	i := new(myint)
	incnAssertion(i, b.N)
}

func incnReflectCall(v interface{}, n int) {
	for k := 0; k < n; k++ {
		reflect.ValueOf(v).Call(nil)
	}
}

func incnReflectOnceCall(v interface{}, n int) {
	fn := reflect.ValueOf(v)
	for k := 0; k < n; k++ {
		fn.Call(nil)
	}
}

func incnIntmethod(i *myint, n int) {
	for k := 0; k < n; k++ {
		i.Inc2()
	}
}

func incnInterface(any Inccer, n int) {
	for k := 0; k < n; k++ {
		any.Inc()
	}
}

func incnSwitch(any Inccer, n int) {
	for k := 0; k < n; k++ {
		switch v := any.(type) {
		case *myint:
			v.Inc()
		}
	}
}

func incnAssertion(any Inccer, n int) {
	for k := 0; k < n; k++ {
		if newint, ok := any.(*myint); ok {
			newint.Inc()
		}
	}
}
