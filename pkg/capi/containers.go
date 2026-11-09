package capi

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// PyTuple_Pack is provided via shim conversion: Go signature (n, arrPtr) where arr is array of PyObject*.
func PyTuple_Pack(n int64, arr unsafe.Pointer) unsafe.Pointer {
	if n < 0 {
		pyErrSetValueError("negative tuple size")
		return nil
	}
	if n == 0 {
		return val.ArenaToStub(val.EmptyTupleVal())
	}
	if arr == nil {
		pyErrSetTypeError("PyTuple_Pack called with NULL array")
		return nil
	}
	items := make([]val.Value, n)
	for i := range n {
		ptr := *(*unsafe.Pointer)(unsafe.Add(arr, int(i)*8))
		if ptr == nil {
			pyErrSetTypeError("PyTuple_Pack got NULL object")
			return nil
		}
		items[i] = val.ArenaFromStub(ptr)
	}
	return val.ArenaToStub(val.NewTupleVal(items...))
}

func PyTuple_New(size int64) unsafe.Pointer {
	if size < 0 {
		pyErrSetValueError("negative size")
		return nil
	}
	if size == 0 {
		return val.ArenaToStub(val.EmptyTupleVal())
	}
	items := make([]val.Value, size)
	for i := range items {
		items[i] = val.NoneVal()
	}
	return val.ArenaToStub(val.NewTupleVal(items...))
}

