package router

import (
	"fmt"
	"hash/crc32"
	"reflect"
	"sync"
	"testing"

	"github.com/volts-dev/volts/router"
)

var i int = 0

// 可缓存
var (
	val  = reflect.ValueOf(&ctrl{Name: "Test", MidWare: &midWare{name: "User"}})
	pool sync.Pool
)

func init() {
	pool.New = func() interface{} {
		return reflect.New(val.Elem().Type())
	}
	//go func() {
	for i := 0; i < 100; i++ {
		pool.Put(reflect.New(val.Elem().Type()).Elem())
	}
	//}()

}

func TestHandlerReflectInterfaceChangedMetohd(t *testing.T) {
	var aa interface{}
	aa = &ctrl{Name: "111"}
	val := ValueOf(aa)

	p := val.Interface()
	if n, ok := p.(iwrapper); ok {
		fmt.Println(n.String())
	}
	/*
		ei := (*emptyInterface)(unsafe.Pointer(&p))
		ptr0 := uintptr(ei.word)
		fmt.Println(ptr0)

		fn := val.MethodByName("String")
		wrapper := &wrapper{
			Func: fn.Interface().(func(...interface{}) string),
		}
		fmt.Println(wrapper.String())
	*/
}

func BenchmarkCreatByMakeFunc(b *testing.B) {
	var creator func() interface{}
	// swap is the implementation passed to MakeFunc.
	// It must work in terms of reflect.Values so that it is possible
	// to write code without knowing beforehand what the types
	// will be.
	swap := func(in []reflect.Value) []reflect.Value {
		//newV := pool.Get().(reflect.Value)
		newV := reflect.New(val.Elem().Type())
		//newV.Field(0).Elem().Set(reflect.ValueOf("creator"))
		return []reflect.Value{newV}
	}
	fnVal := reflect.ValueOf(&creator).Elem()
	// Make a function of the right type.
	v := reflect.MakeFunc(fnVal.Type(), swap)
	fnVal.Set(v)
	var itf interface{}
	for n := 0; n < b.N; n++ {
		itf = creator()
	}
	//fmt.Println("xxx", creator())
	fmt.Println("xxx", itf)

}

func BenchmarkCreate(b *testing.B) {
	var itf interface{}
	for n := 0; n < b.N; n++ {
		itf = &ctrl{Name: "Test", MidWare: &midWare{name: "User"}}
	}
	fmt.Println("xxx", itf)
}

// 测试interface和reflect变更
func TestHandler(t *testing.T) {
	var c interface{}
	c = &ctrl{Name: "Test", MidWare: &midWare{name: "User"}}
	val := reflect.ValueOf(c)
	//mVal := val.Method(0)

	//if m, ok := val.Interface().(iMidWare); ok {
	//	fn = m.String
	//}

	var creator func() interface{}
	// swap is the implementation passed to MakeFunc.
	// It must work in terms of reflect.Values so that it is possible
	// to write code without knowing beforehand what the types
	// will be.
	swap := func(in []reflect.Value) []reflect.Value {
		//newV := reflect.New(val.Elem().Type())
		//newV.Field(0).Elem().Set(reflect.ValueOf("creator"))
		return []reflect.Value{val}
	}
	fnVal := reflect.ValueOf(&creator).Elem()
	// Make a function of the right type.
	v := reflect.MakeFunc(fnVal.Type(), swap)
	fnVal.Set(v)

	for i := 0; i < 3; i++ {
		//c.(*ctrl).Name = fmt.Sprintf("%s%d New", c.(*ctrl).Name, i)
		c = &ctrl{Name: fmt.Sprintf("%s%d New", "Test", i), MidWare: &midWare{name: "User"}}
		fmt.Println(creator())
	}

}

func Aaa() {

}

func TestHandler4(t *testing.T) {
	name := router.GetFuncName(Aaa)
	hash := int(crc32.ChecksumIEEE([]byte(name)))
	fmt.Println(name)
	fmt.Println(hash)
}

func TestHandler3(t *testing.T) {
	//var c interface{}
	//c = &ctrl{Name: "Test", MidWare: &midWare{name: "User"}}
	//val := reflect.ValueOf(c)

	fmt.Println(reflect.TypeOf(&ctrl{}).Elem().Name())

	fn := ctrl.String
	v := reflect.ValueOf(fn)

	rawT := v.Type().In(0)
	newV := reflect.New(rawT)
	model := newV.Interface()
	mV := reflect.ValueOf(model.(*ctrl).String)
	fmt.Println(mV.Type().Name(), mV.Type().String())
	//mm := newV.Elem().FieldByName("String").Interface().(func(...interface{}) string)
	//mmModel := mV.Interface()
	//mm := mmModel.(func(...interface{}) string)

	// 实现函数
	var creator func(...interface{}) string

	swap := func(in []reflect.Value) []reflect.Value {
		//newV := reflect.New(val.Elem().Type())
		//newV.Field(0).Elem().Set(reflect.ValueOf("creator"))
		return []reflect.Value{val}
	}
	fnVal := reflect.ValueOf(&creator).Elem()
	// Make a function of the right type.
	vv := reflect.MakeFunc(mV.Type(), swap)
	fnVal.Set(vv)

	for i := 0; i < 3; i++ {
		//c.(*ctrl).Name = fmt.Sprintf("%s%d New", c.(*ctrl).Name, i)
		model = &ctrl{Name: fmt.Sprintf("%s%d New", "Test", i), MidWare: &midWare{name: "User"}}
		fmt.Println(model.(*ctrl))
	}

}
