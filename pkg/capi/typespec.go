package capi

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

// Slot IDs from CPython slots_generated.h (3.15).
const (
	slot_mp_subscript               = 5
	slot_nb_absolute                = 6
	slot_nb_add                     = 7
	slot_nb_and                     = 8
	slot_nb_bool                    = 9
	slot_nb_divmod                  = 10
	slot_nb_float                   = 11
	slot_nb_floor_divide            = 12
	slot_nb_index                   = 13
	slot_nb_inplace_add             = 14
	slot_nb_inplace_and             = 15
	slot_nb_inplace_floor_divide    = 16
	slot_nb_inplace_lshift          = 17
	slot_nb_inplace_multiply        = 18
	slot_nb_inplace_or              = 19
	slot_nb_inplace_power           = 20
	slot_nb_inplace_remainder       = 21
	slot_nb_inplace_rshift          = 22
	slot_nb_inplace_subtract        = 23
	slot_nb_inplace_true_divide     = 24
	slot_nb_inplace_xor             = 25
	slot_nb_int                     = 26
	slot_nb_invert                  = 27
	slot_nb_lshift                  = 28
	slot_nb_multiply                = 29
	slot_nb_negative                = 30
	slot_nb_or                      = 31
	slot_nb_positive                = 32
	slot_nb_power                   = 33
	slot_nb_remainder               = 34
	slot_nb_rshift                  = 35
	slot_nb_subtract                = 36
	slot_nb_true_divide             = 37
	slot_nb_xor                     = 38
	slot_sq_ass_item                = 39
	slot_sq_concat                  = 40
	slot_sq_contains                = 41
	slot_sq_inplace_concat          = 42
	slot_sq_inplace_repeat          = 43
	slot_sq_item                    = 44
	slot_sq_length                  = 45
	slot_sq_repeat                  = 46
	slot_tp_alloc                   = 47
	slot_tp_base                    = 48
	slot_tp_bases                   = 49
	slot_tp_call                    = 50
	slot_tp_clear                   = 51
	slot_tp_dealloc                 = 52
	slot_tp_del                     = 53
	slot_tp_descr_get               = 54
	slot_tp_descr_set               = 55
	slot_tp_doc                     = 56
	slot_tp_getattr                 = 57
	slot_tp_getattro                = 58
	slot_tp_hash                    = 59
	slot_tp_init                    = 60
	slot_tp_iter                    = 62
	slot_tp_iternext                = 63
	slot_tp_methods                 = 64
	slot_tp_new                     = 65
	slot_tp_repr                    = 66
	slot_tp_richcompare             = 67
	slot_tp_setattr                 = 68
	slot_tp_setattro                = 69
	slot_tp_str                     = 70
	slot_tp_members                 = 72
	slot_tp_getset                  = 73
	slot_tp_free                    = 74
	slot_nb_matrix_multiply         = 75
	slot_nb_inplace_matrix_multiply = 76
	slot_bf_getbuffer               = 88
	slot_bf_releasebuffer           = 89
	slot_mp_ass_subscript           = 90
	slot_mp_length                  = 91
	slot_tp_slots                   = 93
	slot_tp_name                    = 95
	slot_tp_basicsize               = 96
	slot_tp_itemsize                = 98
	slot_tp_flags                   = 99
)

// PyType_Spec layout: name@0, basicsize int32 @8, itemsize int32 @12, flags u32 @16, slots@24.
func readTypeSpec(spec unsafe.Pointer) (name string, basicsize, itemsize int, flags uint32, slotsPtr unsafe.Pointer) {
	if spec == nil {
		return
	}
	namePtr := *(*unsafe.Pointer)(spec)
	if namePtr != nil {
		name = GoString(namePtr)
	}
	basicsize = int(*(*int32)(unsafe.Add(spec, 8)))
	itemsize = int(*(*int32)(unsafe.Add(spec, 12)))
	flags = *(*uint32)(unsafe.Add(spec, 16))
	slotsPtr = *(*unsafe.Pointer)(unsafe.Add(spec, 24))
	return
}

func readSlotArray(p unsafe.Pointer) []struct {
	id    int32
	pfunc unsafe.Pointer
} {
	var out []struct {
		id    int32
		pfunc unsafe.Pointer
	}
	for i := range 256 {
		addr := unsafe.Add(p, i*16)
		id := *(*int32)(addr)
		if id == 0 {
			break
		}
		pfunc := *(*unsafe.Pointer)(unsafe.Add(addr, 8))
		out = append(out, struct {
			id    int32
			pfunc unsafe.Pointer
		}{id: id, pfunc: pfunc})
	}
	return out
}

// init registers the GC finalizer hook for C instances.
func init() {
	val.CDeallocHook = func(v val.Value) {
		if v.HeaderPtr() == nil {
			return
		}
		ci := v.AsCInstance()
		if ci == nil || ci.DeallocCalled {
			return
		}
		ci.DeallocCalled = true
		t := v.Type()
		if t == nil || t.Native == nil || t.Native.CDealloc == nil {
			return
		}
		func() {
			defer func() { _ = recover() }()
			stub := val.ArenaBorrowStub(v)
			var fn func(unsafe.Pointer)
			dynload.RegisterFunc(&fn, uintptr(t.Native.CDealloc))
			// Acquire GIL for C callback.
			rel := AcquireGIL(nil)
			defer rel()
			fn(stub)
		}()
	}
}

// --- generic C-call helpers (GIL + exception handling) ---

func callCBinary(ctx *val.ExecContext, cptr unsafe.Pointer, a, b val.Value) val.Value {
	rel := AcquireGIL(ctx)
	defer rel()
	var fn func(a, b unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cptr))
	aStub := val.ArenaBorrowStub(a)
	bStub := val.ArenaBorrowStub(b)
	var result unsafe.Pointer
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		result = fn(aStub, bStub)
	}()
	if recovered != nil {
		if ev, ok := recovered.(val.Value); ok {
			SetError(ev)
		}
		return val.NoneVal()
	}
	if result == nil {
		if CurrentContext() != nil && CurrentContext().HasException() {
			return val.NoneVal()
		}
		if b.Type() != nil && b.Type().Name != "" {
			// Returning NotImplemented is correct for binaryfunc fallback (e.g. __add__).
			return val.NotImplementedVal()
		}
		return val.NotImplementedVal()
	}
	return val.ArenaFromStub(result)
}

func callCUnary(ctx *val.ExecContext, cptr unsafe.Pointer, a val.Value) val.Value {
	rel := AcquireGIL(ctx)
	defer rel()
	var fn func(a unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cptr))
	aStub := val.ArenaBorrowStub(a)
	var result unsafe.Pointer
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		result = fn(aStub)
	}()
	if recovered != nil {
		if ev, ok := recovered.(val.Value); ok {
			SetError(ev)
		}
		return val.NoneVal()
	}
	if result == nil {
		if CurrentContext() != nil && CurrentContext().HasException() {
			return val.NoneVal()
		}
		return val.NoneVal()
	}
	return val.ArenaFromStub(result)
}

func callCBinaryForOp(ctx *val.ExecContext, cptr unsafe.Pointer, a, b val.Value, op int) val.Value {
	rel := AcquireGIL(ctx)
	defer rel()
	var fn func(a, b unsafe.Pointer, op int32) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cptr))
	aStub := val.ArenaBorrowStub(a)
	bStub := val.ArenaBorrowStub(b)
	var result unsafe.Pointer
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		result = fn(aStub, bStub, int32(op))
	}()
	if recovered != nil {
		return val.NoneVal()
	}
	if result == nil {
		if CurrentContext() != nil && CurrentContext().HasException() {
			return val.NoneVal()
		}
		return val.NotImplementedVal()
	}
	return val.ArenaFromStub(result)
}

// WrapPyCMethod creates a builtin function for a type method (binds self as args[0]).
func WrapPyCMethod(cfn unsafe.Pointer, flags int32, selfVal val.Value, namePtr unsafe.Pointer) val.Value {
	return wrapPyCMethodInternal(cfn, flags, namePtr)
}

func wrapPyCMethodInternal(cfn unsafe.Pointer, flags int32, namePtr unsafe.Pointer) val.Value {
	name := "cmeth"
	if namePtr != nil {
		name = GoString(namePtr)
	}
	cAddr := cfn
	wrapper := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			return val.NoneVal()
		}
		selfVal := args[0]
		callArgs := args[1:]
		selfStub := val.ArenaBorrowStub(selfVal)
		rel := AcquireGIL(ctx)
		defer rel()
		var resultStub unsafe.Pointer
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			switch {
			case flags&METH_FASTCALL != 0 && flags&METH_KEYWORDS != 0:
				resultStub = callFastCallKeywords(cAddr, selfStub, callArgs, kwnames)
			case flags&METH_FASTCALL != 0:
				resultStub = callFastCall(cAddr, selfStub, callArgs)
			case flags&METH_KEYWORDS != 0:
				resultStub = callVarargsKeywords(cAddr, selfStub, callArgs, kwnames)
			case flags&METH_VARARGS != 0:
				resultStub = callVarargs(cAddr, selfStub, callArgs)
			case flags&METH_NOARGS != 0:
				resultStub = callNoargs(cAddr, selfStub)
			case flags&METH_O != 0:
				if len(callArgs) != 1 {
					CurrentContext().Raise(val.NewTypeError("expected exactly 1 argument"))
					return
				}
				resultStub = callO(cAddr, selfStub, callArgs[0])
			default:
				resultStub = callVarargs(cAddr, selfStub, callArgs)
			}
		}()
		if recovered != nil {
			if ev, ok := recovered.(val.Value); ok {
				SetError(ev)
			} else {
				pyErrSetString(val.RuntimeErrorType, "C method raised")
			}
			return val.NoneVal()
		}
		if resultStub == nil {
			if CurrentContext() != nil && CurrentContext().HasException() {
				return val.NoneVal()
			}
			pyErrSetString(val.RuntimeErrorType, "C method returned NULL")
			return val.NoneVal()
		}
		return val.ArenaFromStub(resultStub)
	}
	return val.NewBuiltinFuncVal(name, wrapper)
}

