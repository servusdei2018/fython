package vm

import (
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	SimpleNamespaceType       *val.PyType
	DynamicClassAttributeType *val.PyType
	typesOnce                 sync.Once
)

type pyDynamicClassAttribute struct {
	Header val.ObjectHeader
	FGet   val.Value
	FSet   val.Value
	FDel   val.Value
	Doc    val.Value
	Attrs  *val.PyDict
}

func initTypesModule() {
	typesOnce.Do(func() {
		// 1. SimpleNamespace
		SimpleNamespaceType = val.MustNewType("SimpleNamespace", []*val.PyType{val.ObjectType}, nil)
		SimpleNamespaceType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			instVal := val.NewInstanceOfType(SimpleNamespaceType)
			inst := instVal.AsInstance()
			if len(kwnames) > 0 {
				kwStart := len(args) - len(kwnames)
				for i, kn := range kwnames {
					inst.SetAttr(kn, args[kwStart+i])
				}
			}
			return instVal
		}

		SimpleNamespaceType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) == 0 {
				return val.InternString("namespace()")
			}
			inst := args[0].AsInstance()
			var parts []string
			if inst.Shape != nil {
				for i, attr := range inst.Shape.Attrs {
					v, ok := inst.GetSlot(i)
					if ok && !v.IsUnbound() {
						parts = append(parts, attr.Name.AsString()+"="+v.String())
					}
				}
			}
			return val.InternString("namespace(" + strings.Join(parts, ", ") + ")")
		}))

		// 2. DynamicClassAttribute
		DynamicClassAttributeType = val.MustNewType("DynamicClassAttribute", []*val.PyType{val.ObjectType}, nil)
		DynamicClassAttributeType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			fget := val.NoneVal()
			fset := val.NoneVal()
			fdel := val.NoneVal()
			doc := val.NoneVal()
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

			attr := &pyDynamicClassAttribute{
				Header: val.ObjectHeader{
					Kind:  val.KindNative,
					Flags: val.FlagDataDescriptor,
					Type:  DynamicClassAttributeType,
				},
				FGet: fget,
				FSet: fset,
				FDel: fdel,
				Doc:  doc,
			}
			return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&attr.Header)))
		}

		DynamicClassAttributeType.Slots.TpDescrGet = func(ctx *val.ExecContext, descr, obj val.Value, owner *val.PyType) val.Value {
			attr := (*pyDynamicClassAttribute)(unsafe.Pointer(descr.HeaderPtr()))
			if obj.IsNone() || obj.IsUnbound() {
				if !attr.FGet.IsNone() {
					return ctx.Call(attr.FGet, []val.Value{val.TypeVal(owner)}, nil)
				}
				panic("AttributeError: unreadable attribute")
			}
			if !attr.FGet.IsNone() {
				return ctx.Call(attr.FGet, []val.Value{obj}, nil)
			}
			panic("AttributeError: unreadable attribute")
		}

		DynamicClassAttributeType.Slots.TpDescrSet = func(ctx *val.ExecContext, descr, obj, value val.Value) {
			attr := (*pyDynamicClassAttribute)(unsafe.Pointer(descr.HeaderPtr()))
			if !attr.FSet.IsNone() {
				ctx.Call(attr.FSet, []val.Value{obj, value}, nil)
				return
			}
			panic("AttributeError: can't set attribute")
		}

		DynamicClassAttributeType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			attr := (*pyDynamicClassAttribute)(unsafe.Pointer(obj.HeaderPtr()))
			nameStr := name.AsString()
			switch nameStr {
			case "fget":
				if !attr.FGet.IsNone() && attr.FGet.HeaderPtr() != nil {
					return attr.FGet
				}
				return val.NoneVal()
			case "fset":
				if !attr.FSet.IsNone() && attr.FSet.HeaderPtr() != nil {
					return attr.FSet
				}
				return val.NoneVal()
			case "fdel":
				if !attr.FDel.IsNone() && attr.FDel.HeaderPtr() != nil {
					return attr.FDel
				}
				return val.NoneVal()
			case "__doc__":
				if !attr.Doc.IsNone() && attr.Doc.HeaderPtr() != nil {
					return attr.Doc
				}
				return val.NoneVal()
			}
			if attr.Attrs != nil {
				if v, ok := attr.Attrs.Get(val.InternValue(name)); ok {
					return v
				}
			}
			if descr, _, ok := obj.Type().LookupMRO(val.InternValue(name)); ok {
				descrType := descr.Type()
				if descrType.Slots.TpDescrGet != nil {
					return descrType.Slots.TpDescrGet(ctx, descr, obj, obj.Type())
				}
				return descr
			}
			panic("val: AttributeError: '" + obj.Type().Name + "' object has no attribute '" + nameStr + "'")
		}

		DynamicClassAttributeType.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
			attr := (*pyDynamicClassAttribute)(unsafe.Pointer(obj.HeaderPtr()))
			nameStr := name.AsString()
			switch nameStr {
			case "fget":
				attr.FGet = value
				return val.NoneVal()
			case "fset":
				attr.FSet = value
				return val.NoneVal()
			case "fdel":
				attr.FDel = value
				return val.NoneVal()
			case "__doc__":
				attr.Doc = value
				return val.NoneVal()
			}
			if attr.Attrs == nil {
				attr.Attrs = val.NewDictVal(8).AsDict()
			}
			attr.Attrs.Set(val.InternValue(name), value)
			return val.NoneVal()
		}

		dcaInit := val.NewBuiltinFuncVal("DynamicClassAttribute.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __init__ takes at least 1 argument")
			}
			self := args[0]
			var fget, fset, fdel, doc val.Value
			fget = val.NoneVal()
			fset = val.NoneVal()
			fdel = val.NoneVal()
			doc = val.NoneVal()
			if len(args) > 1 {
				fget = args[1]
			}
			if len(args) > 2 {
				fset = args[2]
			}
			if len(args) > 3 {
				fdel = args[3]
			}
			if len(args) > 4 {
				doc = args[4]
			}
			if len(kwnames) > 0 {
				kwStart := len(args) - len(kwnames)
				for i, kn := range kwnames {
					argVal := args[kwStart+i]
					switch kn.AsString() {
					case "fget":
						fget = argVal
					case "fset":
						fset = argVal
					case "fdel":
						fdel = argVal
					case "doc":
						doc = argVal
					}
				}
			}
			val.SetAttrCtx(ctx, self, val.InternString("fget"), fget)
			val.SetAttrCtx(ctx, self, val.InternString("fset"), fset)
			val.SetAttrCtx(ctx, self, val.InternString("fdel"), fdel)
			val.SetAttrCtx(ctx, self, val.InternString("__doc__"), doc)
			return val.NoneVal()
		})
		DynamicClassAttributeType.Dict.Set(val.DunderInit, dcaInit)

		DynamicClassAttributeType.Dict.Set(val.InternString("getter"), val.NewBuiltinFuncVal("getter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: getter() takes 2 arguments")
			}
			attr := (*pyDynamicClassAttribute)(unsafe.Pointer(args[0].HeaderPtr()))
			newAttr := &pyDynamicClassAttribute{
				Header: val.ObjectHeader{Kind: val.KindNative, Flags: val.FlagDataDescriptor, Type: DynamicClassAttributeType},
				FGet:   args[1],
				FSet:   attr.FSet,
				FDel:   attr.FDel,
				Doc:    attr.Doc,
			}
			return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&newAttr.Header)))
		}))

		DynamicClassAttributeType.Dict.Set(val.InternString("setter"), val.NewBuiltinFuncVal("setter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: setter() takes 2 arguments")
			}
			attr := (*pyDynamicClassAttribute)(unsafe.Pointer(args[0].HeaderPtr()))
			newAttr := &pyDynamicClassAttribute{
				Header: val.ObjectHeader{Kind: val.KindNative, Flags: val.FlagDataDescriptor, Type: DynamicClassAttributeType},
				FGet:   attr.FGet,
				FSet:   args[1],
				FDel:   attr.FDel,
				Doc:    attr.Doc,
			}
			return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&newAttr.Header)))
		}))
	})
}

