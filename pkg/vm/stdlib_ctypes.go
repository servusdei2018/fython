package vm

import (
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

// _ctypes accelerator: pure-Go implementation of CPython's _ctypes C module
// sufficient for upstream Lib/ctypes/ with zero overlays.
//
// Design: every C data object is a KindNative Go struct holding a raw byte
// buffer. C addresses are &buf[0]; lifetime is kept by the Python reference
// (Go heap does not move, so the pointer stays valid while alive).

type pySimpleCData struct {
	Header val.ObjectHeader
	Type   *val.PyType
	Buf    []byte
	RefObj val.Value // for c_char_p/c_void_p holding a referenced bytes/str object
	Pinned bool
}

type pyCStruct struct {
	Header val.ObjectHeader
	Type   *val.PyType
	Buf    []byte
}

type pyCArray struct {
	Header   val.ObjectHeader
	Type     *val.PyType
	Buf      []byte
	Count    int
	ItemType val.Value
}

type pyCPointer struct {
	Header val.ObjectHeader
	Type   *val.PyType
	Target val.Value // pointed-to object (may be None for null)
	Addr   uintptr   // raw address if not wrapping an object
	// When Target is set, Addr is &Target.Buf[0] cached at creation.
}

type pyCFuncPtr struct {
	Header   val.ObjectHeader
	Type     *val.PyType
	Addr     uintptr
	Name     string
	Handle   uintptr   // dlopen handle for dlsym
	Callable val.Value // Python callable for CFUNCTYPE wrappers
	Callback uintptr   // Go callback address for Callable (if any)
	ArgTypes []val.Value
	RestType val.Value
	Flags    int64
	NameErr  bool
}

var (
	ctypesOnce        sync.Once
	ctypesInstallOnce sync.Once
	argumentErrorTyp  *val.PyType
	simpleCDataTyp    *val.PyType
	structTyp         *val.PyType
	unionTyp          *val.PyType
	arrayTyp          *val.PyType
	pointerTyp        *val.PyType // _Pointer base
	cfuncPtrTyp       *val.PyType // CFuncPtr base
	cfieldTyp         *val.PyType
	// errno storage for use_errno (thread-local via GIL; global for MVP, guarded for race detector).
	ctypesErrnoMu sync.Mutex
	ctypesErrno   int64
)

func getCTypesErrno() int64 {
	ctypesErrnoMu.Lock()
	defer ctypesErrnoMu.Unlock()
	return ctypesErrno
}
func setCTypesErrno(v int64) {
	ctypesErrnoMu.Lock()
	defer ctypesErrnoMu.Unlock()
	ctypesErrno = v
}

func initCTypesTypes() {
	ctypesOnce.Do(func() {
		argumentErrorTyp = val.MustNewType("ArgumentError", []*val.PyType{val.ExceptionType}, nil)
		simpleCDataTyp = val.MustNewType("_SimpleCData", []*val.PyType{val.ObjectType}, nil)
		// Structure / Union are distinct types so isinstance checks in ctypes work.
		structTyp = val.MustNewType("Structure", []*val.PyType{val.ObjectType}, nil)
		unionTyp = val.MustNewType("Union", []*val.PyType{val.ObjectType}, nil)
		arrayTyp = val.MustNewType("Array", []*val.PyType{val.ObjectType}, nil)
		pointerTyp = val.MustNewType("_Pointer", []*val.PyType{val.ObjectType}, nil)
		cfuncPtrTyp = val.MustNewType("CFuncPtr", []*val.PyType{val.ObjectType}, nil)
		cfieldTyp = val.MustNewType("CField", []*val.PyType{val.ObjectType}, nil)
	})
}

// helpers

func typeCodeOf(t val.Value) string {
	if t.IsType() {
		tt := t.AsType()
		if tt.Dict != nil {
			if v, ok := tt.Dict.Get(val.InternString("_type_")); ok && v.IsAnyString() {
				return v.AsString()
			}
		}
	}
	return ""
}

func sizeofTypeCode(code string) int {
	if strings.HasPrefix(code, "Z") {
		if len(code) >= 2 {
			switch code[1] {
			case 'f':
				return 8
			case 'd':
				return 16
			case 'g':
				return 32
			}
		}
		return 16
	}
	switch code {
	case "c", "b", "B", "?":
		return 1
	case "h", "H":
		return 2
	case "i", "I", "f":
		return 4
	case "l", "L", "q", "Q", "d", "P", "O", "Z", "u":
		// P (pointer), O (py_object), Z (wchar_p), u (wchar) are 8 on LP64
		return 8
	case "g":
		return 16 // c_longdouble long double (x87 80-bit stored as 16B) — matches sizeof(c_double) check fallback
	case "z":
		return 8 // c_char_p pointer
	default:
		return 8
	}
}

func alignmentOfTypeCode(code string) int {
	sz := sizeofTypeCode(code)
	if sz < 8 {
		return sz
	}
	return 8
}

func getTypeAttrStr(t *val.PyType, name string) (string, bool) {
	if t == nil || t.Dict == nil {
		return "", false
	}
	mroVal, _, ok := t.LookupMRO(val.InternString(name))
	if !ok {
		return "", false
	}
	if mroVal.IsAnyString() {
		return mroVal.AsString(), true
	}
	return "", false
}

// layoutField describes one Structure field.
type layoutField struct {
	Name   string
	Type   val.Value // type object or Array type
	Offset int
	Size   int
	Align  int
}

type structLayout struct {
	Fields []layoutField
	Size   int
	Align  int
	Order  []string
}

func computeStructLayout(t *val.PyType, isUnion bool) (*structLayout, error) {
	// Read _fields_ from type dict (or MRO).
	fieldsVal, _, ok := t.LookupMRO(val.InternString("_fields_"))
	if !ok {
		return &structLayout{Size: 0, Align: 1}, nil
	}
	if fieldsVal.IsNone() {
		return &structLayout{Size: 0, Align: 1}, nil
	}
	// _fields_ must be tuple/list of tuples.
	var items []val.Value
	switch {
	case fieldsVal.IsTuple():
		items = fieldsVal.AsTuple().Items
	case fieldsVal.IsList():
		items = fieldsVal.AsList().Items
	default:
		return nil, errSyntax("ctypes: _fields_ must be tuple/list")
	}
	layout := &structLayout{Align: 1}
	offset := 0
	maxAlign := 1
	for _, f := range items {
		// (name, type) or (name, type, count)
		var name string
		var typ val.Value
		if !f.IsTuple() {
			return nil, errSyntax("ctypes: invalid _fields_ entry")
		}
		tup := f.AsTuple().Items
		if len(tup) < 2 {
			return nil, errSyntax("ctypes: invalid _fields_ entry")
		}
		name = tup[0].AsString()
		typ = tup[1]
		// Array field: (name, type, count) computes size = sizeof(type)*count.
		sz := sizeofOfValue(typ)
		align := alignmentOfValue(typ)
		if len(tup) >= 3 {
			count := int(tup[2].AsInt64())
			sz *= count
		}
		if align > maxAlign {
			maxAlign = align
		}
		if !isUnion {
			// Align offset up.
			if offset%align != 0 {
				offset += align - (offset % align)
			}
		} else {
			offset = 0
		}
		layout.Fields = append(layout.Fields, layoutField{Name: name, Type: typ, Offset: offset, Size: sz, Align: align})
		if !isUnion {
			offset += sz
		} else if sz > layout.Size {
			layout.Size = sz
		}
		layout.Order = append(layout.Order, name)
	}
	if !isUnion {
		layout.Size = offset
	}
	// Round size up to maxAlign.
	if maxAlign > 0 && layout.Size%maxAlign != 0 {
		layout.Size += maxAlign - (layout.Size % maxAlign)
	}
	layout.Align = maxAlign
	return layout, nil
}

func errSyntax(msg string) error { return &ctypesError{msg} }

type ctypesError struct{ msg string }

func (e *ctypesError) Error() string { return e.msg }

func sizeofOfValue(v val.Value) int {
	if v.IsType() {
		t := v.AsType()
		// Structure/Union: compute layout size.
		if t == structTyp || t == unionTyp || val.IsSubclass(t, structTyp) || val.IsSubclass(t, unionTyp) {
			layout, err := computeStructLayout(t, t == unionTyp || val.IsSubclass(t, unionTyp))
			if err != nil {
				return 0
			}
			return layout.Size
		}
		// Array types: created via type * count — store _ctypes_array_size_? Fallback to _type_ code.
		if code := typeCodeOf(v); code != "" {
			// Array types have _length_ attr? Check _fields? For array, _type_ may be array element code.
			// We store explicit _ctypes_size_ if present.
			if szStr, ok := getTypeAttrStr(t, "_ctypes_size_"); ok && szStr != "" {
				_ = szStr
			}
			return sizeofTypeCode(code)
		}
		// Pointer types: _type_ is pointer size ("P").
		return 8
	}
	// Instance: sizeof via type.
	return sizeofOfValue(val.TypeVal(v.Type()))
}

func alignmentOfValue(v val.Value) int {
	if v.IsType() {
		code := typeCodeOf(v)
		if code != "" {
			return alignmentOfTypeCode(code)
		}
		if t := v.AsType(); t != nil {
			// Struct alignment is max field alignment (at least 1).
			if layout, err := computeStructLayout(t, false); err == nil && layout.Align > 0 {
				return layout.Align
			}
		}
	}
	return 8
}

func (e *Engine) createCtypesLowerModule() val.Value {
	initCTypesTypes()
	modVal := val.NewModuleVal("_ctypes", "Pure-Go _ctypes accelerator")
	mod := modVal.AsModule()
	set := func(name string, v val.Value) { mod.Dict.Set(val.InternString(name), v) }
	setF := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewModuleFuncVal(name, fn))
	}

	// --- Types ---
	set("ArgumentError", val.TypeVal(argumentErrorTyp))
	set("_SimpleCData", val.TypeVal(simpleCDataTyp))
	set("Structure", val.TypeVal(structTyp))
	set("Union", val.TypeVal(unionTyp))
	set("Array", val.TypeVal(arrayTyp))
	set("_Pointer", val.TypeVal(pointerTyp))
	set("CFuncPtr", val.TypeVal(cfuncPtrTyp))
	set("CField", val.TypeVal(cfieldTyp))

	// Constants
	set("RTLD_LOCAL", val.Int64Val(0))
	set("RTLD_GLOBAL", val.Int64Val(1)) // 0x100 on Linux, use canonical
	// Use real RTLD_GLOBAL=256 on linux; ctypes only compares for repr? Use 256.
	mod.Dict.Set(val.InternString("RTLD_GLOBAL"), val.Int64Val(256))
	set("SIZEOF_TIME_T", val.Int64Val(8))
	set("FUNCFLAG_CDECL", val.Int64Val(1))
	set("FUNCFLAG_PYTHONAPI", val.Int64Val(2))
	set("FUNCFLAG_USE_ERRNO", val.Int64Val(4))
	set("FUNCFLAG_USE_LASTERROR", val.Int64Val(8))
	set("FUNCFLAG_STDCALL", val.Int64Val(16))

	// --- sizeof / alignment ---
	setF("sizeof", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: sizeof() takes 1 argument")
		}
		return val.Int64Val(int64(sizeofOfValue(args[0])))
	})
	setF("alignment", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: alignment() takes 1 argument")
		}
		return val.Int64Val(int64(alignmentOfValue(args[0])))
	})
	// sf: get_errno / set_errno
	setF("get_errno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// Errno is per-thread in CPython; global for MVP.
		return val.Int64Val(val.Int64Val(0).AsInt64()) // placeholder: return stored
	})
	// Actually store errno globally.
	setF("set_errno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) >= 1 {
			setCTypesErrno(args[0].AsInt64())
		}
		return val.NoneVal()
	})
	// Fix get_errno to return stored.
	mod.Dict.Set(val.InternString("get_errno"), val.NewModuleFuncVal("get_errno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(getCTypesErrno())
	}))
	setF("get_last_error", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(getCTypesErrno())
	})
	setF("set_last_error", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) >= 1 {
			setCTypesErrno(args[0].AsInt64())
		}
		return val.NoneVal()
	})

	// dlopen / LoadLibrary
	setF("dlopen", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if vmInstance, ok := ctx.VM.(*VM); ok && vmInstance != nil && vmInstance.sandboxBlocksNative() {
			panic("PermissionError: native extensions and ctypes are disabled by sandbox policy")
		}
		var name string
		if len(args) >= 1 && !args[0].IsNone() {
			if args[0].IsAnyString() {
				name = args[0].AsString()
			} else if args[0].IsBytes() {
				name = string(args[0].AsBytes().Data)
			}
		}
		handle, err := dlopenForCTypes(name)
		if err != nil {
			exc := val.NewException(argumentErrorTyp, err.Error())
			panic(exc)
		}
		// Store in a handle registry so CFuncPtr can resolve.
		registerCTypesLib(handle)
		return val.Int64Val(int64(handle))
	})
	setF("LoadLibrary", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if vmInstance, ok := ctx.VM.(*VM); ok && vmInstance != nil && vmInstance.sandboxBlocksNative() {
			panic("PermissionError: native extensions and ctypes are disabled by sandbox policy")
		}
		var name string
		if len(args) >= 1 && args[0].IsAnyString() {
			name = args[0].AsString()
		}
		handle, err := dlopenForCTypes(name)
		if err != nil {
			panic(err.Error())
		}
		registerCTypesLib(handle)
		return val.Int64Val(int64(handle))
	})
	setF("dllist", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// List of loaded libs (for ctypes.util). Return empty list + libc.
		return val.NewListVal()
	})

	// buffer_info(type) -> dict? _layout uses buffer_info to get format. Return dict with _type_.
	setF("buffer_info", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := val.NewDictVal(4).AsDict()
		if len(args) >= 1 {
			code := typeCodeOf(args[0])
			if code == "" {
				code = "B"
			}
			d.Set(val.InternString("_type_"), val.NewStringVal(code))
		}
		return val.DictVal(d)
	})

	// addressof / byref / resize
	setF("addressof", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: addressof() takes 1 argument")
		}
		return val.Int64Val(addressOfValue(args[0]))
	})
	setF("byref", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: byref() takes 1 argument")
		}
		return makeByRefValue(args[0])
	})
	setF("resize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// No-op for MVP (arrays fixed size).
		return val.NoneVal()
	})

	// _memmove_addr etc. are integer address constants (not functions) in CPython _ctypes.
	set("_memmove_addr", val.Int64Val(int64(libcFuncAddr("memmove"))))
	set("_memset_addr", val.Int64Val(int64(libcFuncAddr("memset"))))
	set("_string_at_addr", val.Int64Val(-2))
	set("_cast_addr", val.Int64Val(-3))
	set("_memoryview_at_addr", val.Int64Val(-4))
	set("_wstring_at_addr", val.Int64Val(-5))

	// _check_HRESULT stub
	setF("_check_HRESULT", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	})
	setF("COMError", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	})
	// COM stubs
	mod.Dict.Set(val.InternString("FormatError"), val.NewModuleFuncVal("FormatError", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("")
	}))
	mod.Dict.Set(val.InternString("CopyComPointer"), val.NewModuleFuncVal("CopyComPointer", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	// Install type constructors for _SimpleCData, Structure, etc. (once per process; types are global).
	ctypesInstallOnce.Do(func() {
		installSimpleCDataConstructor()
		installStructureConstructor()
		installCFuncPtrConstructor()
		installPointerConstructor()
	})

	return modVal
}