// member type sizes (LP64).
const (
	memberT_SHORT          = 0
	memberT_INT            = 1
	memberT_LONG           = 2
	memberT_FLOAT          = 3
	memberT_DOUBLE         = 4
	memberT_STRING         = 5
	memberT_OBJECT_EX      = 16
	memberT_LONGLONG       = 17
	memberT_ULONGLONG      = 18
	memberT_PYSSIZET       = 19
	memberT_CHAR           = 7
	memberT_BYTE           = 8
	memberT_UBYTE          = 9
	memberT_USHORT         = 10
	memberT_UINT           = 11
	memberT_ULONG          = 12
	memberT_STRING_INPLACE = 13
	memberT_BOOL           = 14
)

func makeMemberProperty(typ int, offset int64, flags int32, doc string) val.Value {
	getter := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: member descriptor missing self")
		}
		self := args[0]
		ci := self.AsCInstance()
		if ci == nil || ci.ArenaPtr == nil {
			// Lazy alloc (should not happen via tp_new, but safe).
			_ = val.AllocCInstanceStub(self)
			ci = self.AsCInstance()
		}
		base := ci.ArenaPtr
		addr := unsafe.Add(base, offset)
		switch typ {
		case memberT_BOOL:
			return val.BoolVal(*(*byte)(addr) != 0)
		case memberT_BYTE:
			return val.Int64Val(int64(int8(*(*byte)(addr))))
		case memberT_UBYTE, memberT_CHAR:
			return val.Int64Val(int64(*(*byte)(addr)))
		case memberT_SHORT:
			return val.Int64Val(int64(*(*int16)(addr)))
		case memberT_USHORT:
			return val.Int64Val(int64(*(*uint16)(addr)))
		case memberT_INT:
			return val.Int64Val(int64(*(*int32)(addr)))
		case memberT_UINT:
			return val.Int64Val(int64(*(*uint32)(addr)))
		case memberT_LONG, memberT_PYSSIZET:
			return val.Int64Val(*(*int64)(addr))
		case memberT_ULONG, memberT_ULONGLONG, memberT_LONGLONG:
			return val.Int64Val(*(*int64)(addr))
		case memberT_FLOAT:
			return val.Float64Val(float64(*(*float32)(addr)))
		case memberT_DOUBLE:
			return val.Float64Val(*(*float64)(addr))
		case memberT_STRING:
			p := *(*unsafe.Pointer)(addr)
			if p == nil {
				return val.NoneVal()
			}
			return val.NewStringVal(GoString(p))
		case memberT_OBJECT_EX:
			ob := *(*unsafe.Pointer)(addr)
			if ob == nil {
				return val.NoneVal()
			}
			return val.ArenaFromStub(ob)
		default:
			for _, size := range []int{8, 4, 2, 1} {
				_ = size
			}
			return val.Int64Val(*(*int64)(addr))
		}
	}
	setter := val.NoneVal()
	if flags&1 == 0 { // not READONLY
		setter = val.NewBuiltinFuncVal("set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: member setter missing value")
			}
			self := args[0]
			v := args[1]
			ci := self.AsCInstance()
			if ci == nil || ci.ArenaPtr == nil {
				_ = val.AllocCInstanceStub(self)
				ci = self.AsCInstance()
			}
			base := ci.ArenaPtr
			addr := unsafe.Add(base, offset)
			switch typ {
			case memberT_BOOL:
				*(*byte)(addr) = 1
				if !v.IsTruthy() {
					*(*byte)(addr) = 0
				}
			case memberT_BYTE:
				*(*byte)(addr) = byte(int8(v.AsInt64()))
			case memberT_UBYTE, memberT_CHAR:
				*(*byte)(addr) = byte(v.AsInt64())
			case memberT_SHORT:
				*(*int16)(addr) = int16(v.AsInt64())
			case memberT_USHORT:
				*(*uint16)(addr) = uint16(v.AsInt64())
			case memberT_INT:
				*(*int32)(addr) = int32(v.AsInt64())
			case memberT_UINT:
				*(*uint32)(addr) = uint32(v.AsInt64())
			case memberT_LONG, memberT_PYSSIZET:
				*(*int64)(addr) = v.AsInt64()
			case memberT_ULONG, memberT_ULONGLONG, memberT_LONGLONG:
				*(*int64)(addr) = v.AsInt64()
			case memberT_FLOAT:
				*(*float32)(addr) = float32(v.AsFloat64())
			case memberT_DOUBLE:
				*(*float64)(addr) = v.AsFloat64()
			case memberT_STRING:
				// Store NUL-terminated copy in payload if inplace, else allocate? For char* field, copy via PyMem? Simplistic: leak Go bytes.
				// Keep a per-instance cache would be needed; for MVP, store bytes in a Go global map keyed by base+offset.
				storeCString(base, offset, v.AsString())
			case memberT_OBJECT_EX:
				old := *(*unsafe.Pointer)(addr)
				if old != nil {
					// DECREF old (balance: C owns it, so DECREF via refcount)
					*(*int64)(old)--
					rc := *(*int64)(old)
					if rc == 0 {
						_Py_Dealloc(old)
					}
				}
				if v.IsNone() || v.HeaderPtr() == nil {
					*(*unsafe.Pointer)(addr) = nil
				} else {
					*(*unsafe.Pointer)(addr) = val.ArenaToStub(v)
				}
			}
			return val.NoneVal()
		})
	}
	return val.NewPropertyVal(val.NewBuiltinFuncVal("get", getter), setter, val.NoneVal())
}

var cstrMu sync.Mutex
var cstrStore = make(map[unsafe.Pointer]map[int64][]byte)

func storeCString(base unsafe.Pointer, offset int64, s string) {
	cstrMu.Lock()
	defer cstrMu.Unlock()
	if cstrStore[base] == nil {
		cstrStore[base] = make(map[int64][]byte)
	}
	b := append([]byte(s), 0)
	cstrStore[base][offset] = b
	*(*unsafe.Pointer)(unsafe.Add(base, offset)) = unsafe.Pointer(&b[0])
}

func makeGetSetProperty(getPtr, setPtr, closure unsafe.Pointer, doc string) val.Value {
	getter := val.NoneVal()
	if getPtr != nil {
		cAddr := getPtr
		clos := closure
		getter = val.NewBuiltinFuncVal("get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0]
			selfStub := val.ArenaBorrowStub(self)
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b unsafe.Pointer) unsafe.Pointer
			dynload.RegisterFunc(&fn, uintptr(cAddr))
			var res unsafe.Pointer
			func() {
				defer func() { _ = recover() }()
				res = fn(selfStub, clos)
			}()
			if res == nil {
				if CurrentContext() != nil && CurrentContext().HasException() {
					return val.NoneVal()
				}
				return val.NoneVal()
			}
			return val.ArenaFromStub(res)
		})
	}
	setter := val.NoneVal()
	if setPtr != nil {
		cAddr := setPtr
		clos := closure
		setter = val.NewBuiltinFuncVal("set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0]
			v := args[1]
			selfStub := val.ArenaBorrowStub(self)
			vStub := val.ArenaBorrowStub(v) // nil -> NULL? setter expects PyObject* may be NULL for del; handle None as NULL? CPython passes NULL for del. Our Value None -> NULL would be better. If v is Unbound, pass NULL.
			if v.IsUnbound() {
				vStub = nil
			}
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b, c unsafe.Pointer) int32
			dynload.RegisterFunc(&fn, uintptr(cAddr))
			var ret int32
			func() {
				defer func() { _ = recover() }()
				ret = fn(selfStub, vStub, clos)
			}()
			if ret < 0 {
				return val.NoneVal()
			}
			return val.NoneVal()
		})
	}
	return val.NewPropertyVal(getter, setter, val.NoneVal())
}

// PyType_FromSpec creates a heap type from a PyType_Spec.
func PyType_FromSpec(spec unsafe.Pointer) unsafe.Pointer {
	return PyType_FromSpecWithBases(spec, nil)
}

