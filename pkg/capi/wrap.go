package capi

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

// METH flags (CPython methodobject.h)
const (
	METH_VARARGS  = 0x0001
	METH_KEYWORDS = 0x0002
	METH_NOARGS   = 0x0004
	METH_O        = 0x0008
	METH_CLASS    = 0x0010
	METH_STATIC   = 0x0020
	METH_FASTCALL = 0x0080
)

// WrapPyCFunction creates a fython builtin function Value that invokes the C function at cfnAddr.
// selfVal is the module (or object) self for module functions.
// namePtr is C string pointer for method name.
func WrapPyCFunction(cfn unsafe.Pointer, flags int32, selfVal val.Value, namePtr unsafe.Pointer) val.Value {
	name := "pyfunc"
	if namePtr != nil {
		name = GoString(namePtr)
	}
	cAddr := cfn
	selfStub := val.ArenaBorrowStub(selfVal)

	wrapper := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// args is slice of call args (positional + kw values), kwnames is names.
		// For builtin funcs, VM passes args including? For module funcs (NoBind), args does not include self; for bound methods, it may include self? Need to handle both:
		// Our wrapper registered via NewModuleFuncVal (NoBind) will be called without self prepended; via NewBuiltinFuncVal (binds) will be called with self as args[0] when accessed as method.
		// To be safe, detect self in args: if flags has METH_STATIC, no self; else if callable is bound (NoBind false and first arg == selfVal), strip it.
		// For module functions stored in module dict, accessing via module.func returns bound method? In fython, module funcs are NewModuleFuncVal with NoBind true, so they don't bind, args are pure call args.
		// For methods on types (not module), same.
		// So assume args are pure call args.

		// Take the Virtual GIL and bind ctx so C-API handlers (SetError etc.) operate on the correct ExecContext.
		release := AcquireGIL(ctx)
		defer release()

		var resultStub unsafe.Pointer
		var recovered any

		func() {
			defer func() { recovered = recover() }()
			switch {
			case flags&METH_FASTCALL != 0 && flags&METH_KEYWORDS != 0:
				resultStub = callFastCallKeywords(cAddr, selfStub, args, kwnames)
			case flags&METH_FASTCALL != 0:
				resultStub = callFastCall(cAddr, selfStub, args)
			case flags&METH_KEYWORDS != 0:
				resultStub = callVarargsKeywords(cAddr, selfStub, args, kwnames)
			case flags&METH_VARARGS != 0:
				resultStub = callVarargs(cAddr, selfStub, args)
			case flags&METH_NOARGS != 0:
				resultStub = callNoargs(cAddr, selfStub)
			case flags&METH_O != 0:
				if len(args) != 1 {
					CurrentContext().Raise(val.NewTypeError("expected exactly 1 argument"))
					return
				}
				resultStub = callO(cAddr, selfStub, args[0])
			default:
				// Assume VARARGS
				resultStub = callVarargs(cAddr, selfStub, args)
			}
		}()
		if recovered != nil {
			if ev, ok := recovered.(val.Value); ok {
				SetError(ev)
			} else {
				pyErrSetString(val.RuntimeErrorType, "C function raised")
			}
			return val.NoneVal()
		}
		if resultStub == nil {
			if CurrentContext() != nil && CurrentContext().HasException() {
				// Return None as placeholder; VM will see exception via HasException and unwind.
				return val.NoneVal()
			}
			// No exception set but NULL returned — treat as RuntimeError
			pyErrSetString(val.RuntimeErrorType, "C function returned NULL")
			return val.NoneVal()
		}
		return val.ArenaFromStub(resultStub)
	}

	// Use NewModuleFuncVal for module functions (no binding) — correct for PyMethodDef on modules.
	// For methods on types, caller should convert to bound? Module functions are always NoBind.
	return val.NewModuleFuncVal(name, wrapper)
}

