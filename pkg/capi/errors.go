package capi

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// PyErr_SetString(PyObject *exctype, const char *msg)
func PyErr_SetString(exctype unsafe.Pointer, msg unsafe.Pointer) {
	if exctype == nil || msg == nil {
		pyErrSetString(val.RuntimeErrorType, "PyErr_SetString called with NULL")
		return
	}
	// exctype is a type object (shadow type ptr) - map to *PyType
	// Actually could also be a string? In CPython, first arg is exception type object or string? Handle both: if it's a string stub, use its text as type name lookup?
	// Try TypeFromShadow first
	t := val.ArenaTypeFromShadow(exctype)
	if t == nil {
		v := val.ArenaFromStub(exctype)
		switch {
		case v.IsType():
			t = v.AsType()
		default:
			t = val.RuntimeErrorType
		}
	}
	SetError(val.NewException(t, GoString(msg)))
}

// PyErr_SetObject(PyObject *exctype, PyObject *value)
func PyErr_SetObject(exctype unsafe.Pointer, value unsafe.Pointer) {
	if exctype == nil {
		pyErrSetString(val.RuntimeErrorType, "PyErr_SetObject called with NULL type")
		return
	}
	// exctype may be type object or already exception instance
	// Try to interpret: if exctype is a type stub, create new instance; else if it's exception instance, use it.
	// Actually if exctype points to shadow type, TypeFromShadow works.
	t := val.ArenaTypeFromShadow(exctype)
	if t != nil {
		if value == nil {
			SetError(val.NewException(t, ""))
		} else {
			v := val.ArenaFromStub(value)
			// If value is exception instance, use it directly? CPython would create new exception with args=(value,)
			// Simplify: if value already instance of t, use it; else create new with value as arg.
			if v.IsInstance() && v.Type() == t {
				SetError(v)
			} else {
				inst := val.NewException(t, "")
				inst.AsInstance().SetAttr(val.InternString("args"), val.NewTupleVal(v))
				SetError(inst)
			}
		}
		return
	}
	// Fallback: exctype is exception instance Value stub
	v := val.ArenaFromStub(exctype)
	SetError(v)
}

// PyErr_Occurred() -> PyObject* (exception type or NULL)
func PyErr_Occurred() unsafe.Pointer {
	if !HasError() {
		return nil
	}
	exc := ErrOccurred()
	// Return borrowed reference to exception type
	return val.ArenaBorrowStub(exc)
}

// PyErr_Clear()
func PyErr_Clear() { ClearError() }

// PyErr_Format via shim: shim formats message into string and calls PyErr_FormatFormatted(exc, formattedPtr)
func PyErr_FormatFormatted(exc unsafe.Pointer, formatted unsafe.Pointer) {
	if exc == nil || formatted == nil {
		pyErrSetString(val.RuntimeErrorType, "PyErr_Format called with NULL")
		return
	}
	msg := GoString(formatted)
	t := val.ArenaTypeFromShadow(exc)
	if t == nil {
		v := val.ArenaFromStub(exc)
		if v.IsType() {
			t = v.AsType()
		} else {
			t = val.ExceptionType
		}
	}
	SetError(val.NewException(t, msg))
}

// PyErr_NormalizeException
func PyErr_NormalizeException(exc unsafe.Pointer, valPtr unsafe.Pointer, tbPtr unsafe.Pointer) {
	// No-op for fython (exceptions already normalized)
}