// PyType_FromSpecWithBases creates a heap type with explicit bases tuple.
func PyType_FromSpecWithBases(spec unsafe.Pointer, bases unsafe.Pointer) unsafe.Pointer {
	if spec == nil {
		pyErrSetString(val.RuntimeErrorType, "PyType_FromSpec called with NULL spec")
		return nil
	}
	name, basicsize, itemsize, flags, slotsPtr := readTypeSpec(spec)
	_ = flags
	if name == "" {
		name = "CType"
	}
	// Collect slots.
	slots := readSlotArray(slotsPtr)
	// Bases: from bases tuple if given, else Py_tp_base slot, else object.
	basesVal := val.NoneVal()
	if bases != nil {
		basesVal = val.ArenaFromStub(bases)
	}
	var baseTypes []*val.PyType
	// Find base slot first.
	for _, s := range slots {
		if s.id == slot_tp_base && s.pfunc != nil {
			if bt := val.ArenaTypeFromShadow(s.pfunc); bt != nil {
				baseTypes = []*val.PyType{bt}
			} else {
				// May be a type stub Value.
				v := val.ArenaFromStub(s.pfunc)
				if v.IsType() {
					baseTypes = []*val.PyType{v.AsType()}
				}
			}
		}
		if s.id == slot_tp_bases && s.pfunc != nil {
			v := val.ArenaFromStub(s.pfunc)
			if v.IsTuple() {
				for _, it := range v.AsTuple().Items {
					if it.IsType() {
						baseTypes = append(baseTypes, it.AsType())
					}
				}
			}
		}
	}
	if len(baseTypes) == 0 {
		if basesVal.IsTuple() {
			for _, it := range basesVal.AsTuple().Items {
				if it.IsType() {
					baseTypes = append(baseTypes, it.AsType())
				}
			}
		}
	}
	if len(baseTypes) == 0 {
		if val.ObjectType != nil {
			baseTypes = []*val.PyType{val.ObjectType}
		}
	}
	dict := val.NewDictVal(16).AsDict()
	// Defer MRO: create type first so descriptors can capture it.
	t, err := val.NewType(name, baseTypes, dict)
	if err != nil {
		pyErrSetString(val.RuntimeErrorType, "PyType_FromSpec: "+err.Error())
		return nil
	}
	t.Flags |= val.FlagHeapType
	if basicsize > 0 {
		t.Basicsize = basicsize
	}
	if itemsize > 0 {
		t.Itemsize = itemsize
	}
	t.Native = &val.NativeTypeInfo{}
	// Populate from slots.
	applySlotsToType(t, slots)
	// Ensure shadow fields for C interop (tp_alloc/new/init/dealloc in block).
	populateShadowTypeCallbacks(t)
	t.InvalidateVersion()
	return val.ArenaToStub(val.TypeVal(t))
}

// PyType_FromModuleAndSpec creates a heap type and adds it to module/dict.
func PyType_FromModuleAndSpec(module unsafe.Pointer, spec unsafe.Pointer, dict unsafe.Pointer) unsafe.Pointer {
	if spec == nil {
		pyErrSetString(val.RuntimeErrorType, "PyType_FromModuleAndSpec called with NULL spec")
		return nil
	}
	// dict is optional extra dict to merge.
	tStub := PyType_FromSpec(spec)
	if tStub == nil {
		return nil
	}
	tVal := val.ArenaFromStub(tStub)
	t := tVal.AsType()
	// Merge extra dict if given.
	if dict != nil {
		dv := val.ArenaFromStub(dict)
		if dv.IsDict() {
			for _, it := range dv.AsDict().Items() {
				t.SetAttr(it.Key, it.Value)
			}
		}
	}
	// If module given, register type in its dict under short name (last component).
	if module != nil {
		mv := val.ArenaFromStub(module)
		if mv.IsModule() {
			mod := mv.AsModule()
			// Skip module prefix after last '.'
			short := t.Name
			for i := len(short) - 1; i >= 0; i-- {
				if short[i] == '.' {
					short = short[i+1:]
					break
				}
			}
			mod.Dict.Set(val.InternString(short), val.TypeVal(t))
		}
	}
	// Populate shadow type callbacks again (after extra dict merges).
	populateShadowTypeCallbacks(t)
	t.InvalidateVersion()
	return val.ArenaToStub(val.TypeVal(t))
}

