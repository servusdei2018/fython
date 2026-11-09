package capi

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

func PyUnicode_FromString(p unsafe.Pointer) unsafe.Pointer {
	if p == nil {
		pyErrSetTypeError("PyUnicode_FromString called with NULL")
		return nil
	}
	s := GoString(p)
	return val.ArenaToStub(val.NewStringVal(s))
}

func PyUnicode_FromStringAndSize(p unsafe.Pointer, size int64) unsafe.Pointer {
	if p == nil {
		if size < 0 {
			pyErrSetTypeError("PyUnicode_FromStringAndSize called with NULL and negative size")
			return nil
		}
		// CPython: NULL with size allocates empty? Actually NULL means "create from no data" -> empty string
		// For bytes, NULL+size allocates size zeros. For unicode, treat as empty.
		return val.ArenaToStub(val.NewStringVal(""))
	}
	if size < 0 {
		s := GoString(p)
		return val.ArenaToStub(val.NewStringVal(s))
	}
	b := GoBytes(p, size)
	return val.ArenaToStub(val.NewStringVal(string(b)))
}

func PyUnicode_AsUTF8(o unsafe.Pointer) unsafe.Pointer {
	if o == nil {
		pyErrSetTypeError("PyUnicode_AsUTF8 called with NULL")
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsAnyString() {
		pyErrSetTypeError("PyUnicode_AsUTF8 argument must be str")
		return nil
	}
	return val.ArenaStubUTF8(o)
}

func PyUnicode_AsUTF8AndSize(o unsafe.Pointer, sizePtr unsafe.Pointer) unsafe.Pointer {
	if o == nil {
		pyErrSetTypeError("PyUnicode_AsUTF8AndSize called with NULL")
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsAnyString() {
		pyErrSetTypeError("PyUnicode_AsUTF8AndSize argument must be str")
		return nil
	}
	if sizePtr != nil {
		*((*int64)(sizePtr)) = int64(v.StringByteLen())
	}
	return val.ArenaStubUTF8(o)
}

func PyBytes_FromStringAndSize(p unsafe.Pointer, size int64) unsafe.Pointer {
	if p == nil {
		if size < 0 {
			pyErrSetTypeError("PyBytes_FromStringAndSize called with NULL and negative size")
			return nil
		}
		b := make([]byte, size)
		return val.ArenaToStub(val.NewBytesVal(b))
	}
	if size < 0 {
		// size -1? CPython requires size >=0? Treat -1 as strlen
		s := GoString(p)
		return val.ArenaToStub(val.NewBytesVal([]byte(s)))
	}
	b := GoBytes(p, size)
	// GoBytes returns copy; ensure non-nil for size 0
	if b == nil {
		b = []byte{}
	}
	return val.ArenaToStub(val.NewBytesVal(b))
}

func PyBytes_AsString(o unsafe.Pointer) unsafe.Pointer {
	if o == nil {
		pyErrSetTypeError("PyBytes_AsString called with NULL")
		return nil
	}
	v := val.ArenaFromStub(o)
	if !v.IsBytes() && !v.IsByteArray() && !v.IsMemoryView() {
		pyErrSetTypeError("PyBytes_AsString argument must be bytes-like")
		return nil
	}
	return val.ArenaStubUTF8(o)
}

func PyBytes_Size(o unsafe.Pointer) int64 {
	if o == nil {
		pyErrSetTypeError("PyBytes_Size called with NULL")
		return -1
	}
	v := val.ArenaFromStub(o)
	switch {
	case v.IsBytes():
		return int64(len(v.AsBytes().Data))
	case v.IsByteArray():
		return int64(len(v.AsByteArray().Data))
	case v.IsMemoryView():
		return int64(len(v.AsMemoryView().Buf))
	default:
		pyErrSetTypeError("PyBytes_Size argument must be bytes-like")
		return -1
	}
}
