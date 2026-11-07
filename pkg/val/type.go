package val

import (
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"
)

// NativeFunc is an alias for VectorcallFunc for direct native method pointers.
type NativeFunc = VectorcallFunc

// ErrInconsistentMRO is returned when C3 superclass linearization cannot find a valid ordering.
var ErrInconsistentMRO = errors.New("val: Cannot create a consistent method resolution order (MRO)")

// ExecContext bridges TypeSlots and descriptor execution back to the active VM
// without circular package imports or interface/slice heap escapes.
type ExecContext struct {
	Stack           []Value
	SP              int
	CallHook        func(ctx *ExecContext, callable Value, argBase int, nargs int, kwnames []Value) Value
	ActiveException Value
	VM              any
	ThreadID        int64
}

// AcquireExecContextHook allows standalone comparison/hashing helpers in pkg/val
// to obtain a temporary VM ExecContext when invoked without an explicit ctx.
var AcquireExecContextHook func() (*ExecContext, func())

// ResolveLazyImportHook allows resolving a PyLazyImport placeholder through the VM.
var ResolveLazyImportHook func(vm any, mod *PyModule, name Value, lazyVal Value) (Value, error)

func (ctx *ExecContext) Raise(exc Value) {
	if ctx != nil {
		ctx.ActiveException = exc
	}
}

func (ctx *ExecContext) HasException() bool {
	return ctx != nil && ctx.ActiveException.ptr != nil && ctx.ActiveException.ptr != &tagNone
}

func (ctx *ExecContext) ClearException() {
	if ctx != nil {
		ctx.ActiveException = NoneVal()
	}
}

// Invoke0 invokes callable with 0 arguments.
func (ctx *ExecContext) Invoke0(callable Value) Value {
	if callable.IsBuiltinFunc() {
		fn := callable.AsBuiltinFunc().Fn
		if ctx != nil && ctx.SP <= len(ctx.Stack) {
			return fn(ctx, callable, ctx.Stack[ctx.SP:ctx.SP], nil)
		}
		return fn(ctx, callable, nil, nil)
	}
	if (ctx == nil || ctx.CallHook == nil) && AcquireExecContextHook != nil {
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		return tmpCtx.Invoke0(callable)
	}
	if ctx != nil && ctx.CallHook != nil {
		return ctx.CallHook(ctx, callable, ctx.SP, 0, nil)
	}
	if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, callable, nil, nil)
	}
	panic("val: object is not callable")
}

// Invoke1 invokes callable with 1 positional argument (a) with 0 heap allocations.
func (ctx *ExecContext) Invoke1(callable, a Value) Value {
	if ctx != nil && ctx.SP+1 <= len(ctx.Stack) {
		base := ctx.SP
		ctx.Stack[base] = a
		if callable.IsBuiltinFunc() {
			return callable.AsBuiltinFunc().Fn(ctx, callable, ctx.Stack[base:base+1], nil)
		}
		if ctx.CallHook != nil {
			return ctx.CallHook(ctx, callable, base, 1, nil)
		}
	}
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) && AcquireExecContextHook != nil {
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		return tmpCtx.Invoke1(callable, a)
	}
	var buf [1]Value
	buf[0] = a
	if callable.IsBuiltinFunc() {
		return callable.AsBuiltinFunc().Fn(ctx, callable, buf[:], nil)
	}
	if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, callable, buf[:], nil)
	}
	panic("val: object is not callable")
}

// Invoke2 invokes callable with 2 positional arguments (a, b) with 0 heap allocations.
func (ctx *ExecContext) Invoke2(callable, a, b Value) Value {
	if ctx != nil && ctx.SP+2 <= len(ctx.Stack) {
		base := ctx.SP
		ctx.Stack[base] = a
		ctx.Stack[base+1] = b
		if callable.IsBuiltinFunc() {
			return callable.AsBuiltinFunc().Fn(ctx, callable, ctx.Stack[base:base+2], nil)
		}
		if ctx.CallHook != nil {
			return ctx.CallHook(ctx, callable, base, 2, nil)
		}
	}
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) && AcquireExecContextHook != nil {
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		return tmpCtx.Invoke2(callable, a, b)
	}
	var buf [2]Value
	buf[0] = a
	buf[1] = b
	if callable.IsBuiltinFunc() {
		return callable.AsBuiltinFunc().Fn(ctx, callable, buf[:], nil)
	}
	if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, callable, buf[:], nil)
	}
	panic("val: object is not callable")
}

// Invoke3 invokes callable with 3 positional arguments (a, b, c) with 0 heap allocations.
func (ctx *ExecContext) Invoke3(callable, a, b, c Value) Value {
	if ctx != nil && ctx.SP+3 <= len(ctx.Stack) {
		base := ctx.SP
		ctx.Stack[base] = a
		ctx.Stack[base+1] = b
		ctx.Stack[base+2] = c
		if callable.IsBuiltinFunc() {
			return callable.AsBuiltinFunc().Fn(ctx, callable, ctx.Stack[base:base+3], nil)
		}
		if ctx.CallHook != nil {
			return ctx.CallHook(ctx, callable, base, 3, nil)
		}
	}
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) && AcquireExecContextHook != nil {
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		return tmpCtx.Invoke3(callable, a, b, c)
	}
	var buf [3]Value
	buf[0] = a
	buf[1] = b
	buf[2] = c
	if callable.IsBuiltinFunc() {
		return callable.AsBuiltinFunc().Fn(ctx, callable, buf[:], nil)
	}
	if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, callable, buf[:], nil)
	}
	panic("val: object is not callable")
}

// Call invokes callable with arbitrary positional arguments and keyword names.
func (ctx *ExecContext) Call(callable Value, args []Value, kwnames []Value) Value {
	if ctx != nil && ctx.CallHook != nil && ctx.SP+len(args) <= len(ctx.Stack) {
		base := ctx.SP
		copy(ctx.Stack[base:], args)
		return ctx.CallHook(ctx, callable, base, len(args), kwnames)
	}
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) && AcquireExecContextHook != nil {
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		return tmpCtx.Call(callable, args, kwnames)
	}
	if callable.IsBuiltinFunc() {
		return callable.AsBuiltinFunc().Fn(ctx, callable, args, kwnames)
	}
	if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, callable, args, kwnames)
	}
	panic("val: object is not callable")
}

// IterStatus indicates the result of an iterator step in TpIterNext.
type IterStatus uint8

const (
	IterYielded IterStatus = iota
	IterExhausted
	IterError
)

// Type Slot function pointer signatures.
type (
	UnaryOpFunc    func(ctx *ExecContext, a Value) Value
	BinaryOpFunc   func(ctx *ExecContext, a, b Value) Value
	TernaryOpFunc  func(ctx *ExecContext, a, b, c Value) Value
	LenFunc        func(ctx *ExecContext, a Value) int64
	DescrGetFunc   func(ctx *ExecContext, descr, obj Value, owner *PyType) Value
	DescrSetFunc   func(ctx *ExecContext, descr, obj, value Value)
	VectorcallFunc func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value
	IterFunc       func(ctx *ExecContext, o Value) Value
	IterNextFunc   func(ctx *ExecContext, o Value) (Value, IterStatus)
	BufferFunc     func(ctx *ExecContext, obj Value, view *PyBuffer, flags int) error
)

// BufferFlags for TpAsBuffer / GetBuffer.
const (
	PyBUF_SIMPLE   = 0
	PyBUF_WRITABLE = 0x0001
	PyBUF_FORMAT   = 0x0004
	PyBUF_ND       = 0x0008
	PyBUF_STRIDES  = 0x0010 | PyBUF_ND
)

// PyBuffer represents a zero-allocation direct memory view into an underlying binary object.
type PyBuffer struct {
	Buf      []byte
	ReadOnly bool
	ItemSize int
	Obj      Value
}

// BufferProvider interface for Go types that can export a zero-copy byte buffer.
type BufferProvider interface {
	GetBuffer(ctx *ExecContext, view *PyBuffer, flags int) error
	ReleaseBuffer(view *PyBuffer)
}