func (e *Engine) createTypesModule() val.Value {
	initTypesModule()
	initTypingTypes()

	modVal := val.NewModuleVal("types", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("MappingProxyType"), val.TypeVal(val.MappingProxyType))
	mod.Dict.Set(val.InternString("SimpleNamespace"), val.TypeVal(SimpleNamespaceType))
	mod.Dict.Set(val.InternString("DynamicClassAttribute"), val.TypeVal(DynamicClassAttributeType))

	mod.Dict.Set(val.InternString("FunctionType"), val.TypeVal(val.FunctionType))
	mod.Dict.Set(val.InternString("LambdaType"), val.TypeVal(val.FunctionType))
	mod.Dict.Set(val.InternString("CodeType"), val.TypeVal(val.CodeType))
	mod.Dict.Set(val.InternString("MethodType"), val.TypeVal(val.BoundMethodType))
	mod.Dict.Set(val.InternString("BuiltinFunctionType"), val.TypeVal(val.BuiltinFuncType))
	mod.Dict.Set(val.InternString("BuiltinMethodType"), val.TypeVal(val.BuiltinFuncType))
	mod.Dict.Set(val.InternString("ModuleType"), val.TypeVal(val.ModuleType))
	mod.Dict.Set(val.InternString("NoneType"), val.TypeVal(val.NoneType))
	mod.Dict.Set(val.InternString("EllipsisType"), val.TypeVal(val.EllipsisType))
	mod.Dict.Set(val.InternString("NotImplementedType"), val.TypeVal(val.NotImplementedType))
	mod.Dict.Set(val.InternString("GeneratorType"), val.TypeVal(val.GeneratorType))
	mod.Dict.Set(val.InternString("CoroutineType"), val.TypeVal(val.CoroutineType))
	mod.Dict.Set(val.InternString("CellType"), val.TypeVal(val.CellType))
	mod.Dict.Set(val.InternString("TracebackType"), val.TypeVal(val.TracebackType))
	mod.Dict.Set(val.InternString("FrameType"), val.TypeVal(val.FrameType))
	mod.Dict.Set(val.InternString("GetSetDescriptorType"), val.TypeVal(val.MemberDescrType))
	mod.Dict.Set(val.InternString("MemberDescriptorType"), val.TypeVal(val.MemberDescrType))
	mod.Dict.Set(val.InternString("GenericAlias"), val.TypeVal(GenericAliasType))
	mod.Dict.Set(val.InternString("UnionType"), val.TypeVal(UnionType))

	// new_class(name, bases=(), kwds=None, exec_body=None)
	mod.Dict.Set(val.InternString("new_class"), val.NewBuiltinFuncVal("new_class", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: new_class() requires at least 1 argument")
		}
		name := args[0].AsString()
		var bases []*val.PyType
		if len(args) > 1 && !args[1].IsNone() {
			if args[1].IsTuple() {
				for _, b := range args[1].AsTuple().Items {
					if b.IsType() {
						bases = append(bases, b.AsType())
					}
				}
			} else if args[1].IsList() {
				for _, b := range args[1].AsList().Items {
					if b.IsType() {
						bases = append(bases, b.AsType())
					}
				}
			}
		}
		if len(bases) == 0 {
			bases = []*val.PyType{val.ObjectType}
		}

		ns := val.NewDictVal(8).AsDict()
		if len(args) > 3 && !args[3].IsNone() {
			execBody := args[3]
			ctx.Call(execBody, []val.Value{val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&ns.Header)))}, nil)
		}

		newCls := val.MustNewType(name, bases, ns)
		newCls.Flags |= val.FlagHeapType
		return val.TypeVal(newCls)
	}))

	// resolve_bases(bases)
	mod.Dict.Set(val.InternString("resolve_bases"), val.NewBuiltinFuncVal("resolve_bases", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.NewTupleVal()
		}
		return args[0]
	}))

	// prepare_class(name, bases=(), kwds=None)
	mod.Dict.Set(val.InternString("prepare_class"), val.NewBuiltinFuncVal("prepare_class", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		meta := val.TypeType
		ns := val.NewDictVal(8)
		return val.NewTupleVal(val.TypeVal(meta), ns, val.NewDictVal(0))
	}))

	return modVal
}
