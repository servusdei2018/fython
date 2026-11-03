package val

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unsafe"
)

// Kind identifies the runtime type category of a Value.
type Kind uint8

const (
	KindNone Kind = iota
	KindBool
	KindInt64
	KindFloat64
	KindInlineStr   // Strings <= 7 bytes packed into Value.num
	KindBigInt      // Heap-allocated math/big.Int wrapper
	KindString      // Heap-allocated string > 7 bytes
	KindList        // Heap-allocated dynamic array of Value
	KindDict        // Heap-allocated hash table
	KindCode        // Heap-allocated VM bytecode object
	KindInstance    // Heap-allocated Python object instance (Shape + flat Slots)
	KindType        // Heap-allocated Python type / class object (*PyType)
	KindFunction    // Heap-allocated Python bytecode function (*PyFunction)
	KindBuiltinFunc // Heap-allocated native Go function (*PyBuiltinFunc)
	KindBoundMethod // Heap-allocated bound method (*PyBoundMethod)
	KindProperty    // Heap-allocated @property data descriptor (*PyProperty)
	KindSuper       // Heap-allocated super() proxy object (*PySuper)
	KindTuple       // Heap-allocated immutable tuple (*PyTuple)
	KindCell        // Heap-allocated closure Cell (*PyCell)
	KindIter        // Heap-allocated sequence/generator iterator (*PyIter)
	KindGenerator   // Heap-allocated detached generator frame (*PyGenerator)
	KindRange       // Unboxed range(start, stop) packed in Value.num
	KindChan        // Heap-allocated Go channel wrapper (*PyChan)
	KindModule      // Heap-allocated Python module (*PyModule)
	KindBytes       // Heap-allocated immutable bytes (*PyBytes)
	KindByteArray   // Heap-allocated mutable bytearray (*PyByteArray)
	KindMemoryView  // Heap-allocated buffer memoryview (*PyMemoryView)
	KindSlice       // Heap-allocated slice object (*PySlice)
)

// ObjectHeader Flags bitmask constants.
const (
	// FlagInterned marks a heap-allocated KindString as canonicalized in an intern pool,
	// enabling O(1) pointer-equality comparison when both strings have FlagInterned set.
	FlagInterned uint8 = 1 << 0
	// FlagDataDescriptor marks a descriptor object (such as @property or custom __set__)
	// as a data descriptor that takes precedence over instance attributes.
	FlagDataDescriptor uint8 = 1 << 1
)

// String returns the human-readable name of the Kind.
func (k Kind) String() string {
	switch k {
	case KindNone:
		return "NoneType"
	case KindBool:
		return "bool"
	case KindInt64:
		return "int"
	case KindFloat64:
		return "float"
	case KindInlineStr, KindString:
		return "str"
	case KindBigInt:
		return "bigint"
	case KindList:
		return "list"
	case KindDict:
		return "dict"
	case KindCode:
		return "code"
	case KindInstance:
		return "instance"
	case KindType:
		return "type"
	case KindFunction:
		return "function"
	case KindBuiltinFunc:
		return "builtin_function_or_method"
	case KindBoundMethod:
		return "method"
	case KindProperty:
		return "property"
	case KindSuper:
		return "super"
	case KindTuple:
		return "tuple"
	case KindCell:
		return "cell"
	case KindIter:
		return "iterator"
	case KindGenerator:
		return "generator"
	case KindRange:
		return "range"
	case KindChan:
		return "channel"
	case KindModule:
		return "module"
	case KindBytes:
		return "bytes"
	case KindByteArray:
		return "bytearray"
	case KindMemoryView:
		return "memoryview"
	case KindSlice:
		return "slice"
	default:
		return "unknown"
	}
}

// ObjectHeader is placed at byte offset 0 of every heap-allocated Python object,
// and is also instantiated as package-level static sentinels for unboxed primitives.
type ObjectHeader struct {
	Kind  Kind
	Flags uint8
	_     uint16
	Hash  uint32
	Type  *PyType
}

