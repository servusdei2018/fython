package vm

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// populateAllBuiltins registers all 71 standard Python 3.14 builtins and exceptions into dict.
func (vm *VM) populateAllBuiltins(d *val.PyDict) {
	// Constants
	d.Set(val.InternString("True"), val.BoolVal(true))
	d.Set(val.InternString("False"), val.BoolVal(false))
	d.Set(val.InternString("None"), val.NoneVal())
	d.Set(val.InternString("NotImplemented"), val.NotImplementedVal())
	d.Set(val.InternString("Ellipsis"), val.EllipsisVal())
	d.Set(val.InternString("__debug__"), val.BoolVal(true))

	// Standard Types
	d.Set(val.InternString("int"), val.TypeVal(val.IntType))
	d.Set(val.InternString("float"), val.TypeVal(val.FloatType))
	d.Set(val.InternString("complex"), val.TypeVal(val.ComplexType))
	d.Set(val.InternString("str"), val.TypeVal(val.StrType))
	d.Set(val.InternString("list"), val.TypeVal(val.ListType))
	d.Set(val.InternString("dict"), val.TypeVal(val.DictType))
	d.Set(val.InternString("bool"), val.TypeVal(val.BoolType))
	d.Set(val.InternString("tuple"), val.TypeVal(val.TupleType))
	d.Set(val.InternString("object"), val.TypeVal(val.ObjectType))
	d.Set(val.InternString("bytes"), val.TypeVal(val.BytesType))
	d.Set(val.InternString("bytearray"), val.TypeVal(val.ByteArrayType))
	d.Set(val.InternString("memoryview"), val.TypeVal(val.MemoryViewType))
	d.Set(val.InternString("slice"), val.TypeVal(val.SliceType))
	d.Set(val.InternString("type"), val.TypeVal(val.TypeType))
	d.Set(val.InternString("set"), val.TypeVal(val.SetType))
	d.Set(val.InternString("frozenset"), val.TypeVal(val.SetType))
	d.Set(val.InternString("frozendict"), val.TypeVal(val.FrozenDictType))
	d.Set(val.InternString("sentinel"), val.TypeVal(val.SentinelType))
	d.Set(val.InternString("classmethod"), val.TypeVal(val.ClassMethodType))
	d.Set(val.InternString("staticmethod"), val.TypeVal(val.StaticMethodType))
	d.Set(val.InternString("property"), val.TypeVal(val.PropertyType))

	// PEP 695 Type Parameters
	d.Set(val.InternString("TypeVar"), val.TypeVal(val.TypeVarType))
	d.Set(val.InternString("TypeVarTuple"), val.TypeVal(val.TypeVarTupleType))
	d.Set(val.InternString("ParamSpec"), val.TypeVal(val.ParamSpecType))
	d.Set(val.InternString("TypeAliasType"), val.TypeVal(val.TypeAliasTypeType))

	// Exception Hierarchy
	d.Set(val.InternString("BaseException"), val.TypeVal(val.BaseExceptionType))
	d.Set(val.InternString("Exception"), val.TypeVal(val.ExceptionType))
	d.Set(val.InternString("TypeError"), val.TypeVal(val.TypeErrorType))
	d.Set(val.InternString("ValueError"), val.TypeVal(val.ValueErrorType))
	d.Set(val.InternString("KeyError"), val.TypeVal(val.KeyErrorType))
	d.Set(val.InternString("IndexError"), val.TypeVal(val.IndexErrorType))
	d.Set(val.InternString("ZeroDivisionError"), val.TypeVal(val.ZeroDivisionErrorType))
	d.Set(val.InternString("NameError"), val.TypeVal(val.NameErrorType))
	d.Set(val.InternString("AttributeError"), val.TypeVal(val.AttributeErrorType))
	d.Set(val.InternString("RuntimeError"), val.TypeVal(val.RuntimeErrorType))
	d.Set(val.InternString("StopIteration"), val.TypeVal(val.StopIterationType))
	d.Set(val.InternString("StopAsyncIteration"), val.TypeVal(val.StopAsyncIterationType))
	d.Set(val.InternString("GeneratorExit"), val.TypeVal(val.GeneratorExitType))
	d.Set(val.InternString("ImportError"), val.TypeVal(val.ImportErrorType))
	d.Set(val.InternString("ModuleNotFoundError"), val.TypeVal(val.ModuleNotFoundErrorType))
	d.Set(val.InternString("BufferError"), val.TypeVal(val.BufferErrorType))
	d.Set(val.InternString("ReferenceError"), val.TypeVal(val.ReferenceErrorType))
	d.Set(val.InternString("AssertionError"), val.TypeVal(val.AssertionErrorType))
	d.Set(val.InternString("BaseExceptionGroup"), val.TypeVal(val.BaseExceptionGroupType))
	d.Set(val.InternString("ExceptionGroup"), val.TypeVal(val.ExceptionGroupType))
	d.Set(val.InternString("ArithmeticError"), val.TypeVal(val.ArithmeticErrorType))
	d.Set(val.InternString("FloatingPointError"), val.TypeVal(val.FloatingPointErrorType))
	d.Set(val.InternString("OverflowError"), val.TypeVal(val.OverflowErrorType))
	d.Set(val.InternString("LookupError"), val.TypeVal(val.LookupErrorType))
	d.Set(val.InternString("OSError"), val.TypeVal(val.OSErrorType))
	d.Set(val.InternString("IOError"), val.TypeVal(val.IOErrorType))
	d.Set(val.InternString("EnvironmentError"), val.TypeVal(val.EnvironmentErrorType))
	d.Set(val.InternString("FileNotFoundError"), val.TypeVal(val.FileNotFoundErrorType))
	d.Set(val.InternString("PermissionError"), val.TypeVal(val.PermissionErrorType))
	d.Set(val.InternString("FileExistsError"), val.TypeVal(val.FileExistsErrorType))
	d.Set(val.InternString("IsADirectoryError"), val.TypeVal(val.IsADirectoryErrorType))
	d.Set(val.InternString("NotADirectoryError"), val.TypeVal(val.NotADirectoryErrorType))
	d.Set(val.InternString("InterruptedError"), val.TypeVal(val.InterruptedErrorType))
	d.Set(val.InternString("ProcessLookupError"), val.TypeVal(val.ProcessLookupErrorType))
	d.Set(val.InternString("TimeoutError"), val.TypeVal(val.TimeoutErrorType))
	d.Set(val.InternString("ConnectionError"), val.TypeVal(val.ConnectionErrorType))
	d.Set(val.InternString("BrokenPipeError"), val.TypeVal(val.BrokenPipeErrorType))
	d.Set(val.InternString("ConnectionAbortedError"), val.TypeVal(val.ConnectionAbortedErrorType))
	d.Set(val.InternString("ConnectionRefusedError"), val.TypeVal(val.ConnectionRefusedErrorType))
	d.Set(val.InternString("ConnectionResetError"), val.TypeVal(val.ConnectionResetErrorType))
	d.Set(val.InternString("BlockingIOError"), val.TypeVal(val.BlockingIOErrorType))
	d.Set(val.InternString("ChildProcessError"), val.TypeVal(val.ChildProcessErrorType))
	d.Set(val.InternString("UnboundLocalError"), val.TypeVal(val.UnboundLocalErrorType))
	d.Set(val.InternString("NotImplementedError"), val.TypeVal(val.NotImplementedErrorType))
	d.Set(val.InternString("RecursionError"), val.TypeVal(val.RecursionErrorType))
	d.Set(val.InternString("SyntaxError"), val.TypeVal(val.SyntaxErrorType))
	d.Set(val.InternString("IndentationError"), val.TypeVal(val.IndentationErrorType))
	d.Set(val.InternString("TabError"), val.TypeVal(val.TabErrorType))
	d.Set(val.InternString("SystemError"), val.TypeVal(val.SystemErrorType))
	d.Set(val.InternString("SystemExit"), val.TypeVal(val.SystemExitType))
	d.Set(val.InternString("KeyboardInterrupt"), val.TypeVal(val.KeyboardInterruptType))
	d.Set(val.InternString("EOFError"), val.TypeVal(val.EOFErrorType))
	d.Set(val.InternString("MemoryError"), val.TypeVal(val.MemoryErrorType))
	d.Set(val.InternString("UnicodeError"), val.TypeVal(val.UnicodeErrorType))
	d.Set(val.InternString("UnicodeDecodeError"), val.TypeVal(val.UnicodeDecodeErrorType))
	d.Set(val.InternString("UnicodeEncodeError"), val.TypeVal(val.UnicodeEncodeErrorType))
	d.Set(val.InternString("UnicodeTranslateError"), val.TypeVal(val.UnicodeTranslateErrorType))

	// Warnings
	d.Set(val.InternString("Warning"), val.TypeVal(val.WarningType))
	d.Set(val.InternString("UserWarning"), val.TypeVal(val.UserWarningType))
	d.Set(val.InternString("DeprecationWarning"), val.TypeVal(val.DeprecationWarningType))
	d.Set(val.InternString("PendingDeprecationWarning"), val.TypeVal(val.PendingDeprecationWarningType))
	d.Set(val.InternString("SyntaxWarning"), val.TypeVal(val.SyntaxWarningType))
	d.Set(val.InternString("RuntimeWarning"), val.TypeVal(val.RuntimeWarningType))
	d.Set(val.InternString("FutureWarning"), val.TypeVal(val.FutureWarningType))
	d.Set(val.InternString("ImportWarning"), val.TypeVal(val.ImportWarningType))
	d.Set(val.InternString("UnicodeWarning"), val.TypeVal(val.UnicodeWarningType))
	d.Set(val.InternString("BytesWarning"), val.TypeVal(val.BytesWarningType))
	d.Set(val.InternString("ResourceWarning"), val.TypeVal(val.ResourceWarningType))
	d.Set(val.InternString("EncodingWarning"), val.TypeVal(val.EncodingWarningType))

	// Builtin Functions
	d.Set(val.InternString("abs"), val.BuiltinAbsVal)

	// len
	d.Set(val.InternString("len"), val.NewBuiltinFuncVal("len", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: len() takes exactly one argument")
		}
		a := args[0]
		if a.IsList() {
			return val.Int64Val(int64(len(a.AsList().Items)))
		}
		if a.IsTuple() {
			return val.Int64Val(int64(len(a.AsTuple().Items)))
		}
		if a.IsDict() {
			return val.Int64Val(int64(a.AsDict().Len()))
		}
		if a.IsAnyString() {
			return val.Int64Val(int64(a.StringLen()))
		}
		if a.IsBytes() {
			return val.Int64Val(int64(len(a.AsBytes().Data)))
		}
		if a.IsByteArray() {
			return val.Int64Val(int64(len(a.AsByteArray().Data)))
		}
		if a.IsMemoryView() {
			return val.Int64Val(int64(len(a.AsMemoryView().Buf)))
		}
		if a.IsSet() {
			return val.Int64Val(int64(a.AsSet().Len()))
		}
		if a.IsMappingProxy() {
			mp := a.AsMappingProxy()
			if mp.Mapping.IsDict() {
				return val.Int64Val(int64(mp.Mapping.AsDict().Len()))
			}
		}
		if sqLen := a.Type().Slots.SqLen; sqLen != nil {
			return val.Int64Val(sqLen(ctx, a))
		}
		panic("TypeError: object of type '" + a.Type().Name + "' has no len()")
	}))

	// print(*args, sep=" ", end="\n", file=None, flush=False)
	d.Set(val.InternString("print"), val.NewBuiltinFuncVal("print", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		sep := " "
		end := "\n"
		file := val.NoneVal()
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				v := args[kwStart+i]
				switch kn.AsString() {
				case "sep":
					if v.IsAnyString() {
						sep = v.AsString()
					} else if !v.IsNone() {
						panic("TypeError: sep must be None or a string, not " + typeNameOf(v))
					}
				case "end":
					if v.IsAnyString() {
						end = v.AsString()
					} else if !v.IsNone() {
						panic("TypeError: end must be None or a string, not " + typeNameOf(v))
					}
				case "file":
					file = v
				case "flush":
				default:
					panic("TypeError: print() got an unexpected keyword argument '" + kn.AsString() + "'")
				}
			}
			args = args[:kwStart]
		}
		var sb strings.Builder
		for i, arg := range args {
			if i > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(val.PyStr(ctx, arg))
		}
		sb.WriteString(end)
		if !file.IsNone() {
			write := val.GetAttrCtx(ctx, file, val.InternString("write"))
			ctx.Invoke1(write, val.NewStringVal(sb.String()))
			return val.NoneVal()
		}
		fmt.Print(sb.String())
		return val.NoneVal()
	}))

	for _, t := range []*val.PyType{val.ListType, val.TupleType, val.DictType, val.SetType, val.BytesType, val.ByteArrayType, val.MappingProxyType} {
		val.InstallNativeRepr(t)
	}

	// dict.fromkeys(iterable, value=None)
	val.DictType.Dict.Set(val.InternString("fromkeys"), val.NewClassMethodVal(val.NewBuiltinFuncVal("fromkeys", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		args = posArgs(args, kwnames)
		if len(args) < 2 || len(args) > 3 {
			panic("TypeError: fromkeys expected 1 or 2 arguments")
		}
		fill := val.NoneVal()
		if len(args) == 3 {
			fill = args[2]
		}
		it := iterFor(ctx, args[1])
		if args[0].IsType() && args[0].AsType() == val.DictType {
			out := val.NewDictVal(8)
			d := out.AsDict()
			for {
				k, ok := it.Next()
				if !ok {
					return out
				}
				d.Set(k, fill)
			}
		}
		out := ctx.Invoke0(args[0])
		for {
			k, ok := it.Next()
			if !ok {
				return out
			}
			val.SetItemCtx(ctx, out, k, fill)
		}
	})))
	val.DictType.InvalidateVersion()

	// isinstance & issubclass
	d.Set(val.InternString("isinstance"), val.NewBuiltinFuncVal("isinstance", builtinIsInstance))
	d.Set(val.InternString("issubclass"), val.NewBuiltinFuncVal("issubclass", builtinIsSubclass))

	// callable
	d.Set(val.InternString("callable"), val.NewBuiltinFuncVal("callable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: callable() takes exactly one argument")
		}
		obj := args[0]
		tp := obj.Type()
		if tp.Slots.TpCall != nil {
			return val.BoolVal(true)
		}
		if _, _, ok := tp.LookupMRO(val.DunderCall); ok {
			return val.BoolVal(true)
		}
		return val.BoolVal(false)
	}))

	// iter(object[, sentinel])
	d.Set(val.InternString("iter"), val.NewBuiltinFuncVal("iter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: iter() expected at least 1 argument, got 0")
		}
		if len(args) == 1 {
			obj := args[0]
			if obj.IsIter() {
				return obj
			}
			if tpIter := obj.Type().Slots.TpIter; tpIter != nil {
				return tpIter(ctx, obj)
			}
			return newIterChecked(obj)
		}
		fn := args[0]
		sentinel := args[1]
		exhausted := false
		return val.NewIterValWithNext(func() (val.Value, bool) {
			if exhausted {
				return val.NoneVal(), false
			}
			res := ctx.Call(fn, nil, nil)
			if val.RichCompare(ctx, res, sentinel, val.CmpEQ).IsTruthy() {
				exhausted = true
				return val.NoneVal(), false
			}
			return res, true
		})
	}))

	// next(iterator[, default])
	d.Set(val.InternString("next"), val.NewBuiltinFuncVal("next", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: next() expected at least 1 argument, got 0")
		}
		it := args[0]
		hasDefault := len(args) > 1
		var defVal val.Value
		if hasDefault {
			defVal = args[1]
		}

		if it.IsIter() {
			if nextVal, ok := it.AsIter().Next(); ok {
				return nextVal
			}
			if hasDefault {
				return defVal
			}
			panic(val.NewException(val.StopIterationType, "StopIteration"))
		}

		if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
			nextVal, status := tpIterNext(ctx, it)
			if status == val.IterYielded {
				return nextVal
			}
			if status == val.IterExhausted {
				if hasDefault {
					return defVal
				}
				panic(val.NewException(val.StopIterationType, "StopIteration"))
			}
			if status == val.IterError {
				if ctx != nil && ctx.HasException() {
					panic(ctx.ActiveException)
				}
			}
			return nextVal
		}

		if nextMeth, _, ok := it.Type().LookupMRO(val.InternString("__next__")); ok {
			var nextVal val.Value
			var exhausted bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						if exc, ok := r.(val.Value); ok && val.IsInstance(exc, val.StopIterationType) {
							exhausted = true
							return
						}
						panic(r)
					}
				}()
				nextVal = ctx.Invoke1(nextMeth, it)
				if ctx != nil && ctx.HasException() && val.IsInstance(ctx.ActiveException, val.StopIterationType) {
					ctx.ClearException()
					exhausted = true
				}
			}()
			if exhausted {
				if hasDefault {
					return defVal
				}
				panic(val.NewException(val.StopIterationType, "StopIteration"))
			}
			return nextVal
		}

		if hasDefault {
			return defVal
		}
		panic(val.NewException(val.TypeErrorType, "'"+it.Type().Name+"' object is not an iterator"))
	}))

	// hasattr, getattr, setattr, delattr
	d.Set(val.InternString("getattr"), val.NewBuiltinFuncVal("getattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: getattr(): not enough arguments")
		}
		obj := args[0]
		name := args[1].AsString()
		hasDefault := len(args) > 2
		defVal := val.NoneVal()
		if hasDefault {
			defVal = args[2]
		}
		res, ok := vmGetAttrSafe(ctx, obj, name)
		if ok {
			return res
		}
		if hasDefault {
			return defVal
		}
		panic("AttributeError: '" + obj.Type().Name + "' object has no attribute '" + name + "'")
	}))

	d.Set(val.InternString("hasattr"), val.NewBuiltinFuncVal("hasattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: hasattr(): not enough arguments")
		}
		_, ok := vmGetAttrSafe(ctx, args[0], args[1].AsString())
		return val.BoolVal(ok)
	}))

	d.Set(val.InternString("setattr"), val.NewBuiltinFuncVal("setattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: setattr(): not enough arguments")
		}
		val.SetAttrCtx(ctx, args[0], val.InternString(args[1].AsString()), args[2])
		if ctx != nil && ctx.HasException() {
			exc := ctx.ActiveException
			ctx.ClearException()
			panic(exc)
		}
		return val.NoneVal()
	}))

	d.Set(val.InternString("delattr"), val.NewBuiltinFuncVal("delattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: delattr(): not enough arguments")
		}
		val.DelAttrCtx(ctx, args[0], val.InternString(args[1].AsString()))
		if ctx != nil && ctx.HasException() {
			exc := ctx.ActiveException
			ctx.ClearException()
			panic(exc)
		}
		return val.NoneVal()
	}))

	// id & hash
	d.Set(val.InternString("id"), val.NewBuiltinFuncVal("id", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: id() takes exactly one argument")
		}
		obj := args[0]
		ptrVal := int64(uintptr(unsafe.Pointer(obj.HeaderPtr())))
		return val.Int64Val(ptrVal)
	}))

	d.Set(val.InternString("hash"), val.NewBuiltinFuncVal("hash", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: hash() takes exactly one argument")
		}
		obj := args[0]
		if obj.IsInt64() {
			return obj
		}
		if obj.IsBool() {
			if obj.AsBool() {
				return val.Int64Val(1)
			}
			return val.Int64Val(0)
		}
		if obj.IsAnyString() || obj.IsFloat64() || obj.IsNone() || obj.Kind() == val.KindBigInt || obj.IsBytes() {
			return val.Int64Val(int64(val.HashValue(obj)))
		}
		if obj.IsTuple() && obj.Type() == val.TupleType {
			return val.Int64Val(int64(val.HashValue(obj)))
		}
		tp := obj.Type()
		if hashFn, owner, ok := tp.LookupMRO(val.InternString("__hash__")); ok {
			if hashFn.IsNone() {
				panic(val.NewTypeError("unhashable type: '" + tp.Name + "'"))
			}
			if owner != val.ObjectType {
				return ctx.Invoke1(hashFn, obj)
			}
		}
		if obj.IsTuple() {
			return val.Int64Val(int64(val.HashValue(obj)))
		}
		if obj.IsList() || obj.IsDict() || obj.IsSet() || obj.IsByteArray() {
			panic(val.NewTypeError("unhashable type: '" + tp.Name + "'"))
		}
		ptrVal := int64(uintptr(unsafe.Pointer(obj.HeaderPtr())))
		return val.Int64Val(ptrVal)
	}))

	// repr & ascii
	d.Set(val.InternString("repr"), val.NewBuiltinFuncVal("repr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: repr() takes exactly one argument")
		}
		return val.NewStringVal(val.PyRepr(ctx, args[0]))
	}))

	d.Set(val.InternString("ascii"), val.NewBuiltinFuncVal("ascii", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ascii() takes exactly one argument")
		}
		r := args[0].String()
		var sb strings.Builder
		for _, runeVal := range r {
			if runeVal < 128 && unicode.IsPrint(runeVal) {
				sb.WriteRune(runeVal)
			} else if runeVal <= 0xFF {
				sb.WriteString(fmt.Sprintf("\\x%02x", runeVal))
			} else if runeVal <= 0xFFFF {
				sb.WriteString(fmt.Sprintf("\\u%04x", runeVal))
			} else {
				sb.WriteString(fmt.Sprintf("\\U%08x", runeVal))
			}
		}
		return val.NewStringVal(sb.String())
	}))

	// format
	d.Set(val.InternString("format"), val.NewBuiltinFuncVal("format", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: format() takes at least 1 argument")
		}
		obj := args[0]
		if formatFn, _, ok := obj.Type().LookupMRO(val.InternString("__format__")); ok {
			spec := val.NewStringVal("")
			if len(args) > 1 {
				spec = args[1]
			}
			return ctx.Invoke2(formatFn, obj, spec)
		}
		return val.NewStringVal(obj.String())
	}))

	getBuiltinIter := func(ctx *val.ExecContext, o val.Value) *val.PyIter {
		if o.IsIter() {
			return o.AsIter()
		}
		return iterFor(ctx, o)
	}

	// all & any
	d.Set(val.InternString("all"), val.NewBuiltinFuncVal("all", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: all() takes exactly one argument")
		}
		it := getBuiltinIter(ctx, args[0])
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			if !item.IsTruthy() {
				return val.BoolVal(false)
			}
		}
		return val.BoolVal(true)
	}))

	d.Set(val.InternString("any"), val.NewBuiltinFuncVal("any", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: any() takes exactly one argument")
		}
		it := getBuiltinIter(ctx, args[0])
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			if item.IsTruthy() {
				return val.BoolVal(true)
			}
		}
		return val.BoolVal(false)
	}))

	// min, max, sum
	d.Set(val.InternString("sum"), val.NewBuiltinFuncVal("sum", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: sum() takes at least 1 argument")
		}
		start := val.Int64Val(0)
		if len(args) > 1 {
			start = args[1]
		}
		total := start
		it := iterFor(ctx, args[0])
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			total = val.AddCtx(ctx, total, item)
		}
		return total
	}))

	minMax := func(name string, op uint8) val.Value {
		return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pos := posArgs(args, kwnames)
			keyFn, hasKey := kwArg(args, kwnames, "key")
			if hasKey && keyFn.IsNone() {
				hasKey = false
			}
			defVal, hasDef := kwArg(args, kwnames, "default")
			for _, kn := range kwnames {
				if n := kn.AsString(); n != "key" && n != "default" {
					panic("TypeError: " + name + "() got an unexpected keyword argument '" + n + "'")
				}
			}
			if len(pos) == 0 {
				panic("TypeError: " + name + " expected at least 1 argument, got 0")
			}
			var items []val.Value
			if len(pos) == 1 {
				it := iterFor(ctx, pos[0])
				for {
					v, ok := it.Next()
					if !ok {
						break
					}
					items = append(items, v)
				}
			} else {
				if hasDef {
					panic("TypeError: Cannot specify a default for " + name + "() with multiple positional arguments")
				}
				items = pos
			}
			if len(items) == 0 {
				if hasDef {
					return defVal
				}
				panic("ValueError: " + name + "() iterable argument is empty")
			}
			keyOf := func(v val.Value) val.Value {
				if hasKey {
					return ctx.Call(keyFn, []val.Value{v}, nil)
				}
				return v
			}
			best := items[0]
			bestKey := keyOf(best)
			for _, item := range items[1:] {
				k := keyOf(item)
				if val.RichCompare(ctx, k, bestKey, op).IsTruthy() {
					best, bestKey = item, k
				}
			}
			return best
		})
	}
	d.Set(val.InternString("min"), minMax("min", val.CmpLT))
	d.Set(val.InternString("max"), minMax("max", val.CmpGT))

	// enumerate & zip & reversed & sorted
	d.Set(val.InternString("enumerate"), val.NewBuiltinFuncVal("enumerate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: enumerate() takes at least 1 argument")
		}
		start := int64(0)
		if len(args) > 1 && args[1].IsInt64() {
			start = args[1].AsInt64()
		}
		it := iterFor(ctx, args[0])
		var tuples []val.Value
		idx := start
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			tuples = append(tuples, val.NewTupleVal(val.Int64Val(idx), item))
			idx++
		}
		return val.NewIterVal(val.NewListVal(tuples...))
	}))

	d.Set(val.InternString("zip"), val.NewBuiltinFuncVal("zip", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		strict := false
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				if kn.AsString() != "strict" {
					panic("TypeError: zip() got an unexpected keyword argument '" + kn.AsString() + "'")
				}
				strict = args[kwStart+i].IsTruthy()
			}
			args = args[:kwStart]
		}
		if len(args) == 0 {
			return val.NewIterVal(val.NewListVal())
		}
		iters := make([]*val.PyIter, len(args))
		for i, a := range args {
			iters[i] = iterFor(ctx, a)
		}
		done := false
		return val.NewIterValWithNext(func() (val.Value, bool) {
			if done {
				return val.NoneVal(), false
			}
			items := make([]val.Value, len(iters))
			for i, it := range iters {
				v, ok := it.Next()
				if !ok {
					done = true
					if strict {
						if i > 0 {
							plural := ""
							if i > 1 {
								plural = "s 1-"
							} else {
								plural = " "
							}
							panic("ValueError: zip() argument " + strconv.Itoa(i+1) + " is shorter than argument" + plural + strconv.Itoa(i))
						}
						for j := 1; j < len(iters); j++ {
							if _, more := iters[j].Next(); more {
								plural := " "
								if j > 1 {
									plural = "s 1-"
								}
								panic("ValueError: zip() argument " + strconv.Itoa(j+1) + " is longer than argument" + plural + strconv.Itoa(j))
							}
						}
					}
					return val.NoneVal(), false
				}
				items[i] = v
			}
			return val.NewTupleVal(items...), true
		})
	}))

	d.Set(val.InternString("reversed"), val.NewBuiltinFuncVal("reversed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: reversed() takes exactly one argument")
		}
		a := args[0]
		if a.IsList() {
			items := a.AsList().Items
			rev := make([]val.Value, len(items))
			for i, v := range items {
				rev[len(items)-1-i] = v
			}
			return val.NewIterVal(val.NewListVal(rev...))
		}
		if a.IsTuple() {
			items := a.AsTuple().Items
			rev := make([]val.Value, len(items))
			for i, v := range items {
				rev[len(items)-1-i] = v
			}
			return val.NewIterVal(val.NewTupleVal(rev...))
		}
		panic("TypeError: object is not reversible")
	}))

	d.Set(val.InternString("sorted"), val.NewBuiltinFuncVal("sorted", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: sorted() takes at least 1 argument")
		}
		it := iterFor(ctx, args[0])
		var items []val.Value
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			items = append(items, v)
		}
		var keyFunc val.Value
		reverse := false
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				if kn.AsString() == "reverse" {
					reverse = args[kwStart+i].IsTruthy()
				} else if kn.AsString() == "key" {
					keyFunc = args[kwStart+i]
				}
			}
		}

		if keyFunc.HeaderPtr() != nil && !keyFunc.IsNone() {
			type sortedItem struct {
				val val.Value
				key val.Value
			}
			pairs := make([]sortedItem, len(items))
			for i, itm := range items {
				pairs[i] = sortedItem{val: itm, key: ctx.Invoke1(keyFunc, itm)}
			}
			sort.SliceStable(pairs, func(i, j int) bool {
				lt := val.RichCompare(ctx, pairs[i].key, pairs[j].key, val.CmpLT).IsTruthy()
				if reverse {
					return !lt && !val.RichCompare(ctx, pairs[i].key, pairs[j].key, val.CmpEQ).IsTruthy()
				}
				return lt
			})
			res := make([]val.Value, len(items))
			for i, p := range pairs {
				res[i] = p.val
			}
			return val.NewListVal(res...)
		}

		sort.SliceStable(items, func(i, j int) bool {
			lt := val.RichCompare(ctx, items[i], items[j], val.CmpLT).IsTruthy()
			if reverse {
				return !lt && !val.RichCompare(ctx, items[i], items[j], val.CmpEQ).IsTruthy()
			}
			return lt
		})
		return val.NewListVal(items...)
	}))

	// map & filter
	d.Set(val.InternString("map"), val.NewBuiltinFuncVal("map", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: map() must have at least two arguments")
		}
		fn := args[0]
		it := iterFor(ctx, args[1])
		var results []val.Value
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			results = append(results, ctx.Invoke1(fn, item))
		}
		return val.NewIterVal(val.NewListVal(results...))
	}))

	d.Set(val.InternString("filter"), val.NewBuiltinFuncVal("filter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: filter expected 2 arguments")
		}
		fn := args[0]
		it := iterFor(ctx, args[1])
		var results []val.Value
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			if fn.IsNone() {
				if item.IsTruthy() {
					results = append(results, item)
				}
			} else {
				if ctx.Invoke1(fn, item).IsTruthy() {
					results = append(results, item)
				}
			}
		}
		return val.NewIterVal(val.NewListVal(results...))
	}))

	// numbers: bin, hex, oct, ord, chr, divmod, round, pow
	d.Set(val.InternString("bin"), val.NewBuiltinFuncVal("bin", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: bin() takes exactly one argument")
		}
		if val.IsAnyInt(args[0]) {
			bi := val.ToBigInt(args[0])
			if bi.Sign() < 0 {
				return val.NewStringVal("-0b" + new(big.Int).Abs(bi).Text(2))
			}
			return val.NewStringVal("0b" + bi.Text(2))
		}
		return val.NewStringVal(fmt.Sprintf("%#b", args[0].AsInt64()))
	}))

	d.Set(val.InternString("hex"), val.NewBuiltinFuncVal("hex", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: hex() takes exactly one argument")
		}
		if val.IsAnyInt(args[0]) {
			bi := val.ToBigInt(args[0])
			if bi.Sign() < 0 {
				return val.NewStringVal("-0x" + new(big.Int).Abs(bi).Text(16))
			}
			return val.NewStringVal("0x" + bi.Text(16))
		}
		return val.NewStringVal(fmt.Sprintf("%#x", args[0].AsInt64()))
	}))

	d.Set(val.InternString("oct"), val.NewBuiltinFuncVal("oct", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: oct() takes exactly one argument")
		}
		if val.IsAnyInt(args[0]) {
			bi := val.ToBigInt(args[0])
			if bi.Sign() < 0 {
				return val.NewStringVal("-0o" + new(big.Int).Abs(bi).Text(8))
			}
			return val.NewStringVal("0o" + bi.Text(8))
		}
		return val.NewStringVal(fmt.Sprintf("%#o", args[0].AsInt64()))
	}))

	d.Set(val.InternString("chr"), val.NewBuiltinFuncVal("chr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: chr() takes exactly one argument")
		}
		return val.NewStringVal(string(rune(args[0].AsInt64())))
	}))

	d.Set(val.InternString("ord"), val.NewBuiltinFuncVal("ord", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ord() takes exactly one argument")
		}
		s := args[0].AsString()
		if len([]rune(s)) != 1 {
			panic("TypeError: ord() expected a character, but string of length " + strconv.Itoa(len(s)) + " found")
		}
		return val.Int64Val(int64([]rune(s)[0]))
	}))

	d.Set(val.InternString("divmod"), val.NewBuiltinFuncVal("divmod", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: divmod expected 2 arguments")
		}
		a := args[0]
		b := args[1]
		q := val.FloorDiv(a, b)
		r := val.Mod(a, b)
		return val.NewTupleVal(q, r)
	}))

	d.Set(val.InternString("pow"), val.NewBuiltinFuncVal("pow", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: pow expected at least 2 arguments")
		}
		if len(args) > 2 && !args[2].IsNone() {
			base := val.ToBigInt(args[0])
			exp := val.ToBigInt(args[1])
			mod := val.ToBigInt(args[2])
			if mod.Sign() == 0 {
				panic("ValueError: pow() 3rd argument cannot be 0")
			}
			res := new(big.Int).Exp(base, exp, mod)
			if res.IsInt64() {
				return val.Int64Val(res.Int64())
			}
			return val.NewBigIntVal(res)
		}
		return val.Pow(args[0], args[1])
	}))

	d.Set(val.InternString("round"), val.NewBuiltinFuncVal("round", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: round() takes at least 1 argument")
		}
		num := args[0]
		digits := int64(0)
		if len(args) > 1 && args[1].IsInt64() {
			digits = args[1].AsInt64()
		}
		f := float64(0)
		if num.IsFloat64() {
			f = num.AsFloat64()
		} else if num.IsInt64() {
			if len(args) == 1 {
				return num
			}
			f = float64(num.AsInt64())
		}
		if digits == 0 && len(args) == 1 {
			return val.Int64Val(int64(math.Round(f)))
		}
		pow := math.Pow(10, float64(digits))
		rounded := math.Round(f*pow) / pow
		return val.Float64Val(rounded)
	}))

	// vars
	d.Set(val.InternString("vars"), val.NewBuiltinFuncVal("vars", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		if len(args) == 0 {
			frameIdx := vmInstance.activeFrameIdx
			if frameIdx >= 0 && frameIdx < len(vmInstance.frames) {
				fVal := vmInstance.materializeFrame(frameIdx)
				if fVal.IsFrame() {
					f := fVal.AsFrame()
					if !f.LocalsProxy.IsNone() {
						return f.LocalsProxy
					}
					proxy := &val.PyLocalsProxy{Frame: f}
					f.LocalsProxy = val.NewLocalsProxyVal(proxy)
					return f.LocalsProxy
				}
			}
			return val.DictVal(vmInstance.globals)
		}
		obj := args[0]
		if obj.IsModule() {
			return val.DictVal(obj.AsModule().Dict)
		}
		if obj.IsType() {
			return val.NewMappingProxyVal(val.DictVal(obj.AsType().Dict))
		}
		if obj.IsInstance() {
			inst := obj.AsInstance()
			if inst.Shape != nil && inst.Shape.Type != nil && (inst.Shape.Type.Flags&uint32(val.FlagForbidDynamicAttrs)) != 0 {
				if ctx != nil {
					ctx.Raise(val.NewTypeError("vars() argument must have __dict__ attribute"))
					return val.NoneVal()
				}
				panic("TypeError: vars() argument must have __dict__ attribute")
			}
			if inst.Shape == nil {
				return val.NewDictVal(0)
			}
			return val.NewInstanceDictProxy(inst)
		}
		if obj.IsDict() || obj.IsList() {
			if d := val.GetAttrCtx(ctx, obj, val.InternString("__dict__")); d.IsDict() {
				return d
			}
		}
		if ctx != nil {
			ctx.Raise(val.NewTypeError("vars() argument must have __dict__ attribute"))
			return val.NoneVal()
		}
		panic("TypeError: vars() argument must have __dict__ attribute")
	}))

	// Dynamic compile, eval, exec
	d.Set(val.InternString("compile"), val.NewBuiltinFuncVal("compile", builtinCompile))

	d.Set(val.InternString("eval"), val.NewBuiltinFuncVal("eval", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: eval() takes at least 1 argument")
		}
		vmInstance := ctx.VM.(*VM)
		expr := args[0]
		var co *CodeObject

		if expr.Kind() == val.KindCode {
			co = AsCodeObject(expr)
		} else if expr.IsAnyString() || expr.IsBytes() {
			var srcBytes []byte
			if expr.IsAnyString() {
				srcBytes = []byte(expr.AsString())
			} else {
				srcBytes = expr.AsBytes().Data
			}
			compiled, err := vmInstance.engine.CompileDynamic(srcBytes, "<string>", 1)
			if err != nil {
				panic("SyntaxError: " + err.Error())
			}
			co = compiled
		} else {
			panic("TypeError: eval() arg 1 must be a string, bytes or code object")
		}

		globalsDict := vmInstance.currentGlobals()
		if len(args) > 1 && !args[1].IsNone() {
			if args[1].IsDict() {
				globalsDict = args[1].AsDict()
			}
		}
		localsVal := val.NoneVal()
		if len(args) > 2 && !args[2].IsNone() {
			localsVal = args[2]
		} else if len(args) == 1 || args[1].IsNone() {
			localsVal = vmInstance.currentLocalsVal()
		}

		if _, ok := globalsDict.Get(val.InternString("__builtins__")); !ok {
			globalsDict.Set(val.InternString("__builtins__"), val.DictVal(vmInstance.builtins))
		}

		res := vmInstance.runNestedCode(co, globalsDict, localsVal, nil)
		if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
			exc := vmInstance.unhandledException
			vmInstance.unhandledException = val.NoneVal()
			panic(exc)
		}
		return res
	}))

	d.Set(val.InternString("exec"), val.NewBuiltinFuncVal("exec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: exec() takes at least 1 argument")
		}
		vmInstance := ctx.VM.(*VM)
		codeObj := args[0]
		var co *CodeObject

		if codeObj.Kind() == val.KindCode {
			co = AsCodeObject(codeObj)
		} else if codeObj.IsAnyString() || codeObj.IsBytes() {
			var srcBytes []byte
			if codeObj.IsAnyString() {
				srcBytes = []byte(codeObj.AsString())
			} else {
				srcBytes = codeObj.AsBytes().Data
			}
			compiled, err := vmInstance.engine.CompileDynamic(srcBytes, "<string>", 0)
			if err != nil {
				panic("SyntaxError: " + err.Error())
			}
			co = compiled
		} else {
			panic("TypeError: exec() arg 1 must be a string, bytes or code object")
		}

		globalsDict := vmInstance.currentGlobals()
		if len(args) > 1 && !args[1].IsNone() {
			if args[1].IsDict() {
				globalsDict = args[1].AsDict()
			}
		}
		localsVal := val.NoneVal()
		if len(args) > 2 && !args[2].IsNone() {
			localsVal = args[2]
		} else if len(args) == 1 || args[1].IsNone() {
			localsVal = vmInstance.currentLocalsVal()
		}

		if _, ok := globalsDict.Get(val.InternString("__builtins__")); !ok {
			globalsDict.Set(val.InternString("__builtins__"), val.DictVal(vmInstance.builtins))
		}

		vmInstance.runNestedCode(co, globalsDict, localsVal, nil)
		if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
			exc := vmInstance.unhandledException
			vmInstance.unhandledException = val.NoneVal()
			panic(exc)
		}
		return val.NoneVal()
	}))

	// __import__
	d.Set(val.InternString("__import__"), val.NewBuiltinFuncVal("__import__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: __import__() takes at least 1 argument")
		}
		name := args[0].AsString()
		level := 0
		var callerGlobals *val.PyDict
		var fromList []string

		if len(args) > 1 && args[1].IsDict() {
			callerGlobals = args[1].AsDict()
		}
		if len(args) > 3 {
			flVal := args[3]
			if flVal.IsList() {
				for _, item := range flVal.AsList().Items {
					if item.IsAnyString() {
						fromList = append(fromList, item.AsString())
					}
				}
			} else if flVal.IsTuple() {
				for _, item := range flVal.AsTuple().Items {
					if item.IsAnyString() {
						fromList = append(fromList, item.AsString())
					}
				}
			}
		}
		if len(args) > 4 && args[4].IsInt64() {
			level = int(args[4].AsInt64())
		}

		vmInstance := ctx.VM.(*VM)
		returnLeaf := len(fromList) > 0
		modVal, err := vmInstance.ImportModule(name, level, returnLeaf, callerGlobals)
		if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
			exc := vmInstance.unhandledException
			vmInstance.unhandledException = val.NoneVal()
			panic(exc)
		}
		if err != nil {
			panic("ModuleNotFoundError: " + err.Error())
		}

		if len(fromList) > 0 && modVal.IsModule() {
			for _, sym := range fromList {
				_, _ = vmInstance.ImportFrom(modVal, sym, callerGlobals)
				if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
					exc := vmInstance.unhandledException
					vmInstance.unhandledException = val.NoneVal()
					panic(exc)
				}
			}
		}

		return modVal
	}))

	// breakpoint & help
	d.Set(val.InternString("breakpoint"), val.NewBuiltinFuncVal("breakpoint", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	d.Set(val.InternString("help"), val.NewBuiltinFuncVal("help", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			docVal, _ := vmGetAttrSafe(ctx, args[0], "__doc__")
			if docVal.IsAnyString() {
				fmt.Println(docVal.AsString())
			}
		}
		return val.NoneVal()
	}))

	d.Set(val.InternString("input"), val.NewBuiltinFuncVal("input", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			fmt.Print(args[0].String())
		}
		var line string
		fmt.Scanln(&line)
		return val.NewStringVal(line)
	}))
}

