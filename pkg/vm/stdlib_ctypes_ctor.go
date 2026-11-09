package vm

import (
	"encoding/binary"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

func installSimpleCDataConstructor() {
	// _SimpleCData.__call__ handles instantiation of c_* types (subclasses defined in Python).
	// For heap types created via NewType, TpCall is used for instantiation.
	// Set Slots.TpCall on _SimpleCData to create simple instances.
	simpleCDataTyp.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		cls := callable.AsType()
		// Determine initial value: first positional arg or None.
		var initVal val.Value
		if len(args) > 0 {
			initVal = args[0]
		}
		// Allocate buffer.
		code := ""
		if v, _, ok := cls.LookupMRO(val.InternString("_type_")); ok && v.IsAnyString() {
			code = v.AsString()
		}
		sz := sizeofTypeCode(code)
		buf := make([]byte, sz)
		// Initialize from initVal if given.
		refObj := val.Value{}
		if initVal.HeaderPtr() != nil && !initVal.IsNone() {
			switch code {
			case "z", "P", "O":
				// Pointer / char_p: keep reference and store address.
				refObj = initVal
				// Store address in buf for P; for z, buf holds pointer to NUL-terminated data.
				if code == "z" {
					// Copy to keep-alive buffer and store its address.
					var data []byte
					switch {
					case initVal.IsBytes():
						data = initVal.AsBytes().Data
					case initVal.IsByteArray():
						data = initVal.AsByteArray().Data
					case initVal.IsAnyString():
						data = []byte(initVal.AsString())
					}
					if data != nil {
						cbuf := make([]byte, len(data)+1)
						copy(cbuf, data)
						keepCStr(cbuf)
						binary.LittleEndian.PutUint64(buf, uint64(uintptr(unsafe.Pointer(&cbuf[0]))))
					}
				} else {
					// P/O: initVal may be int address.
					binary.LittleEndian.PutUint64(buf, uint64(initVal.AsInt64()))
				}
			default:
				encodeSimpleValue(code, initVal, buf)
			}
		}
		obj := &pySimpleCData{Type: cls, Buf: buf, RefObj: refObj}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = cls
		res := val.ObjectVal(&obj.Header)
		// Support _init_ hook in subclass Dict (Python __init__).
		if initFn, _, ok := cls.LookupMRO(val.InternString("__init__")); ok {
			// Only call if it's not the default object.__init__.
			// To avoid recursion with this TpCall, call directly.
			if initFn.IsBuiltinFunc() {
				// Builtin __init__ (e.g., C-level); call with self+args.
				bufArgs := make([]val.Value, 1+len(args))
				bufArgs[0] = res
				copy(bufArgs[1:], args)
				initFn.AsBuiltinFunc().Fn(ctx, initFn, bufArgs, kwnames)
			}
		}
		return res
	}
	// __new__ for subclass instantiation (heap types clear TpCall on inheritance).
	{
		// Clone the TpCall body as __new__: __new__(cls, *args) -> instance
		// We reuse Slots.TpCall by calling it with (cls, args) as callable.
		simpleCDataTyp.Dict.Set(val.InternString("__new__"), val.NewBuiltinFuncVal("_SimpleCData.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 || !args[0].IsType() {
				panic("TypeError: __new__() missing type")
			}
			clsVal := args[0]
			// Delegate to current TpCall implementation with cls as callable.
			return simpleCDataTyp.Slots.TpCall(ctx, clsVal, args[1:], kwnames)
		}))
		if nv, ok := simpleCDataTyp.Dict.Get(val.InternString("__new__")); ok {
			nv.AsBuiltinFunc().NoBind = true
		}
		simpleCDataTyp.InvalidateVersion()
	}
	// value property: get/set via _type_ code.
	getter := val.NewBuiltinFuncVal("value.get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		if !isSimpleCData(self) {
			panic("TypeError: value descriptor on wrong type")
		}
		sc := asSimpleCData(self)
		code := typeCodeOf(val.TypeVal(self.Type()))
		// c_char_p special: RefObj bytes?
		if code == "z" {
			addr := binary.LittleEndian.Uint64(sc.Buf)
			if addr == 0 {
				return val.NoneVal()
			}
			// Read C string.
			ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
			var out []byte
			for i := range 1 << 20 {
				b := *(*byte)(unsafe.Add(ptr, i))
				if b == 0 {
					break
				}
				out = append(out, b)
			}
			return val.NewBytesVal(out)
		}
		return decodeSimpleValue(code, sc.Buf)
	})
	setter := val.NewBuiltinFuncVal("value.set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		v := args[1]
		sc := asSimpleCData(self)
		code := typeCodeOf(val.TypeVal(self.Type()))
		if code == "z" {
			// Store pointer to NUL-terminated copy, keep ref.
			sc.RefObj = v
			var data []byte
			switch {
			case v.IsBytes():
				data = v.AsBytes().Data
			case v.IsByteArray():
				data = v.AsByteArray().Data
			case v.IsAnyString():
				data = []byte(v.AsString())
			}
			if data == nil && (v.IsNone() || v.HeaderPtr() == nil) {
				for i := range sc.Buf {
					sc.Buf[i] = 0
				}
				return val.NoneVal()
			}
			cbuf := make([]byte, len(data)+1)
			copy(cbuf, data)
			keepCStr(cbuf)
			binary.LittleEndian.PutUint64(sc.Buf, uint64(uintptr(unsafe.Pointer(&cbuf[0]))))
			return val.NoneVal()
		}
		if code == "P" || code == "O" {
			// pointer: accept int or ctypes object address.
			var addr uintptr
			if v.IsInt64() {
				addr = uintptr(v.AsInt64())
			} else if !v.IsNone() && v.HeaderPtr() != nil {
				addr = uintptr(addressOfValue(v))
			}
			binary.LittleEndian.PutUint64(sc.Buf, uint64(addr))
			return val.NoneVal()
		}
		encodeSimpleValue(code, v, sc.Buf)
		return val.NoneVal()
	})
	simpleCDataTyp.Dict.Set(val.InternString("value"), val.NewPropertyVal(getter, setter, val.NoneVal()))
	// _mul_ for Array creation: c_int * 5 -> Array type.
	simpleCDataTyp.Dict.Set(val.InternString("__mul__"), val.NewBuiltinFuncVal("_SimpleCData.__mul__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// When called as unbound method via type (c_int * 5), args[0] is the type, args[1] is count.
		// When called via instance, not valid; still create array type.
		typ := args[0]
		count := args[1].AsInt64()
		return makeArrayType(typ, int(count))
	}))
	// __rmul__ = __mul__ (int * type).
	if v, ok := simpleCDataTyp.Dict.Get(val.InternString("__mul__")); ok {
		simpleCDataTyp.Dict.Set(val.InternString("__rmul__"), v)
	}
	// from_param / from_buffer / from_address helpers (required by ctypes/_reset_cache and __repr__ paths).
	fromParamFn := val.NewBuiltinFuncVal("from_param", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// args: [cls_or_self, value] when accessed as classmethod; or [value] when called as unbound? Handle both.
		// If first arg is a type, use it as cls; else use callable's owner? Simplify: if args[0] is type, use it.
		var cls *val.PyType
		var v val.Value
		switch {
		case len(args) >= 2 && args[0].IsType():
			cls = args[0].AsType()
			v = args[1]
		case len(args) >= 1:
			// Called as bound method with cls bound as first arg? Our builtin receives explicit args without binding,
			// so len==1 means direct value (no cls). Return value as-is.
			return args[0]
		default:
			return val.NoneVal()
		}
		// If v already is instance of cls (or subclass), return it.
		if v.HeaderPtr() != nil && v.Type() != nil && val.IsSubclass(v.Type(), cls) {
			return v
		}
		// Otherwise construct cls(v).
		return cls.Slots.TpCall(ctx, val.TypeVal(cls), []val.Value{v}, nil)
	})
	// Make it a classmethod-like descriptor: use ClassMethod wrapper so attribute access binds cls.
	simpleCDataTyp.Dict.Set(val.InternString("from_param"), val.NewClassMethodVal(fromParamFn))
	fromBufferFn := val.NewBuiltinFuncVal("from_buffer", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// from_buffer(source, offset=0): create view sharing source buffer. MVP: copy.
		var cls *val.PyType
		var src val.Value
		switch {
		case len(args) >= 2 && args[0].IsType():
			cls = args[0].AsType()
			src = args[1]
		case len(args) >= 1:
			return args[0]
		default:
			return val.NoneVal()
		}
		// src may be bytearray/memoryview/bytes; copy its bytes into new simple buffer.
		var data []byte
		switch {
		case src.IsBytes():
			data = append([]byte(nil), src.AsBytes().Data...)
		case src.IsByteArray():
			data = append([]byte(nil), src.AsByteArray().Data...)
		case src.IsMemoryView():
			data = append([]byte(nil), src.AsMemoryView().Buf...)
		default:
			data = []byte(src.String())
		}
		// Offset kw/pos: ignore for MVP (assume 0).
		sq := sizeofOfValue(val.TypeVal(cls))
		if len(data) < sq {
			buf := make([]byte, sq)
			copy(buf, data)
			data = buf
		}
		obj := &pySimpleCData{Type: cls, Buf: append([]byte(nil), data...)}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = cls
		return val.ObjectVal(&obj.Header)
	})
	simpleCDataTyp.Dict.Set(val.InternString("from_buffer"), val.NewClassMethodVal(fromBufferFn))
	simpleCDataTyp.Dict.Set(val.InternString("from_buffer_copy"), val.NewClassMethodVal(fromBufferFn))
	fromAddrFn := val.NewBuiltinFuncVal("from_address", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var cls *val.PyType
		var addr int64
		if len(args) >= 2 && args[0].IsType() {
			cls = args[0].AsType()
			addr = args[1].AsInt64()
		} else {
			return val.NoneVal()
		}
		sz := sizeofOfValue(val.TypeVal(cls))
		buf := make([]byte, sz)
		ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
		for i := range sz {
			buf[i] = *(*byte)(unsafe.Add(ptr, i))
		}
		obj := &pySimpleCData{Type: cls, Buf: buf}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = cls
		return val.ObjectVal(&obj.Header)
	})
	simpleCDataTyp.Dict.Set(val.InternString("from_address"), val.NewClassMethodVal(fromAddrFn))
	// Metaclass __mul__ hack: type.__mul__ for `c_int * 5` array creation (CPython uses PyCSimpleType metaclass).
	// Adding __mul__ to type globally is harmless (returns NotImplemented for non-ctypes types).
	if val.TypeType.Dict != nil {
		if _, exists := val.TypeType.Dict.Get(val.InternString("__mul__")); !exists {
			val.TypeType.Dict.Set(val.InternString("__mul__"), val.NewBuiltinFuncVal("type.__mul__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				// args: [cls, n] when called as type.__mul__(cls, n) via binary op dispatch.
				if len(args) < 2 {
					return val.NotImplementedVal()
				}
				clsVal := args[0]
				n := args[1]
				if !clsVal.IsType() {
					return val.NotImplementedVal()
				}
				cls := clsVal.AsType()
				if cls == nil {
					return val.NotImplementedVal()
				}
				// Only for ctypes simple/array/pointer types.
				isCType := (simpleCDataTyp != nil && val.IsSubclass(cls, simpleCDataTyp)) || (arrayTyp != nil && val.IsSubclass(cls, arrayTyp)) || (pointerTyp != nil && val.IsSubclass(cls, pointerTyp)) || (structTyp != nil && val.IsSubclass(cls, structTyp))
				if !isCType {
					return val.NotImplementedVal()
				}
				if !n.IsInt64() {
					panic("TypeError: array size must be int")
				}
				return makeArrayType(clsVal, int(n.AsInt64()))
			}))
			val.TypeType.InvalidateVersion()
		}
	}
	// __repr__
	simpleCDataTyp.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("_SimpleCData.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		v := getter.AsBuiltinFunc().Fn(ctx, getter, args, nil)
		return val.NewStringVal(self.Type().Name + "(" + v.String() + ")")
	}))
	_ = simpleCDataTyp
}