func applySlotsToType(t *val.PyType, slots []struct {
	id    int32
	pfunc unsafe.Pointer
}) {
	for _, s := range slots {
		switch s.id {
		case slot_tp_methods:
			if s.pfunc != nil {
				addMethodsToType(t, s.pfunc)
			}
		case slot_tp_members:
			if s.pfunc != nil {
				addMembersToType(t, s.pfunc)
			}
		case slot_tp_getset:
			if s.pfunc != nil {
				addGetSetsToType(t, s.pfunc)
			}
		case slot_tp_doc:
			if s.pfunc != nil {
				t.SetAttr(val.InternString("__doc__"), val.NewStringVal(GoString(s.pfunc)))
			}
		case slot_tp_dealloc:
			t.Native.CDealloc = s.pfunc
			cptr := s.pfunc
			t.Slots.TpDealloc = func(ctx *val.ExecContext, v val.Value) {
				_ = cptr
				_ = ctx
			}
		case slot_tp_new:
			t.Native.CNew = s.pfunc
			installCNew(t, s.pfunc)
		case slot_tp_init:
			t.Native.CInit = s.pfunc
			installCInit(t, s.pfunc)
		case slot_tp_alloc:
			// Store for shadow block (see populateShadowTypeCallbacks).
			t.Native.CNew = s.pfunc // unused fallback; alloc handled by GenericAlloc wrapper
		case slot_tp_call:
			cptr := s.pfunc
			t.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				_ = callable
				// ternaryfunc: (callable, args_tuple, kwargs_dict)
				selfStub := val.ArenaBorrowStub(callable)
				tupVal := val.NewTupleVal(args...)
				tupStub := val.ArenaToStub(tupVal)
				var kwStub unsafe.Pointer
				if len(kwnames) > 0 {
					kwDict := val.NewDictVal(len(kwnames)).AsDict()
					for i, kn := range kwnames {
						kwDict.Set(kn, args[len(args)-len(kwnames)+i])
					}
					kwStub = val.ArenaToStub(val.DictVal(kwDict))
				}
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a, b, c unsafe.Pointer) unsafe.Pointer
				dynload.RegisterFunc(&fn, uintptr(cptr))
				var res unsafe.Pointer
				func() {
					defer func() { _ = recover() }()
					res = fn(selfStub, tupStub, kwStub)
				}()
				if res == nil {
					return val.NoneVal()
				}
				return val.ArenaFromStub(res)
			}
		case slot_tp_repr:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal(t.Name+".__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCUnary(ctx, cptr, args[0])
			}))
		case slot_tp_str:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal(t.Name+".__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCUnary(ctx, cptr, args[0])
			}))
		case slot_tp_hash:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal(t.Name+".__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a unsafe.Pointer) int64
				dynload.RegisterFunc(&fn, uintptr(cptr))
				stub := val.ArenaBorrowStub(args[0])
				var h int64
				func() {
					defer func() { _ = recover() }()
					h = fn(stub)
				}()
				if CurrentContext() != nil && CurrentContext().HasException() {
					return val.NoneVal()
				}
				return val.Int64Val(h)
			}))
		case slot_tp_richcompare:
			cptr := s.pfunc
			installRichCompare(t, cptr)
		case slot_tp_iter:
			cptr := s.pfunc
			t.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
				return callCUnary(ctx, cptr, o)
			}
			t.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal(t.Name+".__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCUnary(ctx, cptr, args[0])
			}))
		case slot_tp_iternext:
			cptr := s.pfunc
			t.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
				v := callCUnary(ctx, cptr, o)
				if ctx != nil && ctx.HasException() {
					exc := ctx.ActiveException
					ctx.ClearException()
					if val.IsInstance(exc, val.StopIterationType) {
						return val.NoneVal(), val.IterExhausted
					}
					ctx.Raise(exc)
					return val.NoneVal(), val.IterError
				}
				return v, val.IterYielded
			}
			t.Dict.Set(val.InternString("__next__"), val.NewBuiltinFuncVal(t.Name+".__next__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCUnary(ctx, cptr, args[0])
			}))
		case slot_tp_getattro:
			cptr := s.pfunc
			t.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
				// getattrofunc: (obj, name_obj) -> obj
				return callCBinary(ctx, cptr, obj, name)
			}
		case slot_tp_setattro:
			cptr := s.pfunc
			t.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a, b, c unsafe.Pointer) int32
				dynload.RegisterFunc(&fn, uintptr(cptr))
				res := fn(val.ArenaBorrowStub(obj), val.ArenaBorrowStub(name), val.ArenaBorrowStub(value))
				if res < 0 {
					return val.NoneVal()
				}
				return val.NoneVal()
			}
		case slot_tp_descr_get:
			cptr := s.pfunc
			_ = cptr
		case slot_tp_descr_set:
			_ = s.pfunc
		case slot_nb_add:
			cptr := s.pfunc
			_ = cptr
			t.Slots.NbAdd = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
			t.Dict.Set(val.InternString("__add__"), val.NewBuiltinFuncVal(t.Name+".__add__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCBinary(ctx, cptr, args[0], args[1])
			}))
		case slot_nb_subtract:
			cptr := s.pfunc
			t.Slots.NbSub = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
			t.Dict.Set(val.InternString("__sub__"), val.NewBuiltinFuncVal(t.Name+".__sub__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCBinary(ctx, cptr, args[0], args[1])
			}))
		case slot_nb_multiply:
			cptr := s.pfunc
			t.Slots.NbMul = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
			t.Dict.Set(val.InternString("__mul__"), val.NewBuiltinFuncVal(t.Name+".__mul__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCBinary(ctx, cptr, args[0], args[1])
			}))
		case slot_nb_absolute:
			cptr := s.pfunc
			t.Slots.NbAbs = func(ctx *val.ExecContext, a val.Value) val.Value { return callCUnary(ctx, cptr, a) }
			t.Dict.Set(val.InternString("__abs__"), val.NewBuiltinFuncVal(t.Name+".__abs__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCUnary(ctx, cptr, args[0])
			}))
		case slot_nb_bool:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__bool__"), val.NewBuiltinFuncVal(t.Name+".__bool__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a unsafe.Pointer) int32
				dynload.RegisterFunc(&fn, uintptr(cptr))
				res := fn(val.ArenaBorrowStub(args[0]))
				return val.BoolVal(res != 0)
			}))
		case slot_sq_length:
			cptr := s.pfunc
			t.Slots.SqLen = func(ctx *val.ExecContext, a val.Value) int64 {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a unsafe.Pointer) int64
				dynload.RegisterFunc(&fn, uintptr(cptr))
				v := fn(val.ArenaBorrowStub(a))
				return v
			}
			t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal(t.Name+".__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return val.Int64Val(t.Slots.SqLen(ctx, args[0]))
			}))
		case slot_sq_item:
			cptr := s.pfunc
			// ssizeargfunc: (obj, idx)
			t.Slots.SqItem = func(ctx *val.ExecContext, a, b val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a unsafe.Pointer, idx int64) unsafe.Pointer
				dynload.RegisterFunc(&fn, uintptr(cptr))
				res := fn(val.ArenaBorrowStub(a), b.AsInt64())
				if res == nil {
					return val.NoneVal()
				}
				return val.ArenaFromStub(res)
			}
			t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal(t.Name+".__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				// __getitem__ takes (self, index) but C sq_item takes ssize; handle both slice/int by multiplexing via MpSubscript if present.
				return t.Slots.SqItem(ctx, args[0], args[1])
			}))
		case slot_sq_ass_item:
			cptr := s.pfunc
			t.Slots.SqAssItem = func(ctx *val.ExecContext, a, b, c val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a unsafe.Pointer, idx int64, v unsafe.Pointer) int32
				dynload.RegisterFunc(&fn, uintptr(cptr))
				var vStub unsafe.Pointer
				if !c.IsNone() && c.HeaderPtr() != nil {
					vStub = val.ArenaBorrowStub(c)
				}
				ret := fn(val.ArenaBorrowStub(a), b.AsInt64(), vStub)
				if ret < 0 {
					return val.NoneVal()
				}
				return val.NoneVal()
			}
			t.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal(t.Name+".__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				t.Slots.SqAssItem(ctx, args[0], args[1], args[2])
				return val.NoneVal()
			}))
		case slot_sq_concat:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__concat__"), val.NewBuiltinFuncVal(t.Name+".sq_concat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCBinary(ctx, cptr, args[0], args[1])
			}))
		case slot_sq_contains:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__contains__"), val.NewBuiltinFuncVal(t.Name+".__contains__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a, b unsafe.Pointer) int32
				dynload.RegisterFunc(&fn, uintptr(cptr))
				res := fn(val.ArenaBorrowStub(args[0]), val.ArenaBorrowStub(args[1]))
				return val.BoolVal(res != 0)
			}))
		case slot_mp_subscript:
			cptr := s.pfunc
			t.Slots.MpSubscript = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
			t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal(t.Name+".__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCBinary(ctx, cptr, args[0], args[1])
			}))
		case slot_mp_ass_subscript:
			cptr := s.pfunc
			t.Slots.MpAssSubscript = func(ctx *val.ExecContext, a, b, c val.Value) val.Value {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a, b, c unsafe.Pointer) int32
				dynload.RegisterFunc(&fn, uintptr(cptr))
				var vStub unsafe.Pointer
				if !c.IsNone() && c.HeaderPtr() != nil {
					vStub = val.ArenaBorrowStub(c)
				}
				ret := fn(val.ArenaBorrowStub(a), val.ArenaBorrowStub(b), vStub)
				if ret < 0 {
					return val.NoneVal()
				}
				return val.NoneVal()
			}
			t.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal(t.Name+".__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				t.Slots.MpAssSubscript(ctx, args[0], args[1], args[2])
				return val.NoneVal()
			}))
		case slot_mp_length:
			cptr := s.pfunc
			t.Slots.SqLen = func(ctx *val.ExecContext, a val.Value) int64 {
				rel := AcquireGIL(ctx)
				defer rel()
				var fn func(a unsafe.Pointer) int64
				dynload.RegisterFunc(&fn, uintptr(cptr))
				return fn(val.ArenaBorrowStub(a))
			}
			t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal(t.Name+".__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return val.Int64Val(t.Slots.SqLen(ctx, args[0]))
			}))
		case slot_bf_getbuffer:
			cptr := s.pfunc
			t.Native.AsBuffer = cptr
			t.Slots.TpAsBuffer = makeCBufferSlot(cptr, t.Native)
		case slot_bf_releasebuffer:
			// stored; actual release is called via bufferPin path in PyObject_GetBuffer.
			// For Go-side TpAsBuffer, Release is embedded in PyBuffer.
		case slot_tp_slots:
			if s.pfunc != nil {
				nested := readSlotArray(s.pfunc)
				applySlotsToType(t, nested)
			}
		case slot_tp_name:
			if s.pfunc != nil {
				t.Name = GoString(s.pfunc)
			}
		case slot_tp_basicsize:
			t.Basicsize = int(*(*int64)(s.pfunc))
		case slot_tp_itemsize:
			t.Itemsize = int(*(*int64)(s.pfunc))
		case slot_tp_flags:
			_ = s.pfunc
		case slot_nb_matrix_multiply:
			cptr := s.pfunc
			t.Dict.Set(val.InternString("__matmul__"), val.NewBuiltinFuncVal(t.Name+".__matmul__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return callCBinary(ctx, cptr, args[0], args[1])
			}))
		}
	}
}

func installRichCompare(t *val.PyType, cptr unsafe.Pointer) {
	ops := map[string]int{
		"__lt__": 0, "__le__": 1, "__eq__": 2, "__ne__": 3, "__gt__": 4, "__ge__": 5,
	}
	for name, op := range ops {
		o := op
		t.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(t.Name+"."+name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinaryForOp(ctx, cptr, args[0], args[1], o)
		}))
	}
}

func installCNew(t *val.PyType, cptr unsafe.Pointer) {
	// __new__ builtin: __new__(cls, args, kwargs) -> instance
	__new__ := val.NewBuiltinFuncVal(t.Name+".__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: __new__() missing type")
		}
		clsVal := args[0]
		cls := clsVal.AsType()
		// If cls is a Python subclass of t, allocate subclass instance (still C payload via MRO check in NewInstanceOfType).
		populateShadowTypeCallbacks(cls)
		inst := val.NewInstanceOfType(cls)
		instStub := val.AllocCInstanceStub(inst)
		// Ensure ob_type points at the actual subclass shadow (not t, when subclassed).
		if stubType := val.ArenaShadowType(cls); stubType != nil {
			*(*unsafe.Pointer)(unsafe.Add(instStub, 8)) = stubType
		}
		if cptr != nil {
			// Call C tp_new(type, args_tuple, kwargs_dict). type here is cls shadow.
			typeStub := val.ArenaShadowType(cls)
			posArgs := args[1:]
			tupVal := val.NewTupleVal(posArgs...)
			tupStub := val.ArenaToStub(tupVal)
			var kwStub unsafe.Pointer
			if len(kwnames) > 0 {
				kwDict := val.NewDictVal(len(kwnames)).AsDict()
				// kwnames values are appended after positional in VM vectorcall; combine.
				nkw := len(kwnames)
				// args already split by caller; kwnames correspond to trailing callArgs? For __new__, args = [cls, ...pos, ...kwVals].
				// Our args slice includes everything; split by len(kwnames).
				npos := len(args) - 1 - nkw
				if npos < 0 {
					// No positional args; kwDict stays empty.
				} else {
					kwVals := args[1+npos:]
					posArgs = args[1 : 1+npos]
					tupVal = val.NewTupleVal(posArgs...)
					tupStub = val.ArenaToStub(tupVal)
					for i, kn := range kwnames {
						kwDict.Set(kn, kwVals[i])
					}
				}
				kwStub = val.ArenaToStub(val.DictVal(kwDict))
			}
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b, c unsafe.Pointer) unsafe.Pointer
			dynload.RegisterFunc(&fn, uintptr(cptr))
			var res unsafe.Pointer
			func() {
				defer func() { _ = recover() }()
				res = fn(typeStub, tupStub, kwStub)
			}()
			if res == nil {
				if CurrentContext() != nil && CurrentContext().HasException() {
					return val.NoneVal()
				}
				pyErrSetString(val.RuntimeErrorType, "tp_new returned NULL")
				return val.NoneVal()
			}
			rv := val.ArenaFromStub(res)
			// If C returned a different Go value (e.g. subclass instance), use it.
			// Else ensure our pre-allocated stub is the one returned (avoid double allocation).
			// If res != instStub, trust C's allocation (it called tp_alloc).
			if res != instStub {
				return rv
			}
			return inst
		}
		return inst
	})
	__new__.AsBuiltinFunc().NoBind = true
	t.Dict.Set(val.InternString("__new__"), __new__)
	// Also wire dunderNew cache via sync.
}