// Value is an exact 16-byte tagged value representation.
// Unboxed primitives store their payload in num and point ptr at a static sentinel ObjectHeader.
// Heap-allocated objects point ptr at their offset-0 ObjectHeader.
type Value struct {
	num uint64        // Unboxed 64-bit payload
	ptr *ObjectHeader // Points to static sentinel for primitives, or offset-0 header for heap objects
}

// Compile-time assertion that Value is exactly 16 bytes.
var (
	_ [16 - unsafe.Sizeof(Value{})]byte
	_ [unsafe.Sizeof(Value{}) - 16]byte
)

// Static sentinel headers for unboxed primitive types.
var (
	tagNone      = ObjectHeader{Kind: KindNone}
	tagBool      = ObjectHeader{Kind: KindBool}
	tagInt64     = ObjectHeader{Kind: KindInt64}
	tagFloat64   = ObjectHeader{Kind: KindFloat64}
	tagInlineStr = ObjectHeader{Kind: KindInlineStr}
	tagUnbound   = ObjectHeader{Kind: KindNone}
	tagRange     = ObjectHeader{Kind: KindRange}
)

// NoneVal constructs the singleton None Value (0 allocations).
func NoneVal() Value {
	return Value{num: 0, ptr: &tagNone}
}

// UnboundVal constructs a zero-allocation internal sentinel for unbound method receivers in OP_LOAD_METHOD.
func UnboundVal() Value {
	return Value{num: 0, ptr: &tagUnbound}
}

// IsUnbound reports whether v is the unbound receiver sentinel via a single pointer equality check.
func (v Value) IsUnbound() bool {
	return v.ptr == &tagUnbound
}

// BoolVal constructs an unboxed boolean Value (0 allocations).
func BoolVal(b bool) Value {
	var n uint64
	if b {
		n = 1
	}
	return Value{num: n, ptr: &tagBool}
}

// Int64Val constructs an unboxed 64-bit signed integer Value (0 allocations).
func Int64Val(i int64) Value {
	return Value{num: uint64(i), ptr: &tagInt64}
}

// Float64Val constructs an unboxed IEEE-754 float64 Value (0 allocations).
func Float64Val(f float64) Value {
	return Value{num: math.Float64bits(f), ptr: &tagFloat64}
}

// ObjectVal constructs a Value referencing an offset-0 ObjectHeader on the heap.
func ObjectVal(hdr *ObjectHeader) Value {
	return Value{num: 0, ptr: hdr}
}

// Kind returns the Value's Kind directly from its ObjectHeader with zero branching.
func (v Value) Kind() Kind {
	return v.ptr.Kind
}

// Type resolves the Value to its *PyType (either via v.ptr.Type or the static builtinTypes lookup table).
func (v Value) Type() *PyType {
	if t := v.ptr.Type; t != nil {
		return t
	}
	return builtinTypes[v.ptr.Kind]
}

// HeaderPtr returns the underlying *ObjectHeader pointer (sentinel or offset-0 heap header).
func (v Value) HeaderPtr() *ObjectHeader {
	return v.ptr
}

// AsPointer returns the unsafe.Pointer to the underlying heap object header.
func AsPointer(v Value) unsafe.Pointer {
	return unsafe.Pointer(v.ptr)
}

// IsNone reports whether v is None via a single pointer equality check.
func (v Value) IsNone() bool {
	return v.ptr == &tagNone
}

// IsBool reports whether v is an unboxed bool via a single pointer equality check.
func (v Value) IsBool() bool {
	return v.ptr == &tagBool
}

// IsInt64 reports whether v is an unboxed int64 via a single pointer equality check.
func (v Value) IsInt64() bool {
	return v.ptr == &tagInt64
}

// IsFloat64 reports whether v is an unboxed float64 via a single pointer equality check.
func (v Value) IsFloat64() bool {
	return v.ptr == &tagFloat64
}

// IsInlineStr reports whether v is an inline small string (<= 7 bytes) via a single pointer equality check.
func (v Value) IsInlineStr() bool {
	return v.ptr == &tagInlineStr
}