func installStructureConstructor() {
	// __call__ for instances? Structure instances are not callable; construction via __new__.
	// Set __new__ to delegate to TpCall so heap subclasses (via dunderNew) work.
	structTyp.Dict.Set(val.InternString("__new__"), val.NewBuiltinFuncVal("Structure.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: __new__() missing type")
		}
		return structTyp.Slots.TpCall(ctx, args[0], args[1:], kwnames)
	}))
	if nv, ok := structTyp.Dict.Get(val.InternString("__new__")); ok {
		nv.AsBuiltinFunc().NoBind = true
	}
	structTyp.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		cls := callable.AsType()
		// Union uses same logic with isUnion=true if cls is subclass of unionTyp.
		isUnion := (cls == unionTyp) || val.IsSubclass(cls, unionTyp)
		layout, _ := computeStructLayout(cls, isUnion)
		obj := &pyCStruct{Type: cls, Buf: make([]byte, layout.Size)}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = cls
		res := val.ObjectVal(&obj.Header)
		// Initialize fields from args (positional values for each field in order).
		for i, arg := range args {
			if i >= len(layout.Fields) {
				break
			}
			f := layout.Fields[i]
			writeStructField(res, f, arg)
		}
		// Keyword args: field=value.
		for ki, kn := range kwnames {
			name := kn.AsString()
			v := args[len(args)-len(kwnames)+ki]
			for _, f := range layout.Fields {
				if f.Name == name {
					writeStructField(res, f, v)
					break
				}
			}
		}
		return res
	}
	// Field access via __getattribute__/__setattr__ (heap subclasses overwrite Slots.TpGetAttr in NewType).
	structTyp.Dict.Set(val.InternString("__getattribute__"), val.NewBuiltinFuncVal("Structure.__getattribute__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		obj := args[0]
		name := args[1]
		if isCStruct(obj) {
			cls := obj.Type()
			layout, _ := computeStructLayout(cls, false)
			for _, f := range layout.Fields {
				if f.Name == name.AsString() {
					return readStructField(obj, f)
				}
			}
		}
		if name.AsString() == "__class__" {
			return val.TypeVal(obj.Type())
		}
		if v, _, ok := obj.Type().LookupMRO(val.InternString(name.AsString())); ok {
			if v.Type().Slots.TpDescrGet != nil {
				return v.Type().Slots.TpDescrGet(ctx, v, obj, obj.Type())
			}
			return v
		}
		panic("AttributeError: '" + obj.Type().Name + "' object has no attribute '" + name.AsString() + "'")
	}))
	structTyp.Dict.Set(val.InternString("__setattr__"), val.NewBuiltinFuncVal("Structure.__setattr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		obj := args[0]
		name := args[1]
		value := args[2]
		if isCStruct(obj) {
			cls := obj.Type()
			layout, _ := computeStructLayout(cls, false)
			for _, f := range layout.Fields {
				if f.Name == name.AsString() {
					writeStructField(obj, f, value)
					return val.NoneVal()
				}
			}
		}
		panic("AttributeError: '" + obj.Type().Name + "' object cannot set attribute '" + name.AsString() + "'")
	}))
	structTyp.InvalidateVersion()
}

