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
	KindInlineStr      // Strings <= 7 bytes packed into Value.num
	KindBigInt         // Heap-allocated math/big.Int wrapper
	KindString         // Heap-allocated string > 7 bytes
	KindList           // Heap-allocated dynamic array of Value
	KindDict           // Heap-allocated hash table
	KindCode           // Heap-allocated VM bytecode object
	KindInstance       // Heap-allocated Python object instance (Shape + flat Slots)
	KindType           // Heap-allocated Python type / class object (*PyType)
	KindFunction       // Heap-allocated Python bytecode function (*PyFunction)
	KindBuiltinFunc    // Heap-allocated native Go function (*PyBuiltinFunc)
	KindBoundMethod    // Heap-allocated bound method (*PyBoundMethod)
	KindProperty       // Heap-allocated @property data descriptor (*PyProperty)
	KindSuper          // Heap-allocated super() proxy object (*PySuper)
	KindTuple          // Heap-allocated immutable tuple (*PyTuple)
	KindCell           // Heap-allocated closure Cell (*PyCell)
	KindIter           // Heap-allocated sequence/generator iterator (*PyIter)
	KindGenerator      // Heap-allocated detached generator frame (*PyGenerator)
	KindRange          // Unboxed range(start, stop) packed in Value.num
	KindChan           // Heap-allocated Go channel wrapper (*PyChan)
	KindModule         // Heap-allocated Python module (*PyModule)
	KindBytes          // Heap-allocated immutable bytes (*PyBytes)
	KindByteArray      // Heap-allocated mutable bytearray (*PyByteArray)
	KindMemoryView     // Heap-allocated buffer memoryview (*PyMemoryView)
	KindSlice          // Heap-allocated slice object (*PySlice)
	KindCoroutine      // Heap-allocated detached coroutine frame (*PyCoroutine)
	KindFuture         // Heap-allocated async Future (*PyFuture)
	KindTask           // Heap-allocated async Task (*PyTask)
	KindSet            // Heap-allocated set (*PySet)
	KindExceptionGroup // Heap-allocated exception group (*PyExceptionGroup)
	KindMemberDescr    // Heap-allocated member descriptor for __slots__ (*PyMemberDescr)
	KindNotImplemented // Static sentinel for NotImplemented singleton
	KindClassMethod    // Heap-allocated classmethod descriptor (*PyClassMethod)
	KindStaticMethod   // Heap-allocated staticmethod descriptor (*PyStaticMethod)
	KindFrame          // Heap-allocated on-demand Python stack frame (*PyFrame)
	KindLocalsProxy    // Heap-allocated PEP 667 frame locals proxy (*PyLocalsProxy)
	KindTraceback      // Heap-allocated exception traceback node (*PyTraceback)
	KindWeakRef        // Heap-allocated weak reference (*PyWeakRef)
	KindWeakProxy      // Heap-allocated weak reference proxy (*PyWeakProxy)
	KindEllipsis       // Static sentinel for Ellipsis singleton
	KindMappingProxy   // Heap-allocated mappingproxy wrapper (*PyMappingProxy)
	KindNative         // Heap-allocated native Go object with custom struct
	KindComplex        // Heap-allocated complex number (*PyComplex)
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