// TypeSlots holds CPython-style native Go function pointers for fast polymorphic operations.
type TypeSlots struct {
	NbAdd          BinaryOpFunc
	NbSub          BinaryOpFunc
	NbMul          BinaryOpFunc
	NbAbs          UnaryOpFunc
	SqLen          LenFunc
	SqItem         BinaryOpFunc
	SqAssItem      TernaryOpFunc
	MpSubscript    BinaryOpFunc
	MpAssSubscript TernaryOpFunc
	TpCall         VectorcallFunc
	TpGetAttr      BinaryOpFunc
	TpSetAttr      TernaryOpFunc
	TpDescrGet     DescrGetFunc
	TpDescrSet     DescrSetFunc
	TpIter         IterFunc
	TpIterNext     IterNextFunc
	TpAsBuffer     BufferFunc
}

// PyType represents a Python type/class object with CPython-style TypeSlots,
// C3 MRO linearization, and monotonic VersionTag invalidation.
// Header MUST be at byte offset 0.
type PyType struct {
	Header     ObjectHeader
	Name       string
	Bases      []*PyType
	MRO        []*PyType
	Subclasses []*PyType
	Dict       *PyDict
	RootShape  *Shape
	VersionTag uint64
	Flags      uint32
	Slots      TypeSlots

	// Resolved dunder callables cached at class creation/mutation time
	// so slot trampolines execute with 0 dictionary lookups.
	dunderAdd     Value
	dunderSub     Value
	dunderMul     Value
	dunderAbs     Value
	dunderLen     Value
	dunderGetItem Value
	dunderSetItem Value
	dunderCall    Value
	dunderGet     Value
	dunderSet     Value
	dunderInit    Value
	dunderGetAttr Value
	dunderSetAttr Value
	dunderIter    Value
	dunderNext    Value
	dunderEnter   Value
	dunderExit    Value

	// Rich comparisons
	dunderLt Value
	dunderLe Value
	dunderEq Value
	dunderNe Value
	dunderGt Value
	dunderGe Value

	// Reflected arithmetic
	dunderRAdd      Value
	dunderRSub      Value
	dunderRMul      Value
	dunderRMatMul   Value
	dunderRTruediv  Value
	dunderRFloordiv Value
	dunderRMod      Value
	dunderRPow      Value
	dunderRLShift   Value
	dunderRRShift   Value
	dunderRAnd      Value
	dunderRXor      Value
	dunderROr       Value

	// In-place arithmetic
	dunderIAdd      Value
	dunderISub      Value
	dunderIMul      Value
	dunderIMatMul   Value
	dunderITruediv  Value
	dunderIFloordiv Value
	dunderIMod      Value
	dunderIPow      Value
	dunderILShift   Value
	dunderIRShift   Value
	dunderIAnd      Value
	dunderIXor      Value
	dunderIOr       Value

	// Reflection, attributes & lifecycle
	dunderGetAttribute Value
	dunderDelAttr      Value
	dunderDir          Value
	dunderContains     Value
	dunderMissing      Value
	dunderNew          Value
	dunderInitSubclass Value
	dunderSetName      Value
	dunderHash         Value
}

// Compile-time assertion that PyType.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(PyType{}.Header)]byte

var nextTypeVersion uint64

func allocTypeVersion() uint64 {
	return atomic.AddUint64(&nextTypeVersion, 1)
}

// Built-in type singletons and static lookup table indexed by Kind.
var (
	builtinTypes [256]*PyType

	TypeType              *PyType
	ObjectType            *PyType
	NoneType              *PyType
	BoolType              *PyType
	IntType               *PyType
	FloatType             *PyType
	StrType               *PyType
	BigIntType            *PyType
	ListType              *PyType
	DictType              *PyType
	DictKeysType          *PyType
	TupleType             *PyType
	CodeType              *PyType
	FunctionType          *PyType
	BuiltinFuncType       *PyType
	BoundMethodType       *PyType
	PropertyType          *PyType
	ClassMethodType       *PyType
	StaticMethodType      *PyType
	SuperType             *PyType
	CellType              *PyType
	IterType              *PyType
	RangeType             *PyType
	GeneratorType         *PyType
	CoroutineType         *PyType
	FutureType            *PyType
	TaskType              *PyType
	ChanType              *PyType
	ModuleType            *PyType
	BytesType             *PyType
	ByteArrayType         *PyType
	MemoryViewType        *PyType
	SliceType             *PyType
	SetType               *PyType
	ComplexType           *PyType
	FrameType             *PyType
	LocalsProxyType       *PyType
	TracebackType         *PyType
	WeakRefType           *PyType
	WeakProxyType         *PyType
	CallableWeakProxyType *PyType
	EllipsisType          *PyType
	MappingProxyType      *PyType
	FrozenDictType        *PyType
	SentinelType          *PyType

	// Built-in exception types
	BaseExceptionType             *PyType
	ExceptionType                 *PyType
	BaseExceptionGroupType        *PyType
	ExceptionGroupType            *PyType
	AssertionErrorType            *PyType
	TypeErrorType                 *PyType
	ValueErrorType                *PyType
	KeyErrorType                  *PyType
	IndexErrorType                *PyType
	ZeroDivisionErrorType         *PyType
	NameErrorType                 *PyType
	AttributeErrorType            *PyType
	RuntimeErrorType              *PyType
	StopIterationType             *PyType
	StopAsyncIterationType        *PyType
	GeneratorExitType             *PyType
	ImportErrorType               *PyType
	ModuleNotFoundErrorType       *PyType
	BufferErrorType               *PyType
	ReferenceErrorType            *PyType
	ArithmeticErrorType           *PyType
	FloatingPointErrorType        *PyType
	OverflowErrorType             *PyType
	LookupErrorType               *PyType
	OSErrorType                   *PyType
	IOErrorType                   *PyType
	EnvironmentErrorType          *PyType
	FileNotFoundErrorType         *PyType
	PermissionErrorType           *PyType
	FileExistsErrorType           *PyType
	IsADirectoryErrorType         *PyType
	NotADirectoryErrorType        *PyType
	InterruptedErrorType          *PyType
	ProcessLookupErrorType        *PyType
	TimeoutErrorType              *PyType
	ConnectionErrorType           *PyType
	BrokenPipeErrorType           *PyType
	ConnectionAbortedErrorType    *PyType
	ConnectionRefusedErrorType    *PyType
	ConnectionResetErrorType      *PyType
	BlockingIOErrorType           *PyType
	ChildProcessErrorType         *PyType
	UnboundLocalErrorType         *PyType
	NotImplementedErrorType       *PyType
	RecursionErrorType            *PyType
	SyntaxErrorType               *PyType
	IndentationErrorType          *PyType
	TabErrorType                  *PyType
	SystemErrorType               *PyType
	SystemExitType                *PyType
	KeyboardInterruptType         *PyType
	EOFErrorType                  *PyType
	MemoryErrorType               *PyType
	UnicodeErrorType              *PyType
	UnicodeDecodeErrorType        *PyType
	UnicodeEncodeErrorType        *PyType
	UnicodeTranslateErrorType     *PyType
	WarningType                   *PyType
	UserWarningType               *PyType
	DeprecationWarningType        *PyType
	PendingDeprecationWarningType *PyType
	SyntaxWarningType             *PyType
	RuntimeWarningType            *PyType
	FutureWarningType             *PyType
	ImportWarningType             *PyType
	UnicodeWarningType            *PyType
	BytesWarningType              *PyType
	ResourceWarningType           *PyType
	EncodingWarningType           *PyType

	// PEP 695 Type Parameter types
	TypeVarType       *PyType
	TypeVarTupleType  *PyType
	ParamSpecType     *PyType
	TypeAliasTypeType *PyType

	// Metaclass & Descriptors
	MemberDescrType    *PyType
	NotImplementedType *PyType
)

