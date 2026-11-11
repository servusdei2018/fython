package vm

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

// arrayType is the native type of the array.array class.
var arrayType *val.PyType

// pyArray is the native payload of an array.array object.
type pyArray struct {
	inst     val.PyInstance // must be first: Header lives at offset 0
	typecode byte
	data     []byte
	mu       sync.RWMutex
}

// arrayItemSize returns the size in bytes of a type code, and whether the
// type code is known at all.
func arrayItemSize(code byte) (int, bool) {
	switch code {
	case 'b', 'B':
		return 1, true
	case 'h', 'H':
		return 2, true
	case 'i', 'I', 'l', 'L':
		return 4, true
	case 'q', 'Q':
		return 8, true
	case 'f':
		return 4, true
	case 'd':
		return 8, true
	case 'u':
		return 4, true
	case 'w':
		return 4, true
	}
	return 0, false
}

// arrayFormat returns the PEP 3118 format code of a type code.
func arrayFormat(code byte) string {
	switch code {
	case 'b':
		return "b"
	case 'B':
		return "B"
	case 'h':
		return "h"
	case 'H':
		return "H"
	case 'i', 'l':
		return "i"
	case 'I', 'L':
		return "I"
	case 'q':
		return "q"
	case 'Q':
		return "Q"
	case 'f':
		return "f"
	case 'd':
		return "d"
	case 'u', 'w':
		return "I"
	}
	return "B"
}

// arraySetElement writes a value into the backing store of the array.
func arraySetElement(data []byte, offset int, code byte, v val.Value) {
	switch code {
	case 'b':
		data[offset] = byte(int8(v.AsInt64()))
	case 'B':
		data[offset] = byte(v.AsInt64())
	case 'h':
		binary.LittleEndian.PutUint16(data[offset:], uint16(int16(v.AsInt64())))
	case 'H':
		binary.LittleEndian.PutUint16(data[offset:], uint16(v.AsInt64()))
	case 'i', 'l':
		binary.LittleEndian.PutUint32(data[offset:], uint32(int32(v.AsInt64())))
	case 'I', 'L':
		binary.LittleEndian.PutUint32(data[offset:], uint32(v.AsInt64()))
	case 'q':
		binary.LittleEndian.PutUint64(data[offset:], uint64(v.AsInt64()))
	case 'Q':
		binary.LittleEndian.PutUint64(data[offset:], uint64(v.AsInt64()))
	case 'f':
		binary.LittleEndian.PutUint32(data[offset:], math.Float32bits(float32(toFloat(v))))
	case 'd':
		binary.LittleEndian.PutUint64(data[offset:], math.Float64bits(toFloat(v)))
	case 'u', 'w':
		binary.LittleEndian.PutUint32(data[offset:], uint32(v.AsInt64()))
	}
}

// arrayGetElement reads a value from the backing store of the array.
func arrayGetElement(data []byte, offset int, code byte) val.Value {
	switch code {
	case 'b':
		return val.Int64Val(int64(int8(data[offset])))
	case 'B':
		return val.Int64Val(int64(data[offset]))
	case 'h':
		return val.Int64Val(int64(int16(binary.LittleEndian.Uint16(data[offset:]))))
	case 'H':
		return val.Int64Val(int64(binary.LittleEndian.Uint16(data[offset:])))
	case 'i', 'l':
		return val.Int64Val(int64(int32(binary.LittleEndian.Uint32(data[offset:]))))
	case 'I', 'L':
		return val.Int64Val(int64(binary.LittleEndian.Uint32(data[offset:])))
	case 'q':
		return val.Int64Val(int64(binary.LittleEndian.Uint64(data[offset:])))
	case 'Q':
		return val.Int64Val(int64(binary.LittleEndian.Uint64(data[offset:])))
	case 'f':
		return val.Float64Val(float64(math.Float32frombits(binary.LittleEndian.Uint32(data[offset:]))))
	case 'd':
		return val.Float64Val(math.Float64frombits(binary.LittleEndian.Uint64(data[offset:])))
	case 'u', 'w':
		return val.Int64Val(int64(binary.LittleEndian.Uint32(data[offset:])))
	}
	return val.NoneVal()
}

// arrayCount returns the number of elements of the array.
func (a *pyArray) count() int {
	size, _ := arrayItemSize(a.typecode)
	if size == 0 {
		return 0
	}
	return len(a.data) / size
}