const (
	// FlagForbidDynamicAttrs marks a type whose instances forbid dynamic attribute addition (due to __slots__).
	FlagForbidDynamicAttrs uint32 = 1 << 2
	// FlagCustomGetAttribute marks a type that defines a custom __getattribute__ requiring inline cache bypass.
	FlagCustomGetAttribute uint32 = 1 << 3
	// FlagHeapType marks a dynamically constructed Python class (as opposed to a static built-in type).
	FlagHeapType uint32 = 1 << 4
	// FlagInitSubclassKwDone marks that __init_subclass__ has already been invoked with keyword arguments.
	FlagInitSubclassKwDone uint32 = 1 << 5
	// FlagCustomSetAttr marks a type that defines a custom __setattr__ requiring inline cache bypass.
	FlagCustomSetAttr uint32 = 1 << 6
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
	case KindCoroutine:
		return "coroutine"
	case KindFuture:
		return "Future"
	case KindTask:
		return "Task"
	case KindSet:
		return "set"
	case KindExceptionGroup:
		return "ExceptionGroup"
	case KindMemberDescr:
		return "member_descriptor"
	case KindNotImplemented:
		return "NotImplementedType"
	case KindClassMethod:
		return "classmethod"
	case KindStaticMethod:
		return "staticmethod"
	case KindFrame:
		return "frame"
	case KindLocalsProxy:
		return "FrameLocalsProxy"
	case KindTraceback:
		return "traceback"
	case KindWeakRef:
		return "ReferenceType"
	case KindWeakProxy:
		return "ProxyType"
	case KindEllipsis:
		return "ellipsis"
	case KindMappingProxy:
		return "mappingproxy"
	case KindNative:
		return "object"
	case KindComplex:
		return "complex"
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
	tagNone           = ObjectHeader{Kind: KindNone}
	tagBool           = ObjectHeader{Kind: KindBool}
	tagInt64          = ObjectHeader{Kind: KindInt64}
	tagFloat64        = ObjectHeader{Kind: KindFloat64}
	tagInlineStr      = ObjectHeader{Kind: KindInlineStr}
	tagUnbound        = ObjectHeader{Kind: KindNone}
	tagRange          = ObjectHeader{Kind: KindRange}
	tagNotImplemented = ObjectHeader{Kind: KindNotImplemented}
	tagEllipsis       = ObjectHeader{Kind: KindEllipsis}
)

// NoneVal constructs the singleton None Value (0 allocations).
func NoneVal() Value {
	return Value{num: 0, ptr: &tagNone}
}

// NotImplementedVal constructs the singleton NotImplemented Value (0 allocations).
func NotImplementedVal() Value {
	return Value{num: 0, ptr: &tagNotImplemented}
}

// IsNotImplemented reports whether v is the NotImplemented singleton via pointer equality.
func (v Value) IsNotImplemented() bool {
	return v.ptr == &tagNotImplemented
}

// EllipsisVal constructs the singleton Ellipsis Value (0 allocations).
func EllipsisVal() Value {
	return Value{num: 0, ptr: &tagEllipsis}
}

// IsEllipsis reports whether v is the Ellipsis singleton via pointer equality.
func (v Value) IsEllipsis() bool {
	return v.ptr == &tagEllipsis
}

// UnboundVal constructs a zero-allocation internal sentinel for unbound method receivers in OP_LOAD_METHOD.
func UnboundVal() Value {
	return Value{num: 0, ptr: &tagUnbound}
}