// Pre-interned canonical dunder names used for slot synchronization.
var (
	DunderAdd     Value
	DunderSub     Value
	DunderMul     Value
	DunderAbs     Value
	DunderLen     Value
	DunderGetItem Value
	DunderSetItem Value
	DunderCall    Value
	DunderGet     Value
	DunderSet     Value
	DunderInit    Value
	DunderGetAttr Value
	DunderSetAttr Value
	DunderIter    Value
	DunderNext    Value
	DunderEnter   Value
	DunderExit    Value

	// Rich comparisons
	DunderLt Value
	DunderLe Value
	DunderEq Value
	DunderNe Value
	DunderGt Value
	DunderGe Value

	// Reflected arithmetic
	DunderRAdd      Value
	DunderRSub      Value
	DunderRMul      Value
	DunderRMatMul   Value
	DunderRTruediv  Value
	DunderRFloordiv Value
	DunderRMod      Value
	DunderRDivMod   Value
	DunderRPow      Value
	DunderRLShift   Value
	DunderRRShift   Value
	DunderRAnd      Value
	DunderRXor      Value
	DunderROr       Value

	// In-place arithmetic
	DunderIAdd      Value
	DunderISub      Value
	DunderIMul      Value
	DunderIMatMul   Value
	DunderITruediv  Value
	DunderIFloordiv Value
	DunderIMod      Value
	DunderIPow      Value
	DunderILShift   Value
	DunderIRShift   Value
	DunderIAnd      Value
	DunderIXor      Value
	DunderIOr       Value

	// Reflection, attributes & lifecycle
	DunderGetAttribute Value
	DunderDelAttr      Value
	DunderDir          Value
	DunderContains     Value
	DunderMissing      Value
	DunderNew          Value
	DunderSetName      Value
	DunderInitSubclass Value
	DunderPrepare      Value
	DunderMroEntries   Value
	DunderOrigBases    Value
	DunderSlots        Value
)

// TypeVal wraps a *PyType as a Value of KindType.
func TypeVal(t *PyType) Value {
	return Value{num: 0, ptr: &t.Header}
}

// AsType performs a zero-cost offset-0 downcast from Value to *PyType.
func (v Value) AsType() *PyType {
	return (*PyType)(unsafe.Pointer(v.ptr))
}

// ComputeMRO computes the C3 superclass linearization for cls.
func ComputeMRO(cls *PyType) ([]*PyType, error) {
	bases := cls.Bases
	if len(bases) == 0 {
		if cls == ObjectType || ObjectType == nil {
			return []*PyType{cls}, nil
		}
		bases = []*PyType{ObjectType}
	}

	// Reject duplicate direct bases (e.g., class C(A, A)).
	for i := 0; i < len(bases); i++ {
		if bases[i] == cls {
			return nil, ErrInconsistentMRO
		}
		for j := i + 1; j < len(bases); j++ {
			if bases[i] == bases[j] {
				return nil, ErrInconsistentMRO
			}
		}
	}

	seqs := make([][]*PyType, 0, len(bases)+1)
	for _, b := range bases {
		seq := make([]*PyType, len(b.MRO))
		copy(seq, b.MRO)
		seqs = append(seqs, seq)
	}
	directBases := make([]*PyType, len(bases))
	copy(directBases, bases)
	seqs = append(seqs, directBases)

	res := []*PyType{cls}

	for {
		nonEmpty := 0
		for _, s := range seqs {
			if len(s) > 0 {
				nonEmpty++
			}
		}
		if nonEmpty == 0 {
			return res, nil
		}

		var candidate *PyType
		for _, s := range seqs {
			if len(s) == 0 {
				continue
			}
			head := s[0]
			inTail := false
			for _, other := range seqs {
				for k := 1; k < len(other); k++ {
					if other[k] == head {
						inTail = true
						break
					}
				}
				if inTail {
					break
				}
			}
			if !inTail {
				candidate = head
				break
			}
		}

		if candidate == nil {
			return nil, ErrInconsistentMRO
		}

		res = append(res, candidate)
		for i, s := range seqs {
			if len(s) > 0 && s[0] == candidate {
				seqs[i] = s[1:]
			}
		}
	}
}

// NewType constructs a new user-defined or built-in *PyType, computes its C3 MRO,
// links it into its bases' Subclasses lists, and populates its TypeSlots.
func NewType(name string, bases []*PyType, dict *PyDict) (*PyType, error) {
	return NewTypeWithMeta(name, bases, dict, TypeType)
}