func installCInit(t *val.PyType, cptr unsafe.Pointer) {
	initFn := val.NewBuiltinFuncVal(t.Name+".__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		posArgs := args[1:]
		selfStub := val.ArenaBorrowStub(self)
		tupVal := val.NewTupleVal(posArgs...)
		tupStub := val.ArenaToStub(tupVal)
		var kwStub unsafe.Pointer
		if len(kwnames) > 0 {
			nkw := len(kwnames)
			npos := len(posArgs) - nkw
			kwDict := val.NewDictVal(nkw).AsDict()
			var kwVals []val.Value
			if npos >= 0 {
				kwVals = posArgs[npos:]
				posArgs = posArgs[:npos]
				tupVal = val.NewTupleVal(posArgs...)
				tupStub = val.ArenaToStub(tupVal)
				for i, kn := range kwnames {
					kwDict.Set(kn, kwVals[i])
				}
			}
			kwStub = val.ArenaToStub(val.DictVal(kwDict))
		}
		rel := AcquireGIL(ctx)
		defer rel()
		var fn func(a, b, c unsafe.Pointer) int32
		dynload.RegisterFunc(&fn, uintptr(cptr))
		var ret int32
		func() {
			defer func() { _ = recover() }()
			ret = fn(selfStub, tupStub, kwStub)
		}()
		if ret < 0 {
			return val.NoneVal()
		}
		return val.NoneVal()
	})
	t.Dict.Set(val.InternString("__init__"), initFn)
}

func addMethodsToType(t *val.PyType, methodsPtr unsafe.Pointer) {
	// PyMethodDef: name@0, meth@8, flags int @16, doc@24; 32 bytes each.
	for i := range 512 {
		addr := unsafe.Add(methodsPtr, i*32)
		namePtr := *(*unsafe.Pointer)(addr)
		if namePtr == nil {
			break
		}
		name := GoString(namePtr)
		meth := *(*unsafe.Pointer)(unsafe.Add(addr, 8))
		flags := *(*int32)(unsafe.Add(addr, 16))
		docPtr := *(*unsafe.Pointer)(unsafe.Add(addr, 24))
		_ = docPtr
		if meth == nil {
			continue
		}
		fn := WrapPyCMethod(meth, flags, val.NoneVal(), namePtr)
		t.Dict.Set(val.InternString(name), fn)
	}
}

func addMembersToType(t *val.PyType, membersPtr unsafe.Pointer) {
	// PyMemberDef: name@0, type int @8, offset ssize @16, flags int @24, doc@32; 40 bytes.
	for i := range 512 {
		addr := unsafe.Add(membersPtr, i*40)
		namePtr := *(*unsafe.Pointer)(addr)
		if namePtr == nil {
			break
		}
		name := GoString(namePtr)
		typ := *(*int32)(unsafe.Add(addr, 8))
		offset := *(*int64)(unsafe.Add(addr, 16))
		flags := *(*int32)(unsafe.Add(addr, 24))
		t.Dict.Set(val.InternString(name), makeMemberProperty(int(typ), offset, flags, ""))
	}
}

func addGetSetsToType(t *val.PyType, getsetPtr unsafe.Pointer) {
	// PyGetSetDef: name@0, get@8, set@16, doc@24, closure@32; 40 bytes.
	for i := range 512 {
		addr := unsafe.Add(getsetPtr, i*40)
		namePtr := *(*unsafe.Pointer)(addr)
		if namePtr == nil {
			break
		}
		name := GoString(namePtr)
		getPtr := *(*unsafe.Pointer)(unsafe.Add(addr, 8))
		setPtr := *(*unsafe.Pointer)(unsafe.Add(addr, 16))
		closure := *(*unsafe.Pointer)(unsafe.Add(addr, 32))
		t.Dict.Set(val.InternString(name), makeGetSetProperty(getPtr, setPtr, closure, ""))
	}
}

// PyType_Ready handles static PyTypeObject inits (as opposed to FromSpec heap types).
func PyType_Ready(t unsafe.Pointer) int32 {
	if t == nil {
		return -1
	}
	if val.ArenaTypeFromShadow(t) != nil {
		return 0
	}
	// Parse static type object fields (offsets per cpython/object.h).
	namePtr := *(*unsafe.Pointer)(unsafe.Add(t, 24))
	name := ""
	if namePtr != nil {
		name = GoString(namePtr)
	}
	basicsize := 16
	itemsize := 0
	if v := *(*int64)(unsafe.Add(t, 32)); v > 0 {
		basicsize = int(v)
	}
	if v := *(*int64)(unsafe.Add(t, 40)); v > 0 {
		itemsize = int(v)
	}
	// tp_base @256
	var baseTypes []*val.PyType
	basePtr := *(*unsafe.Pointer)(unsafe.Add(t, 256))
	if basePtr != nil {
		if bt := val.ArenaTypeFromShadow(basePtr); bt != nil {
			baseTypes = []*val.PyType{bt}
		} else if v := val.ArenaFromStub(basePtr); v.IsType() {
			baseTypes = []*val.PyType{v.AsType()}
		}
	}
	if len(baseTypes) == 0 {
		if val.ObjectType != nil {
			baseTypes = []*val.PyType{val.ObjectType}
		}
	}
	dict := val.NewDictVal(16).AsDict()
	gt, err := val.NewType(name, baseTypes, dict)
	if err != nil {
		pyErrSetString(val.RuntimeErrorType, "PyType_Ready: "+err.Error())
		return -1
	}
	gt.Basicsize = basicsize
	gt.Itemsize = itemsize
	gt.Native = &val.NativeTypeInfo{}
	// tp_doc @176
	if docP := *(*unsafe.Pointer)(unsafe.Add(t, 176)); docP != nil {
		gt.SetAttr(val.InternString("__doc__"), val.NewStringVal(GoString(docP)))
	}
	// tp_dealloc @48
	if dealloc := *(*unsafe.Pointer)(unsafe.Add(t, 48)); dealloc != nil {
		gt.Native.CDealloc = dealloc
	}
	// tp_new @312
	if np := *(*unsafe.Pointer)(unsafe.Add(t, 312)); np != nil {
		gt.Native.CNew = np
		installCNew(gt, np)
	}
	// tp_init @296
	if ip := *(*unsafe.Pointer)(unsafe.Add(t, 296)); ip != nil {
		gt.Native.CInit = ip
		installCInit(gt, ip)
	}
	// tp_call @128
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 128)); cp != nil {
		cptr := cp
		gt.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			// reuse FromSpec bridge logic via helper.
			selfStub := val.ArenaBorrowStub(callable)
			tupStub := val.ArenaToStub(val.NewTupleVal(args...))
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b, c unsafe.Pointer) unsafe.Pointer
			dynload.RegisterFunc(&fn, uintptr(cptr))
			res := fn(selfStub, tupStub, nil)
			if res == nil {
				return val.NoneVal()
			}
			return val.ArenaFromStub(res)
		}
	}
	// tp_repr @88, tp_str @136, tp_hash @120, tp_richcompare @200, tp_iter @216, tp_iternext @224
	applyStaticMethodSlots(gt, t)
	// tp_methods @232, tp_members @240, tp_getset @248
	if mp := *(*unsafe.Pointer)(unsafe.Add(t, 232)); mp != nil {
		addMethodsToType(gt, mp)
	}
	if mp := *(*unsafe.Pointer)(unsafe.Add(t, 240)); mp != nil {
		addMembersToType(gt, mp)
	}
	if mp := *(*unsafe.Pointer)(unsafe.Add(t, 248)); mp != nil {
		addGetSetsToType(gt, mp)
	}
	// tp_as_number @96, tp_as_sequence @104, tp_as_mapping @112, tp_as_buffer @160
	applyStaticNumberSequenceMapping(gt, t)
	// tp_getattro @144, tp_setattro @152
	if gp := *(*unsafe.Pointer)(unsafe.Add(t, 144)); gp != nil {
		gt.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			return callCBinary(ctx, gp, obj, name)
		}
	}
	if sp := *(*unsafe.Pointer)(unsafe.Add(t, 152)); sp != nil {
		gt.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b, c unsafe.Pointer) int32
			dynload.RegisterFunc(&fn, uintptr(sp))
			fn(val.ArenaBorrowStub(obj), val.ArenaBorrowStub(name), val.ArenaBorrowStub(value))
			return val.NoneVal()
		}
	}
	// tp_dict @264 (extra dict to merge)
	if dp := *(*unsafe.Pointer)(unsafe.Add(t, 264)); dp != nil {
		dv := val.ArenaFromStub(dp)
		if dv.IsDict() {
			for _, it := range dv.AsDict().Items() {
				gt.SetAttr(it.Key, it.Value)
			}
		}
	}
	val.AliasShadowType(val.GetDefaultArena(), gt, t)
	populateShadowTypeCallbacks(gt)
	gt.InvalidateVersion()
	return 0
}