// IsBigInt reports whether v is a heap-allocated arbitrary-precision integer.
func (v Value) IsBigInt() bool {
	return v.ptr.Kind == KindBigInt
}

// IsString reports whether v is a heap-allocated string (> 7 bytes).
func (v Value) IsString() bool {
	return v.ptr.Kind == KindString
}

// IsAnyString reports whether v is either an inline small string or a heap-allocated string.
func (v Value) IsAnyString() bool {
	return v.ptr == &tagInlineStr || v.ptr.Kind == KindString
}

// IsList reports whether v is a heap-allocated PyList.
func (v Value) IsList() bool {
	return v.ptr.Kind == KindList
}

// IsDict reports whether v is a heap-allocated PyDict.
func (v Value) IsDict() bool {
	return v.ptr.Kind == KindDict
}

// IsInstance reports whether v is a heap-allocated PyInstance.
func (v Value) IsInstance() bool {
	return v.ptr.Kind == KindInstance
}

// IsType reports whether v is a heap-allocated PyType.
func (v Value) IsType() bool {
	return v.ptr.Kind == KindType
}

// IsFunction reports whether v is a heap-allocated PyFunction.
func (v Value) IsFunction() bool {
	return v.ptr.Kind == KindFunction
}

// IsBuiltinFunc reports whether v is a heap-allocated PyBuiltinFunc.
func (v Value) IsBuiltinFunc() bool {
	return v.ptr.Kind == KindBuiltinFunc
}

// IsBoundMethod reports whether v is a heap-allocated PyBoundMethod.
func (v Value) IsBoundMethod() bool {
	return v.ptr.Kind == KindBoundMethod
}

// IsProperty reports whether v is a heap-allocated PyProperty.
func (v Value) IsProperty() bool {
	return v.ptr.Kind == KindProperty
}

// IsSuper reports whether v is a heap-allocated PySuper.
func (v Value) IsSuper() bool {
	return v.ptr.Kind == KindSuper
}

// IsTuple reports whether v is a heap-allocated PyTuple.
func (v Value) IsTuple() bool {
	return v.ptr.Kind == KindTuple
}

// IsCell reports whether v is a heap-allocated PyCell.
func (v Value) IsCell() bool {
	return v.ptr.Kind == KindCell
}

// IsIter reports whether v is a heap-allocated PyIter.
func (v Value) IsIter() bool {
	return v.ptr.Kind == KindIter
}

// IsGenerator reports whether v is a heap-allocated PyGenerator.
func (v Value) IsGenerator() bool {
	return v.ptr.Kind == KindGenerator
}

// IsChan reports whether v is a heap-allocated PyChan.
func (v Value) IsChan() bool {
	return v.ptr != nil && v.ptr.Kind == KindChan
}

// IsModule reports whether v is a heap-allocated PyModule.
func (v Value) IsModule() bool {
	return v.ptr != nil && v.ptr.Kind == KindModule
}

// AsModule performs an offset-0 downcast from Value to *PyModule.
func (v Value) AsModule() *PyModule {
	return (*PyModule)(unsafe.Pointer(v.ptr))
}

// IsBytes reports whether v is a heap-allocated PyBytes.
func (v Value) IsBytes() bool {
	return v.ptr != nil && v.ptr.Kind == KindBytes
}

// AsBytes performs an offset-0 downcast from Value to *PyBytes.
func (v Value) AsBytes() *PyBytes {
	return (*PyBytes)(unsafe.Pointer(v.ptr))
}

// IsByteArray reports whether v is a heap-allocated PyByteArray.
func (v Value) IsByteArray() bool {
	return v.ptr != nil && v.ptr.Kind == KindByteArray
}

// AsByteArray performs an offset-0 downcast from Value to *PyByteArray.
func (v Value) AsByteArray() *PyByteArray {
	return (*PyByteArray)(unsafe.Pointer(v.ptr))
}

// IsMemoryView reports whether v is a heap-allocated PyMemoryView.
func (v Value) IsMemoryView() bool {
	return v.ptr != nil && v.ptr.Kind == KindMemoryView
}