// NewTypeWithMeta constructs a new *PyType with a custom metaclass.
func NewTypeWithMeta(name string, bases []*PyType, dict *PyDict, meta *PyType) (*PyType, error) {
	if meta == nil {
		meta = TypeType
	}
	if len(bases) == 0 && ObjectType != nil {
		bases = []*PyType{ObjectType}
	}
	if dict == nil {
		dict = NewDictVal(8).AsDict()
	} else {
		// Ensure all existing keys in dict are canonicalized in the intern pool.
		for i := range dict.Entries {
			e := &dict.Entries[i]
			if e.Active && e.Key.IsAnyString() && !e.Key.IsInterned() {
				e.Key = InternValue(e.Key)
			}
		}
	}

	t := &PyType{
		Header: ObjectHeader{
			Kind: KindType,
			Type: meta,
		},
		Name:       name,
		Bases:      bases,
		Dict:       dict,
		RootShape:  NewRootShapeForType(nil),
		VersionTag: allocTypeVersion(),
	}
	t.RootShape.Type = t

	mro, err := ComputeMRO(t)
	if err != nil {
		return nil, err
	}
	t.MRO = mro

	subclassesMu.Lock()
	for _, b := range bases {
		b.Subclasses = append(b.Subclasses, t)
	}
	subclassesMu.Unlock()

	// Python data model: defining __eq__ without __hash__ implicitly sets __hash__ = None.
	if DunderEq.ptr != nil {
		if _, hasEq := dict.Get(DunderEq); hasEq {
			hashKey := InternString("__hash__")
			if _, hasHash := dict.Get(hashKey); !hasHash {
				dict.Set(hashKey, NoneVal())
			}
		}
	}

	// Python data model: __new__ defined as a function is implicitly wrapped in staticmethod.
	if DunderNew.ptr != nil {
		if newFn, hasNew := dict.Get(DunderNew); hasNew && newFn.IsFunction() {
			dict.Set(DunderNew, NewStaticMethodVal(newFn))
		}
	}

	// Collect inherited fixed slots from base RootShapes (including primitive backing slots and parent __slots__)
	var inheritedAttrs []ShapeAttr
	seenAttr := func(n Value) bool {
		for _, a := range inheritedAttrs {
			if a.Name == n || StringEquals(a.Name, n) {
				return true
			}
		}
		return false
	}
	for _, b := range mro[1:] {
		if b != ObjectType && b != nil && b.RootShape != nil {
			for _, a := range b.RootShape.Attrs {
				if !seenAttr(a.Name) {
					inheritedAttrs = append(inheritedAttrs, ShapeAttr{Name: a.Name, Index: len(inheritedAttrs)})
				}
			}
		}
	}

	// Process __slots__
	slotsVal, hasSlots := dict.Get(DunderSlots)
	if hasSlots || len(inheritedAttrs) > 0 {
		var cleanSlots []string
		hasDict := !hasSlots
		if hasSlots {
			var slotNames []string
			if slotsVal.IsAnyString() {
				slotNames = append(slotNames, slotsVal.AsString())
			} else if slotsVal.IsTuple() {
				for _, item := range slotsVal.AsTuple().Items {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			} else if slotsVal.IsList() {
				for _, item := range slotsVal.AsList().Items {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			} else if slotsVal.IsDict() {
				for _, item := range slotsVal.AsDict().Keys() {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			} else if slotsVal.IsSet() {
				for _, item := range slotsVal.AsSet().Items() {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			} else if slotsVal.IsIter() {
				it := slotsVal.AsIter()
				for {
					item, ok := it.Next()
					if !ok {
						break
					}
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			}

			for _, sn := range slotNames {
				if sn == "__dict__" {
					hasDict = true
				} else if sn == "__weakref__" {
					continue
				} else {
					internedSn := InternString(sn)
					if !seenAttr(internedSn) {
						cleanSlots = append(cleanSlots, sn)
					}
				}
			}
		}

		totalSlots := len(inheritedAttrs) + len(cleanSlots)
		if totalSlots > 0 {
			newAttrs := make([]ShapeAttr, totalSlots)
			curIdx := 0
			for _, a := range inheritedAttrs {
				newAttrs[curIdx] = ShapeAttr{Name: a.Name, Index: curIdx}
				curIdx++
			}
			for _, sn := range cleanSlots {
				internedName := InternString(sn)
				newAttrs[curIdx] = ShapeAttr{Name: internedName, Index: curIdx}
				descr := NewMemberDescr(t, internedName, curIdx)
				t.Dict.Set(internedName, descr)
				curIdx++
			}

			t.RootShape = &Shape{
				ID:        allocShapeID(),
				Type:      t,
				SlotIndex: totalSlots - 1,
				Attrs:     newAttrs,
			}
			empty := make([]ShapeTransition, 0, 4)
			t.RootShape.transitions.Store(&empty)
		}

		if hasSlots && !hasDict {
			t.Flags |= uint32(FlagForbidDynamicAttrs)
		}
	}

	if len(bases) > 0 && bases[0] != nil {
		t.Slots = bases[0].Slots
		t.Slots.TpCall = nil
	}

	isTypeSubclass := false
	for _, b := range t.MRO {
		if b == TypeType {
			isTypeSubclass = true
			break
		}
	}
	if isTypeSubclass {
		t.Slots.TpGetAttr = typeGetAttr
		t.Slots.TpSetAttr = typeSetAttr
		t.Slots.MpSubscript = typeMpSubscript
		// Metaclasses inherit type.__call__ unless they override __call__ (synced below).
		if TypeType != nil && TypeType.Slots.TpCall != nil {
			t.Slots.TpCall = TypeType.Slots.TpCall
		}
	} else {
		isModuleSubclass := false
		if ModuleType != nil {
			for _, b := range t.MRO {
				if b == ModuleType {
					isModuleSubclass = true
					break
				}
			}
		}
		if isModuleSubclass {
			t.Slots.TpGetAttr = moduleGetAttr
			t.Slots.TpSetAttr = moduleSetAttr
		} else {
			t.Slots.TpGetAttr = objectGetAttr
			t.Slots.TpSetAttr = objectSetAttr
		}
	}
	t.syncDunderSlots()

	return t, nil
}

// MustNewType constructs a *PyType or panics if C3 MRO linearization fails.
func MustNewType(name string, bases []*PyType, dict *PyDict) *PyType {
	t, err := NewType(name, bases, dict)
	if err != nil {
		panic(err)
	}
	return t
}

// LookupMRO searches t.MRO in linearization order for internedName with 0 allocations.
func (t *PyType) LookupMRO(internedName Value) (Value, *PyType, bool) {
	for _, cls := range t.MRO {
		if cls.Dict != nil {
			if v, ok := cls.Dict.Get(internedName); ok && v.ptr != nil && !v.IsUnbound() {
				return v, cls, true
			}
		}
	}
	return NoneVal(), nil, false
}

// LookupMROAfter searches t.MRO strictly after startClass for super() resolution with 0 allocations.
func (t *PyType) LookupMROAfter(startClass *PyType, internedName Value) (Value, *PyType, bool) {
	mro := t.MRO
	startIdx := len(mro)
	for i, cls := range mro {
		if cls == startClass {
			startIdx = i + 1
			break
		}
	}
	for i := startIdx; i < len(mro); i++ {
		cls := mro[i]
		if cls.Dict != nil {
			if v, ok := cls.Dict.Get(internedName); ok && v.ptr != nil && !v.IsUnbound() {
				return v, cls, true
			}
		}
	}
	return NoneVal(), nil, false
}

// SetAttr sets or updates an attribute in t.Dict, marks custom data descriptors,
// and invalidates VersionTag + refreshes TypeSlots across t and all its subclasses.
func (t *PyType) SetAttr(name, value Value) {
	interned := InternValue(name)
	if value.Type() != nil && value.Type().Slots.TpDescrSet != nil {
		value.ptr.Flags |= FlagDataDescriptor
	}
	if t.Dict == nil {
		t.Dict = NewDictVal(8).AsDict()
	}
	t.Dict.Set(interned, value)
	t.InvalidateVersion()
}

// SetAttrStr is a convenience wrapper that interns name and calls SetAttr.
func (t *PyType) SetAttrStr(name string, value Value) {
	t.SetAttr(InternString(name), value)
}

// InvalidateVersion bumps t.VersionTag, re-synchronizes dunder TypeSlots on t,
// and recursively invalidates all subclasses of t.
func (t *PyType) InvalidateVersion() {
	t.VersionTag = allocTypeVersion()
	t.syncDunderSlots()
	subclassesMu.RLock()
	subs := append([]*PyType(nil), t.Subclasses...)
	subclassesMu.RUnlock()
	for _, sub := range subs {
		sub.InvalidateVersion()
	}
}

// subclassesMu guards every PyType.Subclasses slice; types are created
// concurrently when several VMs are instantiated at once.
var subclassesMu sync.RWMutex

// Dunder slot trampolines that dispatch through cached dunder callables on *PyType
// without runtime string or dictionary lookups.

func dunderNbAddSlot(ctx *ExecContext, a, b Value) Value {
	fn := a.Type().dunderAdd
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke1(fn, b)
	}
	return ctx.Invoke2(fn, a, b)
}

func dunderNbSubSlot(ctx *ExecContext, a, b Value) Value {
	fn := a.Type().dunderSub
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke1(fn, b)
	}
	return ctx.Invoke2(fn, a, b)
}

func dunderNbMulSlot(ctx *ExecContext, a, b Value) Value {
	fn := a.Type().dunderMul
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke1(fn, b)
	}
	return ctx.Invoke2(fn, a, b)
}

func dunderNbAbsSlot(ctx *ExecContext, a Value) Value {
	fn := a.Type().dunderAbs
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke0(fn)
	}
	return ctx.Invoke1(fn, a)
}

func dunderSqLenSlot(ctx *ExecContext, a Value) int64 {
	fn := a.Type().dunderLen
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke0(fn).AsInt64()
	}
	return ctx.Invoke1(fn, a).AsInt64()
}

func dunderGetItemSlot(ctx *ExecContext, a, b Value) Value {
	fn := a.Type().dunderGetItem
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke1(fn, b)
	}
	return ctx.Invoke2(fn, a, b)
}

func dunderSetItemSlot(ctx *ExecContext, a, b, c Value) Value {
	fn := a.Type().dunderSetItem
	if !fn.IsFunction() && !fn.IsBuiltinFunc() {
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			fn = descrGet(ctx, fn, a, a.Type())
		}
		return ctx.Invoke2(fn, b, c)
	}
	return ctx.Invoke3(fn, a, b, c)
}

func dunderTpCallSlot(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	dunder := callable.Type().dunderCall
	if ctx != nil && ctx.CallHook != nil && ctx.SP+1+len(args) <= len(ctx.Stack) {
		base := ctx.SP
		ctx.Stack[base] = callable
		copy(ctx.Stack[base+1:base+1+len(args)], args)
		return ctx.CallHook(ctx, dunder, base, 1+len(args), kwnames)
	}
	buf := make([]Value, 1+len(args))
	buf[0] = callable
	copy(buf[1:], args)
	if dunder.IsBuiltinFunc() {
		return dunder.AsBuiltinFunc().Fn(ctx, dunder, buf, kwnames)
	}
	return NoneVal()
}

func dunderTpDescrGetSlot(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	ownerVal := NoneVal()
	if owner != nil {
		ownerVal = TypeVal(owner)
	}
	return ctx.Invoke3(descr.Type().dunderGet, descr, obj, ownerVal)
}

func dunderTpDescrSetSlot(ctx *ExecContext, descr, obj, value Value) {
	if value.IsUnbound() {
		if delFn, _, ok := descr.Type().LookupMRO(InternString("__delete__")); ok {
			ctx.Invoke2(delFn, descr, obj)
			return
		}
		panic("val: AttributeError: __delete__")
	}
	if descr.Type().dunderSet.ptr == nil {
		panic("val: AttributeError: __set__")
	}
	ctx.Invoke3(descr.Type().dunderSet, descr, obj, value)
}