// initArrayType creates the native array type.
func initArrayType() {
	if arrayType != nil {
		return
	}
	arrayType = val.MustNewType("array", []*val.PyType{val.ObjectType}, nil)
	arrayType.Dict.Set(val.InternString("__module__"), val.InternString("array"))

	newArray := func(code byte, initial []val.Value) val.Value {
		size, _ := arrayItemSize(code)
		a := &pyArray{
			inst:     val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: arrayType}, Shape: arrayType.RootShape},
			typecode: code,
			data:     make([]byte, 0, size*len(initial)),
		}
		for _, v := range initial {
			a.data = a.data[:len(a.data)+size]
			arraySetElement(a.data, len(a.data)-size, code, v)
		}
		return val.ObjectVal(&a.inst.Header)
	}

	arrayType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: array() must have at least 1 argument")
		}
		if !args[0].IsAnyString() || len(args[0].AsString()) != 1 {
			panic("TypeError: array() argument 1 must be a unicode character, not str")
		}
		code := args[0].AsString()[0]
		if _, ok := arrayItemSize(code); !ok {
			panic("ValueError: bad typecode (must be b, B, u, h, H, i, I, l, L, q, Q, f, d or w)")
		}
		var initial []val.Value
		if len(args) > 1 {
			initial = pySeqItems(args[1])
			if initial == nil {
				it := iterFor(ctx, args[1])
				for {
					item, ok := it.Next()
					if !ok {
						break
					}
					initial = append(initial, item)
				}
			}
		}
		return newArray(code, initial)
	}

	arrayType.Slots.TpAsBuffer = func(ctx *val.ExecContext, obj val.Value, view *val.PyBuffer, flags int) error {
		a := (*pyArray)(val.AsPointer(obj))
		size, _ := arrayItemSize(a.typecode)
		view.Buf = a.data
		view.ReadOnly = false
		view.ItemSize = size
		view.Format = arrayFormat(a.typecode)
		view.NDim = 1
		view.Shape = []int{a.count()}
		view.Strides = []int{size}
		return nil
	}

	setMethod := func(name string, fn func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value) {
		arrayType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal("array."+name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			a := (*pyArray)(val.AsPointer(args[0]))
			a.mu.Lock()
			defer a.mu.Unlock()
			return fn(ctx, a, args)
		}))
	}

	setMethod("append", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: append() takes exactly one argument")
		}
		size, _ := arrayItemSize(a.typecode)
		off := len(a.data)
		a.data = append(a.data, make([]byte, size)...)
		arraySetElement(a.data, off, a.typecode, args[1])
		return val.NoneVal()
	})

	setMethod("extend", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: extend() takes exactly one argument")
		}
		size, _ := arrayItemSize(a.typecode)
		it := iterFor(ctx, args[1])
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			off := len(a.data)
			a.data = append(a.data, make([]byte, size)...)
			arraySetElement(a.data, off, a.typecode, item)
		}
		return val.NoneVal()
	})

	setMethod("frombytes", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		if len(args) < 2 || !args[1].IsBytes() {
			panic("TypeError: frombytes() argument must be bytes-like")
		}
		a.data = append(a.data, args[1].AsBytes().Data...)
		return val.NoneVal()
	})

	setMethod("fromlist", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: fromlist() takes exactly one argument")
		}
		size, _ := arrayItemSize(a.typecode)
		items := pySeqItems(args[1])
		if items == nil {
			panic("TypeError: fromlist() argument must be a list")
		}
		for _, item := range items {
			off := len(a.data)
			a.data = append(a.data, make([]byte, size)...)
			arraySetElement(a.data, off, a.typecode, item)
		}
		return val.NoneVal()
	})

	setMethod("tobytes", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		return val.NewBytesVal(a.data)
	})

	setMethod("tolist", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		size, _ := arrayItemSize(a.typecode)
		items := make([]val.Value, 0, len(a.data)/size)
		for off := 0; off+size <= len(a.data); off += size {
			items = append(items, arrayGetElement(a.data, off, a.typecode))
		}
		return val.NewListVal(items...)
	})

	setMethod("buffer_info", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		size, _ := arrayItemSize(a.typecode)
		return val.NewTupleVal(val.Int64Val(int64(len(a.data))), val.Int64Val(int64(size)))
	})

	setMethod("count", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: count() takes exactly one argument")
		}
		size, _ := arrayItemSize(a.typecode)
		n := 0
		for off := 0; off+size <= len(a.data); off += size {
			if arrayGetElement(a.data, off, a.typecode) == args[1] {
				n++
			}
		}
		return val.Int64Val(int64(n))
	})

	setMethod("index", func(ctx *val.ExecContext, a *pyArray, args []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: index() takes at least one argument")
		}
		size, _ := arrayItemSize(a.typecode)
		for off := 0; off+size <= len(a.data); off += size {
			if arrayGetElement(a.data, off, a.typecode) == args[1] {
				return val.Int64Val(int64(off / size))
			}
		}
		panic("ValueError: array.index(x): x not in array")
	})

	arrayType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("array.__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := (*pyArray)(val.AsPointer(args[0]))
		return val.Int64Val(int64(a.count()))
	}))

	setArrayProp := func(name string, getter func(a *pyArray) val.Value) {
		arrayType.Dict.Set(val.InternString(name), val.NewPropertyVal(
			val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) < 1 {
					panic("TypeError: " + name + " missing self")
				}
				return getter((*pyArray)(val.AsPointer(args[0])))
			}),
			val.NoneVal(),
			val.NoneVal(),
		))
	}
	setArrayProp("itemsize", func(a *pyArray) val.Value {
		size, _ := arrayItemSize(a.typecode)
		return val.Int64Val(int64(size))
	})
	setArrayProp("typecode", func(a *pyArray) val.Value {
		return val.NewStringVal(string([]byte{a.typecode}))
	})

	arrayType.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("array.__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := (*pyArray)(val.AsPointer(args[0]))
		a.mu.RLock()
		defer a.mu.RUnlock()
		size, _ := arrayItemSize(a.typecode)
		if len(args) < 2 {
			panic("TypeError: array indices must be integers")
		}
		if args[1].IsSlice() {
			sl := args[1].AsSlice()
			start, _, _, length, err := val.NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, a.count())
			if err != nil {
				panic("ValueError: " + err.Error())
			}
			items := make([]val.Value, 0, length)
			for i := range length {
				items = append(items, arrayGetElement(a.data, (start+i)*size, a.typecode))
			}
			return val.NewListVal(items...)
		}
		if !args[1].IsInt64() {
			panic("TypeError: array indices must be integers")
		}
		idx := int(args[1].AsInt64())
		if idx < 0 {
			idx += a.count()
		}
		if idx < 0 || idx >= a.count() {
			panic("IndexError: array index out of range")
		}
		return arrayGetElement(a.data, idx*size, a.typecode)
	}))

	arrayType.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal("array.__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := (*pyArray)(val.AsPointer(args[0]))
		a.mu.Lock()
		defer a.mu.Unlock()
		size, _ := arrayItemSize(a.typecode)
		if len(args) < 3 || !args[1].IsInt64() {
			panic("TypeError: array indices must be integers")
		}
		idx := int(args[1].AsInt64())
		if idx < 0 {
			idx += a.count()
		}
		if idx < 0 || idx >= a.count() {
			panic("IndexError: array index out of range")
		}
		arraySetElement(a.data, idx*size, a.typecode, args[2])
		return val.NoneVal()
	}))

	arrayType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("array.__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := (*pyArray)(val.AsPointer(args[0]))
		size, _ := arrayItemSize(a.typecode)
		items := make([]val.Value, 0, a.count())
		for off := 0; off+size <= len(a.data); off += size {
			items = append(items, arrayGetElement(a.data, off, a.typecode))
		}
		return val.NewIterVal(val.NewListVal(items...))
	}))

	arrayType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("array.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := (*pyArray)(val.AsPointer(args[0]))
		size, _ := arrayItemSize(a.typecode)
		parts := make([]string, 0, a.count())
		for off := 0; off+size <= len(a.data); off += size {
			parts = append(parts, fmt.Sprintf("%v", arrayGetElement(a.data, off, a.typecode)))
		}
		return val.NewStringVal("array(" + string([]byte{a.typecode}) + ", [" + stringsJoin(parts, ", ") + "])")
	}))

	arrayType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("array.__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := (*pyArray)(val.AsPointer(args[0]))
		size, _ := arrayItemSize(a.typecode)
		parts := make([]string, 0, a.count())
		for off := 0; off+size <= len(a.data); off += size {
			parts = append(parts, fmt.Sprintf("%v", arrayGetElement(a.data, off, a.typecode)))
		}
		return val.NewStringVal("array(" + string([]byte{a.typecode}) + ", [" + stringsJoin(parts, ", ") + "])")
	}))

	// The dunder slots are resolved from the type dictionary, so they have to
	// be refreshed after the methods were installed.
	arrayType.InvalidateVersion()
}