func callVarargs(cAddr unsafe.Pointer, selfStub unsafe.Pointer, args []val.Value) unsafe.Pointer {
	var fn func(self unsafe.Pointer, args unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cAddr))
	// Pack args into tuple
	tupVal := val.NewTupleVal(args...)
	tupStub := val.ArenaToStub(tupVal)
	return fn(selfStub, tupStub)
}
func callVarargsKeywords(cAddr unsafe.Pointer, selfStub unsafe.Pointer, args []val.Value, kwnames []val.Value) unsafe.Pointer {
	var fn func(self unsafe.Pointer, args unsafe.Pointer, kwargs unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cAddr))
	// kwargs dict: last len(kwnames) args are kw values? In fython's Vectorcall, args slice includes kw values appended after positional? Check calling convention:
	// In VM, for CALL with kwnames, args slice includes positional + kw values where kwnames length = number of kw. npos = len(args)-len(kwnames).
	// So split.
	nkw := len(kwnames)
	npos := len(args) - nkw
	var posArgs []val.Value
	var kwDict *val.PyDict
	if npos < 0 {
		posArgs = args
		kwDict = val.NewDictVal(0).AsDict()
	} else {
		posArgs = args[:npos]
		kwDict = val.NewDictVal(nkw).AsDict()
		for i, kn := range kwnames {
			kwDict.Set(kn, args[npos+i])
		}
	}
	posTuple := val.NewTupleVal(posArgs...)
	posStub := val.ArenaToStub(posTuple)
	kwStub := val.ArenaToStub(val.DictVal(kwDict))
	return fn(selfStub, posStub, kwStub)
}
func callNoargs(cAddr unsafe.Pointer, selfStub unsafe.Pointer) unsafe.Pointer {
	var fn func(self unsafe.Pointer, ignored unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cAddr))
	noneStub := val.ArenaBorrowStub(val.NoneVal())
	return fn(selfStub, noneStub)
}
func callO(cAddr unsafe.Pointer, selfStub unsafe.Pointer, arg val.Value) unsafe.Pointer {
	var fn func(self unsafe.Pointer, arg unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cAddr))
	argStub := val.ArenaBorrowStub(arg)
	return fn(selfStub, argStub)
}
func callFastCall(cAddr unsafe.Pointer, selfStub unsafe.Pointer, args []val.Value) unsafe.Pointer {
	var fn func(self unsafe.Pointer, args unsafe.Pointer, nargs int64) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cAddr))
	// args array of PyObject*
	stubs := make([]unsafe.Pointer, len(args))
	for i, a := range args {
		stubs[i] = val.ArenaBorrowStub(a)
	}
	// Keep slice alive across call: Go slice header points to array; pass pointer to first element.
	// Use unsafe pointer to backing array.
	var arrPtr unsafe.Pointer
	if len(stubs) > 0 {
		arrPtr = unsafe.Pointer(&stubs[0])
	} else {
		arrPtr = nil
	}
	return fn(selfStub, arrPtr, int64(len(args)))
}
func callFastCallKeywords(cAddr unsafe.Pointer, selfStub unsafe.Pointer, args []val.Value, kwnames []val.Value) unsafe.Pointer {
	var fn func(self unsafe.Pointer, args unsafe.Pointer, nargs int64, kwnames unsafe.Pointer) unsafe.Pointer
	dynload.RegisterFunc(&fn, uintptr(cAddr))
	// fython args includes kw values after positional; kwnames gives names.
	// Vectorcall convention for FASTCALL_WITH_KEYWORDS: args array = positional + kw values, kwnames is tuple of names.
	nkw := len(kwnames)
	npos := len(args) - nkw
	if npos < 0 {
		npos = len(args)
		nkw = 0
	}
	stubs := make([]unsafe.Pointer, len(args))
	for i, a := range args {
		stubs[i] = val.ArenaBorrowStub(a)
	}
	var arrPtr unsafe.Pointer
	if len(stubs) > 0 {
		arrPtr = unsafe.Pointer(&stubs[0])
	}
	var kwTupleStub unsafe.Pointer
	if nkw > 0 {
		t := val.NewTupleVal(kwnames...)
		kwTupleStub = val.ArenaToStub(t)
	}
	// nargs = npos (positional count). Flag bits for VECTORCALL? In 3.15, nargsf includes flag 0x8000 for offset; we pass npos as is.
	return fn(selfStub, arrPtr, int64(npos), kwTupleStub)
}

// CallCapsuleDestructor calls a capsule destructor C function: void (*)(PyObject*)
// Exported for errors.go _Py_Dealloc.
func CallCapsuleDestructor(dtor unsafe.Pointer, caps unsafe.Pointer) {
	var fn func(caps unsafe.Pointer)
	dynload.RegisterFunc(&fn, uintptr(dtor))
	fn(caps)
}

// PyCFunction_NewEx(PyMethodDef *ml, PyObject *self, PyObject *module) -> PyObject*
func PyCFunction_NewEx(ml unsafe.Pointer, self unsafe.Pointer, module unsafe.Pointer) unsafe.Pointer {
	if ml == nil {
		pyErrSetTypeError("PyCFunction_NewEx called with NULL")
		return nil
	}
	// Parse PyMethodDef at ml
	namePtr := *(*unsafe.Pointer)(ml)
	methPtr := *(*unsafe.Pointer)(unsafe.Add(ml, 8))
	flags := *(*int32)(unsafe.Add(ml, 16))
	var selfVal val.Value
	if self != nil {
		selfVal = val.ArenaFromStub(self)
	} else {
		// Self may be NULL for free functions; use None
		selfVal = val.NoneVal()
	}
	fnVal := WrapPyCFunction(methPtr, flags, selfVal, namePtr)
	return val.ArenaToStub(fnVal)
}