func builtinIsInstance(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 2 {
		panic("TypeError: isinstance expected 2 arguments")
	}
	return val.BoolVal(checkIsInstance(ctx, args[0], args[1]))
}

func checkIsInstance(ctx *val.ExecContext, obj, target val.Value) bool {
	if target.IsTuple() {
		for _, item := range target.AsTuple().Items {
			if checkIsInstance(ctx, obj, item) {
				return true
			}
		}
		return false
	}
	targetType := target.Type()
	if fn, _, ok := targetType.LookupMRO(val.InternString("__instancecheck__")); ok {
		res := ctx.Invoke2(fn, target, obj)
		return res.IsTruthy()
	}
	if target.IsType() {
		if val.IsSubclass(obj.Type(), target.AsType()) {
			return true
		}
		// Support __class__ spoofing (e.g. unittest.mock.Mock)
		if obj.IsInstance() {
			if spoofedCls, ok := obj.AsInstance().GetAttr(val.InternString("__class__")); ok && spoofedCls.IsType() {
				return val.IsSubclass(spoofedCls.AsType(), target.AsType())
			}
		}
		return false
	}
	return false
}

func builtinIsSubclass(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 2 {
		panic("TypeError: issubclass expected 2 arguments")
	}
	return val.BoolVal(checkIsSubclass(ctx, args[0], args[1]))
}

