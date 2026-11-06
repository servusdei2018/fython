package vm

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	inspectInitOnce sync.Once
	ParameterType   *val.PyType
	SignatureType   *val.PyType
	EmptyObject     val.Value
)

func initInspectTypes() {
	inspectInitOnce.Do(func() {
		EmptyObject = val.NewInstanceOfType(val.ObjectType)

		ParameterType = val.MustNewType("Parameter", []*val.PyType{val.ObjectType}, nil)
		ParameterType.Dict.Set(val.InternString("POSITIONAL_ONLY"), val.Int64Val(0))
		ParameterType.Dict.Set(val.InternString("POSITIONAL_OR_KEYWORD"), val.Int64Val(1))
		ParameterType.Dict.Set(val.InternString("VAR_POSITIONAL"), val.Int64Val(2))
		ParameterType.Dict.Set(val.InternString("KEYWORD_ONLY"), val.Int64Val(3))
		ParameterType.Dict.Set(val.InternString("VAR_KEYWORD"), val.Int64Val(4))
		ParameterType.Dict.Set(val.InternString("empty"), EmptyObject)

		nameKey := val.InternString("name")
		kindKey := val.InternString("kind")
		defaultKey := val.InternString("default")
		annotationKey := val.InternString("annotation")

		ParameterType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: Parameter() takes at least name and kind")
			}
			self := args[0]
			name := args[1]
			kind := args[2]
			var dflt = EmptyObject
			var ann = EmptyObject
			if len(args) > 3 {
				dflt = args[3]
			}
			if len(args) > 4 {
				ann = args[4]
			}
			val.SetAttrCtx(ctx, self, nameKey, name)
			val.SetAttrCtx(ctx, self, kindKey, kind)
			val.SetAttrCtx(ctx, self, defaultKey, dflt)
			val.SetAttrCtx(ctx, self, annotationKey, ann)
			return val.NoneVal()
		}))

		ParameterType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("<Parameter>")
			}
			self := args[0]
			name := val.GetAttrCtx(ctx, self, nameKey).AsString()
			kind := val.GetAttrCtx(ctx, self, kindKey).AsInt64()
			dflt := val.GetAttrCtx(ctx, self, defaultKey)

			var prefix string
			if kind == 2 {
				prefix = "*"
			} else if kind == 4 {
				prefix = "**"
			}
			res := prefix + name
			if dflt != EmptyObject {
				res += "=" + dflt.String()
			}
			return val.NewStringVal(fmt.Sprintf("<Parameter %q>", res))
		}))

		SignatureType = val.MustNewType("Signature", []*val.PyType{val.ObjectType}, nil)
		SignatureType.Dict.Set(val.InternString("empty"), EmptyObject)

		paramsKey := val.InternString("parameters")
		retAnnKey := val.InternString("return_annotation")

		SignatureType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: Signature() requires self")
			}
			self := args[0]
			paramsDict := val.NewDictVal(8)
			if len(args) > 1 && !args[1].IsNone() {
				paramsDict = args[1]
			}
			var retAnn = EmptyObject
			if len(args) > 2 {
				retAnn = args[2]
			}
			val.SetAttrCtx(ctx, self, paramsKey, paramsDict)
			val.SetAttrCtx(ctx, self, retAnnKey, retAnn)
			return val.NoneVal()
		}))

		SignatureType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("()")
			}
			self := args[0]
			paramsVal := val.GetAttrCtx(ctx, self, paramsKey)
			var parts []string
			renderSlash := false
			if paramsVal.IsDict() {
				for _, entry := range paramsVal.AsDict().Items() {
					param := entry.Value
					pName := val.GetAttrCtx(ctx, param, nameKey).AsString()
					pKind := val.GetAttrCtx(ctx, param, kindKey).AsInt64()
					pDflt := val.GetAttrCtx(ctx, param, defaultKey)

					if pKind == 0 {
						renderSlash = true
					} else if renderSlash {
						parts = append(parts, "/")
						renderSlash = false
					}

					prefix := ""
					if pKind == 2 {
						prefix = "*"
					} else if pKind == 4 {
						prefix = "**"
					}
					itemStr := prefix + pName
					if pDflt != EmptyObject {
						itemStr += "=" + pDflt.String()
					}
					parts = append(parts, itemStr)
				}
				if renderSlash {
					parts = append(parts, "/")
				}
			}
			res := "(" + strings.Join(parts, ", ") + ")"
			retAnn := val.GetAttrCtx(ctx, self, retAnnKey)
			if retAnn != EmptyObject {
				res += " -> " + retAnn.String()
			}
			return val.NewStringVal(res)
		}))

		ParameterType.InvalidateVersion()
		SignatureType.InvalidateVersion()
	})
}

