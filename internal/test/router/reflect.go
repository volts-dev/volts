package router

import (
	"reflect"
	"sync"
	"unsafe"
)

const (
	kindDirectIface = 1 << 5
	//kindGCProg      = 1 << 6 // Type.gc points to GC program
	kindMask = (1 << 5) - 1
	// tflagUncommon means that there is a pointer, *uncommonType,
	// just beyond the outer type structure.
	//
	// For example, if t.Kind() == Struct and t.tflag&tflagUncommon != 0,
	// then t has uncommonType data and it can be accessed as:
	//
	//	type tUncommon struct {
	//		structType
	//		u uncommonType
	//	}
	//	u := &(*tUncommon)(unsafe.Pointer(t)).u
	tflagUncommon tflag = 1 << 0
)
const (
	flagKindWidth        = 5 // there are 27 kinds
	flagKindMask    flag = 1<<flagKindWidth - 1
	flagStickyRO    flag = 1 << 5
	flagEmbedRO     flag = 1 << 6
	flagIndir       flag = 1 << 7
	flagAddr        flag = 1 << 8
	flagMethod      flag = 1 << 9
	flagMethodShift      = 10
	flagRO          flag = flagStickyRO | flagEmbedRO
)

type flag uintptr

func (f flag) kind() reflect.Kind {
	return reflect.Kind(f & flagKindMask)
}

func (f flag) ro() flag {
	if f&flagRO != 0 {
		return flagStickyRO
	}
	return 0
}

type nameOff int32 // offset to a name
type typeOff int32 // offset to an *rtype
type textOff int32 // offset from top of text section

// tflag is used by an rtype to signal what extra type information is
// available in the memory directly following the rtype value.
//
// tflag values must be kept in sync with copies in:
//	cmd/compile/internal/reflectdata/reflect.go
//	cmd/link/internal/ld/decodesym.go
//	runtime/type.go
type tflag uint8

// String is the runtime representation of a string.
// It cannot be used safely or portably and its representation may
// change in a later release.
//
// Unlike reflect.StringHeader, its Data field is sufficient to guarantee the
// data it references will not be garbage collected.
type String struct {
	Data unsafe.Pointer
	Len  int
}

// name is an encoded type name with optional extra data.
//
// The first byte is a bit field containing:
//
//	1<<0 the name is exported
//	1<<1 tag data follows the name
//	1<<2 pkgPath nameOff follows the name and tag
//
// Following that, there is a varint-encoded length of the name,
// followed by the name itself.
//
// If tag data is present, it also has a varint-encoded length
// followed by the tag itself.
//
// If the import path follows, then 4 bytes at the end of
// the data form a nameOff. The import path is only set for concrete
// methods that are defined in a different package than their type.
//
// If a name starts with "*", then the exported bit represents
// whether the pointed to type is exported.
//
// Note: this encoding must match here and in:
//   cmd/compile/internal/reflectdata/reflect.go
//   runtime/type.go
//   internal/reflectlite/type.go
//   cmd/link/internal/ld/decodesym.go

type name struct {
	bytes *byte
}
type Value struct {
	typ *rtype
	ptr unsafe.Pointer
	flag
}

// imethod represents a method on an interface type
type imethod struct {
	name nameOff // name of method
	typ  typeOff // .(*FuncType) underneath
}

// interfaceType represents an interface type.
type interfaceType struct {
	rtype
	pkgPath name      // import path
	methods []imethod // sorted by hash
}

func (n name) data(off int, whySafe string) *byte {
	return (*byte)(add(unsafe.Pointer(n.bytes), uintptr(off), whySafe))
}

// readVarint parses a varint as encoded by encoding/binary.
// It returns the number of encoded bytes and the encoded value.
func (n name) readVarint(off int) (int, int) {
	v := 0
	for i := 0; ; i++ {
		x := *n.data(off+i, "read varint")
		v += int(x&0x7f) << (7 * i)
		if x&0x80 == 0 {
			return i + 1, v
		}
	}
}
func (n name) name() (s string) {
	if n.bytes == nil {
		return
	}
	i, l := n.readVarint(1)
	hdr := (*String)(unsafe.Pointer(&s))
	hdr.Data = unsafe.Pointer(n.data(1+i, "non-empty string"))
	hdr.Len = l
	return
}

