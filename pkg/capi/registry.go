package capi

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

// HandlerBind associates shim dispatch slots with Go callbacks.
// Shim defines int fython_abi3_bind(void** table, int n); Go fills table with callback pointers.
// Table order is defined in shim/fython_abi3_shim.h (FythonSlot enum). Keep in sync.
type BindFunc func(table unsafe.Pointer, n int32) int32

var bound bool

// BuildDispatchTable returns Go slice of callback uintptr values in shim slot order.
// This is used by pkg/vm loader after dlopen(shim) to fill shim table.
func BuildDispatchTable() []uintptr {
	cb := dynload.NewCallback
	// Order must match shim header exactly. Count = FYTHON_SLOT_COUNT (see shim).
	// Slots:
	// 0 PyLong_FromLong
	// 1 PyLong_FromLongLong
	// 2 PyLong_FromUnsignedLongLong
	// 3 PyLong_FromSsize_t
	// 4 PyLong_AsLong
	// 5 PyLong_AsLongLong
	// 6 PyLong_AsSsize_t
	// 7 PyBool_FromLong
	// 8 PyFloat_FromDouble
	// 9 PyFloat_AsDouble
	// 10 PyComplex_FromDoubles
	// 11 PyUnicode_FromString
	// 12 PyUnicode_FromStringAndSize
	// 13 PyUnicode_AsUTF8
	// 14 PyUnicode_AsUTF8AndSize
	// 15 PyBytes_FromStringAndSize
	// 16 PyBytes_AsString
	// 17 PyBytes_Size
	// 18 PyTuple_New
	// 19 PyTuple_Pack (via shim array)
	// 20 PyTuple_GetItem
	// 21 PyTuple_SetItem
	// 22 PyTuple_Size
	// 23 PyList_New
	// 24 PyList_Append
	// 25 PyList_GetItem
	// 26 PyList_SetItem
	// 27 PyList_Size
	// 28 PyDict_New
	// 29 PyDict_SetItemString
	// 30 PyDict_GetItemString
	// 31 PyDict_SetItem
	// 32 PyDict_GetItemWithError
	// 33 PyDict_Next
	// 34 PyObject_Call
	// 35 PyObject_CallNoArgs
	// 36 PyObject_Vectorcall
	// 37 PyObject_GetAttrString
	// 38 PyObject_SetAttrString
	// 39 PyArg_ParseTuple
	// 40 PyArg_ParseTupleAndKeywords
	// 41 PyArg_UnpackTuple
	// 42 Py_BuildValue
	// 43 PyModule_Create2
	// 44 PyModuleDef_Init
	// 45 PyModule_AddObject
	// 46 PyModule_AddObjectRef
	// 47 PyModule_AddIntConstant
	// 48 PyModule_AddStringConstant
	// 49 PyModule_GetState
	// 50 PyCapsule_New
	// 51 PyCapsule_GetPointer
	// 52 PyCapsule_Import
	// 53 PyErr_SetString
	// 54 PyErr_SetObject
	// 55 PyErr_Occurred
	// 56 PyErr_Clear
	// 57 PyErr_FormatFormatted (internal, called by shim PyErr_Format)
	// 58 PyGILState_Ensure
	// 59 PyGILState_Release
	// 60 PyEval_SaveThread
	// 61 PyEval_RestoreThread
	// 62 _Py_Dealloc
	// 63 PyType_IsSubtype
	// 64 PyType_Ready
	// 65 PyObject_Type (PyObject_Type internal)
	// 66 PyCFunction_NewEx
	// 67 PyErr_NormalizeException
	// 68 PyErr_GetCause
	// 69 PyErr_GetContext
	// 70 PyType_FromSpec
	// 71 PyType_FromSpecWithBases
	// 72 PyType_FromModuleAndSpec
	// 73 PyObject_GetBuffer
	// 74 PyBuffer_Release
	// 75 PyObject_CheckBuffer
	// 76 PyBuffer_SizeFromFormat
	// 77 PyBuffer_FillContiguousStrides
	// 78 PyBuffer_FillInfo
	// 79 PyBuffer_IsContiguous
	// To keep table in sync, shim header defines FYTHON_SLOT_COUNT=80

	return []uintptr{
		cb(PyLong_FromLong),
		cb(PyLong_FromLongLong),
		cb(PyLong_FromUnsignedLongLong),
		cb(PyLong_FromSsize_t),
		cb(PyLong_AsLong),
		cb(PyLong_AsLongLong),
		cb(PyLong_AsSsize_t),
		cb(PyBool_FromLong),
		cb(PyFloat_FromDouble),
		cb(PyFloat_AsDoublePtr),
		cb(PyComplex_FromDoubles),
		cb(PyUnicode_FromString),
		cb(PyUnicode_FromStringAndSize),
		cb(PyUnicode_AsUTF8),
		cb(PyUnicode_AsUTF8AndSize),
		cb(PyBytes_FromStringAndSize),
		cb(PyBytes_AsString),
		cb(PyBytes_Size),
		cb(PyTuple_New),
		cb(PyTuple_Pack),
		cb(PyTuple_GetItem),
		cb(PyTuple_SetItem),
		cb(PyTuple_Size),
		cb(PyList_New),
		cb(PyList_Append),
		cb(PyList_GetItem),
		cb(PyList_SetItem),
		cb(PyList_Size),
		cb(PyDict_New),
		cb(PyDict_SetItemString),
		cb(PyDict_GetItemString),
		cb(PyDict_SetItem),
		cb(PyDict_GetItemWithError),
		cb(PyDict_Next),
		cb(PyObject_Call),
		cb(PyObject_CallNoArgs),
		cb(PyObject_Vectorcall),
		cb(PyObject_GetAttrString),
		cb(PyObject_SetAttrString),
		cb(PyArg_ParseTuple),
		cb(PyArg_ParseTupleAndKeywords),
		cb(PyArg_UnpackTuple),
		cb(Py_BuildValue),
		cb(PyModule_Create2),
		cb(PyModuleDef_Init),
		cb(PyModule_AddObject),
		cb(PyModule_AddObjectRef),
		cb(PyModule_AddIntConstant),
		cb(PyModule_AddStringConstant),
		cb(PyModule_GetState),
		cb(PyCapsule_New),
		cb(PyCapsule_GetPointer),
		cb(PyCapsule_Import),
		cb(PyErr_SetString),
		cb(PyErr_SetObject),
		cb(PyErr_Occurred),
		cb(PyErr_Clear),
		cb(PyErr_FormatFormatted),
		cb(PyGILState_Ensure),
		cb(PyGILState_Release),
		cb(PyEval_SaveThread),
		cb(PyEval_RestoreThread),
		cb(_Py_Dealloc),
		cb(PyType_IsSubtype),
		cb(PyType_Ready),
		cb(PyObject_Type),
		cb(PyCFunction_NewEx),
		cb(PyErr_NormalizeException),
		cb(PyErr_GetCause),
		cb(PyErr_GetContext),
		cb(PyType_FromSpec),
		cb(PyType_FromSpecWithBases),
		cb(PyType_FromModuleAndSpec),
		cb(PyObject_GetBuffer),
		cb(PyBuffer_Release),
		cb(PyObject_CheckBuffer),
		cb(PyBuffer_SizeFromFormat),
		cb(PyBuffer_FillContiguousStrides),
		cb(PyBuffer_FillInfo),
		cb(PyBuffer_IsContiguous),
	}
}

