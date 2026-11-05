package val

import (
	"bytes"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
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
	Globals    *PyDict
	Dict       *PyDict
}

// MethodPICEntry represents a single slot in a 4-way Polymorphic Inline Cache for method calls.
type MethodPICEntry struct {
	Shape       *Shape         // Verifies both concrete PyType and that no instance attr shadows the method
	TypeVersion uint64         // Verifies the class MRO / method dict hasn't been mutated
	TargetFn    *PyFunction    // Direct concrete pointer to target bytecode function (NO interface{})
	NativeFn    VectorcallFunc // Direct function pointer if builtin/native method
	TargetVal   Value          // Cached Value wrapper
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

// NewFunctionValWithGlobals constructs a heap-allocated KindFunction Value bound to module globals.
func NewFunctionValWithGlobals(name string, codeVal Value, defaults []Value, globals *PyDict) Value {
	obj := &PyFunction{
		Header: ObjectHeader{
			Kind: KindFunction,
			Type: FunctionType,
		},
		Name:     name,
		Code:     codeVal,
		Defaults: defaults,
		Globals:  globals,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewFunctionValWithGlobalsAndClosure constructs a heap-allocated KindFunction Value bound to module globals and closure cells.
func NewFunctionValWithGlobalsAndClosure(name string, codeVal Value, defaults []Value, closure []Value, globals *PyDict) Value {
	obj := &PyFunction{
		Header: ObjectHeader{
			Kind: KindFunction,
			Type: FunctionType,
		},
		Name:     name,
		Code:     codeVal,
		Defaults: defaults,
		Closure:  closure,
		Globals:  globals,
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

// PyMemberDescr represents a member descriptor exposed for __slots__.
type PyMemberDescr struct {
	Header    ObjectHeader
	Owner     *PyType
	Name      Value
	SlotIndex int
}

var _ [0 - unsafe.Offsetof(PyMemberDescr{}.Header)]byte

// NewMemberDescr constructs a heap-allocated KindMemberDescr data descriptor Value.
func NewMemberDescr(owner *PyType, name Value, slotIndex int) Value {
	obj := &PyMemberDescr{
		Header: ObjectHeader{
			Kind:  KindMemberDescr,
			Flags: FlagDataDescriptor,
			Type:  MemberDescrType,
		},
		Owner:     owner,
		Name:      name,
		SlotIndex: slotIndex,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsMemberDescr performs a zero-cost offset-0 downcast from Value to *PyMemberDescr.
func (v Value) AsMemberDescr() *PyMemberDescr {
	return (*PyMemberDescr)(unsafe.Pointer(v.ptr))
}

func memberDescrTpDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	if obj.IsNone() || obj.IsUnbound() {
		return descr
	}
	md := descr.AsMemberDescr()
	if !obj.IsInstance() {
		panic("TypeError: descriptor '" + md.Name.AsString() + "' for '" + md.Owner.Name + "' objects doesn't apply to a '" + obj.Type().Name + "' object")
	}
	inst := obj.AsInstance()
	if md.SlotIndex < 0 || md.SlotIndex >= len(inst.Slots) {
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	val := inst.Slots[md.SlotIndex]
	if val.IsUnbound() {
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	return val
}

func memberDescrTpDescrSet(ctx *ExecContext, descr, obj, value Value) {
	md := descr.AsMemberDescr()
	if !obj.IsInstance() {
		panic("TypeError: descriptor '" + md.Name.AsString() + "' for '" + md.Owner.Name + "' objects doesn't apply to a '" + obj.Type().Name + "' object")
	}
	inst := obj.AsInstance()
	if md.SlotIndex < 0 || md.SlotIndex >= len(inst.Slots) {
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	inst.Slots[md.SlotIndex] = value
}

// PyClassMethod represents a Python classmethod descriptor.
type PyClassMethod struct {
	Header   ObjectHeader
	Callable Value
}

var _ [0 - unsafe.Offsetof(PyClassMethod{}.Header)]byte

// NewClassMethodVal constructs a heap-allocated KindClassMethod descriptor Value.
func NewClassMethodVal(fn Value) Value {
	obj := &PyClassMethod{
		Header: ObjectHeader{
			Kind: KindClassMethod,
			Type: ClassMethodType,
		},
		Callable: fn,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsClassMethod performs a zero-cost offset-0 downcast from Value to *PyClassMethod.
func (v Value) AsClassMethod() *PyClassMethod {
	return (*PyClassMethod)(unsafe.Pointer(v.ptr))
}

func classMethodDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	cm := descr.AsClassMethod()
	var selfType *PyType
	if owner != nil {
		selfType = owner
	} else if !obj.IsNone() && !obj.IsUnbound() {
		selfType = obj.Type()
	} else {
		selfType = ObjectType
	}
	return NewBoundMethodVal(TypeVal(selfType), cm.Callable)
}

func classMethodTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) != 1 {
		panic("TypeError: classmethod() takes exactly one argument")
	}
	return NewClassMethodVal(args[0])
}

// PyStaticMethod represents a Python staticmethod descriptor.
type PyStaticMethod struct {
	Header   ObjectHeader
	Callable Value
}

var _ [0 - unsafe.Offsetof(PyStaticMethod{}.Header)]byte

// NewStaticMethodVal constructs a heap-allocated KindStaticMethod descriptor Value.
func NewStaticMethodVal(fn Value) Value {
	obj := &PyStaticMethod{
		Header: ObjectHeader{
			Kind: KindStaticMethod,
			Type: StaticMethodType,
		},
		Callable: fn,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsStaticMethod performs a zero-cost offset-0 downcast from Value to *PyStaticMethod.
func (v Value) AsStaticMethod() *PyStaticMethod {
	return (*PyStaticMethod)(unsafe.Pointer(v.ptr))
}

func staticMethodDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	sm := descr.AsStaticMethod()
	return sm.Callable
}

func staticMethodTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) != 1 {
		panic("TypeError: staticmethod() takes exactly one argument")
	}
	return NewStaticMethodVal(args[0])
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

func functionTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	fn := obj.AsFunction()
	nameStr := name.AsString()
	if nameStr == "__name__" {
		return InternString(fn.Name)
	}
	if nameStr == "__annotations__" {
		if fn.Dict != nil {
			if ann, ok := fn.Dict.Get(name); ok {
				return ann
			}
		}
		// Lazy PEP 649 deferred annotation evaluation
		if fn.Dict != nil {
			if annotateFn, ok := fn.Dict.Get(InternString("__annotate__")); ok {
				var annDict Value
				if ctx != nil && ctx.CallHook != nil {
					base := ctx.SP
					ctx.Stack[base] = Int64Val(1) // format 1 = inspect.VALUE
					annDict = ctx.CallHook(ctx, annotateFn, base, 1, nil)
				}
				if annDict.IsDict() {
					fn.Dict.Set(name, annDict)
					return annDict
				}
			}
		}
		emptyDict := NewDictVal(4)
		if fn.Dict == nil {
			fn.Dict = NewDictVal(4).AsDict()
		}
		fn.Dict.Set(name, emptyDict)
		return emptyDict
	}
	if fn.Dict != nil {
		if v, ok := fn.Dict.Get(name); ok {
			return v
		}
	}
	panic("val: AttributeError: 'function' object has no attribute '" + nameStr + "'")
}

func functionTpSetAttr(ctx *ExecContext, obj, name, value Value) Value {
	fn := obj.AsFunction()
	if fn.Dict == nil {
		fn.Dict = NewDictVal(4).AsDict()
	}
	fn.Dict.Set(InternValue(name), value)
	return NoneVal()
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
	var selfType *PyType
	var selfObj Value
	if sup.Self.IsType() {
		selfType = sup.Self.AsType()
		selfObj = NoneVal()
	} else {
		selfType = sup.Self.Type()
		selfObj = sup.Self
	}
	descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, interned)
	if !ok {
		panic("AttributeError: 'super' object has no attribute '" + interned.AsString() + "'")
	}
	if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
		return descrGet(ctx, descr, selfObj, selfType)
	}
	return descr
}

func typeTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	cls := callable.AsType()
	if cls == TypeType {
		if len(args) == 1 && len(kwnames) == 0 {
			return TypeVal(args[0].Type())
		}
		if len(args) == 3 {
			name := args[0].AsString()
			var bases []*PyType
			if args[1].IsTuple() {
				for _, b := range args[1].AsTuple().Items {
					if b.IsType() {
						bases = append(bases, b.AsType())
					}
				}
			} else if args[1].IsType() {
				bases = []*PyType{args[1].AsType()}
			}
			var dict *PyDict
			if args[2].IsDict() {
				dict = args[2].AsDict()
			} else if !args[2].IsNone() && args[2].ptr != nil {
				dict = NewDictVal(16).AsDict()
				if itemsFn, _, ok := args[2].Type().LookupMRO(InternString("items")); ok {
					itemsVal := ctx.Invoke1(itemsFn, args[2])
					if tpIter := itemsVal.Type().Slots.TpIter; tpIter != nil {
						itVal := tpIter(ctx, itemsVal)
						if itNext := itVal.Type().Slots.TpIterNext; itNext != nil {
							for {
								pair, status := itNext(ctx, itVal)
								if status == IterExhausted {
									break
								}
								if pair.IsTuple() && len(pair.AsTuple().Items) == 2 {
									dict.Set(pair.AsTuple().Items[0], pair.AsTuple().Items[1])
								}
							}
						}
					}
				}
			}
			t, err := NewType(name, bases, dict)
			if err != nil {
				panic("TypeError: " + err.Error())
			}
			return TypeVal(t)
		}
	}

	if cls.Slots.TpCall != nil {
		return cls.Slots.TpCall(ctx, callable, args, kwnames)
	}

	var inst Value
	if cls.dunderNew.ptr != nil {
		newArgs := make([]Value, 1+len(args))
		newArgs[0] = callable
		copy(newArgs[1:], args)
		if ctx != nil && ctx.CallHook != nil && ctx.SP+len(newArgs) <= len(ctx.Stack) {
			base := ctx.SP
			copy(ctx.Stack[base:base+len(newArgs)], newArgs)
			inst = ctx.CallHook(ctx, cls.dunderNew, base, len(newArgs), kwnames)
		} else if cls.dunderNew.IsBuiltinFunc() {
			inst = cls.dunderNew.AsBuiltinFunc().Fn(ctx, cls.dunderNew, newArgs, kwnames)
		} else if tpCall := cls.dunderNew.Type().Slots.TpCall; tpCall != nil {
			inst = tpCall(ctx, cls.dunderNew, newArgs, kwnames)
		}
	} else {
		inst = NewInstanceOfType(cls)
	}

	if IsSubclass(inst.Type(), cls) {
		if cls.HasInit() {
			initFn := cls.DunderInitVal()
			if ctx != nil && ctx.SP+1+len(args) <= len(ctx.Stack) {
				base := ctx.SP
				ctx.Stack[base] = inst
				copy(ctx.Stack[base+1:base+1+len(args)], args)
				if initFn.IsBuiltinFunc() {
					initFn.AsBuiltinFunc().Fn(ctx, initFn, ctx.Stack[base:base+1+len(args)], kwnames)
				} else if ctx.CallHook != nil {
					ctx.CallHook(ctx, initFn, base, 1+len(args), kwnames)
				}
			} else {
				buf := make([]Value, 1+len(args))
				buf[0] = inst
				copy(buf[1:], args)
				if initFn.IsBuiltinFunc() {
					initFn.AsBuiltinFunc().Fn(ctx, initFn, buf, kwnames)
				} else if tpCall := initFn.Type().Slots.TpCall; tpCall != nil {
					tpCall(ctx, initFn, buf, kwnames)
				}
			}
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

func listMpSubscript(ctx *ExecContext, a, idxVal Value) Value {
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		items := a.AsList().Items
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(items))
		if err != nil {
			panic(err.Error())
		}
		if sliceLen == 0 {
			return NewListVal()
		}
		if step == 1 {
			res := make([]Value, sliceLen)
			copy(res, items[start:stop])
			return NewListVal(res...)
		}
		res := make([]Value, sliceLen)
		cur := start
		for i := 0; i < sliceLen; i++ {
			res[i] = items[cur]
			cur += step
		}
		return NewListVal(res...)
	}
	return listSqItem(ctx, a, idxVal)
}

func listSqAssItem(ctx *ExecContext, a, idxVal, val Value) Value {
	if !a.AsList().Set(int(idxVal.AsInt64()), val) {
		panic("val: IndexError: list assignment index out of range")
	}
	return NoneVal()
}

func listMpAssSubscript(ctx *ExecContext, a, idxVal, val Value) Value {
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		l := a.AsList()
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(l.Items))
		if err != nil {
			panic(err.Error())
		}
		var rhsItems []Value
		if val.IsList() {
			rhsItems = val.AsList().Items
		} else if val.IsTuple() {
			rhsItems = val.AsTuple().Items
		} else {
			panic("TypeError: can only assign an iterable to a slice")
		}
		if step == 1 {
			newItems := make([]Value, 0, len(l.Items)-sliceLen+len(rhsItems))
			newItems = append(newItems, l.Items[:start]...)
			newItems = append(newItems, rhsItems...)
			newItems = append(newItems, l.Items[stop:]...)
			l.Items = newItems
			return NoneVal()
		}
		if len(rhsItems) != sliceLen {
			panic("ValueError: attempt to assign sequence of size X to extended slice of size Y")
		}
		cur := start
		for i := 0; i < sliceLen; i++ {
			l.Items[cur] = rhsItems[i]
			cur += step
		}
		return NoneVal()
	}
	return listSqAssItem(ctx, a, idxVal, val)
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

func tupleMpSubscript(ctx *ExecContext, a, idxVal Value) Value {
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		items := a.AsTuple().Items
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(items))
		if err != nil {
			panic(err.Error())
		}
		if sliceLen == 0 {
			return EmptyTupleVal()
		}
		if step == 1 {
			// Zero-copy sub-slice view! 0 element copies!
			return NewTupleView(items[start:stop:stop])
		}
		res := make([]Value, sliceLen)
		cur := start
		for i := 0; i < sliceLen; i++ {
			res[i] = items[cur]
			cur += step
		}
		return NewTupleVal(res...)
	}
	return tupleSqItem(ctx, a, idxVal)
}

func bytesSqLen(ctx *ExecContext, a Value) int64 {
	return int64(len(a.AsBytes().Data))
}

func bytesTpAsBuffer(ctx *ExecContext, obj Value, view *PyBuffer, flags int) error {
	return obj.AsBytes().GetBuffer(ctx, view, flags)
}

func bytesMpSubscript(ctx *ExecContext, a, idxVal Value) Value {
	data := a.AsBytes().Data
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(data))
		if err != nil {
			panic(err.Error())
		}
		if sliceLen == 0 {
			return NewBytesVal(nil)
		}
		if step == 1 {
			// Zero-copy sub-slice view! 0 bytes copied!
			return NewBytesVal(data[start:stop:stop])
		}
		res := make([]byte, sliceLen)
		cur := start
		for i := 0; i < sliceLen; i++ {
			res[i] = data[cur]
			cur += step
		}
		return NewBytesVal(res)
	}
	idx := int(idxVal.AsInt64())
	if idx < 0 {
		idx += len(data)
	}
	if idx < 0 || idx >= len(data) {
		panic("val: IndexError: index out of range")
	}
	return Int64Val(int64(data[idx]))
}

