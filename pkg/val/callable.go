package val

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"
)

// PyTuple represents an immutable sequence of Values on the heap (used for kwnames and tuple constants).
// Header MUST be at byte offset 0.
type PyTuple struct {
	Header ObjectHeader
	Items  []Value
	Attrs  *PyDict
}

// PyBuiltinFunc represents a zero-allocation native Go built-in callable using the Vectorcall convention.
// Header MUST be at byte offset 0.
type PyBuiltinFunc struct {
	Header ObjectHeader
	Name   string
	Fn     VectorcallFunc
	NoBind bool
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
	CellPtrs   []*PyCell
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
	Doc    Value
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

// NewModuleFuncVal constructs a heap-allocated KindBuiltinFunc Value representing a standalone
// module-level function that never binds as an instance method.
func NewModuleFuncVal(name string, fn VectorcallFunc) Value {
	obj := &PyBuiltinFunc{
		Header: ObjectHeader{
			Kind: KindBuiltinFunc,
			Type: BuiltinFuncType,
		},
		Name:   name,
		Fn:     fn,
		NoBind: true,
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
	var cellPtrs []*PyCell
	if len(closure) > 0 {
		cellPtrs = make([]*PyCell, len(closure))
		for i, c := range closure {
			cellPtrs[i] = c.AsCell()
		}
	}
	obj := &PyFunction{
		Header: ObjectHeader{
			Kind: KindFunction,
			Type: FunctionType,
		},
		Name:     name,
		Code:     codeVal,
		Defaults: defaults,
		Closure:  closure,
		CellPtrs: cellPtrs,
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
	var cellPtrs []*PyCell
	if len(closure) > 0 {
		cellPtrs = make([]*PyCell, len(closure))
		for i, c := range closure {
			cellPtrs[i] = c.AsCell()
		}
	}
	obj := &PyFunction{
		Header: ObjectHeader{
			Kind: KindFunction,
			Type: FunctionType,
		},
		Name:     name,
		Code:     codeVal,
		Defaults: defaults,
		Closure:  closure,
		CellPtrs: cellPtrs,
		Globals:  globals,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsFunction performs a zero-cost offset-0 downcast from Value to *PyFunction.
func (v Value) AsFunction() *PyFunction {
	return (*PyFunction)(unsafe.Pointer(v.ptr))
}

// IsCallable returns true if the value is callable in Python.
func IsCallable(v Value) bool {
	if v.IsType() {
		return true
	}
	if v.IsFunction() || v.IsBuiltinFunc() {
		return true
	}
	switch v.Kind() {
	case KindBoundMethod, KindClassMethod, KindStaticMethod:
		return true
	}
	tp := v.Type()
	if tp == nil {
		return false
	}
	if fn, _, ok := tp.LookupMRO(DunderCall); ok && fn.HeaderPtr() != nil && !fn.IsUnbound() {
		return true
	}
	return false
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
	if obj.IsTuple() && IsSubclass(obj.Type(), md.Owner) {
		t := obj.AsTuple()
		if t.Attrs != nil {
			if v, ok := t.Attrs.Get(md.Name); ok && !v.IsUnbound() {
				return v
			}
		}
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	if obj.IsDict() && IsSubclass(obj.Type(), md.Owner) {
		d := obj.AsDict()
		if d.Attrs != nil {
			if v, ok := d.Attrs.Get(md.Name); ok && !v.IsUnbound() {
				return v
			}
		}
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	if obj.IsList() && IsSubclass(obj.Type(), md.Owner) {
		l := obj.AsList()
		if l.Attrs != nil {
			if v, ok := l.Attrs.Get(md.Name); ok && !v.IsUnbound() {
				return v
			}
		}
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	if obj.IsModule() && IsSubclass(obj.Type(), md.Owner) {
		m := obj.AsModule()
		if m.Dict != nil {
			if v, ok := m.Dict.Get(md.Name); ok && !v.IsUnbound() {
				return v
			}
		}
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
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
	if obj.IsTuple() && IsSubclass(obj.Type(), md.Owner) {
		t := obj.AsTuple()
		if t.Attrs == nil {
			t.Attrs = NewDictVal(4).AsDict()
		}
		if value.IsUnbound() {
			t.Attrs.Delete(md.Name)
			return
		}
		t.Attrs.Set(md.Name, value)
		return
	}
	if obj.IsDict() && IsSubclass(obj.Type(), md.Owner) {
		d := obj.AsDict()
		if d.Attrs == nil {
			d.Attrs = NewDictVal(4).AsDict()
		}
		if value.IsUnbound() {
			d.Attrs.Delete(md.Name)
			return
		}
		d.Attrs.Set(md.Name, value)
		return
	}
	if obj.IsList() && IsSubclass(obj.Type(), md.Owner) {
		l := obj.AsList()
		if l.Attrs == nil {
			l.Attrs = NewDictVal(4).AsDict()
		}
		if value.IsUnbound() {
			l.Attrs.Delete(md.Name)
			return
		}
		l.Attrs.Set(md.Name, value)
		return
	}
	if obj.IsModule() && IsSubclass(obj.Type(), md.Owner) {
		m := obj.AsModule()
		if m.Dict == nil {
			m.Dict = NewDictVal(4).AsDict()
		}
		if value.IsUnbound() {
			m.Dict.Delete(md.Name)
			return
		}
		m.Dict.Set(md.Name, value)
		return
	}
	if !obj.IsInstance() {
		panic("TypeError: descriptor '" + md.Name.AsString() + "' for '" + md.Owner.Name + "' objects doesn't apply to a '" + obj.Type().Name + "' object")
	}
	inst := obj.AsInstance()
	if md.SlotIndex < 0 || md.SlotIndex >= len(inst.Slots) {
		panic("val: AttributeError: '" + md.Owner.Name + "' object has no attribute '" + md.Name.AsString() + "'")
	}
	if value.IsUnbound() && inst.Slots[md.SlotIndex].IsUnbound() {
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
	switch {
	case owner != nil:
		selfType = owner
	case !obj.IsNone() && !obj.IsUnbound():
		selfType = obj.Type()
	default:
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
	if callable.IsType() {
		if len(args) != 1 {
			panic("TypeError: staticmethod() takes exactly one argument")
		}
		return NewStaticMethodVal(args[0])
	}
	if callable.IsStaticMethod() {
		inner := callable.AsStaticMethod().Callable
		if inner.IsFunction() {
			return functionTpCall(ctx, inner, args, kwnames)
		}
		if inner.IsBuiltinFunc() {
			return inner.AsBuiltinFunc().Fn(ctx, inner, args, kwnames)
		}
		if tpCall := inner.Type().Slots.TpCall; tpCall != nil {
			return tpCall(ctx, inner, args, kwnames)
		}
	}
	panic("TypeError: staticmethod object is not callable")
}

// Descriptor and Callable TypeSlot implementations.

func functionDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	if obj.IsNone() || obj.IsUnbound() {
		return descr
	}
	return NewBoundMethodVal(obj, descr)
}

func builtinFuncDescrGet(ctx *ExecContext, descr, obj Value, owner *PyType) Value {
	if obj.IsNone() || obj.IsUnbound() {
		return descr
	}
	bf := descr.AsBuiltinFunc()
	if bf.NoBind {
		return descr
	}
	return NewBoundMethodVal(obj, descr)
}

// FunctionBuiltinsHook returns the builtins namespace (a dict) for function
// objects whose globals do not carry a __builtins__ entry. Set by the VM.
var FunctionBuiltinsHook func() Value

// newFunctionFromArgs implements types.FunctionType(code, globals, name=None,
// argdefs=None, closure=None, kwdefaults=None).
func newFunctionFromArgs(args []Value, kwnames []Value) Value {
	names := []string{"code", "globals", "name", "argdefs", "closure", "kwdefaults"}
	slots := make([]Value, len(names))
	set := make([]bool, len(names))
	npos := len(args) - len(kwnames)
	if npos > len(names) {
		panic("TypeError: function() takes at most 6 arguments")
	}
	for i := range npos {
		slots[i], set[i] = args[i], true
	}
	for i, kn := range kwnames {
		found := false
		for j, n := range names {
			if kn.AsString() == n {
				if set[j] {
					panic("TypeError: function() got multiple values for argument '" + n + "'")
				}
				slots[j], set[j] = args[npos+i], true
				found = true
				break
			}
		}
		if !found {
			panic("TypeError: function() got an unexpected keyword argument '" + kn.AsString() + "'")
		}
	}
	if !set[0] || !set[1] {
		panic("TypeError: function() missing required argument 'code' or 'globals'")
	}
	if !slots[0].IsCode() {
		panic("TypeError: function() argument 'code' must be code, not " + slots[0].Type().Name)
	}
	if !slots[1].IsDict() {
		panic("TypeError: function() argument 'globals' must be dict, not " + slots[1].Type().Name)
	}
	var defaults, closure []Value
	if set[3] && !slots[3].IsNone() {
		if !slots[3].IsTuple() {
			panic("TypeError: arg 4 (defaults) must be None or tuple")
		}
		defaults = slots[3].AsTuple().Items
	}
	if set[4] && !slots[4].IsNone() {
		if !slots[4].IsTuple() {
			panic("TypeError: arg 5 (closure) must be tuple")
		}
		closure = slots[4].AsTuple().Items
	}
	name := ""
	if set[2] && !slots[2].IsNone() {
		if !slots[2].IsAnyString() {
			panic("TypeError: arg 3 (name) must be None or string")
		}
		name = slots[2].AsString()
	} else if CodeInspectorHook != nil {
		if n := CodeInspectorHook.Name(slots[0]); n != "" {
			name = n
		}
	}
	fv := NewFunctionValWithGlobalsAndClosure(name, slots[0], defaults, closure, slots[1].AsDict())
	if set[5] && !slots[5].IsNone() {
		if !slots[5].IsDict() {
			panic("TypeError: arg 6 (kwdefaults) must be None or dict")
		}
		fv.AsFunction().KwDefaults = slots[5].AsDict()
	}
	return fv
}

func functionTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if callable.IsType() && callable.AsType() == FunctionType {
		return newFunctionFromArgs(args, kwnames)
	}
	if ctx != nil && ctx.CallHook != nil && ctx.SP+len(args) <= len(ctx.Stack) {
		base := ctx.SP
		copy(ctx.Stack[base:base+len(args)], args)
		return ctx.CallHook(ctx, callable, base, len(args), kwnames)
	}
	if AcquireExecContextHook != nil {
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		if tmpCtx != nil && tmpCtx.CallHook != nil && tmpCtx.SP+len(args) <= len(tmpCtx.Stack) {
			base := tmpCtx.SP
			copy(tmpCtx.Stack[base:base+len(args)], args)
			return tmpCtx.CallHook(tmpCtx, callable, base, len(args), kwnames)
		}
	}
	panic("val: cannot call Python bytecode function without active VM ExecContext")
}

func functionTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	fn := obj.AsFunction()
	nameStr := name.AsString()
	switch nameStr {
	case "__name__":
		return InternString(fn.Name)
	case "__qualname__":
		if fn.Dict != nil {
			if v, ok := fn.Dict.Get(name); ok {
				return v
			}
		}
		if CodeInspectorHook != nil && fn.Code.ptr != nil {
			qn := CodeInspectorHook.QualName(fn.Code)
			if qn != "" {
				return InternString(qn)
			}
		}
		return InternString(fn.Name)
	case "__module__":
		if fn.Dict != nil {
			if v, ok := fn.Dict.Get(name); ok {
				return v
			}
		}
		if fn.Globals != nil {
			if modVal, ok := fn.Globals.Get(InternString("__name__")); ok {
				return modVal
			}
		}
		return InternString("builtins")
	case "__code__":
		return fn.Code
	case "__defaults__":
		if fn.Dict != nil {
			if v, ok := fn.Dict.Get(name); ok {
				return v
			}
		}
		if len(fn.Defaults) > 0 {
			return NewTupleVal(fn.Defaults...)
		}
		return NoneVal()
	case "__kwdefaults__":
		if fn.Dict != nil {
			if v, ok := fn.Dict.Get(name); ok {
				return v
			}
		}
		if fn.KwDefaults != nil {
			return ObjectVal(&fn.KwDefaults.Header)
		}
		return NoneVal()
	case "__closure__":
		if fn.Dict != nil {
			if v, ok := fn.Dict.Get(name); ok {
				return v
			}
		}
		if len(fn.Closure) > 0 {
			return NewTupleVal(fn.Closure...)
		}
		return NoneVal()
	case "__globals__":
		if fn.Globals != nil {
			return ObjectVal(&fn.Globals.Header)
		}
		return NewDictVal(4)
	case "__builtins__":
		if fn.Globals != nil {
			if b, ok := fn.Globals.Get(InternString("__builtins__")); ok {
				if b.IsModule() {
					return ObjectVal(&b.AsModule().Dict.Header)
				}
				return b
			}
		}
		if FunctionBuiltinsHook != nil {
			return FunctionBuiltinsHook()
		}
		return NewDictVal(4)
	case "__dict__":
		if fn.Dict == nil {
			fn.Dict = NewDictVal(4).AsDict()
		}
		return ObjectVal(&fn.Dict.Header)
	case "__doc__":
		if fn.Dict != nil {
			if v, ok := fn.Dict.Get(name); ok {
				return v
			}
		}
		if CodeInspectorHook != nil && fn.Code.ptr != nil {
			docVal := CodeInspectorHook.Doc(fn.Code)
			if !docVal.IsNone() && docVal.ptr != nil {
				return docVal
			}
		}
		return NoneVal()
	case "__annotations__":
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
	return objectGetAttr(ctx, obj, name)
}

func functionTpSetAttr(ctx *ExecContext, obj, name, value Value) Value {
	fn := obj.AsFunction()
	nameStr := name.AsString()
	switch nameStr {
	case "__defaults__":
		if value.IsTuple() {
			fn.Defaults = value.AsTuple().Items
		} else if value.IsNone() {
			fn.Defaults = nil
		}
	case "__kwdefaults__":
		if value.IsDict() {
			fn.KwDefaults = value.AsDict()
		} else if value.IsNone() {
			fn.KwDefaults = nil
		}
	case "__code__":
		if value.IsCode() {
			fn.Code = value
		}
	case "__name__":
		if value.IsAnyString() {
			fn.Name = value.AsString()
		}
	}
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
	if callable.IsType() {
		if len(args) != 2 || len(kwnames) != 0 {
			panic("TypeError: method expected 2 arguments, got " + strconv.Itoa(len(args)))
		}
		if args[1].IsNone() {
			panic("TypeError: method() arg 2 must not be None")
		}
		return NewBoundMethodVal(args[1], args[0])
	}
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

func boundMethodTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	bm := obj.AsBoundMethod()
	nameStr := name.AsString()
	switch nameStr {
	case "__func__", "__wrapped__":
		return bm.Func
	case "__self__":
		return bm.Self
	}
	interned := InternValue(name)
	if descr, _, found := BoundMethodType.LookupMRO(interned); found {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			return descrType.Slots.TpDescrGet(ctx, descr, obj, BoundMethodType)
		}
		return descr
	}
	if bm.Func.HeaderPtr() != nil {
		return GetAttrCtx(ctx, bm.Func, name)
	}
	return objectGetAttr(ctx, obj, name)
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
	if value.IsUnbound() {
		if prop.FDel.ptr == nil || prop.FDel.IsNone() {
			panic("val: AttributeError: property has no deleter")
		}
		ctx.Invoke1(prop.FDel, obj)
		return
	}
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
	if sup.Self.IsType() && IsSubclass(sup.Self.AsType(), sup.ThisClass) {
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

var (
	ObjectNewBuiltin  Value
	ObjectInitBuiltin Value
)

func callInitSubclass(ctx *ExecContext, t *PyType, kwArgs []Value, kwnames []Value) {
	if len(t.Bases) == 0 {
		return
	}
	initSubclassFn, _, ok := t.LookupMROAfter(t, DunderInitSubclass)
	if !ok {
		return
	}
	if initSubclassFn.Kind() == KindClassMethod {
		initSubclassFn = initSubclassFn.AsClassMethod().Callable
	}
	tVal := TypeVal(t)
	totalArgs := 1 + len(kwArgs)
	if len(kwnames) > 0 {
		t.Flags |= FlagInitSubclassKwDone
	}
	if ctx != nil && ctx.CallHook != nil && ctx.SP+totalArgs <= len(ctx.Stack) {
		base := ctx.SP
		ctx.Stack[base] = tVal
		copy(ctx.Stack[base+1:base+totalArgs], kwArgs)
		ctx.CallHook(ctx, initSubclassFn, base, totalArgs, kwnames)
	} else {
		callArgs := make([]Value, totalArgs)
		callArgs[0] = tVal
		copy(callArgs[1:], kwArgs)
		if initSubclassFn.IsBuiltinFunc() {
			initSubclassFn.AsBuiltinFunc().Fn(ctx, initSubclassFn, callArgs, kwnames)
		} else if tpCall := initSubclassFn.Type().Slots.TpCall; tpCall != nil {
			tpCall(ctx, initSubclassFn, callArgs, kwnames)
		}
	}
}

func initTypeDescriptorsAndSubclasses(ctx *ExecContext, t *PyType, kwArgs []Value, kwnames []Value) {
	tVal := TypeVal(t)
	// Descriptor Notification (__set_name__)
	if t.Dict != nil {
		for _, item := range t.Dict.Items() {
			attrVal := item.Value
			if !attrVal.IsNone() && attrVal.HeaderPtr() != nil {
				if setNameFn, _, ok := attrVal.Type().LookupMRO(DunderSetName); ok {
					if ctx != nil && ctx.CallHook != nil && ctx.SP+3 <= len(ctx.Stack) {
						base := ctx.SP
						ctx.Stack[base] = attrVal
						ctx.Stack[base+1] = tVal
						ctx.Stack[base+2] = item.Key
						ctx.CallHook(ctx, setNameFn, base, 3, nil)
					} else if setNameFn.IsBuiltinFunc() {
						setNameFn.AsBuiltinFunc().Fn(ctx, setNameFn, []Value{attrVal, tVal, item.Key}, nil)
					} else if tpCall := setNameFn.Type().Slots.TpCall; tpCall != nil {
						tpCall(ctx, setNameFn, []Value{attrVal, tVal, item.Key}, nil)
					}
				}
			}
		}
	}
	// Subclass Notification (__init_subclass__)
	callInitSubclass(ctx, t, kwArgs, kwnames)
}

func typeTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	cls := callable.AsType()
	posCount := len(args) - len(kwnames)
	if cls == TypeType || (posCount >= 4 && args[0].IsType() && IsSubclass(args[0].AsType(), TypeType)) {
		if posCount == 1 && len(kwnames) == 0 {
			return TypeVal(args[0].Type())
		}
		var nameArg, basesArg, dictArg Value
		metacls := cls
		matched := false
		if posCount == 3 {
			nameArg = args[0]
			basesArg = args[1]
			dictArg = args[2]
			matched = true

			// Check if bases require a derived metaclass
			var bases []*PyType
			if basesArg.IsTuple() {
				for _, b := range basesArg.AsTuple().Items {
					if b.IsType() {
						bases = append(bases, b.AsType())
					}
				}
			} else if basesArg.IsType() {
				bases = []*PyType{basesArg.AsType()}
			}
			for _, b := range bases {
				bMeta := b.Header.Type
				if bMeta == nil {
					bMeta = TypeType
				}
				if IsSubclass(bMeta, metacls) {
					metacls = bMeta
				}
			}
			if metacls != cls {
				if ctx != nil && ctx.CallHook != nil && ctx.SP+len(args) <= len(ctx.Stack) {
					base := ctx.SP
					copy(ctx.Stack[base:base+len(args)], args)
					return ctx.CallHook(ctx, TypeVal(metacls), base, len(args), kwnames)
				} else if tpCall := metacls.Slots.TpCall; tpCall != nil {
					return tpCall(ctx, TypeVal(metacls), args, kwnames)
				}
			}
		} else if posCount >= 4 && args[0].IsType() {
			metacls = args[0].AsType()
			nameArg = args[1]
			basesArg = args[2]
			dictArg = args[3]
			matched = true
		}
		if matched {
			name := nameArg.AsString()
			var bases []*PyType
			if basesArg.IsTuple() {
				for _, b := range basesArg.AsTuple().Items {
					if b.IsType() {
						bases = append(bases, b.AsType())
					}
				}
			} else if basesArg.IsType() {
				bases = []*PyType{basesArg.AsType()}
			}
			var dict *PyDict
			if dictArg.IsDict() {
				dict = dictArg.AsDict()
			} else if !dictArg.IsNone() && dictArg.ptr != nil {
				dict = NewDictVal(16).AsDict()
				if itemsFn, _, ok := dictArg.Type().LookupMRO(InternString("items")); ok {
					itemsVal := ctx.Invoke1(itemsFn, dictArg)
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
			t.Flags |= FlagHeapType
			if metacls != nil && metacls != TypeType {
				t.Header.Type = metacls
			}
			var kwArgs []Value
			if len(kwnames) > 0 {
				kwArgs = args[len(args)-len(kwnames):]
			}
			initTypeDescriptorsAndSubclasses(ctx, t, kwArgs, kwnames)
			return TypeVal(t)
		}
	}

	if cls.Slots.TpCall != nil && cls.dunderCall.ptr == nil && ((cls.Flags&FlagHeapType) == 0 || ((cls.dunderNew.ptr == nil || cls.dunderNew.ptr == ObjectNewBuiltin.ptr) && !cls.HasInit())) {
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
		if inst.IsType() && len(kwnames) > 0 && (inst.AsType().Flags&FlagInitSubclassKwDone) == 0 {
			consumedByMeta := false
			if cls.dunderNew.IsFunction() && CodeInspectorHook != nil {
				fnCode := cls.dunderNew.AsFunction().Code
				if fnCode.ptr != nil {
					vnames := CodeInspectorHook.VarNames(fnCode)
					for _, kn := range kwnames {
						knStr := kn.AsString()
						for _, vn := range vnames {
							if vn.AsString() == knStr {
								consumedByMeta = true
								break
							}
						}
						if consumedByMeta {
							break
						}
					}
				}
			}
			if !consumedByMeta {
				kwArgs := args[len(args)-len(kwnames):]
				callInitSubclass(ctx, inst.AsType(), kwArgs, kwnames)
			}
		}
		if cls.HasInit() {
			initFn := cls.DunderInitVal()
			buf := make([]Value, 1+len(args))
			buf[0] = inst
			copy(buf[1:], args)
			if initFn.IsBuiltinFunc() {
				initFn.AsBuiltinFunc().Fn(ctx, initFn, buf, kwnames)
			} else if ctx != nil && ctx.CallHook != nil && ctx.SP+len(buf) <= len(ctx.Stack) {
				base := ctx.SP
				copy(ctx.Stack[base:base+len(buf)], buf)
				ctx.CallHook(ctx, initFn, base, len(buf), kwnames)
			} else if tpCall := initFn.Type().Slots.TpCall; tpCall != nil {
				tpCall(ctx, initFn, buf, kwnames)
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
	if !idxVal.IsInt64() {
		if ctx != nil {
			ctx.Raise(NewTypeError("string indices must be integers, not '" + idxVal.Type().Name + "'"))
			return NoneVal()
		}
		panic("TypeError: string indices must be integers, not '" + idxVal.Type().Name + "'")
	}
	idx := int(idxVal.AsInt64())
	s := a.AsString()
	if isASCII(s) {
		if idx < 0 {
			idx += len(s)
		}
		if idx < 0 || idx >= len(s) {
			if ctx != nil {
				ctx.Raise(NewIndexError("string index out of range"))
				return NoneVal()
			}
			panic("IndexError: string index out of range")
		}
		return SmallStringVal(s[idx : idx+1])
	}
	runes := []rune(s)
	if idx < 0 {
		idx += len(runes)
	}
	if idx < 0 || idx >= len(runes) {
		if ctx != nil {
			ctx.Raise(NewIndexError("string index out of range"))
			return NoneVal()
		}
		panic("IndexError: string index out of range")
	}
	return NewStringVal(string(runes[idx]))
}

func strMpSubscript(ctx *ExecContext, a, idxVal Value) Value {
	if idxVal.IsSlice() {
		sl := idxVal.AsSlice()
		s := a.AsString()
		if isASCII(s) {
			start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(s))
			if err != nil {
				panic(err.Error())
			}
			if sliceLen == 0 {
				return NewStringVal("")
			}
			if step == 1 {
				return NewStringVal(s[start:stop])
			}
			var buf strings.Builder
			buf.Grow(sliceLen)
			cur := start
			for range sliceLen {
				buf.WriteByte(s[cur])
				cur += step
			}
			return NewStringVal(buf.String())
		}
		runes := []rune(s)
		start, stop, step, sliceLen, err := NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(runes))
		if err != nil {
			panic(err.Error())
		}
		if sliceLen == 0 {
			return NewStringVal("")
		}
		if step == 1 {
			return NewStringVal(string(runes[start:stop]))
		}
		resRunes := make([]rune, sliceLen)
		cur := start
		for i := range sliceLen {
			resRunes[i] = runes[cur]
			cur += step
		}
		return NewStringVal(string(resRunes))
	}
	return strSqItem(ctx, a, idxVal)
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
		for i := range sliceLen {
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
		switch {
		case val.IsList():
			rhsItems = val.AsList().Items
		case val.IsTuple():
			rhsItems = val.AsTuple().Items
		default:
			func() {
				defer func() {
					if r := recover(); r != nil {
						panic("TypeError: can only assign an iterable to a slice")
					}
				}()
				rhsItems = ExtractIterableItems(ctx, val)
			}()
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
		for i := range sliceLen {
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
		for i := range sliceLen {
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
		for i := range sliceLen {
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
		for i := range sliceLen {
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
		switch {
		case val.IsBytes():
			rhsBytes = val.AsBytes().Data
		case val.IsByteArray():
			rhsBytes = val.AsByteArray().Data
		case val.IsMemoryView():
			rhsBytes = val.AsMemoryView().Buf
		default:
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
		for i := range sliceLen {
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
		switch {
		case val.IsBytes():
			rhsBytes = val.AsBytes().Data
		case val.IsByteArray():
			rhsBytes = val.AsByteArray().Data
		case val.IsMemoryView():
			rhsBytes = val.AsMemoryView().Buf
		default:
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
		s := x.AsString()
		if isASCII(s) {
			return Int64Val(int64(len(s)))
		}
		return Int64Val(int64(utf8.RuneCountInString(s)))
	}
	switch x.ptr.Kind {
	case KindList:
		return Int64Val(int64(len(x.AsList().Items)))
	case KindString:
		s := x.AsStringObj().Data
		if isASCII(s) {
			return Int64Val(int64(len(s)))
		}
		return Int64Val(int64(utf8.RuneCountInString(s)))
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
	switch {
	case b.IsBytes():
		bBytes = b.AsBytes().Data
	case b.IsByteArray():
		bBytes = b.AsByteArray().Data
	default:
		panic("TypeError: can't concat " + b.Type().Name + " to bytes")
	}
	res := make([]byte, len(aBytes)+len(bBytes))
	copy(res, aBytes)
	copy(res[len(aBytes):], bBytes)
	return NewBytesVal(res)
}

func byteArrayNbAdd(ctx *ExecContext, a, b Value) Value {
	var aBytes, bBytes []byte
	switch {
	case a.IsByteArray():
		ba := a.AsByteArray()
		ba.mu.RLock()
		aBytes = ba.Data
		ba.mu.RUnlock()
	case a.IsBytes():
		aBytes = a.AsBytes().Data
	default:
		panic("TypeError: can't concat " + a.Type().Name + " to bytearray")
	}

	switch {
	case b.IsByteArray():
		bb := b.AsByteArray()
		bb.mu.RLock()
		bBytes = bb.Data
		bb.mu.RUnlock()
	case b.IsBytes():
		bBytes = b.AsBytes().Data
	case b.IsMemoryView():
		bBytes = b.AsMemoryView().Buf
	default:
		panic("TypeError: can't concat " + b.Type().Name + " to bytearray")
	}

	res := make([]byte, len(aBytes)+len(bBytes))
	copy(res, aBytes)
	copy(res[len(aBytes):], bBytes)
	return NewByteArrayVal(res)
}

func bytesNbMul(ctx *ExecContext, a, b Value) Value {
	var data []byte
	var n int64
	switch {
	case a.IsBytes() && (b.IsInt64() || b.IsBool()):
		data = a.AsBytes().Data
		n = b.AsInt64()
	case (a.IsInt64() || a.IsBool()) && b.IsBytes():
		data = b.AsBytes().Data
		n = a.AsInt64()
	default:
		return NotImplementedVal()
	}
	if n <= 0 {
		return NewBytesVal([]byte{})
	}
	return NewBytesVal(bytes.Repeat(data, int(n)))
}

func byteArrayNbMul(ctx *ExecContext, a, b Value) Value {
	var data []byte
	var n int64
	switch {
	case a.IsByteArray() && (b.IsInt64() || b.IsBool()):
		ba := a.AsByteArray()
		ba.mu.RLock()
		data = ba.Data
		ba.mu.RUnlock()
		n = b.AsInt64()
	case (a.IsInt64() || a.IsBool()) && b.IsByteArray():
		ba := b.AsByteArray()
		ba.mu.RLock()
		data = ba.Data
		ba.mu.RUnlock()
		n = a.AsInt64()
	default:
		return NotImplementedVal()
	}
	if n <= 0 {
		return NewByteArrayVal([]byte{})
	}
	return NewByteArrayVal(bytes.Repeat(data, int(n)))
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
	// Fall back to general iterable extraction (generators, ranges, etc.)
	if ctx != nil {
		items := ExtractIterableItems(ctx, arg)
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
	// Fall back to general iterable extraction (generators, ranges, etc.)
	if ctx != nil {
		items := ExtractIterableItems(ctx, arg)
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
	if a.IsType() {
		if fn, _, ok := a.AsType().LookupMRO(InternString("__class_getitem__")); ok {
			if fn.Kind() == KindClassMethod {
				fn = fn.AsClassMethod().Callable
			}
			return ctx.Invoke2(fn, a, b)
		}
	}
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

func codeTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	nameStr := name.AsString()
	if CodeInspectorHook != nil {
		switch nameStr {
		case "co_name":
			return InternString(CodeInspectorHook.Name(obj))
		case "co_qualname":
			return InternString(CodeInspectorHook.QualName(obj))
		case "co_filename":
			return InternString(CodeInspectorHook.FileName(obj))
		case "co_firstlineno":
			return Int64Val(int64(CodeInspectorHook.FirstLineNo(obj)))
		case "co_argcount":
			return Int64Val(int64(CodeInspectorHook.ArgCount(obj)))
		case "co_posonlyargcount":
			return Int64Val(int64(CodeInspectorHook.PosOnlyArgCount(obj)))
		case "co_kwonlyargcount":
			return Int64Val(int64(CodeInspectorHook.KWOnlyArgCount(obj)))
		case "co_varnames":
			return NewTupleVal(CodeInspectorHook.VarNames(obj)...)
		case "co_cellvars":
			return NewTupleVal(CodeInspectorHook.CellVars(obj)...)
		case "co_freevars":
			return NewTupleVal(CodeInspectorHook.FreeVars(obj)...)
		case "co_flags":
			return Int64Val(int64(CodeInspectorHook.Flags(obj)))
		case "co_linetable":
			return NewBytesVal(CodeInspectorHook.LineTable(obj))
		case "co_code":
			return NewBytesVal(CodeInspectorHook.CodeBytes(obj))
		case "co_consts":
			return NewTupleVal(CodeInspectorHook.Consts(obj)...)
		case "co_stacksize":
			return Int64Val(int64(CodeInspectorHook.StackSize(obj)))
		case "co_lines":
			return NewBuiltinFuncVal("co_lines", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
				spans := CodeInspectorHook.LineSpans(obj)
				items := make([]Value, len(spans))
				for i, s := range spans {
					if v, ok := s.(Value); ok {
						items[i] = v
					}
				}
				return NewIterVal(NewTupleVal(items...))
			})
		}
	}
	panic("val: AttributeError: 'code' object has no attribute '" + nameStr + "'")
}

func frameTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	f := obj.AsFrame()
	nameStr := name.AsString()
	switch nameStr {
	case "f_back":
		if f.Back.IsNone() && !f.Detached && f.VM != nil {
			if vm, ok := f.VM.(FrameVM); ok {
				f.Back = vm.MaterializeFrame(f.FrameIdx - 1)
			}
		}
		return f.Back
	case "f_code":
		return f.CO
	case "f_globals":
		if f.Globals != nil {
			return ObjectVal(&f.Globals.Header)
		}
		return NewDictVal(4)
	case "f_locals":
		if f.LocalsProxy.IsLocalsProxy() {
			return f.LocalsProxy
		}
		proxy := &PyLocalsProxy{Frame: f}
		f.LocalsProxy = NewLocalsProxyVal(proxy)
		return f.LocalsProxy
	case "f_lineno":
		if CodeInspectorHook != nil && f.CO.ptr != nil {
			return Int64Val(int64(CodeInspectorHook.LineAt(f.CO, f.IP)))
		}
		return Int64Val(1)
	case "f_lasti":
		return Int64Val(int64(f.IP))
	case "f_trace":
		return NoneVal()
	}
	panic("val: AttributeError: 'frame' object has no attribute '" + nameStr + "'")
}

func tracebackTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	tb := obj.AsTraceback()
	switch name.AsString() {
	case "tb_frame":
		return tb.Frame
	case "tb_lineno":
		return Int64Val(int64(tb.LineNo))
	case "tb_lasti":
		return Int64Val(int64(tb.LastI))
	case "tb_next":
		return tb.Next
	}
	panic("val: AttributeError: 'traceback' object has no attribute '" + name.AsString() + "'")
}

func localsProxyCollectPairs(p *PyLocalsProxy) []DictEntry {
	f := p.Frame
	var entries []DictEntry
	seen := make(map[string]bool)

	if !f.Detached {
		if CodeInspectorHook != nil && f.CO.ptr != nil {
			varnames := CodeInspectorHook.VarNames(f.CO)
			if vm, ok := f.VM.(FrameVM); ok {
				for i, name := range varnames {
					s := name.AsString()
					if seen[s] {
						continue
					}
					v := vm.GetStackVal(f.BaseOffset + i)
					if !v.IsNone() || v.ptr != nil {
						seen[s] = true
						entries = append(entries, DictEntry{
							Hash:   HashValue(name),
							Active: true,
							Key:    name,
							Value:  v,
						})
					}
				}
			}
			cellvars := CodeInspectorHook.CellVars(f.CO)
			for i, name := range cellvars {
				s := name.AsString()
				if seen[s] {
					continue
				}
				if i < len(f.Cells) && f.Cells[i].IsCell() {
					seen[s] = true
					entries = append(entries, DictEntry{
						Hash:   HashValue(name),
						Active: true,
						Key:    name,
						Value:  f.Cells[i].AsCell().Value,
					})
				}
			}
			freevars := CodeInspectorHook.FreeVars(f.CO)
			for j, name := range freevars {
				s := name.AsString()
				if seen[s] {
					continue
				}
				cellIdx := len(cellvars) + j
				if cellIdx < len(f.Cells) && f.Cells[cellIdx].IsCell() {
					seen[s] = true
					entries = append(entries, DictEntry{
						Hash:   HashValue(name),
						Active: true,
						Key:    name,
						Value:  f.Cells[cellIdx].AsCell().Value,
					})
				}
			}
		}
		if f.ExtraLocals != nil {
			for _, e := range f.ExtraLocals.Entries {
				if !e.Key.IsUnbound() {
					s := e.Key.AsString()
					if !seen[s] {
						seen[s] = true
						entries = append(entries, e)
					}
				}
			}
		}
	} else if f.SavedLocals != nil {
		for _, e := range f.SavedLocals.Entries {
			if !e.Key.IsUnbound() {
				entries = append(entries, e)
			}
		}
	}
	return entries
}

func localsProxyMpSubscript(ctx *ExecContext, obj, key Value) Value {
	p := obj.AsLocalsProxy()
	f := p.Frame
	keyStr := key.AsString()

	if !f.Detached {
		if CodeInspectorHook != nil && f.CO.ptr != nil {
			varnames := CodeInspectorHook.VarNames(f.CO)
			for i, name := range varnames {
				if name.AsString() == keyStr {
					if vm, ok := f.VM.(FrameVM); ok {
						v := vm.GetStackVal(f.BaseOffset + i)
						if !v.IsNone() || v.ptr != nil {
							return v
						}
					}
				}
			}
			cellvars := CodeInspectorHook.CellVars(f.CO)
			for i, name := range cellvars {
				if name.AsString() == keyStr {
					if i < len(f.Cells) && f.Cells[i].IsCell() {
						return f.Cells[i].AsCell().Value
					}
				}
			}
			freevars := CodeInspectorHook.FreeVars(f.CO)
			for j, name := range freevars {
				if name.AsString() == keyStr {
					cellIdx := len(cellvars) + j
					if cellIdx < len(f.Cells) && f.Cells[cellIdx].IsCell() {
						return f.Cells[cellIdx].AsCell().Value
					}
				}
			}
		}
		if f.ExtraLocals != nil {
			if v, ok := f.ExtraLocals.Get(key); ok {
				return v
			}
		}
	} else if f.SavedLocals != nil {
		if v, ok := f.SavedLocals.Get(key); ok {
			return v
		}
	}
	panic("KeyError: '" + keyStr + "'")
}

func localsProxyMpAssSubscript(ctx *ExecContext, obj, key, val Value) Value {
	p := obj.AsLocalsProxy()
	f := p.Frame
	keyStr := key.AsString()

	if !f.Detached {
		if CodeInspectorHook != nil && f.CO.ptr != nil {
			varnames := CodeInspectorHook.VarNames(f.CO)
			for i, name := range varnames {
				if name.AsString() == keyStr {
					if vm, ok := f.VM.(FrameVM); ok {
						vm.SetStackVal(f.BaseOffset+i, val)
						return NoneVal()
					}
				}
			}
			cellvars := CodeInspectorHook.CellVars(f.CO)
			for i, name := range cellvars {
				if name.AsString() == keyStr {
					if i < len(f.Cells) && f.Cells[i].IsCell() {
						f.Cells[i].AsCell().Value = val
						return NoneVal()
					}
				}
			}
			freevars := CodeInspectorHook.FreeVars(f.CO)
			for j, name := range freevars {
				if name.AsString() == keyStr {
					cellIdx := len(cellvars) + j
					if cellIdx < len(f.Cells) && f.Cells[cellIdx].IsCell() {
						f.Cells[cellIdx].AsCell().Value = val
						return NoneVal()
					}
				}
			}
		}
		if f.ExtraLocals == nil {
			f.ExtraLocals = NewDictVal(4).AsDict()
		}
		f.ExtraLocals.Set(InternString(keyStr), val)
		return NoneVal()
	}

	if f.SavedLocals == nil {
		f.SavedLocals = NewDictVal(4).AsDict()
	}
	f.SavedLocals.Set(InternString(keyStr), val)
	return NoneVal()
}

func localsProxyContains(ctx *ExecContext, obj, key Value) bool {
	p := obj.AsLocalsProxy()
	f := p.Frame
	keyStr := key.AsString()

	if !f.Detached {
		if CodeInspectorHook != nil && f.CO.ptr != nil {
			varnames := CodeInspectorHook.VarNames(f.CO)
			for i, name := range varnames {
				if name.AsString() == keyStr {
					if vm, ok := f.VM.(FrameVM); ok {
						v := vm.GetStackVal(f.BaseOffset + i)
						if !v.IsNone() || v.ptr != nil {
							return true
						}
					}
				}
			}
			cellvars := CodeInspectorHook.CellVars(f.CO)
			for i, name := range cellvars {
				if name.AsString() == keyStr {
					if i < len(f.Cells) && f.Cells[i].IsCell() {
						return true
					}
				}
			}
			freevars := CodeInspectorHook.FreeVars(f.CO)
			for j, name := range freevars {
				if name.AsString() == keyStr {
					cellIdx := len(cellvars) + j
					if cellIdx < len(f.Cells) && f.Cells[cellIdx].IsCell() {
						return true
					}
				}
			}
		}
		if f.ExtraLocals != nil {
			if _, ok := f.ExtraLocals.Get(key); ok {
				return true
			}
		}
		return false
	}

	if f.SavedLocals != nil {
		_, ok := f.SavedLocals.Get(key)
		return ok
	}
	return false
}

func reprVal(v Value) string {
	if v.IsAnyString() {
		return strconv.Quote(v.AsString())
	}
	return v.String()
}

func localsProxyRepr(o Value) string {
	entries := localsProxyCollectPairs(o.AsLocalsProxy())
	var sb strings.Builder
	sb.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(reprVal(e.Key))
		sb.WriteString(": ")
		sb.WriteString(reprVal(e.Value))
	}
	sb.WriteByte('}')
	return sb.String()
}

func weakRefTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	ref := callable.AsWeakRef()
	if ref.IsDead.Load() {
		return NoneVal()
	}
	hdr := ref.WP.Value()
	if hdr == nil {
		ref.IsDead.Store(true)
		return NoneVal()
	}
	return ObjectVal(hdr)
}

func weakRefTpHash(o Value) int64 {
	ref := o.AsWeakRef()
	if ref.HasHash {
		return ref.Hash
	}
	hdr := ref.WP.Value()
	if hdr == nil {
		panic("TypeError: cannot compute hash of dead weakref")
	}
	h := int64(HashValue(ObjectVal(hdr)))
	ref.Hash = h
	ref.HasHash = true
	return h
}

func weakRefEqual(a, b Value) bool {
	if a.ptr == nil || b.ptr == nil || a.ptr.Kind != KindWeakRef || b.ptr.Kind != KindWeakRef {
		return false
	}
	refA := a.AsWeakRef()
	refB := b.AsWeakRef()
	hdrA := refA.WP.Value()
	hdrB := refB.WP.Value()

	if hdrA != nil && hdrB != nil {
		return hdrA == hdrB
	}
	if hdrA == nil && hdrB == nil {
		return refA.Callback == refB.Callback
	}
	return false
}

func weakRefTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	ref := obj.AsWeakRef()
	if name.AsString() == "__callback__" {
		return ref.Callback
	}
	panic("val: AttributeError: 'ReferenceType' object has no attribute '" + name.AsString() + "'")
}

func weakProxyGetTarget(ctx *ExecContext, obj Value) (Value, bool) {
	p := obj.AsWeakProxy()
	hdr := p.WP.Value()
	if hdr == nil {
		if ctx != nil {
			ctx.Raise(NewException(ReferenceErrorType, "weakly-referenced object no longer exists"))
		} else {
			panic("ReferenceError: weakly-referenced object no longer exists")
		}
		return Value{}, false
	}
	return ObjectVal(hdr), true
}

func weakProxyTpGetAttr(ctx *ExecContext, obj, name Value) Value {
	target, ok := weakProxyGetTarget(ctx, obj)
	if !ok {
		return NoneVal()
	}
	return GetAttrCtx(ctx, target, name)
}

func weakProxyTpSetAttr(ctx *ExecContext, obj, name, value Value) Value {
	target, ok := weakProxyGetTarget(ctx, obj)
	if !ok {
		return NoneVal()
	}
	SetAttrCtx(ctx, target, name, value)
	return NoneVal()
}

func weakProxyTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	target, ok := weakProxyGetTarget(ctx, callable)
	if !ok {
		return NoneVal()
	}
	if ctx != nil && ctx.CallHook != nil {
		base := ctx.SP
		ctx.Stack[base] = target
		copy(ctx.Stack[base+1:base+1+len(args)], args)
		return ctx.CallHook(ctx, target, base+1, len(args), kwnames)
	}
	if tpCall := target.Type().Slots.TpCall; tpCall != nil {
		return tpCall(ctx, target, args, kwnames)
	}
	panic("TypeError: '" + target.Type().Name + "' object is not callable")
}

func tupleNbAdd(ctx *ExecContext, a, b Value) Value {
	if a.IsTuple() && b.IsTuple() {
		ta := a.AsTuple().Items
		tb := b.AsTuple().Items
		merged := make([]Value, len(ta)+len(tb))
		copy(merged, ta)
		copy(merged[len(ta):], tb)
		return NewTupleVal(merged...)
	}
	panic("TypeError: can only concatenate tuple (not \"" + b.Type().Name + "\") to tuple")
}

func listNbMul(ctx *ExecContext, a, b Value) Value {
	var lVal Value
	var n int64
	switch {
	case a.IsList() && (b.IsInt64() || b.IsBool()):
		lVal = a
		n = b.AsInt64()
	case (a.IsInt64() || a.IsBool()) && b.IsList():
		lVal = b
		n = a.AsInt64()
	default:
		return NotImplementedVal()
	}
	if n <= 0 {
		return NewListVal()
	}
	items := lVal.AsList().Items
	newItems := make([]Value, 0, len(items)*int(n))
	for range n {
		newItems = append(newItems, items...)
	}
	return NewListVal(newItems...)
}

func tupleNbMul(ctx *ExecContext, a, b Value) Value {
	var tVal Value
	var n int64
	switch {
	case a.IsTuple() && (b.IsInt64() || b.IsBool()):
		tVal = a
		n = b.AsInt64()
	case (a.IsInt64() || a.IsBool()) && b.IsTuple():
		tVal = b
		n = a.AsInt64()
	default:
		return NotImplementedVal()
	}
	if n <= 0 {
		return NewTupleVal()
	}
	items := tVal.AsTuple().Items
	newItems := make([]Value, 0, len(items)*int(n))
	for range n {
		newItems = append(newItems, items...)
	}
	return NewTupleVal(newItems...)
}

func strNbMul(ctx *ExecContext, a, b Value) Value {
	var s string
	var n int64
	switch {
	case a.IsAnyString() && (b.IsInt64() || b.IsBool()):
		s = a.AsString()
		n = b.AsInt64()
	case (a.IsInt64() || a.IsBool()) && b.IsAnyString():
		s = b.AsString()
		n = a.AsInt64()
	default:
		return NotImplementedVal()
	}
	if n <= 0 {
		return NewStringVal("")
	}
	return NewStringVal(strings.Repeat(s, int(n)))
}

// ExtractIterableItems returns all items from an iterable Value.
func ExtractIterableItems(ctx *ExecContext, arg Value) []Value {
	if arg.IsSet() {
		return arg.AsSet().Items()
	}
	if arg.IsList() {
		l := arg.AsList()
		l.mu.RLock()
		res := make([]Value, len(l.Items))
		copy(res, l.Items)
		l.mu.RUnlock()
		return res
	}
	if arg.IsTuple() {
		t := arg.AsTuple()
		res := make([]Value, len(t.Items))
		copy(res, t.Items)
		return res
	}
	if arg.IsDict() {
		return arg.AsDict().Keys()
	}
	if arg.Type() == DictKeysType {
		pk := (*PyDictKeys)(AsPointer(arg))
		return pk.Dict.Keys()
	}
	if arg.IsAnyString() {
		s := arg.AsString()
		var res []Value
		for _, r := range s {
			res = append(res, NewStringVal(string(r)))
		}
		return res
	}
	if arg.IsBytes() {
		data := arg.AsBytes().Data
		res := make([]Value, len(data))
		for i, b := range data {
			res[i] = Int64Val(int64(b))
		}
		return res
	}
	if arg.IsByteArray() {
		data := arg.AsByteArray().Data
		res := make([]Value, len(data))
		for i, b := range data {
			res[i] = Int64Val(int64(b))
		}
		return res
	}
	if arg.IsIter() {
		it := arg.AsIter()
		if it.Kind == IterKindInvalid {
			panic("TypeError: '" + it.Obj.Type().Name + "' object is not iterable")
		}
		var res []Value
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			res = append(res, v)
		}
		return res
	}
	if ctx == nil && AcquireExecContextHook != nil {
		var release func()
		ctx, release = AcquireExecContextHook()
		defer release()
	}
	tp := arg.Type()
	if tp != nil {
		var it Value
		hasIter := false
		if tp.Slots.TpIter != nil {
			it = tp.Slots.TpIter(ctx, arg)
			hasIter = true
		} else if iterFn, _, ok := tp.LookupMRO(InternString("__iter__")); ok && !iterFn.IsNone() {
			it = ctx.Invoke1(iterFn, arg)
			hasIter = true
		} else {
			cand := NewIterVal(arg)
			if cand.IsIter() && cand.AsIter().Kind != IterKindInvalid {
				it = cand
				hasIter = true
			}
		}
		if ctx != nil && ctx.HasException() {
			return nil
		}
		if hasIter {
			if it.IsIter() {
				return ExtractIterableItems(ctx, it)
			}
			itType := it.Type()
			if itType != nil && itType.Slots.TpIterNext != nil {
				var res []Value
				for {
					var v Value
					var status IterStatus
					var done bool
					func() {
						defer func() {
							if r := recover(); r != nil {
								if exc, ok := r.(Value); ok && IsInstance(exc, StopIterationType) {
									done = true
									return
								}
								panic(r)
							}
						}()
						v, status = itType.Slots.TpIterNext(ctx, it)
					}()
					if done {
						break
					}
					if status == IterError {
						if ctx != nil && ctx.HasException() && IsInstance(ctx.ActiveException, StopIterationType) {
							ctx.ClearException()
						}
						break
					}
					if status == IterExhausted {
						break
					}
					res = append(res, v)
				}
				return res
			}
			if itType != nil {
				if nextFn, _, okNext := itType.LookupMRO(InternString("__next__")); okNext {
					var res []Value
					for {
						var v Value
						var done bool
						func() {
							defer func() {
								if r := recover(); r != nil {
									if exc, ok := r.(Value); ok && IsInstance(exc, StopIterationType) {
										done = true
										return
									}
									panic(r)
								}
							}()
							v = ctx.Invoke1(nextFn, it)
							if ctx != nil && ctx.HasException() {
								if IsInstance(ctx.ActiveException, StopIterationType) {
									ctx.ClearException()
									done = true
									return
								}
							}
						}()
						if done {
							break
						}
						res = append(res, v)
					}
					return res
				}
			}
		}
	}
	typeName := "object"
	if tp != nil {
		typeName = tp.Name
	}
	panic("TypeError: '" + typeName + "' object is not iterable")
}

// NewFrozenDictVal wraps target dict into a frozendict Value.
func NewFrozenDictVal(target Value) Value {
	if !target.IsDict() {
		target = NewDictVal(0)
	}
	mp := &PyMappingProxy{
		Header: ObjectHeader{
			Kind: KindMappingProxy,
			Type: FrozenDictType,
		},
		Mapping: target,
	}
	return Value{ptr: &mp.Header}
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
	primAttrName := InternString("\x00prim")
	initPrimRootShape := func(t *PyType) {
		t.RootShape = &Shape{
			ID:        allocShapeID(),
			Type:      t,
			SlotIndex: 0,
			Attrs:     []ShapeAttr{{Name: primAttrName, Index: 0}},
		}
		empty := make([]ShapeTransition, 0, 4)
		t.RootShape.transitions.Store(&empty)
	}
	unwrapIntVal := func(v Value) (int64, bool) {
		if v.IsInt64() {
			return v.AsInt64(), true
		}
		if v.IsBool() {
			if v.AsBool() {
				return 1, true
			}
			return 0, true
		}
		if v.IsInstance() {
			inst := v.AsInstance()
			if len(inst.Slots) > 0 && inst.Slots[0].IsInt64() {
				return inst.Slots[0].AsInt64(), true
			}
		}
		return 0, false
	}

	NoneType = MustNewType("NoneType", []*PyType{ObjectType}, nil)
	IntType = MustNewType("int", []*PyType{ObjectType}, nil)
	initPrimRootShape(IntType)
	IntType.Slots.NbAdd = numericNbAdd
	IntType.Slots.NbSub = numericNbSub
	IntType.Slots.NbMul = numericNbMul
	IntType.Slots.NbAbs = intNbAbs

	intNewBuiltin := NewBuiltinFuncVal("int.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		valToConvert := Int64Val(0)
		if len(args) > 1 {
			valToConvert = args[1]
		}
		var num int64
		switch {
		case valToConvert.IsInt64():
			num = valToConvert.AsInt64()
		case valToConvert.IsFloat64():
			num = int64(valToConvert.AsFloat64())
		case valToConvert.IsBool():
			if valToConvert.AsBool() {
				num = 1
			}
		case valToConvert.IsAnyString():
			n, _ := strconv.ParseInt(strings.TrimSpace(valToConvert.AsString()), 10, 64)
			num = n
		case valToConvert.IsInstance():
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
		instInst := inst.AsInstance()
		if len(instInst.Slots) == 0 {
			instInst.Slots = []Value{Int64Val(num)}
		} else {
			instInst.Slots[0] = Int64Val(num)
		}
		return inst
	})
	IntType.Dict.Set(DunderNew, intNewBuiltin)
	IntType.dunderNew = intNewBuiltin
	intReprBuiltin := NewBuiltinFuncVal("int.__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if args[0].Kind() == KindBigInt {
				return NewStringVal(args[0].AsBigInt().Val.String())
			}
			if n, ok := unwrapIntVal(args[0]); ok {
				return IntToStringVal(n)
			}
		}
		return NewStringVal("0")
	})
	IntType.Dict.Set(InternString("__repr__"), intReprBuiltin)
	IntType.Dict.Set(InternString("__str__"), intReprBuiltin)
	IntType.Dict.Set(InternString("__format__"), NewBuiltinFuncVal("int.__format__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) != 2 || !args[1].IsAnyString() {
			panic("TypeError: int.__format__() takes exactly one argument (str)")
		}
		spec := args[1].AsString()
		if args[0].IsBool() {
			return NewStringVal(FormatBoolSpec(args[0].AsBool(), spec))
		}
		if args[0].Kind() == KindBigInt {
			return NewStringVal(FormatIntSpec(&args[0].AsBigInt().Val, spec))
		}
		if n, ok := unwrapIntVal(args[0]); ok {
			return NewStringVal(FormatInt64Spec(n, spec))
		}
		panic("TypeError: descriptor '__format__' requires a 'int' object")
	}))
	IntType.Dict.Set(DunderEq, NewBuiltinFuncVal("int.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		if !okA {
			return NotImplementedVal()
		}
		if b, okB := unwrapIntVal(args[1]); okB {
			return BoolVal(a == b)
		}
		if args[1].IsFloat64() {
			return BoolVal(float64(a) == args[1].AsFloat64())
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderNe, NewBuiltinFuncVal("int.__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		if !okA {
			return NotImplementedVal()
		}
		if b, okB := unwrapIntVal(args[1]); okB {
			return BoolVal(a != b)
		}
		if args[1].IsFloat64() {
			return BoolVal(float64(a) != args[1].AsFloat64())
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderLt, NewBuiltinFuncVal("int.__lt__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return BoolVal(a < b)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderLe, NewBuiltinFuncVal("int.__le__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return BoolVal(a <= b)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderGt, NewBuiltinFuncVal("int.__gt__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return BoolVal(a > b)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderGe, NewBuiltinFuncVal("int.__ge__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return BoolVal(a >= b)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(InternString("__hash__"), NewBuiltinFuncVal("int.__hash__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if n, ok := unwrapIntVal(args[0]); ok {
				return Int64Val(n)
			}
		}
		return Int64Val(0)
	}))
	IntType.Dict.Set(InternString("__bool__"), NewBuiltinFuncVal("int.__bool__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if n, ok := unwrapIntVal(args[0]); ok {
				return BoolVal(n != 0)
			}
		}
		return BoolVal(false)
	}))
	intIndexBuiltin := NewBuiltinFuncVal("int.__int__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if n, ok := unwrapIntVal(args[0]); ok {
				return Int64Val(n)
			}
		}
		return Int64Val(0)
	})
	IntType.Dict.Set(InternString("__int__"), intIndexBuiltin)
	IntType.Dict.Set(InternString("__index__"), intIndexBuiltin)
	IntType.Dict.Set(DunderAdd, NewBuiltinFuncVal("int.__add__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(a + b)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderRAdd, NewBuiltinFuncVal("int.__radd__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(b + a)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderSub, NewBuiltinFuncVal("int.__sub__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(a - b)
		}
		return NotImplementedVal()
	}))
	IntType.Dict.Set(DunderRSub, NewBuiltinFuncVal("int.__rsub__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(b - a)
		}
		return NotImplementedVal()
	}))
	intAndBuiltin := NewBuiltinFuncVal("int.__and__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(a & b)
		}
		return NotImplementedVal()
	})
	IntType.Dict.Set(InternString("__and__"), intAndBuiltin)
	IntType.Dict.Set(DunderRAnd, intAndBuiltin)
	intOrBuiltin := NewBuiltinFuncVal("int.__or__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(a | b)
		}
		return NotImplementedVal()
	})
	IntType.Dict.Set(InternString("__or__"), intOrBuiltin)
	IntType.Dict.Set(DunderROr, intOrBuiltin)
	intXorBuiltin := NewBuiltinFuncVal("int.__xor__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapIntVal(args[0])
		b, okB := unwrapIntVal(args[1])
		if okA && okB {
			return Int64Val(a ^ b)
		}
		return NotImplementedVal()
	})
	IntType.Dict.Set(InternString("__xor__"), intXorBuiltin)
	IntType.Dict.Set(DunderRXor, intXorBuiltin)
	IntType.Dict.Set(InternString("__invert__"), NewBuiltinFuncVal("int.__invert__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if n, ok := unwrapIntVal(args[0]); ok {
				return Int64Val(^n)
			}
		}
		return Int64Val(-1)
	}))
	IntType.Dict.Set(InternString("bit_length"), NewBuiltinFuncVal("int.bit_length", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if n, ok := unwrapIntVal(args[0]); ok {
				var u uint64
				if n < 0 {
					u = uint64(-n)
				} else {
					u = uint64(n)
				}
				return Int64Val(int64(bits.Len64(u)))
			}
			if args[0].Kind() == KindBigInt {
				return Int64Val(int64(args[0].AsBigInt().Val.BitLen()))
			}
		}
		return Int64Val(0)
	}))
	IntType.Dict.Set(InternString("bit_count"), NewBuiltinFuncVal("int.bit_count", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if n, ok := unwrapIntVal(args[0]); ok {
				var u uint64
				if n < 0 {
					u = uint64(-n)
				} else {
					u = uint64(n)
				}
				return Int64Val(int64(bits.OnesCount64(u)))
			}
			if args[0].Kind() == KindBigInt {
				words := args[0].AsBigInt().Val.Bits()
				var cnt int
				for _, w := range words {
					cnt += bits.OnesCount(uint(w))
				}
				return Int64Val(int64(cnt))
			}
		}
		return Int64Val(0)
	}))

	IntType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pa, kw := splitStrKw(args, kwnames)
		if len(pa) == 0 {
			return Int64Val(0)
		}
		base := 10
		if len(pa) > 1 && pa[1].IsInt64() {
			base = int(pa[1].AsInt64())
		} else if bVal, ok := kw["base"]; ok && bVal.IsInt64() {
			base = int(bVal.AsInt64())
		}
		a := pa[0]
		if a.IsInt64() && base == 10 {
			return a
		}
		if a.Kind() == KindBigInt && base == 10 {
			return a
		}
		if a.IsFloat64() && base == 10 {
			return Int64Val(int64(a.AsFloat64()))
		}
		if a.IsBool() && base == 10 {
			if a.AsBool() {
				return Int64Val(1)
			}
			return Int64Val(0)
		}
		if a.IsAnyString() || a.IsBytes() || a.IsByteArray() {
			var s string
			switch {
			case a.IsAnyString():
				s = strings.TrimSpace(a.AsString())
			case a.IsBytes():
				s = strings.TrimSpace(string(a.AsBytes().Data))
			default:
				s = strings.TrimSpace(string(a.AsByteArray().Data))
			}
			if base == 16 {
				switch {
				case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
					s = s[2:]
				case strings.HasPrefix(s, "+0x") || strings.HasPrefix(s, "+0X"):
					s = s[3:]
				case strings.HasPrefix(s, "-0x") || strings.HasPrefix(s, "-0X"):
					s = "-" + s[3:]
				}
			}
			bi := new(big.Int)
			if _, ok := bi.SetString(s, base); ok {
				return normBig(bi)
			}
			panic("ValueError: invalid literal for int() with base " + strconv.Itoa(base) + ": '" + s + "'")
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

	IntType.Dict.Set(InternString("from_bytes"), NewBuiltinFuncVal("int.from_bytes", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pa, kw := splitStrKw(args, kwnames)
		if len(pa) > 0 && pa[0].IsType() {
			pa = pa[1:]
		}
		if len(pa) < 1 {
			panic("TypeError: from_bytes() takes at least 1 argument")
		}
		var rawBytes []byte
		if b, ok := bytesMethodArg(pa[0]); ok {
			rawBytes = b
		} else {
			items := ExtractIterableItems(ctx, pa[0])
			rawBytes = make([]byte, len(items))
			for i, it := range items {
				if it.IsInt64() {
					bVal := it.AsInt64()
					if bVal < 0 || bVal > 255 {
						panic("ValueError: byte must be in range(0, 256)")
					}
					rawBytes[i] = byte(bVal)
				} else {
					panic("TypeError: an integer is required")
				}
			}
		}
		byteorder := "big"
		if len(pa) > 1 && pa[1].IsAnyString() {
			byteorder = pa[1].AsString()
		} else if bo, ok := kw["byteorder"]; ok && bo.IsAnyString() {
			byteorder = bo.AsString()
		}
		signed := false
		if sVal, ok := kw["signed"]; ok {
			signed = sVal.IsTruthy()
		} else if len(pa) > 2 {
			signed = pa[2].IsTruthy()
		}
		if len(rawBytes) == 0 {
			return Int64Val(0)
		}
		bCopy := make([]byte, len(rawBytes))
		copy(bCopy, rawBytes)
		if byteorder == "little" {
			for i, j := 0, len(bCopy)-1; i < j; i, j = i+1, j-1 {
				bCopy[i], bCopy[j] = bCopy[j], bCopy[i]
			}
		} else if byteorder != "big" {
			panic("ValueError: byteorder must be either 'little' or 'big'")
		}
		bi := new(big.Int).SetBytes(bCopy)
		if signed && (bCopy[0]&0x80) != 0 {
			shift := uint(len(bCopy) * 8)
			twoPow := new(big.Int).Lsh(big.NewInt(1), shift)
			bi.Sub(bi, twoPow)
		}
		return normBig(bi)
	}))

	IntType.Dict.Set(InternString("to_bytes"), NewBuiltinFuncVal("int.to_bytes", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pa, kw := splitStrKw(args, kwnames)
		if len(pa) < 2 {
			panic("TypeError: to_bytes() takes at least 1 argument")
		}
		self := pa[0]
		length := int(pa[1].AsInt64())
		byteorder := "big"
		if len(pa) > 2 && pa[2].IsAnyString() {
			byteorder = pa[2].AsString()
		} else if bo, ok := kw["byteorder"]; ok && bo.IsAnyString() {
			byteorder = bo.AsString()
		}
		signed := false
		if sVal, ok := kw["signed"]; ok {
			signed = sVal.IsTruthy()
		}
		bi := toBigInt(self)
		if !signed && bi.Sign() < 0 {
			panic("OverflowError: can't convert negative int to unsigned")
		}
		if signed && bi.Sign() < 0 {
			shift := uint(length * 8)
			twoPow := new(big.Int).Lsh(big.NewInt(1), shift)
			bi = new(big.Int).Add(bi, twoPow)
		}
		b := bi.Bytes()
		if len(b) > length {
			panic("OverflowError: int too big to convert")
		}
		res := make([]byte, length)
		copy(res[length-len(b):], b)
		if byteorder == "little" {
			for i, j := 0, len(res)-1; i < j; i, j = i+1, j-1 {
				res[i], res[j] = res[j], res[i]
			}
		} else if byteorder != "big" {
			panic("ValueError: byteorder must be either 'little' or 'big'")
		}
		return NewBytesVal(res)
	}))

	IntType.Dict.Set(InternString("as_integer_ratio"), NewBuiltinFuncVal("int.as_integer_ratio", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: as_integer_ratio() takes at least 1 argument")
		}
		self := args[0]
		return NewTupleVal(self, Int64Val(1))
	}))

	BoolType = MustNewType("bool", []*PyType{IntType}, nil)
	BoolType.Slots.NbAdd = numericNbAdd
	BoolType.Slots.NbSub = numericNbSub
	BoolType.Slots.NbMul = numericNbMul
	BoolType.Slots.NbAbs = intNbAbs
	BoolType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return BoolVal(false)
		}
		a := args[0]
		if a.IsInstance() || a.Kind() == KindNative || a.IsMappingProxy() {
			if fn, _, ok := a.Type().LookupMRO(InternString("__bool__")); ok {
				res := ctx.Invoke1(fn, a)
				return BoolVal(res.IsTruthy())
			}
			if sqLen := a.Type().Slots.SqLen; sqLen != nil {
				return BoolVal(sqLen(ctx, a) != 0)
			}
		}
		return BoolVal(a.IsTruthy())
	}
	boolNewBuiltin := NewBuiltinFuncVal("bool.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) <= 1 {
			return BoolVal(false)
		}
		return BoolType.Slots.TpCall(ctx, callable, args[1:], nil)
	})
	BoolType.Dict.Set(DunderNew, boolNewBuiltin)
	BoolType.dunderNew = boolNewBuiltin

	FloatType = MustNewType("float", []*PyType{ObjectType}, nil)
	initPrimRootShape(FloatType)
	FloatType.Slots.NbAdd = numericNbAdd
	FloatType.Slots.NbSub = numericNbSub
	FloatType.Slots.NbMul = numericNbMul
	FloatType.Slots.NbAbs = floatNbAbs

	floatNewBuiltin := NewBuiltinFuncVal("float.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		var f float64
		if len(args) > 1 {
			a := args[1]
			switch {
			case a.IsFloat64():
				f = a.AsFloat64()
			case a.IsInt64():
				f = float64(a.AsInt64())
			case a.IsBool():
				if a.AsBool() {
					f = 1.0
				} else {
					f = 0.0
				}
			case a.IsAnyString():
				v, err := strconv.ParseFloat(strings.TrimSpace(a.AsString()), 64)
				if err == nil {
					f = v
				} else {
					panic("ValueError: could not convert string to float: " + a.AsString())
				}
			case a.IsInstance():
				inst := a.AsInstance()
				if len(inst.Slots) > 0 && inst.Slots[0].IsFloat64() {
					f = inst.Slots[0].AsFloat64()
				}
			}
		}
		if len(args) > 0 && args[0].IsType() && args[0].AsType() == FloatType {
			return Float64Val(f)
		}
		cls := FloatType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		inst := NewInstanceOfType(cls)
		instInst := inst.AsInstance()
		if len(instInst.Slots) == 0 {
			instInst.Slots = []Value{Float64Val(f)}
		} else {
			instInst.Slots[0] = Float64Val(f)
		}
		return inst
	})
	FloatType.Dict.Set(InternString("__format__"), NewBuiltinFuncVal("float.__format__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) != 2 || !args[1].IsAnyString() {
			panic("TypeError: float.__format__() takes exactly one argument (str)")
		}
		a := args[0]
		if !a.IsFloat64() && a.IsInstance() {
			if inst := a.AsInstance(); len(inst.Slots) > 0 && inst.Slots[0].IsFloat64() {
				a = inst.Slots[0]
			}
		}
		if !a.IsFloat64() {
			panic("TypeError: descriptor '__format__' requires a 'float' object")
		}
		return NewStringVal(FormatFloatSpec(a.AsFloat64(), args[1].AsString()))
	}))
	FloatType.Dict.Set(DunderNew, floatNewBuiltin)
	FloatType.dunderNew = floatNewBuiltin

	FloatType.Dict.Set(InternString("as_integer_ratio"), NewBuiltinFuncVal("float.as_integer_ratio", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: as_integer_ratio() takes at least 1 argument")
		}
		a := args[0]
		if !a.IsFloat64() && a.IsInstance() {
			if inst := a.AsInstance(); len(inst.Slots) > 0 && inst.Slots[0].IsFloat64() {
				a = inst.Slots[0]
			}
		}
		if !a.IsFloat64() {
			panic("TypeError: descriptor 'as_integer_ratio' requires a 'float' object")
		}
		f := a.AsFloat64()
		if math.IsInf(f, 0) {
			panic("OverflowError: cannot convert Infinity to integer ratio")
		}
		if math.IsNaN(f) {
			panic("ValueError: cannot convert NaN to integer ratio")
		}
		rat := new(big.Rat).SetFloat64(f)
		if rat == nil {
			panic("ValueError: cannot convert float to integer ratio")
		}
		num := rat.Num()
		denom := rat.Denom()
		var numVal, denomVal Value
		if num.IsInt64() {
			numVal = Int64Val(num.Int64())
		} else {
			numVal = NewBigIntVal(new(big.Int).Set(num))
		}
		if denom.IsInt64() {
			denomVal = Int64Val(denom.Int64())
		} else {
			denomVal = NewBigIntVal(new(big.Int).Set(denom))
		}
		return NewTupleVal(numVal, denomVal)
	}))

	FloatType.Dict.Set(InternString("is_integer"), NewBuiltinFuncVal("float.is_integer", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: is_integer() takes at least 1 argument")
		}
		a := args[0]
		if !a.IsFloat64() && a.IsInstance() {
			if inst := a.AsInstance(); len(inst.Slots) > 0 && inst.Slots[0].IsFloat64() {
				a = inst.Slots[0]
			}
		}
		if !a.IsFloat64() {
			panic("TypeError: descriptor 'is_integer' requires a 'float' object")
		}
		f := a.AsFloat64()
		return BoolVal(!math.IsNaN(f) && !math.IsInf(f, 0) && math.Trunc(f) == f)
	}))

	FloatType.Dict.Set(InternString("hex"), NewBuiltinFuncVal("float.hex", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: hex() takes at least 1 argument")
		}
		a := args[0]
		if !a.IsFloat64() && a.IsInstance() {
			if inst := a.AsInstance(); len(inst.Slots) > 0 && inst.Slots[0].IsFloat64() {
				a = inst.Slots[0]
			}
		}
		if !a.IsFloat64() {
			panic("TypeError: descriptor 'hex' requires a 'float' object")
		}
		f := a.AsFloat64()
		if math.IsNaN(f) {
			return NewStringVal("nan")
		}
		if math.IsInf(f, 1) {
			return NewStringVal("inf")
		}
		if math.IsInf(f, -1) {
			return NewStringVal("-inf")
		}
		return NewStringVal(fmt.Sprintf("%x", f))
	}))

	fromhexBuiltin := NewBuiltinFuncVal("float.fromhex", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: fromhex() takes at least 1 argument")
		}
		target := args[0]
		if target.IsType() && len(args) >= 2 {
			target = args[1]
		}
		if !target.IsAnyString() {
			panic("TypeError: fromhex() argument must be str")
		}
		s := strings.TrimSpace(target.AsString())
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			panic("ValueError: invalid hexadecimal floating-point string: " + s)
		}
		return Float64Val(f)
	})
	FloatType.Dict.Set(InternString("fromhex"), fromhexBuiltin)

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

	initComplexType()

	BigIntType = MustNewType("bigint", []*PyType{IntType}, nil)
	BigIntType.Slots.NbAdd = numericNbAdd
	BigIntType.Slots.NbSub = numericNbSub
	BigIntType.Slots.NbMul = numericNbMul
	BigIntType.Slots.NbAbs = bigIntNbAbs

	StrType = MustNewType("str", []*PyType{ObjectType}, nil)
	initPrimRootShape(StrType)
	unwrapStrVal := func(v Value) (string, bool) {
		if v.IsAnyString() {
			return v.AsString(), true
		}
		if v.IsInstance() {
			inst := v.AsInstance()
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				return inst.Slots[0].AsString(), true
			}
		}
		return "", false
	}
	StrType.Slots.NbAdd = strNbAdd
	StrType.Slots.NbMul = strNbMul
	StrType.Slots.SqLen = strSqLen
	StrType.Slots.SqItem = strSqItem
	StrType.Slots.MpSubscript = strMpSubscript
	StrType.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
		return NewIterVal(o)
	}
	strNewBuiltin := NewBuiltinFuncVal("str.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := NewStringVal("")
		npos := len(args) - len(kwnames)
		if npos > 1 {
			a := args[1]
			var encodingVal Value
			var errorsVal Value
			if npos >= 3 {
				encodingVal = args[2]
			}
			if npos >= 4 {
				errorsVal = args[3]
			}
			for i, kn := range kwnames {
				name, _ := unwrapStrVal(kn)
				switch name {
				case "encoding":
					encodingVal = args[npos+i]
				case "errors":
					errorsVal = args[npos+i]
				}
			}

			if (errorsVal.ptr != nil && !errorsVal.IsNone()) && (encodingVal.ptr == nil || encodingVal.IsNone()) {
				encodingVal = NewStringVal("utf-8")
			}

			switch {
			case encodingVal.ptr != nil && !encodingVal.IsNone():
				b, ok := bytesMethodArg(a)
				if !ok {
					panic("TypeError: decoding to str need a bytes-like object, got '" + a.Type().Name + "'")
				}
				encStr, ok := unwrapStrVal(encodingVal)
				if !ok {
					panic("TypeError: str() argument 'encoding' must be str")
				}
				decoded, err := DecodeBytesToString(b, encStr)
				if err != nil {
					panic(err.Error())
				}
				s = NewStringVal(decoded)
			case a.IsInt64():
				s = IntToStringVal(a.AsInt64())
			case a.IsAnyString():
				s = a
			case a.IsInstance():
				inst := a.AsInstance()
				switch {
				case len(inst.Slots) > 0 && inst.Slots[0].IsAnyString():
					s = inst.Slots[0]
				case ctx != nil && a.Type() != nil && a.Type() != StrType:
					if strFn, _, ok := a.Type().LookupMRO(InternString("__str__")); ok {
						res := ctx.Invoke1(strFn, a)
						if res.IsAnyString() {
							s = res
						} else {
							s = NewStringVal(res.String())
						}
					} else {
						s = NewStringVal(a.String())
					}
				default:
					s = NewStringVal(a.String())
				}
			case ctx != nil && a.Type() != nil && a.Type() != StrType:
				if strFn, _, ok := a.Type().LookupMRO(InternString("__str__")); ok {
					res := ctx.Invoke1(strFn, a)
					if res.IsAnyString() {
						s = res
					} else {
						s = NewStringVal(res.String())
					}
				} else if reprFn, _, ok := a.Type().LookupMRO(InternString("__repr__")); ok {
					res := ctx.Invoke1(reprFn, a)
					if res.IsAnyString() {
						s = res
					} else {
						s = NewStringVal(res.String())
					}
				} else {
					s = NewStringVal(a.String())
				}
			default:
				s = NewStringVal(a.String())
			}
		}
		if len(args) > 0 && args[0].IsType() && args[0].AsType() == StrType {
			return s
		}
		cls := StrType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		inst := NewInstanceOfType(cls)
		instInst := inst.AsInstance()
		if len(instInst.Slots) == 0 {
			instInst.Slots = []Value{s}
		} else {
			instInst.Slots[0] = s
		}
		return inst
	})
	StrType.Dict.Set(DunderNew, strNewBuiltin)
	StrType.dunderNew = strNewBuiltin
	StrType.Dict.Set(InternString("__str__"), NewBuiltinFuncVal("str.__str__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if s, ok := unwrapStrVal(args[0]); ok {
				return NewStringVal(s)
			}
		}
		return NewStringVal("")
	}))
	StrType.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal("str.__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if s, ok := unwrapStrVal(args[0]); ok {
				return NewStringVal(strconv.Quote(s))
			}
		}
		return NewStringVal("''")
	}))
	StrType.Dict.Set(InternString("__format__"), NewBuiltinFuncVal("str.__format__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) != 2 || !args[1].IsAnyString() {
			panic("TypeError: str.__format__() takes exactly one argument (str)")
		}
		if s, ok := unwrapStrVal(args[0]); ok {
			return NewStringVal(FormatStrSpec(s, args[1].AsString()))
		}
		panic("TypeError: descriptor '__format__' requires a 'str' object")
	}))
	StrType.Dict.Set(DunderEq, NewBuiltinFuncVal("str.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapStrVal(args[0])
		b, okB := unwrapStrVal(args[1])
		if okA && okB {
			return BoolVal(a == b)
		}
		return NotImplementedVal()
	}))
	StrType.Dict.Set(DunderNe, NewBuiltinFuncVal("str.__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapStrVal(args[0])
		b, okB := unwrapStrVal(args[1])
		if okA && okB {
			return BoolVal(a != b)
		}
		return NotImplementedVal()
	}))
	StrType.Dict.Set(DunderLt, NewBuiltinFuncVal("str.__lt__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapStrVal(args[0])
		b, okB := unwrapStrVal(args[1])
		if okA && okB {
			return BoolVal(a < b)
		}
		return NotImplementedVal()
	}))
	StrType.Dict.Set(DunderLe, NewBuiltinFuncVal("str.__le__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapStrVal(args[0])
		b, okB := unwrapStrVal(args[1])
		if okA && okB {
			return BoolVal(a <= b)
		}
		return NotImplementedVal()
	}))
	StrType.Dict.Set(DunderGt, NewBuiltinFuncVal("str.__gt__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapStrVal(args[0])
		b, okB := unwrapStrVal(args[1])
		if okA && okB {
			return BoolVal(a > b)
		}
		return NotImplementedVal()
	}))
	StrType.Dict.Set(DunderGe, NewBuiltinFuncVal("str.__ge__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		a, okA := unwrapStrVal(args[0])
		b, okB := unwrapStrVal(args[1])
		if okA && okB {
			return BoolVal(a >= b)
		}
		return NotImplementedVal()
	}))
	StrType.Dict.Set(InternString("__hash__"), NewBuiltinFuncVal("str.__hash__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			if s, ok := unwrapStrVal(args[0]); ok {
				return Int64Val(int64(HashString(s)))
			}
		}
		return Int64Val(0)
	}))

	StrType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		npos := len(args) - len(kwnames)
		if npos == 0 && len(kwnames) == 0 {
			return NewStringVal("")
		}
		a := args[0]
		var encodingVal Value
		var errorsVal Value
		if npos >= 2 {
			encodingVal = args[1]
		}
		if npos >= 3 {
			errorsVal = args[2]
		}
		for i, kn := range kwnames {
			name, _ := unwrapStrVal(kn)
			switch name {
			case "encoding":
				encodingVal = args[npos+i]
			case "errors":
				errorsVal = args[npos+i]
			}
		}
		if (errorsVal.ptr != nil && !errorsVal.IsNone()) && (encodingVal.ptr == nil || encodingVal.IsNone()) {
			encodingVal = NewStringVal("utf-8")
		}
		if encodingVal.ptr != nil && !encodingVal.IsNone() {
			b, ok := bytesMethodArg(a)
			if !ok {
				panic("TypeError: decoding to str need a bytes-like object, got '" + a.Type().Name + "'")
			}
			encStr, ok := unwrapStrVal(encodingVal)
			if !ok {
				panic("TypeError: str() argument 'encoding' must be str")
			}
			decoded, err := DecodeBytesToString(b, encStr)
			if err != nil {
				panic(err.Error())
			}
			return NewStringVal(decoded)
		}
		if a.IsInt64() {
			return IntToStringVal(a.AsInt64())
		}
		if a.IsAnyString() {
			if callable.IsType() && callable.AsType() == StrType {
				if a.Type() == StrType {
					return a
				}
				return NewStringVal(a.AsString())
			}
			cls := StrType
			if callable.IsType() {
				cls = callable.AsType()
			}
			inst := NewInstanceOfType(cls)
			instInst := inst.AsInstance()
			s := NewStringVal(a.AsString())
			if len(instInst.Slots) == 0 {
				instInst.Slots = []Value{s}
			} else {
				instInst.Slots[0] = s
			}
			return inst
		}
		if ctx != nil && a.Type() != nil && a.Type() != StrType {
			if strFn, _, ok := a.Type().LookupMRO(InternString("__str__")); ok {
				res := ctx.Invoke1(strFn, a)
				if res.IsAnyString() {
					return res
				}
				return NewStringVal(res.String())
			}
			if reprFn, _, ok := a.Type().LookupMRO(InternString("__repr__")); ok {
				res := ctx.Invoke1(reprFn, a)
				if res.IsAnyString() {
					return res
				}
				return NewStringVal(res.String())
			}
		}
		return NewStringVal(a.String())
	}

	// Backfill Type = StrType on heap dunder strings interned prior to StrType creation.
	for _, d := range []Value{DunderGetItem, DunderSetItem, DunderCall, DunderInit, DunderGetAttr, DunderSetAttr} {
		if d.IsString() {
			d.ptr.Type = StrType
		}
	}

	StrType.Dict.Set(InternString("maketrans"), NewBuiltinFuncVal("maketrans", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		resDict := NewDictVal(16).AsDict()
		if len(args) == 1 {
			if args[0].IsDict() {
				for _, it := range args[0].AsDict().Items() {
					var k Value
					if it.Key.IsAnyString() && len(it.Key.AsString()) > 0 {
						k = Int64Val(int64([]rune(it.Key.AsString())[0]))
					} else {
						k = it.Key
					}
					resDict.Set(k, it.Value)
				}
			}
			return Value{ptr: &resDict.Header}
		}
		if len(args) >= 2 {
			fromStr := []rune(args[0].AsString())
			toStr := []rune(args[1].AsString())
			if len(fromStr) != len(toStr) {
				panic("ValueError: the first two maketrans arguments must have equal length")
			}
			for i := range fromStr {
				resDict.Set(Int64Val(int64(fromStr[i])), Int64Val(int64(toStr[i])))
			}
		}
		if len(args) >= 3 {
			for _, r := range args[2].AsString() {
				resDict.Set(Int64Val(int64(r)), NoneVal())
			}
		}
		return Value{ptr: &resDict.Header}
	}))

	StrType.Dict.Set(InternString("lower"), NewBuiltinFuncVal("lower", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'lower' of 'str' object needs an argument")
		}
		if s, ok := unwrapStrVal(args[0]); ok {
			return NewStringVal(strings.ToLower(s))
		}
		return NewStringVal(strings.ToLower(args[0].AsString()))
	}))

	StrType.Dict.Set(InternString("upper"), NewBuiltinFuncVal("upper", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'upper' of 'str' object needs an argument")
		}
		if s, ok := unwrapStrVal(args[0]); ok {
			return NewStringVal(strings.ToUpper(s))
		}
		return NewStringVal(strings.ToUpper(args[0].AsString()))
	}))

	StrType.Dict.Set(InternString("strip"), NewBuiltinFuncVal("strip", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'strip' of 'str' object needs an argument")
		}
		cutset := " \t\r\n\v\f"
		if len(args) > 1 && args[1].IsAnyString() {
			cutset = args[1].AsString()
		}
		return NewStringVal(strings.Trim(args[0].AsString(), cutset))
	}))

	StrType.Dict.Set(InternString("lstrip"), NewBuiltinFuncVal("lstrip", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'lstrip' of 'str' object needs an argument")
		}
		cutset := " \t\r\n\v\f"
		if len(args) > 1 && args[1].IsAnyString() {
			cutset = args[1].AsString()
		}
		return NewStringVal(strings.TrimLeft(args[0].AsString(), cutset))
	}))

	StrType.Dict.Set(InternString("rstrip"), NewBuiltinFuncVal("rstrip", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'rstrip' of 'str' object needs an argument")
		}
		cutset := " \t\r\n\v\f"
		if len(args) > 1 && args[1].IsAnyString() {
			cutset = args[1].AsString()
		}
		return NewStringVal(strings.TrimRight(args[0].AsString(), cutset))
	}))

	StrType.Dict.Set(InternString("startswith"), NewBuiltinFuncVal("startswith", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: startswith() takes at least 1 argument")
		}
		s := args[0].AsString()
		sLen := len(s)
		start := 0
		end := sLen
		if len(args) > 2 && !args[2].IsNone() {
			if args[2].IsInt64() {
				start = int(args[2].AsInt64())
			}
			if start < 0 {
				start += sLen
				if start < 0 {
					start = 0
				}
			} else if start > sLen {
				start = sLen
			}
		}
		if len(args) > 3 && !args[3].IsNone() {
			if args[3].IsInt64() {
				end = int(args[3].AsInt64())
			}
			if end < 0 {
				end += sLen
				if end < 0 {
					end = 0
				}
			} else if end > sLen {
				end = sLen
			}
		}
		if start > end {
			start = end
		}
		s = s[start:end]
		prefix := args[1]
		if prefix.IsTuple() {
			for _, item := range prefix.AsTuple().Items {
				if strings.HasPrefix(s, item.AsString()) {
					return BoolVal(true)
				}
			}
			return BoolVal(false)
		}
		return BoolVal(strings.HasPrefix(s, prefix.AsString()))
	}))

	StrType.Dict.Set(InternString("endswith"), NewBuiltinFuncVal("endswith", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: endswith() takes at least 1 argument")
		}
		s := args[0].AsString()
		sLen := len(s)
		start := 0
		end := sLen
		if len(args) > 2 && !args[2].IsNone() {
			if args[2].IsInt64() {
				start = int(args[2].AsInt64())
			}
			if start < 0 {
				start += sLen
				if start < 0 {
					start = 0
				}
			} else if start > sLen {
				start = sLen
			}
		}
		if len(args) > 3 && !args[3].IsNone() {
			if args[3].IsInt64() {
				end = int(args[3].AsInt64())
			}
			if end < 0 {
				end += sLen
				if end < 0 {
					end = 0
				}
			} else if end > sLen {
				end = sLen
			}
		}
		if start > end {
			start = end
		}
		s = s[start:end]
		suffix := args[1]
		if suffix.IsTuple() {
			for _, item := range suffix.AsTuple().Items {
				if strings.HasSuffix(s, item.AsString()) {
					return BoolVal(true)
				}
			}
			return BoolVal(false)
		}
		return BoolVal(strings.HasSuffix(s, suffix.AsString()))
	}))

	StrType.Dict.Set(InternString("replace"), NewBuiltinFuncVal("replace", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: replace() takes at least 2 arguments")
		}
		s := args[0].AsString()
		oldStr := args[1].AsString()
		newStr := args[2].AsString()
		n := -1
		if len(args) > 3 && args[3].IsInt64() {
			n = int(args[3].AsInt64())
		}
		return NewStringVal(strings.Replace(s, oldStr, newStr, n))
	}))

	StrType.Dict.Set(InternString("split"), NewBuiltinFuncVal("split", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'split' of 'str' object needs an argument")
		}
		s := args[0].AsString()
		var parts []string
		if len(args) > 1 && !args[1].IsNone() {
			sep := args[1].AsString()
			maxsplit := -1
			if len(args) > 2 && args[2].IsInt64() {
				maxsplit = int(args[2].AsInt64())
			}
			if maxsplit >= 0 {
				parts = strings.SplitN(s, sep, maxsplit+1)
			} else {
				parts = strings.Split(s, sep)
			}
		} else {
			parts = strings.Fields(s)
		}
		items := make([]Value, len(parts))
		for i, p := range parts {
			items[i] = NewStringVal(p)
		}
		return NewListVal(items...)
	}))

	TypeType.Dict.Set(DunderCall, NewBuiltinFuncVal("type.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor '__call__' of 'type' object needs an argument")
		}
		cls := args[0]
		return typeTpCall(ctx, cls, args[1:], kwnames)
	}))

	StrType.Dict.Set(InternString("join"), NewBuiltinFuncVal("join", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: join() takes exactly one argument")
		}
		sep := args[0].AsString()
		items := ExtractIterableItems(ctx, args[1])
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = it.AsString()
		}
		return NewStringVal(strings.Join(parts, sep))
	}))

	StrType.Dict.Set(InternString("find"), NewBuiltinFuncVal("find", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: find() takes at least 1 argument")
		}
		s := args[0].AsString()
		sub := args[1].AsString()
		start := 0
		if len(args) > 2 && args[2].IsInt64() {
			start = int(args[2].AsInt64())
			if start < 0 {
				start += len(s)
				if start < 0 {
					start = 0
				}
			}
		}
		if start >= len(s) {
			return Int64Val(-1)
		}
		idx := strings.Index(s[start:], sub)
		if idx == -1 {
			return Int64Val(-1)
		}
		return Int64Val(int64(start + idx))
	}))

	StrType.Dict.Set(InternString("rfind"), NewBuiltinFuncVal("rfind", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: rfind() takes at least 1 argument")
		}
		s := args[0].AsString()
		sub := args[1].AsString()
		idx := strings.LastIndex(s, sub)
		return Int64Val(int64(idx))
	}))

	StrType.Dict.Set(InternString("count"), NewBuiltinFuncVal("count", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: count() takes at least 1 argument")
		}
		s := args[0].AsString()
		sub := args[1].AsString()
		return Int64Val(int64(strings.Count(s, sub)))
	}))

	StrType.Dict.Set(InternString("encode"), NewBuiltinFuncVal("encode", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'encode' of 'str' object needs an argument")
		}
		return NewBytesVal([]byte(args[0].AsString()))
	}))

	initStrExtraMethods()

	ListType = MustNewType("list", []*PyType{ObjectType}, nil)
	ListType.Slots.NbAdd = listNbAdd
	ListType.Slots.NbMul = listNbMul
	ListType.Slots.SqLen = listSqLen
	ListType.Slots.SqItem = listSqItem
	ListType.Slots.MpSubscript = listMpSubscript
	ListType.Slots.SqAssItem = listSqAssItem
	ListType.Slots.MpAssSubscript = listMpAssSubscript
	ListType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return NewListVal()
		}
		return NewListVal(ExtractIterableItems(ctx, args[0])...)
	}
	listNewBuiltin := NewBuiltinFuncVal("list.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		cls := ListType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		l := NewListVal().AsList()
		l.Header.Type = cls
		return Value{num: uint64(KindList), ptr: &l.Header}
	})
	ListType.Dict.Set(DunderNew, listNewBuiltin)
	ListType.dunderNew = listNewBuiltin
	listInitBuiltin := NewBuiltinFuncVal("list.__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsList() {
			return NoneVal()
		}
		l := args[0].AsList()
		l.Items = l.Items[:0]
		if len(args) > 1 {
			l.Items = append(l.Items, ExtractIterableItems(ctx, args[1])...)
		}
		return NoneVal()
	})
	ListType.Dict.Set(DunderInit, listInitBuiltin)
	ListType.dunderInit = listInitBuiltin
	ListType.Dict.Set(DunderGetItem, NewBuiltinFuncVal("__getitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__getitem__' of 'list' object needs an argument")
		}
		return listMpSubscript(ctx, args[0], args[1])
	}))
	ListType.Dict.Set(DunderSetItem, NewBuiltinFuncVal("__setitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: descriptor '__setitem__' of 'list' object needs 2 arguments")
		}
		listMpAssSubscript(ctx, args[0], args[1], args[2])
		return NoneVal()
	}))
	ListType.Dict.Set(DunderLen, NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__len__' of 'list' object needs an argument")
		}
		return Int64Val(int64(args[0].AsList().Len()))
	}))
	ListType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'list' object needs an argument")
		}
		return NewIterVal(args[0])
	}))
	ListType.Dict.Set(DunderContains, NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return BoolVal(false)
		}
		return BoolVal(args[0].AsList().Contains(args[1]))
	}))
	ListType.Dict.Set(InternString("index"), NewBuiltinFuncVal("index", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: index expected at least 1 argument, got 0")
		}
		items := args[0].AsList().Items
		target := args[1]
		start := 0
		stop := len(items)
		if len(args) > 2 && args[2].IsInt64() {
			start = int(args[2].AsInt64())
			if start < 0 {
				start += len(items)
				if start < 0 {
					start = 0
				}
			}
		}
		if len(args) > 3 && args[3].IsInt64() {
			stop = int(args[3].AsInt64())
			if stop < 0 {
				stop += len(items)
			}
			if stop > len(items) {
				stop = len(items)
			}
		}
		for i := start; i < stop && i < len(items); i++ {
			if Equals(items[i], target) {
				return Int64Val(int64(i))
			}
		}
		panic(NewException(ValueErrorType, fmt.Sprintf("%v is not in list", target)))
	}))
	ListType.Dict.Set(InternString("count"), NewBuiltinFuncVal("count", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: count() takes exactly one argument")
		}
		items := args[0].AsList().Items
		target := args[1]
		var n int64
		for _, it := range items {
			if Equals(it, target) {
				n++
			}
		}
		return Int64Val(n)
	}))
	ListType.Dict.Set(InternString("sort"), NewBuiltinFuncVal("sort", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsList() {
			panic("TypeError: descriptor 'sort' requires a list")
		}
		var keyFn Value
		reverse := false
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				v := args[kwStart+i]
				switch kn.AsString() {
				case "key":
					keyFn = v
				case "reverse":
					reverse = v.IsTruthy()
				default:
					panic("TypeError: sort() got an unexpected keyword argument '" + kn.AsString() + "'")
				}
			}
		}
		l := args[0].AsList()
		l.mu.Lock()
		items := append([]Value(nil), l.Items...)
		l.mu.Unlock()
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if keyFn.ptr != nil && !keyFn.IsNone() {
				a = ctx.Call(keyFn, []Value{a}, nil)
				b = ctx.Call(keyFn, []Value{b}, nil)
			}
			if reverse {
				return LessThan(b, a).IsTruthy()
			}
			return LessThan(a, b).IsTruthy()
		})
		l.mu.Lock()
		l.Items = items
		l.mu.Unlock()
		return NoneVal()
	}))
	ListType.Dict.Set(InternString("insert"), NewBuiltinFuncVal("insert", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: insert expected 2 arguments")
		}
		l := args[0].AsList()
		idx := int(args[1].AsInt64())
		l.mu.Lock()
		defer l.mu.Unlock()
		if idx < 0 {
			idx += len(l.Items)
			if idx < 0 {
				idx = 0
			}
		}
		if idx > len(l.Items) {
			idx = len(l.Items)
		}
		l.Items = append(l.Items, NoneVal())
		copy(l.Items[idx+1:], l.Items[idx:])
		l.Items[idx] = args[2]
		return NoneVal()
	}))
	ListType.Dict.Set(InternString("remove"), NewBuiltinFuncVal("remove", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: list.remove() takes exactly one argument")
		}
		l := args[0].AsList()
		target := args[1]
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, it := range l.Items {
			if Equals(it, target) {
				copy(l.Items[i:], l.Items[i+1:])
				l.Items = l.Items[:len(l.Items)-1]
				return NoneVal()
			}
		}
		panic(NewException(ValueErrorType, "list.remove(x): x not in list"))
	}))
	ListType.Dict.Set(InternString("reverse"), NewBuiltinFuncVal("reverse", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'reverse' of 'list' object needs an argument")
		}
		l := args[0].AsList()
		l.mu.Lock()
		for i, j := 0, len(l.Items)-1; i < j; i, j = i+1, j-1 {
			l.Items[i], l.Items[j] = l.Items[j], l.Items[i]
		}
		l.mu.Unlock()
		return NoneVal()
	}))
	ListType.Dict.Set(InternString("copy"), NewBuiltinFuncVal("copy", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'copy' of 'list' object needs an argument")
		}
		return NewListVal(args[0].AsList().CopyItems()...)
	}))
	ListType.Dict.Set(InternString("append"), NewBuiltinFuncVal("append", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: append() takes exactly one argument")
		}
		list := args[0].AsList()
		list.Append(args[1])
		return NoneVal()
	}))

	ListType.Dict.Set(InternString("extend"), NewBuiltinFuncVal("extend", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: extend() takes exactly one argument")
		}
		list := args[0].AsList()
		it := NewIterVal(args[1]).AsIter()
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			list.Append(item)
		}
		return NoneVal()
	}))

	ListType.Dict.Set(InternString("pop"), NewBuiltinFuncVal("pop", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'pop' of 'list' object needs an argument")
		}
		list := args[0].AsList()
		list.mu.Lock()
		defer list.mu.Unlock()
		if len(list.Items) == 0 {
			panic(NewException(IndexErrorType, "pop from empty list"))
		}
		idx := len(list.Items) - 1
		if len(args) > 1 && args[1].IsInt64() {
			idx = int(args[1].AsInt64())
			if idx < 0 {
				idx += len(list.Items)
			}
		}
		if idx < 0 || idx >= len(list.Items) {
			panic(NewException(IndexErrorType, "pop index out of range"))
		}
		res := list.Items[idx]
		copy(list.Items[idx:], list.Items[idx+1:])
		list.Items = list.Items[:len(list.Items)-1]
		return res
	}))
	ListType.Dict.Set(InternString("clear"), NewBuiltinFuncVal("clear", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'clear' of 'list' object needs an argument")
		}
		list := args[0].AsList()
		list.mu.Lock()
		list.Items = list.Items[:0]
		list.mu.Unlock()
		return NoneVal()
	}))

	ListType.Dict.Set(InternString("__delitem__"), NewBuiltinFuncVal("__delitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__delitem__' of 'list' object needs 2 arguments")
		}
		list := args[0].AsList()
		if !args[1].IsInt64() {
			panic("TypeError: list indices must be integers")
		}
		idx := int(args[1].AsInt64())
		list.mu.Lock()
		defer list.mu.Unlock()
		if idx < 0 {
			idx += len(list.Items)
		}
		if idx < 0 || idx >= len(list.Items) {
			panic(NewException(IndexErrorType, "list assignment index out of range"))
		}
		copy(list.Items[idx:], list.Items[idx+1:])
		list.Items = list.Items[:len(list.Items)-1]
		return NoneVal()
	}))
	ListType.Dict.Set(InternString("__hash__"), NoneVal())
	ListType.Dict.Set(DunderEq, NewBuiltinFuncVal("list.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsList() || !args[1].IsList() {
			return NotImplementedVal()
		}
		return compareSliceRich(ctx, args[0].AsList().Items, args[1].AsList().Items, CmpEQ)
	}))
	ListType.Dict.Set(DunderNe, NewBuiltinFuncVal("list.__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsList() || !args[1].IsList() {
			return NotImplementedVal()
		}
		return compareSliceRich(ctx, args[0].AsList().Items, args[1].AsList().Items, CmpNEQ)
	}))
	listAddBuiltin := NewBuiltinFuncVal("list.__add__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: list.__add__ takes 2 arguments")
		}
		return listNbAdd(ctx, args[0], args[1])
	})
	ListType.Dict.Set(DunderAdd, listAddBuiltin)
	ListType.dunderAdd = listAddBuiltin
	listMulBuiltin := NewBuiltinFuncVal("list.__mul__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: list.__mul__ takes 2 arguments")
		}
		return listNbMul(ctx, args[0], args[1])
	})
	ListType.Dict.Set(DunderMul, listMulBuiltin)
	ListType.dunderMul = listMulBuiltin
	ListType.Dict.Set(DunderRMul, listMulBuiltin)
	ListType.dunderRMul = listMulBuiltin
	ListType.syncDunderSlots()

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
		return Value{num: uint64(KindDict), ptr: &d.Header}
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

	DictType.Dict.Set(InternString("__delitem__"), NewBuiltinFuncVal("__delitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__delitem__' of 'dict' object needs 2 arguments")
		}
		d := args[0].AsDict()
		if !d.Delete(args[1]) {
			panic(NewException(KeyErrorType, fmt.Sprintf("KeyError: %v", args[1])))
		}
		return NoneVal()
	}))

	DictType.Dict.Set(InternString("get"), NewBuiltinFuncVal("get", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: get expected at least 1 arguments, got 0")
		}
		d := args[0].AsDict()
		if val, ok := d.Get(args[1]); ok {
			return val
		}
		if len(args) > 2 {
			return args[2]
		}
		return NoneVal()
	}))

	DictKeysType = MustNewType("dict_keys", []*PyType{ObjectType}, nil)
	DictKeysType.Slots.SqLen = func(ctx *ExecContext, self Value) int64 {
		pk := (*PyDictKeys)(AsPointer(self))
		return int64(pk.Dict.Len())
	}
	DictKeysType.Slots.TpIter = func(ctx *ExecContext, self Value) Value {
		pk := (*PyDictKeys)(AsPointer(self))
		return NewIterVal(Value{num: uint64(KindDict), ptr: &pk.Dict.Header})
	}
	dictKeysSub := func(ctx *ExecContext, a, b Value) Value {
		s1 := NewSetVal(ExtractIterableItems(ctx, a)...).AsSet()
		s2 := NewSetVal(ExtractIterableItems(ctx, b)...).AsSet()
		return s1.Difference(s2)
	}
	DictKeysType.Slots.NbSub = dictKeysSub
	dictKeysOr := func(ctx *ExecContext, a, b Value) Value {
		s1 := NewSetVal(ExtractIterableItems(ctx, a)...).AsSet()
		s2 := NewSetVal(ExtractIterableItems(ctx, b)...).AsSet()
		return s1.Union(s2)
	}
	dictKeysAnd := func(ctx *ExecContext, a, b Value) Value {
		s1 := NewSetVal(ExtractIterableItems(ctx, a)...).AsSet()
		s2 := NewSetVal(ExtractIterableItems(ctx, b)...).AsSet()
		return s1.Intersection(s2)
	}
	dictKeysXor := func(ctx *ExecContext, a, b Value) Value {
		s1 := NewSetVal(ExtractIterableItems(ctx, a)...).AsSet()
		s2 := NewSetVal(ExtractIterableItems(ctx, b)...).AsSet()
		return s1.SymmetricDifference(s2)
	}
	DictKeysType.Dict.Set(DunderSub, NewBuiltinFuncVal("__sub__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __sub__() takes 1 argument")
		}
		return dictKeysSub(ctx, args[0], args[1])
	}))
	DictKeysType.Dict.Set(InternString("__rsub__"), NewBuiltinFuncVal("__rsub__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __rsub__() takes 1 argument")
		}
		return dictKeysSub(ctx, args[1], args[0])
	}))
	DictKeysType.Dict.Set(InternString("__or__"), NewBuiltinFuncVal("__or__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __or__() takes 1 argument")
		}
		return dictKeysOr(ctx, args[0], args[1])
	}))
	DictKeysType.Dict.Set(InternString("__ror__"), NewBuiltinFuncVal("__ror__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __ror__() takes 1 argument")
		}
		return dictKeysOr(ctx, args[1], args[0])
	}))
	DictKeysType.Dict.Set(InternString("__and__"), NewBuiltinFuncVal("__and__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __and__() takes 1 argument")
		}
		return dictKeysAnd(ctx, args[0], args[1])
	}))
	DictKeysType.Dict.Set(InternString("__rand__"), NewBuiltinFuncVal("__rand__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __rand__() takes 1 argument")
		}
		return dictKeysAnd(ctx, args[1], args[0])
	}))
	DictKeysType.Dict.Set(InternString("__xor__"), NewBuiltinFuncVal("__xor__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __xor__() takes 1 argument")
		}
		return dictKeysXor(ctx, args[0], args[1])
	}))
	DictKeysType.Dict.Set(InternString("__rxor__"), NewBuiltinFuncVal("__rxor__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __rxor__() takes 1 argument")
		}
		return dictKeysXor(ctx, args[1], args[0])
	}))
	DictKeysType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: __iter__() missing self")
		}
		return DictKeysType.Slots.TpIter(ctx, args[0])
	}))
	DictKeysType.Dict.Set(DunderLen, NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: __len__() missing self")
		}
		return Int64Val(DictKeysType.Slots.SqLen(ctx, args[0]))
	}))
	DictKeysType.Dict.Set(InternString("__contains__"), NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: __contains__() takes 1 argument")
		}
		pk := (*PyDictKeys)(AsPointer(args[0]))
		_, found := pk.Dict.Get(args[1])
		return BoolVal(found)
	}))

	DictType.Dict.Set(InternString("keys"), NewBuiltinFuncVal("keys", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'keys' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		return NewDictKeysVal(d)
	}))

	DictType.Dict.Set(InternString("values"), NewBuiltinFuncVal("values", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'values' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		return NewListVal(d.Values()...)
	}))

	DictType.Dict.Set(InternString("items"), NewBuiltinFuncVal("items", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'items' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		rawItems := d.Items()
		tuples := make([]Value, len(rawItems))
		for i, it := range rawItems {
			tuples[i] = NewTupleVal(it.Key, it.Value)
		}
		return NewListVal(tuples...)
	}))

	DictType.Dict.Set(InternString("copy"), NewBuiltinFuncVal("copy", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'copy' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		newD := NewDictVal(d.Len()).AsDict()
		for _, e := range d.ActiveEntries() {
			newD.Set(e.Key, e.Value)
		}
		return Value{ptr: &newD.Header}
	}))

	DictType.Dict.Set(InternString("clear"), NewBuiltinFuncVal("clear", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'clear' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		d.Clear()
		return NoneVal()
	}))

	DictType.Dict.Set(InternString("pop"), NewBuiltinFuncVal("pop", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: pop expected at least 1 arguments, got 0")
		}
		d := args[0].AsDict()
		if val, ok := d.Get(args[1]); ok {
			d.Delete(args[1])
			return val
		}
		if len(args) > 2 {
			return args[2]
		}
		panic(NewException(KeyErrorType, "KeyError: "+args[1].String()))
	}))

	DictType.Dict.Set(InternString("popitem"), NewBuiltinFuncVal("popitem", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'popitem' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		k, v, ok := d.PopItem()
		if !ok {
			panic(NewException(KeyErrorType, "popitem(): dictionary is empty"))
		}
		return NewTupleVal(k, v)
	}))

	DictType.Dict.Set(InternString("setdefault"), NewBuiltinFuncVal("setdefault", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: setdefault expected at least 1 arguments, got 0")
		}
		d := args[0].AsDict()
		if val, ok := d.Get(args[1]); ok {
			return val
		}
		defVal := NoneVal()
		if len(args) > 2 {
			defVal = args[2]
		}
		d.Set(args[1], defVal)
		return defVal
	}))

	DictType.Dict.Set(InternString("update"), NewBuiltinFuncVal("update", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'update' of 'dict' object needs an argument")
		}
		d := args[0].AsDict()
		posCount := len(args) - len(kwnames)
		if posCount > 1 {
			other := args[1]
			if other.IsDict() {
				od := other.AsDict()
				entries := od.ActiveEntries()
				for _, e := range entries {
					d.Set(e.Key, e.Value)
				}
			} else if other.IsMappingProxy() {
				mp := other.AsMappingProxy()
				if mp.Mapping.IsDict() {
					od := mp.Mapping.AsDict()
					entries := od.ActiveEntries()
					for _, e := range entries {
						d.Set(e.Key, e.Value)
					}
				}
			} else if keysFn, _, ok := other.Type().LookupMRO(InternString("keys")); ok {
				keysVal := ctx.Invoke1(keysFn, other)
				for _, k := range ExtractIterableItems(ctx, keysVal) {
					var v Value
					if getItemFn, _, ok := other.Type().LookupMRO(DunderGetItem); ok {
						v = ctx.Invoke2(getItemFn, other, k)
					}
					d.Set(k, v)
				}
			} else {
				for _, item := range ExtractIterableItems(ctx, other) {
					if item.IsTuple() && len(item.AsTuple().Items) >= 2 {
						d.Set(item.AsTuple().Items[0], item.AsTuple().Items[1])
					}
				}
			}
		}
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				d.Set(kn, args[kwStart+i])
			}
		}
		return NoneVal()
	}))

	dictInitBuiltin := NewBuiltinFuncVal("dict.__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NoneVal()
		}
		if updateFn, ok := DictType.Dict.Get(InternString("update")); ok {
			return updateFn.AsBuiltinFunc().Fn(ctx, updateFn, args, kwnames)
		}
		return NoneVal()
	})
	DictType.Dict.Set(DunderInit, dictInitBuiltin)
	DictType.dunderInit = dictInitBuiltin

	DictType.Dict.Set(DunderContains, NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__contains__' of 'dict' object needs 2 arguments")
		}
		d := args[0].AsDict()
		_, ok := d.Get(args[1])
		return BoolVal(ok)
	}))

	DictType.Dict.Set(DunderLen, NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__len__' of 'dict' object needs an argument")
		}
		return Int64Val(int64(args[0].AsDict().Len()))
	}))

	DictType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'dict' object needs an argument")
		}
		return NewIterVal(args[0])
	}))
	DictType.Dict.Set(InternString("__hash__"), NoneVal())
	DictType.Dict.Set(DunderEq, NewBuiltinFuncVal("dict.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsDict() || !args[1].IsDict() {
			return NotImplementedVal()
		}
		return BoolVal(equalsStructural(args[0], args[1]))
	}))
	DictType.Dict.Set(DunderNe, NewBuiltinFuncVal("dict.__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsDict() || !args[1].IsDict() {
			return NotImplementedVal()
		}
		return BoolVal(!equalsStructural(args[0], args[1]))
	}))

	TupleType = MustNewType("tuple", []*PyType{ObjectType}, nil)
	TupleType.Slots.NbAdd = tupleNbAdd
	TupleType.Slots.NbMul = tupleNbMul
	TupleType.Slots.SqLen = tupleSqLen
	TupleType.Slots.SqItem = tupleSqItem
	TupleType.Slots.MpSubscript = tupleMpSubscript
	TupleType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return EmptyTupleVal()
		}
		if args[0].Kind() == KindTuple && args[0].Type() == TupleType {
			return args[0]
		}
		items := ExtractIterableItems(ctx, args[0])
		return NewTupleVal(items...)
	}
	tupleNewBuiltin := NewBuiltinFuncVal("tuple.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		cls := TupleType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		var items []Value
		if len(args) > 1 {
			items = ExtractIterableItems(ctx, args[1])
		}
		if cls == TupleType {
			if len(items) == 0 {
				return EmptyTupleVal()
			}
			return NewTupleVal(items...)
		}
		obj := &PyTuple{
			Header: ObjectHeader{
				Kind: KindTuple,
				Type: cls,
			},
			Items: items,
		}
		return Value{num: 0, ptr: &obj.Header}
	})
	TupleType.Dict.Set(DunderNew, tupleNewBuiltin)
	TupleType.dunderNew = tupleNewBuiltin
	TupleType.Dict.Set(DunderGetItem, NewBuiltinFuncVal("__getitem__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__getitem__' of 'tuple' object needs an argument")
		}
		return tupleMpSubscript(ctx, args[0], args[1])
	}))
	TupleType.Dict.Set(DunderLen, NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__len__' of 'tuple' object needs an argument")
		}
		return Int64Val(int64(len(args[0].AsTuple().Items)))
	}))
	TupleType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'tuple' object needs an argument")
		}
		return NewIterVal(args[0])
	}))
	TupleType.Dict.Set(DunderContains, NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return BoolVal(false)
		}
		for _, it := range args[0].AsTuple().Items {
			if Equals(it, args[1]) {
				return BoolVal(true)
			}
		}
		return BoolVal(false)
	}))
	TupleType.Dict.Set(DunderEq, NewBuiltinFuncVal("tuple.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsTuple() || !args[1].IsTuple() {
			return NotImplementedVal()
		}
		return compareSliceRich(ctx, args[0].AsTuple().Items, args[1].AsTuple().Items, CmpEQ)
	}))
	TupleType.Dict.Set(DunderNe, NewBuiltinFuncVal("tuple.__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsTuple() || !args[1].IsTuple() {
			return NotImplementedVal()
		}
		return compareSliceRich(ctx, args[0].AsTuple().Items, args[1].AsTuple().Items, CmpNEQ)
	}))
	TupleType.Dict.Set(InternString("__hash__"), NewBuiltinFuncVal("tuple.__hash__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsTuple() {
			return Int64Val(0)
		}
		items := args[0].AsTuple().Items
		var acc uint64 = 0x345678
		for _, it := range items {
			acc = (acc ^ uint64(HashValue(it))) * 1000003
		}
		acc ^= uint64(len(items))
		return Int64Val(int64(mix64To32(acc)))
	}))
	TupleType.Dict.Set(InternString("index"), NewBuiltinFuncVal("index", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: index expected at least 1 argument, got 0")
		}
		items := args[0].AsTuple().Items
		target := args[1]
		start := 0
		stop := len(items)
		if len(args) > 2 && args[2].IsInt64() {
			start = int(args[2].AsInt64())
			if start < 0 {
				start += len(items)
				if start < 0 {
					start = 0
				}
			}
		}
		if len(args) > 3 && args[3].IsInt64() {
			stop = int(args[3].AsInt64())
			if stop < 0 {
				stop += len(items)
			}
			if stop > len(items) {
				stop = len(items)
			}
		}
		for i := start; i < stop && i < len(items); i++ {
			if Equals(items[i], target) {
				return Int64Val(int64(i))
			}
		}
		panic(NewException(ValueErrorType, "tuple.index(x): x not in tuple"))
	}))
	TupleType.Dict.Set(InternString("count"), NewBuiltinFuncVal("count", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: count() takes exactly one argument")
		}
		var n int64
		for _, it := range args[0].AsTuple().Items {
			if Equals(it, args[1]) {
				n++
			}
		}
		return Int64Val(n)
	}))
	tupleAddBuiltin := NewBuiltinFuncVal("tuple.__add__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: tuple.__add__ takes 2 arguments")
		}
		return tupleNbAdd(ctx, args[0], args[1])
	})
	TupleType.Dict.Set(DunderAdd, tupleAddBuiltin)
	TupleType.dunderAdd = tupleAddBuiltin
	tupleMulBuiltin := NewBuiltinFuncVal("tuple.__mul__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: tuple.__mul__ takes 2 arguments")
		}
		return tupleNbMul(ctx, args[0], args[1])
	})
	TupleType.Dict.Set(DunderMul, tupleMulBuiltin)
	TupleType.dunderMul = tupleMulBuiltin
	TupleType.Dict.Set(DunderRMul, tupleMulBuiltin)
	TupleType.dunderRMul = tupleMulBuiltin
	TupleType.syncDunderSlots()

	CodeType = MustNewType("code", []*PyType{ObjectType}, nil)
	CodeType.Slots.TpCall = functionTpCall
	CodeType.Slots.TpGetAttr = codeTpGetAttr

	FrameType = MustNewType("frame", []*PyType{ObjectType}, nil)
	FrameType.Slots.TpGetAttr = frameTpGetAttr

	TracebackType = MustNewType("traceback", []*PyType{ObjectType}, nil)
	TracebackType.Slots.TpGetAttr = tracebackTpGetAttr

	LocalsProxyType = MustNewType("FrameLocalsProxy", []*PyType{ObjectType}, nil)
	LocalsProxyType.Slots.MpSubscript = localsProxyMpSubscript
	LocalsProxyType.Slots.MpAssSubscript = localsProxyMpAssSubscript
	LocalsProxyType.Slots.SqLen = func(ctx *ExecContext, o Value) int64 {
		return int64(len(localsProxyCollectPairs(o.AsLocalsProxy())))
	}
	LocalsProxyType.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
		entries := localsProxyCollectPairs(o.AsLocalsProxy())
		keys := make([]Value, len(entries))
		for i, e := range entries {
			keys[i] = e.Key
		}
		return NewIterVal(NewListVal(keys...))
	}
	LocalsProxyType.Dict.Set(DunderContains, NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__contains__' of 'FrameLocalsProxy' object needs 2 arguments")
		}
		return BoolVal(localsProxyContains(ctx, args[0], args[1]))
	}))
	LocalsProxyType.Dict.Set(InternString("__str__"), NewBuiltinFuncVal("__str__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__str__' of 'FrameLocalsProxy' object needs an argument")
		}
		return NewStringVal(localsProxyRepr(args[0]))
	}))
	LocalsProxyType.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal("__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' of 'FrameLocalsProxy' object needs an argument")
		}
		return NewStringVal(localsProxyRepr(args[0]))
	}))
	LocalsProxyType.Dict.Set(InternString("keys"), NewBuiltinFuncVal("keys", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: keys() takes at least 1 argument")
		}
		entries := localsProxyCollectPairs(args[0].AsLocalsProxy())
		keys := make([]Value, len(entries))
		for i, e := range entries {
			keys[i] = e.Key
		}
		return NewListVal(keys...)
	}))
	LocalsProxyType.Dict.Set(InternString("values"), NewBuiltinFuncVal("values", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: values() takes at least 1 argument")
		}
		entries := localsProxyCollectPairs(args[0].AsLocalsProxy())
		vals := make([]Value, len(entries))
		for i, e := range entries {
			vals[i] = e.Value
		}
		return NewListVal(vals...)
	}))
	LocalsProxyType.Dict.Set(InternString("items"), NewBuiltinFuncVal("items", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: items() takes at least 1 argument")
		}
		entries := localsProxyCollectPairs(args[0].AsLocalsProxy())
		items := make([]Value, len(entries))
		for i, e := range entries {
			items[i] = NewTupleVal(e.Key, e.Value)
		}
		return NewListVal(items...)
	}))

	TypeType.Dict.Set(DunderCall, NewBuiltinFuncVal("type.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor '__call__' of 'type' object needs an argument")
		}
		cls := args[0]
		return typeTpCall(ctx, cls, args[1:], kwnames)
	}))
	LocalsProxyType.Dict.Set(InternString("get"), NewBuiltinFuncVal("get", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: get() takes at least 1 argument")
		}
		defVal := NoneVal()
		if len(args) >= 3 {
			defVal = args[2]
		}
		key := args[1]
		if localsProxyContains(ctx, args[0], key) {
			return localsProxyMpSubscript(ctx, args[0], key)
		}
		return defVal
	}))

	WeakRefType = MustNewType("ReferenceType", []*PyType{ObjectType}, nil)
	WeakRefType.Slots.TpCall = weakRefTpCall
	WeakRefType.Slots.TpGetAttr = weakRefTpGetAttr
	WeakRefType.Dict.Set(DunderCall, NewBuiltinFuncVal("ReferenceType.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__call__' of 'ReferenceType' object needs an argument")
		}
		return weakRefTpCall(ctx, args[0], args[1:], kwnames)
	}))
	WeakRefType.Dict.Set(DunderEq, NewBuiltinFuncVal("__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return BoolVal(false)
		}
		return BoolVal(weakRefEqual(args[0], args[1]))
	}))
	WeakRefType.Dict.Set(InternString("__hash__"), NewBuiltinFuncVal("__hash__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__hash__' of 'ReferenceType' object needs an argument")
		}
		return Int64Val(weakRefTpHash(args[0]))
	}))
	WeakRefType.SyncDunderSlots()

	WeakProxyType = MustNewType("ProxyType", []*PyType{ObjectType}, nil)
	WeakProxyType.Slots.TpGetAttr = weakProxyTpGetAttr
	WeakProxyType.Slots.TpSetAttr = weakProxyTpSetAttr

	CallableWeakProxyType = MustNewType("CallableProxyType", []*PyType{WeakProxyType}, nil)
	CallableWeakProxyType.Slots.TpGetAttr = weakProxyTpGetAttr
	CallableWeakProxyType.Slots.TpSetAttr = weakProxyTpSetAttr
	CallableWeakProxyType.Slots.TpCall = weakProxyTpCall
	CallableWeakProxyType.Dict.Set(DunderCall, NewBuiltinFuncVal("CallableProxyType.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__call__' of 'CallableProxyType' object needs an argument")
		}
		return weakProxyTpCall(ctx, args[0], args[1:], kwnames)
	}))
	CallableWeakProxyType.SyncDunderSlots()

	FunctionType = MustNewType("function", []*PyType{ObjectType}, nil)
	FunctionType.Slots.TpDescrGet = functionDescrGet
	FunctionType.Slots.TpCall = functionTpCall
	FunctionType.Slots.TpGetAttr = functionTpGetAttr
	FunctionType.Slots.TpSetAttr = functionTpSetAttr

	funcGetBuiltin := NewBuiltinFuncVal("function.__get__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__get__' of 'function' object needs an argument")
		}
		fn := args[0]
		obj := NoneVal()
		if len(args) > 1 {
			obj = args[1]
		}
		var owner *PyType
		if len(args) > 2 && args[2].IsType() {
			owner = args[2].AsType()
		}
		return functionDescrGet(ctx, fn, obj, owner)
	})
	FunctionType.Dict.Set(DunderGet, funcGetBuiltin)
	FunctionType.dunderGet = funcGetBuiltin

	BuiltinFuncType = MustNewType("builtin_function_or_method", []*PyType{ObjectType}, nil)
	BuiltinFuncType.Slots.TpDescrGet = builtinFuncDescrGet
	BuiltinFuncType.Slots.TpCall = builtinFuncTpCall

	builtinFuncGetBuiltin := NewBuiltinFuncVal("builtin_function_or_method.__get__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__get__' of 'builtin_function_or_method' object needs an argument")
		}
		fn := args[0]
		obj := NoneVal()
		if len(args) > 1 {
			obj = args[1]
		}
		var owner *PyType
		if len(args) > 2 && args[2].IsType() {
			owner = args[2].AsType()
		}
		return builtinFuncDescrGet(ctx, fn, obj, owner)
	})
	BuiltinFuncType.Dict.Set(DunderGet, builtinFuncGetBuiltin)
	BuiltinFuncType.dunderGet = builtinFuncGetBuiltin
	BuiltinFuncType.Slots.TpGetAttr = func(ctx *ExecContext, obj, name Value) Value {
		bf := obj.AsBuiltinFunc()
		switch name.AsString() {
		case "__name__":
			return InternString(bf.Name)
		case "__qualname__":
			return InternString(bf.Name)
		case "__module__":
			return InternString("builtins")
		case "__doc__":
			return NoneVal()
		}
		return ObjectType.Slots.TpGetAttr(ctx, obj, name)
	}

	BoundMethodType = MustNewType("method", []*PyType{ObjectType}, nil)
	BoundMethodType.Slots.TpCall = boundMethodTpCall
	BoundMethodType.Slots.TpGetAttr = boundMethodTpGetAttr

	funcCall := NewBuiltinFuncVal("function.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__call__' requires at least 1 argument")
		}
		target := args[0]
		return ctx.Call(target, args[1:], kwnames)
	})
	FunctionType.Dict.Set(InternString("__call__"), funcCall)
	BuiltinFuncType.Dict.Set(InternString("__call__"), funcCall)
	BoundMethodType.Dict.Set(InternString("__call__"), funcCall)

	PropertyType = MustNewType("property", []*PyType{ObjectType}, nil)
	PropertyType.Slots.TpDescrGet = propertyDescrGet
	PropertyType.Slots.TpDescrSet = propertyDescrSet
	PropertyType.Slots.TpGetAttr = func(ctx *ExecContext, obj, name Value) Value {
		p := obj.AsProperty()
		switch name.AsString() {
		case "fget":
			if p.FGet.ptr != nil || p.FGet.num != 0 {
				return p.FGet
			}
			return NoneVal()
		case "fset":
			if p.FSet.ptr != nil || p.FSet.num != 0 {
				return p.FSet
			}
			return NoneVal()
		case "fdel":
			if p.FDel.ptr != nil || p.FDel.num != 0 {
				return p.FDel
			}
			return NoneVal()
		case "__doc__":
			if p.Doc.ptr != nil || p.Doc.num != 0 {
				return p.Doc
			}
			return NoneVal()
		}
		return ObjectType.Slots.TpGetAttr(ctx, obj, name)
	}
	PropertyType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		var fget, fset, fdel, doc Value
		fget = NoneVal()
		fset = NoneVal()
		fdel = NoneVal()
		doc = NoneVal()
		if len(args) > 0 {
			fget = args[0]
		}
		if len(args) > 1 {
			fset = args[1]
		}
		if len(args) > 2 {
			fdel = args[2]
		}
		if len(args) > 3 {
			doc = args[3]
		}
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				val := args[kwStart+i]
				switch kn.AsString() {
				case "fget":
					fget = val
				case "fset":
					fset = val
				case "fdel":
					fdel = val
				case "doc":
					doc = val
				}
			}
		}
		p := NewPropertyVal(fget, fset, fdel)
		p.AsProperty().Doc = doc
		return p
	}
	PropertyType.Dict.Set(InternString("getter"), NewBuiltinFuncVal("getter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: getter() takes exactly one argument")
		}
		self := args[0].AsProperty()
		p := NewPropertyVal(args[1], self.FSet, self.FDel)
		p.AsProperty().Doc = self.Doc
		return p
	}))
	PropertyType.Dict.Set(InternString("setter"), NewBuiltinFuncVal("setter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: setter() takes exactly one argument")
		}
		self := args[0].AsProperty()
		p := NewPropertyVal(self.FGet, args[1], self.FDel)
		p.AsProperty().Doc = self.Doc
		return p
	}))
	PropertyType.Dict.Set(InternString("deleter"), NewBuiltinFuncVal("deleter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: deleter() takes exactly one argument")
		}
		self := args[0].AsProperty()
		p := NewPropertyVal(self.FGet, self.FSet, args[1])
		p.AsProperty().Doc = self.Doc
		return p
	}))
	PropertyType.Dict.Set(DunderGet, NewBuiltinFuncVal("property.__get__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__get__' of 'property' object needs an argument")
		}
		prop := args[0]
		obj := args[1]
		var owner *PyType
		if len(args) > 2 && args[2].IsType() {
			owner = args[2].AsType()
		}
		return propertyDescrGet(ctx, prop, obj, owner)
	}))
	PropertyType.Dict.Set(DunderSet, NewBuiltinFuncVal("property.__set__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: descriptor '__set__' of 'property' object needs 2 arguments")
		}
		propertyDescrSet(ctx, args[0], args[1], args[2])
		return NoneVal()
	}))

	typeAnnotationsGetter := NewBuiltinFuncVal("type.__annotations__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__annotations__' of 'type' object needs an argument")
		}
		if !args[0].IsType() {
			panic("TypeError: descriptor '__annotations__' requires a 'type' object")
		}
		return TypeAnnotations(ctx, args[0].AsType())
	})
	typeAnnotationsSetter := NewBuiltinFuncVal("type.__annotations__.setter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: descriptor '__annotations__' setter needs 2 arguments")
		}
		if !args[0].IsType() {
			panic("TypeError: descriptor '__annotations__' requires a 'type' object")
		}
		TypeSetAnnotations(args[0].AsType(), args[1])
		return NoneVal()
	})
	typeAnnotationsDeleter := NewBuiltinFuncVal("type.__annotations__.deleter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__annotations__' deleter needs an argument")
		}
		if !args[0].IsType() {
			panic("TypeError: descriptor '__annotations__' requires a 'type' object")
		}
		TypeDelAnnotations(args[0].AsType())
		return NoneVal()
	})
	typeAnnotationsProp := NewPropertyVal(typeAnnotationsGetter, typeAnnotationsSetter, typeAnnotationsDeleter)
	TypeType.Dict.Set(InternString("__annotations__"), typeAnnotationsProp)

	typeAnnotateGetter := NewBuiltinFuncVal("type.__annotate__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor '__annotate__' requires a 'type' object")
		}
		return TypeAnnotate(args[0].AsType())
	})
	typeAnnotateSetter := NewBuiltinFuncVal("type.__annotate__.setter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsType() {
			panic("TypeError: descriptor '__annotate__' requires a 'type' object")
		}
		TypeSetAnnotate(args[0].AsType(), args[1])
		return NoneVal()
	})
	typeAnnotateDeleter := NewBuiltinFuncVal("type.__annotate__.deleter", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		panic("TypeError: cannot delete __annotate__ attribute")
	})
	TypeType.Dict.Set(InternString("__annotate__"), NewPropertyVal(typeAnnotateGetter, typeAnnotateSetter, typeAnnotateDeleter))

	codeAttrs := []string{
		"co_argcount", "co_posonlyargcount", "co_kwonlyargcount", "co_nlocals",
		"co_stacksize", "co_flags", "co_code", "co_consts", "co_names",
		"co_varnames", "co_filename", "co_name", "co_qualname", "co_firstlineno",
		"co_linetable", "co_lnotab", "co_freevars", "co_cellvars", "co_lines", "co_positions",
	}
	for _, attr := range codeAttrs {
		attrName := attr
		fget := NewBuiltinFuncVal(attrName, func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '" + attrName + "' of 'code' object needs an argument")
			}
			return codeTpGetAttr(ctx, args[0], InternString(attrName))
		})
		CodeType.Dict.Set(InternString(attrName), NewPropertyVal(fget, Value{}, Value{}))
	}

	MappingProxyType = MustNewType("mappingproxy", []*PyType{ObjectType}, nil)
	MappingProxyType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			panic("TypeError: mappingproxy() takes at least 1 argument")
		}
		return NewMappingProxyVal(args[0])
	}
	MappingProxyType.Slots.MpSubscript = func(ctx *ExecContext, o, key Value) Value {
		mp := o.AsMappingProxy()
		return GetItemCtx(ctx, mp.Mapping, key)
	}
	MappingProxyType.Slots.SqLen = func(ctx *ExecContext, o Value) int64 {
		mp := o.AsMappingProxy()
		if mp.Mapping.IsDict() {
			return int64(mp.Mapping.AsDict().Len())
		}
		return 0
	}
	MappingProxyType.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
		mp := o.AsMappingProxy()
		return NewIterVal(mp.Mapping)
	}
	MappingProxyType.Dict.Set(InternString("get"), NewBuiltinFuncVal("get", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: get() takes at least 1 argument")
		}
		mp := args[0].AsMappingProxy()
		defVal := NoneVal()
		if len(args) > 2 {
			defVal = args[2]
		}
		if mp.Mapping.IsDict() {
			if v, ok := mp.Mapping.AsDict().Get(args[1]); ok {
				return v
			}
		}
		return defVal
	}))
	MappingProxyType.Dict.Set(InternString("keys"), NewBuiltinFuncVal("keys", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		mp := args[0].AsMappingProxy()
		if mp.Mapping.IsDict() {
			return NewListVal(mp.Mapping.AsDict().Keys()...)
		}
		return NewListVal()
	}))
	MappingProxyType.Dict.Set(InternString("values"), NewBuiltinFuncVal("values", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		mp := args[0].AsMappingProxy()
		if mp.Mapping.IsDict() {
			return NewListVal(mp.Mapping.AsDict().Values()...)
		}
		return NewListVal()
	}))
	MappingProxyType.Dict.Set(InternString("items"), NewBuiltinFuncVal("items", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		mp := args[0].AsMappingProxy()
		if mp.Mapping.IsDict() {
			items := mp.Mapping.AsDict().Items()
			list := make([]Value, len(items))
			for i, it := range items {
				list[i] = NewTupleVal(it.Key, it.Value)
			}
			return NewListVal(list...)
		}
		return NewListVal()
	}))
	MappingProxyType.Dict.Set(InternString("copy"), NewBuiltinFuncVal("copy", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		mp := args[0].AsMappingProxy()
		if mp.Mapping.IsDict() {
			d := mp.Mapping.AsDict()
			newD := NewDictVal(d.Len()).AsDict()
			for _, it := range d.Items() {
				newD.Set(it.Key, it.Value)
			}
			return DictVal(newD)
		}
		return mp.Mapping
	}))
	MappingProxyType.Dict.Set(DunderContains, NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return BoolVal(false)
		}
		mp := args[0].AsMappingProxy()
		if mp.Mapping.IsDict() {
			_, ok := mp.Mapping.AsDict().Get(args[1])
			return BoolVal(ok)
		}
		return BoolVal(false)
	}))

	FrozenDictType = MustNewType("frozendict", []*PyType{ObjectType}, nil)
	FrozenDictType.Slots.MpSubscript = MappingProxyType.Slots.MpSubscript
	FrozenDictType.Slots.SqLen = MappingProxyType.Slots.SqLen
	FrozenDictType.Slots.TpIter = MappingProxyType.Slots.TpIter
	FrozenDictType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		targetDict := NewDictVal(8).AsDict()
		posArgs := args
		if len(kwnames) > 0 {
			posArgs = args[:len(args)-len(kwnames)]
		}
		if len(posArgs) > 0 {
			arg := posArgs[0]
			switch {
			case arg.IsDict():
				d := arg.AsDict()
				for _, it := range d.Items() {
					targetDict.Set(it.Key, it.Value)
				}
			case arg.IsMappingProxy():
				mp := arg.AsMappingProxy()
				if mp.Mapping.IsDict() {
					for _, it := range mp.Mapping.AsDict().Items() {
						targetDict.Set(it.Key, it.Value)
					}
				}
			default:
				items := ExtractIterableItems(ctx, arg)
				for _, it := range items {
					if it.IsTuple() && len(it.AsTuple().Items) >= 2 {
						targetDict.Set(it.AsTuple().Items[0], it.AsTuple().Items[1])
					} else if it.IsList() && len(it.AsList().Items) >= 2 {
						targetDict.Set(it.AsList().Items[0], it.AsList().Items[1])
					}
				}
			}
		}
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, name := range kwnames {
				targetDict.Set(name, args[kwStart+i])
			}
		}
		mp := &PyMappingProxy{
			Header: ObjectHeader{
				Kind: KindMappingProxy,
				Type: FrozenDictType,
			},
			Mapping: DictVal(targetDict),
		}
		return Value{ptr: &mp.Header}
	}
	for _, it := range MappingProxyType.Dict.Items() {
		FrozenDictType.Dict.Set(it.Key, it.Value)
	}
	FrozenDictType.Dict.Set(InternString("copy"), NewBuiltinFuncVal("copy", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'copy' of 'frozendict' object needs an argument")
		}
		mp := args[0].AsMappingProxy()
		if mp.Mapping.IsDict() {
			d := mp.Mapping.AsDict()
			newD := NewDictVal(d.Len()).AsDict()
			for _, it := range d.Items() {
				newD.Set(it.Key, it.Value)
			}
			return NewFrozenDictVal(DictVal(newD))
		}
		return args[0]
	}))

	EllipsisType = MustNewType("ellipsis", []*PyType{ObjectType}, nil)

	SentinelType = MustNewType("sentinel", []*PyType{ObjectType}, nil)
	SentinelType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		posCount := len(args) - len(kwnames)
		var nameVal Value
		reprVal := NoneVal()
		if posCount > 0 {
			nameVal = args[0]
		}
		if posCount > 1 {
			reprVal = args[1]
		}
		for i, kn := range kwnames {
			kv := args[posCount+i]
			switch kn.AsString() {
			case "name":
				nameVal = kv
			case "repr":
				reprVal = kv
			}
		}
		if !nameVal.IsAnyString() {
			panic("TypeError: sentinel() requires a string 'name' argument")
		}
		inst := NewInstanceOfType(SentinelType)
		inst.AsInstance().SetAttr(InternString("name"), nameVal)
		if reprVal.IsNone() {
			reprVal = NewStringVal("<" + nameVal.AsString() + ">")
		}
		inst.AsInstance().SetAttr(InternString("_repr"), reprVal)
		return inst
	}
	SentinelType.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal("sentinel.__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 && args[0].IsInstance() {
			if r, ok := args[0].AsInstance().GetAttr(InternString("_repr")); ok {
				return r
			}
		}
		return InternString("<sentinel>")
	}))
	tagEllipsis.Type = EllipsisType

	SuperType = MustNewType("super", []*PyType{ObjectType}, nil)
	SuperType.Slots.TpGetAttr = superGetAttr

	MemberDescrType = MustNewType("member_descriptor", []*PyType{ObjectType}, nil)
	MemberDescrType.Slots.TpDescrGet = memberDescrTpDescrGet
	MemberDescrType.Slots.TpDescrSet = memberDescrTpDescrSet

	ClassMethodType = MustNewType("classmethod", []*PyType{ObjectType}, nil)
	ClassMethodType.Slots.TpDescrGet = classMethodDescrGet
	ClassMethodType.Slots.TpCall = classMethodTpCall
	ClassMethodType.Dict.Set(DunderGet, NewBuiltinFuncVal("classmethod.__get__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__get__' of 'classmethod' object needs an argument")
		}
		cm := args[0]
		obj := NoneVal()
		if len(args) > 1 {
			obj = args[1]
		}
		var owner *PyType
		if len(args) > 2 && args[2].IsType() {
			owner = args[2].AsType()
		}
		return classMethodDescrGet(ctx, cm, obj, owner)
	}))

	StaticMethodType = MustNewType("staticmethod", []*PyType{ObjectType}, nil)
	StaticMethodType.Slots.TpDescrGet = staticMethodDescrGet
	StaticMethodType.Slots.TpCall = staticMethodTpCall
	StaticMethodType.Dict.Set(DunderGet, NewBuiltinFuncVal("staticmethod.__get__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__get__' of 'staticmethod' object needs an argument")
		}
		sm := args[0]
		obj := NoneVal()
		if len(args) > 1 {
			obj = args[1]
		}
		var owner *PyType
		if len(args) > 2 && args[2].IsType() {
			owner = args[2].AsType()
		}
		return staticMethodDescrGet(ctx, sm, obj, owner)
	}))

	NotImplementedType = MustNewType("NotImplementedType", []*PyType{ObjectType}, nil)

	CellType = MustNewType("cell", []*PyType{ObjectType}, nil)
	CellType.Slots.TpGetAttr = func(ctx *ExecContext, obj, name Value) Value {
		if name.AsString() == "cell_contents" {
			v := obj.AsCell().Value
			if v.IsUnbound() {
				panic(NewException(ValueErrorType, "Cell is empty"))
			}
			return v
		}
		return objectGetAttr(ctx, obj, name)
	}
	CellType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if !callable.IsType() || callable.AsType() != CellType {
			panic("TypeError: 'cell' object is not callable")
		}
		if len(kwnames) > 0 {
			panic("TypeError: cell() takes no keyword arguments")
		}
		switch len(args) {
		case 0:
			return NewCellVal(UnboundVal())
		case 1:
			return NewCellVal(args[0])
		}
		panic("TypeError: cell expected at most 1 argument, got " + strconv.Itoa(len(args)))
	}
	CellType.Slots.TpSetAttr = func(ctx *ExecContext, obj, name, value Value) Value {
		if name.AsString() == "cell_contents" {
			obj.AsCell().Value = value
			return NoneVal()
		}
		return objectSetAttr(ctx, obj, name, value)
	}
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
	IterType.Dict.Set(InternString("__next__"), NewBuiltinFuncVal("iterator.__next__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__next__' of 'iterator' object needs an argument")
		}
		v, status := IterType.Slots.TpIterNext(ctx, args[0])
		if status == IterExhausted {
			panic(NewException(StopIterationType, ""))
		}
		return v
	}))
	IterType.Dict.Set(InternString("__iter__"), NewBuiltinFuncVal("iterator.__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'iterator' object needs an argument")
		}
		return args[0]
	}))

	RangeType = MustNewType("range", []*PyType{ObjectType}, nil)
	RangeType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		var start, stop, step int64
		step = 1
		switch {
		case len(args) == 1:
			stop = args[0].AsInt64()
		case len(args) == 2:
			start = args[0].AsInt64()
			stop = args[1].AsInt64()
		case len(args) >= 3:
			start = args[0].AsInt64()
			stop = args[1].AsInt64()
			step = args[2].AsInt64()
		}
		if step == 1 && start >= -0x80000000 && start <= 0x7FFFFFFF && stop >= -0x80000000 && stop <= 0x7FFFFFFF {
			return RangeVal(int32(start), int32(stop))
		}
		return NewRangeIterVal(start, stop, step)
	}
	RangeType.Slots.SqLen = func(ctx *ExecContext, o Value) int64 {
		if o.ptr == &tagRange {
			start := o.RangeStart()
			stop := o.RangeStop()
			if stop > start {
				return int64(stop - start)
			}
			return 0
		}
		return 0
	}
	RangeType.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
		if o.ptr == &tagRange {
			start := int64(o.RangeStart())
			stop := int64(o.RangeStop())
			return NewRangeIterVal(start, stop, 1)
		}
		return o
	}
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
	RangeType.Dict.Set(InternString("__len__"), NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return Int64Val(0)
		}
		return Int64Val(RangeType.Slots.SqLen(ctx, args[0]))
	}))
	RangeType.Dict.Set(InternString("__iter__"), NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NoneVal()
		}
		return RangeType.Slots.TpIter(ctx, args[0])
	}))

	GeneratorType = MustNewType("generator", []*PyType{ObjectType}, nil)
	GeneratorType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return o }
	GeneratorType.Slots.TpIterNext = func(ctx *ExecContext, o Value) (Value, IterStatus) {
		gen := o.AsGenerator()
		return gen.Send(ctx, NoneVal())
	}
	GeneratorType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'generator' object needs an argument")
		}
		return args[0]
	}))
	GeneratorType.Dict.Set(DunderNext, NewBuiltinFuncVal("__next__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__next__' of 'generator' object needs an argument")
		}
		gen := args[0].AsGenerator()
		v, status := gen.Send(ctx, NoneVal())
		if status == IterExhausted {
			panic(NewStopIteration(v))
		}
		if status == IterError {
			if ctx != nil && ctx.HasException() {
				panic(ctx.ActiveException)
			}
		}
		return v
	}))
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
		exc := args[1]
		if exc.IsType() {
			excType := exc.AsType()
			if len(args) >= 3 && !args[2].IsNone() {
				if IsInstance(args[2], excType) {
					exc = args[2]
				} else {
					exc = ctx.Invoke1(exc, args[2])
				}
			} else {
				exc = NewInstanceOfType(excType)
			}
		}
		if len(args) >= 4 && !args[3].IsNone() && exc.IsInstance() {
			exc.AsInstance().SetAttr(InternString("__traceback__"), args[3])
		}
		v, status := gen.Throw(ctx, exc)
		if status == IterExhausted {
			ctx.Raise(NewStopIteration(v))
		}
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
		v, _ := coro.Send(ctx, args[1])
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
	BaseExceptionType.Dict.Set(InternString("__traceback__"), NoneVal())
	BaseExceptionType.Dict.Set(InternString("__cause__"), NoneVal())
	BaseExceptionType.Dict.Set(InternString("__context__"), NoneVal())
	BaseExceptionType.Dict.Set(InternString("__suppress_context__"), BoolVal(false))
	BaseExceptionType.Dict.Set(InternString("args"), NewTupleVal())
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
				self.AsInstance().SetAttr(InternString("__traceback__"), NoneVal())
				self.AsInstance().SetAttr(InternString("__cause__"), NoneVal())
				self.AsInstance().SetAttr(InternString("__context__"), NoneVal())
				self.AsInstance().SetAttr(InternString("__suppress_context__"), BoolVal(false))
			}
		}
		return NoneVal()
	}))

	BaseExceptionType.Dict.Set(InternString("__str__"), NewBuiltinFuncVal("__str__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NewStringVal("")
		}
		self := args[0]
		if self.IsInstance() {
			if argsVal, ok := self.AsInstance().GetAttr(InternString("args")); ok && argsVal.IsTuple() {
				items := argsVal.AsTuple().Items
				if len(items) == 1 {
					return StrType.Slots.TpCall(ctx, TypeVal(StrType), []Value{items[0]}, nil)
				}
				if len(items) > 1 {
					return StrType.Slots.TpCall(ctx, TypeVal(StrType), []Value{argsVal}, nil)
				}
			}
		}
		return NewStringVal("")
	}))
	BaseExceptionType.Dict.Set(InternString("with_traceback"), NewBuiltinFuncVal("with_traceback", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: with_traceback() takes exactly one argument")
		}
		self := args[0]
		tb := args[1]
		if self.IsInstance() {
			self.AsInstance().SetAttr(InternString("__traceback__"), tb)
		}
		return self
	}))
	BaseExceptionType.InvalidateVersion()
	ExceptionType = MustNewType("Exception", []*PyType{BaseExceptionType}, nil)
	TypeErrorType = MustNewType("TypeError", []*PyType{ExceptionType}, nil)
	ValueErrorType = MustNewType("ValueError", []*PyType{ExceptionType}, nil)
	UnicodeErrorType = MustNewType("UnicodeError", []*PyType{ValueErrorType}, nil)
	UnicodeDecodeErrorType = MustNewType("UnicodeDecodeError", []*PyType{UnicodeErrorType}, nil)
	UnicodeEncodeErrorType = MustNewType("UnicodeEncodeError", []*PyType{UnicodeErrorType}, nil)
	UnicodeTranslateErrorType = MustNewType("UnicodeTranslateError", []*PyType{UnicodeErrorType}, nil)

	LookupErrorType = MustNewType("LookupError", []*PyType{ExceptionType}, nil)
	IndexErrorType = MustNewType("IndexError", []*PyType{LookupErrorType}, nil)
	KeyErrorType = MustNewType("KeyError", []*PyType{LookupErrorType}, nil)

	ArithmeticErrorType = MustNewType("ArithmeticError", []*PyType{ExceptionType}, nil)
	ZeroDivisionErrorType = MustNewType("ZeroDivisionError", []*PyType{ArithmeticErrorType}, nil)
	OverflowErrorType = MustNewType("OverflowError", []*PyType{ArithmeticErrorType}, nil)
	FloatingPointErrorType = MustNewType("FloatingPointError", []*PyType{ArithmeticErrorType}, nil)

	NameErrorType = MustNewType("NameError", []*PyType{ExceptionType}, nil)
	UnboundLocalErrorType = MustNewType("UnboundLocalError", []*PyType{NameErrorType}, nil)

	AttributeErrorType = MustNewType("AttributeError", []*PyType{ExceptionType}, nil)

	RuntimeErrorType = MustNewType("RuntimeError", []*PyType{ExceptionType}, nil)
	NotImplementedErrorType = MustNewType("NotImplementedError", []*PyType{RuntimeErrorType}, nil)
	RecursionErrorType = MustNewType("RecursionError", []*PyType{RuntimeErrorType}, nil)

	SyntaxErrorType = MustNewType("SyntaxError", []*PyType{ExceptionType}, nil)
	IncompleteInputErrorType = MustNewType("_IncompleteInputError", []*PyType{SyntaxErrorType}, nil)
	// CPython SyntaxError location attributes default to None on the class
	// so instances always expose them (code.InteractiveConsole reads them).
	for _, attr := range []string{"filename", "lineno", "offset", "text", "end_lineno", "end_offset", "msg", "print_file_and_line"} {
		SyntaxErrorType.Dict.Set(InternString(attr), NoneVal())
	}
	SyntaxErrorType.InvalidateVersion()
	IndentationErrorType = MustNewType("IndentationError", []*PyType{SyntaxErrorType}, nil)
	TabErrorType = MustNewType("TabError", []*PyType{IndentationErrorType}, nil)

	OSErrorType = MustNewType("OSError", []*PyType{ExceptionType}, nil)
	IOErrorType = OSErrorType
	EnvironmentErrorType = OSErrorType
	FileNotFoundErrorType = MustNewType("FileNotFoundError", []*PyType{OSErrorType}, nil)
	PermissionErrorType = MustNewType("PermissionError", []*PyType{OSErrorType}, nil)
	FileExistsErrorType = MustNewType("FileExistsError", []*PyType{OSErrorType}, nil)
	IsADirectoryErrorType = MustNewType("IsADirectoryError", []*PyType{OSErrorType}, nil)
	NotADirectoryErrorType = MustNewType("NotADirectoryError", []*PyType{OSErrorType}, nil)
	InterruptedErrorType = MustNewType("InterruptedError", []*PyType{OSErrorType}, nil)
	ProcessLookupErrorType = MustNewType("ProcessLookupError", []*PyType{OSErrorType}, nil)
	TimeoutErrorType = MustNewType("TimeoutError", []*PyType{OSErrorType}, nil)
	ConnectionErrorType = MustNewType("ConnectionError", []*PyType{OSErrorType}, nil)
	BrokenPipeErrorType = MustNewType("BrokenPipeError", []*PyType{ConnectionErrorType}, nil)
	ConnectionAbortedErrorType = MustNewType("ConnectionAbortedError", []*PyType{ConnectionErrorType}, nil)
	ConnectionRefusedErrorType = MustNewType("ConnectionRefusedError", []*PyType{ConnectionErrorType}, nil)
	ConnectionResetErrorType = MustNewType("ConnectionResetError", []*PyType{ConnectionErrorType}, nil)
	BlockingIOErrorType = MustNewType("BlockingIOError", []*PyType{OSErrorType}, nil)
	ChildProcessErrorType = MustNewType("ChildProcessError", []*PyType{OSErrorType}, nil)

	EOFErrorType = MustNewType("EOFError", []*PyType{ExceptionType}, nil)
	MemoryErrorType = MustNewType("MemoryError", []*PyType{ExceptionType}, nil)
	SystemErrorType = MustNewType("SystemError", []*PyType{ExceptionType}, nil)

	WarningType = MustNewType("Warning", []*PyType{ExceptionType}, nil)
	UserWarningType = MustNewType("UserWarning", []*PyType{WarningType}, nil)
	DeprecationWarningType = MustNewType("DeprecationWarning", []*PyType{WarningType}, nil)
	PendingDeprecationWarningType = MustNewType("PendingDeprecationWarning", []*PyType{WarningType}, nil)
	SyntaxWarningType = MustNewType("SyntaxWarning", []*PyType{WarningType}, nil)
	RuntimeWarningType = MustNewType("RuntimeWarning", []*PyType{WarningType}, nil)
	FutureWarningType = MustNewType("FutureWarning", []*PyType{WarningType}, nil)
	ImportWarningType = MustNewType("ImportWarning", []*PyType{WarningType}, nil)
	UnicodeWarningType = MustNewType("UnicodeWarning", []*PyType{WarningType}, nil)
	BytesWarningType = MustNewType("BytesWarning", []*PyType{WarningType}, nil)
	ResourceWarningType = MustNewType("ResourceWarning", []*PyType{WarningType}, nil)
	EncodingWarningType = MustNewType("EncodingWarning", []*PyType{WarningType}, nil)

	StopIterationType = MustNewType("StopIteration", []*PyType{ExceptionType}, nil)
	StopAsyncIterationType = MustNewType("StopAsyncIteration", []*PyType{ExceptionType}, nil)
	GeneratorExitType = MustNewType("GeneratorExit", []*PyType{BaseExceptionType}, nil)
	SystemExitType = MustNewType("SystemExit", []*PyType{BaseExceptionType}, nil)
	SystemExitType.Dict.Set(DunderInit, NewBuiltinFuncVal("__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 && args[0].IsInstance() {
			inst := args[0].AsInstance()
			rest := append([]Value(nil), args[1:]...)
			inst.SetAttr(InternString("args"), NewTupleVal(rest...))
			code := NoneVal()
			switch len(rest) {
			case 0:
			case 1:
				code = rest[0]
			default:
				code = NewTupleVal(rest...)
			}
			inst.SetAttr(InternString("code"), code)
		}
		return NoneVal()
	}))
	SystemExitType.InvalidateVersion()
	KeyboardInterruptType = MustNewType("KeyboardInterrupt", []*PyType{BaseExceptionType}, nil)
	ImportErrorType = MustNewType("ImportError", []*PyType{ExceptionType}, nil)
	ImportErrorType.Dict.Set(InternString("name"), NoneVal())
	ImportErrorType.Dict.Set(InternString("path"), NoneVal())
	ImportErrorType.Dict.Set(InternString("msg"), NoneVal())
	ImportErrorType.Dict.Set(DunderInit, NewBuiltinFuncVal("__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 && args[0].IsInstance() {
			inst := args[0].AsInstance()
			posEnd := max(1, len(args)-len(kwnames))
			rest := append([]Value(nil), args[1:posEnd]...)
			inst.SetAttr(InternString("args"), NewTupleVal(rest...))
			if len(rest) == 1 {
				inst.SetAttr(InternString("msg"), rest[0])
			} else {
				inst.SetAttr(InternString("msg"), NoneVal())
			}
			nameVal := NoneVal()
			pathVal := NoneVal()
			for i, kw := range kwnames {
				v := args[posEnd+i]
				switch kw.AsString() {
				case "name":
					nameVal = v
				case "path":
					pathVal = v
				}
			}
			inst.SetAttr(InternString("name"), nameVal)
			inst.SetAttr(InternString("path"), pathVal)
		}
		return NoneVal()
	}))
	ImportErrorType.InvalidateVersion()
	ModuleNotFoundErrorType = MustNewType("ModuleNotFoundError", []*PyType{ImportErrorType}, nil)
	BufferErrorType = MustNewType("BufferError", []*PyType{ExceptionType}, nil)
	ReferenceErrorType = MustNewType("ReferenceError", []*PyType{ExceptionType}, nil)
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
	SetType.Dict.Set(InternString("__hash__"), NoneVal())
	SetType.Slots.SqLen = func(ctx *ExecContext, o Value) int64 {
		if o.IsSet() {
			return int64(o.AsSet().Len())
		}
		return 0
	}
	SetType.Slots.TpIter = func(ctx *ExecContext, o Value) Value {
		if o.IsSet() {
			items := o.AsSet().Items()
			idx := 0
			return NewIterValWithNext(func() (Value, bool) {
				if idx >= len(items) {
					return NoneVal(), false
				}
				v := items[idx]
				idx++
				return v, true
			})
		}
		return NewIterVal(o)
	}

	setNewBuiltin := NewBuiltinFuncVal("set.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		cls := SetType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		s := NewSetVal().AsSet()
		s.Header.Type = cls
		return Value{num: 0, ptr: &s.Header}
	})
	SetType.Dict.Set(DunderNew, setNewBuiltin)
	SetType.dunderNew = setNewBuiltin

	SetType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			return NewSetVal()
		}
		items := ExtractIterableItems(ctx, args[0])
		return NewSetVal(items...)
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

	SetType.Dict.Set(InternString("remove"), NewBuiltinFuncVal("remove", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: remove() takes exactly 1 argument")
		}
		if args[0].IsSet() {
			if !args[0].AsSet().Remove(args[1]) {
				panic(fmt.Sprintf("KeyError: %v", args[1]))
			}
		}
		return NoneVal()
	}))

	SetType.Dict.Set(InternString("discard"), NewBuiltinFuncVal("discard", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: discard() takes exactly 1 argument")
		}
		if args[0].IsSet() {
			args[0].AsSet().Discard(args[1])
		}
		return NoneVal()
	}))

	SetType.Dict.Set(InternString("clear"), NewBuiltinFuncVal("clear", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'clear' of 'set' object needs an argument")
		}
		if args[0].IsSet() {
			args[0].AsSet().Clear()
		}
		return NoneVal()
	}))

	SetType.Dict.Set(InternString("pop"), NewBuiltinFuncVal("pop", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'pop' of 'set' object needs an argument")
		}
		if args[0].IsSet() {
			val, ok := args[0].AsSet().Pop()
			if !ok {
				panic("KeyError: 'pop from an empty set'")
			}
			return val
		}
		return NoneVal()
	}))

	SetType.Dict.Set(InternString("copy"), NewBuiltinFuncVal("copy", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'copy' of 'set' object needs an argument")
		}
		if args[0].IsSet() {
			return NewSetVal(args[0].AsSet().Items()...)
		}
		return NewSetVal()
	}))

	SetType.Dict.Set(InternString("update"), NewBuiltinFuncVal("update", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'update' of 'set' object needs an argument")
		}
		if args[0].IsSet() {
			s := args[0].AsSet()
			for _, other := range args[1:] {
				for _, it := range ExtractIterableItems(ctx, other) {
					s.Add(it)
				}
			}
		}
		return NoneVal()
	}))

	SetType.Dict.Set(InternString("union"), NewBuiltinFuncVal("union", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor 'union' of 'set' object needs an argument")
		}
		res := NewSetVal()
		if args[0].IsSet() {
			res = NewSetVal(args[0].AsSet().Items()...)
		}
		resSet := res.AsSet()
		for _, other := range args[1:] {
			for _, it := range ExtractIterableItems(ctx, other) {
				resSet.Add(it)
			}
		}
		return res
	}))

	SetType.Dict.Set(InternString("intersection"), NewBuiltinFuncVal("intersection", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return args[0]
		}
		if !args[0].IsSet() {
			return NewSetVal()
		}
		cur := args[0].AsSet()
		for _, other := range args[1:] {
			otherSet := NewSetVal(ExtractIterableItems(ctx, other)...).AsSet()
			cur = cur.Intersection(otherSet).AsSet()
		}
		return SetVal(cur)
	}))

	SetType.Dict.Set(InternString("difference"), NewBuiltinFuncVal("difference", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return args[0]
		}
		if !args[0].IsSet() {
			return NewSetVal()
		}
		cur := args[0].AsSet()
		for _, other := range args[1:] {
			otherSet := NewSetVal(ExtractIterableItems(ctx, other)...).AsSet()
			cur = cur.Difference(otherSet).AsSet()
		}
		return SetVal(cur)
	}))

	SetType.Dict.Set(InternString("symmetric_difference"), NewBuiltinFuncVal("symmetric_difference", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return args[0]
		}
		if !args[0].IsSet() {
			return NewSetVal()
		}
		otherSet := NewSetVal(ExtractIterableItems(ctx, args[1])...).AsSet()
		return args[0].AsSet().SymmetricDifference(otherSet)
	}))

	SetType.Dict.Set(InternString("issubset"), NewBuiltinFuncVal("issubset", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsSet() {
			return BoolVal(false)
		}
		otherSet := NewSetVal(ExtractIterableItems(ctx, args[1])...).AsSet()
		return BoolVal(args[0].AsSet().IsSubset(otherSet))
	}))

	SetType.Dict.Set(InternString("issuperset"), NewBuiltinFuncVal("issuperset", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsSet() {
			return BoolVal(false)
		}
		otherSet := NewSetVal(ExtractIterableItems(ctx, args[1])...).AsSet()
		return BoolVal(args[0].AsSet().IsSuperset(otherSet))
	}))

	SetType.Dict.Set(InternString("isdisjoint"), NewBuiltinFuncVal("isdisjoint", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsSet() {
			return BoolVal(true)
		}
		otherSet := NewSetVal(ExtractIterableItems(ctx, args[1])...).AsSet()
		return BoolVal(args[0].AsSet().Intersection(otherSet).AsSet().Len() == 0)
	}))

	SetType.Dict.Set(InternString("__contains__"), NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsSet() {
			return BoolVal(false)
		}
		return BoolVal(args[0].AsSet().Contains(args[1]))
	}))

	SetType.Dict.Set(InternString("__len__"), NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsSet() {
			return Int64Val(0)
		}
		return Int64Val(int64(args[0].AsSet().Len()))
	}))

	SetType.Dict.Set(InternString("__iter__"), NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsSet() {
			return NewIterVal(NoneVal())
		}
		return SetType.Slots.TpIter(ctx, args[0])
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
	BytesType.Slots.NbMul = bytesNbMul
	BytesType.Slots.TpCall = bytesTpCall
	BytesType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return NewIterVal(o) }
	BytesType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'bytes' object needs an argument")
		}
		return NewIterVal(args[0])
	}))

	bytesNewBuiltin := NewBuiltinFuncVal("bytes.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		var callArgs []Value
		if len(args) > 1 {
			callArgs = args[1:]
		}
		res := bytesTpCall(ctx, callable, callArgs, kwnames)
		if len(args) > 0 && args[0].IsType() && args[0].AsType() == BytesType {
			return res
		}
		cls := BytesType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		inst := NewInstanceOfType(cls)
		inst.AsInstance().Slots = []Value{res}
		return inst
	})
	BytesType.Dict.Set(DunderNew, bytesNewBuiltin)
	BytesType.dunderNew = bytesNewBuiltin
	BytesType.Dict.Set(InternString("decode"), NewBuiltinFuncVal("decode", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: decode() takes at least 1 argument")
		}
		b := args[0].AsBytes()
		npos := len(args) - len(kwnames)
		encStr := "utf-8"
		if npos >= 2 && !args[1].IsNone() {
			if s, ok := unwrapStrVal(args[1]); ok {
				encStr = s
			}
		}
		for i, kn := range kwnames {
			name, _ := unwrapStrVal(kn)
			if name == "encoding" {
				if s, ok := unwrapStrVal(args[npos+i]); ok {
					encStr = s
				}
			}
		}
		decoded, err := DecodeBytesToString(b.Data, encStr)
		if err != nil {
			panic(err.Error())
		}
		return NewStringVal(decoded)
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
		switch {
		case args[1].IsBytes():
			prefix = args[1].AsBytes().Data
		case args[1].IsByteArray():
			prefix = args[1].AsByteArray().Data
		default:
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
		switch {
		case args[1].IsBytes():
			suffix = args[1].AsBytes().Data
		case args[1].IsByteArray():
			suffix = args[1].AsByteArray().Data
		default:
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
		switch {
		case args[1].IsBytes():
			sub = args[1].AsBytes().Data
		case args[1].IsByteArray():
			sub = args[1].AsByteArray().Data
		default:
			panic("TypeError: find argument must be bytes")
		}
		return Int64Val(int64(bytes.Index(b, sub)))
	}))

	ByteArrayType = MustNewType("bytearray", []*PyType{ObjectType}, nil)
	ByteArrayType.Dict.Set(InternString("__hash__"), NoneVal())
	ByteArrayType.Slots.SqLen = byteArraySqLen
	ByteArrayType.Slots.SqItem = byteArrayMpSubscript
	ByteArrayType.Slots.MpSubscript = byteArrayMpSubscript
	ByteArrayType.Slots.SqAssItem = byteArrayMpAssSubscript
	ByteArrayType.Slots.MpAssSubscript = byteArrayMpAssSubscript
	ByteArrayType.Slots.TpAsBuffer = byteArrayTpAsBuffer
	ByteArrayType.Slots.NbAdd = byteArrayNbAdd
	ByteArrayType.Slots.NbMul = byteArrayNbMul
	ByteArrayType.Slots.TpCall = byteArrayTpCall
	ByteArrayType.Slots.TpIter = func(ctx *ExecContext, o Value) Value { return NewIterVal(o) }
	ByteArrayType.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__iter__' of 'bytearray' object needs an argument")
		}
		return NewIterVal(args[0])
	}))
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
		switch {
		case val.IsBytes():
			toAppend = val.AsBytes().Data
		case val.IsByteArray():
			toAppend = val.AsByteArray().Data
		case val.IsMemoryView():
			toAppend = val.AsMemoryView().Buf
		case val.IsList():
			for _, item := range val.AsList().Items {
				b := item.AsInt64()
				if b < 0 || b > 255 {
					panic("ValueError: byte must be in range(0, 256)")
				}
				toAppend = append(toAppend, byte(b))
			}
		default:
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
		data := make([]byte, len(ba.Data))
		copy(data, ba.Data)
		ba.mu.RUnlock()
		npos := len(args) - len(kwnames)
		encStr := "utf-8"
		if npos >= 2 && !args[1].IsNone() {
			if s, ok := unwrapStrVal(args[1]); ok {
				encStr = s
			}
		}
		for i, kn := range kwnames {
			name, _ := unwrapStrVal(kn)
			if name == "encoding" {
				if s, ok := unwrapStrVal(args[npos+i]); ok {
					encStr = s
				}
			}
		}
		decoded, err := DecodeBytesToString(data, encStr)
		if err != nil {
			panic(err.Error())
		}
		return NewStringVal(decoded)
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

	initBytesMethods()

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

	TypeType.Dict.Set(DunderCall, NewBuiltinFuncVal("type.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor '__call__' of 'type' object needs an argument")
		}
		cls := args[0]
		return typeTpCall(ctx, cls, args[1:], kwnames)
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
	MemoryViewType.Dict.Set(InternString("__enter__"), NewBuiltinFuncVal("__enter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: __enter__() missing self")
		}
		return args[0]
	}))
	MemoryViewType.Dict.Set(InternString("__exit__"), NewBuiltinFuncVal("__exit__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) > 0 {
			mv := args[0].AsMemoryView()
			mv.released = true
			mv.Buf = nil
		}
		return NoneVal()
	}))
	MemoryViewType.Dict.Set(InternString("cast"), NewBuiltinFuncVal("cast", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: cast() takes at least 1 argument")
		}
		mv := args[0].AsMemoryView()
		if mv.released {
			panic("ValueError: operation forbidden on released memoryview object")
		}
		obj := &PyMemoryView{
			Header: ObjectHeader{
				Kind: KindMemoryView,
				Type: MemoryViewType,
			},
			Obj:      mv.Obj,
			Buf:      mv.Buf,
			ReadOnly: mv.ReadOnly,
			ItemSize: 1,
		}
		return Value{num: 0, ptr: &obj.Header}
	}))
	MemoryViewType.Dict.Set(InternString("nbytes"), NewPropertyVal(
		NewBuiltinFuncVal("nbytes", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
			if len(args) < 1 {
				panic("TypeError: nbytes missing self")
			}
			mv := args[0].AsMemoryView()
			return Int64Val(int64(len(mv.Buf)))
		}),
		NoneVal(),
		NoneVal(),
	))

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

	moduleNewBuiltin := NewBuiltinFuncVal("module.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: module.__new__(): not enough arguments")
		}
		cls := args[0]
		if !cls.IsType() {
			panic("TypeError: module.__new__(X): X is not a type object")
		}
		return NewInstanceOfType(cls.AsType())
	})
	ModuleType.Dict.Set(DunderNew, moduleNewBuiltin)
	ModuleType.dunderNew = moduleNewBuiltin

	moduleInitBuiltin := NewBuiltinFuncVal("module.__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			panic("TypeError: module.__init__() takes at least 1 argument")
		}
		self := args[0]
		if !self.IsModule() {
			panic("TypeError: module.__init__() target must be a module")
		}
		mod := self.AsModule()
		npos := len(args) - len(kwnames)
		var nameV, docV Value
		haveName := false
		docV = NoneVal()
		if npos > 3 {
			panic("TypeError: module.__init__() takes at most 3 arguments")
		}
		if npos >= 2 {
			nameV, haveName = args[1], true
		}
		if npos >= 3 {
			docV = args[2]
		}
		for i, kn := range kwnames {
			switch kn.AsString() {
			case "name":
				nameV, haveName = args[npos+i], true
			case "doc":
				docV = args[npos+i]
			default:
				panic("TypeError: module.__init__() got an unexpected keyword argument '" + kn.AsString() + "'")
			}
		}
		if haveName {
			if !nameV.IsAnyString() {
				panic("TypeError: module.__init__() argument 'name' must be str, not " + nameV.Type().Name)
			}
			mod.Name = nameV.AsString()
			mod.Dict.Set(InternString("__name__"), nameV)
		}
		mod.Dict.Set(InternString("__doc__"), docV)
		mod.Dict.Set(InternString("__package__"), NoneVal())
		mod.Dict.Set(InternString("__loader__"), NoneVal())
		mod.Dict.Set(InternString("__spec__"), NoneVal())
		return NoneVal()
	})
	ModuleType.Dict.Set(DunderInit, moduleInitBuiltin)
	ModuleType.dunderInit = moduleInitBuiltin

	moduleGetAttributeBuiltin := NewBuiltinFuncVal("module.__getattribute__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: module.__getattribute__() takes 2 arguments")
		}
		return defaultModuleGetAttr(ctx, args[0], InternValue(args[1]))
	})
	ModuleType.Dict.Set(DunderGetAttribute, moduleGetAttributeBuiltin)

	moduleSetAttrBuiltin := NewBuiltinFuncVal("module.__setattr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: module.__setattr__() takes 3 arguments")
		}
		return moduleSetAttr(ctx, args[0], args[1], args[2])
	})
	ModuleType.Dict.Set(DunderSetAttr, moduleSetAttrBuiltin)

	moduleDelAttrBuiltin := NewBuiltinFuncVal("module.__delattr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: module.__delattr__() takes 2 arguments")
		}
		objectDelAttr(ctx, args[0], args[1])
		return NoneVal()
	})
	ModuleType.Dict.Set(DunderDelAttr, moduleDelAttrBuiltin)

	moduleReprBuiltin := NewBuiltinFuncVal("module.__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NewStringVal("<module>")
		}
		if !args[0].IsModule() {
			return NewStringVal(fmt.Sprintf("<%s object at %p>", args[0].Type().Name, args[0].HeaderPtr()))
		}
		m := args[0].AsModule()
		name := m.Name
		if nameV, ok := m.Dict.Get(InternString("__name__")); ok && nameV.IsAnyString() {
			name = nameV.AsString()
		}
		if fileV, ok := m.Dict.Get(InternString("__file__")); ok && fileV.IsAnyString() {
			return NewStringVal(fmt.Sprintf("<module '%s' from '%s'>", name, fileV.AsString()))
		}
		return NewStringVal(fmt.Sprintf("<module '%s'>", name))
	})
	ModuleType.Dict.Set(InternString("__repr__"), moduleReprBuiltin)
	ModuleType.syncDunderSlots()

	ModuleType.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		cls := ModuleType
		if callable.IsType() {
			cls = callable.AsType()
		}
		inst := NewInstanceOfType(cls)
		if cls.HasInit() {
			initArgs := make([]Value, 1+len(args))
			initArgs[0] = inst
			copy(initArgs[1:], args)
			ctx.Call(cls.DunderInitVal(), initArgs, kwnames)
		}
		return inst
	}

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
	builtinTypes[KindFrame] = FrameType
	builtinTypes[KindLocalsProxy] = LocalsProxyType
	builtinTypes[KindTraceback] = TracebackType
	builtinTypes[KindWeakRef] = WeakRefType
	builtinTypes[KindWeakProxy] = WeakProxyType
	builtinTypes[KindEllipsis] = EllipsisType
	builtinTypes[KindMappingProxy] = MappingProxyType
	builtinTypes[KindSet] = SetType
	builtinTypes[KindNative] = ObjectType
	builtinTypes[KindComplex] = ComplexType
	builtinTypes[KindLazyImport] = ObjectType

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
		posCount := len(args) - len(kwnames)
		if posCount == 2 && len(kwnames) == 0 {
			return TypeVal(args[1].Type())
		}
		if posCount < 4 {
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
		metaType := TypeType
		if mcls.IsType() {
			metaType = mcls.AsType()
		}
		t, err := NewTypeWithMeta(name, bases, dict, metaType)
		if err != nil {
			panic("TypeError: " + err.Error())
		}
		t.Flags |= FlagHeapType
		var kwArgs []Value
		if len(kwnames) > 0 {
			kwArgs = args[posCount:]
		}
		initTypeDescriptorsAndSubclasses(ctx, t, kwArgs, kwnames)
		return TypeVal(t)
	})
	typeNewBuiltin.AsBuiltinFunc().NoBind = true
	TypeType.Dict.Set(DunderNew, typeNewBuiltin)
	TypeType.dunderNew = typeNewBuiltin
	TypeType.Dict.Set(DunderSetAttr, NewBuiltinFuncVal("type.__setattr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 || !args[0].IsType() {
			panic("TypeError: type.__setattr__() takes 3 arguments")
		}
		cls := args[0].AsType()
		interned := InternValue(args[1])
		value := args[2]
		meta := cls.Header.Type
		if meta == nil {
			meta = TypeType
		}
		if descr, _, ok := meta.LookupMRO(interned); ok {
			if descrSet := descr.Type().Slots.TpDescrSet; descrSet != nil {
				descrSet(ctx, descr, args[0], value)
				return NoneVal()
			}
		}
		cls.SetAttr(interned, value)
		return NoneVal()
	}))
	TypeType.Dict.Set(DunderDelAttr, NewBuiltinFuncVal("type.__delattr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 || !args[0].IsType() {
			panic("TypeError: type.__delattr__() takes 2 arguments")
		}
		cls := args[0].AsType()
		interned := InternValue(args[1])
		meta := cls.Header.Type
		if meta == nil {
			meta = TypeType
		}
		if descr, _, ok := meta.LookupMRO(interned); ok {
			if descrSet := descr.Type().Slots.TpDescrSet; descrSet != nil {
				descrSet(ctx, descr, args[0], UnboundVal())
				return NoneVal()
			}
		}
		if cls.Dict != nil && cls.Dict.Delete(interned) {
			cls.InvalidateVersion()
			return NoneVal()
		}
		panic(NewAttributeError("type object '" + cls.Name + "' has no attribute '" + interned.AsString() + "'"))
	}))

	TypeType.Dict.Set(InternString("mro"), NewBuiltinFuncVal("type.mro", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor 'mro' of 'type' object needs an argument")
		}
		cls := args[0].AsType()
		var items []Value
		for _, b := range cls.MRO {
			items = append(items, TypeVal(b))
		}
		return NewListVal(items...)
	}))

	TypeType.Dict.Set(InternString("__subclasses__"), NewBuiltinFuncVal("type.__subclasses__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor '__subclasses__' of 'type' object needs an argument")
		}
		cls := args[0].AsType()
		subclassesMu.RLock()
		defer subclassesMu.RUnlock()
		var items []Value
		for _, s := range cls.Subclasses {
			items = append(items, TypeVal(s))
		}
		return NewListVal(items...)
	}))

	TypeType.Dict.Set(DunderCall, NewBuiltinFuncVal("type.__call__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: descriptor '__call__' of 'type' object needs an argument")
		}
		cls := args[0]
		return typeTpCall(ctx, cls, args[1:], kwnames)
	}))

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

	objectSetAttrBuiltin := NewBuiltinFuncVal("object.__setattr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 3 {
			panic("TypeError: object.__setattr__() takes 3 arguments")
		}
		obj := args[0]
		interned := InternValue(args[1])
		value := args[2]
		tp := obj.Type()
		if descr, _, foundInClass := tp.LookupMRO(interned); foundInClass {
			descrType := descr.Type()
			if descrType.Slots.TpDescrSet != nil {
				descrType.Slots.TpDescrSet(ctx, descr, obj, value)
				return NoneVal()
			}
		}
		if obj.IsInstance() {
			if tp.Flags&FlagForbidDynamicAttrs != 0 {
				if BaseExceptionType != nil && IsSubclass(tp, BaseExceptionType) {
					switch interned.AsString() {
					case "__traceback__", "__cause__", "__context__", "__suppress_context__":
						goto allowDynamicSet
					}
				}
				panic(NewAttributeError("'" + tp.Name + "' object has no attribute '" + interned.AsString() + "'"))
			}
		allowDynamicSet:
			obj.AsInstance().SetAttr(interned, value)
			return NoneVal()
		}
		if obj.IsFunction() {
			fn := obj.AsFunction()
			if fn.Dict == nil {
				fn.Dict = NewDictVal(4).AsDict()
			}
			fn.Dict.Set(interned, value)
			return NoneVal()
		}
		if obj.IsModule() {
			if interned.AsString() == "__class__" {
				AssignInstanceClass(obj, value)
				return NoneVal()
			}
			obj.AsModule().Dict.Set(interned, value)
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
		if k := obj.Kind(); k == KindFuture || k == KindTask {
			AsyncAttrs(obj, true).Set(interned, value)
			return NoneVal()
		}
		panic(NewAttributeError("'" + tp.Name + "' object cannot set attribute '" + interned.AsString() + "'"))
	})
	ObjectType.Dict.Set(DunderSetAttr, objectSetAttrBuiltin)

	objectDelAttrBuiltin := NewBuiltinFuncVal("object.__delattr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			panic("TypeError: object.__delattr__() takes 2 arguments")
		}
		obj := args[0]
		interned := InternValue(args[1])
		tp := obj.Type()
		if descr, _, foundInClass := tp.LookupMRO(interned); foundInClass {
			descrType := descr.Type()
			if descrType.Slots.TpDescrSet != nil {
				descrType.Slots.TpDescrSet(ctx, descr, obj, UnboundVal())
				return NoneVal()
			}
		}
		if obj.IsInstance() {
			if !obj.AsInstance().DeleteAttr(interned) {
				panic(NewAttributeError("'" + tp.Name + "' object has no attribute '" + interned.AsString() + "'"))
			}
			return NoneVal()
		}
		if obj.IsFunction() {
			fn := obj.AsFunction()
			if fn.Dict != nil && fn.Dict.Delete(interned) {
				return NoneVal()
			}
			panic(NewAttributeError("'" + tp.Name + "' object has no attribute '" + interned.AsString() + "'"))
		}
		if obj.IsModule() {
			if !obj.AsModule().Dict.Delete(interned) {
				panic(NewAttributeError("module '" + obj.AsModule().Name + "' has no attribute '" + interned.AsString() + "'"))
			}
			return NoneVal()
		}
		panic(NewAttributeError("'" + tp.Name + "' object cannot delete attribute '" + interned.AsString() + "'"))
	})
	ObjectType.Dict.Set(DunderDelAttr, objectDelAttrBuiltin)

	ObjectType.Dict.Set(DunderEq, NewBuiltinFuncVal("object.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		if args[0] == args[1] {
			return BoolVal(true)
		}
		return NotImplementedVal()
	}))
	ObjectType.Dict.Set(DunderNe, NewBuiltinFuncVal("object.__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		eqRes := RichCompare(ctx, args[0], args[1], CmpEQ)
		if eqRes.IsNotImplemented() {
			return NotImplementedVal()
		}
		return BoolVal(!eqRes.IsTruthy())
	}))
	ObjectType.Dict.Set(InternString("__hash__"), NewBuiltinFuncVal("object.__hash__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return Int64Val(0)
		}
		return Int64Val(int64(uintptr(unsafe.Pointer(args[0].HeaderPtr()))))
	}))
	ObjectType.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal("object.__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NewStringVal("<object>")
		}
		return NewStringVal(fmt.Sprintf("<%s object at %p>", args[0].Type().Name, args[0].HeaderPtr()))
	}))
	ObjectType.Dict.Set(InternString("__str__"), NewBuiltinFuncVal("object.__str__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NewStringVal("<object>")
		}
		return NewStringVal(PyRepr(ctx, args[0]))
	}))
	ObjectType.Dict.Set(InternString("__format__"), NewBuiltinFuncVal("object.__format__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NewStringVal("")
		}
		if len(args) > 1 && args[1].IsAnyString() && args[1].AsString() != "" {
			panic("TypeError: unsupported format string passed to " + args[0].Type().Name + ".__format__")
		}
		return NewStringVal(PyStr(ctx, args[0]))
	}))
	ObjectType.Dict.Set(InternString("__sizeof__"), NewBuiltinFuncVal("object.__sizeof__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return Int64Val(64)
	}))
	ObjectType.Dict.Set(InternString("__reduce_ex__"), NewBuiltinFuncVal("object.__reduce_ex__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NoneVal()
		}
		self := args[0]
		// Honour an overridden __reduce__, like CPython's object.__reduce_ex__.
		if fn, owner, ok := self.Type().LookupMRO(InternString("__reduce__")); ok && owner != ObjectType {
			return ctx.Invoke1(fn, self)
		}

		cls := self.Type()
		clsVal := TypeVal(cls)

		if self.IsInstance() {
			inst := self.AsInstance()
			dictState := NoneVal()
			slotState := NoneVal()

			if (cls.Flags & FlagForbidDynamicAttrs) != 0 {
				if inst.Shape != nil && len(inst.Shape.Attrs) > 0 {
					slotDict := NewDictVal(len(inst.Shape.Attrs)).AsDict()
					for _, a := range inst.Shape.Attrs {
						if v, ok := inst.GetSlot(a.Index); ok && !v.IsUnbound() {
							slotDict.Set(a.Name, v)
						}
					}
					if slotDict.Len() > 0 {
						slotState = Value{num: uint64(KindDict), ptr: &slotDict.Header}
					}
				}
			} else {
				if inst.Shape != nil && len(inst.Shape.Attrs) > 0 {
					dynDict := NewDictVal(len(inst.Shape.Attrs)).AsDict()
					for _, a := range inst.Shape.Attrs {
						if v, ok := inst.GetSlot(a.Index); ok && !v.IsUnbound() {
							dynDict.Set(a.Name, v)
						}
					}
					if dynDict.Len() > 0 {
						dictState = Value{num: uint64(KindDict), ptr: &dynDict.Header}
					}
				}
			}

			state := NoneVal()
			if slotState.HeaderPtr() != nil {
				state = NewTupleVal(NoneVal(), slotState)
			} else if dictState.HeaderPtr() != nil && !dictState.IsNone() {
				state = dictState
			}

			if !state.IsNone() {
				return NewTupleVal(ObjectNewBuiltin, NewTupleVal(clsVal), state)
			}
		}

		return NewTupleVal(ObjectNewBuiltin, NewTupleVal(clsVal))
	}))
	ObjectType.Dict.Set(InternString("__reduce__"), NewBuiltinFuncVal("object.__reduce__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return NoneVal()
		}
		if redEx, _, ok := args[0].Type().LookupMRO(InternString("__reduce_ex__")); ok && redEx != callable {
			return ctx.Invoke2(redEx, args[0], Int64Val(0))
		}
		return NewTupleVal(ObjectNewBuiltin, NewTupleVal(TypeVal(args[0].Type())))
	}))
	ObjectType.Dict.Set(InternString("__dir__"), NewBuiltinFuncVal("object.__dir__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return NewListVal()
	}))
	ObjectInitBuiltin = NewBuiltinFuncVal("object.__init__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return NoneVal()
	})
	ObjectType.Dict.Set(DunderInit, ObjectInitBuiltin)
	ObjectType.dunderInit = ObjectInitBuiltin

	ObjectNewBuiltin = NewBuiltinFuncVal("object.__new__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) == 0 {
			panic("TypeError: object.__new__(): not enough arguments")
		}
		if !args[0].IsType() {
			panic("TypeError: object.__new__(X): X is not a type object")
		}
		cls := args[0].AsType()
		return NewInstanceOfType(cls)
	})
	ObjectNewBuiltin.AsBuiltinFunc().NoBind = true
	ObjectType.Dict.Set(DunderNew, ObjectNewBuiltin)
	ObjectType.dunderNew = ObjectNewBuiltin

	ObjectType.syncDunderSlots()
	TypeType.syncDunderSlots()
	TypeType.Slots.TpCall = typeTpCall
}

type PyDictKeys struct {
	inst PyInstance
	Dict *PyDict
}

func NewDictKeysVal(d *PyDict) Value {
	pk := &PyDictKeys{
		inst: PyInstance{
			Header: ObjectHeader{Kind: KindInstance, Type: DictKeysType},
			Shape:  DictKeysType.RootShape,
		},
		Dict: d,
	}
	return ObjectVal(&pk.inst.Header)
}
