package base

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"
)

func OutputAnyParameter() any {
	fn := func(arg *int) {
		*arg = 123
	}

	var i int
	fn(&i)
	return i
}

func ReflectOutputAnyParameter() any {
	type eface struct {
		typ, val unsafe.Pointer
	}

	fn := func(arg any) {
		var vv any = 123
		reflect.Indirect(reflect.ValueOf(arg)).Set(reflect.ValueOf(vv))
	}

	var i int
	fn(&i)
	return i
}

// TODO 判断类型
func UnsafeOutputAnyParameter() any {
	//type user struct {
	//	name string
	//}
	type eface struct {
		typ, val unsafe.Pointer
	}

	fn := func(arg any) {
		//var val any = user{"test"}
		var val any = 123
		//fmt.Println(reflect.TypeOf(arg).Elem().Name(), reflect.TypeOf(val).Name())
		//*
		t := reflect.TypeOf(arg)
		tt := reflect.TypeOf(val)
		// 一下代码会导致Unsafe出错
		//fmt.Println(t == tt)

		if tt.String() == tt.String() && t != tt {
			//	panic("could not convert differnt type!")
		}
		//*/
		dst := (*eface)(unsafe.Pointer(&arg))
		src := (*eface)(unsafe.Pointer(&val))
		//fmt.Println(*(*eface)(dst.val), *(*eface)(src.val))

		*(*eface)(dst.val) = *(*eface)(src.val)
	}

	var i int

	fn(&i)
	return i
}

func BenchmarkUnsafeOutputAnyParameter(b *testing.B) {
	for n := 0; n < b.N; n++ {
		UnsafeOutputAnyParameter()
	}
}
func TestUnsafeOutputAnyParameter(t *testing.T) {
	for n := 0; n < 100; n++ {
		fmt.Println("Output:", UnsafeOutputAnyParameter())
	}
}

func BenchmarkOutputAnyParameter(b *testing.B) {
	for n := 0; n < b.N; n++ {
		OutputAnyParameter()
	}
}

func BenchmarkReflectOutputAnyParameter(b *testing.B) {
	for n := 0; n < b.N; n++ {
		ReflectOutputAnyParameter()
	}
}