func byteArraySqLen(ctx *ExecContext, a Value) int64 {
	return int64(len(a.AsByteArray().Data))
}

func byteArrayTpAsBuffer(ctx *ExecContext, obj Value, view *PyBuffer, flags int) error {
	return obj.AsByteArray().GetBuffer(ctx, view, flags)
}

func byteArrayMpSubscript(ctx *ExecContext, a, idxVal Value) Value {
	ba := a.AsByteArray()
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	data := ba.Data
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(data))
		if err != nil {
			panic(err.Error())
		}
		if sliceLen == 0 {
			return NewByteArrayVal(nil)
		}
		if step == 1 {
			res := make([]byte, sliceLen)
			copy(res, data[start:stop])
			return NewByteArrayVal(res)
		}
		res := make([]byte, sliceLen)
		cur := start
		for i := 0; i < sliceLen; i++ {
			res[i] = data[cur]
			cur += step
		}
		return NewByteArrayVal(res)
	}
	idx := int(idxVal.AsInt64())
	if idx < 0 {
		idx += len(data)
	}
	if idx < 0 || idx >= len(data) {
		panic("val: IndexError: bytearray index out of range")
	}
	return Int64Val(int64(data[idx]))
}

func byteArrayMpAssSubscript(ctx *ExecContext, a, idxVal, val Value) Value {
	ba := a.AsByteArray()
	ba.mu.Lock()
	defer ba.mu.Unlock()
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(ba.Data))
		if err != nil {
			panic(err.Error())
		}
		var rhsBytes []byte
		if val.IsBytes() {
			rhsBytes = val.AsBytes().Data
		} else if val.IsByteArray() {
			rhsBytes = val.AsByteArray().Data
		} else if val.IsMemoryView() {
			rhsBytes = val.AsMemoryView().Buf
		} else {
			panic("TypeError: can't set slice with non-bytes-like object")
		}
		if step == 1 {
			newData := make([]byte, 0, len(ba.Data)-sliceLen+len(rhsBytes))
			newData = append(newData, ba.Data[:start]...)
			newData = append(newData, rhsBytes...)
			newData = append(newData, ba.Data[stop:]...)
			ba.Data = newData
			return NoneVal()
		}
		if len(rhsBytes) != sliceLen {
			panic("ValueError: attempt to assign sequence of size X to extended slice of size Y")
		}
		cur := start
		for i := 0; i < sliceLen; i++ {
			ba.Data[cur] = rhsBytes[i]
			cur += step
		}
		return NoneVal()
	}
	idx := int(idxVal.AsInt64())
	if idx < 0 {
		idx += len(ba.Data)
	}
	if idx < 0 || idx >= len(ba.Data) {
		panic("val: IndexError: bytearray assignment index out of range")
	}
	b := val.AsInt64()
	if b < 0 || b > 255 {
		panic("ValueError: byte must be in range(0, 256)")
	}
	ba.Data[idx] = byte(b)
	return NoneVal()
}

