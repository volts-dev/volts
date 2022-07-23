package router

import (
	"reflect"
	"unsafe"
)

const (
	//kindDirectIface = 1 << 5
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

func (t *rtype) Kind() reflect.Kind { return reflect.Kind(t.kind & kindMask) }

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
