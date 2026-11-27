package val

import (
	"errors"
	"slices"
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
	Stack               []Value
	SP                  int
	CallHook            func(ctx *ExecContext, callable Value, argBase int, nargs int, kwnames []Value) Value
	ActiveException     Value
	VM                  any
	ThreadID            int64
	LazyAttrErrFreeList []*PyInstance
}

// AcquireExecContextHook allows standalone comparison/hashing helpers in pkg/val
// to obtain a temporary VM ExecContext when invoked without an explicit ctx.
var AcquireExecContextHook func() (*ExecContext, func())

// AcquireExecContextDirectHook and ReleaseExecContextDirectHook provide a
// zero-allocation pair for acquiring and releasing a pooled VM ExecContext
// without allocating a release closure.
var (
	AcquireExecContextDirectHook func() *ExecContext
	ReleaseExecContextDirectHook func(*ExecContext)
)

// invokeArgBufPool recycles small argument buffers for invoke1Slow/invoke2Slow/
// invoke3Slow when ctx == nil, avoiding per-call heap slice allocations.
var invokeArgBufPool = sync.Pool{
	New: func() any { return new([3]Value) },
}

// ResolveLazyImportHook allows resolving a PyLazyImport placeholder through the VM.
var ResolveLazyImportHook func(vm any, mod *PyModule, name Value, lazyVal Value) (Value, error)

// JoinGeneratorHook drains a generator directly into a separator-joined
// string without per-yield call overhead. It returns (result, true) when
// handled (including when an exception was set on ctx), or (Value{}, false)
// to fall back to the default send loop.
var JoinGeneratorHook func(ctx *ExecContext, sep string, gen *PyGenerator) (Value, bool)

// DrainGeneratorHook drains all yielded values from a generator in a single
// VM entry without per-yield Send/runFrame re-entry overhead. Returns
// (items, true) on completion or (nil, false) when an exception is raised on
// ctx or fallback is required.
var DrainGeneratorHook func(ctx *ExecContext, gen *PyGenerator) ([]Value, bool)

func (ctx *ExecContext) Raise(exc Value) {
	if ctx == nil {
		panic(exc)
	}
	ctx.ActiveException = exc
}

func (ctx *ExecContext) HasException() bool {
	return ctx != nil && ctx.ActiveException.ptr != nil && ctx.ActiveException.ptr != &tagNone
}

func (ctx *ExecContext) ClearException() {
	if ctx != nil {
		ctx.ActiveException = NoneVal()
	}
}

// ensureStack grows ctx.Stack in place when fewer than minCap slots remain.
func (ctx *ExecContext) ensureStack(minCap int) {
	if minCap <= len(ctx.Stack) {
		return
	}
	newCap := len(ctx.Stack) * 2
	if newCap < minCap {
		newCap = minCap + 256
	}
	ns := make([]Value, newCap)
	copy(ns, ctx.Stack)
	ctx.Stack = ns
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
	return RaiseTypeError(ctx, "'"+callable.Type().Name+"' object is not callable")
}

// Invoke1 invokes callable with 1 positional argument (a) with 0 heap allocations.
func (ctx *ExecContext) Invoke1(callable, a Value) Value {
	if ctx != nil {
		if ctx.SP+1 > len(ctx.Stack) && ctx.CallHook != nil {
			ctx.ensureStack(ctx.SP + 1)
		}
		if ctx.SP+1 <= len(ctx.Stack) {
			base := ctx.SP
			ctx.Stack[base] = a
			if callable.IsBuiltinFunc() {
				return callable.AsBuiltinFunc().Fn(ctx, callable, ctx.Stack[base:base+1], nil)
			}
			if ctx.CallHook != nil {
				return ctx.CallHook(ctx, callable, base, 1, nil)
			}
		}
	}
	return ctx.invoke1Slow(callable, a)
}

//go:noinline
func (ctx *ExecContext) invoke1Slow(callable, a Value) Value {
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) {
		if AcquireExecContextDirectHook != nil && ReleaseExecContextDirectHook != nil {
			tmpCtx := AcquireExecContextDirectHook()
			res := tmpCtx.Invoke1(callable, a)
			ReleaseExecContextDirectHook(tmpCtx)
			return res
		}
		if AcquireExecContextHook != nil {
			tmpCtx, release := AcquireExecContextHook()
			defer release()
			return tmpCtx.Invoke1(callable, a)
		}
	}
	buf := invokeArgBufPool.Get().(*[3]Value)
	buf[0] = a
	var res Value
	if callable.IsBuiltinFunc() {
		res = callable.AsBuiltinFunc().Fn(ctx, callable, buf[:1], nil)
	} else if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		res = tpCall(ctx, callable, buf[:1], nil)
	} else {
		buf[0] = Value{}
		invokeArgBufPool.Put(buf)
		return RaiseTypeError(ctx, "'"+callable.Type().Name+"' object is not callable")
	}
	buf[0] = Value{}
	invokeArgBufPool.Put(buf)
	return res
}

// Invoke2 invokes callable with 2 positional arguments (a, b) with 0 heap allocations.
func (ctx *ExecContext) Invoke2(callable, a, b Value) Value {
	if ctx != nil {
		if ctx.SP+2 > len(ctx.Stack) && ctx.CallHook != nil {
			ctx.ensureStack(ctx.SP + 2)
		}
		if ctx.SP+2 <= len(ctx.Stack) {
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
	}
	return ctx.invoke2Slow(callable, a, b)
}

//go:noinline
func (ctx *ExecContext) invoke2Slow(callable, a, b Value) Value {
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) {
		if AcquireExecContextDirectHook != nil && ReleaseExecContextDirectHook != nil {
			tmpCtx := AcquireExecContextDirectHook()
			res := tmpCtx.Invoke2(callable, a, b)
			ReleaseExecContextDirectHook(tmpCtx)
			return res
		}
		if AcquireExecContextHook != nil {
			tmpCtx, release := AcquireExecContextHook()
			defer release()
			return tmpCtx.Invoke2(callable, a, b)
		}
	}
	buf := invokeArgBufPool.Get().(*[3]Value)
	buf[0] = a
	buf[1] = b
	var res Value
	if callable.IsBuiltinFunc() {
		res = callable.AsBuiltinFunc().Fn(ctx, callable, buf[:2], nil)
	} else if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		res = tpCall(ctx, callable, buf[:2], nil)
	} else {
		buf[0] = Value{}
		buf[1] = Value{}
		invokeArgBufPool.Put(buf)
		return RaiseTypeError(ctx, "'"+callable.Type().Name+"' object is not callable")
	}
	buf[0] = Value{}
	buf[1] = Value{}
	invokeArgBufPool.Put(buf)
	return res
}

// Invoke3 invokes callable with 3 positional arguments (a, b, c) with 0 heap allocations.
func (ctx *ExecContext) Invoke3(callable, a, b, c Value) Value {
	if ctx != nil {
		if ctx.SP+3 > len(ctx.Stack) && ctx.CallHook != nil {
			ctx.ensureStack(ctx.SP + 3)
		}
		if ctx.SP+3 <= len(ctx.Stack) {
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
	}
	return ctx.invoke3Slow(callable, a, b, c)
}

//go:noinline
func (ctx *ExecContext) invoke3Slow(callable, a, b, c Value) Value {
	if !callable.IsBuiltinFunc() && (ctx == nil || ctx.CallHook == nil) {
		if AcquireExecContextDirectHook != nil && ReleaseExecContextDirectHook != nil {
			tmpCtx := AcquireExecContextDirectHook()
			res := tmpCtx.Invoke3(callable, a, b, c)
			ReleaseExecContextDirectHook(tmpCtx)
			return res
		}
		if AcquireExecContextHook != nil {
			tmpCtx, release := AcquireExecContextHook()
			defer release()
			return tmpCtx.Invoke3(callable, a, b, c)
		}
	}
	buf := invokeArgBufPool.Get().(*[3]Value)
	buf[0] = a
	buf[1] = b
	buf[2] = c
	var res Value
	if callable.IsBuiltinFunc() {
		res = callable.AsBuiltinFunc().Fn(ctx, callable, buf[:3], nil)
	} else if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
		res = tpCall(ctx, callable, buf[:3], nil)
	} else {
		buf[0] = Value{}
		buf[1] = Value{}
		buf[2] = Value{}
		invokeArgBufPool.Put(buf)
		return RaiseTypeError(ctx, "'"+callable.Type().Name+"' object is not callable")
	}
	buf[0] = Value{}
	buf[1] = Value{}
	buf[2] = Value{}
	invokeArgBufPool.Put(buf)
	return res
}