func readStructField(obj val.Value, f layoutField) val.Value {
	st := asCStruct(obj)
	off := f.Offset
	sz := f.Size
	if sz <= 0 {
		sz = sizeofOfValue(f.Type)
	}
	code := typeCodeOf(f.Type)
	if code != "" {
		return decodeSimpleValue(code, st.Buf[off:off+sz])
	}
	// Nested structure: return a sub-view sharing buffer.
	// For MVP, copy to new struct of nested type.
	if f.Type.IsType() {
		nt := f.Type.AsType()
		// Allocate new struct and copy bytes.
		subLayout, _ := computeStructLayout(nt, false)
		sub := newCStructValue(nt, subLayout)
		copy(asCStruct(sub).Buf, st.Buf[off:off+subLayout.Size])
		return sub
	}
	return val.NoneVal()
}

func writeStructField(obj val.Value, f layoutField, v val.Value) {
	st := asCStruct(obj)
	off := f.Offset
	sz := f.Size
	if sz <= 0 {
		sz = sizeofOfValue(f.Type)
	}
	code := typeCodeOf(f.Type)
	if code != "" {
		encodeSimpleValue(code, v, st.Buf[off:off+sz])
		return
	}
	if f.Type.IsType() {
		// Nested struct: copy from v's buffer.
		if isCStruct(v) {
			copy(st.Buf[off:off+sz], asCStruct(v).Buf)
		}
	}
}