// syncDunderSlots populates t.Slots function pointers at class creation/mutation time
// by inspecting t.MRO for canonical dunder methods.
func (t *PyType) syncDunderSlots() {
	if DunderAdd.ptr == nil {
		return
	}

	if fn, _, ok := t.LookupMRO(DunderAdd); ok {
		t.dunderAdd = fn
		t.Slots.NbAdd = dunderNbAddSlot
	}
	if fn, _, ok := t.LookupMRO(DunderSub); ok {
		t.dunderSub = fn
		t.Slots.NbSub = dunderNbSubSlot
	}
	if fn, _, ok := t.LookupMRO(DunderMul); ok {
		t.dunderMul = fn
		t.Slots.NbMul = dunderNbMulSlot
	}
	if fn, _, ok := t.LookupMRO(DunderAbs); ok {
		t.dunderAbs = fn
		t.Slots.NbAbs = dunderNbAbsSlot
	}
	if fn, _, ok := t.LookupMRO(DunderLen); ok {
		t.dunderLen = fn
		t.Slots.SqLen = dunderSqLenSlot
	}
	if fn, _, ok := t.LookupMRO(DunderGetItem); ok {
		t.dunderGetItem = fn
		t.Slots.SqItem = dunderGetItemSlot
		t.Slots.MpSubscript = dunderGetItemSlot
	}
	if fn, _, ok := t.LookupMRO(DunderSetItem); ok {
		t.dunderSetItem = fn
		t.Slots.SqAssItem = dunderSetItemSlot
		t.Slots.MpAssSubscript = dunderSetItemSlot
	}
	if fn, _, ok := t.LookupMRO(DunderCall); ok {
		t.dunderCall = fn
		t.Slots.TpCall = dunderTpCallSlot
	}
	if fn, _, ok := t.LookupMRO(DunderGet); ok {
		t.dunderGet = fn
		t.Slots.TpDescrGet = dunderTpDescrGetSlot
	}
	if fn, _, ok := t.LookupMRO(DunderSet); ok {
		t.dunderSet = fn
		t.Slots.TpDescrSet = dunderTpDescrSetSlot
	} else if _, _, ok := t.LookupMRO(InternString("__delete__")); ok {
		t.dunderSet = Value{}
		t.Slots.TpDescrSet = dunderTpDescrSetSlot
	} else {
		t.dunderSet = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderInit); ok {
		t.dunderInit = fn
	} else {
		t.dunderInit = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderGetAttr); ok {
		t.dunderGetAttr = fn
	} else {
		t.dunderGetAttr = Value{}
	}
	if fn, owner, ok := t.LookupMRO(DunderSetAttr); ok && owner != ObjectType && owner != TypeType && owner != ModuleType {
		t.dunderSetAttr = fn
		t.Flags |= FlagCustomSetAttr
	} else {
		t.dunderSetAttr = Value{}
		t.Flags &= ^FlagCustomSetAttr
	}
	if fn, owner, ok := t.LookupMRO(DunderIter); ok {
		t.dunderIter = fn
		// Builtin collections publish __iter__ as a method that calls the
		// native slot. Replacing that slot with a wrapper around the method
		// recurses. Heap classes that define __iter__ still need the wrapper.
		if t.Slots.TpIter == nil || (owner == t && ((t.Flags&FlagHeapType) != 0 || fn.IsFunction())) {
			t.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
				return ctx.Invoke1(t.dunderIter, o)
			}
		}
	} else {
		t.dunderIter = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderNext); ok {
		t.dunderNext = fn
		t.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
			var res Value
			var exc Value
			hasExc := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						if v, ok := r.(Value); ok {
							exc = v
							hasExc = true
							return
						}
						panic(r)
					}
				}()
				res = ctx.Invoke1(t.dunderNext, o)
			}()
			if hasExc {
				if IsInstance(exc, StopIterationType) {
					return NoneVal(), IterExhausted
				}
				if ctx != nil {
					ctx.Raise(exc)
				}
				return NoneVal(), IterError
			}
			if ctx != nil && ctx.HasException() {
				exc = ctx.ActiveException
				ctx.ClearException()
				if IsInstance(exc, StopIterationType) {
					return NoneVal(), IterExhausted
				}
				ctx.Raise(exc)
				return NoneVal(), IterError
			}
			return res, IterYielded
		}
	} else {
		t.dunderNext = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderEnter); ok {
		t.dunderEnter = fn
	} else {
		t.dunderEnter = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderExit); ok {
		t.dunderExit = fn
	} else {
		t.dunderExit = Value{}
	}

	// Rich comparisons
	if fn, _, ok := t.LookupMRO(DunderLt); ok {
		t.dunderLt = fn
	} else {
		t.dunderLt = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderLe); ok {
		t.dunderLe = fn
	} else {
		t.dunderLe = Value{}
	}
	if fn, owner, ok := t.LookupMRO(DunderEq); ok && owner != ObjectType {
		t.dunderEq = fn
	} else {
		t.dunderEq = Value{}
	}
	if fn, owner, ok := t.LookupMRO(DunderNe); ok && owner != ObjectType {
		t.dunderNe = fn
	} else {
		t.dunderNe = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderGt); ok {
		t.dunderGt = fn
	} else {
		t.dunderGt = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderGe); ok {
		t.dunderGe = fn
	} else {
		t.dunderGe = Value{}
	}

	// Reflected arithmetic
	if fn, _, ok := t.LookupMRO(DunderRAdd); ok {
		t.dunderRAdd = fn
	} else {
		t.dunderRAdd = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRSub); ok {
		t.dunderRSub = fn
	} else {
		t.dunderRSub = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRMul); ok {
		t.dunderRMul = fn
	} else {
		t.dunderRMul = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRMatMul); ok {
		t.dunderRMatMul = fn
	} else {
		t.dunderRMatMul = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRTruediv); ok {
		t.dunderRTruediv = fn
	} else {
		t.dunderRTruediv = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRFloordiv); ok {
		t.dunderRFloordiv = fn
	} else {
		t.dunderRFloordiv = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRMod); ok {
		t.dunderRMod = fn
	} else {
		t.dunderRMod = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRPow); ok {
		t.dunderRPow = fn
	} else {
		t.dunderRPow = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRLShift); ok {
		t.dunderRLShift = fn
	} else {
		t.dunderRLShift = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRRShift); ok {
		t.dunderRRShift = fn
	} else {
		t.dunderRRShift = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRAnd); ok {
		t.dunderRAnd = fn
	} else {
		t.dunderRAnd = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderRXor); ok {
		t.dunderRXor = fn
	} else {
		t.dunderRXor = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderROr); ok {
		t.dunderROr = fn
	} else {
		t.dunderROr = Value{}
	}

	// In-place arithmetic
	if fn, _, ok := t.LookupMRO(DunderIAdd); ok {
		t.dunderIAdd = fn
	} else {
		t.dunderIAdd = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderISub); ok {
		t.dunderISub = fn
	} else {
		t.dunderISub = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIMul); ok {
		t.dunderIMul = fn
	} else {
		t.dunderIMul = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIMatMul); ok {
		t.dunderIMatMul = fn
	} else {
		t.dunderIMatMul = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderITruediv); ok {
		t.dunderITruediv = fn
	} else {
		t.dunderITruediv = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIFloordiv); ok {
		t.dunderIFloordiv = fn
	} else {
		t.dunderIFloordiv = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIMod); ok {
		t.dunderIMod = fn
	} else {
		t.dunderIMod = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIPow); ok {
		t.dunderIPow = fn
	} else {
		t.dunderIPow = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderILShift); ok {
		t.dunderILShift = fn
	} else {
		t.dunderILShift = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIRShift); ok {
		t.dunderIRShift = fn
	} else {
		t.dunderIRShift = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIAnd); ok {
		t.dunderIAnd = fn
	} else {
		t.dunderIAnd = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIXor); ok {
		t.dunderIXor = fn
	} else {
		t.dunderIXor = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIOr); ok {
		t.dunderIOr = fn
	} else {
		t.dunderIOr = Value{}
	}

	// Reflection, attributes & lifecycle
	if fn, owner, ok := t.LookupMRO(DunderGetAttribute); ok && owner != ObjectType && owner != ModuleType {
		t.dunderGetAttribute = fn
		t.Flags |= uint32(FlagCustomGetAttribute)
	} else {
		t.dunderGetAttribute = Value{}
		t.Flags &= ^uint32(FlagCustomGetAttribute)
	}
	if fn, owner, ok := t.LookupMRO(DunderDelAttr); ok && owner != ObjectType && owner != TypeType && owner != ModuleType {
		t.dunderDelAttr = fn
	} else {
		t.dunderDelAttr = Value{}
	}
	if fn, owner, ok := t.LookupMRO(InternString("__hash__")); ok && owner != ObjectType {
		t.dunderHash = fn
	} else {
		t.dunderHash = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderDir); ok {
		t.dunderDir = fn
	} else {
		t.dunderDir = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderContains); ok {
		t.dunderContains = fn
	} else {
		t.dunderContains = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderMissing); ok {
		t.dunderMissing = fn
	} else {
		t.dunderMissing = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderNew); ok {
		if fn.IsStaticMethod() {
			t.dunderNew = fn.AsStaticMethod().Callable
		} else {
			t.dunderNew = fn
		}
	} else {
		t.dunderNew = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderInitSubclass); ok {
		t.dunderInitSubclass = fn
	} else {
		t.dunderInitSubclass = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderSetName); ok {
		t.dunderSetName = fn
	} else {
		t.dunderSetName = Value{}
	}
}

