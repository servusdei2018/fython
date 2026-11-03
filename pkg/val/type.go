package val

import (
	"errors"
	"sync/atomic"
	"unsafe"
)

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
}

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

	TypeType        *PyType
	ObjectType      *PyType
	NoneType        *PyType
	BoolType        *PyType
	IntType         *PyType
	FloatType       *PyType
	StrType         *PyType
	BigIntType      *PyType
	ListType        *PyType
	DictType        *PyType
	TupleType       *PyType
	CodeType        *PyType
	FunctionType    *PyType
	BuiltinFuncType *PyType
	BoundMethodType *PyType
	PropertyType    *PyType
	SuperType       *PyType
	CellType        *PyType
	IterType        *PyType
	RangeType       *PyType
	GeneratorType   *PyType
	ChanType        *PyType
	ModuleType      *PyType
	BytesType       *PyType
	ByteArrayType   *PyType
	MemoryViewType  *PyType
	SliceType       *PyType

	// Built-in exception types
	BaseExceptionType       *PyType
	ExceptionType           *PyType
	TypeErrorType           *PyType
	ValueErrorType          *PyType
	KeyErrorType            *PyType
	IndexErrorType          *PyType
	ZeroDivisionErrorType   *PyType
	NameErrorType           *PyType
	AttributeErrorType      *PyType
	RuntimeErrorType        *PyType
	StopIterationType       *PyType
	GeneratorExitType       *PyType
	ImportErrorType         *PyType
	ModuleNotFoundErrorType *PyType
	BufferErrorType         *PyType
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
			Type: TypeType,
		},
		Name:       name,
		Bases:      bases,
		Dict:       dict,
		RootShape:  NewRootShape(),
		VersionTag: allocTypeVersion(),
	}

	mro, err := ComputeMRO(t)
	if err != nil {
		return nil, err
	}
	t.MRO = mro

	for _, b := range bases {
		b.Subclasses = append(b.Subclasses, t)
	}

	t.Slots.TpGetAttr = objectGetAttr
	t.Slots.TpSetAttr = objectSetAttr
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
			if v, ok := cls.Dict.Get(internedName); ok {
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
			if v, ok := cls.Dict.Get(internedName); ok {
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
	for _, sub := range t.Subclasses {
		sub.InvalidateVersion()
	}
}

// Dunder slot trampolines that dispatch through cached dunder callables on *PyType
// without runtime string or dictionary lookups.

func dunderNbAddSlot(ctx *ExecContext, a, b Value) Value {
	return ctx.Invoke2(a.Type().dunderAdd, a, b)
}

func dunderNbSubSlot(ctx *ExecContext, a, b Value) Value {
	return ctx.Invoke2(a.Type().dunderSub, a, b)
}

func dunderNbMulSlot(ctx *ExecContext, a, b Value) Value {
	return ctx.Invoke2(a.Type().dunderMul, a, b)
}

func dunderNbAbsSlot(ctx *ExecContext, a Value) Value {
	return ctx.Invoke1(a.Type().dunderAbs, a)
}

func dunderSqLenSlot(ctx *ExecContext, a Value) int64 {
	res := ctx.Invoke1(a.Type().dunderLen, a)
	return res.AsInt64()
}

func dunderGetItemSlot(ctx *ExecContext, a, b Value) Value {
	return ctx.Invoke2(a.Type().dunderGetItem, a, b)
}

func dunderSetItemSlot(ctx *ExecContext, a, b, c Value) Value {
	return ctx.Invoke3(a.Type().dunderSetItem, a, b, c)
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
	if fn, _, ok := t.LookupMRO(DunderSetAttr); ok {
		t.dunderSetAttr = fn
	} else {
		t.dunderSetAttr = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderIter); ok {
		t.dunderIter = fn
		t.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
			return ctx.Invoke1(t.dunderIter, o)
		}
	} else {
		t.dunderIter = Value{}
	}
	if fn, _, ok := t.LookupMRO(DunderNext); ok {
		t.dunderNext = fn
		t.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
			res := ctx.Invoke1(t.dunderNext, o)
			if ctx.HasException() {
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
}

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
	return t.dunderInit.ptr != nil
}

// DunderInitVal returns the cached __init__ callable for t.
func (t *PyType) DunderInitVal() Value {
	return t.dunderInit
}

// objectGetAttr implements the standard Python object.__getattribute__ protocol,
// enforcing data descriptor precedence -> instance Shape slots -> non-data descriptors.
func objectGetAttr(ctx *ExecContext, obj, name Value) Value {
	interned := InternValue(name)
	tp := obj.Type()

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
		if val, ok := obj.AsInstance().GetAttr(interned); ok {
			return val
		}
	}

	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			return descrType.Slots.TpDescrGet(ctx, descr, obj, tp)
		}
		return descr
	}

	if tp.dunderGetAttr.ptr != nil {
		return ctx.Invoke2(tp.dunderGetAttr, obj, interned)
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

	if descr, _, foundInClass := tp.LookupMRO(interned); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			descrType.Slots.TpDescrSet(ctx, descr, obj, value)
			return NoneVal()
		}
	}

	if obj.IsInstance() {
		obj.AsInstance().SetAttr(interned, value)
		return NoneVal()
	}

	panic("val: AttributeError: '" + tp.Name + "' object cannot set attribute '" + interned.AsString() + "'")
}

// typeGetAttr implements attribute lookup on a *PyType object itself.
func typeGetAttr(ctx *ExecContext, typeObj, name Value) Value {
	cls := typeObj.AsType()
	interned := InternValue(name)
	if descr, _, ok := cls.LookupMRO(interned); ok {
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			return descrGet(ctx, descr, NoneVal(), cls)
		}
		return descr
	}
	panic("val: AttributeError: type object '" + cls.Name + "' has no attribute '" + interned.AsString() + "'")
}

// typeSetAttr implements attribute assignment on a *PyType object, invalidating VersionTag.
func typeSetAttr(ctx *ExecContext, typeObj, name, value Value) Value {
	cls := typeObj.AsType()
	cls.SetAttr(name, value)
	return NoneVal()
}

// moduleGetAttr implements attribute lookup on a *PyModule object.
func moduleGetAttr(ctx *ExecContext, obj, name Value) Value {
	mod := obj.AsModule()
	interned := InternValue(name)
	if v, ok := mod.Dict.Get(interned); ok {
		return v
	}
	if ModuleType != nil {
		if descr, _, ok := ModuleType.LookupMRO(interned); ok {
			if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
				return descrGet(ctx, descr, obj, ModuleType)
			}
			return descr
		}
	}
	panic("val: AttributeError: module '" + mod.Name + "' has no attribute '" + interned.AsString() + "'")
}

// moduleSetAttr implements attribute assignment on a *PyModule object.
func moduleSetAttr(ctx *ExecContext, obj, name, value Value) Value {
	mod := obj.AsModule()
	interned := InternValue(name)
	mod.Dict.Set(interned, value)
	return NoneVal()
}