// AsMemoryView performs an offset-0 downcast from Value to *PyMemoryView.
func (v Value) AsMemoryView() *PyMemoryView {
	return (*PyMemoryView)(unsafe.Pointer(v.ptr))
}

// IsSlice reports whether v is a heap-allocated PySlice.
func (v Value) IsSlice() bool {
	return v.ptr != nil && v.ptr.Kind == KindSlice
}

// AsSlice performs an offset-0 downcast from Value to *PySlice.
func (v Value) AsSlice() *PySlice {
	return (*PySlice)(unsafe.Pointer(v.ptr))
}

// IsRange reports whether v is an unboxed range Value.
func (v Value) IsRange() bool {
	return v.ptr == &tagRange
}

// RangeVal constructs a zero-allocation unboxed range(start, stop) Value.
func RangeVal(start, stop int32) Value {
	return Value{
		num: (uint64(uint32(start)) << 32) | uint64(uint32(stop)),
		ptr: &tagRange,
	}
}

// RangeStart returns the start index of an unboxed range Value.
func (v Value) RangeStart() int32 {
	return int32(v.num >> 32)
}

// RangeStop returns the stop index of an unboxed range Value.
func (v Value) RangeStop() int32 {
	return int32(v.num)
}

// IsDataDescriptor reports whether v has FlagDataDescriptor set in its ObjectHeader.
func (v Value) IsDataDescriptor() bool {
	return (v.ptr.Flags & FlagDataDescriptor) != 0
}

// IsInterned reports whether v has FlagInterned set in its ObjectHeader, or is an inline string.
func (v Value) IsInterned() bool {
	return v.ptr == &tagInlineStr || (v.ptr.Flags&FlagInterned) != 0
}

// AsBool extracts the boolean payload from a KindBool Value.
func (v Value) AsBool() bool {
	return v.num != 0
}

// AsInt64 extracts the int64 payload from a KindInt64 Value.
func (v Value) AsInt64() int64 {
	return int64(v.num)
}

// AsFloat64 extracts the float64 payload from a KindFloat64 Value.
func (v Value) AsFloat64() float64 {
	return math.Float64frombits(v.num)
}

// IsTruthy evaluates Python truthiness of v without heap allocation.
func (v Value) IsTruthy() bool {
	if v.ptr == &tagBool {
		return v.num != 0
	}
	if v.ptr == &tagInt64 {
		return v.num != 0
	}
	if v.ptr == &tagNone {
		return false
	}
	if v.ptr == &tagFloat64 {
		return math.Float64frombits(v.num) != 0.0
	}
	if v.ptr == &tagInlineStr {
		return (v.num & 0xFF) != 0
	}
	switch v.ptr.Kind {
	case KindBigInt:
		return v.AsBigInt().Val.Sign() != 0
	case KindString:
		return len(v.AsStringObj().Data) > 0
	case KindList:
		return len(v.AsList().Items) > 0
	case KindDict:
		return v.AsDict().Len() > 0
	case KindTuple:
		return len(v.AsTuple().Items) > 0
	case KindBytes:
		return len(v.AsBytes().Data) > 0
	case KindByteArray:
		return len(v.AsByteArray().Data) > 0
	case KindMemoryView:
		return len(v.AsMemoryView().Buf) > 0
	case KindSlice:
		return true
	default:
		return true
	}
}