type (
	// rtype is the common implementation of most values.
	// It is embedded in other struct types.
	//
	// rtype must be kept in sync with ../runtime/type.go:/^type._type.
	rtype struct {
		size       uintptr
		ptrdata    uintptr // number of bytes in the type that can contain pointers
		hash       uint32  // hash of type; avoids computation in hash tables
		tflag      tflag   // extra type information flags
		align      uint8   // alignment of variable with this type
		fieldAlign uint8   // alignment of struct field with this type
		kind       uint8   // enumeration for C
		// function for comparing objects of this type
		// (ptr to object A, ptr to object B) -> ==?
		equal     func(unsafe.Pointer, unsafe.Pointer) bool
		gcdata    *byte   // garbage collection data
		str       nameOff // string form
		ptrToThis typeOff // type for pointer to this type, may be zero
	}

	// uncommonType is present only for defined types or types with methods
	// (if T is a defined type, the uncommonTypes for T and *T have methods).
	// Using a pointer to this struct reduces the overall size required
	// to describe a non-defined type with no methods.
	uncommonType struct {
		pkgPath nameOff // import path; empty for built-in types like int, string
		mcount  uint16  // number of methods
		xcount  uint16  // number of exported methods
		moff    uint32  // offset from this uncommontype to [mcount]method
		_       uint32  // unused
	}
	// Method on non-interface type
	method struct {
		name nameOff // name of method
		mtyp typeOff // method type (without receiver)
		ifn  textOff // fn used in interface call (one-word receiver)
		tfn  textOff // fn used for normal method call
	}

	// ptrType represents a pointer type.
	ptrType struct {
		rtype
		elem *rtype // pointer element (pointed at) type
	}
)

// ValueOf returns a new Value initialized to the concrete value
// stored in the interface i. ValueOf(nil) returns the zero Value.
func ValueOf(i interface{}) Value {
	if i == nil {
		return Value{}
	}

	// TODO: Maybe allow contents of a Value to live on the stack.
	// For now we make the contents always escape to the heap. It
	// makes life easier in a few places (see chanrecv/mapassign
	// comment below).
	//escapes(i)

	e := (*emptyInterface)(unsafe.Pointer(&i))
	// NOTE: don't read e.word until we know whether it is really a pointer or not.
	t := e.typ
	if t == nil {
		return Value{}
	}
	f := flag(t.Kind())
	if t.kind&kindDirectIface == 0 {
		f |= flagIndir
	}

	//return *(*reflect.Value)(unsafe.Pointer(&Value{t, e.word, f}))
	return Value{t, e.word, f}
}

// MethodByName returns a function value corresponding to the method
// of v with the given name.
// The arguments to a Call on the returned function should not include
// a receiver; the returned function will always use v as the receiver.
// It returns the zero Value if no method was found.
func (v Value) MethodByName(name string) Value {
	if v.typ == nil {
		//panic(&ValueError{"reflect.Value.MethodByName", Invalid})
	}
	if v.flag&flagMethod != 0 {
		return Value{}
	}
	m, ok := v.typ.MethodByName(name)
	if !ok {
		return Value{}
	}
	return v.Method(m.Index)
}

// Interface returns v's current value as an interface{}.
// It is equivalent to:
//	var i interface{} = (v's underlying value)
// It panics if the Value was obtained by accessing
// unexported struct fields.
func (v Value) Interface() (i interface{}) {
	return valueInterface(v, true)
}

// ifaceIndir reports whether t is stored indirectly in an interface value.
func ifaceIndir(t *rtype) bool {
	return t.kind&kindDirectIface == 0
}

//go:linkname unsafe_New reflect.unsafe_New
func unsafe_New(rtype unsafe.Pointer) unsafe.Pointer

//go:linkname typedmemmove reflect.typedmemmove
func typedmemmove(rtype unsafe.Pointer, dst, src unsafe.Pointer)

//go:linkname unsafe_NewArray reflect.unsafe_NewArray
func unsafe_NewArray(rtype unsafe.Pointer, length int) unsafe.Pointer

// packEface converts v to the empty interface.
func packEface(v Value) any {
	t := v.typ
	var i any
	e := (*emptyInterface)(unsafe.Pointer(&i))
	// First, fill in the data portion of the interface.
	switch {
	case ifaceIndir(t):
		if v.flag&flagIndir == 0 {
			panic("bad indir")
		}
		// Value is indirect, and so is the interface we're making.
		ptr := v.ptr
		if v.flag&flagAddr != 0 {
			// TODO: pass safe boolean from valueInterface so
			// we don't need to copy if safe==true?
			c := unsafe_New((unsafe.Pointer)(t))
			typedmemmove((unsafe.Pointer)(t), c, ptr)
			ptr = c
		}
		e.word = ptr
	case v.flag&flagIndir != 0:
		// Value is indirect, but interface is direct. We need
		// to load the data at v.ptr into the interface data word.
		e.word = *(*unsafe.Pointer)(v.ptr)
	default:
		// Value is direct, and so is the interface.
		e.word = v.ptr
	}
	// Now, fill in the type portion. We're very careful here not
	// to have any operation between the e.word and e.typ assignments
	// that would let the garbage collector observe the partially-built
	// interface value.
	e.typ = t
	return i
}

