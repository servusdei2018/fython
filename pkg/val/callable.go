package val

import (
	"math"
	"unsafe"
)

// PyTuple represents an immutable sequence of Values on the heap (used for kwnames and tuple constants).
// Header MUST be at byte offset 0.
type PyTuple struct {
	Header ObjectHeader
	Items  []Value
}

// PyBuiltinFunc represents a zero-allocation native Go built-in callable using the Vectorcall convention.
// Header MUST be at byte offset 0.
type PyBuiltinFunc struct {
	Header ObjectHeader
	Name   string
	Fn     VectorcallFunc
}

// PyFunction represents a Python function object wrapping a KindCode bytecode object,
// default positional argument values, and keyword default values.
// Header MUST be at byte offset 0.
type PyFunction struct {
	Header     ObjectHeader
	Name       string
	Code       Value
	Defaults   []Value
	KwDefaults *PyDict
	Closure    []Value
}

// PyBoundMethod represents a first-class bound method object allocated on the heap ONLY when
// `obj.method` is accessed as a value (via OP_LOAD_ATTR / TpDescrGet) rather than called via OP_LOAD_METHOD.
// Header MUST be at byte offset 0.
type PyBoundMethod struct {
	Header ObjectHeader
	Self   Value
	Func   Value
}

// PyProperty represents a Python @property data descriptor (fget, fset, fdel).
// Header MUST be at byte offset 0 and has FlagDataDescriptor set.
type PyProperty struct {
	Header ObjectHeader
	FGet   Value
	FSet   Value
	FDel   Value
}

// PySuper represents a Python super(ThisClass, Self) proxy object for cooperative MRO traversal.
// Header MUST be at byte offset 0.
type PySuper struct {
	Header    ObjectHeader
	ThisClass *PyType
	Self      Value
}