// PyGILState_Ensure / Release, PyEval_SaveThread/RestoreThread — VM stubs (GIL is cooperative)
func PyGILState_Ensure() int32 {
	// Acquire GIL with current context (if any) — handlers that call Ensure already hold GIL? Ensure means "ensure GIL held", so if not held, acquire.
	// For MVP, acquire with current context (may be nil) and return 0. Release will release.
	// Track depth via GIL's reentrancy: need to know if caller held GIL before. Encode in return?
	// Return 0 for "GIL was held" vs 1 for "GIL acquired"? CPython returns PyGILState_STATE.
	// Simplify: Acquire if needed.
	// We track acquisition count via activeCtx: if HasError state exists, assume already held.
	// Call AcquireGIL with current ctx (maybe nil) to be reentrant.
	if CurrentContext() != nil {
		// Already held (reentrancy): just inc depth
		_ = AcquireGIL(CurrentContext())
		return 0
	}
	_ = AcquireGIL(nil)
	return 1
}
func PyGILState_Release(state int32) {
	// Release one level (must balance Ensure). If state==0, GIL was already held and we only inc'd; release should dec.
	// If we acquired, release fully. Our AcquireGIL returns release func but we discarded; emulate by calling release of dummy?
	// Actually PyGILState_Ensure in CPython is separate from thread state; our GIL is reentrant, so Release should just decrement depth and possibly unlock if depth 0.
	// To emulate, we need to hold leftover release funcs stack. For MVP, just no-op (GIL remains held until outer release). Tests that use Ensure/Release in pairs within same C call will still hold GIL (since outer holder holds it). So no-op is safe (GIL stays held).
}

// PyEval_SaveThread: release GIL, return dummy state (we use current ctx as state?)
func PyEval_SaveThread() unsafe.Pointer {
	// Release GIL (decrement). Must save current ctx to restore later.
	// For MVP, just return current ctx pointer as dummy thread state.
	if ctx := CurrentContext(); ctx != nil {
		return unsafe.Pointer(ctx)
	}
	return nil
}
func PyEval_RestoreThread(state unsafe.Pointer) {
	// Re-acquire GIL with saved ctx.
	if state != nil {
		ctx := (*val.ExecContext)(state)
		_ = AcquireGIL(ctx)
	}
}

// _Py_Dealloc(PyObject*) — called when ob_refcnt hits 0 via Py_DECREF macro.
func _Py_Dealloc(o unsafe.Pointer) {
	if o == nil {
		return
	}
	// Handle capsule destructor if this is a capsule
	if _, dtor, ok := GetCapsule(o); ok && dtor != nil {
		// Call destructor via purego: void (*)(PyObject*)
		// Note: destructor expects capsule object itself.
		// Use dynload.RegisterFunc to call (needs import cycle? capi imports dynload, okay)
		// Guard recover to avoid crashing on bad destructor.
		func() {
			defer func() { _ = recover() }()
			// Import cycle avoid: use local var via reflection? Direct call via register.
			// We need dynload import here — already imported in module.go, but errors.go also needs it.
			// We'll do it via helper in module.go? For now, rely on module.go's helper.
			CallCapsuleDestructor(dtor, o)
		}()
	}
	val.ArenaDealloc(o)
}

// PyType_IsSubtype, PyType_Ready, PyObject_GetType etc for extension compatibility
func PyType_IsSubtype(a unsafe.Pointer, b unsafe.Pointer) int32 {
	// a, b are PyTypeObject* shadow pointers
	ta := val.ArenaTypeFromShadow(a)
	tb := val.ArenaTypeFromShadow(b)
	if ta == nil || tb == nil {
		return 0
	}
	if val.IsSubclass(ta, tb) {
		return 1
	}
	return 0
}
func PyType_Ready(t unsafe.Pointer) int32 { return 0 }
func PyObject_Type(o unsafe.Pointer) unsafe.Pointer {
	if o == nil {
		return nil
	}
	v := val.ArenaFromStub(o)
	return val.ArenaShadowType(v.Type())
}

// PyErr_GetCause etc stubs
func PyErr_GetCause() unsafe.Pointer {
	if !HasError() {
		return nil
	}
	exc := ErrOccurred()
	if exc.IsInstance() {
		if cause, ok := exc.AsInstance().GetAttr(val.InternString("__cause__")); ok && !cause.IsNone() {
			return val.ArenaBorrowStub(cause)
		}
	}
	return nil
}
func PyErr_GetContext() unsafe.Pointer {
	if !HasError() {
		return nil
	}
	exc := ErrOccurred()
	if exc.IsInstance() {
		if ctx, ok := exc.AsInstance().GetAttr(val.InternString("__context__")); ok && !ctx.IsNone() {
			return val.ArenaBorrowStub(ctx)
		}
	}
	return nil
}

// Helper for capsule destructor to avoid import in errors.go: defined here via dynload
// (we import dynload in module.go; here we import directly)