func memoryViewSqLen(ctx *ExecContext, a Value) int64 {
	return int64(len(a.AsMemoryView().Buf))
}

func memoryViewTpAsBuffer(ctx *ExecContext, obj Value, view *PyBuffer, flags int) error {
	return obj.AsMemoryView().GetBuffer(ctx, view, flags)
}

func memoryViewMpSubscript(ctx *ExecContext, a, idxVal Value) Value {
	mv := a.AsMemoryView()
	if mv.released {
		panic("ValueError: operation on released memoryview")
	}
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(mv.Buf))
		if err != nil {
			panic(err.Error())
		}
		if step != 1 {
			panic("NotImplementedError: non-contiguous memoryview slice not supported")
		}
		if sliceLen == 0 {
			obj := &PyMemoryView{
				Header: ObjectHeader{
					Kind: KindMemoryView,
					Type: MemoryViewType,
				},
				Obj:      mv.Obj,
				Buf:      mv.Buf[start:start:start],
				ReadOnly: mv.ReadOnly,
				ItemSize: mv.ItemSize,
			}
			return Value{num: 0, ptr: &obj.Header}
		}
		// Zero-copy sub-slice view! 0 bytes copied, shares underlying buffer!
		obj := &PyMemoryView{
			Header: ObjectHeader{
				Kind: KindMemoryView,
				Type: MemoryViewType,
			},
			Obj:      mv.Obj,
			Buf:      mv.Buf[start:stop:stop],
			ReadOnly: mv.ReadOnly,
			ItemSize: mv.ItemSize,
		}
		return Value{num: 0, ptr: &obj.Header}
	}
	idx := int(idxVal.AsInt64())
	if idx < 0 {
		idx += len(mv.Buf)
	}
	if idx < 0 || idx >= len(mv.Buf) {
		panic("IndexError: index out of bounds")
	}
	return Int64Val(int64(mv.Buf[idx]))
}