func valueInterface(v Value, safe bool) any {
	if v.flag == 0 {
		//panic(&ValueError{"reflect.Value.Interface", Invalid})
	}
	if safe && v.flag&flagRO != 0 {
		// Do not allow access to unexported values via Interface,
		// because they might be pointers that should not be
		// writable or methods or function that should not be callable.
		panic("reflect.Value.Interface: cannot return value obtained from unexported field or method")
	}
	//// 本身就是个 method
	if v.flag&flagMethod != 0 {
		//v = makeMethodValue("Interface", v)
	}
	/*
		if v.kind() == reflect.Interface {
			// Special case: return the element inside the interface.
			// Empty interface has one layout, all interfaces with
			// methods have a second layout.
			if v.NumMethod() == 0 {
				return *(*any)(v.ptr)
			}
			return *(*interface {
				M()
			})(v.ptr)
		}
	*/
	// TODO: pass safe to packEface so we don't need to copy if safe==true?
	return packEface(v)
}

// Method returns a function value corresponding to v's i'th method.
// The arguments to a Call on the returned function should not include
// a receiver; the returned function will always use v as the receiver.
// Method panics if i is out of range or if v is a nil interface value.
func (v Value) Method(i int) Value {
	if v.typ == nil {
		//panic(&ValueError{"reflect.Value.Method", Invalid})
	}
	if v.flag&flagMethod != 0 || uint(i) >= uint(v.typ.NumMethod()) {
		panic("reflect: Method index out of range")
	}
	if v.typ.Kind() == reflect.Interface && v.IsNil() {
		panic("reflect: Method on nil interface value")
	}
	fl := v.flag.ro() | (v.flag & flagIndir)
	fl |= flag(reflect.Func)
	fl |= flag(i)<<flagMethodShift | flagMethod
	return Value{v.typ, v.ptr, fl}
}

// IsNil reports whether its argument v is nil. The argument must be
// a chan, func, interface, map, pointer, or slice value; if it is
// not, IsNil panics. Note that IsNil is not always equivalent to a
// regular comparison with nil in Go. For example, if v was created
// by calling ValueOf with an uninitialized interface variable i,
// i==nil will be true but v.IsNil will panic as v will be the zero
// Value.
func (v Value) IsNil() bool {
	k := v.kind()
	switch k {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.UnsafePointer:
		if v.flag&flagMethod != 0 {
			return false
		}
		ptr := v.ptr
		if v.flag&flagIndir != 0 {
			ptr = *(*unsafe.Pointer)(ptr)
		}
		return ptr == nil
	case reflect.Interface, reflect.Slice:
		// Both interface and slice are nil if first word is 0.
		// Both are always bigger than a word; assume flagIndir.
		return *(*unsafe.Pointer)(v.ptr) == nil
	}
	panic(&reflect.ValueError{Method: "reflect.Value.IsNil", Kind: v.kind()})
}
func (t *rtype) Kind() reflect.Kind { return reflect.Kind(t.kind & kindMask) }

//go:linkname resolveTypeOff reflect.resolveTypeOff
func resolveTypeOff(rtype unsafe.Pointer, off int32) unsafe.Pointer

// ptrMap is the cache for PointerTo.
var ptrMap sync.Map // map[*rtype]*ptrType
func (t *rtype) typeOff(off typeOff) *rtype {
	return (*rtype)(resolveTypeOff(unsafe.Pointer(t), int32(off)))
}
func (t *rtype) uncommon() *uncommonType {
	if t.tflag&tflagUncommon == 0 {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		//return &(*structTypeUncommon)(unsafe.Pointer(t)).u
		return nil
	case reflect.Pointer:
		type u struct {
			ptrType
			u uncommonType
		}
		return &(*u)(unsafe.Pointer(t)).u

	case reflect.Func:
		return nil
		/*		type u struct {
						funcType
						u uncommonType
					}
					return &(*u)(unsafe.Pointer(t)).u
				case Slice:
					type u struct {
						sliceType
						u uncommonType
					}
					return &(*u)(unsafe.Pointer(t)).u
				case Array:
					type u struct {
						arrayType
						u uncommonType
					}
					return &(*u)(unsafe.Pointer(t)).u
				case Chan:
					type u struct {
						chanType
						u uncommonType
					}
					return &(*u)(unsafe.Pointer(t)).u
				case Map:
					type u struct {
						mapType
						u uncommonType
					}
					return &(*u)(unsafe.Pointer(t)).u
				case Interface:
					type u struct {
						interfaceType
						u uncommonType
					}
					return &(*u)(unsafe.Pointer(t)).u
		*/
	default:
		type u struct {
			rtype
			u uncommonType
		}
		return &(*u)(unsafe.Pointer(t)).u
	}
}