// Compile-time assertions that every callable/descriptor heap struct places Header at byte offset 0.
var (
	_ [0 - unsafe.Offsetof(PyTuple{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyBuiltinFunc{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyFunction{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyBoundMethod{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyProperty{}.Header)]byte
	_ [0 - unsafe.Offsetof(PySuper{}.Header)]byte
)

// NewTupleVal constructs a heap-allocated KindTuple Value wrapping items.
func NewTupleVal(items ...Value) Value {
	obj := &PyTuple{
		Header: ObjectHeader{
			Kind: KindTuple,
			Type: TupleType,
		},
		Items: items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsTuple performs a zero-cost offset-0 downcast from Value to *PyTuple.
func (v Value) AsTuple() *PyTuple {
	return (*PyTuple)(unsafe.Pointer(v.ptr))
}

// NewBuiltinFuncVal constructs a heap-allocated KindBuiltinFunc Value.
func NewBuiltinFuncVal(name string, fn VectorcallFunc) Value {
	obj := &PyBuiltinFunc{
		Header: ObjectHeader{
			Kind: KindBuiltinFunc,
			Type: BuiltinFuncType,
		},
		Name: name,
		Fn:   fn,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsBuiltinFunc performs a zero-cost offset-0 downcast from Value to *PyBuiltinFunc.
func (v Value) AsBuiltinFunc() *PyBuiltinFunc {
	return (*PyBuiltinFunc)(unsafe.Pointer(v.ptr))
}

// NewFunctionVal constructs a heap-allocated KindFunction Value wrapping a KindCode Value.
func NewFunctionVal(name string, codeVal Value, defaults ...Value) Value {
	obj := &PyFunction{
		Header: ObjectHeader{
			Kind: KindFunction,
			Type: FunctionType,
		},
		Name:     name,
		Code:     codeVal,
		Defaults: defaults,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewFunctionValWithClosure constructs a heap-allocated KindFunction Value with closure cells.
func NewFunctionValWithClosure(name string, codeVal Value, defaults []Value, closure []Value) Value {
	obj := &PyFunction{
		Header: ObjectHeader{
			Kind: KindFunction,
			Type: FunctionType,
		},
		Name:     name,
		Code:     codeVal,
		Defaults: defaults,
		Closure:  closure,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsFunction performs a zero-cost offset-0 downcast from Value to *PyFunction.
func (v Value) AsFunction() *PyFunction {
	return (*PyFunction)(unsafe.Pointer(v.ptr))
}

// NewBoundMethodVal constructs a heap-allocated KindBoundMethod Value binding self to fn.
func NewBoundMethodVal(self, fn Value) Value {
	obj := &PyBoundMethod{
		Header: ObjectHeader{
			Kind: KindBoundMethod,
			Type: BoundMethodType,
		},
		Self: self,
		Func: fn,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsBoundMethod performs a zero-cost offset-0 downcast from Value to *PyBoundMethod.
func (v Value) AsBoundMethod() *PyBoundMethod {
	return (*PyBoundMethod)(unsafe.Pointer(v.ptr))
}

// NewPropertyVal constructs a heap-allocated KindProperty data descriptor Value.
func NewPropertyVal(fget, fset, fdel Value) Value {
	obj := &PyProperty{
		Header: ObjectHeader{
			Kind:  KindProperty,
			Flags: FlagDataDescriptor,
			Type:  PropertyType,
		},
		FGet: fget,
		FSet: fset,
		FDel: fdel,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsProperty performs a zero-cost offset-0 downcast from Value to *PyProperty.
func (v Value) AsProperty() *PyProperty {
	return (*PyProperty)(unsafe.Pointer(v.ptr))
}

// NewSuperVal constructs a heap-allocated KindSuper proxy Value for cooperative MRO resolution.
func NewSuperVal(thisClass *PyType, self Value) Value {
	obj := &PySuper{
		Header: ObjectHeader{
			Kind: KindSuper,
			Type: SuperType,
		},
		ThisClass: thisClass,
		Self:      self,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsSuper performs a zero-cost offset-0 downcast from Value to *PySuper.
func (v Value) AsSuper() *PySuper {
	return (*PySuper)(unsafe.Pointer(v.ptr))
}

// Descriptor and Callable TypeSlot implementations.

func functionDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	if obj.IsNone() || obj.IsUnbound() {
		return descr
	}
	return NewBoundMethodVal(obj, descr)
}

func functionTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if ctx != nil && ctx.CallHook != nil && ctx.SP+len(args) <= len(ctx.Stack) {
		base := ctx.SP
		copy(ctx.Stack[base:base+len(args)], args)
		return ctx.CallHook(ctx, callable, base, len(args), kwnames)
	}
	panic("val: cannot call Python bytecode function without active VM ExecContext")
}

func builtinFuncTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	return callable.AsBuiltinFunc().Fn(ctx, callable, args, kwnames)
}

func boundMethodTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	bm := callable.AsBoundMethod()
	fn := bm.Func
	if ctx != nil && ctx.SP+1+len(args) <= len(ctx.Stack) {
		base := ctx.SP
		ctx.Stack[base] = bm.Self
		copy(ctx.Stack[base+1:base+1+len(args)], args)
		if fn.IsBuiltinFunc() {
			return fn.AsBuiltinFunc().Fn(ctx, fn, ctx.Stack[base:base+1+len(args)], kwnames)
		}
		if ctx.CallHook != nil {
			return ctx.CallHook(ctx, fn, base, 1+len(args), kwnames)
		}
	}
	buf := make([]Value, 1+len(args))
	buf[0] = bm.Self
	copy(buf[1:], args)
	if fn.IsBuiltinFunc() {
		return fn.AsBuiltinFunc().Fn(ctx, fn, buf, kwnames)
	}
	if tpCall := fn.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, fn, buf, kwnames)
	}
	panic("val: bound method target is not callable")
}

func propertyDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	prop := descr.AsProperty()
	if obj.IsNone() || obj.IsUnbound() {
		return descr
	}
	if prop.FGet.ptr == nil || prop.FGet.IsNone() {
		panic("val: AttributeError: unreadable attribute")
	}
	return ctx.Invoke1(prop.FGet, obj)
}

func propertyDescrSet(ctx *ExecContext, descr, obj, value Value) {
	prop := descr.AsProperty()
	if prop.FSet.ptr == nil || prop.FSet.IsNone() {
		panic("val: AttributeError: can't set attribute")
	}
	ctx.Invoke2(prop.FSet, obj, value)
}

func superGetAttr(ctx *ExecContext, superObj, name Value) Value {
	sup := superObj.AsSuper()
	interned := InternValue(name)
	selfType := sup.Self.Type()
	descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, interned)
	if !ok {
		panic("val: AttributeError: 'super' object has no attribute '" + interned.AsString() + "'")
	}
	if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
		return descrGet(ctx, descr, sup.Self, selfType)
	}
	return descr
}

func typeTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	cls := callable.AsType()
	inst := NewInstanceOfType(cls)
	if cls.HasInit() {
		initFn := cls.DunderInitVal()
		if ctx != nil && ctx.SP+1+len(args) <= len(ctx.Stack) {
			base := ctx.SP
			ctx.Stack[base] = inst
			copy(ctx.Stack[base+1:base+1+len(args)], args)
			if initFn.IsBuiltinFunc() {
				initFn.AsBuiltinFunc().Fn(ctx, initFn, ctx.Stack[base:base+1+len(args)], kwnames)
				return inst
			}
			if ctx.CallHook != nil {
				ctx.CallHook(ctx, initFn, base, 1+len(args), kwnames)
				return inst
			}
		}
		buf := make([]Value, 1+len(args))
		buf[0] = inst
		copy(buf[1:], args)
		if initFn.IsBuiltinFunc() {
			initFn.AsBuiltinFunc().Fn(ctx, initFn, buf, kwnames)
		} else if tpCall := initFn.Type().Slots.TpCall; tpCall != nil {
			tpCall(ctx, initFn, buf, kwnames)
		}
	}
	return inst
}

// Built-in TypeSlots for primitive and collection types.

func intNbAbs(ctx *ExecContext, a Value) Value {
	x := a.AsInt64()
	if x < 0 {
		if x == math.MinInt64 {
			return subInt64Overflow(0, x)
		}
		return Int64Val(-x)
	}
	return a
}

func floatNbAbs(ctx *ExecContext, a Value) Value {
	return Float64Val(math.Abs(a.AsFloat64()))
}

func bigIntNbAbs(ctx *ExecContext, a Value) Value {
	obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType}}
	obj.Val.Abs(&a.AsBigInt().Val)
	return Value{num: 0, ptr: &obj.Header}
}

func strSqLen(ctx *ExecContext, a Value) int64 {
	return int64(a.StringLen())
}

func strSqItem(ctx *ExecContext, a, idxVal Value) Value {
	idx := int(idxVal.AsInt64())
	s := a.AsString()
	if idx < 0 {
		idx += len(s)
	}
	if idx < 0 || idx >= len(s) {
		panic("val: IndexError: string index out of range")
	}
	return SmallStringVal(s[idx : idx+1])
}

func listSqLen(ctx *ExecContext, a Value) int64 {
	return int64(a.AsList().Len())
}

func listSqItem(ctx *ExecContext, a, idxVal Value) Value {
	val, ok := a.AsList().Get(int(idxVal.AsInt64()))
	if !ok {
		panic("val: IndexError: list index out of range")
	}
	return val
}

func listSqAssItem(ctx *ExecContext, a, idxVal, val Value) Value {
	if !a.AsList().Set(int(idxVal.AsInt64()), val) {
		panic("val: IndexError: list assignment index out of range")
	}
	return NoneVal()
}

func tupleSqLen(ctx *ExecContext, a Value) int64 {
	return int64(len(a.AsTuple().Items))
}

func tupleSqItem(ctx *ExecContext, a, idxVal Value) Value {
	items := a.AsTuple().Items
	idx := int(idxVal.AsInt64())
	if idx < 0 {
		idx += len(items)
	}
	if idx < 0 || idx >= len(items) {
		panic("val: IndexError: tuple index out of range")
	}
	return items[idx]
}

func dictSqLen(ctx *ExecContext, a Value) int64 {
	return int64(a.AsDict().Len())
}

func dictMpSubscript(ctx *ExecContext, a, key Value) Value {
	v, ok := a.AsDict().Get(key)
	if !ok {
		panic("val: KeyError")
	}
	return v
}

func dictMpAssSubscript(ctx *ExecContext, a, key, val Value) Value {
	a.AsDict().Set(key, val)
	return NoneVal()
}

// Standard Zero-Allocation Vectorcall Native Built-in Functions.

// BuiltinLen implements the Python len(x) built-in with 0 heap allocations.
func BuiltinLen(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) != 1 || len(kwnames) != 0 {
		panic("val: TypeError: len() takes exactly one positional argument")
	}
	x := args[0]
	if x.ptr == &tagInlineStr {
		return Int64Val(int64(x.num & 0xFF))
	}
	switch x.ptr.Kind {
	case KindList:
		return Int64Val(int64(len(x.AsList().Items)))
	case KindString:
		return Int64Val(int64(len(x.AsStringObj().Data)))
	case KindDict:
		return Int64Val(int64(x.AsDict().Len()))
	case KindTuple:
		return Int64Val(int64(len(x.AsTuple().Items)))
	}
	sqLen := x.Type().Slots.SqLen
	if sqLen == nil {
		panic("val: TypeError: object of type '" + x.Type().Name + "' has no len()")
	}
	return Int64Val(sqLen(ctx, x))
}

// BuiltinAbs implements the Python abs(x) built-in with 0 heap allocations.
func BuiltinAbs(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) != 1 || len(kwnames) != 0 {
		panic("val: TypeError: abs() takes exactly one positional argument")
	}
	x := args[0]
	if x.ptr == &tagInt64 {
		i := int64(x.num)
		if i < 0 {
			if i == math.MinInt64 {
				return subInt64Overflow(0, i)
			}
			return Value{num: uint64(-i), ptr: &tagInt64}
		}
		return x
	}
	if x.ptr == &tagFloat64 {
		return Float64Val(math.Abs(math.Float64frombits(x.num)))
	}
	nbAbs := x.Type().Slots.NbAbs
	if nbAbs == nil {
		panic("val: TypeError: bad operand type for abs(): '" + x.Type().Name + "'")
	}
	return nbAbs(ctx, x)
}

// Built-in function singletons.
var (
	BuiltinLenVal Value
	BuiltinAbsVal Value
)

func init() {
	initTypes()
}

func initTypes() {
	// 1. Pre-intern canonical dunder method names.
	DunderAdd = InternString("__add__")
	DunderSub = InternString("__sub__")
	DunderMul = InternString("__mul__")
	DunderAbs = InternString("__abs__")
	DunderLen = InternString("__len__")
	DunderGetItem = InternString("__getitem__")
	DunderSetItem = InternString("__setitem__")
	DunderCall = InternString("__call__")
	DunderGet = InternString("__get__")
	DunderSet = InternString("__set__")
	DunderInit = InternString("__init__")
	DunderGetAttr = InternString("__getattr__")
	DunderSetAttr = InternString("__setattr__")
	DunderIter = InternString("__iter__")
	DunderNext = InternString("__next__")
	DunderEnter = InternString("__enter__")
	DunderExit = InternString("__exit__")

	// 2. Bootstrap ObjectType and TypeType.
	ObjectType = &PyType{
		Header:     ObjectHeader{Kind: KindType},
		Name:       "object",
		RootShape:  defaultRootShape,
		VersionTag: allocTypeVersion(),
	}
	ObjectType.MRO = []*PyType{ObjectType}
	ObjectType.Slots.TpGetAttr = objectGetAttr
	ObjectType.Slots.TpSetAttr = objectSetAttr

	TypeType = &PyType{
		Header:     ObjectHeader{Kind: KindType},
		Name:       "type",
		Bases:      []*PyType{ObjectType},
		MRO:        []*PyType{nil, ObjectType},
		RootShape:  NewRootShape(),
		VersionTag: allocTypeVersion(),
	}
	TypeType.MRO[0] = TypeType
	TypeType.Header.Type = TypeType
	ObjectType.Header.Type = TypeType
	ObjectType.Dict = NewDictVal(8).AsDict()
	TypeType.Dict = NewDictVal(8).AsDict()
	TypeType.Slots.TpGetAttr = typeGetAttr
	TypeType.Slots.TpSetAttr = typeSetAttr
	TypeType.Slots.TpCall = typeTpCall

	// 3. Construct built-in primitive and heap object types.
	NoneType = MustNewType("NoneType", []*PyType{ObjectType}, nil)
	IntType = MustNewType("int", []*PyType{ObjectType}, nil)
	IntType.Slots.NbAdd = numericNbAdd
	IntType.Slots.NbSub = numericNbSub
	IntType.Slots.NbMul = numericNbMul
	IntType.Slots.NbAbs = intNbAbs

	BoolType = MustNewType("bool", []*PyType{IntType}, nil)
	BoolType.Slots.NbAdd = numericNbAdd
	BoolType.Slots.NbSub = numericNbSub
	BoolType.Slots.NbMul = numericNbMul
	BoolType.Slots.NbAbs = intNbAbs

	FloatType = MustNewType("float", []*PyType{ObjectType}, nil)
	FloatType.Slots.NbAdd = numericNbAdd
	FloatType.Slots.NbSub = numericNbSub
	FloatType.Slots.NbMul = numericNbMul
	FloatType.Slots.NbAbs = floatNbAbs

	BigIntType = MustNewType("bigint", []*PyType{IntType}, nil)
	BigIntType.Slots.NbAdd = numericNbAdd
	BigIntType.Slots.NbSub = numericNbSub
	BigIntType.Slots.NbMul = numericNbMul
	BigIntType.Slots.NbAbs = bigIntNbAbs

	StrType = MustNewType("str", []*PyType{ObjectType}, nil)
	StrType.Slots.NbAdd = strNbAdd
	StrType.Slots.SqLen = strSqLen
	StrType.Slots.SqItem = strSqItem
	StrType.Slots.MpSubscript = strSqItem

	// Backfill Type = StrType on heap dunder strings interned prior to StrType creation.
	for _, d := range []Value{DunderGetItem, DunderSetItem, DunderCall, DunderInit, DunderGetAttr, DunderSetAttr} {
		if d.IsString() {
			d.ptr.Type = StrType
		}
	}

	ListType = MustNewType("list", []*PyType{ObjectType}, nil)
	ListType.Slots.NbAdd = listNbAdd
	ListType.Slots.SqLen = listSqLen
	ListType.Slots.SqItem = listSqItem
	ListType.Slots.MpSubscript = listSqItem
	ListType.Slots.SqAssItem = listSqAssItem
	ListType.Slots.MpAssSubscript = listSqAssItem
	ListType.Dict.Set(InternString("append"), NewBuiltinFuncVal("append", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: append() takes exactly one argument")
		}
		list := args[0].AsList()
		list.Append(args[1])
		return NoneVal()
	}))

	DictType = MustNewType("dict", []*PyType{ObjectType}, nil)
	DictType.Slots.SqLen = dictSqLen
	DictType.Slots.MpSubscript = dictMpSubscript
	DictType.Slots.MpAssSubscript = dictMpAssSubscript

	TupleType = MustNewType("tuple", []*PyType{ObjectType}, nil)
	TupleType.Slots.SqLen = tupleSqLen
	TupleType.Slots.SqItem = tupleSqItem
	TupleType.Slots.MpSubscript = tupleSqItem

	CodeType = MustNewType("code", []*PyType{ObjectType}, nil)
	CodeType.Slots.TpCall = functionTpCall

	FunctionType = MustNewType("function", []*PyType{ObjectType}, nil)
	FunctionType.Slots.TpDescrGet = functionDescrGet
	FunctionType.Slots.TpCall = functionTpCall

	BuiltinFuncType = MustNewType("builtin_function_or_method", []*PyType{ObjectType}, nil)
	BuiltinFuncType.Slots.TpDescrGet = functionDescrGet
	BuiltinFuncType.Slots.TpCall = builtinFuncTpCall

	BoundMethodType = MustNewType("method", []*PyType{ObjectType}, nil)
	BoundMethodType.Slots.TpCall = boundMethodTpCall

	PropertyType = MustNewType("property", []*PyType{ObjectType}, nil)
	PropertyType.Slots.TpDescrGet = propertyDescrGet
	PropertyType.Slots.TpDescrSet = propertyDescrSet

	SuperType = MustNewType("super", []*PyType{ObjectType}, nil)
	SuperType.Slots.TpGetAttr = superGetAttr

	CellType = MustNewType("cell", []*PyType{ObjectType}, nil)
	IterType = MustNewType("iterator", []*PyType{ObjectType}, nil)
	IterType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return o }
	IterType.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
		it := o.AsIter()
		v, ok := it.Next()
		if ok {
			return v, IterYielded
		}
		return NoneVal(), IterExhausted
	}

	RangeType = MustNewType("range", []*PyType{ObjectType}, nil)
	RangeType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return o }
	RangeType.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
		if o.IsIter() {
			it := o.AsIter()
			v, ok := it.Next()
			if ok {
				return v, IterYielded
			}
			return NoneVal(), IterExhausted
		}
		return NoneVal(), IterExhausted
	}

	GeneratorType = MustNewType("generator", []*PyType{ObjectType}, nil)
	GeneratorType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return o }
	GeneratorType.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
		gen := o.AsGenerator()
		return gen.Send(ctx, NoneVal())
	}
	GeneratorType.Dict.Set(InternString("send"), NewBuiltinFuncVal("send", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: send() takes exactly one argument")
		}
		gen := args[0].AsGenerator()
		v, status := gen.Send(ctx, args[1])
		if status == IterExhausted {
			ctx.Raise(NewStopIteration(v))
		}
		return v
	}))
	GeneratorType.Dict.Set(InternString("close"), NewBuiltinFuncVal("close", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: close() takes at least 1 argument")
		}
		gen := args[0].AsGenerator()
		gen.Close(ctx)
		return NoneVal()
	}))
	GeneratorType.Dict.Set(InternString("throw"), NewBuiltinFuncVal("throw", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: throw() takes at least 1 argument")
		}
		gen := args[0].AsGenerator()
		v, _ := gen.Throw(ctx, args[1])
		return v
	}))

	ChanType = MustNewType("chan", []*PyType{ObjectType}, nil)
	ChanType.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
		return o
	}
	ChanType.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
		ch := o.AsChan()
		v, ok := ch.Recv()
		if !ok {
			return NoneVal(), IterExhausted
		}
		return v, IterYielded
	}
	ChanType.Dict.Set(InternString("send"), NewBuiltinFuncVal("send", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: chan.send() takes exactly 1 argument")
		}
		ch := args[0].AsChan()
		if err := ch.Send(args[1]); err != nil {
			panic("RuntimeError: " + err.Error())
		}
		return NoneVal()
	}))
	ChanType.Dict.Set(InternString("recv"), NewBuiltinFuncVal("recv", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: chan.recv() takes at least 0 arguments")
		}
		ch := args[0].AsChan()
		v, ok := ch.Recv()
		if !ok {
			if len(args) >= 2 {
				return args[1]
			}
			panic("StopIteration")
		}
		return v
	}))
	ChanType.Dict.Set(InternString("close"), NewBuiltinFuncVal("close", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: chan.close() takes no arguments")
		}
		ch := args[0].AsChan()
		ch.Close()
		return NoneVal()
	}))

	ListType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return NewIterVal(o) }
	TupleType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return NewIterVal(o) }
	DictType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return NewIterVal(o) }

	// Initialize built-in exception types
	BaseExceptionType = MustNewType("BaseException", []*PyType{ObjectType}, nil)
	BaseExceptionType.Dict.Set(DunderInit, NewBuiltinFuncVal("__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			self := args[0]
			if self.IsInstance() {
				var tupleItems []Value
				if len(args) > 1 {
					tupleItems = make([]Value, len(args)-1)
					copy(tupleItems, args[1:])
				}
				self.AsInstance().SetAttr(InternString("args"), NewTupleVal(tupleItems...))
			}
		}
		return NoneVal()
	}))
	ExceptionType = MustNewType("Exception", []*PyType{BaseExceptionType}, nil)
	TypeErrorType = MustNewType("TypeError", []*PyType{ExceptionType}, nil)
	ValueErrorType = MustNewType("ValueError", []*PyType{ExceptionType}, nil)
	KeyErrorType = MustNewType("KeyError", []*PyType{ExceptionType}, nil)
	IndexErrorType = MustNewType("IndexError", []*PyType{ExceptionType}, nil)
	ZeroDivisionErrorType = MustNewType("ZeroDivisionError", []*PyType{ExceptionType}, nil)
	NameErrorType = MustNewType("NameError", []*PyType{ExceptionType}, nil)
	AttributeErrorType = MustNewType("AttributeError", []*PyType{ExceptionType}, nil)
	RuntimeErrorType = MustNewType("RuntimeError", []*PyType{ExceptionType}, nil)
	StopIterationType = MustNewType("StopIteration", []*PyType{ExceptionType}, nil)
	GeneratorExitType = MustNewType("GeneratorExit", []*PyType{BaseExceptionType}, nil)

	// 4. Populate static lookup table and sentinel headers.
	builtinTypes[KindNone] = NoneType
	builtinTypes[KindBool] = BoolType
	builtinTypes[KindInt64] = IntType
	builtinTypes[KindFloat64] = FloatType
	builtinTypes[KindInlineStr] = StrType
	builtinTypes[KindBigInt] = BigIntType
	builtinTypes[KindString] = StrType
	builtinTypes[KindList] = ListType
	builtinTypes[KindDict] = DictType
	builtinTypes[KindCode] = CodeType
	builtinTypes[KindInstance] = ObjectType
	builtinTypes[KindType] = TypeType
	builtinTypes[KindFunction] = FunctionType
	builtinTypes[KindBuiltinFunc] = BuiltinFuncType
	builtinTypes[KindBoundMethod] = BoundMethodType
	builtinTypes[KindProperty] = PropertyType
	builtinTypes[KindSuper] = SuperType
	builtinTypes[KindTuple] = TupleType
	builtinTypes[KindCell] = CellType
	builtinTypes[KindIter] = IterType
	builtinTypes[KindRange] = RangeType
	builtinTypes[KindGenerator] = GeneratorType
	builtinTypes[KindChan] = ChanType

	tagNone.Type = NoneType
	tagBool.Type = BoolType
	tagInt64.Type = IntType
	tagFloat64.Type = FloatType
	tagInlineStr.Type = StrType
	tagUnbound.Type = NoneType
	tagRange.Type = RangeType

	// 5. Initialize standard native built-in callables.
	BuiltinLenVal = NewBuiltinFuncVal("len", BuiltinLen)
	BuiltinAbsVal = NewBuiltinFuncVal("abs", BuiltinAbs)
}
