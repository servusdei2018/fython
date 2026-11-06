package vm

import (
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// Native _contextvars. Context objects wrap *val.PyContext. The active
// context pointer lives on the VM so asyncio can swap it when a task runs.

var (
	contextVarType *val.PyType
	tokenType      *val.PyType
	contextType    *val.PyType
	tokenMissing   val.Value
	contextOnce    sync.Once
)

type pyContextVar struct {
	Header     val.ObjectHeader
	Name       val.Value
	Default    val.Value
	HasDefault bool
	Key        uintptr
}

type pyToken struct {
	Header  val.ObjectHeader
	Var     val.Value
	Old     val.Value
	Ctx     *val.PyContext
	Missing bool
	Used    bool
}

type pyContext struct {
	Header val.ObjectHeader
	Ctx    *val.PyContext
}

func initContextVarTypes() {
	contextOnce.Do(initContextVarTypesBody)
}

func initContextVarTypesBody() {

	missingType := val.MustNewType("_TokenMissing", []*val.PyType{val.ObjectType}, nil)
	missingType.Dict.Set(val.InternString("__module__"), val.InternString("_contextvars"))
	missingType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.InternString("<Token.MISSING>")
	}))
	missingObj := &struct{ Header val.ObjectHeader }{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: missingType},
	}
	tokenMissing = val.ObjectVal(&missingObj.Header)

	contextVarType = val.MustNewType("ContextVar", []*val.PyType{val.ObjectType}, nil)
	contextVarType.Dict.Set(val.InternString("__module__"), val.InternString("_contextvars"))
	contextVarType.Slots.TpGetAttr = contextVarGetAttr
	contextVarType.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("get", contextVarGet))
	contextVarType.Dict.Set(val.InternString("set"), val.NewBuiltinFuncVal("set", contextVarSet))
	contextVarType.Dict.Set(val.InternString("reset"), val.NewBuiltinFuncVal("reset", contextVarReset))
	contextVarType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.InternString("ContextVar()")
		}
		cv := asContextVar(args[0])
		return val.NewStringVal("<ContextVar name=" + val.PyRepr(ctx, cv.Name) + ">")
	}))
	contextVarType.Slots.TpCall = contextVarTpCall

	tokenType = val.MustNewType("Token", []*val.PyType{val.ObjectType}, nil)
	tokenType.Dict.Set(val.InternString("__module__"), val.InternString("_contextvars"))
	tokenType.Dict.Set(val.InternString("MISSING"), tokenMissing)
	tokenType.Slots.TpGetAttr = tokenGetAttr

	contextType = val.MustNewType("Context", []*val.PyType{val.ObjectType}, nil)
	contextType.Dict.Set(val.InternString("__module__"), val.InternString("_contextvars"))
	contextType.Slots.TpCall = contextTpCall
	contextType.Dict.Set(val.InternString("run"), val.NewBuiltinFuncVal("run", contextRun))
	contextType.Dict.Set(val.InternString("copy"), val.NewBuiltinFuncVal("copy", contextCopy))
	contextType.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("get", contextGet))
	contextType.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", contextGetItem))
	contextType.Dict.Set(val.InternString("__contains__"), val.NewBuiltinFuncVal("__contains__", contextContains))
	contextType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", contextLen))
	contextType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", contextIter))
	contextType.InvalidateVersion()
}

func (e *Engine) createContextVarsModule() val.Value {
	initContextVarTypes()
	modVal := val.NewModuleVal("_contextvars", "")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("ContextVar"), val.TypeVal(contextVarType))
	mod.Dict.Set(val.InternString("Token"), val.TypeVal(tokenType))
	mod.Dict.Set(val.InternString("Context"), val.TypeVal(contextType))
	mod.Dict.Set(val.InternString("copy_context"), val.NewBuiltinFuncVal("copy_context", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vm := ctx.VM.(*VM)
		return wrapContext(vm.ActiveContext().ShallowCopy())
	}))
	return modVal
}

func contextVarTpCall(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 1 || (len(kwnames) == 0 && len(args) != 1) || (len(kwnames) > 0 && len(args)-len(kwnames) != 1) {
		panic(val.NewTypeError("ContextVar() takes exactly 1 positional argument"))
	}
	name := args[0]
	if !name.IsAnyString() {
		panic(val.NewTypeError("ContextVar() name must be a str"))
	}
	hasDefault := false
	def := val.NoneVal()
	for i, kn := range kwnames {
		if kn.AsString() != "default" {
			panic(val.NewTypeError("ContextVar() got an unexpected keyword argument"))
		}
		def = args[len(args)-len(kwnames)+i]
		hasDefault = true
	}
	cv := &pyContextVar{
		Header:     val.ObjectHeader{Kind: val.KindNative, Type: contextVarType},
		Name:       name,
		Default:    def,
		HasDefault: hasDefault,
	}
	cv.Key = uintptr(unsafe.Pointer(cv))
	return val.ObjectVal(&cv.Header)
}

func asContextVar(v val.Value) *pyContextVar {
	return (*pyContextVar)(unsafe.Pointer(v.HeaderPtr()))
}

func contextVarGetAttr(ctx *val.ExecContext, obj, name val.Value) val.Value {
	if name.AsString() == "name" {
		return asContextVar(obj).Name
	}
	return val.ObjectGetAttr(ctx, obj, name)
}

func vmContext(ctx *val.ExecContext) *val.PyContext {
	return ctx.VM.(*VM).ActiveContext()
}