// BindToShim fills shim's table by calling fython_abi3_bind(table, n).
func BindToShim(shimHandle dynload.Handle) error {
	if bound {
		return nil
	}
	EnableGIL()
	if shimHandle == 0 {
		return nil
	}
	sym, err := dynload.Sym(shimHandle, "fython_abi3_bind")
	if err != nil {
		return err
	}
	table := BuildDispatchTable()
	if len(table) == 0 {
		return nil
	}
	var bindFn func(table unsafe.Pointer, n int32) int32
	dynload.RegisterFunc(&bindFn, sym)
	_ = bindFn(unsafe.Pointer(&table[0]), int32(len(table)))

	// Initialize shim's PyExc_* data variables to shadow exception types.
	excMap := map[string]*val.PyType{
		"PyExc_BaseException":       val.BaseExceptionType,
		"PyExc_Exception":           val.ExceptionType,
		"PyExc_ArithmeticError":     val.ArithmeticErrorType,
		"PyExc_AssertionError":      val.AssertionErrorType,
		"PyExc_AttributeError":      val.AttributeErrorType,
		"PyExc_BufferError":         val.BufferErrorType,
		"PyExc_ImportError":         val.ImportErrorType,
		"PyExc_IndexError":          val.IndexErrorType,
		"PyExc_KeyError":            val.KeyErrorType,
		"PyExc_LookupError":         val.LookupErrorType,
		"PyExc_MemoryError":         val.MemoryErrorType,
		"PyExc_NameError":           val.NameErrorType,
		"PyExc_OverflowError":       val.OverflowErrorType,
		"PyExc_RuntimeError":        val.RuntimeErrorType,
		"PyExc_SyntaxError":         val.SyntaxErrorType,
		"PyExc_SystemError":         val.SystemErrorType,
		"PyExc_TypeError":           val.TypeErrorType,
		"PyExc_ValueError":          val.ValueErrorType,
		"PyExc_ZeroDivisionError":   val.ZeroDivisionErrorType,
		"PyExc_OSError":             val.OSErrorType,
		"PyExc_StopIteration":       val.StopIterationType,
		"PyExc_KeyboardInterrupt":   val.KeyboardInterruptType,
		"PyExc_SystemExit":          val.SystemExitType,
		"PyExc_NotImplementedError": val.NotImplementedErrorType,
		"PyExc_RecursionError":      val.RecursionErrorType,
		"PyExc_ReferenceError":      val.ReferenceErrorType,
		"PyExc_UnboundLocalError":   val.UnboundLocalErrorType,
		"PyExc_ModuleNotFoundError": val.ModuleNotFoundErrorType,
	}
	for name, t := range excMap {
		if t == nil {
			continue
		}
		addr, err := dynload.Sym(shimHandle, name)
		if err != nil || addr == 0 {
			continue
		}
		shadow := val.ArenaShadowType(t)
		// addr is a C data symbol address (uintptr from dlsym). Convert to unsafe.Pointer via memory reinterpret.
		ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
		*(*unsafe.Pointer)(ptr) = shadow
	}

	// Alias builtin type objects: register shim data symbols as shadow types so TypeCheck via IsSubtype works for direct &PyModule_Type references.
	// For each shim type data symbol, map it to Go type.
	typeAlias := map[string]*val.PyType{
		"PyModule_Type":    val.ModuleType,
		"PyType_Type":      val.TypeType,
		"PyLong_Type":      val.IntType,
		"PyFloat_Type":     val.FloatType,
		"PyUnicode_Type":   val.StrType,
		"PyBytes_Type":     val.BytesType,
		"PyTuple_Type":     val.TupleType,
		"PyList_Type":      val.ListType,
		"PyDict_Type":      val.DictType,
		"PyCFunction_Type": val.BuiltinFuncType,
		"PyComplex_Type":   val.ComplexType,
		"PyBool_Type":      val.BoolType,
	}
	for name, t := range typeAlias {
		if t == nil {
			continue
		}
		addr, err := dynload.Sym(shimHandle, name)
		if err != nil || addr == 0 {
			continue
		}
		// Register shim address as this type's shadow (keep arena map in sync).
		// Get arena and inject.
		arena := val.GetDefaultArena()
		ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
		val.AliasShadowType(arena, t, ptr)
		// Initialize tp_name / basicsize in shim block (mirrors arena init).
		initShimTypeBlock(t, ptr)
	}

	// Alias singleton objects (_Py_NoneStruct etc.) to immortal stubs so C code can
	// return/compare Py_None, Py_True, Py_False, Py_NotImplemented, Py_Ellipsis.
	{
		arena := val.GetDefaultArena()
		singletons := map[string]val.Value{
			"_Py_NoneStruct":           val.NoneVal(),
			"_Py_TrueStruct":           val.BoolVal(true),
			"_Py_FalseStruct":          val.BoolVal(false),
			"_Py_NotImplementedStruct": val.NotImplementedVal(),
			"_Py_EllipsisStruct":       val.EllipsisVal(),
		}
		for name, v := range singletons {
			addr, err := dynload.Sym(shimHandle, name)
			if err != nil || addr == 0 {
				continue
			}
			ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
			val.AliasShadowStub(arena, v, ptr)
		}
	}
	// Pre-create shadow types for all builtin types to populate typeMap.
	for _, t := range allBuiltinTypes() {
		_ = val.ArenaShadowType(t)
	}
	bound = true
	return nil
}