// Call invokes callable with arbitrary positional arguments and keyword names.
func (ctx *ExecContext) Call(callable Value, args []Value, kwnames []Value) Value {
	if ctx != nil && ctx.CallHook != nil {
		if ctx.SP+len(args) > len(ctx.Stack) {
			ctx.ensureStack(ctx.SP + len(args))
		}
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
	return RaiseTypeError(ctx, "'"+callable.Type().Name+"' object is not callable")
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

// BufferFlags for TpAsBuffer / GetBuffer (PEP 3118 / CPython bufferinfo).
const (
	PyBUF_SIMPLE         = 0
	PyBUF_WRITABLE       = 0x0001
	PyBUF_FORMAT         = 0x0004
	PyBUF_ND             = 0x0008
	PyBUF_STRIDES        = 0x0010 | PyBUF_ND
	PyBUF_C_CONTIGUOUS   = 0x0020 | PyBUF_STRIDES
	PyBUF_F_CONTIGUOUS   = 0x0040 | PyBUF_STRIDES
	PyBUF_ANY_CONTIGUOUS = 0x0080 | PyBUF_STRIDES
	PyBUF_INDIRECT       = 0x0100 | PyBUF_STRIDES
	PyBUF_FULL_RO        = PyBUF_INDIRECT | 0x0200
	PyBUF_FULL           = PyBUF_INDIRECT | PyBUF_WRITABLE
	PyBUF_RECORDS_RO     = PyBUF_STRIDES | PyBUF_FORMAT | PyBUF_WRITABLE // compat alias
)

// PyBuffer represents a zero-allocation direct memory view into an underlying binary object.
// PEP 3118: Format, NDim, Shape, Strides, Suboffsets describe typed, multi-dimensional data.
// Release is called when the C side releases the view; Unpin undoes pinning.
type PyBuffer struct {
	Buf        []byte
	ReadOnly   bool
	ItemSize   int
	Obj        Value
	Format     string
	NDim       int
	Shape      []int
	Strides    []int
	Suboffsets []int
	Release    func()
	// Pinner pins Buf backing array for zero-copy C access; nil when off-heap.
	Pinner *PinnerState
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
	// Extended slots for C extension types (PyType_FromSpec / static PyTypeObject).
	TpNew         VectorcallFunc
	TpInit        VectorcallFunc
	TpDealloc     func(ctx *ExecContext, v Value)
	TpRepr        UnaryOpFunc
	TpStr         UnaryOpFunc
	TpHash        UnaryOpFunc
	TpRichCompare func(ctx *ExecContext, a, b Value, op int) Value
	NbBool        UnaryOpFunc
	SqContains    BinaryOpFunc
	SqConcat      BinaryOpFunc
	SqRepeat      BinaryOpFunc
	MpLen         LenFunc
}

// PyType represents a Python type/class object with CPython-style TypeSlots,
// C3 MRO linearization, and monotonic VersionTag invalidation.
// Header MUST be at byte offset 0.
type PyType struct {
	Header        ObjectHeader
	Name          string
	Bases         []*PyType
	MRO           []*PyType
	Subclasses    []*PyType
	Dict          *PyDict
	RootShape     *Shape
	VersionTag    uint64
	Flags         uint32
	likelySlotCap atomic.Uint32
	Slots         TypeSlots
	// UsesDefaultNew reports whether instantiation may skip a custom
	// __new__: the resolved __new__ is absent or object.__new__. The
	// metaclass gate is checked live at call sites, so this flag only
	// tracks __new__ and stays fresh via syncDunderSlots.
	UsesDefaultNew bool
	// UsesDefaultTpCall reports whether Slots.TpCall is nil or typeTpCall
	// (no custom native TpCall or Python __call__ override).
	UsesDefaultTpCall bool

	// Resolved dunder callables cached at class creation/mutation time
	// so slot trampolines execute with 0 dictionary lookups.
	dunderAdd     Value
	dunderSub     Value
	dunderMul     Value
	dunderAbs     Value
	dunderLen     Value
	dunderBool    Value
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

	// C extension type payload info (PEP 384 / PyType_FromSpec).
	// Zero for pure-Python types. Basicsize includes PyObject header.
	Basicsize int
	Itemsize  int
	// CImports tracks raw C slot function pointers for bridging.
	Native *NativeTypeInfo
}

// NativeTypeInfo holds raw C function pointers for a C-defined type.
// Pointers are unsafe.Pointer; they are interpreted only via capi bridges.
type NativeTypeInfo struct {
	CDealloc unsafe.Pointer
	CNew     unsafe.Pointer
	CInit    unsafe.Pointer
	CRepr    unsafe.Pointer
	CStr     unsafe.Pointer
	CHash    unsafe.Pointer
	CRichcmp unsafe.Pointer
	CCall    unsafe.Pointer
	CFree    unsafe.Pointer
	// Number/Sequence/Mapping slot tables (opaque to Go, parsed by capi).
	AsNumber   unsafe.Pointer
	AsSequence unsafe.Pointer
	AsMapping  unsafe.Pointer
	AsBuffer   unsafe.Pointer
}

// Compile-time assertion that PyType.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(PyType{}.Header)]byte

var nextTypeVersion atomic.Uint64

func allocTypeVersion() uint64 {
	return nextTypeVersion.Add(1)
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
	IncompleteInputErrorType      *PyType
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

// HasCustomGetAttrOrGetAttribute reports whether t overrides __getattribute__ or __getattr__.
func (t *PyType) HasCustomGetAttrOrGetAttribute() bool {
	return (t.Flags&FlagCustomGetAttribute) != 0 || t.dunderGetAttribute.ptr != nil || t.dunderGetAttr.ptr != nil
}

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
	for i := range bases {
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
		dict = NewDictVal(4).AsDict()
	} else {
		dict.EnsureCombined()
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
	dict.owner = t

	mro, err := ComputeMRO(t)
	if err != nil {
		return nil, err
	}
	t.MRO = mro
	t.RefreshInstanceFlags()

	subclassesMu.Lock()
	for _, b := range bases {
		b.Subclasses = append(b.Subclasses, t)
	}
	subclassesMu.Unlock()

	// Python data model: defining __eq__ without __hash__ implicitly sets __hash__ = None.
	if DunderEq.ptr != nil {
		if _, hasEq := dict.Get(DunderEq); hasEq {
			hashKey := strHash
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
			switch {
			case slotsVal.IsAnyString():
				slotNames = append(slotNames, slotsVal.AsString())
			case slotsVal.IsTuple():
				for _, item := range slotsVal.AsTuple().Items {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			case slotsVal.IsList():
				for _, item := range slotsVal.AsList().Items {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			case slotsVal.IsDict():
				for _, item := range slotsVal.AsDict().Keys() {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			case slotsVal.IsSet():
				for _, item := range slotsVal.AsSet().Items() {
					if item.IsAnyString() {
						slotNames = append(slotNames, item.AsString())
					}
				}
			case slotsVal.IsIter():
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
				switch sn {
				case "__dict__":
					hasDict = true
				case "__weakref__":
					continue
				default:
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
			t.Flags |= FlagForbidDynamicAttrs
		}
	}

	if len(bases) > 0 && bases[0] != nil {
		t.Slots = bases[0].Slots
		t.Slots.TpCall = nil
	}

	isTypeSubclass := slices.Contains(t.MRO, TypeType)
	if isTypeSubclass {
		t.Slots.TpGetAttr = typeGetAttr
		t.Slots.TpSetAttr = typeSetAttr
		t.Slots.MpSubscript = typeMpSubscript
		// Metaclasses inherit type.__call__ unless they override __call__ (synced below).
		if TypeType != nil && TypeType.Slots.TpCall != nil {
			t.Slots.TpCall = TypeType.Slots.TpCall
			t.UsesDefaultTpCall = true
		}
	} else {
		isModuleSubclass := ModuleType != nil && slices.Contains(t.MRO, ModuleType)
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

// RefreshInstanceFlags precomputes FlagHasCExtBase, FlagIsModuleSub, and
// primitive-subclass flags from t.MRO so NewInstanceOfType and
// TryFastPrimitiveDunderNew can test them in O(1) without walking MRO.
func (t *PyType) RefreshInstanceFlags() {
	if t == nil {
		return
	}
	hasCExt := t.Basicsize > 16
	isMod := false
	isStrSub := false
	isIntSub := false
	isBytesSub := false
	isTupleSub := false
	for _, b := range t.MRO {
		if b == nil {
			continue
		}
		if b.Basicsize > 16 || (b.Flags&FlagHasCExtBase) != 0 {
			hasCExt = true
		}
		if ModuleType != nil && b == ModuleType {
			isMod = true
		}
		if StrType != nil && b == StrType && t != StrType {
			isStrSub = true
		}
		if IntType != nil && b == IntType && t != IntType {
			isIntSub = true
		}
		if BytesType != nil && b == BytesType && t != BytesType {
			isBytesSub = true
		}
		if TupleType != nil && b == TupleType && t != TupleType {
			isTupleSub = true
		}
	}
	t.Flags &^= FlagHasCExtBase | FlagIsModuleSub | FlagIsStrSub | FlagIsIntSub | FlagIsBytesSub | FlagIsTupleSub
	if hasCExt {
		t.Flags |= FlagHasCExtBase
	}
	if isMod {
		t.Flags |= FlagIsModuleSub
	}
	if isStrSub {
		t.Flags |= FlagIsStrSub
	}
	if isIntSub {
		t.Flags |= FlagIsIntSub
	}
	if isBytesSub {
		t.Flags |= FlagIsBytesSub
	}
	if isTupleSub {
		t.Flags |= FlagIsTupleSub
	}
}

// MustNewType constructs a *PyType or panics if C3 MRO linearization fails.
func MustNewType(name string, bases []*PyType, dict *PyDict) *PyType {
	t, err := NewType(name, bases, dict)
	if err != nil {
		panic(err)
	}
	return t
}

// LookupMRO searches t.MRO in linearization order for internedName. Results
// (including misses) are served from a global allocation-free cache keyed on
// the type version tag and the interned name, so a hit never walks the MRO
// and a miss never allocates on the heap. Names that are not interned
// (neither inline nor FlagInterned) bypass the cache.
func (t *PyType) LookupMRO(internedName Value) (Value, *PyType, bool) {
	if !internedName.IsInterned() {
		return t.lookupMROUncached(internedName)
	}
	ver := t.Version()
	if ver == 0 {
		return t.lookupMROUncached(internedName)
	}
	slot := &typeLookupCache[typeLookupSlot(ver, internedName)]
	slot.mu.Lock()
	if ver != 0 && slot.ver == ver && slot.name == internedName {
		v, owner, found := slot.val, slot.owner, slot.found
		slot.mu.Unlock()
		return v, owner, found
	}
	slot.mu.Unlock()

	v, owner, ok := t.lookupMROUncached(internedName)
	// Only publish when the type was not mutated while we walked it.
	if ver != 0 && t.Version() == ver {
		slot.mu.Lock()
		slot.ver = ver
		slot.name = internedName
		slot.val = v
		slot.owner = owner
		slot.found = ok
		slot.mu.Unlock()
	}
	return v, owner, ok
}

// lookupMROUncached walks t.MRO directly. syncDunderSlots uses it so that the
// roughly sixty dunder probes per class creation do not evict hot entries.
func (t *PyType) lookupMROUncached(internedName Value) (Value, *PyType, bool) {
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
	if !internedName.IsInterned() {
		return t.lookupMROAfterUncached(startClass, internedName)
	}
	ver := t.Version()
	if ver == 0 {
		return t.lookupMROAfterUncached(startClass, internedName)
	}
	slot := &superLookupCache[typeLookupSlot(ver^uint64(uintptr(unsafe.Pointer(startClass))), internedName)]
	slot.mu.Lock()
	if ver != 0 && slot.ver == ver && slot.start == startClass && slot.name == internedName {
		v, owner, found := slot.val, slot.owner, slot.found
		slot.mu.Unlock()
		return v, owner, found
	}
	slot.mu.Unlock()

	v, owner, ok := t.lookupMROAfterUncached(startClass, internedName)
	if ver != 0 && t.Version() == ver {
		slot.mu.Lock()
		slot.ver = ver
		slot.start = startClass
		slot.name = internedName
		slot.val = v
		slot.owner = owner
		slot.found = ok
		slot.mu.Unlock()
	}
	return v, owner, ok
}

type superLookupEntry struct {
	mu    sync.Mutex
	ver   uint64
	start *PyType
	name  Value
	val   Value
	owner *PyType
	found bool
}

var superLookupCache [typeLookupCacheSize]superLookupEntry

func (t *PyType) lookupMROAfterUncached(startClass *PyType, internedName Value) (Value, *PyType, bool) {
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
		t.SetDict(NewDictVal(4).AsDict())
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
	atomic.StoreUint64(&t.VersionTag, allocTypeVersion())
	t.syncDunderSlots()
	subclassesMu.RLock()
	subs := append([]*PyType(nil), t.Subclasses...)
	subclassesMu.RUnlock()
	for _, sub := range subs {
		sub.InvalidateVersion()
	}
}

// Version returns the current monotonic version tag of t. Tags are drawn from
// one global counter and never reused, so two equal tags always denote the same
// type in the same unmutated state. The load is atomic (a plain MOV on amd64).
func (t *PyType) Version() uint64 {
	return atomic.LoadUint64(&t.VersionTag)
}

// bumpVersion invalidates t and every subclass without resynchronizing the
// dunder TypeSlots. It is the hook run by a type namespace dict on every
// mutation, so it must stay cheap and must not look anything up.
func (t *PyType) bumpVersion() {
	atomic.StoreUint64(&t.VersionTag, allocTypeVersion())
	subclassesMu.RLock()
	if len(t.Subclasses) == 0 {
		subclassesMu.RUnlock()
		return
	}
	subs := append([]*PyType(nil), t.Subclasses...)
	subclassesMu.RUnlock()
	for _, sub := range subs {
		sub.bumpVersion()
	}
}

// SetDict installs d as the namespace dict of t and makes every later mutation
// of d invalidate t's version tag.
func (t *PyType) SetDict(d *PyDict) {
	t.Dict = d
	if d != nil {
		d.owner = t
	}
}

// typeLookupCacheSize is the number of slots in the global (version, name)
// lookup cache behind LookupMRO.
const typeLookupCacheSize = 4096

type typeLookupEntry struct {
	mu    sync.Mutex
	ver   uint64
	name  Value
	val   Value
	owner *PyType
	found bool
}

var typeLookupCache [typeLookupCacheSize]typeLookupEntry

// FlushTypeLookupCache drops every cached lookup so that types and values
// referenced only by the cache can be collected (called from gc.collect()).
func FlushTypeLookupCache() {
	for i := range typeLookupCache {
		s := &typeLookupCache[i]
		s.mu.Lock()
		s.ver = 0
		s.name = Value{}
		s.val = Value{}
		s.owner = nil
		s.found = false
		s.mu.Unlock()

		ss := &superLookupCache[i]
		ss.mu.Lock()
		ss.ver = 0
		ss.start = nil
		ss.name = Value{}
		ss.val = Value{}
		ss.owner = nil
		ss.found = false
		ss.mu.Unlock()
	}
}

func typeLookupSlot(ver uint64, name Value) uint {
	h := ver*0x9E3779B97F4A7C15 ^ uint64(uintptr(unsafe.Pointer(name.ptr)))>>3 ^ name.num*0xC2B2AE3D27D4EB4F
	return uint(h>>32) & (typeLookupCacheSize - 1)
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
		if delFn, _, ok := descr.Type().LookupMRO(strDelete); ok {
			ctx.Invoke2(delFn, descr, obj)
			return
		}
		ctx.Raise(NewException(AttributeErrorType, "__delete__"))
		return
	}
	if descr.Type().dunderSet.ptr == nil {
		ctx.Raise(NewException(AttributeErrorType, "__set__"))
		return
	}
	ctx.Invoke3(descr.Type().dunderSet, descr, obj, value)
}

// SyncDunderSlots populates t.Slots function pointers at class creation/mutation time.
func (t *PyType) SyncDunderSlots() {
	t.syncDunderSlots()
}

// syncDunderSlots populates t.Slots function pointers at class creation/mutation time
// by inspecting t.MRO for canonical dunder methods.
func (t *PyType) syncDunderSlots() {
	if DunderAdd.ptr == nil {
		return
	}

	if fn, _, ok := t.lookupMROUncached(DunderAdd); ok {
		t.dunderAdd = fn
		t.Slots.NbAdd = dunderNbAddSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderSub); ok {
		t.dunderSub = fn
		t.Slots.NbSub = dunderNbSubSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderMul); ok {
		t.dunderMul = fn
		t.Slots.NbMul = dunderNbMulSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderAbs); ok {
		t.dunderAbs = fn
		t.Slots.NbAbs = dunderNbAbsSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderLen); ok {
		t.dunderLen = fn
		t.Slots.SqLen = dunderSqLenSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderGetItem); ok {
		t.dunderGetItem = fn
		t.Slots.SqItem = dunderGetItemSlot
		t.Slots.MpSubscript = dunderGetItemSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderSetItem); ok {
		t.dunderSetItem = fn
		t.Slots.SqAssItem = dunderSetItemSlot
		t.Slots.MpAssSubscript = dunderSetItemSlot
	}
	if fn, owner, ok := t.lookupMROUncached(DunderCall); ok {
		if owner == TypeType {
			t.dunderCall = Value{}
			if TypeType != nil && TypeType.Slots.TpCall != nil {
				t.Slots.TpCall = TypeType.Slots.TpCall
			}
			t.UsesDefaultTpCall = true
		} else {
			t.dunderCall = fn
			t.Slots.TpCall = dunderTpCallSlot
			t.UsesDefaultTpCall = false
		}
	} else {
		if t.dunderCall.ptr != nil {
			t.dunderCall = Value{}
			t.Slots.TpCall = nil
		}
		t.UsesDefaultTpCall = t.Slots.TpCall == nil
	}
	if fn, _, ok := t.lookupMROUncached(DunderGet); ok {
		t.dunderGet = fn
		t.Slots.TpDescrGet = dunderTpDescrGetSlot
	}
	if fn, _, ok := t.lookupMROUncached(DunderSet); ok {
		t.dunderSet = fn
		t.Slots.TpDescrSet = dunderTpDescrSetSlot
	} else if _, _, ok := t.lookupMROUncached(strDelete); ok {
		t.dunderSet = Value{}
		t.Slots.TpDescrSet = dunderTpDescrSetSlot
	} else {
		t.dunderSet = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderInit); ok {
		t.dunderInit = fn
	} else {
		t.dunderInit = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderGetAttr); ok {
		t.dunderGetAttr = fn
	} else {
		t.dunderGetAttr = Value{}
	}
	if fn, owner, ok := t.lookupMROUncached(DunderSetAttr); ok && owner != ObjectType && owner != TypeType && owner != ModuleType {
		t.dunderSetAttr = fn
		t.Flags |= FlagCustomSetAttr
	} else {
		t.dunderSetAttr = Value{}
		t.Flags &= ^FlagCustomSetAttr
	}
	if fn, owner, ok := t.lookupMROUncached(DunderIter); ok {
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
	if fn, _, ok := t.lookupMROUncached(DunderNext); ok {
		t.dunderNext = fn
		t.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
			res := ctx.Invoke1(t.dunderNext, o)
			if ctx != nil && ctx.HasException() {
				exc := ctx.ActiveException
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
	if fn, _, ok := t.lookupMROUncached(DunderEnter); ok {
		t.dunderEnter = fn
	} else {
		t.dunderEnter = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderExit); ok {
		t.dunderExit = fn
	} else {
		t.dunderExit = Value{}
	}

	// Rich comparisons
	if fn, _, ok := t.lookupMROUncached(DunderLt); ok {
		t.dunderLt = fn
	} else {
		t.dunderLt = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderLe); ok {
		t.dunderLe = fn
	} else {
		t.dunderLe = Value{}
	}
	if fn, owner, ok := t.lookupMROUncached(DunderEq); ok && owner != ObjectType {
		t.dunderEq = fn
		switch {
		case (t.Flags&FlagIsIntSub) != 0 && owner == IntType && IntEqBuiltin.ptr != nil && fn == IntEqBuiltin:
			t.Flags |= FlagDefaultIntEq
			t.Flags &^= FlagDefaultStrEq
		case (t.Flags&FlagIsStrSub) != 0 && owner == StrType && StrEqBuiltin.ptr != nil && fn == StrEqBuiltin:
			t.Flags |= FlagDefaultStrEq
			t.Flags &^= FlagDefaultIntEq
		default:
			t.Flags &^= FlagDefaultIntEq | FlagDefaultStrEq
		}
	} else {
		t.dunderEq = Value{}
		t.Flags &^= FlagDefaultIntEq | FlagDefaultStrEq
	}
	if fn, owner, ok := t.lookupMROUncached(DunderNe); ok && owner != ObjectType {
		t.dunderNe = fn
	} else {
		t.dunderNe = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderGt); ok {
		t.dunderGt = fn
	} else {
		t.dunderGt = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderGe); ok {
		t.dunderGe = fn
	} else {
		t.dunderGe = Value{}
	}

	// Reflected arithmetic
	if fn, _, ok := t.lookupMROUncached(DunderRAdd); ok {
		t.dunderRAdd = fn
	} else {
		t.dunderRAdd = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRSub); ok {
		t.dunderRSub = fn
	} else {
		t.dunderRSub = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRMul); ok {
		t.dunderRMul = fn
	} else {
		t.dunderRMul = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRMatMul); ok {
		t.dunderRMatMul = fn
	} else {
		t.dunderRMatMul = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRTruediv); ok {
		t.dunderRTruediv = fn
	} else {
		t.dunderRTruediv = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRFloordiv); ok {
		t.dunderRFloordiv = fn
	} else {
		t.dunderRFloordiv = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRMod); ok {
		t.dunderRMod = fn
	} else {
		t.dunderRMod = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRPow); ok {
		t.dunderRPow = fn
	} else {
		t.dunderRPow = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRLShift); ok {
		t.dunderRLShift = fn
	} else {
		t.dunderRLShift = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRRShift); ok {
		t.dunderRRShift = fn
	} else {
		t.dunderRRShift = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRAnd); ok {
		t.dunderRAnd = fn
	} else {
		t.dunderRAnd = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderRXor); ok {
		t.dunderRXor = fn
	} else {
		t.dunderRXor = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderROr); ok {
		t.dunderROr = fn
	} else {
		t.dunderROr = Value{}
	}

	// In-place arithmetic
	if fn, _, ok := t.lookupMROUncached(DunderIAdd); ok {
		t.dunderIAdd = fn
	} else {
		t.dunderIAdd = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderISub); ok {
		t.dunderISub = fn
	} else {
		t.dunderISub = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIMul); ok {
		t.dunderIMul = fn
	} else {
		t.dunderIMul = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIMatMul); ok {
		t.dunderIMatMul = fn
	} else {
		t.dunderIMatMul = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderITruediv); ok {
		t.dunderITruediv = fn
	} else {
		t.dunderITruediv = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIFloordiv); ok {
		t.dunderIFloordiv = fn
	} else {
		t.dunderIFloordiv = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIMod); ok {
		t.dunderIMod = fn
	} else {
		t.dunderIMod = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIPow); ok {
		t.dunderIPow = fn
	} else {
		t.dunderIPow = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderILShift); ok {
		t.dunderILShift = fn
	} else {
		t.dunderILShift = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIRShift); ok {
		t.dunderIRShift = fn
	} else {
		t.dunderIRShift = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIAnd); ok {
		t.dunderIAnd = fn
	} else {
		t.dunderIAnd = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIXor); ok {
		t.dunderIXor = fn
	} else {
		t.dunderIXor = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderIOr); ok {
		t.dunderIOr = fn
	} else {
		t.dunderIOr = Value{}
	}

	// Reflection, attributes & lifecycle
	if fn, owner, ok := t.lookupMROUncached(DunderGetAttribute); ok && owner != ObjectType && owner != ModuleType {
		t.dunderGetAttribute = fn
		t.Flags |= FlagCustomGetAttribute
	} else {
		t.dunderGetAttribute = Value{}
		t.Flags &= ^FlagCustomGetAttribute
	}
	if fn, owner, ok := t.lookupMROUncached(DunderDelAttr); ok && owner != ObjectType && owner != TypeType && owner != ModuleType {
		t.dunderDelAttr = fn
	} else {
		t.dunderDelAttr = Value{}
	}
	if fn, owner, ok := t.lookupMROUncached(strHash); ok && owner != ObjectType {
		t.dunderHash = fn
	} else {
		t.dunderHash = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(strBool); ok {
		t.dunderBool = fn
	} else {
		t.dunderBool = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderDir); ok {
		t.dunderDir = fn
	} else {
		t.dunderDir = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderContains); ok {
		t.dunderContains = fn
	} else {
		t.dunderContains = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderMissing); ok {
		t.dunderMissing = fn
	} else {
		t.dunderMissing = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderNew); ok {
		if fn.IsStaticMethod() {
			t.dunderNew = fn.AsStaticMethod().Callable
		} else {
			t.dunderNew = fn
		}
	} else {
		t.dunderNew = Value{}
	}
	t.UsesDefaultNew = t.dunderNew.ptr == nil || t.dunderNew.ptr == ObjectNewBuiltin.ptr
	if fn, _, ok := t.lookupMROUncached(DunderInitSubclass); ok {
		t.dunderInitSubclass = fn
	} else {
		t.dunderInitSubclass = Value{}
	}
	if fn, _, ok := t.lookupMROUncached(DunderSetName); ok {
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
	return slices.Contains(cls.MRO, base)
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

// DunderCallVal returns the cached __call__ callable for t.
func (t *PyType) DunderCallVal() Value {
	return t.dunderCall
}

// UsesDefaultTypeCall reports whether this metaclass uses standard type.__call__
// instantiation without a custom __call__ or native TpCall override.
func (t *PyType) UsesDefaultTypeCall() bool {
	return t == nil || t == TypeType || (t.dunderCall.ptr == nil && t.UsesDefaultTpCall)
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

func clearAttrError(ctx *ExecContext) {
	if ctx != nil && ctx.HasException() && IsInstance(ctx.ActiveException, AttributeErrorType) {
		exc := ctx.ActiveException
		ctx.ClearException()
		ReleaseLazyAttributeErrorCtx(ctx, exc)
	}
}

// LookupAttrOptional resolves obj.name without panicking or allocating an AttributeError
// when the attribute does not exist on standard objects/modules/types.
func LookupAttrOptional(ctx *ExecContext, obj, name Value) (Value, bool) {
	if obj.IsType() {
		v, ok := typeGetAttrOptional(ctx, obj, name)
		if !ok {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		if ctx != nil && ctx.HasException() {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		return v, true
	}
	if obj.IsSuper() {
		v, ok := superGetAttrOptional(ctx, obj, name)
		if !ok {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		if ctx != nil && ctx.HasException() {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		return v, true
	}
	tp := obj.Type()
	if (tp.Flags&FlagCustomGetAttribute) != 0 || tp.dunderGetAttribute.ptr != nil {
		v := GetAttrCtx(ctx, obj, name)
		if ctx != nil && ctx.HasException() {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		return v, true
	}
	if obj.IsModule() && tp == ModuleType {
		v, ok := defaultModuleGetAttrOptional(ctx, obj, InternValue(name))
		if !ok {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		if ctx != nil && ctx.HasException() {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		return v, true
	}
	if tp.Slots.TpGetAttr == nil || (obj.Kind() != KindNative && !obj.IsModule() && ((tp.Flags&FlagHeapType) != 0 ||
		tp == DictType || tp == ListType || tp == TupleType || tp == StrType ||
		tp == IntType || tp == BigIntType || tp == FloatType || tp == BoolType ||
		tp == NoneType || tp == BytesType || tp == ByteArrayType || tp == SetType ||
		tp == RangeType)) {
		v, ok := defaultObjectGetAttrOptional(ctx, obj, name)
		if !ok {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		if ctx != nil && ctx.HasException() {
			clearAttrError(ctx)
			return NoneVal(), false
		}
		return v, true
	}
	if obj.Kind() == KindBoundMethod {
		interned := name
		if !interned.IsInterned() {
			interned = InternValue(name)
		}
		if bm := obj.AsBoundMethod(); bm.Func.IsFunction() && !interned.StringStartsWithTwoBytes('_', '_') {
			if fn := bm.Func.AsFunction(); fn.Dict != nil {
				if v, ok := fn.Dict.Get(interned); ok {
					return v, true
				}
			}
			return NoneVal(), false
		}
	}
	v := GetAttrCtx(ctx, obj, name)
	if ctx != nil && ctx.HasException() {
		clearAttrError(ctx)
		return NoneVal(), false
	}
	return v, true
}

// HasAttrFast checks whether obj has attribute interned (which must already be
// interned) without allocating BoundMethod wrappers for plain class methods or
// constructing AttributeError instances on misses. Non-AttributeError exceptions
// from descriptors or __getattr__ are left pending on ctx.
func HasAttrFast(ctx *ExecContext, obj, interned Value) bool {
	if obj.Kind() == KindBoundMethod {
		if bm := obj.AsBoundMethod(); bm.Func.IsFunction() && !interned.StringStartsWithTwoBytes('_', '_') {
			if fn := bm.Func.AsFunction(); fn.Dict != nil {
				_, ok := fn.Dict.Get(interned)
				return ok
			}
			return false
		}
	}
	if !obj.IsType() && !obj.IsSuper() && !obj.IsModule() {
		tp := obj.Type()
		if (tp.Flags&FlagCustomGetAttribute) == 0 && tp.dunderGetAttribute.ptr == nil {
			isStd := tp.Slots.TpGetAttr == nil || (obj.Kind() != KindNative && ((tp.Flags&FlagHeapType) != 0 ||
				tp == DictType || tp == ListType || tp == TupleType || tp == StrType ||
				tp == IntType || tp == BigIntType || tp == FloatType || tp == BoolType ||
				tp == NoneType || tp == BytesType || tp == ByteArrayType || tp == SetType ||
				tp == RangeType))
			if isStd {
				if interned == strClass {
					return true
				}
				descr, _, foundInClass := tp.LookupMRO(interned)
				if foundInClass {
					if descr.IsFunction() || descr.IsBuiltinFunc() ||
						descr.Kind() == KindStaticMethod || descr.Kind() == KindClassMethod {
						return true
					}
					descrType := descr.Type()
					if descrType.Slots.TpDescrGet == nil {
						return true
					}
					if descr.IsDataDescriptor() || descrType.Slots.TpDescrSet != nil {
						_ = descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
						if ctx != nil && ctx.HasException() {
							clearAttrError(ctx)
							return false
						}
						return true
					}
				}
				if obj.IsInstance() {
					if inst := obj.AsInstance(); inst.Shape != nil {
						if _, ok := inst.GetAttr(interned); ok {
							return true
						}
					}
					if !foundInClass && tp.dunderGetAttr.ptr == nil &&
						interned != strDict && interned != strAnnotations && interned != strDoc {
						return false
					}
				} else if !foundInClass && (tp == StrType || tp == IntType || tp == DictType ||
					tp == ListType || tp == TupleType || tp == FloatType || tp == BoolType || tp == NoneType) {
					return interned == strDoc
				}
			}
		}
	}
	_, ok := LookupAttrOptional(ctx, obj, interned)
	return ok
}

func defaultObjectGetAttr(ctx *ExecContext, obj, name Value) Value {
	v, ok := defaultObjectGetAttrOptional(ctx, obj, name)
	if !ok {
		if ctx != nil {
			if !ctx.HasException() {
				ctx.Raise(NewLazyAttributeErrorCtx(ctx, obj.Type(), InternValue(name)))
			}
			return NoneVal()
		}
		interned := InternValue(name)
		ctx.Raise(NewException(AttributeErrorType, "'"+obj.Type().Name+"' object has no attribute '"+interned.AsString()+"'"))
		return NoneVal()
	}
	return v
}

func defaultObjectGetAttrOptional(ctx *ExecContext, obj, name Value) (Value, bool) {
	interned := name
	if !interned.IsInterned() {
		interned = InternValue(name)
	}
	tp := obj.Type()

	if interned == strClass {
		return TypeVal(tp), true
	}

	descr, _, foundInClass := tp.LookupMRO(interned)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			if descrType.Slots.TpDescrGet != nil {
				return descrType.Slots.TpDescrGet(ctx, descr, obj, tp), true
			}
		}
	} else if tp == StrType || tp == IntType || tp == DictType || tp == ListType ||
		tp == TupleType || tp == FloatType || tp == BoolType || tp == NoneType {
		if interned == strDoc {
			return NoneVal(), true
		}
		return NoneVal(), false
	}

	if obj.IsInstance() {
		if inst := obj.AsInstance(); inst.Shape != nil {
			if val, ok := inst.GetAttr(interned); ok {
				return val, true
			}
		}
	}

	if obj.IsCInstance() {
		if ci := obj.AsCInstance(); ci != nil && ci.Dict != nil {
			if val, ok := ci.Dict.Get(interned); ok {
				return val, true
			}
		}
	}

	if obj.IsModule() {
		mod := obj.AsModule()
		if interned == strDict {
			return DictVal(mod.Dict), true
		}
		if v, ok := mod.Dict.Get(interned); ok {
			if v.IsLazyImport() && ResolveLazyImportHook != nil {
				var vm any
				if ctx != nil {
					vm = ctx.VM
				}
				resolved, err := ResolveLazyImportHook(vm, mod, interned, v)
				if err != nil {
					if ctx != nil {
						ctx.Raise(NewImportError(err.Error()))
						return NoneVal(), false
					}
					ctx.Raise(NewException(ImportErrorType, err.Error()))
					return NoneVal(), false
				}
				return resolved, true
			}
			return v, true
		}
		switch {
		case interned.StringEquals("__name__"):
			return InternString(mod.Name), true
		case interned.StringEquals("__doc__"):
			return NoneVal(), true
		case interned.StringEquals("__file__"):
			if mod.File != "" {
				return NewStringVal(mod.File), true
			}
		case interned.StringEquals("__annotations__"):
			return ModuleAnnotations(ctx, mod), true
		case interned.StringEquals("__annotate__"):
			return NoneVal(), true
		}
	}

	if obj.IsDict() && tp != DictType {
		if d := obj.AsDict(); d.Attrs != nil {
			if val, ok := d.Attrs.Get(interned); ok {
				return val, true
			}
		}
	}

	if obj.IsList() && tp != ListType {
		if l := obj.AsList(); l.Attrs != nil {
			if val, ok := l.Attrs.Get(interned); ok {
				return val, true
			}
		}
	}

	if obj.IsTuple() && tp != TupleType {
		if t := obj.AsTuple(); t.Attrs != nil {
			if val, ok := t.Attrs.Get(interned); ok {
				return val, true
			}
		}
	}

	if k := obj.Kind(); k == KindFuture || k == KindTask {
		if d := AsyncAttrs(obj, false); d != nil {
			if val, ok := d.Get(interned); ok {
				return val, true
			}
		}
	}

	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			return descrType.Slots.TpDescrGet(ctx, descr, obj, tp), true
		}
		return descr, true
	}

	if interned == strDict && obj.IsInstance() && (tp.Flags&FlagForbidDynamicAttrs) == 0 {
		inst := obj.AsInstance()
		if inst.Shape != nil {
			return NewInstanceDictProxy(inst), true
		}
	}
	if interned == strDict && obj.IsCInstance() && (tp.Flags&FlagForbidDynamicAttrs) == 0 {
		ci := obj.AsCInstance()
		if ci != nil {
			if ci.Dict == nil {
				ci.Dict = NewDictVal(4).AsDict()
			}
			return DictVal(ci.Dict), true
		}
	}

	if tp.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(tp.dunderGetAttr, obj, interned), true
	}

	if k := obj.Kind(); k == KindStaticMethod || k == KindClassMethod || k == KindBoundMethod {
		if interned.StringEquals("__func__") || interned.StringEquals("__wrapped__") {
			if k == KindStaticMethod {
				return obj.AsStaticMethod().Callable, true
			}
			if k == KindClassMethod {
				return obj.AsClassMethod().Callable, true
			}
			return obj.AsBoundMethod().Func, true
		}
		if k == KindBoundMethod {
			if interned.StringEquals("__self__") {
				return obj.AsBoundMethod().Self, true
			}
			bm := obj.AsBoundMethod()
			if bm.Func.HeaderPtr() != nil {
				return defaultObjectGetAttrOptional(ctx, bm.Func, interned)
			}
		}
	}

	if interned == strDict {
		if obj.IsDict() && tp != DictType {
			d := obj.AsDict()
			if d.Attrs == nil {
				d.Attrs = NewDictVal(4).AsDict()
			}
			return Value{num: uint64(KindDict), ptr: &d.Attrs.Header}, true
		}
		if obj.IsList() && tp != ListType {
			l := obj.AsList()
			if l.Attrs == nil {
				l.Attrs = NewDictVal(4).AsDict()
			}
			return Value{num: uint64(KindDict), ptr: &l.Attrs.Header}, true
		}
		if obj.IsTuple() && tp != TupleType {
			t := obj.AsTuple()
			if t.Attrs == nil {
				t.Attrs = NewDictVal(4).AsDict()
			}
			return Value{num: uint64(KindDict), ptr: &t.Attrs.Header}, true
		}
	}

	if interned == strAnnotations {
		if obj.IsType() {
			return TypeAnnotations(ctx, obj.AsType()), true
		}
		if obj.IsInstance() {
			inst := obj.AsInstance()
			if annotateFn, ok := inst.GetAttr(strAnnotate); ok {
				var annDict Value
				if ctx != nil && ctx.CallHook != nil {
					base := ctx.SP
					ctx.Stack[base] = Int64Val(1)
					annDict = ctx.CallHook(ctx, annotateFn, base, 1, nil)
				}
				if annDict.IsDict() {
					inst.SetAttr(interned, annDict)
					return annDict, true
				}
			}
			emptyDict := NewDictVal(4)
			inst.SetAttr(interned, emptyDict)
			return emptyDict, true
		}
	}

	if interned == strDoc {
		return NoneVal(), true
	}

	return NoneVal(), false
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

	if obj.IsInstance() && obj.AsInstance().Shape != nil && interned == strClass {
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
		if interned == strDict && (tp.Flags&FlagForbidDynamicAttrs) == 0 {
			ReplaceInstanceDict(ctx, obj.AsInstance(), value)
			return NoneVal()
		}
		obj.AsInstance().SetAttr(interned, value)
		return NoneVal()
	}

	if obj.IsCInstance() {
		ci := obj.AsCInstance()
		if interned == strDict {
			if !value.IsDict() {
				return RaiseTypeError(ctx, "__dict__ must be set to a dictionary, not a '"+value.Type().Name+"'")
			}
			ci.Dict = value.AsDict()
			return NoneVal()
		}
		if ci.Dict == nil {
			ci.Dict = NewDictVal(4).AsDict()
		}
		ci.Dict.Set(interned, value)
		return NoneVal()
	}

	if obj.IsModule() {
		if interned == strClass {
			AssignInstanceClass(obj, value)
			return NoneVal()
		}
		mod := obj.AsModule()
		if interned == strDict {
			if !value.IsDict() {
				return RaiseTypeError(ctx, "__dict__ must be set to a dictionary, not a '"+value.Type().Name+"'")
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
			d.Attrs = NewDictVal(4).AsDict()
		}
		d.Attrs.Set(interned, value)
		return NoneVal()
	}

	if obj.IsList() && tp != ListType {
		l := obj.AsList()
		if l.Attrs == nil {
			l.Attrs = NewDictVal(4).AsDict()
		}
		l.Attrs.Set(interned, value)
		return NoneVal()
	}

	if obj.IsTuple() && tp != TupleType {
		t := obj.AsTuple()
		if t.Attrs == nil {
			t.Attrs = NewDictVal(4).AsDict()
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
			cls.SetDict(NewDictVal(16).AsDict())
		}
		cls.Dict.Set(interned, value)
		cls.InvalidateVersion()
		return NoneVal()
	}

	return RaiseAttributeErrorMsg(ctx, "'"+tp.Name+"' object cannot set attribute '"+interned.AsString()+"'")
}

// typeGetAttr implements attribute lookup on a *PyType object itself.
func typeGetAttr(ctx *ExecContext, typeObj, name Value) Value {
	v, ok := typeGetAttrOptional(ctx, typeObj, name)
	if !ok {
		if ctx != nil {
			if !ctx.HasException() {
				ctx.Raise(NewLazyAttributeErrorCtx(ctx, typeObj.AsType(), InternValue(name)))
			}
			return NoneVal()
		}
		interned := InternValue(name)
		ctx.Raise(NewException(AttributeErrorType, "type object '"+typeObj.AsType().Name+"' has no attribute '"+interned.AsString()+"'"))
		return NoneVal()
	}
	return v
}

// typeGetAttrOptional resolves attribute lookup on a *PyType object without panicking on miss.
func typeGetAttrOptional(ctx *ExecContext, typeObj, name Value) (Value, bool) {
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
				return descrType.Slots.TpDescrGet(ctx, descr, typeObj, meta), true
			}
		}
	}

	// __doc__ on a class is never inherited from parent classes via MRO.
	if interned == strDoc {
		if cls.Dict != nil {
			if descr, ok := cls.Dict.Get(interned); ok {
				if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
					return descrGet(ctx, descr, NoneVal(), cls), true
				}
				return descr, true
			}
		}
		return NoneVal(), true
	}

	// 2. Check class MRO
	if descr, _, ok := cls.LookupMRO(interned); ok {
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			return descrGet(ctx, descr, NoneVal(), cls), true
		}
		return descr, true
	}

	// 3. Check metaclass MRO for non-data descriptors and class attributes
	if descr, _, ok := meta.LookupMRO(interned); ok {
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			return descrGet(ctx, descr, typeObj, meta), true
		}
		return descr, true
	}

	// 4. Builtin type reflection attributes
	switch interned {
	case strClass:
		return TypeVal(meta), true
	case strBases:
		basesItems := make([]Value, len(cls.Bases))
		for i, b := range cls.Bases {
			basesItems[i] = TypeVal(b)
		}
		return NewTupleVal(basesItems...), true
	case strName:
		return InternString(cls.Name), true
	case strQualname:
		if qn, ok := cls.Dict.Get(strQualname); ok {
			return qn, true
		}
		return InternString(cls.Name), true
	case strTypeParams:
		if tp, ok := cls.Dict.Get(strTypeParams); ok {
			return tp, true
		}
		return NewTupleVal(), true
	case strModule:
		if modVal, ok := cls.Dict.Get(strModule); ok {
			return modVal, true
		}
		return strBuiltins, true
	case strDict:
		return NewMappingProxyVal(ObjectVal(&cls.Dict.Header)), true
	case strMRO:
		mroItems := make([]Value, len(cls.MRO))
		for i, m := range cls.MRO {
			mroItems[i] = TypeVal(m)
		}
		return NewTupleVal(mroItems...), true
	}

	// 5. Fallback to metaclass __getattr__ if defined
	if meta.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(meta.dunderGetAttr, typeObj, interned), true
	}

	return NoneVal(), false
}

func resolveMethodDescr(ctx *ExecContext, descr, selfObj Value, selfType *PyType) (Value, Value) {
	if descr.IsFunction() {
		if selfObj.IsNone() || selfObj.IsUnbound() {
			return UnboundVal(), descr
		}
		return selfObj, descr
	}
	if descr.IsBuiltinFunc() {
		bf := descr.AsBuiltinFunc()
		if bf.NoBind || selfObj.IsNone() || selfObj.IsUnbound() {
			return UnboundVal(), descr
		}
		return selfObj, descr
	}
	switch descr.Kind() {
	case KindClassMethod:
		if selfType == nil {
			selfType = ObjectType
		}
		return TypeVal(selfType), descr.AsClassMethod().Callable
	case KindStaticMethod:
		return UnboundVal(), descr.AsStaticMethod().Callable
	}
	if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
		res := descrGet(ctx, descr, selfObj, selfType)
		if res.IsBoundMethod() {
			bm := res.AsBoundMethod()
			return bm.Self, bm.Func
		}
		return UnboundVal(), res
	}
	return UnboundVal(), descr
}

// LoadMethodNoBind resolves obj.name for an immediate method call without
// allocating a temporary PyBoundMethod object. The caller invokes fn with rec
// prepended when !rec.IsUnbound(), or with args alone otherwise.
func LoadMethodNoBind(ctx *ExecContext, obj, name Value) (Value, Value) {
	interned := InternValue(name)

	if obj.IsSuper() {
		sup := obj.AsSuper()
		var selfType *PyType
		var selfObj Value
		if sup.Self.IsType() && IsSubclass(sup.Self.AsType(), sup.ThisClass) {
			selfType = sup.Self.AsType()
			selfObj = NoneVal()
		} else {
			selfType = sup.Self.Type()
			selfObj = sup.Self
		}
		descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, interned)
		if !ok {
			if ctx != nil {
				ctx.Raise(NewLazyAttributeErrorCtx(ctx, obj.Type(), interned))
			}
			return UnboundVal(), NoneVal()
		}
		return resolveMethodDescr(ctx, descr, selfObj, selfType)
	}

	if obj.IsType() {
		cls := obj.AsType()
		meta := cls.Header.Type
		if meta == nil {
			meta = TypeType
		}
		if (meta.Flags&FlagCustomGetAttribute) == 0 && meta.dunderGetAttribute.ptr == nil && interned != strDoc {
			if descr, _, ok := meta.LookupMRO(interned); ok {
				descrType := descr.Type()
				if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
					if descrType.Slots.TpDescrGet != nil {
						res := descrType.Slots.TpDescrGet(ctx, descr, obj, meta)
						if res.IsBoundMethod() {
							bm := res.AsBoundMethod()
							return bm.Self, bm.Func
						}
						return UnboundVal(), res
					}
				}
			}
			if descr, _, ok := cls.LookupMRO(interned); ok {
				return resolveMethodDescr(ctx, descr, NoneVal(), cls)
			}
			if descr, _, ok := meta.LookupMRO(interned); ok {
				return resolveMethodDescr(ctx, descr, obj, meta)
			}
		}
		res := GetAttrCtx(ctx, obj, interned)
		if res.IsBoundMethod() {
			bm := res.AsBoundMethod()
			return bm.Self, bm.Func
		}
		return UnboundVal(), res
	}

	tp := obj.Type()
	if (tp.Flags&FlagCustomGetAttribute) == 0 && tp.dunderGetAttribute.ptr == nil {
		switch obj.Kind() {
		case KindInstance, KindInt64, KindBigInt, KindFloat64, KindComplex, KindBool, KindNone,
			KindString, KindInlineStr, KindBytes, KindByteArray, KindList, KindTuple,
			KindDict, KindSet, KindSlice, KindRange:
			if interned == strClass {
				return UnboundVal(), TypeVal(tp)
			}
			descr, _, foundInClass := tp.LookupMRO(interned)
			if foundInClass {
				descrType := descr.Type()
				if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
					if descrType.Slots.TpDescrGet != nil {
						res := descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
						if res.IsBoundMethod() {
							bm := res.AsBoundMethod()
							return bm.Self, bm.Func
						}
						return UnboundVal(), res
					}
					return UnboundVal(), descr
				}
			}
			switch {
			case obj.IsInstance():
				if inst := obj.AsInstance(); inst.Shape != nil {
					if v, ok := inst.GetAttr(interned); ok {
						return UnboundVal(), v
					}
				}
			case obj.IsDict() && tp != DictType:
				if d := obj.AsDict(); d.Attrs != nil {
					if v, ok := d.Attrs.Get(interned); ok {
						return UnboundVal(), v
					}
				}
			case obj.IsList() && tp != ListType:
				if l := obj.AsList(); l.Attrs != nil {
					if v, ok := l.Attrs.Get(interned); ok {
						return UnboundVal(), v
					}
				}
			case obj.IsTuple() && tp != TupleType:
				if t := obj.AsTuple(); t.Attrs != nil {
					if v, ok := t.Attrs.Get(interned); ok {
						return UnboundVal(), v
					}
				}
			}
			if foundInClass {
				return resolveMethodDescr(ctx, descr, obj, tp)
			}
		}
	}

	res := GetAttrCtx(ctx, obj, interned)
	if res.IsBoundMethod() {
		bm := res.AsBoundMethod()
		return bm.Self, bm.Func
	}
	return UnboundVal(), res
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
		RaiseAttributeError(ctx, cls, interned)
		return
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
			RaiseAttributeError(ctx, tp, interned)
		}
		return
	}

	if obj.IsFunction() {
		fn := obj.AsFunction()
		if fn.Dict != nil && fn.Dict.Delete(interned) {
			return
		}
		RaiseAttributeError(ctx, tp, interned)
		return
	}

	if obj.IsModule() {
		if !obj.AsModule().Dict.Delete(interned) {
			RaiseAttributeError(ctx, tp, interned)
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

	RaiseAttributeError(ctx, tp, interned)
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
	v, ok := defaultModuleGetAttrOptional(ctx, obj, interned)
	if !ok {
		if ctx != nil {
			if !ctx.HasException() {
				ctx.Raise(NewLazyAttributeErrorCtx(ctx, obj.Type(), interned))
			}
			return NoneVal()
		}
		ctx.Raise(NewException(AttributeErrorType, "module '"+obj.AsModule().Name+"' has no attribute '"+interned.AsString()+"'"))
		return NoneVal()
	}
	return v
}

func defaultModuleGetAttrOptional(ctx *ExecContext, obj, interned Value) (Value, bool) {
	mod := obj.AsModule()
	tp := obj.Type()
	if interned == strClass {
		return TypeVal(tp), true
	}

	// 1. Data descriptors on class MRO
	descr, _, foundInClass := tp.LookupMRO(interned)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			if descrType.Slots.TpDescrGet != nil {
				return descrType.Slots.TpDescrGet(ctx, descr, obj, tp), true
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
				if ctx != nil {
					ctx.Raise(NewImportError(err.Error()))
					return NoneVal(), false
				}
				ctx.Raise(NewException(ImportErrorType, err.Error()))
				return NoneVal(), false
			}
			return resolved, true
		}
		return v, true
	}

	if interned == strDict {
		return DictVal(mod.Dict), true
	}

	// 3. Non-data descriptors on class MRO
	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			return descrType.Slots.TpDescrGet(ctx, descr, obj, tp), true
		}
		return descr, true
	}

	// 4. Built-in module reflection attributes
	switch interned {
	case strName:
		return InternString(mod.Name), true
	case strDoc:
		return NoneVal(), true
	case strFile:
		if mod.File != "" {
			return NewStringVal(mod.File), true
		}
	case strAnnotations:
		return ModuleAnnotations(ctx, mod), true
	case strAnnotate:
		return NoneVal(), true
	}

	// 5. Class-level __getattr__
	if tp.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(tp.dunderGetAttr, obj, interned), true
	}

	// 6. PEP 562 module-level __getattr__
	if interned != DunderGetAttr {
		if getattrFn, ok := mod.Dict.Get(DunderGetAttr); ok && !getattrFn.IsNone() && !getattrFn.IsUnbound() {
			return ctx.Invoke1(getattrFn, interned), true
		}
	}

	return NoneVal(), false
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
	if interned.StringEquals("__class__") {
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
	if interned.StringEquals("__dict__") {
		if !value.IsDict() {
			return RaiseTypeError(ctx, "__dict__ must be set to a dictionary, not a '"+value.Type().Name+"'")
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
		RaiseTypeError(ctx, "__dict__ must be set to a dictionary, not a '"+mapping.Type().Name+"'")
		return
	}
	entries := mapping.AsDict().ActiveEntries()
	tp := inst.Header.Type
	if MultiThreadedFlag.Load() {
		mu := inst.lock()
		mu.RLock()
		names := make([]Value, 0, len(inst.Shape.Attrs))
		for _, a := range inst.Shape.Attrs {
			names = append(names, a.Name)
		}
		mu.RUnlock()
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
		return
	}
	names := make([]Value, 0, len(inst.Shape.Attrs))
	for _, a := range inst.Shape.Attrs {
		names = append(names, a.Name)
	}
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
	var attrs []ShapeAttr
	var slots []Value
	if MultiThreadedFlag.Load() {
		mu := old.lock()
		mu.RLock()
		attrs = append([]ShapeAttr(nil), old.Shape.Attrs...)
		slots = append([]Value(nil), old.Slots...)
		mu.RUnlock()
	} else {
		attrs = append([]ShapeAttr(nil), old.Shape.Attrs...)
		slots = append([]Value(nil), old.Slots...)
	}
	for _, a := range attrs {
		if a.Index < len(slots) && slots[a.Index].ptr != nil {
			fi.SetAttr(a.Name, slots[a.Index])
		}
	}
	if MultiThreadedFlag.Load() {
		mu := old.lock()
		mu.Lock()
		old.Shape = fi.Shape
		old.Slots = fi.Slots
		old.Header.Type = newT
		mu.Unlock()
	} else {
		old.Shape = fi.Shape
		old.Slots = fi.Slots
		old.Header.Type = newT
	}
}
