package val

import (
	"math"
	"unsafe"
)

// Kind identifies the runtime type category of a Value.
type Kind uint8

const (
	KindNone Kind = iota
	KindBool
	KindInt64
	KindFloat64
	KindInlineStr // Strings <= 7 bytes packed into Value.num
	KindBigInt    // Heap-allocated math/big.Int wrapper
	KindString    // Heap-allocated string > 7 bytes
	KindList      // Heap-allocated dynamic array of Value
	KindDict      // Heap-allocated hash table
	KindCode      // Heap-allocated VM bytecode object
	KindInstance  // Heap-allocated Python object instance (Shape + flat Slots)
)

// ObjectHeader Flags bitmask constants.
const (
	// FlagInterned marks a heap-allocated KindString as canonicalized in an intern pool,
	// enabling O(1) pointer-equality comparison when both strings have FlagInterned set.
	FlagInterned uint8 = 1 << 0
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
	default:
		return "unknown"
	}
}

// PyType represents a Python type descriptor (stubbed in Phase 1).
type PyType struct{}

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
)

// NoneVal constructs the singleton None Value (0 allocations).
func NoneVal() Value {
	return Value{num: 0, ptr: &tagNone}
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

// HeaderPtr returns the underlying *ObjectHeader pointer (sentinel or offset-0 heap header).
func (v Value) HeaderPtr() *ObjectHeader {
	return v.ptr
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