func memoryViewMpAssSubscript(ctx *ExecContext, a, idxVal, val Value) Value {
	mv := a.AsMemoryView()
	if mv.released {
		panic("ValueError: operation on released memoryview")
	}
	if mv.ReadOnly {
		panic("TypeError: cannot modify read-only memoryview")
	}
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(mv.Buf))
		if err != nil {
			panic(err.Error())
		}
		if step != 1 {
			panic("NotImplementedError: non-contiguous memoryview slice assignment")
		}
		var rhsBytes []byte
		if val.IsBytes() {
			rhsBytes = val.AsBytes().Data
		} else if val.IsByteArray() {
			rhsBytes = val.AsByteArray().Data
		} else if val.IsMemoryView() {
			rhsBytes = val.AsMemoryView().Buf
		} else {
			panic("TypeError: must assign bytes-like object")
		}
		if len(rhsBytes) != sliceLen {
			panic("ValueError: cannot resize memoryview")
		}
		copy(mv.Buf[start:stop], rhsBytes)
		return NoneVal()
	}
	idx := int(idxVal.AsInt64())
	if idx < 0 {
		idx += len(mv.Buf)
	}
	if idx < 0 || idx >= len(mv.Buf) {
		panic("IndexError: index out of bounds")
	}
	b := val.AsInt64()
	if b < 0 || b > 255 {
		panic("ValueError: byte must be in range(0, 256)")
	}
	mv.Buf[idx] = byte(b)
	return NoneVal()
}

func sliceGetAttr(ctx *ExecContext, obj, name Value) Value {
	sl := obj.AsSlice()
	attr := name.AsString()
	switch attr {
	case "start":
		return sl.Start
	case "stop":
		return sl.Stop
	case "step":
		return sl.Step
	default:
		panic("AttributeError: 'slice' object has no attribute '" + attr + "'")
	}
}

func dictSqLen(ctx *ExecContext, a Value) int64 {
	return int64(a.AsDict().Len())
}

func dictMpSubscript(ctx *ExecContext, a, key Value) Value {
	v, ok := a.AsDict().Get(key)
	if !ok {
		if a.Type() != DictType && a.Type().dunderMissing.ptr != nil {
			return ctx.Invoke2(a.Type().dunderMissing, a, key)
		}
		panic("val: KeyError: " + key.String())
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

func bytesNbAdd(ctx *ExecContext, a, b Value) Value {
	var aBytes, bBytes []byte
	if a.IsBytes() {
		aBytes = a.AsBytes().Data
	} else {
		panic("TypeError: can't concat " + a.Type().Name + " to bytes")
	}
	if b.IsBytes() {
		bBytes = b.AsBytes().Data
	} else if b.IsByteArray() {
		bBytes = b.AsByteArray().Data
	} else {
		panic("TypeError: can't concat " + b.Type().Name + " to bytes")
	}
	res := make([]byte, len(aBytes)+len(bBytes))
	copy(res, aBytes)
	copy(res[len(aBytes):], bBytes)
	return NewBytesVal(res)
}

func bytesTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) == 0 {
		return NewBytesVal(nil)
	}
	arg := args[0]
	if arg.IsInt64() {
		n := int(arg.AsInt64())
		if n < 0 {
			panic("ValueError: negative count")
		}
		return NewBytesVal(make([]byte, n))
	}
	if arg.IsBytes() {
		return arg
	}
	if arg.IsByteArray() {
		ba := arg.AsByteArray()
		res := make([]byte, len(ba.Data))
		copy(res, ba.Data)
		return NewBytesVal(res)
	}
	if arg.IsMemoryView() {
		mv := arg.AsMemoryView()
		res := make([]byte, len(mv.Buf))
		copy(res, mv.Buf)
		return NewBytesVal(res)
	}
	if arg.IsAnyString() {
		return NewBytesVal([]byte(arg.AsString()))
	}
	if arg.IsList() {
		items := arg.AsList().Items
		res := make([]byte, len(items))
		for i, it := range items {
			b := it.AsInt64()
			if b < 0 || b > 255 {
				panic("ValueError: bytes must be in range(0, 256)")
			}
			res[i] = byte(b)
		}
		return NewBytesVal(res)
	}
	if arg.IsTuple() {
		items := arg.AsTuple().Items
		res := make([]byte, len(items))
		for i, it := range items {
			b := it.AsInt64()
			if b < 0 || b > 255 {
				panic("ValueError: bytes must be in range(0, 256)")
			}
			res[i] = byte(b)
		}
		return NewBytesVal(res)
	}
	panic("TypeError: cannot convert '" + arg.Type().Name + "' object to bytes")
}

func byteArrayTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) == 0 {
		return NewByteArrayVal(nil)
	}
	arg := args[0]
	if arg.IsInt64() {
		n := int(arg.AsInt64())
		if n < 0 {
			panic("ValueError: negative count")
		}
		return NewByteArrayVal(make([]byte, n))
	}
	if arg.IsBytes() {
		res := make([]byte, len(arg.AsBytes().Data))
		copy(res, arg.AsBytes().Data)
		return NewByteArrayVal(res)
	}
	if arg.IsByteArray() {
		ba := arg.AsByteArray()
		res := make([]byte, len(ba.Data))
		copy(res, ba.Data)
		return NewByteArrayVal(res)
	}
	if arg.IsMemoryView() {
		mv := arg.AsMemoryView()
		res := make([]byte, len(mv.Buf))
		copy(res, mv.Buf)
		return NewByteArrayVal(res)
	}
	if arg.IsAnyString() {
		return NewByteArrayVal([]byte(arg.AsString()))
	}
	if arg.IsList() {
		items := arg.AsList().Items
		res := make([]byte, len(items))
		for i, it := range items {
			b := it.AsInt64()
			if b < 0 || b > 255 {
				panic("ValueError: byte must be in range(0, 256)")
			}
			res[i] = byte(b)
		}
		return NewByteArrayVal(res)
	}
	if arg.IsTuple() {
		items := arg.AsTuple().Items
		res := make([]byte, len(items))
		for i, it := range items {
			b := it.AsInt64()
			if b < 0 || b > 255 {
				panic("ValueError: byte must be in range(0, 256)")
			}
			res[i] = byte(b)
		}
		return NewByteArrayVal(res)
	}
	panic("TypeError: cannot convert '" + arg.Type().Name + "' object to bytearray")
}

func memoryViewTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) < 1 {
		panic("TypeError: memoryview() takes at least 1 argument")
	}
	return NewMemoryViewVal(args[0])
}

func sliceTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) == 1 {
		return NewSliceVal(NoneVal(), args[0], NoneVal())
	}
	if len(args) == 2 {
		return NewSliceVal(args[0], args[1], NoneVal())
	}
	if len(args) >= 3 {
		return NewSliceVal(args[0], args[1], args[2])
	}
	panic("TypeError: slice() takes at least 1 argument")
}