func resolveNameOff(ptrInModule unsafe.Pointer, off nameOff) name {
	if off == 0 {
		return name{}
	}
	base := uintptr(ptrInModule)
	for md := &Firstmoduledata; md != nil; md = md.next {
		if base >= md.types && base < md.etypes {
			res := md.types + uintptr(off)
			if res > md.etypes {
				//println("runtime: nameOff", hex(off), "out of range", hex(md.types), "-", hex(md.etypes))
				//throw("runtime: name offset out of range")
			}
			return name{(*byte)(unsafe.Pointer(res))}
		}
	}
	return name{}
	// No module found. see if it is a run time name.
	/*
		reflectOffsLock()
		res, found := reflectOffs.m[int32(off)]
		reflectOffsUnlock()
		if !found {
			println("runtime: nameOff", hex(off), "base", hex(base), "not in ranges:")
			for next := &firstmoduledata; next != nil; next = next.next {
				println("\ttypes", hex(next.types), "etypes", hex(next.etypes))
			}
			throw("runtime: name offset base pointer out of range")
		}

		return name{(*byte)(res)}
	*/
}

func reflect_resolveNameOff(ptrInModule unsafe.Pointer, off int32) unsafe.Pointer {
	return unsafe.Pointer(resolveNameOff(ptrInModule, nameOff(off)).bytes)
}

func (t *rtype) nameOff(off nameOff) name {
	return name{(*byte)(reflect_resolveNameOff(unsafe.Pointer(t), int32(off)))}
}

func (t *rtype) exportedMethods() []method {
	ut := t.uncommon()
	if ut == nil {
		return nil
	}
	return ut.exportedMethods()
}

func (t *rtype) NumMethod() int {
	if t.Kind() == reflect.Interface {
		tt := (*interfaceType)(unsafe.Pointer(t))
		return tt.NumMethod()
	}
	return len(t.exportedMethods())
}

func (t *rtype) SetMethodByName(name, newName string) {
	if t.Kind() == reflect.Interface {
		//tt := (*interfaceType)(unsafe.Pointer(t))
		//return tt.MethodByName(name)
	}
	ut := t.uncommon()

	// TODO(mdempsky): Binary search.
	for _, p := range ut.exportedMethods() {
		if t.nameOff(p.name).name() == name {
			//return t.Method(i), true
			//str:=[]byte{'a','d'}
			//t.nameOff(p.name).bytes =  (*byte)(unsafe.Pointer(&str))
		}
	}

}

func (t *rtype) MethodByName(name string) (m reflect.Method, ok bool) {
	if t.Kind() == reflect.Interface {
		//tt := (*interfaceType)(unsafe.Pointer(t))
		//return tt.MethodByName(name)
	}
	ut := t.uncommon()
	if ut == nil {
		return reflect.Method{}, false
	}
	// TODO(mdempsky): Binary search.
	for _, p := range ut.exportedMethods() {
		if t.nameOff(p.name).name() == name {
			//return t.Method(i), true
		}
	}
	return reflect.Method{}, false
}

// add returns p+x.
//
// The whySafe string is ignored, so that the function still inlines
// as efficiently as p+x, but all call sites should use the string to
// record why the addition is safe, which is to say why the addition
// does not cause x to advance to the very end of p's allocation
// and therefore point incorrectly at the next block in memory.
func add(p unsafe.Pointer, x uintptr, whySafe string) unsafe.Pointer {
	return unsafe.Pointer(uintptr(p) + x)
}

func (t *uncommonType) methods() []method {
	if t.mcount == 0 {
		return nil
	}
	return (*[1 << 16]method)(add(unsafe.Pointer(t), uintptr(t.moff), "t.mcount > 0"))[:t.mcount:t.mcount]
}

func (t *uncommonType) exportedMethods() []method {
	if t.xcount == 0 {
		return nil
	}
	return (*[1 << 16]method)(add(unsafe.Pointer(t), uintptr(t.moff), "t.xcount > 0"))[:t.xcount:t.xcount]
}
