package val

import (
	"math/big"
	"unsafe"
)

// PyBigInt wraps an arbitrary-precision math/big.Int on the heap.
// Header MUST be at offset 0.
type PyBigInt struct {
	Header ObjectHeader
	Val    big.Int
}

// PyString wraps a heap-allocated string (> 7 bytes).
// Header MUST be at offset 0.
type PyString struct {
	Header ObjectHeader
	Data   string
}

// PyList represents a dynamic array of Value on the heap.
// Header MUST be at offset 0.
type PyList struct {
	Header ObjectHeader
	Items  []Value
}

// DictEntry represents a key-value pair stored in a PyDict.
type DictEntry struct {
	Key   Value
	Value Value
}

// PyDict represents a heap-allocated dictionary with offset-0 ObjectHeader.
// Header MUST be at offset 0.
type PyDict struct {
	Header  ObjectHeader
	Entries []DictEntry
}

// Compile-time assertions that every heap object places Header at offset 0.
var (
	_ [0 - unsafe.Offsetof(PyBigInt{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyString{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyList{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyDict{}.Header)]byte
)

func init() {
	if unsafe.Sizeof(Value{}) != 16 {
		panic("val: unsafe.Sizeof(Value{}) != 16")
	}
	if unsafe.Offsetof(PyBigInt{}.Header) != 0 ||
		unsafe.Offsetof(PyString{}.Header) != 0 ||
		unsafe.Offsetof(PyList{}.Header) != 0 ||
		unsafe.Offsetof(PyDict{}.Header) != 0 {
		panic("val: heap object Header must be at offset 0")
	}
}

// NewBigIntVal constructs a heap-allocated KindBigInt Value from a *big.Int.
func NewBigIntVal(bi *big.Int) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt},
	}
	if bi != nil {
		obj.Val.Set(bi)
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewBigIntFromInt64 constructs a heap-allocated KindBigInt Value initialized to x.
func NewBigIntFromInt64(x int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt},
	}
	obj.Val.SetInt64(x)
	return Value{num: 0, ptr: &obj.Header}
}

// NewListVal constructs a heap-allocated KindList Value wrapping the provided slice.
func NewListVal(items ...Value) Value {
	obj := &PyList{
		Header: ObjectHeader{Kind: KindList},
		Items:  items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewDictVal constructs an empty heap-allocated KindDict Value with initial capacity.
func NewDictVal(capacity int) Value {
	var entries []DictEntry
	if capacity > 0 {
		entries = make([]DictEntry, 0, capacity)
	}
	obj := &PyDict{
		Header:  ObjectHeader{Kind: KindDict},
		Entries: entries,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsBigInt performs a zero-cost offset-0 downcast from Value to *PyBigInt.
func (v Value) AsBigInt() *PyBigInt {
	return (*PyBigInt)(unsafe.Pointer(v.ptr))
}

// AsStringObj performs a zero-cost offset-0 downcast from Value to *PyString.
func (v Value) AsStringObj() *PyString {
	return (*PyString)(unsafe.Pointer(v.ptr))
}

// AsList performs a zero-cost offset-0 downcast from Value to *PyList.
func (v Value) AsList() *PyList {
	return (*PyList)(unsafe.Pointer(v.ptr))
}

// AsDict performs a zero-cost offset-0 downcast from Value to *PyDict.
func (v Value) AsDict() *PyDict {
	return (*PyDict)(unsafe.Pointer(v.ptr))
}

// Set inserts or updates a key-value pair in PyDict.
func (d *PyDict) Set(key, value Value) {
	for i := range d.Entries {
		if Equals(d.Entries[i].Key, key) {
			d.Entries[i].Value = value
			return
		}
	}
	d.Entries = append(d.Entries, DictEntry{Key: key, Value: value})
}

// Get retrieves a value by key from PyDict.
func (d *PyDict) Get(key Value) (Value, bool) {
	for i := range d.Entries {
		if Equals(d.Entries[i].Key, key) {
			return d.Entries[i].Value, true
		}
	}
	return NoneVal(), false
}

// Len returns the number of entries in PyDict.
func (d *PyDict) Len() int {
	return len(d.Entries)
}