func typeMpSubscript(ctx *ExecContext, a, b Value) Value {
	clsName := a.Type().Name
	if a.IsType() {
		clsName = a.AsType().Name
	}
	argName := b.Type().Name
	if b.IsType() {
		argName = b.AsType().Name
	}
	t := MustNewType(clsName+"["+argName+"]", []*PyType{ObjectType}, nil)
	return TypeVal(t)
}

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

	// Rich comparisons
	DunderLt = InternString("__lt__")
	DunderLe = InternString("__le__")
	DunderEq = InternString("__eq__")
	DunderNe = InternString("__ne__")
	DunderGt = InternString("__gt__")
	DunderGe = InternString("__ge__")

	// Reflected arithmetic
	DunderRAdd = InternString("__radd__")
	DunderRSub = InternString("__rsub__")
	DunderRMul = InternString("__rmul__")
	DunderRMatMul = InternString("__rmatmul__")
	DunderRTruediv = InternString("__rtruediv__")
	DunderRFloordiv = InternString("__rfloordiv__")
	DunderRMod = InternString("__rmod__")
	DunderRDivMod = InternString("__rdivmod__")
	DunderRPow = InternString("__rpow__")
	DunderRLShift = InternString("__rlshift__")
	DunderRRShift = InternString("__rrshift__")
	DunderRAnd = InternString("__rand__")
	DunderRXor = InternString("__rxor__")
	DunderROr = InternString("__ror__")

	// In-place arithmetic
	DunderIAdd = InternString("__iadd__")
	DunderISub = InternString("__isub__")
	DunderIMul = InternString("__imul__")
	DunderIMatMul = InternString("__imatmul__")
	DunderITruediv = InternString("__itruediv__")
	DunderIFloordiv = InternString("__ifloordiv__")
	DunderIMod = InternString("__imod__")
	DunderIPow = InternString("__ipow__")
	DunderILShift = InternString("__ilshift__")
	DunderIRShift = InternString("__irshift__")
	DunderIAnd = InternString("__iand__")
	DunderIXor = InternString("__ixor__")
	DunderIOr = InternString("__ior__")

	// Reflection, attributes & lifecycle
	DunderGetAttribute = InternString("__getattribute__")
	DunderDelAttr = InternString("__delattr__")
	DunderDir = InternString("__dir__")
	DunderContains = InternString("__contains__")
	DunderMissing = InternString("__missing__")
	DunderNew = InternString("__new__")
	DunderSetName = InternString("__set_name__")
	DunderInitSubclass = InternString("__init_subclass__")
	DunderPrepare = InternString("__prepare__")
	DunderMroEntries = InternString("__mro_entries__")
	DunderOrigBases = InternString("__orig_bases__")
	DunderSlots = InternString("__slots__")

	// 2. Bootstrap ObjectType and TypeType.
	ObjectType = &PyType{
		Header:     ObjectHeader{Kind: KindType},
		Name:       "object",
		RootShape:  defaultRootShape,
		VersionTag: allocTypeVersion(),
	}
	defaultRootShape.Type = ObjectType
	ObjectType.MRO = []*PyType{ObjectType}
	ObjectType.Slots.TpGetAttr = objectGetAttr
	ObjectType.Slots.TpSetAttr = objectSetAttr

	TypeType = &PyType{
		Header:     ObjectHeader{Kind: KindType},
		Name:       "type",
		Bases:      []*PyType{ObjectType},
		MRO:        []*PyType{nil, ObjectType},
		RootShape:  NewRootShapeForType(nil),
		VersionTag: allocTypeVersion(),
	}
	TypeType.RootShape.Type = TypeType
	TypeType.MRO[0] = TypeType
	TypeType.Header.Type = TypeType
	ObjectType.Header.Type = TypeType
	ObjectType.Dict = NewDictVal(8).AsDict()
	TypeType.Dict = NewDictVal(8).AsDict()
	TypeType.Slots.TpGetAttr = typeGetAttr
	TypeType.Slots.TpSetAttr = typeSetAttr
	TypeType.Slots.TpCall = typeTpCall
	TypeType.Slots.MpSubscript = typeMpSubscript

	// 3. Construct built-in primitive and heap object types.
	NoneType = MustNewType("NoneType", []*PyType{ObjectType}, nil)
	IntType = MustNewType("int", []*PyType{ObjectType}, nil)
	IntType.Slots.NbAdd = numericNbAdd
	IntType.Slots.NbSub = numericNbSub
	IntType.Slots.NbMul = numericNbMul
	IntType.Slots.NbAbs = intNbAbs

	intNewBuiltin := NewBuiltinFuncVal("int.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		var valToConvert Value = Int64Val(0)
		if len(args) > 1 {
			valToConvert = args[1]
		}
		var num int64
		if valToConvert.IsInt64() {
			num = valToConvert.AsInt64()
		} else if valToConvert.IsFloat64() {
			num = int64(valToConvert.AsFloat64())
		} else if valToConvert.IsBool() {
			if valToConvert.AsBool() {
				num = 1
			}
		} else if valToConvert.IsAnyString() {
			n, _ := strconv.ParseInt(strings.TrimSpace(valToConvert.AsString()), 10, 64)
			num = n
		} else if valToConvert.IsInstance() {
			inst := valToConvert.AsInstance()
			if len(inst.Slots) > 0 && inst.Slots[0].IsInt64() {
				num = inst.Slots[0].AsInt64()
			}
		}
		if len(args) > 0 && args[0].IsType() && args[0].AsType() == IntType {
			return Int64Val(num)
		}
		cls := IntType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		inst := NewInstanceOfType(cls)
		inst.AsInstance().Slots = []Value{Int64Val(num)}
		return inst
	})
	IntType.Dict.Set(DunderNew, intNewBuiltin)
	IntType.dunderNew = intNewBuiltin

	IntType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return Int64Val(0)
		}
		a := args[0]
		if a.IsInt64() {
			return a
		}
		if a.IsFloat64() {
			return Int64Val(int64(a.AsFloat64()))
		}
		if a.IsBool() {
			if a.AsBool() {
				return Int64Val(1)
			}
			return Int64Val(0)
		}
		if a.IsAnyString() {
			n, err := strconv.ParseInt(strings.TrimSpace(a.AsString()), 10, 64)
			if err == nil {
				return Int64Val(n)
			}
		}
		if a.IsInstance() {
			inst := a.AsInstance()
			if len(inst.Slots) > 0 && inst.Slots[0].IsInt64() {
				return inst.Slots[0]
			}
			if fn, _, ok := a.Type().LookupMRO(InternString("__int__")); ok {
				return ctx.Invoke1(fn, a)
			}
			if fn, _, ok := a.Type().LookupMRO(InternString("__index__")); ok {
				return ctx.Invoke1(fn, a)
			}
		}
		panic("TypeError: int() argument must be a string or number")
	}

	BoolType = MustNewType("bool", []*PyType{IntType}, nil)
	BoolType.Slots.NbAdd = numericNbAdd
	BoolType.Slots.NbSub = numericNbSub
	BoolType.Slots.NbMul = numericNbMul
	BoolType.Slots.NbAbs = intNbAbs
	BoolType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return BoolVal(false)
		}
		return BoolVal(args[0].IsTruthy())
	}

	FloatType = MustNewType("float", []*PyType{ObjectType}, nil)
	FloatType.Slots.NbAdd = numericNbAdd
	FloatType.Slots.NbSub = numericNbSub
	FloatType.Slots.NbMul = numericNbMul
	FloatType.Slots.NbAbs = floatNbAbs
	FloatType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return Float64Val(0.0)
		}
		a := args[0]
		if a.IsFloat64() {
			return a
		}
		if a.IsInt64() {
			return Float64Val(float64(a.AsInt64()))
		}
		if a.IsBool() {
			if a.AsBool() {
				return Float64Val(1.0)
			}
			return Float64Val(0.0)
		}
		if a.IsAnyString() {
			f, err := strconv.ParseFloat(strings.TrimSpace(a.AsString()), 64)
			if err == nil {
				return Float64Val(f)
			}
		}
		panic("TypeError: float() argument must be a string or number")
	}

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
	StrType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return NewStringVal("")
		}
		a := args[0]
		if a.IsInt64() {
			return IntToStringVal(a.AsInt64())
		}
		if a.IsAnyString() {
			return a
		}
		return NewStringVal(a.String())
	}

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
	ListType.Slots.MpSubscript = listMpSubscript
	ListType.Slots.SqAssItem = listSqAssItem
	ListType.Slots.MpAssSubscript = listMpAssSubscript
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

	dictNewBuiltin := NewBuiltinFuncVal("dict.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		cls := DictType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		d := NewDictVal(16).AsDict()
		d.Header.Type = cls
		return Value{num: 0, ptr: &d.Header}
	})
	DictType.Dict.Set(DunderNew, dictNewBuiltin)
	DictType.dunderNew = dictNewBuiltin

	DictType.Dict.Set(DunderGetItem, NewBuiltinFuncVal("__getitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__getitem__' of 'dict' object needs an argument")
		}
		return dictMpSubscript(ctx, args[0], args[1])
	}))
	DictType.Dict.Set(DunderSetItem, NewBuiltinFuncVal("__setitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: descriptor '__setitem__' of 'dict' object needs 2 arguments")
		}
		dictMpAssSubscript(ctx, args[0], args[1], args[2])
		return NoneVal()
	}))

	TupleType = MustNewType("tuple", []*PyType{ObjectType}, nil)
	TupleType.Slots.SqLen = tupleSqLen
	TupleType.Slots.SqItem = tupleSqItem
	TupleType.Slots.MpSubscript = tupleMpSubscript

	CodeType = MustNewType("code", []*PyType{ObjectType}, nil)
	CodeType.Slots.TpCall = functionTpCall

	FunctionType = MustNewType("function", []*PyType{ObjectType}, nil)
	FunctionType.Slots.TpDescrGet = functionDescrGet
	FunctionType.Slots.TpCall = functionTpCall
	FunctionType.Slots.TpGetAttr = functionTpGetAttr
	FunctionType.Slots.TpSetAttr = functionTpSetAttr

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

	MemberDescrType = MustNewType("member_descriptor", []*PyType{ObjectType}, nil)
	MemberDescrType.Slots.TpDescrGet = memberDescrTpDescrGet
	MemberDescrType.Slots.TpDescrSet = memberDescrTpDescrSet

	ClassMethodType = MustNewType("classmethod", []*PyType{ObjectType}, nil)
	ClassMethodType.Slots.TpDescrGet = classMethodDescrGet
	ClassMethodType.Slots.TpCall = classMethodTpCall

	StaticMethodType = MustNewType("staticmethod", []*PyType{ObjectType}, nil)
	StaticMethodType.Slots.TpDescrGet = staticMethodDescrGet
	StaticMethodType.Slots.TpCall = staticMethodTpCall

	NotImplementedType = MustNewType("NotImplementedType", []*PyType{ObjectType}, nil)

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

	CoroutineType = MustNewType("coroutine", []*PyType{ObjectType}, nil)
	CoroutineType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return o }
	CoroutineType.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
		coro := o.AsCoroutine()
		return coro.Send(ctx, NoneVal())
	}
	CoroutineType.Dict.Set(InternString("send"), NewBuiltinFuncVal("send", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: send() takes exactly one argument")
		}
		coro := args[0].AsCoroutine()
		v, status := coro.Send(ctx, args[1])
		if status == IterExhausted {
			ctx.Raise(NewStopIteration(v))
		}
		return v
	}))
	CoroutineType.Dict.Set(InternString("close"), NewBuiltinFuncVal("close", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: close() takes at least 1 argument")
		}
		coro := args[0].AsCoroutine()
		coro.Close(ctx)
		return NoneVal()
	}))
	CoroutineType.Dict.Set(InternString("throw"), NewBuiltinFuncVal("throw", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: throw() takes at least 1 argument")
		}
		coro := args[0].AsCoroutine()
		v, _ := coro.Throw(ctx, args[1])
		return v
	}))
	CoroutineType.Dict.Set(InternString("__await__"), NewBuiltinFuncVal("__await__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: __await__() takes at least 1 argument")
		}
		return args[0]
	}))

	FutureType = MustNewType("Future", []*PyType{ObjectType}, nil)
	FutureType.Dict.Set(InternString("done"), NewBuiltinFuncVal("done", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: done() takes at least 1 argument")
		}
		return BoolVal(args[0].AsFuture().IsDone())
	}))
	FutureType.Dict.Set(InternString("result"), NewBuiltinFuncVal("result", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: result() takes at least 1 argument")
		}
		fut := args[0].AsFuture()
		res, exc := fut.Result()
		if !exc.IsNone() && exc.HeaderPtr() != nil {
			if ctx != nil {
				ctx.Raise(exc)
			}
			return NoneVal()
		}
		return res
	}))
	FutureType.Dict.Set(InternString("set_result"), NewBuiltinFuncVal("set_result", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: set_result() takes exactly one argument")
		}
		args[0].AsFuture().SetResult(args[1])
		return NoneVal()
	}))
	FutureType.Dict.Set(InternString("set_exception"), NewBuiltinFuncVal("set_exception", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: set_exception() takes exactly one argument")
		}
		args[0].AsFuture().SetException(args[1])
		return NoneVal()
	}))
	FutureType.Dict.Set(InternString("__await__"), NewBuiltinFuncVal("__await__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: __await__() takes at least 1 argument")
		}
		return args[0]
	}))

	TaskType = MustNewType("Task", []*PyType{FutureType}, nil)
	if fn, ok := FutureType.Dict.Get(InternString("done")); ok {
		TaskType.Dict.Set(InternString("done"), fn)
	}
	if fn, ok := FutureType.Dict.Get(InternString("result")); ok {
		TaskType.Dict.Set(InternString("result"), fn)
	}
	if fn, ok := FutureType.Dict.Get(InternString("__await__")); ok {
		TaskType.Dict.Set(InternString("__await__"), fn)
	}

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
	StopAsyncIterationType = MustNewType("StopAsyncIteration", []*PyType{ExceptionType}, nil)
	GeneratorExitType = MustNewType("GeneratorExit", []*PyType{BaseExceptionType}, nil)
	ImportErrorType = MustNewType("ImportError", []*PyType{ExceptionType}, nil)
	ModuleNotFoundErrorType = MustNewType("ModuleNotFoundError", []*PyType{ImportErrorType}, nil)
	BufferErrorType = MustNewType("BufferError", []*PyType{ExceptionType}, nil)
	AssertionErrorType = MustNewType("AssertionError", []*PyType{ExceptionType}, nil)
	BaseExceptionGroupType = MustNewType("BaseExceptionGroup", []*PyType{BaseExceptionType}, nil)
	ExceptionGroupType = MustNewType("ExceptionGroup", []*PyType{BaseExceptionGroupType, ExceptionType}, nil)
	egCall := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		cls := ExceptionGroupType
		if callable.IsType() {
			cls = callable.AsType()
		}
		msg := ""
		if len(args) > 0 {
			msg = args[0].AsString()
		}
		var excs []Value
		if len(args) > 1 {
			if args[1].IsList() {
				excs = append(excs, args[1].AsList().Items...)
			} else if args[1].IsTuple() {
				excs = append(excs, args[1].AsTuple().Items...)
			}
		}
		return NewExceptionGroupVal(cls, msg, excs)
	}
	BaseExceptionGroupType.Slots.TpCall = egCall
	ExceptionGroupType.Slots.TpCall = egCall

	// Set Type
	SetType = MustNewType("set", []*PyType{ObjectType}, nil)
	SetType.Slots.SqLen = func(ctx *ExecContext, o Value) int64 {
		if o.IsSet() {
			return int64(o.AsSet().Len())
		}
		return 0
	}
	SetType.Dict.Set(InternString("add"), NewBuiltinFuncVal("add", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: add() takes exactly 1 argument")
		}
		if args[0].IsSet() {
			args[0].AsSet().Add(args[1])
		}
		return NoneVal()
	}))

	// PEP 695 Type Parameter types
	TypeVarType = MustNewType("TypeVar", []*PyType{ObjectType}, nil)
	TypeVarType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		name := ""
		bound := NoneVal()
		if len(args) > 0 {
			name = args[0].AsString()
		}
		if len(args) > 1 {
			bound = args[1]
		}
		return NewTypeVarVal(name, bound)
	}
	TypeVarTupleType = MustNewType("TypeVarTuple", []*PyType{ObjectType}, nil)
	TypeVarTupleType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		name := ""
		if len(args) > 0 {
			name = args[0].AsString()
		}
		return NewTypeVarTupleVal(name)
	}
	ParamSpecType = MustNewType("ParamSpec", []*PyType{ObjectType}, nil)
	ParamSpecType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		name := ""
		if len(args) > 0 {
			name = args[0].AsString()
		}
		return NewParamSpecVal(name)
	}
	TypeAliasTypeType = MustNewType("TypeAliasType", []*PyType{ObjectType}, nil)
	TypeAliasTypeType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		name := ""
		var targetVal Value
		var typeParams []Value
		if len(args) > 0 {
			name = args[0].AsString()
		}
		if len(args) > 1 {
			targetVal = args[1]
		}
		if len(args) > 2 && args[2].IsTuple() {
			typeParams = args[2].AsTuple().Items
		}
		return NewTypeAliasTypeVal(name, targetVal, typeParams)
	}

	// Binary Types and Slice Type
	BytesType = MustNewType("bytes", []*PyType{ObjectType}, nil)
	BytesType.Slots.SqLen = bytesSqLen
	BytesType.Slots.SqItem = bytesMpSubscript
	BytesType.Slots.MpSubscript = bytesMpSubscript
	BytesType.Slots.TpAsBuffer = bytesTpAsBuffer
	BytesType.Slots.NbAdd = bytesNbAdd
	BytesType.Slots.TpCall = bytesTpCall
	BytesType.Dict.Set(InternString("decode"), NewBuiltinFuncVal("decode", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: decode() takes at least 1 argument")
		}
		b := args[0].AsBytes()
		return NewStringVal(string(b.Data))
	}))
	BytesType.Dict.Set(InternString("hex"), NewBuiltinFuncVal("hex", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: hex() takes at least 1 argument")
		}
		b := args[0].AsBytes()
		return NewStringVal(hex.EncodeToString(b.Data))
	}))
	BytesType.Dict.Set(InternString("startswith"), NewBuiltinFuncVal("startswith", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: startswith() takes 1 argument")
		}
		b := args[0].AsBytes().Data
		var prefix []byte
		if args[1].IsBytes() {
			prefix = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			prefix = args[1].AsByteArray().Data
		} else {
			panic("TypeError: startswith first arg must be bytes or a tuple of bytes")
		}
		return BoolVal(bytes.HasPrefix(b, prefix))
	}))
	BytesType.Dict.Set(InternString("endswith"), NewBuiltinFuncVal("endswith", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: endswith() takes 1 argument")
		}
		b := args[0].AsBytes().Data
		var suffix []byte
		if args[1].IsBytes() {
			suffix = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			suffix = args[1].AsByteArray().Data
		} else {
			panic("TypeError: endswith first arg must be bytes or a tuple of bytes")
		}
		return BoolVal(bytes.HasSuffix(b, suffix))
	}))
	BytesType.Dict.Set(InternString("find"), NewBuiltinFuncVal("find", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: find() takes 1 argument")
		}
		b := args[0].AsBytes().Data
		var sub []byte
		if args[1].IsBytes() {
			sub = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			sub = args[1].AsByteArray().Data
		} else {
			panic("TypeError: find argument must be bytes")
		}
		return Int64Val(int64(bytes.Index(b, sub)))
	}))

	ByteArrayType = MustNewType("bytearray", []*PyType{ObjectType}, nil)
	ByteArrayType.Slots.SqLen = byteArraySqLen
	ByteArrayType.Slots.SqItem = byteArrayMpSubscript
	ByteArrayType.Slots.MpSubscript = byteArrayMpSubscript
	ByteArrayType.Slots.SqAssItem = byteArrayMpAssSubscript
	ByteArrayType.Slots.MpAssSubscript = byteArrayMpAssSubscript
	ByteArrayType.Slots.TpAsBuffer = byteArrayTpAsBuffer
	ByteArrayType.Slots.TpCall = byteArrayTpCall
	ByteArrayType.Dict.Set(InternString("append"), NewBuiltinFuncVal("append", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: append() takes exactly one argument")
		}
		ba := args[0].AsByteArray()
		b := args[1].AsInt64()
		if b < 0 || b > 255 {
			panic("ValueError: byte must be in range(0, 256)")
		}
		ba.mu.Lock()
		ba.Data = append(ba.Data, byte(b))
		ba.mu.Unlock()
		return NoneVal()
	}))
	ByteArrayType.Dict.Set(InternString("extend"), NewBuiltinFuncVal("extend", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: extend() takes exactly one argument")
		}
		ba := args[0].AsByteArray()
		val := args[1]
		var toAppend []byte
		if val.IsBytes() {
			toAppend = val.AsBytes().Data
		} else if val.IsByteArray() {
			toAppend = val.AsByteArray().Data
		} else if val.IsMemoryView() {
			toAppend = val.AsMemoryView().Buf
		} else if val.IsList() {
			for _, item := range val.AsList().Items {
				b := item.AsInt64()
				if b < 0 || b > 255 {
					panic("ValueError: byte must be in range(0, 256)")
				}
				toAppend = append(toAppend, byte(b))
			}
		} else {
			panic("TypeError: 'extend' requires an iterable of ints or bytes-like object")
		}
		ba.mu.Lock()
		ba.Data = append(ba.Data, toAppend...)
		ba.mu.Unlock()
		return NoneVal()
	}))
	ByteArrayType.Dict.Set(InternString("decode"), NewBuiltinFuncVal("decode", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: decode() takes at least 1 argument")
		}
		ba := args[0].AsByteArray()
		ba.mu.RLock()
		s := string(ba.Data)
		ba.mu.RUnlock()
		return NewStringVal(s)
	}))
	ByteArrayType.Dict.Set(InternString("hex"), NewBuiltinFuncVal("hex", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: hex() takes at least 1 argument")
		}
		ba := args[0].AsByteArray()
		ba.mu.RLock()
		s := hex.EncodeToString(ba.Data)
		ba.mu.RUnlock()
		return NewStringVal(s)
	}))

	MemoryViewType = MustNewType("memoryview", []*PyType{ObjectType}, nil)
	MemoryViewType.Slots.SqLen = memoryViewSqLen
	MemoryViewType.Slots.SqItem = memoryViewMpSubscript
	MemoryViewType.Slots.MpSubscript = memoryViewMpSubscript
	MemoryViewType.Slots.SqAssItem = memoryViewMpAssSubscript
	MemoryViewType.Slots.MpAssSubscript = memoryViewMpAssSubscript
	MemoryViewType.Slots.TpAsBuffer = memoryViewTpAsBuffer
	MemoryViewType.Slots.TpCall = memoryViewTpCall
	MemoryViewType.Dict.Set(InternString("tobytes"), NewBuiltinFuncVal("tobytes", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: tobytes() takes no arguments")
		}
		mv := args[0].AsMemoryView()
		if mv.released {
			panic("ValueError: operation on released memoryview")
		}
		res := make([]byte, len(mv.Buf))
		copy(res, mv.Buf)
		return NewBytesVal(res)
	}))
	MemoryViewType.Dict.Set(InternString("tolist"), NewBuiltinFuncVal("tolist", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: tolist() takes no arguments")
		}
		mv := args[0].AsMemoryView()
		if mv.released {
			panic("ValueError: operation on released memoryview")
		}
		items := make([]Value, len(mv.Buf))
		for i, b := range mv.Buf {
			items[i] = Int64Val(int64(b))
		}
		return NewListVal(items...)
	}))
	MemoryViewType.Dict.Set(InternString("release"), NewBuiltinFuncVal("release", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: release() takes no arguments")
		}
		mv := args[0].AsMemoryView()
		mv.released = true
		mv.Buf = nil
		return NoneVal()
	}))

	SliceType = MustNewType("slice", []*PyType{ObjectType}, nil)
	SliceType.Slots.TpGetAttr = sliceGetAttr
	SliceType.Slots.TpCall = sliceTpCall
	SliceType.Dict.Set(InternString("indices"), NewBuiltinFuncVal("indices", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: slice.indices() takes 1 argument")
		}
		sl := args[0].AsSlice()
		length := int(args[1].AsInt64())
		start, stop, step, _, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, length)
		if err != nil {
			panic(err.Error())
		}
		return NewTupleVal(Int64Val(int64(start)), Int64Val(int64(stop)), Int64Val(int64(step)))
	}))

	ModuleType = MustNewType("module", []*PyType{ObjectType}, nil)
	ModuleType.Slots.TpGetAttr = moduleGetAttr
	ModuleType.Slots.TpSetAttr = moduleSetAttr

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
	builtinTypes[KindCoroutine] = CoroutineType
	builtinTypes[KindFuture] = FutureType
	builtinTypes[KindTask] = TaskType
	builtinTypes[KindChan] = ChanType
	builtinTypes[KindModule] = ModuleType
	builtinTypes[KindBytes] = BytesType
	builtinTypes[KindByteArray] = ByteArrayType
	builtinTypes[KindMemoryView] = MemoryViewType
	builtinTypes[KindSlice] = SliceType
	builtinTypes[KindMemberDescr] = MemberDescrType
	builtinTypes[KindClassMethod] = ClassMethodType
	builtinTypes[KindStaticMethod] = StaticMethodType
	builtinTypes[KindNotImplemented] = NotImplementedType

	tagNone.Type = NoneType
	tagBool.Type = BoolType
	tagInt64.Type = IntType
	tagFloat64.Type = FloatType
	tagInlineStr.Type = StrType
	tagUnbound.Type = NoneType
	tagRange.Type = RangeType
	tagNotImplemented.Type = NotImplementedType

	// 5. Initialize standard native built-in callables.
	BuiltinLenVal = NewBuiltinFuncVal("len", BuiltinLen)
	BuiltinAbsVal = NewBuiltinFuncVal("abs", BuiltinAbs)

	typeNewBuiltin := NewBuiltinFuncVal("type.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 4 {
			panic("TypeError: type.__new__() takes 4 arguments")
		}
		mcls := args[0]
		name := args[1].AsString()
		var bases []*PyType
		if args[2].IsTuple() {
			for _, b := range args[2].AsTuple().Items {
				if b.IsType() {
					bases = append(bases, b.AsType())
				}
			}
		} else if args[2].IsType() {
			bases = []*PyType{args[2].AsType()}
		}
		var dict *PyDict
		if args[3].IsDict() {
			dict = args[3].AsDict()
		} else if !args[3].IsNone() && args[3].ptr != nil {
			dict = NewDictVal(16).AsDict()
			if itemsFn, _, ok := args[3].Type().LookupMRO(InternString("items")); ok {
				itemsVal := ctx.Invoke1(itemsFn, args[3])
				if tpIter := itemsVal.Type().Slots.TpIter; tpIter != nil {
					itVal := tpIter(ctx, itemsVal)
					if itNext := itVal.Type().Slots.TpIterNext; itNext != nil {
						for {
							pair, status := itNext(ctx, itVal)
							if status == IterExhausted {
								break
							}
							if pair.IsTuple() && len(pair.AsTuple().Items) == 2 {
								dict.Set(pair.AsTuple().Items[0], pair.AsTuple().Items[1])
							}
						}
					}
				}
			}
		}
		var metaType *PyType = TypeType
		if mcls.IsType() {
			metaType = mcls.AsType()
		}
		t, err := NewTypeWithMeta(name, bases, dict, metaType)
		if err != nil {
			panic("TypeError: " + err.Error())
		}
		return TypeVal(t)
	})
	TypeType.Dict.Set(DunderNew, typeNewBuiltin)

	objectInitSubclassBuiltin := NewBuiltinFuncVal("object.__init_subclass__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(kwnames) > 0 {
			panic("TypeError: __init_subclass__() takes no keyword arguments")
		}
		return NoneVal()
	})
	ObjectType.Dict.Set(DunderInitSubclass, objectInitSubclassBuiltin)

	objectGetAttributeBuiltin := NewBuiltinFuncVal("object.__getattribute__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: object.__getattribute__() takes 2 arguments")
		}
		return defaultObjectGetAttr(ctx, args[0], args[1])
	})
	ObjectType.Dict.Set(DunderGetAttribute, objectGetAttributeBuiltin)

	ObjectType.syncDunderSlots()
	TypeType.syncDunderSlots()
}