func checkIsSubclass(ctx *val.ExecContext, cls, target val.Value) bool {
	if target.IsTuple() {
		for _, item := range target.AsTuple().Items {
			if checkIsSubclass(ctx, cls, item) {
				return true
			}
		}
		return false
	}
	targetType := target.Type()
	if fn, _, ok := targetType.LookupMRO(val.InternString("__subclasscheck__")); ok {
		res := ctx.Invoke2(fn, target, cls)
		return res.IsTruthy()
	}
	if !cls.IsType() {
		panic("TypeError: issubclass() arg 1 must be a class")
	}
	if target.IsType() {
		return val.IsSubclass(cls.AsType(), target.AsType())
	}
	return false
}

func vmGetAttrSafe(ctx *val.ExecContext, obj val.Value, name string) (res val.Value, ok bool) {
	interned := val.InternString(name)
	defer func() {
		if r := recover(); r != nil {
			exc := panicToPyException(r)
			if val.IsInstance(exc, val.AttributeErrorType) {
				res = val.NoneVal()
				ok = false
				return
			}
			panic(r)
		}
	}()
	v := val.GetAttrCtx(ctx, obj, interned)
	if ctx != nil && ctx.HasException() {
		exc := ctx.ActiveException
		ctx.ClearException()
		if val.IsInstance(exc, val.AttributeErrorType) {
			return val.NoneVal(), false
		}
		panic(exc)
	}
	return v, true
}

