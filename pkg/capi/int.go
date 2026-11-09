package capi

import (
	"math"
	"math/big"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// PyLong_FromLong etc. — handlers are C-callable via purego callbacks.
// Signatures use int64 for C long (64-bit on linux/darwin).
func PyLong_FromLong(v int64) unsafe.Pointer {
	if v >= math.MinInt32 && v <= math.MaxInt32 {
		// still use Int64Val (fython unifies) — value identity preserved via map key.
		return val.ArenaToStub(val.Int64Val(v))
	}
	// CPython would create value; for small ints also Int64Val.
	return val.ArenaToStub(val.Int64Val(v))
}
func PyLong_FromLongLong(v int64) unsafe.Pointer {
	return val.ArenaToStub(val.Int64Val(v))
}
func PyLong_FromUnsignedLongLong(v uint64) unsafe.Pointer {
	if v <= uint64(math.MaxInt64) {
		return val.ArenaToStub(val.Int64Val(int64(v)))
	}
	bi := new(big.Int).SetUint64(v)
	return val.ArenaToStub(val.NewBigIntVal(bi))
}
func PyLong_FromSsize_t(v int64) unsafe.Pointer {
	return val.ArenaToStub(val.Int64Val(v))
}

func toInt64Checked(v val.Value) (int64, bool, string) {
	if v.IsInt64() || v.IsBool() {
		return v.AsInt64(), true, ""
	}
	if v.Kind() == val.KindBigInt {
		bi := &v.AsBigInt().Val
		if !bi.IsInt64() {
			return 0, false, "OverflowError"
		}
		return bi.Int64(), true, ""
	}
	// Accept float/int subclasses via __index__? For C API, PyLong_AsLong allows __index__.
	// Try invoking __index__ if available via ctx.
	ctx := CurrentContext()
	if ctx != nil {
		// Look up __index__ on type and call it
		if t := v.Type(); t != nil {
			if fn, _, ok := t.LookupMRO(val.InternString("__index__")); ok {
				func() {
					defer func() { _ = recover() }()
					_ = ctx.Invoke1(fn, v)
				}()
			}
		}
		// Re-check after __index__ attempt (simple path: only Int64/Bool/BigInt supported)
		// If object has __int__? Fall through to failure.
	}
	return 0, false, "TypeError"
}

func PyLong_AsLong(o unsafe.Pointer) int64 {
	if o == nil {
		pyErrSetTypeError("PyLong_AsLong called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	i, ok, kind := toInt64Checked(v)
	if !ok {
		if kind == "OverflowError" {
			pyErrSetOverflow("Python int too large to convert to C long")
		} else {
			pyErrSetTypeError("'int' object expected")
		}
		return -1
	}
	return i
}
func PyLong_AsLongLong(o unsafe.Pointer) int64 { return PyLong_AsLong(o) }
func PyLong_AsSsize_t(o unsafe.Pointer) int64  { return PyLong_AsLong(o) }

func PyBool_FromLong(v int64) unsafe.Pointer {
	if v != 0 {
		return val.ArenaBorrowStub(val.BoolVal(true))
	}
	return val.ArenaBorrowStub(val.BoolVal(false))
}

// Float / Complex
func PyFloat_FromDouble(v float64) unsafe.Pointer {
	return val.ArenaToStub(val.Float64Val(v))
}

// PyFloat_AsDouble — out-pointer form for purego (float returns unsupported in callbacks).
// Shim's PyFloat_AsDouble allocates storage and calls this to obtain the double.
func PyFloat_AsDoublePtr(o unsafe.Pointer, out unsafe.Pointer) {
	if out == nil {
		return
	}
	*((*float64)(out)) = -1.0
	if o == nil {
		pyErrSetTypeError("PyFloat_AsDouble called with NULL")
		return
	}
	v := val.ArenaFromStub(o)
	if v.IsFloat64() {
		*((*float64)(out)) = v.AsFloat64()
		return
	}
	if v.IsInt64() || v.IsBool() {
		*((*float64)(out)) = float64(v.AsInt64())
		return
	}
	if v.Kind() == val.KindBigInt {
		f, _ := v.AsBigInt().Val.Float64()
		*((*float64)(out)) = f
		return
	}
	pyErrSetTypeError("float expected")
}

// Backwards-compat wrapper kept for direct Go callers (not used as callback).
func PyFloat_AsDouble(o unsafe.Pointer) float64 {
	var out float64
	PyFloat_AsDoublePtr(o, unsafe.Pointer(&out))
	return out
}
func PyComplex_FromDoubles(re, im float64) unsafe.Pointer {
	return val.ArenaToStub(val.NewComplexVal(re, im))
}

// Aliases for registry convenience.
var (
	_ = PyLong_FromLong
)