// IsUnbound reports whether v is the unbound receiver sentinel or uninitialized slot.
func (v Value) IsUnbound() bool {
	return v.ptr == &tagUnbound || v.ptr == nil
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

// Kind returns the Value's Kind directly from its ObjectHeader.
func (v Value) Kind() Kind {
	if v.ptr == nil {
		return KindNone
	}
	return v.ptr.Kind
}

// Type resolves the Value to its *PyType (either via v.ptr.Type or the static builtinTypes lookup table).
func (v Value) Type() *PyType {
	if v.ptr == nil {
		return NoneType
	}
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
	return v.ptr != nil && v.ptr.Kind == KindBigInt
}

// IsString reports whether v is a heap-allocated string (> 7 bytes).
func (v Value) IsString() bool {
	return v.ptr != nil && v.ptr.Kind == KindString
}

// IsAnyString reports whether v is either an inline small string or a heap-allocated string.
func (v Value) IsAnyString() bool {
	return v.ptr == &tagInlineStr || (v.ptr != nil && v.ptr.Kind == KindString)
}

// IsList reports whether v is a heap-allocated PyList.
func (v Value) IsList() bool {
	return v.ptr != nil && v.ptr.Kind == KindList
}

// IsDict reports whether v is a heap-allocated PyDict.
func (v Value) IsDict() bool {
	return v.ptr != nil && v.ptr.Kind == KindDict
}

func (v Value) IsCode() bool {
	return v.ptr != nil && v.ptr.Kind == KindCode
}

// IsInstance reports whether v is a heap-allocated PyInstance.
func (v Value) IsInstance() bool {
	return v.ptr != nil && v.ptr.Kind == KindInstance
}

// IsType reports whether v is a heap-allocated PyType.
func (v Value) IsType() bool {
	return v.ptr != nil && v.ptr.Kind == KindType
}

// IsFunction reports whether v is a heap-allocated PyFunction.
func (v Value) IsFunction() bool {
	return v.ptr != nil && v.ptr.Kind == KindFunction
}

// IsBuiltinFunc reports whether v is a heap-allocated PyBuiltinFunc.
func (v Value) IsBuiltinFunc() bool {
	return v.ptr != nil && v.ptr.Kind == KindBuiltinFunc
}

// IsBoundMethod reports whether v is a heap-allocated PyBoundMethod.
func (v Value) IsBoundMethod() bool {
	return v.ptr != nil && v.ptr.Kind == KindBoundMethod
}

// IsProperty reports whether v is a heap-allocated PyProperty.
func (v Value) IsProperty() bool {
	return v.ptr != nil && v.ptr.Kind == KindProperty
}

// IsSuper reports whether v is a heap-allocated PySuper.
func (v Value) IsSuper() bool {
	return v.ptr != nil && v.ptr.Kind == KindSuper
}

// IsTuple reports whether v is a heap-allocated PyTuple.
func (v Value) IsTuple() bool {
	return v.ptr != nil && v.ptr.Kind == KindTuple
}

// IsMemberDescr reports whether v is a heap-allocated PyMemberDescr.
func (v Value) IsMemberDescr() bool {
	return v.ptr != nil && v.ptr.Kind == KindMemberDescr
}

// IsClassMethod reports whether v is a heap-allocated PyClassMethod.
func (v Value) IsClassMethod() bool {
	return v.ptr != nil && v.ptr.Kind == KindClassMethod
}

// IsStaticMethod reports whether v is a heap-allocated PyStaticMethod.
func (v Value) IsStaticMethod() bool {
	return v.ptr != nil && v.ptr.Kind == KindStaticMethod
}

// IsCell reports whether v is a heap-allocated PyCell.
func (v Value) IsCell() bool {
	return v.ptr != nil && v.ptr.Kind == KindCell
}

// IsIter reports whether v is a heap-allocated PyIter.
func (v Value) IsIter() bool {
	return v.ptr != nil && v.ptr.Kind == KindIter
}

// IsGenerator reports whether v is a heap-allocated PyGenerator.
func (v Value) IsGenerator() bool {
	return v.ptr != nil && v.ptr.Kind == KindGenerator
}

// IsCoroutine reports whether v is a heap-allocated PyCoroutine.
func (v Value) IsCoroutine() bool {
	return v.ptr != nil && v.ptr.Kind == KindCoroutine
}

// IsFuture reports whether v is a heap-allocated PyFuture.
func (v Value) IsFuture() bool {
	return v.ptr != nil && v.ptr.Kind == KindFuture
}

// IsTask reports whether v is a heap-allocated PyTask.
func (v Value) IsTask() bool {
	return v.ptr != nil && v.ptr.Kind == KindTask
}

func (v Value) IsFrame() bool {
	return v.ptr != nil && v.ptr.Kind == KindFrame
}

func (v Value) IsLocalsProxy() bool {
	return v.ptr != nil && v.ptr.Kind == KindLocalsProxy
}

func (v Value) IsTraceback() bool {
	return v.ptr != nil && v.ptr.Kind == KindTraceback
}

func (v Value) IsWeakRef() bool {
	return v.ptr != nil && v.ptr.Kind == KindWeakRef
}

func (v Value) IsWeakProxy() bool {
	return v.ptr != nil && v.ptr.Kind == KindWeakProxy
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

// IsSet reports whether v is a heap-allocated PySet.
func (v Value) IsSet() bool {
	return v.ptr != nil && v.ptr.Kind == KindSet
}

// AsSet performs an offset-0 downcast from Value to *PySet.
func (v Value) AsSet() *PySet {
	return (*PySet)(unsafe.Pointer(v.ptr))
}

// IsExceptionGroup reports whether v is a heap-allocated PyExceptionGroup.
func (v Value) IsExceptionGroup() bool {
	return v.ptr != nil && v.ptr.Kind == KindExceptionGroup
}

// AsExceptionGroup performs an offset-0 downcast from Value to *PyExceptionGroup.
func (v Value) AsExceptionGroup() *PyExceptionGroup {
	return (*PyExceptionGroup)(unsafe.Pointer(v.ptr))
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
	return v.ptr != nil && (v.ptr.Flags&FlagDataDescriptor) != 0
}

// IsInterned reports whether v has FlagInterned set in its ObjectHeader, or is an inline string.
func (v Value) IsInterned() bool {
	return v.ptr == &tagInlineStr || (v.ptr != nil && (v.ptr.Flags&FlagInterned) != 0)
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
	case KindSet:
		return v.AsSet().Len() > 0
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
	case KindComplex:
		c := v.AsComplex()
		return c.Real != 0 || c.Imag != 0
	case KindInstance, KindNative:
		return objectTruthy(v)
	default:
		return true
	}
}

// objectTruthy applies __bool__, then __len__ (via SqLen), to instances and
// native objects. Objects defining neither are truthy.
func objectTruthy(v Value) bool {
	t := v.Type()
	if t == nil {
		return true
	}
	if fn, _, ok := t.LookupMRO(InternString("__bool__")); ok {
		var ctx *ExecContext
		return (*ExecContext).Invoke1(ctx, fn, v).IsTruthy()
	}
	if sqLen := t.Slots.SqLen; sqLen != nil {
		return sqLen(nil, v) != 0
	}
	return true
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

	// Custom __eq__ dispatch on user classes
	if (a.Type() != nil && a.Type().dunderEq.ptr != nil) || (b.Type() != nil && b.Type().dunderEq.ptr != nil) {
		res := RichCompare(nil, a, b, CmpEQ)
		return res.IsTruthy()
	}

	return equalsStructural(a, b)
}

func equalsStructural(a, b Value) bool {
	if a.ptr == b.ptr {
		if a.ptr == &tagInt64 || a.ptr == &tagBool || a.ptr == &tagNone || a.ptr == &tagInlineStr {
			return a.num == b.num
		}
		if a.ptr == &tagFloat64 {
			return math.Float64frombits(a.num) == math.Float64frombits(b.num)
		}
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
	if a.ptr.Kind == KindTuple && b.ptr.Kind == KindTuple {
		ia := a.AsTuple().Items
		ib := b.AsTuple().Items
		if len(ia) != len(ib) {
			return false
		}
		for i := range ia {
			if !Equals(ia[i], ib[i]) {
				return false
			}
		}
		return true
	}
	if a.ptr.Kind == KindList && b.ptr.Kind == KindList {
		ia := a.AsList().Items
		ib := b.AsList().Items
		if len(ia) != len(ib) {
			return false
		}
		for i := range ia {
			if !Equals(ia[i], ib[i]) {
				return false
			}
		}
		return true
	}
	if a.ptr.Kind == KindDict && b.ptr.Kind == KindDict {
		da := a.AsDict()
		db := b.AsDict()
		if da.Len() != db.Len() {
			return false
		}
		for _, item := range da.Items() {
			vb, ok := db.Get(item.Key)
			if !ok || !Equals(item.Value, vb) {
				return false
			}
		}
		return true
	}
	if a.ptr.Kind == KindSet && b.ptr.Kind == KindSet {
		sa := a.AsSet()
		sb := b.AsSet()
		if sa.Len() != sb.Len() {
			return false
		}
		for _, k := range sa.Items() {
			if !sb.Contains(k) {
				return false
			}
		}
		return true
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
	if v.ptr.Kind == KindComplex {
		return hashComplex(v.AsComplex())
	}
	if v.ptr.Kind == KindTuple && (v.ptr.Type == nil || v.ptr.Type == TupleType || v.ptr.Type.dunderHash.ptr == nil) {
		if v.ptr.Hash != 0 {
			return v.ptr.Hash
		}
		items := v.AsTuple().Items
		var acc uint64 = 0x345678
		for _, it := range items {
			acc = (acc ^ uint64(HashValue(it))) * 1000003
		}
		acc ^= uint64(len(items))
		h := mix64To32(acc)
		v.ptr.Hash = h
		return h
	}
	if v.ptr.Kind == KindBytes {
		if v.ptr.Hash != 0 {
			return v.ptr.Hash
		}
		data := v.AsBytes().Data
		h := fnvOffset32
		for i := 0; i < len(data); i++ {
			h ^= uint32(data[i])
			h *= fnvPrime32
		}
		if h == 0 {
			h = 1
		}
		v.ptr.Hash = h
		return h
	}
	tp := v.Type()
	if tp != nil && tp.dunderHash.ptr != nil {
		if tp.dunderHash.IsNone() {
			panic(NewTypeError("unhashable type: '" + tp.Name + "'"))
		}
		var ctx *ExecContext
		res := ctx.Invoke1(tp.dunderHash, v)
		if res.ptr == &tagInt64 || res.ptr == &tagBool {
			f := float64(int64(res.num))
			if f == 0.0 {
				return mix64To32(0)
			}
			return mix64To32(math.Float64bits(f))
		}
		if res.ptr != nil && res.ptr.Kind == KindBigInt {
			return HashValue(res)
		}
		panic(NewTypeError("__hash__ method should return an integer"))
	}
	if v.ptr.Kind == KindList || v.ptr.Kind == KindDict || v.ptr.Kind == KindSet || v.ptr.Kind == KindByteArray {
		typeName := v.Kind().String()
		if tp != nil {
			typeName = tp.Name
		}
		panic(NewTypeError("unhashable type: '" + typeName + "'"))
	}
	return mix64To32(uint64(uintptr(unsafe.Pointer(v.ptr))))
}

// String returns the Python string representation of Value.
func (v Value) String() string {
	switch v.Kind() {
	case KindBigInt:
		return v.AsBigInt().Val.String()
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
		return FloatRepr(v.AsFloat64())
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
		for _, e := range d.ActiveEntries() {
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
	case KindSet:
		s := v.AsSet()
		if s == nil || s.Len() == 0 {
			return "set()"
		}
		var sb strings.Builder
		sb.WriteString("{")
		first := true
		for item := range s.Entries {
			if !first {
				sb.WriteString(", ")
			}
			first = false
			sb.WriteString(item.String())
		}
		sb.WriteString("}")
		return sb.String()
	case KindExceptionGroup:
		eg := v.AsExceptionGroup()
		return fmt.Sprintf("ExceptionGroup(%q, %v)", eg.Message, eg.Exceptions)
	case KindComplex:
		c := v.AsComplex()
		return ComplexRepr(c.Real, c.Imag)
	case KindType:
		return TypeRepr(v.AsType())
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
	case KindCoroutine:
		return fmt.Sprintf("<coroutine object %s at %p>", v.AsCoroutine().Name, v.ptr)
	case KindFuture:
		return fmt.Sprintf("<Future at %p>", v.ptr)
	case KindTask:
		return fmt.Sprintf("<Task at %p>", v.ptr)
	case KindFrame:
		return fmt.Sprintf("<frame at %p>", v.ptr)
	case KindLocalsProxy:
		return fmt.Sprintf("<FrameLocalsProxy at %p>", v.ptr)
	case KindTraceback:
		return fmt.Sprintf("<traceback object at %p>", v.ptr)
	case KindWeakRef:
		return fmt.Sprintf("<weakref at %p>", v.ptr)
	case KindWeakProxy:
		return fmt.Sprintf("<weakproxy at %p>", v.ptr)
	case KindModule:
		mod := v.AsModule()
		if mod.File != "" {
			return fmt.Sprintf("<module '%s' from '%s'>", mod.Name, mod.File)
		}
		return fmt.Sprintf("<module '%s'>", mod.Name)
	case KindBytes:
		return bytesRepr(v.AsBytes().Data)
	case KindByteArray:
		return "bytearray(" + bytesRepr(v.AsByteArray().Data) + ")"
	case KindMemoryView:
		return fmt.Sprintf("<memory at %p>", v.ptr)
	case KindSlice:
		sl := v.AsSlice()
		return fmt.Sprintf("slice(%s, %s, %s)", sl.Start.String(), sl.Stop.String(), sl.Step.String())
	case KindMemberDescr:
		md := v.AsMemberDescr()
		return fmt.Sprintf("<member '%s' of '%s' objects>", md.Name.AsString(), md.Owner.Name)
	case KindNotImplemented:
		return "NotImplemented"
	case KindEllipsis:
		return "Ellipsis"
	case KindMappingProxy:
		mp := v.AsMappingProxy()
		return fmt.Sprintf("mappingproxy(%s)", mp.Mapping.String())
	default:
		return fmt.Sprintf("<%s object at %p>", v.Type().Name, v.ptr)
	}
}

// bytesRepr formats data as a Python bytes literal.
func bytesRepr(data []byte) string {
	quote := byte('\'')
	hasSingle, hasDouble := false, false
	for _, b := range data {
		switch b {
		case '\'':
			hasSingle = true
		case '"':
			hasDouble = true
		}
	}
	if hasSingle && !hasDouble {
		quote = '"'
	}
	const hexDigits = "0123456789abcdef"
	buf := make([]byte, 0, len(data)+3)
	buf = append(buf, 'b', quote)
	for _, b := range data {
		switch {
		case b == quote || b == '\\':
			buf = append(buf, '\\', b)
		case b == '\t':
			buf = append(buf, '\\', 't')
		case b == '\n':
			buf = append(buf, '\\', 'n')
		case b == '\r':
			buf = append(buf, '\\', 'r')
		case b < 0x20 || b >= 0x7f:
			buf = append(buf, '\\', 'x', hexDigits[b>>4], hexDigits[b&15])
		default:
			buf = append(buf, b)
		}
	}
	return string(append(buf, quote))
}

// TypeQualName returns "module.qualname" (or just "qualname" for builtins),
// the form used inside union and generic-alias reprs.
func TypeQualName(t *PyType) string {
	qual := t.Name
	module := ""
	if t.Dict != nil {
		if q, ok := t.Dict.Get(InternString("__qualname__")); ok && q.IsAnyString() {
			qual = q.AsString()
		}
		if m, ok := t.Dict.Get(InternString("__module__")); ok && m.IsAnyString() {
			module = m.AsString()
		}
	}
	if module != "" && module != "builtins" {
		return module + "." + qual
	}
	return qual
}

// TypeRepr formats a class like CPython: <class 'module.qualname'>, or
// <class 'qualname'> for builtins.
func TypeRepr(t *PyType) string {
	return "<class '" + TypeQualName(t) + "'>"
}
