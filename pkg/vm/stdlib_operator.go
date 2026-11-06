package vm

import (
	"crypto/subtle"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createOperatorModule() val.Value {
	modVal := val.NewModuleVal("_operator", "")
	mod := modVal.AsModule()

	// Arithmetic
	mod.Dict.Set(val.InternString("add"), val.NewBuiltinFuncVal("add", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.AddCtx(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("sub"), val.NewBuiltinFuncVal("sub", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.SubCtx(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("mul"), val.NewBuiltinFuncVal("mul", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.MulCtx(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("truediv"), val.NewBuiltinFuncVal("truediv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.TrueDiv(args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("floordiv"), val.NewBuiltinFuncVal("floordiv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.FloorDiv(args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("mod"), val.NewBuiltinFuncVal("mod", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Mod(args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("pow"), val.NewBuiltinFuncVal("pow", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Pow(args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("neg"), val.NewBuiltinFuncVal("neg", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.SubCtx(ctx, val.Int64Val(0), args[0])
	}))
	mod.Dict.Set(val.InternString("pos"), val.NewBuiltinFuncVal("pos", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return args[0]
	}))
	mod.Dict.Set(val.InternString("abs"), val.BuiltinAbsVal)

	// Bitwise
	mod.Dict.Set(val.InternString("and_"), val.NewBuiltinFuncVal("and_", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BitAnd(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("or_"), val.NewBuiltinFuncVal("or_", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BitOr(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("xor"), val.NewBuiltinFuncVal("xor", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BitXor(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("invert"), val.NewBuiltinFuncVal("invert", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(^args[0].AsInt64())
	}))
	mod.Dict.Set(val.InternString("inv"), mod.Dict.Entries[len(mod.Dict.Entries)-1].Value)
	mod.Dict.Set(val.InternString("lshift"), val.NewBuiltinFuncVal("lshift", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.LShift(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("rshift"), val.NewBuiltinFuncVal("rshift", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RShift(ctx, args[0], args[1])
	}))

	// Truth / Logic
	mod.Dict.Set(val.InternString("not_"), val.NewBuiltinFuncVal("not_", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(!args[0].IsTruthy())
	}))
	mod.Dict.Set(val.InternString("truth"), val.NewBuiltinFuncVal("truth", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(args[0].IsTruthy())
	}))
	mod.Dict.Set(val.InternString("is_"), val.NewBuiltinFuncVal("is_", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(args[0].HeaderPtr() == args[1].HeaderPtr())
	}))
	mod.Dict.Set(val.InternString("is_not"), val.NewBuiltinFuncVal("is_not", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(args[0].HeaderPtr() != args[1].HeaderPtr())
	}))
	mod.Dict.Set(val.InternString("is_none"), val.NewBuiltinFuncVal("is_none", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(args[0].IsNone())
	}))
	mod.Dict.Set(val.InternString("is_not_none"), val.NewBuiltinFuncVal("is_not_none", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(!args[0].IsNone())
	}))

	// Comparisons
	mod.Dict.Set(val.InternString("eq"), val.NewBuiltinFuncVal("eq", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RichCompare(ctx, args[0], args[1], val.CmpEQ)
	}))
	mod.Dict.Set(val.InternString("ne"), val.NewBuiltinFuncVal("ne", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RichCompare(ctx, args[0], args[1], val.CmpNEQ)
	}))
	mod.Dict.Set(val.InternString("lt"), val.NewBuiltinFuncVal("lt", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RichCompare(ctx, args[0], args[1], val.CmpLT)
	}))
	mod.Dict.Set(val.InternString("le"), val.NewBuiltinFuncVal("le", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RichCompare(ctx, args[0], args[1], val.CmpLTE)
	}))
	mod.Dict.Set(val.InternString("gt"), val.NewBuiltinFuncVal("gt", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RichCompare(ctx, args[0], args[1], val.CmpGT)
	}))
	mod.Dict.Set(val.InternString("ge"), val.NewBuiltinFuncVal("ge", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.RichCompare(ctx, args[0], args[1], val.CmpGTE)
	}))

	// Sequence / Mapping
	mod.Dict.Set(val.InternString("getitem"), val.NewBuiltinFuncVal("getitem", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.GetItemCtx(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("setitem"), val.NewBuiltinFuncVal("setitem", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		val.SetItemCtx(ctx, args[0], args[1], args[2])
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("delitem"), val.NewBuiltinFuncVal("delitem", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[0].IsDict() {
			args[0].AsDict().Delete(args[1])
			return val.NoneVal()
		}
		if delFn, _, ok := args[0].Type().LookupMRO(val.InternString("__delitem__")); ok {
			return ctx.Invoke2(delFn, args[0], args[1])
		}
		panic("TypeError: 'delitem' unsupported on type " + args[0].Type().Name)
	}))
	mod.Dict.Set(val.InternString("concat"), val.NewBuiltinFuncVal("concat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.AddCtx(ctx, args[0], args[1])
	}))
	mod.Dict.Set(val.InternString("contains"), val.NewBuiltinFuncVal("contains", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		it := iterFor(ctx, args[0])
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			if val.RichCompare(ctx, v, args[1], val.CmpEQ).IsTruthy() {
				return val.BoolVal(true)
			}
		}
		return val.BoolVal(false)
	}))
	mod.Dict.Set(val.InternString("countOf"), val.NewBuiltinFuncVal("countOf", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		it := iterFor(ctx, args[0])
		count := int64(0)
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			if val.RichCompare(ctx, v, args[1], val.CmpEQ).IsTruthy() {
				count++
			}
		}
		return val.Int64Val(count)
	}))
	mod.Dict.Set(val.InternString("indexOf"), val.NewBuiltinFuncVal("indexOf", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		it := iterFor(ctx, args[0])
		idx := int64(0)
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			if val.RichCompare(ctx, v, args[1], val.CmpEQ).IsTruthy() {
				return val.Int64Val(idx)
			}
			idx++
		}
		panic("ValueError: sequence.index(x): x not in sequence")
	}))

	// In-place aliases
	for _, name := range []string{"add", "sub", "mul", "truediv", "floordiv", "mod", "pow", "and_", "or_", "xor", "lshift", "rshift", "concat"} {
		fn, _ := mod.Dict.Get(val.InternString(name))
		mod.Dict.Set(val.InternString("i"+name), fn)
	}

	// index & length_hint
	mod.Dict.Set(val.InternString("index"), val.NewBuiltinFuncVal("index", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[0].IsInt64() {
			return args[0]
		}
		if idxFn, _, ok := args[0].Type().LookupMRO(val.InternString("__index__")); ok {
			return ctx.Invoke1(idxFn, args[0])
		}
		panic("TypeError: 'index' expected integer")
	}))

	mod.Dict.Set(val.InternString("length_hint"), val.NewBuiltinFuncVal("length_hint", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		defVal := int64(0)
		if len(args) > 1 && args[1].IsInt64() {
			defVal = args[1].AsInt64()
		}
		if sqLen := args[0].Type().Slots.SqLen; sqLen != nil {
			return val.Int64Val(sqLen(ctx, args[0]))
		}
		return val.Int64Val(defVal)
	}))

	// _compare_digest(a, b)
	mod.Dict.Set(val.InternString("_compare_digest"), val.NewBuiltinFuncVal("_compare_digest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var b1, b2 []byte
		if args[0].IsAnyString() {
			b1 = []byte(args[0].AsString())
		} else if args[0].IsBytes() {
			b1 = args[0].AsBytes().Data
		}
		if args[1].IsAnyString() {
			b2 = []byte(args[1].AsString())
		} else if args[1].IsBytes() {
			b2 = args[1].AsBytes().Data
		}
		return val.BoolVal(subtle.ConstantTimeCompare(b1, b2) == 1)
	}))

	// itemgetter(*items)
	mod.Dict.Set(val.InternString("itemgetter"), val.NewBuiltinFuncVal("itemgetter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: itemgetter needs at least 1 item")
		}
		items := append([]val.Value(nil), args...)
		if len(items) == 1 {
			it := items[0]
			return val.NewBuiltinFuncVal("itemgetter", func(subCtx *val.ExecContext, subCallable val.Value, subArgs []val.Value, subKwnames []val.Value) val.Value {
				return val.GetItemCtx(subCtx, subArgs[0], it)
			})
		}
		return val.NewBuiltinFuncVal("itemgetter", func(subCtx *val.ExecContext, subCallable val.Value, subArgs []val.Value, subKwnames []val.Value) val.Value {
			res := make([]val.Value, len(items))
			for i, it := range items {
				res[i] = val.GetItemCtx(subCtx, subArgs[0], it)
			}
			return val.NewTupleVal(res...)
		})
	}))

	// attrgetter(*attrs)
	mod.Dict.Set(val.InternString("attrgetter"), val.NewBuiltinFuncVal("attrgetter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: attrgetter needs at least 1 attribute")
		}
		attrs := make([]string, len(args))
		for i, a := range args {
			attrs[i] = a.AsString()
		}
		resolveAttr := func(subCtx *val.ExecContext, obj val.Value, attr string) val.Value {
			parts := strings.Split(attr, ".")
			curr := obj
			for _, p := range parts {
				v, ok := vmGetAttrSafe(subCtx, curr, p)
				if !ok {
					panic("AttributeError: '" + curr.Type().Name + "' object has no attribute '" + p + "'")
				}
				curr = v
			}
			return curr
		}
		if len(attrs) == 1 {
			at := attrs[0]
			return val.NewBuiltinFuncVal("attrgetter", func(subCtx *val.ExecContext, subCallable val.Value, subArgs []val.Value, subKwnames []val.Value) val.Value {
				return resolveAttr(subCtx, subArgs[0], at)
			})
		}
		return val.NewBuiltinFuncVal("attrgetter", func(subCtx *val.ExecContext, subCallable val.Value, subArgs []val.Value, subKwnames []val.Value) val.Value {
			res := make([]val.Value, len(attrs))
			for i, at := range attrs {
				res[i] = resolveAttr(subCtx, subArgs[0], at)
			}
			return val.NewTupleVal(res...)
		})
	}))

	// methodcaller(name, *args, **kwargs)
	mod.Dict.Set(val.InternString("methodcaller"), val.NewBuiltinFuncVal("methodcaller", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: methodcaller needs a method name")
		}
		methodName := args[0].AsString()
		mArgs := append([]val.Value(nil), args[1:]...)
		mKwNames := append([]val.Value(nil), kwnames...)
		return val.NewBuiltinFuncVal("methodcaller", func(subCtx *val.ExecContext, subCallable val.Value, subArgs []val.Value, subKwnames []val.Value) val.Value {
			method, ok := vmGetAttrSafe(subCtx, subArgs[0], methodName)
			if !ok {
				panic("AttributeError: '" + subArgs[0].Type().Name + "' object has no attribute '" + methodName + "'")
			}
			return subCtx.Call(method, mArgs, mKwNames)
		})
	}))

	// call(obj, *args, **kwargs)
	mod.Dict.Set(val.InternString("call"), val.NewBuiltinFuncVal("call", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: call() takes at least 1 argument")
		}
		return ctx.Call(args[0], args[1:], kwnames)
	}))

	return modVal
}