func applyStaticMethodSlots(gt *val.PyType, t unsafe.Pointer) {
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 88)); cp != nil {
		cptr := cp
		gt.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal(gt.Name+".__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCUnary(ctx, cptr, args[0])
		}))
	}
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 136)); cp != nil {
		cptr := cp
		gt.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal(gt.Name+".__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCUnary(ctx, cptr, args[0])
		}))
	}
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 120)); cp != nil {
		cptr := cp
		gt.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal(gt.Name+".__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a unsafe.Pointer) int64
			dynload.RegisterFunc(&fn, uintptr(cptr))
			h := fn(val.ArenaBorrowStub(args[0]))
			return val.Int64Val(h)
		}))
	}
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 200)); cp != nil {
		installRichCompare(gt, cp)
	}
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 216)); cp != nil {
		cptr := cp
		gt.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value { return callCUnary(ctx, cptr, o) }
	}
	if cp := *(*unsafe.Pointer)(unsafe.Add(t, 224)); cp != nil {
		cptr := cp
		gt.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			v := callCUnary(ctx, cptr, o)
			if ctx != nil && ctx.HasException() {
				exc := ctx.ActiveException
				ctx.ClearException()
				if val.IsInstance(exc, val.StopIterationType) {
					return val.NoneVal(), val.IterExhausted
				}
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
			return v, val.IterYielded
		}
	}
}

func applyStaticNumberSequenceMapping(gt *val.PyType, t unsafe.Pointer) {
	numPtr := *(*unsafe.Pointer)(unsafe.Add(t, 96))
	if numPtr != nil {
		// offsets in 8-byte words.
		applyNumberMethods(gt, numPtr)
	}
	seqPtr := *(*unsafe.Pointer)(unsafe.Add(t, 104))
	if seqPtr != nil {
		applySequenceMethods(gt, seqPtr)
	}
	mapPtr := *(*unsafe.Pointer)(unsafe.Add(t, 112))
	if mapPtr != nil {
		applyMappingMethods(gt, mapPtr)
	}
	bufPtr := *(*unsafe.Pointer)(unsafe.Add(t, 160))
	if bufPtr != nil {
		getPtr := *(*unsafe.Pointer)(bufPtr)
		if getPtr != nil {
			gt.Native.AsBuffer = getPtr
			releasePtr := *(*unsafe.Pointer)(unsafe.Add(bufPtr, 8))
			_ = releasePtr
			gt.Slots.TpAsBuffer = makeCBufferSlot(getPtr, gt.Native)
		}
	}
}

func applyNumberMethods(gt *val.PyType, num unsafe.Pointer) {
	get := func(idx int) unsafe.Pointer {
		return *(*unsafe.Pointer)(unsafe.Add(num, idx*8))
	}
	if p := get(0); p != nil {
		cptr := p
		gt.Slots.NbAdd = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
		gt.Dict.Set(val.InternString("__add__"), val.NewBuiltinFuncVal(gt.Name+".__add__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinary(ctx, cptr, args[0], args[1])
		}))
	}
	if p := get(1); p != nil {
		cptr := p
		gt.Slots.NbSub = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
		gt.Dict.Set(val.InternString("__sub__"), val.NewBuiltinFuncVal(gt.Name+".__sub__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinary(ctx, cptr, args[0], args[1])
		}))
	}
	if p := get(2); p != nil {
		cptr := p
		gt.Slots.NbMul = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
		gt.Dict.Set(val.InternString("__mul__"), val.NewBuiltinFuncVal(gt.Name+".__mul__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinary(ctx, cptr, args[0], args[1])
		}))
	}
	if p := get(8); p != nil {
		cptr := p
		gt.Slots.NbAbs = func(ctx *val.ExecContext, a val.Value) val.Value { return callCUnary(ctx, cptr, a) }
		gt.Dict.Set(val.InternString("__abs__"), val.NewBuiltinFuncVal(gt.Name+".__abs__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCUnary(ctx, cptr, args[0])
		}))
	}
	if p := get(9); p != nil {
		cptr := p
		gt.Dict.Set(val.InternString("__bool__"), val.NewBuiltinFuncVal(gt.Name+".__bool__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a unsafe.Pointer) int32
			dynload.RegisterFunc(&fn, uintptr(cptr))
			r := fn(val.ArenaBorrowStub(args[0]))
			return val.BoolVal(r != 0)
		}))
	}
	if p := get(34); p != nil { // nb_matrix_multiply
		cptr := p
		gt.Dict.Set(val.InternString("__matmul__"), val.NewBuiltinFuncVal(gt.Name+".__matmul__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinary(ctx, cptr, args[0], args[1])
		}))
	}
}

func applySequenceMethods(gt *val.PyType, seq unsafe.Pointer) {
	get := func(idx int) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Add(seq, idx*8)) }
	if p := get(0); p != nil { // sq_length
		cptr := p
		gt.Slots.SqLen = func(ctx *val.ExecContext, a val.Value) int64 {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a unsafe.Pointer) int64
			dynload.RegisterFunc(&fn, uintptr(cptr))
			return fn(val.ArenaBorrowStub(a))
		}
		gt.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal(gt.Name+".__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.Int64Val(gt.Slots.SqLen(ctx, args[0]))
		}))
	}
	if p := get(3); p != nil { // sq_item
		cptr := p
		gt.Slots.SqItem = func(ctx *val.ExecContext, a, b val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a unsafe.Pointer, idx int64) unsafe.Pointer
			dynload.RegisterFunc(&fn, uintptr(cptr))
			r := fn(val.ArenaBorrowStub(a), b.AsInt64())
			if r == nil {
				return val.NoneVal()
			}
			return val.ArenaFromStub(r)
		}
	}
	if p := get(5); p != nil { // sq_ass_item
		cptr := p
		gt.Slots.SqAssItem = func(ctx *val.ExecContext, a, b, c val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a unsafe.Pointer, idx int64, v unsafe.Pointer) int32
			dynload.RegisterFunc(&fn, uintptr(cptr))
			var vs unsafe.Pointer
			if !c.IsNone() {
				vs = val.ArenaBorrowStub(c)
			}
			fn(val.ArenaBorrowStub(a), b.AsInt64(), vs)
			return val.NoneVal()
		}
	}
	if p := get(1); p != nil { // sq_concat
		cptr := p
		gt.Dict.Set(val.InternString("__concat__"), val.NewBuiltinFuncVal(gt.Name+".sq_concat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinary(ctx, cptr, args[0], args[1])
		}))
	}
	if p := get(7); p != nil { // sq_contains
		cptr := p
		gt.Dict.Set(val.InternString("__contains__"), val.NewBuiltinFuncVal(gt.Name+".__contains__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b unsafe.Pointer) int32
			dynload.RegisterFunc(&fn, uintptr(cptr))
			r := fn(val.ArenaBorrowStub(args[0]), val.ArenaBorrowStub(args[1]))
			return val.BoolVal(r != 0)
		}))
	}
}

func applyMappingMethods(gt *val.PyType, mp unsafe.Pointer) {
	get := func(idx int) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Add(mp, idx*8)) }
	if p := get(0); p != nil {
		cptr := p
		gt.Slots.SqLen = func(ctx *val.ExecContext, a val.Value) int64 {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a unsafe.Pointer) int64
			dynload.RegisterFunc(&fn, uintptr(cptr))
			return fn(val.ArenaBorrowStub(a))
		}
	}
	if p := get(1); p != nil {
		cptr := p
		gt.Slots.MpSubscript = func(ctx *val.ExecContext, a, b val.Value) val.Value { return callCBinary(ctx, cptr, a, b) }
		gt.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal(gt.Name+".__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return callCBinary(ctx, cptr, args[0], args[1])
		}))
	}
	if p := get(2); p != nil {
		cptr := p
		gt.Slots.MpAssSubscript = func(ctx *val.ExecContext, a, b, c val.Value) val.Value {
			rel := AcquireGIL(ctx)
			defer rel()
			var fn func(a, b, c unsafe.Pointer) int32
			dynload.RegisterFunc(&fn, uintptr(cptr))
			var vs unsafe.Pointer
			if !c.IsNone() {
				vs = val.ArenaBorrowStub(c)
			}
			fn(val.ArenaBorrowStub(a), val.ArenaBorrowStub(b), vs)
			return val.NoneVal()
		}
		gt.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal(gt.Name+".__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			gt.Slots.MpAssSubscript(ctx, args[0], args[1], args[2])
			return val.NoneVal()
		}))
	}
}