func makeArrayType(itemType val.Value, count int) val.Value {
	arrayTypeCacheMu.Lock()
	if t, ok := arrayTypeCache[itemType.String()+":count"]; ok {
		arrayTypeCacheMu.Unlock()
		return t
	}
	arrayTypeCacheMu.Unlock()
	// Create a new heap type subclassing Array with _length_ and _type_ attrs.
	// Build dict with _length_ and _type_ (item type code).
	dict := val.NewDictVal(8).AsDict()
	name := "<ctypes array type>"
	if itemType.IsType() {
		name = "Array_" + itemType.AsType().Name + "_" + ctypesItoa(count)
	}
	dict.Set(val.InternString("_length_"), val.Int64Val(int64(count)))
	dict.Set(val.InternString("_ctypes_size_"), val.Int64Val(int64(sizeofOfValue(itemType)*count)))
	// Store item type for sizeof.
	// Keep a Go-side map for constructors.
	t, err := val.NewType(name, []*val.PyType{arrayTyp}, dict)
	if err != nil {
		panic(err.Error())
	}
	// Array instance constructor: __new__ creates buffer.
	t.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		cls := callable.AsType()
		// Determine count/item from cls dict.
		countVal, _, _ := cls.LookupMRO(val.InternString("_length_"))
		cnt := int(countVal.AsInt64())
		itemVal, _, _ := cls.LookupMRO(val.InternString("_type_")) // may be missing; fallback to itemType passed in
		_ = itemVal
		// Use outer itemType/count if lookup fails (for first creation).
		if cnt == 0 {
			cnt = count
		}
		// Initialize from args: varargs (5,2,4,1,3) or single list [5,2,4,1,3].
		obj := newCArrayValue(cls, cnt, itemType)
		if len(args) > 0 {
			arr := asCArray(obj)
			elemSize := sizeofOfValue(itemType)
			code := typeCodeOf(itemType)
			switch {
			case len(args) == 1 && args[0].IsList():
				for i, it := range args[0].AsList().Items {
					if i >= cnt {
						break
					}
					if code != "" {
						encodeSimpleValue(code, it, arr.Buf[i*elemSize:(i+1)*elemSize])
					}
				}
			case len(args) == 1 && args[0].IsTuple():
				for i, it := range args[0].AsTuple().Items {
					if i >= cnt {
						break
					}
					if code != "" {
						encodeSimpleValue(code, it, arr.Buf[i*elemSize:(i+1)*elemSize])
					}
				}
			default:
				// Varargs: each arg is an element.
				for i, it := range args {
					if i >= cnt {
						break
					}
					if code != "" {
						encodeSimpleValue(code, it, arr.Buf[i*elemSize:(i+1)*elemSize])
					}
				}
			}
		}
		return obj
	}
	// Array supports __getitem__/__setitem__ for element access.
	t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal(name+".__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		arrObj := args[0]
		arr := asCArray(arrObj)
		idx := int(args[1].AsInt64())
		if idx < 0 {
			idx += arr.Count
		}
		if idx < 0 || idx >= arr.Count {
			panic("IndexError: array index out of range")
		}
		code := typeCodeOf(arr.ItemType)
		elemSize := sizeofOfValue(arr.ItemType)
		elem := arr.Buf[idx*elemSize : (idx+1)*elemSize]
		if code != "" {
			return decodeSimpleValue(code, elem)
		}
		return val.NoneVal()
	}))
	t.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal(name+".__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		arrObj := args[0]
		arr := asCArray(arrObj)
		idx := int(args[1].AsInt64())
		if idx < 0 {
			idx += arr.Count
		}
		if idx < 0 || idx >= arr.Count {
			panic("IndexError: array index out of range")
		}
		elemSize := sizeofOfValue(arr.ItemType)
		code := typeCodeOf(arr.ItemType)
		if code != "" {
			encodeSimpleValue(code, args[2], arr.Buf[idx*elemSize:(idx+1)*elemSize])
		}
		return val.NoneVal()
	}))
	t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal(name+".__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(asCArray(args[0]).Count))
	}))
	t.InvalidateVersion()
	arrayTypeCacheMu.Lock()
	arrayTypeCache[itemType.String()+":count"] = val.TypeVal(t)
	arrayTypeCacheMu.Unlock()
	return val.TypeVal(t)
}