func initShimTypeBlock(t *val.PyType, blk unsafe.Pointer) {
	// Same layout as val shadow: refcnt @0, type @8, size @16, tp_name @24
	*(*int64)(blk) = val.ImmortalRefcnt
	// tp_flags @168 (unsigned long) for PyType_FastSubclass checks (PyLong_Check etc.).
	var cflags uint64
	switch t {
	case val.IntType, val.BoolType:
		cflags |= 1 << 24 // Py_TPFLAGS_LONG_SUBCLASS
	case val.ListType:
		cflags |= 1 << 25
	case val.TupleType:
		cflags |= 1 << 26
	case val.BytesType, val.ByteArrayType:
		cflags |= 1 << 27
	case val.StrType:
		cflags |= 1 << 28
	case val.DictType:
		cflags |= 1 << 29
	case val.TypeType:
		cflags |= 1 << 31
	}
	// Py_TPFLAGS_BASETYPE (1<<10) to indicate subclassing allowed for heap types.
	cflags |= 1 << 10
	*(*uint64)(unsafe.Add(blk, 168)) = cflags
	// ob_type for TypeType points to self, others to TypeType's shadow
	if t == val.TypeType {
		*(*unsafe.Pointer)(unsafe.Add(blk, 8)) = blk
	} else {
		// Use shim PyType_Type address if available? For now leave as arena shadow of TypeType.
		if mt := val.ArenaShadowType(val.TypeType); mt != nil {
			*(*unsafe.Pointer)(unsafe.Add(blk, 8)) = mt
		}
	}
	// tp_name: need stable C string. Allocate Go bytes and keep alive via global pool.
	nameBytes := append([]byte(t.Name), 0)
	// Keep alive forever
	shimTypeNamePool = append(shimTypeNamePool, nameBytes)
	*(*unsafe.Pointer)(unsafe.Add(blk, 24)) = unsafe.Pointer(&nameBytes[0])
	bs := int64(32)
	switch t.Name {
	case "str", "bytes", "bytearray":
		bs = 64
	case "list", "dict", "tuple", "set", "module":
		bs = 64
	}
	*(*int64)(unsafe.Add(blk, 32)) = bs
}

var shimTypeNamePool [][]byte

func allBuiltinTypes() []*val.PyType {
	// Collect via builtinTypes? Not exported. Enumerate known exported vars.
	return []*val.PyType{
		val.ObjectType, val.TypeType, val.NoneType, val.BoolType, val.IntType, val.FloatType,
		val.StrType, val.ListType, val.DictType, val.TupleType, val.ModuleType, val.BytesType,
		val.ByteArrayType, val.ComplexType,
	}
}
