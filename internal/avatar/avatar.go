package avatar

import (
	"reflect"
	"unsafe"
)

type (
	Avatar struct {
		model interface{}
		body  reflect.Value
		//typ    reflect.Type
		fields []interface{}
	}
	emptyInterface struct {
		typ  *struct{}
		word unsafe.Pointer
	}
)

func New(target reflect.Value) *Avatar {
	val := target.Elem()
	////t := reflect.TypeOf(val)
	return &Avatar{
		//	model: target,
		body: val,
		//typ:  target,
	}
}

func (self *Avatar) SetFields(fields ...interface{}) {
	self.fields = append(self.fields, fields...)
}

func (self *Avatar) swap() func(in []reflect.Value) []reflect.Value {
	return func(in []reflect.Value) []reflect.Value {
		newV := reflect.New(self.body.Type()).Elem()
		//p := newV.Interface()
		//ptr0 := uintptr((*emptyInterface)(unsafe.Pointer(&p)).word)
		//ptr1 := ptr0 + self.typ.Field(0).Offset
		//*((*string)(unsafe.Pointer(ptr0))) = "fadf" //self.fields[0]
		//var i interface{} = &midWare{name: "fdfd"}

		//*((*interface{})(unsafe.Pointer(ptr1))) = &self.fields[1]
		for i := 0; i < newV.NumField(); i++ {
			newV.Field(i).Set(reflect.ValueOf(self.fields[i]))
		}
		return []reflect.Value{newV}
	}
}

func (self *Avatar) NewCreator() func() interface{} {
	var creator func() interface{}
	fnVal := reflect.ValueOf(&creator).Elem()
	// Make a function of the right type.
	v := reflect.MakeFunc(fnVal.Type(), self.swap())
	fnVal.Set(v)
	return creator
}