func callableCode(args []val.Value) *CodeObject {
	if len(args) < 1 {
		return nil
	}
	v := args[0]
	for i := 0; i < 8; i++ {
		if v.IsBoundMethod() {
			v = v.AsBoundMethod().Func
			continue
		}
		if !v.IsFunction() {
			return nil
		}
		fn := v.AsFunction()
		if fn.Dict != nil {
			if w, ok := fn.Dict.Get(val.InternString("__wrapped__")); ok && w.HeaderPtr() != nil && w.HeaderPtr() != v.HeaderPtr() {
				v = w
				continue
			}
		}
		return AsCodeObject(fn.Code)
	}
	return nil
}

func (e *Engine) createInspectModule() val.Value {
	initInspectTypes()

	modVal := val.NewModuleVal("inspect", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Parameter"), val.TypeVal(ParameterType))
	mod.Dict.Set(val.InternString("Signature"), val.TypeVal(SignatureType))
	mod.Dict.Set(val.InternString("_empty"), EmptyObject)

	mod.Dict.Set(val.InternString("cleandoc"), val.NewBuiltinFuncVal("cleandoc", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) != 1 || !args[0].IsAnyString() {
			panic("TypeError: cleandoc() argument must be a str")
		}
		return val.NewStringVal(cleanDoc(args[0].AsString()))
	}))

	// currentframe() -> frame
	mod.Dict.Set(val.InternString("currentframe"), val.NewBuiltinFuncVal("currentframe", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		// 1 level above currentframe
		targetIdx := vmInstance.activeFrameIdx
		if targetIdx < 0 {
			return val.NoneVal()
		}
		return vmInstance.materializeFrame(targetIdx)
	}))

	// stack() -> list of FrameInfo
	mod.Dict.Set(val.InternString("stack"), val.NewBuiltinFuncVal("stack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		var listItems []val.Value
		for idx := vmInstance.activeFrameIdx; idx >= 0; idx-- {
			fVal := vmInstance.materializeFrame(idx)
			if fVal.IsFrame() {
				f := fVal.AsFrame()
				fileName := "<unknown>"
				fnName := "<unknown>"
				line := f.IP
				if val.CodeInspectorHook != nil && f.CO.HeaderPtr() != nil {
					fileName = val.CodeInspectorHook.FileName(f.CO)
					fnName = val.CodeInspectorHook.QualName(f.CO)
					line = val.CodeInspectorHook.LineAt(f.CO, f.IP)
				}
				info := val.NewTupleVal(
					fVal,
					val.NewStringVal(fileName),
					val.Int64Val(int64(line)),
					val.NewStringVal(fnName),
					val.NewListVal(),
					val.Int64Val(0),
				)
				listItems = append(listItems, info)
			}
		}
		return val.NewListVal(listItems...)
	}))

	// isfunction(obj)
	mod.Dict.Set(val.InternString("isfunction"), val.NewBuiltinFuncVal("isfunction", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		v := args[0]
		return val.BoolVal(v.IsFunction() || v.IsBuiltinFunc())
	}))

	// isclass(obj)
	mod.Dict.Set(val.InternString("isclass"), val.NewBuiltinFuncVal("isclass", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		return val.BoolVal(args[0].IsType())
	}))

	mod.Dict.Set(val.InternString("isabstract"), val.NewBuiltinFuncVal("isabstract", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			return val.BoolVal(false)
		}
		am, _, ok := args[0].AsType().LookupMRO(val.InternString("__abstractmethods__"))
		if !ok || am.IsNone() {
			return val.BoolVal(false)
		}
		n := 0
		switch {
		case am.IsSet():
			n = am.AsSet().Len()
		case am.IsTuple():
			n = len(am.AsTuple().Items)
		case am.IsList():
			n = am.AsList().Len()
		default:
			if tp := am.Type(); tp != nil && tp.Slots.SqLen != nil {
				n = int(tp.Slots.SqLen(ctx, am))
			}
		}
		return val.BoolVal(n != 0)
	}))

	// isgenerator(obj)
	mod.Dict.Set(val.InternString("isgenerator"), val.NewBuiltinFuncVal("isgenerator", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		return val.BoolVal(args[0].IsGenerator())
	}))

	// iscoroutine(obj)
	mod.Dict.Set(val.InternString("iscoroutine"), val.NewBuiltinFuncVal("iscoroutine", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		return val.BoolVal(args[0].IsCoroutine())
	}))

	mod.Dict.Set(val.InternString("isgeneratorfunction"), val.NewBuiltinFuncVal("isgeneratorfunction", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		co := callableCode(args)
		return val.BoolVal(co != nil && co.IsGenerator && !co.IsCoroutine)
	}))
	mod.Dict.Set(val.InternString("iscoroutinefunction"), val.NewBuiltinFuncVal("iscoroutinefunction", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		co := callableCode(args)
		return val.BoolVal(co != nil && co.IsCoroutine && !co.IsGenerator)
	}))
	mod.Dict.Set(val.InternString("isasyncgenfunction"), val.NewBuiltinFuncVal("isasyncgenfunction", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		co := callableCode(args)
		return val.BoolVal(co != nil && co.IsGenerator && co.IsCoroutine)
	}))

	// iscode(obj)
	mod.Dict.Set(val.InternString("iscode"), val.NewBuiltinFuncVal("iscode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		return val.BoolVal(args[0].IsCode())
	}))

	// isframe(obj)
	mod.Dict.Set(val.InternString("isframe"), val.NewBuiltinFuncVal("isframe", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		return val.BoolVal(args[0].IsFrame())
	}))

	// istraceback(obj)
	mod.Dict.Set(val.InternString("istraceback"), val.NewBuiltinFuncVal("istraceback", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		return val.BoolVal(args[0].IsTraceback())
	}))

	// getdoc(obj)
	mod.Dict.Set(val.InternString("getdoc"), val.NewBuiltinFuncVal("getdoc", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.NoneVal()
		}
		docVal, ok := val.GetAttrCtx(ctx, args[0], val.InternString("__doc__")), true
		_ = ok
		return docVal
	}))

	// getfile(obj)
	mod.Dict.Set(val.InternString("getfile"), val.NewBuiltinFuncVal("getfile", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getfile() requires 1 argument")
		}
		ob := args[0]
		if ob.IsFunction() {
			co := ob.AsFunction().Code
			if val.CodeInspectorHook != nil && co.HeaderPtr() != nil {
				return val.NewStringVal(val.CodeInspectorHook.FileName(co))
			}
		}
		if ob.IsCode() {
			if val.CodeInspectorHook != nil {
				return val.NewStringVal(val.CodeInspectorHook.FileName(ob))
			}
		}
		if ob.IsModule() {
			return val.NewStringVal(ob.AsModule().File)
		}
		panic("TypeError: could not determine file for " + ob.String())
	}))

	// signature(fn) -> Signature
	mod.Dict.Set(val.InternString("signature"), val.NewBuiltinFuncVal("signature", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: signature() requires at least 1 argument")
		}
		target := args[0]
		var co val.Value
		var defaults []val.Value
		var kwdefaults *val.PyDict

		if target.IsFunction() {
			fn := target.AsFunction()
			co = fn.Code
			defaults = fn.Defaults
			kwdefaults = fn.KwDefaults
		} else if target.IsCode() {
			co = target
		} else if target.IsType() {
			// Check __init__
			initMethod, _, ok := target.Type().LookupMRO(val.DunderInit)
			if ok && initMethod.IsFunction() {
				fn := initMethod.AsFunction()
				co = fn.Code
				defaults = fn.Defaults
				kwdefaults = fn.KwDefaults
			} else {
				// Default signature ()
				sigInst := val.NewInstanceOfType(SignatureType)
				val.SetAttrCtx(ctx, sigInst, val.InternString("parameters"), val.NewDictVal(0))
				val.SetAttrCtx(ctx, sigInst, val.InternString("return_annotation"), EmptyObject)
				return sigInst
			}
		} else {
			panic("TypeError: " + target.String() + " is not a callable object")
		}

		if val.CodeInspectorHook == nil || co.HeaderPtr() == nil {
			panic("ValueError: no code object available for signature")
		}

		hook := val.CodeInspectorHook
		posonly := hook.PosOnlyArgCount(co)
		argcount := hook.ArgCount(co)
		kwonly := hook.KWOnlyArgCount(co)
		flags := hook.Flags(co)
		hasVarArgs := (flags & CO_VARARGS) != 0
		hasVarKW := (flags & CO_VARKEYWORDS) != 0
		varnames := hook.VarNames(co)

		paramDict := val.NewDictVal(8).AsDict()

		// defaults align with the right end of positional parameters
		numDefaults := len(defaults)
		defOffset := argcount - numDefaults

		idx := 0
		// 1. Positional-only params
		for i := 0; i < posonly && idx < len(varnames); i++ {
			name := varnames[idx]
			dflt := EmptyObject
			if idx >= defOffset && idx-defOffset < numDefaults {
				dflt = defaults[idx-defOffset]
			}
			pInst := val.NewInstanceOfType(ParameterType)
			val.SetAttrCtx(ctx, pInst, val.InternString("name"), name)
			val.SetAttrCtx(ctx, pInst, val.InternString("kind"), val.Int64Val(0))
			val.SetAttrCtx(ctx, pInst, val.InternString("default"), dflt)
			val.SetAttrCtx(ctx, pInst, val.InternString("annotation"), EmptyObject)
			paramDict.Set(name, pInst)
			idx++
		}

		// 2. Positional-or-keyword params
		for i := posonly; i < argcount && idx < len(varnames); i++ {
			name := varnames[idx]
			dflt := EmptyObject
			if idx >= defOffset && idx-defOffset < numDefaults {
				dflt = defaults[idx-defOffset]
			}
			pInst := val.NewInstanceOfType(ParameterType)
			val.SetAttrCtx(ctx, pInst, val.InternString("name"), name)
			val.SetAttrCtx(ctx, pInst, val.InternString("kind"), val.Int64Val(1))
			val.SetAttrCtx(ctx, pInst, val.InternString("default"), dflt)
			val.SetAttrCtx(ctx, pInst, val.InternString("annotation"), EmptyObject)
			paramDict.Set(name, pInst)
			idx++
		}

		// 3. Var-positional (*args)
		if hasVarArgs && idx < len(varnames) {
			name := varnames[idx]
			pInst := val.NewInstanceOfType(ParameterType)
			val.SetAttrCtx(ctx, pInst, val.InternString("name"), name)
			val.SetAttrCtx(ctx, pInst, val.InternString("kind"), val.Int64Val(2))
			val.SetAttrCtx(ctx, pInst, val.InternString("default"), EmptyObject)
			val.SetAttrCtx(ctx, pInst, val.InternString("annotation"), EmptyObject)
			paramDict.Set(name, pInst)
			idx++
		}

		// 4. Keyword-only params
		for i := 0; i < kwonly && idx < len(varnames); i++ {
			name := varnames[idx]
			dflt := EmptyObject
			if kwdefaults != nil {
				if d, ok := kwdefaults.Get(name); ok {
					dflt = d
				}
			}
			pInst := val.NewInstanceOfType(ParameterType)
			val.SetAttrCtx(ctx, pInst, val.InternString("name"), name)
			val.SetAttrCtx(ctx, pInst, val.InternString("kind"), val.Int64Val(3))
			val.SetAttrCtx(ctx, pInst, val.InternString("default"), dflt)
			val.SetAttrCtx(ctx, pInst, val.InternString("annotation"), EmptyObject)
			paramDict.Set(name, pInst)
			idx++
		}

		// 5. Var-keyword (**kwargs)
		if hasVarKW && idx < len(varnames) {
			name := varnames[idx]
			pInst := val.NewInstanceOfType(ParameterType)
			val.SetAttrCtx(ctx, pInst, val.InternString("name"), name)
			val.SetAttrCtx(ctx, pInst, val.InternString("kind"), val.Int64Val(4))
			val.SetAttrCtx(ctx, pInst, val.InternString("default"), EmptyObject)
			val.SetAttrCtx(ctx, pInst, val.InternString("annotation"), EmptyObject)
			paramDict.Set(name, pInst)
			idx++
		}

		sigInst := val.NewInstanceOfType(SignatureType)
		val.SetAttrCtx(ctx, sigInst, val.InternString("parameters"), val.ObjectVal(&paramDict.Header))
		val.SetAttrCtx(ctx, sigInst, val.InternString("return_annotation"), EmptyObject)
		return sigInst
	}))

	return modVal
}

// cleanDoc mirrors inspect.cleandoc: expand tabs, strip the common
// indentation of lines after the first, and drop leading/trailing blank lines.
func cleanDoc(doc string) string {
	var sb strings.Builder
	col := 0
	for _, r := range doc {
		switch r {
		case '\t':
			n := 8 - col%8
			sb.WriteString(strings.Repeat(" ", n))
			col += n
		case '\n', '\r':
			sb.WriteRune(r)
			col = 0
		default:
			sb.WriteRune(r)
			col++
		}
	}
	lines := strings.Split(sb.String(), "\n")
	margin := math.MaxInt
	for _, l := range lines[1:] {
		trimmed := strings.TrimLeftFunc(l, unicode.IsSpace)
		if trimmed != "" {
			if ind := len(l) - len(trimmed); ind < margin {
				margin = ind
			}
		}
	}
	lines[0] = strings.TrimLeftFunc(lines[0], unicode.IsSpace)
	if margin < math.MaxInt {
		for i := 1; i < len(lines); i++ {
			if len(lines[i]) >= margin {
				lines[i] = lines[i][margin:]
			} else {
				lines[i] = ""
			}
		}
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}