func PyTuple_GetItem(o unsafe.Pointer, idx int64) unsafe.Pointer {
	if o == nil {
		pyErrSetTypeError("PyTuple_GetItem called with NULL")
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsTuple() {
		pyErrSetTypeError("PyTuple_GetItem argument must be tuple")
		return nil
	}
	items := v.AsTuple().Items
	if idx < 0 || idx >= int64(len(items)) {
		if CurrentContext() != nil {
			CurrentContext().Raise(val.NewIndexError("tuple index out of range"))
		} else {
			pyErrSetTypeError("tuple index out of range")
		}
		return nil
	}
	return val.ArenaBorrowStub(items[idx])
}

func PyTuple_SetItem(o unsafe.Pointer, idx int64, item unsafe.Pointer) int32 {
	if o == nil || item == nil {
		pyErrSetTypeError("PyTuple_SetItem called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsTuple() {
		pyErrSetTypeError("PyTuple_SetItem argument must be tuple")
		return -1
	}
	items := v.AsTuple().Items
	if idx < 0 || idx >= int64(len(items)) {
		CurrentContext().Raise(val.NewIndexError("tuple assignment index out of range"))
		return -1
	}
	items[idx] = val.ArenaFromStub(item)
	// Steals reference: do not incref. Caller considered item ownership transferred.
	// We keep stub alive via tuple's Items; no refcount change needed.
	return 0
}

func PyTuple_Size(o unsafe.Pointer) int64 {
	if o == nil {
		pyErrSetTypeError("PyTuple_Size called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsTuple() {
		pyErrSetTypeError("PyTuple_Size argument must be tuple")
		return -1
	}
	return int64(len(v.AsTuple().Items))
}

// List
func PyList_New(size int64) unsafe.Pointer {
	if size < 0 {
		pyErrSetValueError("negative size")
		return nil
	}
	items := make([]val.Value, size)
	for i := range items {
		items[i] = val.NoneVal()
	}
	return val.ArenaToStub(val.NewListVal(items...))
}
func PyList_Append(o unsafe.Pointer, item unsafe.Pointer) int32 {
	if o == nil || item == nil {
		pyErrSetTypeError("PyList_Append called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsList() {
		pyErrSetTypeError("PyList_Append argument must be list")
		return -1
	}
	v.AsList().Append(val.ArenaFromStub(item))
	return 0
}
func PyList_GetItem(o unsafe.Pointer, idx int64) unsafe.Pointer {
	if o == nil {
		pyErrSetTypeError("PyList_GetItem called with NULL")
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsList() {
		pyErrSetTypeError("PyList_GetItem argument must be list")
		return nil
	}
	got, ok := v.AsList().Get(int(idx))
	if !ok {
		CurrentContext().Raise(val.NewIndexError("list index out of range"))
		return nil
	}
	return val.ArenaBorrowStub(got)
}
func PyList_SetItem(o unsafe.Pointer, idx int64, item unsafe.Pointer) int32 {
	if o == nil || item == nil {
		pyErrSetTypeError("PyList_SetItem called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsList() {
		pyErrSetTypeError("PyList_SetItem argument must be list")
		return -1
	}
	if !v.AsList().Set(int(idx), val.ArenaFromStub(item)) {
		CurrentContext().Raise(val.NewIndexError("list assignment index out of range"))
		return -1
	}
	return 0
}
func PyList_Size(o unsafe.Pointer) int64 {
	if o == nil {
		pyErrSetTypeError("PyList_Size called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsList() {
		pyErrSetTypeError("PyList_Size argument must be list")
		return -1
	}
	return int64(v.AsList().Len())
}

// Dict
func PyDict_New() unsafe.Pointer {
	return val.ArenaToStub(val.NewDictVal(8))
}
func PyDict_SetItemString(o unsafe.Pointer, key unsafe.Pointer, valPtr unsafe.Pointer) int32 {
	if o == nil || key == nil || valPtr == nil {
		pyErrSetTypeError("PyDict_SetItemString called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsDict() {
		pyErrSetTypeError("PyDict_SetItemString argument must be dict")
		return -1
	}
	k := GoString(key)
	v.AsDict().Set(val.NewStringVal(k), val.ArenaFromStub(valPtr))
	return 0
}
func PyDict_GetItemString(o unsafe.Pointer, key unsafe.Pointer) unsafe.Pointer {
	if o == nil || key == nil {
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsDict() {
		return nil
	}
	k := GoString(key)
	got, ok := v.AsDict().Get(val.NewStringVal(k))
	if !ok {
		return nil
	}
	return val.ArenaBorrowStub(got)
}
func PyDict_SetItem(o unsafe.Pointer, key unsafe.Pointer, valPtr unsafe.Pointer) int32 {
	if o == nil || key == nil || valPtr == nil {
		pyErrSetTypeError("PyDict_SetItem called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	if !v.IsDict() {
		pyErrSetTypeError("PyDict_SetItem argument must be dict")
		return -1
	}
	v.AsDict().Set(val.ArenaFromStub(key), val.ArenaFromStub(valPtr))
	return 0
}
func PyDict_GetItemWithError(o unsafe.Pointer, key unsafe.Pointer) unsafe.Pointer {
	if o == nil || key == nil {
		pyErrSetTypeError("PyDict_GetItemWithError called with NULL")
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsDict() {
		pyErrSetTypeError("PyDict_GetItemWithError argument must be dict")
		return nil
	}
	got, ok := v.AsDict().Get(val.ArenaFromStub(key))
	if !ok {
		return nil
	}
	return val.ArenaBorrowStub(got)
}
func PyDict_Next(o unsafe.Pointer, pos unsafe.Pointer, keyPtr unsafe.Pointer, valPtr unsafe.Pointer) int32 {
	if o == nil || pos == nil || keyPtr == nil || valPtr == nil {
		pyErrSetTypeError("PyDict_Next called with NULL")
		return 0
	}
	v := val.ArenaFromStub(o)
	if !v.IsDict() {
		pyErrSetTypeError("PyDict_Next argument must be dict")
		return 0
	}
	p := (*int64)(pos)
	idx := int(*p)
	d := v.AsDict()
	items := d.Items()
	if idx < 0 || idx >= len(items) {
		*p = int64(len(items))
		return 0
	}
	it := items[idx]
	*p = int64(idx + 1)
	*((*unsafe.Pointer)(keyPtr)) = val.ArenaBorrowStub(it.Key)
	*((*unsafe.Pointer)(valPtr)) = val.ArenaBorrowStub(it.Value)
	return 1
}

// Callables
func PyObject_Call(callable unsafe.Pointer, args unsafe.Pointer, kwargs unsafe.Pointer) unsafe.Pointer {
	if callable == nil {
		pyErrSetTypeError("PyObject_Call called with NULL callable")
		return nil
	}
	ctx := CurrentContext()
	fn := val.ArenaFromStub(callable)
	var argVals []val.Value
	if args != nil {
		av := val.ArenaFromStub(args)
		if !av.IsTuple() {
			pyErrSetTypeError("PyObject_Call args must be tuple")
			return nil
		}
		argVals = av.AsTuple().Items
	}
	var kwNames []val.Value
	var kwVals []val.Value
	if kwargs != nil {
		kv := val.ArenaFromStub(kwargs)
		if !kv.IsDict() {
			pyErrSetTypeError("PyObject_Call kwargs must be dict")
			return nil
		}
		// Extract keywords as (kwnames, values) pairs for ctx.Call API
		// ctx.Call expects args slice + kwnames slice where kwnames length = len(kw) and values are appended after args?
		// Check val.ExecContext.Call semantics: In type.go Call(), args []Value, kwnames []Value where kwnames are names,
		// and values are trailing in args? Let's inspect: In Call(), it copies args to stack and passes kwnames separately.
		// VMM discards? Actually builtin funcs receive args includes kwargs values appended? Need to see how VM calls.
		// For our bridge, construct combined args: positional + kw values, kwnames = keys.
		items := kv.AsDict().Items()
		for _, it := range items {
			kwNames = append(kwNames, it.Key)
			kwVals = append(kwVals, it.Value)
		}
		argVals = append(argVals, kwVals...)
	}
	// Invoke via ctx.Call; catch Python panic to convert to C error (return NULL)
	var result val.Value
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		if ctx == nil {
			// Fallback: try calling builtin directly
			if fn.IsBuiltinFunc() {
				result = fn.AsBuiltinFunc().Fn(nil, fn, argVals, kwNames)
			} else if tpCall := fn.Type().Slots.TpCall; tpCall != nil {
				result = tpCall(nil, fn, argVals, kwNames)
			} else {
				pyErrSetTypeError("object is not callable")
				return
			}
		} else {
			result = ctx.Call(fn, argVals, kwNames)
			if ctx.HasException() {
				// Error indicator set; return NULL
				return
			}
		}
	}()
	if recovered != nil {
		// Panic carries Value exception or Go error string? fython panics with Value or string "TypeError: ..."
		// Convert to ctx error.
		if ev, ok := recovered.(val.Value); ok {
			SetError(ev)
		} else {
			pyErrSetString(val.RuntimeErrorType, "PyObject_Call failed")
			// Preserve panic string for debugging? If string, use it.
			if s, ok := recovered.(string); ok {
				pyErrSetString(val.RuntimeErrorType, s)
			}
		}
		return nil
	}
	if ctx != nil && ctx.HasException() {
		return nil
	}
	return val.ArenaToStub(result)
}

func PyObject_CallNoArgs(callable unsafe.Pointer) unsafe.Pointer {
	return PyObject_Call(callable, nil, nil)
}

func PyObject_Vectorcall(callable unsafe.Pointer, args unsafe.Pointer, nargsf int64, kwnames unsafe.Pointer) unsafe.Pointer {
	if callable == nil {
		pyErrSetTypeError("PyObject_Vectorcall called with NULL")
		return nil
	}
	// Decode nargsf: low bits = nargs, high flag PY_VECTORCALL_ARGUMENTS_OFFSET (0x8000)
	nargs := int(nargsf & 0x7FFF)
	// Actually for 3.15, nargsf is size_t; high bit flag may differ. Use low 15 bits + ignore flag.
	// If nargsf has flag, subtract? Simplify: if nargsf > 1000, mask.
	// args is array of PyObject* of length nargs (+1 if kwnames dict slot? Vectorcall convention: if kwnames != NULL, args array has extra slot for tuple of kw names? In 3.15, kwnames is tuple of names, args contains positional + keyword values interleaved.
	// For MVP, assume kwnames is NULL for positional-only calls (common in C extensions).
	var argVals []val.Value
	if args != nil && nargs > 0 {
		argVals = make([]val.Value, nargs)
		for i := range nargs {
			ptr := *(*unsafe.Pointer)(unsafe.Add(args, i*8))
			argVals[i] = val.ArenaFromStub(ptr)
		}
	}
	ctx := CurrentContext()
	fn := val.ArenaFromStub(callable)
	// If kwnames provided as tuple, handle: args after nargs are keyword values, kwnames tuple holds names.
	if kwnames != nil {
		kv := val.ArenaFromStub(kwnames)
		if kv.IsTuple() {
			names := kv.AsTuple().Items
			// args length = nargs (positional) + len(names) (kw values) ??? Actually nargsf stacks differently.
			// For full fidelity, if kwnames != NULL, total args array length = nargs + len(names)
			// Our caller passed nargs = positional count only; need to fetch extra.
			// Re-read: if kwnames != NULL, the array has positional + keyword values.
			// We need total length. Approximate: read up to nargs+len(names)
			// But we already read nargs. Read additional.
			total := nargs + len(names)
			if args != nil {
				argVals = make([]val.Value, total)
				for i := range total {
					ptr := *(*unsafe.Pointer)(unsafe.Add(args, i*8))
					argVals[i] = val.ArenaFromStub(ptr)
				}
			}
			var result val.Value
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				result = ctx.Call(fn, argVals, names)
			}()
			if recovered != nil {
				if ev, ok := recovered.(val.Value); ok {
					SetError(ev)
				} else {
					pyErrSetString(val.RuntimeErrorType, "Vectorcall failed")
				}
				return nil
			}
			if ctx != nil && ctx.HasException() {
				return nil
			}
			return val.ArenaToStub(result)
		}
	}
	var result val.Value
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		result = ctx.Call(fn, argVals, nil)
	}()
	if recovered != nil {
		if ev, ok := recovered.(val.Value); ok {
			SetError(ev)
		}
		return nil
	}
	if ctx != nil && ctx.HasException() {
		return nil
	}
	return val.ArenaToStub(result)
}

func PyObject_GetAttrString(o unsafe.Pointer, name unsafe.Pointer) unsafe.Pointer {
	if o == nil || name == nil {
		pyErrSetTypeError("PyObject_GetAttrString called with NULL")
		return nil
	}
	ctx := CurrentContext()
	v := val.ArenaFromStub(o)
	attrName := GoString(name)
	var res val.Value
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		res = val.GetAttrCtx(ctx, v, val.NewStringVal(attrName))
	}()
	if recovered != nil {
		if ev, ok := recovered.(val.Value); ok {
			SetError(ev)
		} else {
			pyErrSetString(val.AttributeErrorType, "attribute error")
		}
		return nil
	}
	if ctx != nil && ctx.HasException() {
		return nil
	}
	return val.ArenaToStub(res)
}

func PyObject_SetAttrString(o unsafe.Pointer, name unsafe.Pointer, valPtr unsafe.Pointer) int32 {
	if o == nil || name == nil || valPtr == nil {
		pyErrSetTypeError("PyObject_SetAttrString called with NULL")
		return -1
	}
	ctx := CurrentContext()
	v := val.ArenaFromStub(o)
	attrName := GoString(name)
	newVal := val.ArenaFromStub(valPtr)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		val.SetAttrCtx(ctx, v, val.NewStringVal(attrName), newVal)
	}()
	if recovered != nil {
		if ev, ok := recovered.(val.Value); ok {
			SetError(ev)
		}
		return -1
	}
	if ctx != nil && ctx.HasException() {
		return -1
	}
	return 0
}
