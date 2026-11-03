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

// DictEntry represents a key-value pair stored in the dense, insertion-ordered PyDict array.
type DictEntry struct {
	Hash   uint32
	Active bool
	Key    Value
	Value  Value
}

const (
	dictIndexEmpty  = 0
	dictIndexDummy  = 1
	dictIndexOffset = 2
)

// PyDict represents a Hettinger-style compact open-addressed dictionary with offset-0 ObjectHeader.
// It separates the sparse hash index table (indices, dynamically sized as uint8, uint16, or uint32 per slot)
// from the dense insertion-ordered Entries slice.
// Header MUST be at offset 0.
type PyDict struct {
	Header     ObjectHeader
	Entries    []DictEntry
	indices    []byte
	mask       uint32
	used       int
	filled     int
	indexShift uint8 // 0: uint8 slots, 1: uint16 slots, 2: uint32 slots
	VersionTag uint64
}

// PyCell wraps a shared mutable Value on the heap for closures and nonlocal variables.
// Header MUST be at offset 0.
type PyCell struct {
	Header ObjectHeader
	Value  Value
}

// IterKind classifies the source collection of a PyIter.
type IterKind uint8

const (
	IterKindList IterKind = iota
	IterKindTuple
	IterKindDict
	IterKindRange
)

// PyIter represents a zero-allocation iterator over lists, tuples, dicts, or ranges.
// Header MUST be at offset 0.
type PyIter struct {
	Header ObjectHeader
	Kind   IterKind
	Obj    Value
	Idx    int
	Cur    int64
	Stop   int64
	Step   int64
}

// GenState classifies the lifecycle state of a PyGenerator.
type GenState uint8

const (
	GenCreated GenState = iota
	GenRunning
	GenSuspended
	GenClosed
)

// PyGenerator represents a frame-detaching Python generator object on the heap.
// Header MUST be at offset 0.
type PyGenerator struct {
	Header     ObjectHeader
	Name       string
	Code       Value
	State      GenState
	Stack      []Value
	SP         int
	Base       int
	IP         int
	Cells      []Value
	Globals    *PyDict
	YieldVal   Value
	RetVal     Value
	LastSent   Value
	ResumeHook func(ctx *ExecContext, gen *PyGenerator, val Value) (Value, IterStatus)
	Iters      [8]PyIter
}