var arrayTypeCache = make(map[string]val.Value)
var arrayTypeCacheMu sync.Mutex

func ctypesItoa(n int) string {
	// small helper (avoid strconv import cycle? use fmt).
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

func installCFuncPtrConstructor() {
	// __call__ for instances: (func_ptr)(args...) -> call C function. Set on base; subclasses inherit via MRO dunderCall.
	cfuncPtrTyp.Dict.Set(val.InternString("__call__"), val.NewBuiltinFuncVal("CFuncPtr.__call__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// args[0] is self (the funcptr instance) when called via __call__ dispatch.
		if len(args) < 1 {
			panic("TypeError: __call__() missing self")
		}
		self := args[0]
		callArgs := args[1:]
		return callCFuncPtr(ctx, self, callArgs, kwnames)
	}))
	// __new__ for construction via type call (delegates to Slots.TpCall construction logic).
	cfuncPtrTyp.Dict.Set(val.InternString("__new__"), val.NewBuiltinFuncVal("CFuncPtr.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: __new__() missing type")
		}
		clsVal := args[0]
		// Direct construction (do NOT delegate to Slots.TpCall — it is dunderTpCallSlot for __call__ and would recurse).
		return cfuncPtrConstruct(ctx, clsVal, args[1:], kwnames)
	}))
	if nv, ok := cfuncPtrTyp.Dict.Get(val.InternString("__new__")); ok {
		nv.AsBuiltinFunc().NoBind = true
	}
	cfuncPtrTyp.InvalidateVersion()
	// CFuncPtr is constructed as CFuncPtr(name_or_addr_or_callable, dll_handle?) or CFUNCTYPE wrapping.
	// Cases:
	//  - CFuncPtr((name, dll)) where dll is CDLL object with _handle
	//  - CFUNCTYPE(type)(python_func) where type is CFUNCTYPE subclass with _argtypes_/_restype_
	//  - CFUNCTYPE(type)(int_addr) for memmove_addr etc.
	cfuncPtrTyp.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		cls := callable.AsType()
		// Default empty.
		obj := &pyCFuncPtr{Type: cls}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = cls
		res := val.ObjectVal(&obj.Header)
		if len(args) == 0 {
			// Empty funcptr (null).
			return res
		}
		arg0 := args[0]
		// Tuple (name, dll)
		if arg0.IsTuple() && len(arg0.AsTuple().Items) == 2 {
			items := arg0.AsTuple().Items
			nameVal := items[0]
			dllVal := items[1]
			var handle uintptr
			if dllVal.HeaderPtr() != nil && dllVal.IsInstance() {
				if h, ok := dllVal.AsInstance().GetAttr(val.InternString("_handle")); ok {
					handle = uintptr(h.AsInt64())
				}
			} else if dllVal.IsInt64() {
				handle = uintptr(dllVal.AsInt64())
			}
			obj.Handle = handle
			if nameVal.IsAnyString() {
				obj.Name = nameVal.AsString()
				addr, _ := dlsymIn(handle, obj.Name)
				obj.Addr = addr
			} else if nameVal.IsInt64() {
				obj.Addr = uintptr(nameVal.AsInt64())
			}
			// Resolve argtypes/restype from cls dict.
			if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
				obj.ArgTypes = v.AsTuple().Items
			}
			if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
				obj.RestType = v
			}
			if v, ok := cls.Dict.Get(val.InternString("_flags_")); ok && v.IsInt64() {
				obj.Flags = v.AsInt64()
			}
			return res
		}
		// Callable (Python function) for CFUNCTYPE.
		if arg0.IsBuiltinFunc() || arg0.IsFunction() || (arg0.Type() != nil && arg0.Type().Slots.TpCall != nil) {
			obj.Callable = arg0
			// Create Go callback trampoline matching ArgTypes/RestType.
			obj.Callback = makeCFuncCallback(ctx, cls, arg0)
			// Addr is callback address for C.
			obj.Addr = obj.Callback
			if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
				obj.ArgTypes = v.AsTuple().Items
			}
			if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
				obj.RestType = v
			}
			return res
		}
		// Integer address.
		if arg0.IsInt64() {
			obj.Addr = uintptr(arg0.AsInt64())
			// Sentinel handling for _string_at etc.: callCFuncPtr intercepts negative Addr.
			if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
				obj.ArgTypes = v.AsTuple().Items
			}
			if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
				obj.RestType = v
			}
			return res
		}
		// Fallback: treat as callable.
		obj.Callable = arg0
		obj.Callback = makeCFuncCallback(ctx, cls, arg0)
		obj.Addr = obj.Callback
		return res
	}
	// Call: CFuncPtr instances are callable.
	cfuncPtrTyp.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// If callable is a type (CFUNCTYPE subclass), this is construction (handled above).
		// If callable is an instance, this is a call.
		if callable.IsType() {
			// Construction path (same as above, but invoked as type)
			// Reuse constructor logic by delegating: create instance via above? To avoid duplication,
			// check: our TpCall is on the base type; subclass types inherit it via Slots copy? NewType copies Slots from base and clears TpCall.
			// So subclass TpCall is nil, and construction goes via typeTpCall -> __new__? This is messy.
			// Fallback path: treat as construction here as well.
			// Duplicate logic (call the same code path).
			// For simplicity, if IsType, do construction.
			// (We replicate by calling the same function body via a helper.)
			return cfuncPtrConstruct(ctx, callable, args, kwnames)
		}
		// Instance call.
		return callCFuncPtr(ctx, callable, args, kwnames)
	}
	// Also need __call__ attribute for instances accessed via bound method? TpCall suffices for `obj(...)`.
	// Per-instance attributes via __getattribute__/__setattr__ (heap subclasses overwrite
	// Slots.TpGetAttr/TpSetAttr in NewType, so Slots alone will not be inherited).
	cfuncPtrTyp.Dict.Set(val.InternString("__getattribute__"), val.NewBuiltinFuncVal("CFuncPtr.__getattribute__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		obj := args[0]
		name := args[1]
		if isCFuncPtr(obj) {
			fp := asCFuncPtr(obj)
			switch name.AsString() {
			case "__name__":
				return val.NewStringVal(fp.Name)
			case "argtypes":
				if fp.ArgTypes == nil {
					// Fall back to type-level _argtypes_.
					if v, _, ok := obj.Type().LookupMRO(val.InternString("_argtypes_")); ok {
						return v
					}
					return val.NoneVal()
				}
				return val.NewTupleVal(fp.ArgTypes...)
			case "restype":
				if fp.RestType.HeaderPtr() == nil {
					if v, _, ok := obj.Type().LookupMRO(val.InternString("_restype_")); ok {
						return v
					}
					return val.NoneVal()
				}
				return fp.RestType
			}
		}
		// Fallback: look up in type MRO (non-data descriptors + class attrs).
		if v, _, ok := obj.Type().LookupMRO(val.InternString(name.AsString())); ok {
			if v.Type().Slots.TpDescrGet != nil {
				return v.Type().Slots.TpDescrGet(ctx, v, obj, obj.Type())
			}
			return v
		}
		if name.AsString() == "__class__" {
			return val.TypeVal(obj.Type())
		}
		panic("AttributeError: '" + obj.Type().Name + "' object has no attribute '" + name.AsString() + "'")
	}))
	cfuncPtrTyp.Dict.Set(val.InternString("__setattr__"), val.NewBuiltinFuncVal("CFuncPtr.__setattr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		obj := args[0]
		name := args[1]
		value := args[2]
		if isCFuncPtr(obj) {
			fp := asCFuncPtr(obj)
			switch name.AsString() {
			case "__name__":
				if value.IsAnyString() {
					fp.Name = value.AsString()
				}
				return val.NoneVal()
			case "argtypes":
				switch {
				case value.IsNone():
					fp.ArgTypes = nil
				case value.IsTuple():
					fp.ArgTypes = value.AsTuple().Items
				case value.IsList():
					fp.ArgTypes = append([]val.Value(nil), value.AsList().Items...)
				}
				return val.NoneVal()
			case "restype":
				fp.RestType = value
				return val.NoneVal()
			}
		}
		panic("AttributeError: '" + obj.Type().Name + "' object cannot set attribute '" + name.AsString() + "'")
	}))
	cfuncPtrTyp.InvalidateVersion()
	_ = cfuncPtrTyp
}