// stringsJoin joins the parts with a separator.
func stringsJoin(parts []string, sep string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(p)
	}
	return b.String()
}

// createArrayModule builds the array module.
func (e *Engine) createArrayModule() val.Value {
	initArrayType()
	modVal := val.NewModuleVal("array", "typed compact arrays")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("array"), val.TypeVal(arrayType))
	mod.Dict.Set(val.InternString("ArrayType"), val.TypeVal(arrayType))
	mod.Dict.Set(val.InternString("typecodes"), val.NewStringVal("bBuhHiIlLqQfdw"))
	mod.Dict.Set(val.InternString("itemsize"), val.NewBuiltinFuncVal("itemsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsAnyString() || len(args[0].AsString()) != 1 {
			panic("TypeError: itemsize() argument must be a unicode character")
		}
		size, ok := arrayItemSize(args[0].AsString()[0])
		if !ok {
			panic("ValueError: bad typecode")
		}
		return val.Int64Val(int64(size))
	}))
	mod.Dict.Set(val.InternString("typecode"), val.NewBuiltinFuncVal("typecode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsAnyString() || len(args[0].AsString()) != 1 {
			panic("TypeError: typecode() argument must be a unicode character")
		}
		if _, ok := arrayItemSize(args[0].AsString()[0]); !ok {
			panic("ValueError: bad typecode")
		}
		return val.NewStringVal(args[0].AsString())
	}))
	return modVal
}