// Equals performs Python value equality comparison (a == b).
func Equals(a, b Value) bool {
	if a.ptr == b.ptr {
		if a.ptr == &tagInt64 || a.ptr == &tagBool || a.ptr == &tagNone || a.ptr == &tagInlineStr {
			return a.num == b.num
		}
		if a.ptr == &tagFloat64 {
			return math.Float64frombits(a.num) == math.Float64frombits(b.num)
		}
		// Identical heap object pointer.
		return true
	}

	// Fast path: two distinct interned heap strings can never be equal.
	if (a.ptr.Flags & b.ptr.Flags & FlagInterned) != 0 {
		return false
	}

	// String comparison across inline and heap strings.
	if (a.ptr == &tagInlineStr || a.ptr.Kind == KindString) &&
		(b.ptr == &tagInlineStr || b.ptr.Kind == KindString) {
		return StringEquals(a, b)
	}

	// Binary data types comparison (bytes, bytearray, memoryview).
	isBufA := a.ptr.Kind == KindBytes || a.ptr.Kind == KindByteArray || a.ptr.Kind == KindMemoryView
	isBufB := b.ptr.Kind == KindBytes || b.ptr.Kind == KindByteArray || b.ptr.Kind == KindMemoryView
	if isBufA && isBufB {
		var bufA, bufB []byte
		switch a.ptr.Kind {
		case KindBytes:
			bufA = a.AsBytes().Data
		case KindByteArray:
			bufA = a.AsByteArray().Data
		case KindMemoryView:
			bufA = a.AsMemoryView().Buf
		}
		switch b.ptr.Kind {
		case KindBytes:
			bufB = b.AsBytes().Data
		case KindByteArray:
			bufB = b.AsByteArray().Data
		case KindMemoryView:
			bufB = b.AsMemoryView().Buf
		}
		return bytes.Equal(bufA, bufB)
	}
	if a.ptr.Kind == KindSlice && b.ptr.Kind == KindSlice {
		sa := a.AsSlice()
		sb := b.AsSlice()
		return Equals(sa.Start, sb.Start) && Equals(sa.Stop, sb.Stop) && Equals(sa.Step, sb.Step)
	}

	// Numeric cross-type equality (int64, float64, bool, bigint).
	if a.ptr == &tagInt64 && b.ptr == &tagFloat64 {
		return float64(int64(a.num)) == math.Float64frombits(b.num)
	}
	if a.ptr == &tagFloat64 && b.ptr == &tagInt64 {
		return math.Float64frombits(a.num) == float64(int64(b.num))
	}
	if a.ptr == &tagInt64 && b.ptr == &tagBool {
		return a.num == b.num
	}
	if a.ptr == &tagBool && b.ptr == &tagInt64 {
		return a.num == b.num
	}
	if a.ptr == &tagFloat64 && b.ptr == &tagBool {
		return math.Float64frombits(a.num) == float64(b.num)
	}
	if a.ptr == &tagBool && b.ptr == &tagFloat64 {
		return float64(a.num) == math.Float64frombits(b.num)
	}
	if a.ptr.Kind == KindBigInt && b.ptr.Kind == KindBigInt {
		return a.AsBigInt().Val.Cmp(&b.AsBigInt().Val) == 0
	}
	if a.ptr.Kind == KindBigInt && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		bi := int64(b.num)
		return a.AsBigInt().Val.IsInt64() && a.AsBigInt().Val.Int64() == bi
	}
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && b.ptr.Kind == KindBigInt {
		ai := int64(a.num)
		return b.AsBigInt().Val.IsInt64() && b.AsBigInt().Val.Int64() == ai
	}
	if a.ptr.Kind == KindBigInt && b.ptr == &tagFloat64 {
		f, _ := a.AsBigInt().Val.Float64()
		return f == math.Float64frombits(b.num)
	}
	if a.ptr == &tagFloat64 && b.ptr.Kind == KindBigInt {
		f, _ := b.AsBigInt().Val.Float64()
		return math.Float64frombits(a.num) == f
	}

	return false
}

// mix64To32 folds a 64-bit value into a non-zero 32-bit hash in registers (0 allocations).
func mix64To32(x uint64) uint32 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	h := uint32(x)
	if h == 0 {
		return 1
	}
	return h
}