func cfuncPtrConstruct(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	// Same as TpCall construction above (extracted to avoid recursion).
	cls := callable.AsType()
	obj := &pyCFuncPtr{Type: cls}
	obj.Header.Kind = val.KindNative
	obj.Header.Type = cls
	res := val.ObjectVal(&obj.Header)
	if len(args) == 0 {
		return res
	}
	arg0 := args[0]
	if arg0.IsTuple() && len(arg0.AsTuple().Items) == 2 {
		items := arg0.AsTuple().Items
		nameVal := items[0]
		dllVal := items[1]
		var handle uintptr
		if dllVal.HeaderPtr() != nil && dllVal.IsInstance() {
			if h, ok := dllVal.AsInstance().GetAttr(val.InternString("_handle")); ok {
				handle = uintptr(h.AsInt64())
			}
		} else if dllVal.IsInt64() {
			handle = uintptr(dllVal.AsInt64())
		}
		obj.Handle = handle
		if nameVal.IsAnyString() {
			obj.Name = nameVal.AsString()
			addr, _ := dlsymIn(handle, obj.Name)
			obj.Addr = addr
		} else if nameVal.IsInt64() {
			obj.Addr = uintptr(nameVal.AsInt64())
		}
		if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
			obj.ArgTypes = v.AsTuple().Items
		}
		if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
			obj.RestType = v
		}
		if v, ok := cls.Dict.Get(val.InternString("_flags_")); ok && v.IsInt64() {
			obj.Flags = v.AsInt64()
		}
		return res
	}
	if arg0.IsInt64() {
		obj.Addr = uintptr(arg0.AsInt64())
		if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
			obj.ArgTypes = v.AsTuple().Items
		}
		if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
			obj.RestType = v
		}
		return res
	}
	// Callable.
	obj.Callable = arg0
	obj.Callback = makeCFuncCallback(ctx, cls, arg0)
	obj.Addr = obj.Callback
	if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
		obj.ArgTypes = v.AsTuple().Items
	}
	if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
		obj.RestType = v
	}
	return res
}