func populateShadowTypeCallbacks(t *val.PyType) {
	blk := val.ArenaShadowType(t)
	if blk == nil {
		return
	}
	// Ensure tp_alloc/tp_free/tp_new/tp_init/tp_dealloc in shadow block point at Go callbacks.
	// Offsets: tp_alloc 304, tp_free 320, tp_new 312, tp_init 296, tp_dealloc 48.
	// Only set if currently nil (preserve C-provided values).
	setIfNil := func(off int, cb uintptr) {
		if *(*uintptr)(unsafe.Add(blk, off)) == 0 {
			*(*uintptr)(unsafe.Add(blk, off)) = cb
		}
	}
	// Generic alloc returns fresh zeroed instance pointer (arena payload). It is used by
	// C tp_new implementations that call type->tp_alloc(type, 0).
	allocCb := dynload.NewCallback(goTpAlloc)
	freeCb := dynload.NewCallback(goTpFree)
	newCb := dynload.NewCallback(goTpNew)
	initCb := dynload.NewCallback(goTpInit)
	deallocCb := dynload.NewCallback(goTpDealloc)
	setIfNil(304, allocCb)
	setIfNil(320, freeCb)
	setIfNil(312, newCb)
	setIfNil(296, initCb)
	setIfNil(48, deallocCb)
	// tp_basicsize/itemsize reflect Go type (effective incl. C bases for Python subclasses).
	eff := t.Basicsize
	for _, b := range t.MRO {
		if b != nil && b.Basicsize > eff {
			eff = b.Basicsize
		}
	}
	if eff == 0 {
		eff = 16
	}
	*(*int64)(unsafe.Add(blk, 32)) = int64(eff)
	*(*int64)(unsafe.Add(blk, 40)) = int64(t.Itemsize)
}

func goTpAlloc(typ unsafe.Pointer, nitems int64) unsafe.Pointer {
	t := val.ArenaTypeFromShadow(typ)
	if t == nil {
		return nil
	}
	// Allocate a new instance; for heap types this allocates the arena payload eagerly.
	// For C types with Python subclass, NewInstanceOfType returns PyCInstance.
	v := val.NewInstanceOfType(t)
	// If t has itemsize, nitems scales payload; our arena alloc uses fixed basicsize for MVP
	// (variable-length objects like tuples need itemsize handling in Val layer; C types
	// with tp_itemsize are rare and will need Val support).
	_ = nitems
	return val.AllocCInstanceStub(v)
}

func goTpFree(p unsafe.Pointer) {
	if p == nil {
		return
	}
	// Free is called by tp_dealloc after C cleanup; arena block is append-only so leak the block.
	// Still clear reverse mapping so a subsequent FromStub on the stale pointer does not alias.
	// No-op: actual handle freeing happens in _Py_Dealloc path.
}

func goTpNew(typ unsafe.Pointer, args, kwargs unsafe.Pointer) unsafe.Pointer {
	// Generic new: allocate instance without calling __init__.
	t := val.ArenaTypeFromShadow(typ)
	if t == nil {
		return nil
	}
	v := val.NewInstanceOfType(t)
	return val.AllocCInstanceStub(v)
}

func goTpInit(self, args, kwargs unsafe.Pointer) int32 {
	// Generic init: no-op success.
	return 0
}

func goTpDealloc(self unsafe.Pointer) {
	if self == nil {
		return
	}
	v := val.ArenaFromStub(self)
	if v.HeaderPtr() != nil {
		if t := v.Type(); t != nil && t.Native != nil && t.Native.CDealloc != nil {
			cptr := t.Native.CDealloc
			var fn func(unsafe.Pointer)
			dynload.RegisterFunc(&fn, uintptr(cptr))
			// Avoid infinite recursion if C dealloc calls Py_XDECREF(self) -> _Py_Dealloc -> goTpDealloc.
			// Mark guard via DeallocCalled check in CDeallocHook; here just call directly.
			func() {
				defer func() { _ = recover() }()
				fn(self)
			}()
		}
	}
	// Free payload: arena block is leaked (append-only); _Py_Dealloc path also frees handle.
}

// --- Buffer C-API (PEP 3118 bridging) ---

type bufferPin struct {
	pinner     *val.PinnerState
	shape      []int64
	strides    []int64
	suboffsets []int64
	format     []byte // NUL-terminated
	bufPtr     unsafe.Pointer
	// Off-heap Py_buffer for C-buffer providers (kept alive until Release).
	cView    []byte
	cViewPtr unsafe.Pointer
	objStub  unsafe.Pointer
}

var (
	bufMu       sync.Mutex
	liveBuffers = make(map[unsafe.Pointer]*bufferPin)
)

// PyObject_GetBuffer marshals Go PyBuffer to C Py_buffer (80 bytes, stable ABI).
func PyObject_GetBuffer(obj unsafe.Pointer, view unsafe.Pointer, flags int32) int32 {
	if obj == nil || view == nil {
		pyErrSetString(val.RuntimeErrorType, "PyObject_GetBuffer called with NULL")
		return -1
	}
	v := val.ArenaFromStub(obj)
	tp := v.Type()
	if tp == nil || tp.Slots.TpAsBuffer == nil {
		if tp != nil {
			pyErrSetString(val.TypeErrorType, "object does not support buffer protocol: "+tp.Name)
		} else {
			pyErrSetString(val.TypeErrorType, "object does not support buffer protocol")
		}
		return -1
	}
	var pybuf val.PyBuffer
	if err := tp.Slots.TpAsBuffer(nil, v, &pybuf, int(flags)); err != nil {
		pyErrSetString(val.BufferErrorType, err.Error())
		return -1
	}
	// Pin backing bytes for zero-copy.
	pin := &bufferPin{
		pinner:  &val.PinnerState{},
		bufPtr:  nil,
		objStub: val.ArenaToStub(v), // owned ref; released on PyBuffer_Release
	}
	if len(pybuf.Buf) > 0 {
		pin.pinner.Pin(pybuf.Buf)
		pin.bufPtr = unsafe.Pointer(&pybuf.Buf[0])
	}
	// Prepare format C string.
	fmtStr := pybuf.Format
	if fmtStr == "" {
		fmtStr = "B"
	}
	pin.format = append([]byte(fmtStr), 0)
	ndim := pybuf.NDim
	if ndim == 0 {
		ndim = 1
		if pybuf.ItemSize == 0 {
			pin.shape = []int64{int64(len(pybuf.Buf))}
		} else {
			pin.shape = []int64{int64(len(pybuf.Buf) / max(pybuf.ItemSize, 1))}
		}
		pin.strides = []int64{int64(max(pybuf.ItemSize, 1))}
	} else {
		pin.shape = make([]int64, ndim)
		for i := 0; i < ndim && i < len(pybuf.Shape); i++ {
			pin.shape[i] = int64(pybuf.Shape[i])
		}
		pin.strides = make([]int64, ndim)
		for i := 0; i < ndim && i < len(pybuf.Strides); i++ {
			pin.strides[i] = int64(pybuf.Strides[i])
		}
		if len(pybuf.Suboffsets) > 0 {
			pin.suboffsets = make([]int64, ndim)
			for i := 0; i < ndim && i < len(pybuf.Suboffsets); i++ {
				pin.suboffsets[i] = int64(pybuf.Suboffsets[i])
			}
		}
	}
	itemSize := pybuf.ItemSize
	if itemSize == 0 {
		if sz, err := val.FormatSize(fmtStr); err == nil && sz > 0 {
			itemSize = sz
		} else {
			itemSize = 1
		}
	}
	// len = total bytes = product(shape)*itemSize (ndim>0) else len(Buf).
	total := int64(len(pybuf.Buf))
	if ndim > 0 && len(pin.shape) > 0 {
		prod := int64(1)
		for _, d := range pin.shape {
			prod *= d
		}
		total = prod * int64(itemSize)
	}
	readonly := 0
	if pybuf.ReadOnly {
		readonly = 1
	}
	// Write C Py_buffer @ view.
	*(*unsafe.Pointer)(unsafe.Add(view, 0)) = pin.bufPtr
	*(*unsafe.Pointer)(unsafe.Add(view, 8)) = pin.objStub
	*(*int64)(unsafe.Add(view, 16)) = total
	*(*int64)(unsafe.Add(view, 24)) = int64(itemSize)
	*(*int32)(unsafe.Add(view, 32)) = int32(readonly)
	*(*int32)(unsafe.Add(view, 36)) = int32(ndim)
	*(*unsafe.Pointer)(unsafe.Add(view, 40)) = unsafe.Pointer(&pin.format[0])
	var shapePtr, stridesPtr, suboffsetsPtr unsafe.Pointer
	if len(pin.shape) > 0 {
		shapePtr = unsafe.Pointer(&pin.shape[0])
	}
	if len(pin.strides) > 0 {
		stridesPtr = unsafe.Pointer(&pin.strides[0])
	}
	if len(pin.suboffsets) > 0 {
		suboffsetsPtr = unsafe.Pointer(&pin.suboffsets[0])
	}
	*(*unsafe.Pointer)(unsafe.Add(view, 48)) = shapePtr
	*(*unsafe.Pointer)(unsafe.Add(view, 56)) = stridesPtr
	*(*unsafe.Pointer)(unsafe.Add(view, 64)) = suboffsetsPtr
	*(*unsafe.Pointer)(unsafe.Add(view, 72)) = unsafe.Pointer(pin)
	bufMu.Lock()
	liveBuffers[unsafe.Pointer(pin)] = pin
	bufMu.Unlock()
	return 0
}

// PyBuffer_Release releases a view obtained via PyObject_GetBuffer.
func PyBuffer_Release(view unsafe.Pointer) {
	if view == nil {
		return
	}
	internal := *(*unsafe.Pointer)(unsafe.Add(view, 72))
	objStub := *(*unsafe.Pointer)(unsafe.Add(view, 8))
	if internal == nil {
		return
	}
	bufMu.Lock()
	pin, ok := liveBuffers[internal]
	if ok {
		delete(liveBuffers, internal)
	}
	bufMu.Unlock()
	if ok && pin != nil {
		pin.pinner.Unpin()
	}
	// DECREF obj (balances the ToStub new ref in GetBuffer).
	if objStub != nil {
		rc := *(*int64)(objStub)
		if rc < val.ImmortalRefcnt && rc > 0 {
			rc--
			*(*int64)(objStub) = rc
			if rc == 0 {
				_Py_Dealloc(objStub)
			}
		}
	}
}