func (t *PyType) DunderLtVal() Value { return t.dunderLt }
func (t *PyType) DunderLeVal() Value { return t.dunderLe }
func (t *PyType) DunderEqVal() Value { return t.dunderEq }
func (t *PyType) DunderNeVal() Value { return t.dunderNe }
func (t *PyType) DunderGtVal() Value { return t.dunderGt }
func (t *PyType) DunderGeVal() Value { return t.dunderGe }

func (t *PyType) DunderRAddVal() Value      { return t.dunderRAdd }
func (t *PyType) DunderRSubVal() Value      { return t.dunderRSub }
func (t *PyType) DunderRMulVal() Value      { return t.dunderRMul }
func (t *PyType) DunderRMatMulVal() Value   { return t.dunderRMatMul }
func (t *PyType) DunderRTruedivVal() Value  { return t.dunderRTruediv }
func (t *PyType) DunderRFloordivVal() Value { return t.dunderRFloordiv }
func (t *PyType) DunderRModVal() Value      { return t.dunderRMod }
func (t *PyType) DunderRPowVal() Value      { return t.dunderRPow }
func (t *PyType) DunderRLShiftVal() Value   { return t.dunderRLShift }
func (t *PyType) DunderRRShiftVal() Value   { return t.dunderRRShift }
func (t *PyType) DunderRAndVal() Value      { return t.dunderRAnd }
func (t *PyType) DunderRXorVal() Value      { return t.dunderRXor }
func (t *PyType) DunderROrVal() Value       { return t.dunderROr }

func (t *PyType) DunderIAddVal() Value      { return t.dunderIAdd }
func (t *PyType) DunderISubVal() Value      { return t.dunderISub }
func (t *PyType) DunderIMulVal() Value      { return t.dunderIMul }
func (t *PyType) DunderIMatMulVal() Value   { return t.dunderIMatMul }
func (t *PyType) DunderITruedivVal() Value  { return t.dunderITruediv }
func (t *PyType) DunderIFloordivVal() Value { return t.dunderIFloordiv }
func (t *PyType) DunderIModVal() Value      { return t.dunderIMod }
func (t *PyType) DunderIPowVal() Value      { return t.dunderIPow }
func (t *PyType) DunderILShiftVal() Value   { return t.dunderILShift }
func (t *PyType) DunderIRShiftVal() Value   { return t.dunderIRShift }
func (t *PyType) DunderIAndVal() Value      { return t.dunderIAnd }
func (t *PyType) DunderIXorVal() Value      { return t.dunderIXor }
func (t *PyType) DunderIOrVal() Value       { return t.dunderIOr }

func (t *PyType) DunderGetAttributeVal() Value { return t.dunderGetAttribute }
func (t *PyType) DunderDelAttrVal() Value      { return t.dunderDelAttr }
func (t *PyType) DunderDirVal() Value          { return t.dunderDir }
func (t *PyType) DunderContainsVal() Value     { return t.dunderContains }
func (t *PyType) DunderMissingVal() Value      { return t.dunderMissing }
func (t *PyType) DunderNewVal() Value          { return t.dunderNew }
func (t *PyType) DunderInitSubclassVal() Value { return t.dunderInitSubclass }
func (t *PyType) DunderSetNameVal() Value      { return t.dunderSetName }

// DunderEnterVal returns the cached __enter__ callable for t.
func (t *PyType) DunderEnterVal() Value {
	return t.dunderEnter
}

// DunderExitVal returns the cached __exit__ callable for t.
func (t *PyType) DunderExitVal() Value {
	return t.dunderExit
}

// IsSubclass reports whether cls is a subclass of base by checking base in cls.MRO.
func IsSubclass(cls, base *PyType) bool {
	if cls == nil || base == nil {
		return false
	}
	for _, m := range cls.MRO {
		if m == base {
			return true
		}
	}
	return false
}

// IsInstance reports whether v is an instance of cls or any of its subclasses.
func IsInstance(v Value, cls *PyType) bool {
	return IsSubclass(v.Type(), cls)
}

// HasInit reports whether t defines or inherits an __init__ method.
func (t *PyType) HasInit() bool {
	return t.dunderInit.ptr != nil && t.dunderInit.ptr != ObjectInitBuiltin.ptr
}

// DunderInitVal returns the cached __init__ callable for t.
func (t *PyType) DunderInitVal() Value {
	return t.dunderInit
}

// objectGetAttr implements the standard Python object.__getattribute__ protocol,
// enforcing data descriptor precedence -> instance Shape slots -> non-data descriptors.
func objectGetAttr(ctx *ExecContext, obj, name Value) Value {
	tp := obj.Type()
	if tp.dunderGetAttribute.ptr != nil {
		return ctx.Invoke2(tp.dunderGetAttribute, obj, name)
	}
	return defaultObjectGetAttr(ctx, obj, name)
}

func defaultObjectGetAttr(ctx *ExecContext, obj, name Value) Value {
	interned := InternValue(name)
	tp := obj.Type()

	if interned.AsString() == "__class__" {
		return TypeVal(tp)
	}

	descr, _, foundInClass := tp.LookupMRO(interned)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			if descrType.Slots.TpDescrGet != nil {
				return descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
			}
		}
	}

	if obj.IsInstance() {
		if inst := obj.AsInstance(); inst.Shape != nil {
			if val, ok := inst.GetAttr(interned); ok {
				return val
			}
		}
	}

	if obj.IsModule() {
		mod := obj.AsModule()
		if interned.AsString() == "__dict__" {
			return DictVal(mod.Dict)
		}
		if v, ok := mod.Dict.Get(interned); ok {
			if v.IsLazyImport() && ResolveLazyImportHook != nil {
				var vm any
				if ctx != nil {
					vm = ctx.VM
				}
				resolved, err := ResolveLazyImportHook(vm, mod, interned, v)
				if err != nil {
					panic("ImportError: " + err.Error())
				}
				return resolved
			}
			return v
		}
		switch interned.AsString() {
		case "__name__":
			return InternString(mod.Name)
		case "__doc__":
			return NoneVal()
		case "__file__":
			if mod.File != "" {
				return NewStringVal(mod.File)
			}
		case "__annotations__":
			return ModuleAnnotations(ctx, mod)
		case "__annotate__":
			return NoneVal()
		}
	}

	if obj.IsDict() && tp != DictType {
		if d := obj.AsDict(); d.Attrs != nil {
			if val, ok := d.Attrs.Get(interned); ok {
				return val
			}
		}
	}

	if obj.IsList() && tp != ListType {
		if l := obj.AsList(); l.Attrs != nil {
			if val, ok := l.Attrs.Get(interned); ok {
				return val
			}
		}
	}

	if obj.IsTuple() && tp != TupleType {
		if t := obj.AsTuple(); t.Attrs != nil {
			if val, ok := t.Attrs.Get(interned); ok {
				return val
			}
		}
	}

	if k := obj.Kind(); k == KindFuture || k == KindTask {
		if d := AsyncAttrs(obj, false); d != nil {
			if val, ok := d.Get(interned); ok {
				return val
			}
		}
	}

	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			return descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
		}
		return descr
	}

	if interned.AsString() == "__dict__" && obj.IsInstance() && (tp.Flags&FlagForbidDynamicAttrs) == 0 {
		inst := obj.AsInstance()
		if inst.Shape != nil {
			return NewInstanceDictProxy(inst)
		}
	}

	if tp.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(tp.dunderGetAttr, obj, interned)
	}

	if interned.AsString() == "__func__" || interned.AsString() == "__wrapped__" {
		if obj.Kind() == KindStaticMethod {
			return obj.AsStaticMethod().Callable
		}
		if obj.Kind() == KindClassMethod {
			return obj.AsClassMethod().Callable
		}
		if obj.Kind() == KindBoundMethod {
			return obj.AsBoundMethod().Func
		}
	}

	if obj.Kind() == KindBoundMethod {
		if interned.AsString() == "__self__" {
			return obj.AsBoundMethod().Self
		}
		bm := obj.AsBoundMethod()
		if bm.Func.HeaderPtr() != nil {
			return defaultObjectGetAttr(ctx, bm.Func, name)
		}
	}

	if interned.AsString() == "__dict__" {
		if obj.IsDict() && tp != DictType {
			d := obj.AsDict()
			if d.Attrs == nil {
				d.Attrs = NewDictVal(8).AsDict()
			}
			return Value{num: uint64(KindDict), ptr: &d.Attrs.Header}
		}
		if obj.IsList() && tp != ListType {
			l := obj.AsList()
			if l.Attrs == nil {
				l.Attrs = NewDictVal(8).AsDict()
			}
			return Value{num: uint64(KindDict), ptr: &l.Attrs.Header}
		}
		if obj.IsTuple() && tp != TupleType {
			t := obj.AsTuple()
			if t.Attrs == nil {
				t.Attrs = NewDictVal(8).AsDict()
			}
			return Value{num: uint64(KindDict), ptr: &t.Attrs.Header}
		}
	}

	if interned.AsString() == "__annotations__" {
		if obj.IsType() {
			return TypeAnnotations(ctx, obj.AsType())
		}
		if obj.IsInstance() {
			inst := obj.AsInstance()
			if annotateFn, ok := inst.GetAttr(InternString("__annotate__")); ok {
				var annDict Value
				if ctx != nil && ctx.CallHook != nil {
					base := ctx.SP
					ctx.Stack[base] = Int64Val(1)
					annDict = ctx.CallHook(ctx, annotateFn, base, 1, nil)
				}
				if annDict.IsDict() {
					inst.SetAttr(interned, annDict)
					return annDict
				}
			}
			emptyDict := NewDictVal(4)
			inst.SetAttr(interned, emptyDict)
			return emptyDict
		}
	}

	if interned.AsString() == "__doc__" {
		return NoneVal()
	}

	panic("val: AttributeError: '" + tp.Name + "' object has no attribute '" + interned.AsString() + "'")
}