func makeCFuncCallback(ctx *val.ExecContext, cls *val.PyType, callable val.Value) uintptr {
	// Build a Go callback matching _argtypes_/_restype_ for C invocation.
	// MVP supports: (c_void_p, c_void_p) -> c_int (qsort comparator) and simple variants.
	var argTypes []val.Value
	var restType val.Value
	if v, ok := cls.Dict.Get(val.InternString("_argtypes_")); ok && v.IsTuple() {
		argTypes = v.AsTuple().Items
	}
	if v, ok := cls.Dict.Get(val.InternString("_restype_")); ok {
		restType = v
	}
	restCode := typeCodeOf(restType)
	// Capture ctx for Python invocation (use thread-local via Acquire hook).
	pyCallable := callable
	captureCtx := ctx
	// Helper to invoke Python callable with converted args.
	invokePy := func(pyArgs []val.Value) val.Value {
		if captureCtx != nil && captureCtx.CallHook != nil {
			// Use CallHook via temporary ExecContext.
			if val.AcquireExecContextHook != nil {
				acq, rel := val.AcquireExecContextHook() // use hook
				defer rel()
				return acq.Call(pyCallable, pyArgs, nil)
			}
			return captureCtx.Call(pyCallable, pyArgs, nil)
		}
		if val.AcquireExecContextHook != nil {
			acq, rel := val.AcquireExecContextHook() // use hook
			defer rel()
			return acq.Call(pyCallable, pyArgs, nil)
		}
		return val.NoneVal()
	}
	// Select callback signature by arity.
	switch len(argTypes) {
	case 2:
		fn := func(a, b unsafe.Pointer) int32 {
			// Convert pointers to c_void_p Python ints.
			pyArgs := []val.Value{val.Int64Val(int64(uintptr(a))), val.Int64Val(int64(uintptr(b)))}
			res := invokePy(pyArgs)
			if restCode == "?" {
				if res.IsTruthy() {
					return 1
				}
				return 0
			}
			return int32(res.AsInt64())
		}
		return dynload.NewCallback(fn)
	case 1:
		fn := func(a unsafe.Pointer) int32 {
			pyArgs := []val.Value{val.Int64Val(int64(uintptr(a)))}
			res := invokePy(pyArgs)
			return int32(res.AsInt64())
		}
		return dynload.NewCallback(fn)
	case 0:
		fn := func() int32 {
			res := invokePy(nil)
			return int32(res.AsInt64())
		}
		return dynload.NewCallback(fn)
	default:
		// Generic: support up to 4 void* args returning int.
		if len(argTypes) <= 4 {
			switch len(argTypes) {
			case 3:
				fn := func(a, b, c unsafe.Pointer) int32 {
					pyArgs := []val.Value{val.Int64Val(int64(uintptr(a))), val.Int64Val(int64(uintptr(b))), val.Int64Val(int64(uintptr(c)))}
					res := invokePy(pyArgs)
					return int32(res.AsInt64())
				}
				return dynload.NewCallback(fn)
			case 4:
				fn := func(a, b, c, d unsafe.Pointer) int32 {
					pyArgs := []val.Value{val.Int64Val(int64(uintptr(a))), val.Int64Val(int64(uintptr(b))), val.Int64Val(int64(uintptr(c))), val.Int64Val(int64(uintptr(d)))}
					res := invokePy(pyArgs)
					return int32(res.AsInt64())
				}
				return dynload.NewCallback(fn)
			}
		}
		// Fallback: 0-arg.
		fn := func() int32 { return 0 }
		return dynload.NewCallback(fn)
	}
}