func PyObject_CheckBuffer(obj unsafe.Pointer) int32 {
	if obj == nil {
		return 0
	}
	v := val.ArenaFromStub(obj)
	if tp := v.Type(); tp != nil && tp.Slots.TpAsBuffer != nil {
		return 1
	}
	return 0
}

func PyBuffer_SizeFromFormat(format unsafe.Pointer) int64 {
	if format == nil {
		return -1
	}
	s := GoString(format)
	if s == "" {
		return 1
	}
	// Handle count prefix: "4B" -> 4, "16s" -> 16.
	idx := 0
	for idx < len(s) && s[idx] >= '0' && s[idx] <= '9' {
		idx++
	}
	count := int64(1)
	if idx > 0 {
		var n int64
		for _, c := range s[:idx] {
			n = n*10 + int64(c-'0')
		}
		count = n
	}
	rest := s[idx:]
	sz, err := val.FormatSize(rest)
	if err != nil {
		return -1
	}
	if rest == "s" {
		return count
	}
	return int64(sz) * count
}

func PyBuffer_FillContiguousStrides(ndims int32, shape, strides unsafe.Pointer, itemsize int32, order byte) {
	if ndims <= 0 || shape == nil || strides == nil {
		return
	}
	shapeGo := make([]int, ndims)
	stridePtr := unsafe.Add(strides, 0)
	for i := range ndims {
		shapeGo[i] = int(*(*int64)(unsafe.Add(shape, int(i)*8)))
	}
	var s []int
	if order == 'F' || order == 'f' {
		s = val.FContiguousStrides(shapeGo, int(itemsize))
	} else {
		s = val.CContiguousStrides(shapeGo, int(itemsize))
	}
	for i := range int(ndims) {
		*(*int64)(unsafe.Add(stridePtr, i*8)) = int64(s[i])
	}
}

func PyBuffer_FillInfo(view, o, buf unsafe.Pointer, length int64, readonly int32, flags int32) int32 {
	if view == nil {
		return -1
	}
	// Fill as contiguous unsigned bytes (like PyBuffer_FillInfo).
	*(*unsafe.Pointer)(unsafe.Add(view, 0)) = buf
	*(*unsafe.Pointer)(unsafe.Add(view, 8)) = o
	*(*int64)(unsafe.Add(view, 16)) = length
	*(*int64)(unsafe.Add(view, 24)) = 1
	*(*int32)(unsafe.Add(view, 32)) = readonly
	*(*int32)(unsafe.Add(view, 36)) = 1
	// format "B" stable buffer kept leaked (CPython leak is acceptable for test scale).
	fmtBytes := []byte("B\x00")
	*(*unsafe.Pointer)(unsafe.Add(view, 40)) = unsafe.Pointer(&fmtBytes[0])
	shape := []int64{length}
	strides := []int64{1}
	*(*unsafe.Pointer)(unsafe.Add(view, 48)) = unsafe.Pointer(&shape[0])
	*(*unsafe.Pointer)(unsafe.Add(view, 56)) = unsafe.Pointer(&strides[0])
	*(*unsafe.Pointer)(unsafe.Add(view, 64)) = nil
	// Keep arrays leaked via closure (global keepalive not needed for short test lifetime).
	// Store in liveBuffers under view pointer to prevent GC of heap arrays? Go heap arrays referenced only by C pointers
	// would be invisible to GC. Pin them by holding in a global.
	keepBufferArrays(view, shape, strides)
	*(*unsafe.Pointer)(unsafe.Add(view, 72)) = nil
	return 0
}

var keepMu sync.Mutex
var keepArrays = make(map[unsafe.Pointer][]any)

func keepBufferArrays(view unsafe.Pointer, shape, strides []int64) {
	keepMu.Lock()
	defer keepMu.Unlock()
	keepArrays[view] = []any{shape, strides}
}

func PyBuffer_IsContiguous(view unsafe.Pointer, order byte) int32 {
	if view == nil {
		return 0
	}
	shapePtr := *(*unsafe.Pointer)(unsafe.Add(view, 48))
	stridesPtr := *(*unsafe.Pointer)(unsafe.Add(view, 56))
	itemsize := *(*int64)(unsafe.Add(view, 24))
	ndim := *(*int32)(unsafe.Add(view, 36))
	if ndim <= 0 {
		return 1
	}
	if shapePtr == nil || stridesPtr == nil {
		return 0
	}
	shape := make([]int, ndim)
	strides := make([]int, ndim)
	for i := range ndim {
		shape[i] = int(*(*int64)(unsafe.Add(shapePtr, int(i)*8)))
		strides[i] = int(*(*int64)(unsafe.Add(stridesPtr, int(i)*8)))
	}
	var ok bool
	if order == 'F' || order == 'f' {
		ok = val.IsFContiguous(shape, strides, int(itemsize))
	} else {
		// 'C' (default), 'A' accepts either.
		if order == 'A' || order == 'a' {
			ok = val.IsCContiguous(shape, strides, int(itemsize)) || val.IsFContiguous(shape, strides, int(itemsize))
		} else {
			ok = val.IsCContiguous(shape, strides, int(itemsize))
		}
	}
	if ok {
		return 1
	}
	return 0
}

// makeCBufferSlot bridges a C bf_getbuffer to Go TpAsBuffer (copy semantics, then pin).
func makeCBufferSlot(cGetPtr unsafe.Pointer, native *val.NativeTypeInfo) val.BufferFunc {
	return func(ctx *val.ExecContext, obj val.Value, view *val.PyBuffer, flags int) error {
		// Allocate 80B off-heap Py_buffer.
		cView := make([]byte, 80)
		cViewPtr := unsafe.Pointer(&cView[0])
		objStub := val.ArenaBorrowStub(obj)
		var fn func(o, v unsafe.Pointer, f int32) int32
		dynload.RegisterFunc(&fn, uintptr(cGetPtr))
		rel := AcquireGIL(ctx)
		defer rel()
		ret := fn(objStub, cViewPtr, int32(flags))
		if ret != 0 {
			if CurrentContext() != nil && CurrentContext().HasException() {
				return errors.New("c bf_getbuffer failed")
			}
			return errors.New("c bf_getbuffer failed")
		}
		bufPtr := *(*unsafe.Pointer)(unsafe.Add(cViewPtr, 0))
		length := int(*(*int64)(unsafe.Add(cViewPtr, 16)))
		itemSize := int(*(*int64)(unsafe.Add(cViewPtr, 24)))
		readonly := *(*int32)(unsafe.Add(cViewPtr, 32)) != 0
		ndim := int(*(*int32)(unsafe.Add(cViewPtr, 36)))
		fmtPtr := *(*unsafe.Pointer)(unsafe.Add(cViewPtr, 40))
		shapePtr := *(*unsafe.Pointer)(unsafe.Add(cViewPtr, 48))
		stridesPtr := *(*unsafe.Pointer)(unsafe.Add(cViewPtr, 56))
		formatStr := "B"
		if fmtPtr != nil {
			formatStr = GoString(fmtPtr)
		}
		if itemSize == 0 {
			if sz, err := val.FormatSize(formatStr); err == nil {
				itemSize = sz
			} else {
				itemSize = 1
			}
		}
		if readonly && (flags&val.PyBUF_WRITABLE) != 0 {
			return errors.New("cannot create writable buffer view into read-only buffer")
		}
		// Copy bytes (C may point at static or heap memory we cannot pin safely across Release).
		var data []byte
		if bufPtr != nil && length > 0 {
			data = make([]byte, length)
			for i := range length {
				data[i] = *(*byte)(unsafe.Add(bufPtr, i))
			}
		}
		view.Buf = data
		view.Obj = obj
		view.Format = formatStr
		view.ItemSize = itemSize
		view.ReadOnly = readonly
		if ndim <= 0 {
			view.NDim = 1
			if itemSize > 0 {
				view.Shape = []int{len(data) / itemSize}
			} else {
				view.Shape = []int{len(data)}
			}
			view.Strides = val.CContiguousStrides(view.Shape, itemSize)
		} else {
			view.NDim = ndim
			view.Shape = make([]int, ndim)
			view.Strides = make([]int, ndim)
			for i := range ndim {
				if shapePtr != nil {
					view.Shape[i] = int(*(*int64)(unsafe.Add(shapePtr, i*8)))
				}
				if stridesPtr != nil {
					view.Strides[i] = int(*(*int64)(unsafe.Add(stridesPtr, i*8)))
				}
			}
		}
		// Keep cView alive until Release.
		pin := &bufferPin{cView: cView, cViewPtr: cViewPtr, bufPtr: bufPtr}
		view.Release = func() {
			// Release callback is stored separately (Py_bf_releasebuffer). We do not have it here;
			// the common case releases in-place copy view.
			_ = pin
			_ = native
		}
		runtime.KeepAlive(pin)
		return nil
	}
}