// HashValue computes the 32-bit hash of v with 0 heap allocations.
// Enforces Python numeric hash equivalence: for any int64 i, float64 f, and bool b
// where float64(i) == f, HashValue(i) == HashValue(f) == HashValue(b).
func HashValue(v Value) uint32 {
	if v.ptr == &tagInt64 || v.ptr == &tagBool {
		f := float64(int64(v.num))
		if f == 0.0 {
			return mix64To32(0)
		}
		return mix64To32(math.Float64bits(f))
	}
	if v.ptr.Kind == KindString {
		if v.ptr.Hash != 0 {
			return v.ptr.Hash
		}
		h := HashString((*PyString)(unsafe.Pointer(v.ptr)).Data)
		v.ptr.Hash = h
		return h
	}
	if v.ptr == &tagInlineStr {
		return InlineStrHash(v)
	}
	if v.ptr == &tagFloat64 {
		f := math.Float64frombits(v.num)
		if f == 0.0 {
			return mix64To32(0)
		}
		return mix64To32(v.num)
	}
	if v.ptr == &tagNone {
		return 0x4e6f6e65
	}
	if v.ptr.Kind == KindBigInt {
		bi := &v.AsBigInt().Val
		if bi.IsInt64() {
			f := float64(bi.Int64())
			if f == 0.0 {
				return mix64To32(0)
			}
			return mix64To32(math.Float64bits(f))
		}
		f, _ := bi.Float64()
		return mix64To32(math.Float64bits(f))
	}
	return mix64To32(uint64(uintptr(unsafe.Pointer(v.ptr))))
}

// String returns the Python string representation of Value.
func (v Value) String() string {
	switch v.Kind() {
	case KindNone:
		return "None"
	case KindBool:
		if v.AsBool() {
			return "True"
		}
		return "False"
	case KindInt64:
		return strconv.FormatInt(v.AsInt64(), 10)
	case KindFloat64:
		return strconv.FormatFloat(v.AsFloat64(), 'g', -1, 64)
	case KindInlineStr, KindString:
		return v.AsString()
	case KindList:
		var sb strings.Builder
		sb.WriteString("[")
		items := v.AsList().Items
		for i, it := range items {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(it.String())
		}
		sb.WriteString("]")
		return sb.String()
	case KindTuple:
		var sb strings.Builder
		sb.WriteString("(")
		items := v.AsTuple().Items
		for i, it := range items {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(it.String())
		}
		if len(items) == 1 {
			sb.WriteString(",")
		}
		sb.WriteString(")")
		return sb.String()
	case KindDict:
		var sb strings.Builder
		sb.WriteString("{")
		d := v.AsDict()
		first := true
		for _, e := range d.Entries {
			if !e.Key.IsUnbound() {
				if !first {
					sb.WriteString(", ")
				}
				first = false
				sb.WriteString(e.Key.String())
				sb.WriteString(": ")
				sb.WriteString(e.Value.String())
			}
		}
		sb.WriteString("}")
		return sb.String()
	case KindType:
		return "<class '" + v.AsType().Name + "'>"
	case KindFunction:
		return "<function " + v.AsFunction().Name + ">"
	case KindBuiltinFunc:
		return "<built-in function " + v.AsBuiltinFunc().Name + ">"
	case KindRange:
		start := v.RangeStart()
		stop := v.RangeStop()
		if start == 0 {
			return fmt.Sprintf("range(0, %d)", stop)
		}
		return fmt.Sprintf("range(%d, %d)", start, stop)
	case KindGenerator:
		return fmt.Sprintf("<generator object %s at %p>", v.AsGenerator().Name, v.ptr)
	case KindModule:
		mod := v.AsModule()
		if mod.File != "" {
			return fmt.Sprintf("<module '%s' from '%s'>", mod.Name, mod.File)
		}
		return fmt.Sprintf("<module '%s'>", mod.Name)
	case KindBytes:
		return fmt.Sprintf("b%q", string(v.AsBytes().Data))
	case KindByteArray:
		return fmt.Sprintf("bytearray(b%q)", string(v.AsByteArray().Data))
	case KindMemoryView:
		return fmt.Sprintf("<memory at %p>", v.ptr)
	case KindSlice:
		sl := v.AsSlice()
		return fmt.Sprintf("slice(%s, %s, %s)", sl.Start.String(), sl.Stop.String(), sl.Step.String())
	default:
		return fmt.Sprintf("<%s object at %p>", v.Type().Name, v.ptr)
	}
}