// Compile-time assertions that every heap object places Header at offset 0.
var (
	_ [0 - unsafe.Offsetof(PyBigInt{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyString{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyList{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyDict{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyCell{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyIter{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyGenerator{}.Header)]byte
)

func init() {
	if unsafe.Sizeof(Value{}) != 16 {
		panic("val: unsafe.Sizeof(Value{}) != 16")
	}
	if unsafe.Offsetof(PyBigInt{}.Header) != 0 ||
		unsafe.Offsetof(PyString{}.Header) != 0 ||
		unsafe.Offsetof(PyList{}.Header) != 0 ||
		unsafe.Offsetof(PyDict{}.Header) != 0 ||
		unsafe.Offsetof(PyCell{}.Header) != 0 ||
		unsafe.Offsetof(PyIter{}.Header) != 0 ||
		unsafe.Offsetof(PyGenerator{}.Header) != 0 {
		panic("val: heap object Header must be at offset 0")
	}
}

// NewBigIntVal constructs a heap-allocated KindBigInt Value from a *big.Int.
func NewBigIntVal(bi *big.Int) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	if bi != nil {
		obj.Val.Set(bi)
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewBigIntFromInt64 constructs a heap-allocated KindBigInt Value initialized to x.
func NewBigIntFromInt64(x int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	obj.Val.SetInt64(x)
	return Value{num: 0, ptr: &obj.Header}
}

// NewListVal constructs a heap-allocated KindList Value wrapping the provided slice.
func NewListVal(items ...Value) Value {
	obj := &PyList{
		Header: ObjectHeader{Kind: KindList, Type: ListType},
		Items:  items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewDictVal constructs an empty heap-allocated KindDict Value with initial capacity.
func NewDictVal(capacity int) Value {
	obj := &PyDict{
		Header: ObjectHeader{Kind: KindDict, Type: DictType},
	}
	if capacity > 0 {
		obj.initTable(capacity)
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

// NewCellVal constructs a heap-allocated KindCell Value initialized to v.
func NewCellVal(v Value) Value {
	obj := &PyCell{
		Header: ObjectHeader{Kind: KindCell, Type: CellType},
		Value:  v,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsCell performs a zero-cost offset-0 downcast from Value to *PyCell.
func (v Value) AsCell() *PyCell {
	return (*PyCell)(unsafe.Pointer(v.ptr))
}

// NewIterVal constructs a heap-allocated KindIter Value wrapping an iterable.
func NewIterVal(obj Value) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Obj:    obj,
	}
	if obj.IsList() {
		it.Kind = IterKindList
	} else if obj.IsTuple() {
		it.Kind = IterKindTuple
	} else if obj.IsDict() {
		it.Kind = IterKindDict
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewRangeIterVal constructs a heap-allocated KindIter Value for range(start, stop, step).
func NewRangeIterVal(start, stop, step int64) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindRange,
		Cur:    start,
		Stop:   stop,
		Step:   step,
	}
	return Value{num: 0, ptr: &it.Header}
}

// AsIter performs a zero-cost offset-0 downcast from Value to *PyIter.
func (v Value) AsIter() *PyIter {
	return (*PyIter)(unsafe.Pointer(v.ptr))
}

// Next advances the iterator and returns the next Value and true, or NoneVal() and false when exhausted.
func (it *PyIter) Next() (Value, bool) {
	switch it.Kind {
	case IterKindList:
		list := it.Obj.AsList()
		if it.Idx >= len(list.Items) {
			return NoneVal(), false
		}
		res := list.Items[it.Idx]
		it.Idx++
		return res, true
	case IterKindTuple:
		tuple := it.Obj.AsTuple()
		if it.Idx >= len(tuple.Items) {
			return NoneVal(), false
		}
		res := tuple.Items[it.Idx]
		it.Idx++
		return res, true
	case IterKindDict:
		dict := it.Obj.AsDict()
		for it.Idx < len(dict.Entries) {
			entry := &dict.Entries[it.Idx]
			it.Idx++
			if entry.Active {
				return entry.Key, true
			}
		}
		return NoneVal(), false
	case IterKindRange:
		if (it.Step > 0 && it.Cur >= it.Stop) || (it.Step < 0 && it.Cur <= it.Stop) {
			return NoneVal(), false
		}
		res := Int64Val(it.Cur)
		it.Cur += it.Step
		return res, true
	default:
		return NoneVal(), false
	}
}

// AsGenerator performs a zero-cost offset-0 downcast from Value to *PyGenerator.
func (v Value) AsGenerator() *PyGenerator {
	return (*PyGenerator)(unsafe.Pointer(v.ptr))
}

// NewGeneratorVal constructs a heap-allocated KindGenerator Value with pre-allocated stack.
func NewGeneratorVal(name string, code Value, maxStack int, cells []Value, globals *PyDict) Value {
	if maxStack < 8 {
		maxStack = 8
	}
	gen := &PyGenerator{
		Header:  ObjectHeader{Kind: KindGenerator, Type: GeneratorType},
		Name:    name,
		Code:    code,
		State:   GenCreated,
		Stack:   make([]Value, maxStack),
		SP:      0,
		Base:    0,
		IP:      0,
		Cells:   cells,
		Globals: globals,
		RetVal:  NoneVal(),
	}
	return Value{num: 0, ptr: &gen.Header}
}

// Send advances the generator with a value, returning (yieldedValue, status).
func (g *PyGenerator) Send(ctx *ExecContext, val Value) (Value, IterStatus) {
	if g.State == GenClosed {
		return NoneVal(), IterExhausted
	}
	if g.ResumeHook != nil {
		return g.ResumeHook(ctx, g, val)
	}
	return NoneVal(), IterExhausted
}

// Close closes the generator and marks it GenClosed.
func (g *PyGenerator) Close(ctx *ExecContext) {
	g.State = GenClosed
}

// Throw resumes the generator by raising an exception at the point of suspension.
func (g *PyGenerator) Throw(ctx *ExecContext, exc Value) (Value, IterStatus) {
	if g.State == GenClosed {
		return NoneVal(), IterExhausted
	}
	if ctx != nil {
		ctx.Raise(exc)
	}
	return g.Send(ctx, NoneVal())
}

// Standard Exception Constructors

// NewException constructs a new Python exception instance of the specified class.
func NewException(cls *PyType, msg string) Value {
	if cls == nil {
		cls = ExceptionType
	}
	inst := NewInstanceOfType(cls)
	argsTuple := NewTupleVal()
	if msg != "" {
		strVal := InternString(msg)
		argsTuple = NewTupleVal(strVal)
	}
	inst.AsInstance().SetAttr(InternString("args"), argsTuple)
	return inst
}

// NewTypeError constructs a TypeError instance.
func NewTypeError(msg string) Value {
	return NewException(TypeErrorType, msg)
}

// NewValueError constructs a ValueError instance.
func NewValueError(msg string) Value {
	return NewException(ValueErrorType, msg)
}

// NewKeyError constructs a KeyError instance.
func NewKeyError(key Value) Value {
	inst := NewInstanceOfType(KeyErrorType)
	inst.AsInstance().SetAttr(InternString("args"), NewTupleVal(key))
	return inst
}

// NewIndexError constructs an IndexError instance.
func NewIndexError(msg string) Value {
	return NewException(IndexErrorType, msg)
}

// NewZeroDivisionError constructs a ZeroDivisionError instance.
func NewZeroDivisionError(msg string) Value {
	return NewException(ZeroDivisionErrorType, msg)
}

// NewNameError constructs a NameError instance.
func NewNameError(msg string) Value {
	return NewException(NameErrorType, msg)
}

// NewAttributeError constructs an AttributeError instance.
func NewAttributeError(msg string) Value {
	return NewException(AttributeErrorType, msg)
}

// NewRuntimeError constructs a RuntimeError instance.
func NewRuntimeError(msg string) Value {
	return NewException(RuntimeErrorType, msg)
}

// NewStopIteration constructs a StopIteration instance.
func NewStopIteration(retVal Value) Value {
	inst := NewInstanceOfType(StopIterationType)
	if !retVal.IsNone() && retVal.ptr != nil && retVal.ptr != &tagNone {
		inst.AsInstance().SetAttr(InternString("args"), NewTupleVal(retVal))
		inst.AsInstance().SetAttr(InternString("value"), retVal)
	} else {
		inst.AsInstance().SetAttr(InternString("args"), NewTupleVal())
		inst.AsInstance().SetAttr(InternString("value"), NoneVal())
	}
	return inst
}

func (d *PyDict) initTable(minEntries int) {
	tableCap := uint32(8)
	for int(tableCap)*2 < minEntries*3 {
		tableCap <<= 1
	}
	shift := uint8(0)
	if tableCap > 65536 {
		shift = 2
	} else if tableCap > 256 {
		shift = 1
	}
	d.indices = make([]byte, int(tableCap)<<shift)
	d.mask = tableCap - 1
	d.indexShift = shift
	maxDense := int(tableCap) * 2 / 3
	if cap(d.Entries) < maxDense {
		d.Entries = make([]DictEntry, 0, maxDense)
	}
	if d.VersionTag == 0 {
		d.VersionTag = 1
	}
}

func (d *PyDict) readIndex(slot uint32) uint32 {
	switch d.indexShift {
	case 0:
		return uint32(d.indices[slot])
	case 1:
		off := slot << 1
		return uint32(d.indices[off]) | (uint32(d.indices[off+1]) << 8)
	default:
		off := slot << 2
		return uint32(d.indices[off]) |
			(uint32(d.indices[off+1]) << 8) |
			(uint32(d.indices[off+2]) << 16) |
			(uint32(d.indices[off+3]) << 24)
	}
}

func (d *PyDict) writeIndex(slot uint32, val uint32) {
	switch d.indexShift {
	case 0:
		d.indices[slot] = uint8(val)
	case 1:
		off := slot << 1
		d.indices[off] = uint8(val)
		d.indices[off+1] = uint8(val >> 8)
	default:
		off := slot << 2
		d.indices[off] = uint8(val)
		d.indices[off+1] = uint8(val >> 8)
		d.indices[off+2] = uint8(val >> 16)
		d.indices[off+3] = uint8(val >> 24)
	}
}

func (d *PyDict) maxIndexValue() int {
	switch d.indexShift {
	case 0:
		return 0xFF
	case 1:
		return 0xFFFF
	default:
		return 0x7FFFFFFF
	}
}

func (d *PyDict) resize(minEntries int) {
	tableCap := uint32(8)
	for int(tableCap)*2 <= minEntries*3 {
		tableCap <<= 1
	}
	shift := uint8(0)
	if tableCap > 65536 {
		shift = 2
	} else if tableCap > 256 {
		shift = 1
	}

	oldEntries := d.Entries
	maxDense := int(tableCap) * 2 / 3
	newEntries := make([]DictEntry, 0, maxDense)
	d.indices = make([]byte, int(tableCap)<<shift)
	d.mask = tableCap - 1
	d.indexShift = shift
	d.used = 0
	d.filled = 0

	mask := d.mask
	for i := range oldEntries {
		e := &oldEntries[i]
		if !e.Active {
			continue
		}
		newIdx := len(newEntries)
		newEntries = append(newEntries, *e)
		slot := e.Hash & mask
		for d.readIndex(slot) != dictIndexEmpty {
			slot = (slot + 1) & mask
		}
		d.writeIndex(slot, uint32(newIdx+dictIndexOffset))
		d.used++
		d.filled++
	}
	d.Entries = newEntries
}

// Set inserts or updates a key-value pair in PyDict while preserving insertion order
// and keeping the first-inserted key on value updates (Python dict invariant).
func (d *PyDict) Set(key, value Value) {
	if len(d.indices) == 0 {
		d.initTable(8)
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask
	insertSlot := -1

	if d.indexShift == 0 {
		for {
			raw := d.indices[slot]
			if raw == dictIndexEmpty {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
				break
			}
			if raw == dictIndexDummy {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
			} else {
				e := &d.Entries[int(raw)-dictIndexOffset]
				if e.Hash == h && e.Active && Equals(e.Key, key) {
					e.Value = value
					d.VersionTag++
					return
				}
			}
			slot = (slot + 1) & mask
		}
	} else {
		for {
			raw := d.readIndex(slot)
			if raw == dictIndexEmpty {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
				break
			}
			if raw == dictIndexDummy {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
			} else {
				e := &d.Entries[int(raw)-dictIndexOffset]
				if e.Hash == h && e.Active && Equals(e.Key, key) {
					e.Value = value
					d.VersionTag++
					return
				}
			}
			slot = (slot + 1) & mask
		}
	}

	// Inserting a new key: check if table needs resizing.
	if (d.filled+1)*3 > int(d.mask+1)*2 || len(d.Entries)+dictIndexOffset >= d.maxIndexValue() {
		d.resize(d.used + 1)
		mask = d.mask
		slot = h & mask
		for d.readIndex(slot) != dictIndexEmpty {
			slot = (slot + 1) & mask
		}
		insertSlot = int(slot)
	}

	entryIdx := len(d.Entries)
	d.Entries = append(d.Entries, DictEntry{
		Hash:   h,
		Active: true,
		Key:    key,
		Value:  value,
	})
	if d.readIndex(uint32(insertSlot)) == dictIndexEmpty {
		d.filled++
	}
	d.writeIndex(uint32(insertSlot), uint32(entryIdx+dictIndexOffset))
	d.used++
	d.VersionTag++
}

// Get retrieves a value by key from PyDict with 0 heap allocations.
func (d *PyDict) Get(key Value) (Value, bool) {
	if d.used == 0 {
		return NoneVal(), false
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask

	if d.indexShift == 0 {
		for {
			raw := d.indices[slot]
			if raw == dictIndexEmpty {
				return NoneVal(), false
			}
			if raw >= dictIndexOffset {
				e := &d.Entries[int(raw)-dictIndexOffset]
				if e.Hash == h && e.Active && Equals(e.Key, key) {
					return e.Value, true
				}
			}
			slot = (slot + 1) & mask
		}
	}

	for {
		raw := d.readIndex(slot)
		if raw == dictIndexEmpty {
			return NoneVal(), false
		}
		if raw >= dictIndexOffset {
			e := &d.Entries[int(raw)-dictIndexOffset]
			if e.Hash == h && e.Active && Equals(e.Key, key) {
				return e.Value, true
			}
		}
		slot = (slot + 1) & mask
	}
}

// Delete removes key from PyDict if present, returning true if an entry was removed.
func (d *PyDict) Delete(key Value) bool {
	if d.used == 0 {
		return false
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask

	for {
		raw := d.readIndex(slot)
		if raw == dictIndexEmpty {
			return false
		}
		if raw >= dictIndexOffset {
			e := &d.Entries[int(raw)-dictIndexOffset]
			if e.Hash == h && e.Active && Equals(e.Key, key) {
				e.Active = false
				e.Key = NoneVal()
				e.Value = NoneVal()
				d.writeIndex(slot, dictIndexDummy)
				d.used--
				d.VersionTag++
				return true
			}
		}
		slot = (slot + 1) & mask
	}
}

// Len returns the number of active entries in PyDict.
func (d *PyDict) Len() int {
	return d.used
}