func contextVarGet(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 1 {
		panic(val.NewTypeError("get() missing self"))
	}
	cv := asContextVar(args[0])
	if v, ok := vmContext(ctx).Get(cv.Key); ok {
		return v
	}
	pos := args
	if len(kwnames) > 0 {
		pos = args[:len(args)-len(kwnames)]
		for i, kn := range kwnames {
			if kn.AsString() != "default" {
				panic(val.NewTypeError("get() got an unexpected keyword argument"))
			}
			return args[len(args)-len(kwnames)+i]
		}
	}
	if len(pos) >= 2 {
		return pos[1]
	}
	if len(pos) > 2 {
		panic(val.NewTypeError("get() takes at most 2 arguments"))
	}
	if cv.HasDefault {
		return cv.Default
	}
	panic(val.NewException(val.LookupErrorType, "ContextVar "+cv.Name.AsString()+" is unset"))
}

func contextVarSet(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(kwnames) > 0 || len(args) != 2 {
		panic(val.NewTypeError("set() takes exactly 1 positional argument"))
	}
	cv := asContextVar(args[0])
	cur := vmContext(ctx)
	prev, had := cur.Set(cv.Key, args[0], args[1])
	tok := &pyToken{
		Header:  val.ObjectHeader{Kind: val.KindNative, Type: tokenType},
		Var:     args[0],
		Ctx:     cur,
		Missing: !had,
	}
	if had {
		tok.Old = prev
	} else {
		tok.Old = tokenMissing
	}
	return val.ObjectVal(&tok.Header)
}

func contextVarReset(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 2 {
		panic(val.NewTypeError("reset() takes exactly 1 positional argument"))
	}
	cv := asContextVar(args[0])
	if args[1].Type() != tokenType {
		panic(val.NewTypeError("reset() argument must be a Token"))
	}
	tok := (*pyToken)(unsafe.Pointer(args[1].HeaderPtr()))
	if tok.Used || tok.Var.HeaderPtr() != args[0].HeaderPtr() || tok.Ctx != vmContext(ctx) {
		panic(val.NewException(val.ValueErrorType, "token was created in a different Context"))
	}
	tok.Used = true
	cur := tok.Ctx
	if tok.Missing {
		cur.Del(cv.Key)
	} else {
		cur.Set(cv.Key, args[0], tok.Old)
	}
	return val.NoneVal()
}

func tokenGetAttr(ctx *val.ExecContext, obj, name val.Value) val.Value {
	tok := (*pyToken)(unsafe.Pointer(obj.HeaderPtr()))
	switch name.AsString() {
	case "var":
		return tok.Var
	case "old_value":
		return tok.Old
	default:
		return val.ObjectGetAttr(ctx, obj, name)
	}
}

func contextTpCall(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 0 || len(kwnames) != 0 {
		panic(val.NewTypeError("Context() takes no arguments"))
	}
	return wrapContext(val.NewPyContext())
}

func wrapContext(c *val.PyContext) val.Value {
	obj := &pyContext{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: contextType},
		Ctx:    c,
	}
	return val.ObjectVal(&obj.Header)
}

func asContext(v val.Value) *pyContext {
	return (*pyContext)(unsafe.Pointer(v.HeaderPtr()))
}

func requireContext(v val.Value) *pyContext {
	if v.Type() != contextType {
		panic(val.NewTypeError("expected a Context"))
	}
	return asContext(v)
}

func requireVar(v val.Value) *pyContextVar {
	if v.Type() != contextVarType {
		panic(val.NewTypeError("a ContextVar was expected"))
	}
	return asContextVar(v)
}

func contextRun(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 2 {
		panic(val.NewTypeError("run() missing required argument: 'callable'"))
	}
	self := requireContext(args[0])
	vm := ctx.VM.(*VM)
	prev := vm.context
	vm.context = self.Ctx
	defer func() { vm.context = prev }()
	return vm.Vectorcall(args[1], args[2:], kwnames)
}

func contextCopy(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 1 {
		panic(val.NewTypeError("copy() takes no arguments"))
	}
	return wrapContext(requireContext(args[0]).Ctx.ShallowCopy())
}

func contextGet(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 2 {
		panic(val.NewTypeError("get() missing required argument"))
	}
	self := requireContext(args[0])
	cv := requireVar(args[1])
	if v, ok := self.Ctx.Get(cv.Key); ok {
		return v
	}
	if len(args) >= 3 {
		return args[2]
	}
	return val.NoneVal()
}

func contextGetItem(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 2 {
		panic(val.NewTypeError("__getitem__() takes exactly one argument"))
	}
	self := requireContext(args[0])
	cv := requireVar(args[1])
	if v, ok := self.Ctx.Get(cv.Key); ok {
		return v
	}
	panic(val.NewKeyError(args[1]))
}

func contextContains(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 2 {
		panic(val.NewTypeError("__contains__() takes exactly one argument"))
	}
	self := requireContext(args[0])
	if args[1].Type() != contextVarType {
		return val.BoolVal(false)
	}
	_, ok := self.Ctx.Get(asContextVar(args[1]).Key)
	return val.BoolVal(ok)
}

func contextLen(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 1 {
		panic(val.NewTypeError("__len__() takes no arguments"))
	}
	return val.Int64Val(int64(requireContext(args[0]).Ctx.Len()))
}

func contextIter(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) != 1 {
		panic(val.NewTypeError("__iter__() takes no arguments"))
	}
	vars := requireContext(args[0]).Ctx.Vars()
	return val.NewIterVal(val.NewTupleVal(vars...))
}