// iterFor returns a *val.PyIter over any Python iterable, honouring the
// __iter__/__next__ protocol for user-defined and native iterator objects.
func iterFor(ctx *val.ExecContext, o val.Value) *val.PyIter {
	if o.IsIter() {
		return o.AsIter()
	}
	if !o.IsGenerator() {
		if tpIter := o.Type().Slots.TpIter; tpIter != nil {
			o = tpIter(ctx, o)
			if o.IsIter() {
				return o.AsIter()
			}
		} else if iterFn, _, ok := o.Type().LookupMRO(val.InternString("__iter__")); ok && !iterFn.IsNone() && !isPrimitiveIterable(o) {
			o = ctx.Invoke1(iterFn, o)
			if o.IsIter() {
				return o.AsIter()
			}
		}
	}
	it := val.NewIterVal(o).AsIter()
	if it.Kind != val.IterKindInvalid {
		return it
	}
	tp := o.Type()
	if nextFn := tp.Slots.TpIterNext; nextFn != nil {
		return val.NewIterValWithNext(func() (val.Value, bool) {
			v, status := nextFn(ctx, o)
			if status == val.IterYielded {
				return v, true
			}
			if status == val.IterError && ctx.HasException() && val.IsInstance(ctx.ActiveException, val.StopIterationType) {
				ctx.ClearException()
			}
			return val.NoneVal(), false
		}).AsIter()
	}
	if nextFn, _, ok := tp.LookupMRO(val.InternString("__next__")); ok {
		return val.NewIterValWithNext(func() (val.Value, bool) {
			var v val.Value
			var done bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						if exc, ok := r.(val.Value); ok && val.IsInstance(exc, val.StopIterationType) {
							done = true
							return
						}
						panic(r)
					}
				}()
				v = ctx.Invoke1(nextFn, o)
				if ctx.HasException() && val.IsInstance(ctx.ActiveException, val.StopIterationType) {
					ctx.ClearException()
					done = true
				}
			}()
			if done {
				return val.NoneVal(), false
			}
			return v, true
		}).AsIter()
	}
	panic("TypeError: '" + tp.Name + "' object is not iterable")
}

func isPrimitiveIterable(o val.Value) bool {
	return o.IsList() || o.IsTuple() || o.IsDict() || o.IsSet() || o.IsRange() || o.IsAnyString()
}