// objectSetAttr implements the standard Python object.__setattr__ protocol,
// enforcing data descriptor (__set__) precedence before transitioning instance Shape slots.
func objectSetAttr(ctx *ExecContext, obj, name, value Value) Value {
	interned := InternValue(name)
	tp := obj.Type()

	if tp.dunderSetAttr.ptr != nil {
		ctx.Invoke3(tp.dunderSetAttr, obj, interned, value)
		return NoneVal()
	}

	if obj.IsInstance() && obj.AsInstance().Shape != nil && interned.AsString() == "__class__" {
		AssignInstanceClass(obj, value)
		return NoneVal()
	}

	if descr, _, foundInClass := tp.LookupMRO(interned); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			descrType.Slots.TpDescrSet(ctx, descr, obj, value)
			return NoneVal()
		}
	}

	if obj.IsInstance() && obj.AsInstance().Shape != nil {
		if interned.AsString() == "__dict__" && (tp.Flags&FlagForbidDynamicAttrs) == 0 {
			ReplaceInstanceDict(ctx, obj.AsInstance(), value)
			return NoneVal()
		}
		obj.AsInstance().SetAttr(interned, value)
		return NoneVal()
	}

	if obj.IsModule() {
		if interned.AsString() == "__class__" {
			AssignInstanceClass(obj, value)
			return NoneVal()
		}
		mod := obj.AsModule()
		if interned.AsString() == "__dict__" {
			if !value.IsDict() {
				panic("TypeError: __dict__ must be set to a dictionary, not a '" + value.Type().Name + "'")
			}
			mod.Dict = value.AsDict()
			return NoneVal()
		}
		mod.Dict.Set(interned, value)
		return NoneVal()
	}

	if obj.IsDict() && tp != DictType {
		d := obj.AsDict()
		if d.Attrs == nil {
			d.Attrs = NewDictVal(8).AsDict()
		}
		d.Attrs.Set(interned, value)
		return NoneVal()
	}

	if obj.IsList() && tp != ListType {
		l := obj.AsList()
		if l.Attrs == nil {
			l.Attrs = NewDictVal(8).AsDict()
		}
		l.Attrs.Set(interned, value)
		return NoneVal()
	}

	if obj.IsTuple() && tp != TupleType {
		t := obj.AsTuple()
		if t.Attrs == nil {
			t.Attrs = NewDictVal(8).AsDict()
		}
		t.Attrs.Set(interned, value)
		return NoneVal()
	}

	if k := obj.Kind(); k == KindFuture || k == KindTask {
		AsyncAttrs(obj, true).Set(interned, value)
		return NoneVal()
	}

	if obj.IsType() {
		cls := obj.AsType()
		if cls.Dict == nil {
			cls.Dict = NewDictVal(16).AsDict()
		}
		cls.Dict.Set(interned, value)
		cls.syncDunderSlots()
		return NoneVal()
	}

	panic("val: AttributeError: '" + tp.Name + "' object cannot set attribute '" + interned.AsString() + "'")
}

// typeGetAttr implements attribute lookup on a *PyType object itself.
func typeGetAttr(ctx *ExecContext, typeObj, name Value) Value {
	cls := typeObj.AsType()
	interned := InternValue(name)
	meta := cls.Header.Type
	if meta == nil {
		meta = TypeType
	}

	// 1. Check metaclass MRO for data descriptors
	if descr, _, ok := meta.LookupMRO(interned); ok {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			if descrType.Slots.TpDescrGet != nil {
				return descrType.Slots.TpDescrGet(ctx, descr, typeObj, meta)
			}
		}
	}

	// __doc__ on a class is never inherited from parent classes via MRO.
	if interned.AsString() == "__doc__" {
		if cls.Dict != nil {
			if descr, ok := cls.Dict.Get(interned); ok {
				if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
					return descrGet(ctx, descr, NoneVal(), cls)
				}
				return descr
			}
		}
		return NoneVal()
	}

	// 2. Check class MRO
	if descr, _, ok := cls.LookupMRO(interned); ok {
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			return descrGet(ctx, descr, NoneVal(), cls)
		}
		return descr
	}

	// 3. Check metaclass MRO for non-data descriptors and class attributes
	if descr, _, ok := meta.LookupMRO(interned); ok {
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			return descrGet(ctx, descr, typeObj, meta)
		}
		return descr
	}

	// 4. Builtin type reflection attributes
	switch interned.AsString() {
	case "__class__":
		return TypeVal(meta)
	case "__bases__":
		basesItems := make([]Value, len(cls.Bases))
		for i, b := range cls.Bases {
			basesItems[i] = TypeVal(b)
		}
		return NewTupleVal(basesItems...)
	case "__name__":
		return InternString(cls.Name)
	case "__qualname__":
		if qn, ok := cls.Dict.Get(InternString("__qualname__")); ok {
			return qn
		}
		return InternString(cls.Name)
	case "__type_params__":
		if tp, ok := cls.Dict.Get(InternString("__type_params__")); ok {
			return tp
		}
		return NewTupleVal()
	case "__module__":
		if modVal, ok := cls.Dict.Get(InternString("__module__")); ok {
			return modVal
		}
		return InternString("builtins")
	case "__dict__":
		return NewMappingProxyVal(ObjectVal(&cls.Dict.Header))
	case "__mro__":
		mroItems := make([]Value, len(cls.MRO))
		for i, m := range cls.MRO {
			mroItems[i] = TypeVal(m)
		}
		return NewTupleVal(mroItems...)
	}

	// 5. Fallback to metaclass __getattr__ if defined
	if meta.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(meta.dunderGetAttr, typeObj, interned)
	}

	panic("val: AttributeError: type object '" + cls.Name + "' has no attribute '" + interned.AsString() + "'")
}

// typeSetAttr implements attribute assignment on a *PyType object, invalidating VersionTag.
func typeSetAttr(ctx *ExecContext, typeObj, name, value Value) Value {
	cls := typeObj.AsType()
	interned := InternValue(name)
	meta := cls.Header.Type
	if meta == nil {
		meta = TypeType
	}
	if meta != TypeType && meta.dunderSetAttr.ptr != nil {
		ctx.Invoke3(meta.dunderSetAttr, typeObj, interned, value)
		return NoneVal()
	}
	if descr, _, ok := meta.LookupMRO(interned); ok {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			descrType.Slots.TpDescrSet(ctx, descr, typeObj, value)
			return NoneVal()
		}
	}
	cls.SetAttr(interned, value)
	return NoneVal()
}