func AcquireExecContextHelper() (*val.ExecContext, func()) {
	return val.AcquireExecContextHook()
}

func installPointerConstructor() {
	// __new__(cls, *args): LP_*() with 0 args -> null pointer; LP_*(obj) -> pointer to obj.
	pointerTyp.Dict.Set(val.InternString("__new__"), val.NewBuiltinFuncVal("_Pointer.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: __new__() missing type")
		}
		cls := args[0].AsType()
		obj := &pyCPointer{Type: cls}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = cls
		res := val.ObjectVal(&obj.Header)
		if len(args) >= 2 {
			arg1 := args[1]
			if arg1.IsInt64() {
				obj.Addr = uintptr(arg1.AsInt64())
			} else if arg1.HeaderPtr() != nil {
				obj.Target = arg1
				obj.Addr = uintptr(addressOfValue(arg1))
			}
		}
		return res
	}))
	if nv, ok := pointerTyp.Dict.Get(val.InternString("__new__")); ok {
		nv.AsBuiltinFunc().NoBind = true
	}
	// contents property: dereference.
	getter := val.NewBuiltinFuncVal("contents.get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		if !isCPointer(self) {
			panic("TypeError: contents descriptor on wrong type")
		}
		fp := asCPointer(self)
		// Determine pointed-to type from cls._type_.
		cls := self.Type()
		pt, _, ok := cls.LookupMRO(val.InternString("_type_"))
		if !ok || !pt.IsType() {
			return val.NoneVal()
		}
		ptType := pt.AsType()
		addr := fp.PointerAddr()
		if addr == 0 {
			return val.NoneVal()
		}
		// If pointing to an object via Target, return a copy view (new instance sharing buffer copy).
		// For simple types, allocate new simple and copy bytes from addr.
		sz := sizeofOfValue(pt)
		if sz == 0 {
			sz = 8
		}
		buf := make([]byte, sz)
		ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
		for i := range sz {
			buf[i] = *(*byte)(unsafe.Add(ptr, i))
		}
		// Construct pointed-to instance.
		if val.IsSubclass(ptType, structTyp) || val.IsSubclass(ptType, unionTyp) {
			obj := &pyCStruct{Type: ptType, Buf: buf}
			obj.Header.Kind = val.KindNative
			obj.Header.Type = ptType
			return val.ObjectVal(&obj.Header)
		}
		obj := &pySimpleCData{Type: ptType, Buf: buf}
		obj.Header.Kind = val.KindNative
		obj.Header.Type = ptType
		return val.ObjectVal(&obj.Header)
	})
	pointerTyp.Dict.Set(val.InternString("contents"), val.NewPropertyVal(getter, val.NoneVal(), val.NoneVal()))
	pointerTyp.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("_Pointer.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		if isCPointer(self) {
			fp := asCPointer(self)
			return val.NewStringVal("<" + self.Type().Name + " object at " + ptrStr(fp.PointerAddr()) + ">")
		}
		return val.NewStringVal("<pointer>")
	}))
	pointerTyp.InvalidateVersion()
}

func ptrStr(addr uintptr) string {
	if addr == 0 {
		return "0x0"
	}
	// hex without fmt to avoid import bloat? Use fmt.Sprintf via helper.
	return hexAddr(addr)
}

func hexAddr(a uintptr) string {
	if a == 0 {
		return "0x0"
	}
	digits := "0123456789abcdef"
	s := ""
	for a > 0 {
		s = string(digits[a&0xf]) + s
		a >>= 4
	}
	return "0x" + s
}