// objectDelAttr implements the standard Python attribute deletion protocol,
// dispatching through __delattr__ and data descriptor __delete__ before removing slots.
func objectDelAttr(ctx *ExecContext, obj, name Value) {
	interned := InternValue(name)
	tp := obj.Type()

	if obj.IsType() {
		cls := obj.AsType()
		meta := cls.Header.Type
		if meta == nil {
			meta = TypeType
		}
		if meta != TypeType && meta.dunderDelAttr.ptr != nil {
			ctx.Invoke2(meta.dunderDelAttr, obj, interned)
			return
		}
		if descr, _, ok := meta.LookupMRO(interned); ok {
			if descrSet := descr.Type().Slots.TpDescrSet; descrSet != nil {
				descrSet(ctx, descr, obj, UnboundVal())
				return
			}
		}
		if cls.Dict != nil && cls.Dict.Delete(interned) {
			cls.InvalidateVersion()
			return
		}
		panic(NewAttributeError("type object '" + cls.Name + "' has no attribute '" + interned.AsString() + "'"))
	}

	if tp.dunderDelAttr.ptr != nil {
		ctx.Invoke2(tp.dunderDelAttr, obj, interned)
		return
	}

	if descr, _, foundInClass := tp.LookupMRO(interned); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			descrType.Slots.TpDescrSet(ctx, descr, obj, UnboundVal())
			return
		}
	}

	if obj.IsInstance() {
		if !obj.AsInstance().DeleteAttr(interned) {
			panic(NewAttributeError("'" + tp.Name + "' object has no attribute '" + interned.AsString() + "'"))
		}
		return
	}

	if obj.IsFunction() {
		fn := obj.AsFunction()
		if fn.Dict != nil && fn.Dict.Delete(interned) {
			return
		}
		panic(NewAttributeError("'" + tp.Name + "' object has no attribute '" + interned.AsString() + "'"))
	}

	if obj.IsModule() {
		if !obj.AsModule().Dict.Delete(interned) {
			panic(NewAttributeError("module '" + obj.AsModule().Name + "' has no attribute '" + interned.AsString() + "'"))
		}
		return
	}

	if obj.IsDict() && obj.AsDict().Attrs != nil && obj.AsDict().Attrs.Delete(interned) {
		return
	}

	if obj.IsList() && obj.AsList().Attrs != nil && obj.AsList().Attrs.Delete(interned) {
		return
	}

	if d := AsyncAttrs(obj, false); d != nil && d.Delete(interned) {
		return
	}

	panic(NewAttributeError("'" + tp.Name + "' object has no attribute '" + interned.AsString() + "'"))
}

// moduleGetAttr implements attribute lookup on a *PyModule object.
func moduleGetAttr(ctx *ExecContext, obj, name Value) Value {
	if obj.Kind() != KindModule {
		return objectGetAttr(ctx, obj, name)
	}
	tp := obj.Type()
	interned := InternValue(name)
	if tp.dunderGetAttribute.ptr != nil && tp != ModuleType {
		return ctx.Invoke2(tp.dunderGetAttribute, obj, interned)
	}
	return defaultModuleGetAttr(ctx, obj, interned)
}

func defaultModuleGetAttr(ctx *ExecContext, obj, interned Value) Value {
	mod := obj.AsModule()
	tp := obj.Type()
	if interned.AsString() == "__class__" {
		return TypeVal(tp)
	}

	// 1. Data descriptors on class MRO
	descr, _, foundInClass := tp.LookupMRO(interned)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			if descrType.Slots.TpDescrGet != nil {
				return descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
			}
		}
	}

	// 2. Module dict
	if v, ok := mod.Dict.Get(interned); ok {
		if v.IsLazyImport() && ResolveLazyImportHook != nil {
			var vm any
			if ctx != nil {
				vm = ctx.VM
			}
			resolved, err := ResolveLazyImportHook(vm, mod, interned, v)
			if err != nil {
				panic("ImportError: " + err.Error())
			}
			return resolved
		}
		return v
	}

	if interned.AsString() == "__dict__" {
		return DictVal(mod.Dict)
	}

	// 3. Non-data descriptors on class MRO
	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			return descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
		}
		return descr
	}

	// 4. Built-in module reflection attributes
	switch interned.AsString() {
	case "__name__":
		return InternString(mod.Name)
	case "__doc__":
		return NoneVal()
	case "__file__":
		if mod.File != "" {
			return NewStringVal(mod.File)
		}
	case "__annotations__":
		return ModuleAnnotations(ctx, mod)
	case "__annotate__":
		return NoneVal()
	}

	// 5. Class-level __getattr__
	if tp.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(tp.dunderGetAttr, obj, interned)
	}

	// 6. PEP 562 module-level __getattr__
	if interned != DunderGetAttr {
		if getattrFn, ok := mod.Dict.Get(DunderGetAttr); ok && !getattrFn.IsNone() && !getattrFn.IsUnbound() {
			return ctx.Invoke1(getattrFn, interned)
		}
	}

	panic("val: AttributeError: module '" + mod.Name + "' has no attribute '" + interned.AsString() + "'")
}

// moduleSetAttr implements attribute assignment on a *PyModule object.
func moduleSetAttr(ctx *ExecContext, obj, name, value Value) Value {
	if obj.Kind() != KindModule {
		return objectSetAttr(ctx, obj, name, value)
	}
	interned := InternValue(name)
	tp := obj.Type()
	if tp.dunderSetAttr.ptr != nil && tp != ModuleType {
		ctx.Invoke3(tp.dunderSetAttr, obj, interned, value)
		return NoneVal()
	}
	if interned.AsString() == "__class__" {
		AssignInstanceClass(obj, value)
		return NoneVal()
	}
	if descr, _, foundInClass := tp.LookupMRO(interned); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			descrType.Slots.TpDescrSet(ctx, descr, obj, value)
			return NoneVal()
		}
	}
	if interned.AsString() == "__dict__" {
		if !value.IsDict() {
			panic("TypeError: __dict__ must be set to a dictionary, not a '" + value.Type().Name + "'")
		}
		obj.AsModule().Dict = value.AsDict()
		return NoneVal()
	}
	obj.AsModule().Dict.Set(interned, value)
	return NoneVal()
}

// ReplaceInstanceDict implements `obj.__dict__ = mapping`: it tombstones the
// instance's dynamic attributes and re-populates them from mapping through the
// regular Shape transitions, keeping the instance Shape-backed.
func ReplaceInstanceDict(ctx *ExecContext, inst *PyInstance, mapping Value) {
	if !mapping.IsDict() {
		panic("TypeError: __dict__ must be set to a dictionary, not a '" + mapping.Type().Name + "'")
	}
	entries := mapping.AsDict().ActiveEntries()
	tp := inst.Header.Type
	inst.mu.RLock()
	names := make([]Value, 0, len(inst.Shape.Attrs))
	for _, a := range inst.Shape.Attrs {
		names = append(names, a.Name)
	}
	inst.mu.RUnlock()
	for _, n := range names {
		if tp != nil {
			if _, _, isClassAttr := tp.LookupMRO(n); isClassAttr {
				continue
			}
		}
		inst.DeleteAttr(n)
	}
	for _, e := range entries {
		inst.SetAttr(InternValue(e.Key), e.Value)
	}
}

// AssignInstanceClass implements `obj.__class__ = cls` for Shape-backed
// instances by replaying the instance's attributes onto the new class's root
// shape, so slot layouts and inline caches stay consistent.
func AssignInstanceClass(obj, value Value) {
	if !value.IsType() {
		panic("TypeError: __class__ must be set to a class, not '" + value.Type().Name + "' object")
	}
	newT := value.AsType()
	if obj.IsModule() {
		if ModuleType == nil || !IsSubclass(newT, ModuleType) {
			panic("TypeError: __class__ assignment only supported for ModuleType subclasses")
		}
		obj.AsModule().Header.Type = newT
		return
	}
	old := obj.AsInstance()
	fresh := NewInstanceOfType(newT)
	if !fresh.IsInstance() {
		panic("TypeError: __class__ assignment only supported for heap types or ModuleType subclasses")
	}
	fi := fresh.AsInstance()
	old.mu.RLock()
	attrs := append([]ShapeAttr(nil), old.Shape.Attrs...)
	slots := append([]Value(nil), old.Slots...)
	old.mu.RUnlock()
	for _, a := range attrs {
		if a.Index < len(slots) && slots[a.Index].ptr != nil {
			fi.SetAttr(a.Name, slots[a.Index])
		}
	}
	old.mu.Lock()
	old.Shape = fi.Shape
	old.Slots = fi.Slots
	old.Header.Type = newT
	old.mu.Unlock()
}
