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
	d.Set(val.InternString("_IncompleteInputError"), val.TypeVal(val.IncompleteInputErrorType))
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
			return val.RaiseTypeError(ctx, "len() takes exactly one argument")
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
		return val.RaiseTypeError(ctx, "object of type '"+a.Type().Name+"' has no len()")
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
						return val.RaiseTypeError(ctx, "sep must be None or a string, not "+typeNameOf(v))
					}
				case "end":
					if v.IsAnyString() {
						end = v.AsString()
					} else if !v.IsNone() {
						return val.RaiseTypeError(ctx, "end must be None or a string, not "+typeNameOf(v))
					}
				case "file":
					file = v
				case "flush":
				default:
					return val.RaiseTypeError(ctx, "print() got an unexpected keyword argument '"+kn.AsString()+"'")
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
			write := val.GetAttrCtx(ctx, file, nameWrite)
			ctx.Invoke1(write, val.NewStringVal(sb.String()))
			return val.NoneVal()
		}
		if ctx != nil && ctx.VM != nil {
			if vmInstance, ok := ctx.VM.(*VM); ok {
				if sysVal, ok := vmInstance.modules.Get(nameSys); ok && sysVal.IsModule() {
					if stdoutVal, ok := sysVal.AsModule().Dict.Get(nameStdout); ok && !stdoutVal.IsNone() {
						write := val.GetAttrCtx(ctx, stdoutVal, nameWrite)
						ctx.Invoke1(write, val.NewStringVal(sb.String()))
						return val.NoneVal()
					}
				}
			}
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
			return val.RaiseTypeError(ctx, "fromkeys expected 1 or 2 arguments")
		}
		fill := val.NoneVal()
		if len(args) == 3 {
			fill = args[2]
		}
		it := iterFor(ctx, args[1])
		if it == nil {
			return val.NoneVal()
		}
		if args[0].IsType() && args[0].AsType() == val.DictType {
			out := val.NewDictVal(4)
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
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		for {
			k, ok := it.Next()
			if !ok {
				return out
			}
			val.SetItemCtx(ctx, out, k, fill)
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
		}
	})))
	val.DictType.InvalidateVersion()

	// isinstance & issubclass
	d.Set(val.InternString("isinstance"), val.NewBuiltinFuncVal("isinstance", builtinIsInstance))
	d.Set(val.InternString("issubclass"), val.NewBuiltinFuncVal("issubclass", builtinIsSubclass))

	// callable
	d.Set(val.InternString("callable"), val.NewBuiltinFuncVal("callable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "callable() takes exactly one argument")
		}
		return val.BoolVal(val.IsCallable(args[0]))
	}))

	// iter(object[, sentinel])
	d.Set(val.InternString("iter"), val.NewBuiltinFuncVal("iter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "iter() expected at least 1 argument, got 0")
		}
		if len(args) == 1 {
			obj := args[0]
			if obj.IsIter() {
				return obj
			}
			if tpIter := obj.Type().Slots.TpIter; tpIter != nil {
				return tpIter(ctx, obj)
			}
			return newIterCheckedCtx(ctx, obj)
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
			return val.RaiseTypeError(ctx, "next() expected at least 1 argument, got 0")
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
			if ctx != nil && ctx.HasException() {
				if val.IsInstance(ctx.ActiveException, val.StopIterationType) {
					ctx.ClearException()
				} else {
					return val.NoneVal()
				}
			}
			if hasDefault {
				return defVal
			}
			return val.RaiseStopIteration(ctx, val.NoneVal())
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
				return val.RaiseStopIteration(ctx, val.NoneVal())
			}
			if status == val.IterError {
				if ctx != nil && ctx.HasException() {
					if val.IsInstance(ctx.ActiveException, val.StopIterationType) && hasDefault {
						ctx.ClearException()
						return defVal
					}
					return val.NoneVal()
				}
			}
			return nextVal
		}

		if nextMeth, _, ok := it.Type().LookupMRO(nameNext); ok {
			nextVal := ctx.Invoke1(nextMeth, it)
			if ctx != nil && ctx.HasException() {
				if val.IsInstance(ctx.ActiveException, val.StopIterationType) {
					if hasDefault {
						ctx.ClearException()
						return defVal
					}
				}
				return val.NoneVal()
			}
			return nextVal
		}

		if hasDefault {
			return defVal
		}
		return val.RaiseTypeError(ctx, "'"+it.Type().Name+"' object is not an iterator")
	}))

	// hasattr, getattr, setattr, delattr
	d.Set(val.InternString("getattr"), val.NewBuiltinFuncVal("getattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewTypeError("getattr(): not enough arguments"))
			return val.NoneVal()
		}
		obj := args[0]
		hasDefault := len(args) > 2
		defVal := val.NoneVal()
		if hasDefault {
			defVal = args[2]
		}
		interned := val.InternValue(args[1])
		res, ok := val.LookupAttrOptional(ctx, obj, interned)
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		recordGetAttrCall(obj, interned, ok, hasDefault)
		if ok {
			return res
		}
		if hasDefault {
			return defVal
		}
		ctx.Raise(val.NewLazyAttributeErrorCtx(ctx, obj.Type(), interned))
		return val.NoneVal()
	}))

	d.Set(val.InternString("hasattr"), val.NewBuiltinFuncVal("hasattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewTypeError("hasattr(): not enough arguments"))
			return val.NoneVal()
		}
		interned := val.InternValue(args[1])
		ok := val.HasAttrFast(ctx, args[0], interned)
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		recordHasAttrCall(args[0], interned, ok)
		return val.BoolVal(ok)
	}))

	d.Set(val.InternString("setattr"), val.NewBuiltinFuncVal("setattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			return val.RaiseTypeError(ctx, "setattr(): not enough arguments")
		}
		val.SetAttrCtx(ctx, args[0], val.InternValue(args[1]), args[2])
		return val.NoneVal()
	}))

	d.Set(val.InternString("delattr"), val.NewBuiltinFuncVal("delattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			return val.RaiseTypeError(ctx, "delattr(): not enough arguments")
		}
		val.DelAttrCtx(ctx, args[0], val.InternValue(args[1]))
		return val.NoneVal()
	}))

	// id & hash
	d.Set(val.InternString("id"), val.NewBuiltinFuncVal("id", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "id() takes exactly one argument")
		}
		obj := args[0]
		ptrVal := int64(uintptr(unsafe.Pointer(obj.HeaderPtr())))
		return val.Int64Val(ptrVal)
	}))

	d.Set(val.InternString("hash"), val.NewBuiltinFuncVal("hash", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "hash() takes exactly one argument")
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
		if hashFn, owner, ok := tp.LookupMRO(nameHash); ok {
			if hashFn.IsNone() {
				return val.RaiseTypeError(ctx, "unhashable type: '"+tp.Name+"'")
			}
			if owner != val.ObjectType {
				return ctx.Invoke1(hashFn, obj)
			}
		}
		if obj.IsTuple() {
			return val.Int64Val(int64(val.HashValue(obj)))
		}
		if obj.IsList() || obj.IsDict() || obj.IsSet() || obj.IsByteArray() {
			return val.RaiseTypeError(ctx, "unhashable type: '"+tp.Name+"'")
		}
		ptrVal := int64(uintptr(unsafe.Pointer(obj.HeaderPtr())))
		return val.Int64Val(ptrVal)
	}))

	// repr & ascii
	d.Set(val.InternString("repr"), val.NewBuiltinFuncVal("repr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "repr() takes exactly one argument")
		}
		return val.NewStringVal(val.PyRepr(ctx, args[0]))
	}))

	d.Set(val.InternString("ascii"), val.NewBuiltinFuncVal("ascii", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "ascii() takes exactly one argument")
		}
		r := args[0].String()
		var sb strings.Builder
		for _, runeVal := range r {
			switch {
			case runeVal < 128 && unicode.IsPrint(runeVal):
				sb.WriteRune(runeVal)
			case runeVal <= 0xFF:
				fmt.Fprintf(&sb, "\\x%02x", runeVal)
			case runeVal <= 0xFFFF:
				fmt.Fprintf(&sb, "\\u%04x", runeVal)
			default:
				fmt.Fprintf(&sb, "\\U%08x", runeVal)
			}
		}
		return val.NewStringVal(sb.String())
	}))

	// format
	d.Set(val.InternString("format"), val.NewBuiltinFuncVal("format", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "format() takes at least 1 argument")
		}
		obj := args[0]
		if formatFn, _, ok := obj.Type().LookupMRO(nameFormat); ok {
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
			return val.RaiseTypeError(ctx, "all() takes exactly one argument")
		}
		arg := args[0]
		if arg.IsGenerator() {
			gen := arg.AsGenerator()
			for {
				v, status := gen.Send(ctx, val.NoneVal())
				if status == val.IterExhausted || status == val.IterError {
					if ctx != nil && ctx.HasException() {
						return val.NoneVal()
					}
					return val.BoolVal(true)
				}
				if !v.IsTruthy() {
					return val.BoolVal(false)
				}
			}
		}
		if arg.IsList() {
			for _, item := range arg.AsList().Items {
				if !item.IsTruthy() {
					return val.BoolVal(false)
				}
			}
			return val.BoolVal(true)
		}
		if arg.IsTuple() {
			for _, item := range arg.AsTuple().Items {
				if !item.IsTruthy() {
					return val.BoolVal(false)
				}
			}
			return val.BoolVal(true)
		}
		it := getBuiltinIter(ctx, arg)
		if it == nil {
			return val.NoneVal()
		}
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
			return val.RaiseTypeError(ctx, "any() takes exactly one argument")
		}
		arg := args[0]
		if arg.IsGenerator() {
			gen := arg.AsGenerator()
			for {
				v, status := gen.Send(ctx, val.NoneVal())
				if status == val.IterExhausted || status == val.IterError {
					if ctx != nil && ctx.HasException() {
						return val.NoneVal()
					}
					return val.BoolVal(false)
				}
				if v.IsTruthy() {
					return val.BoolVal(true)
				}
			}
		}
		if arg.IsList() {
			for _, item := range arg.AsList().Items {
				if item.IsTruthy() {
					return val.BoolVal(true)
				}
			}
			return val.BoolVal(false)
		}
		if arg.IsTuple() {
			for _, item := range arg.AsTuple().Items {
				if item.IsTruthy() {
					return val.BoolVal(true)
				}
			}
			return val.BoolVal(false)
		}
		it := getBuiltinIter(ctx, arg)
		if it == nil {
			return val.NoneVal()
		}
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
			return val.RaiseTypeError(ctx, "sum() takes at least 1 argument")
		}
		start := val.Int64Val(0)
		if len(args) > 1 {
			start = args[1]
		}
		total := start
		arg := args[0]
		if arg.IsGenerator() {
			gen := arg.AsGenerator()
			for {
				item, status := gen.Send(ctx, val.NoneVal())
				if status == val.IterExhausted || status == val.IterError {
					if ctx != nil && ctx.HasException() {
						return val.NoneVal()
					}
					return total
				}
				total = val.AddCtx(ctx, total, item)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
			}
		}
		if arg.IsList() {
			for _, item := range arg.AsList().Items {
				total = val.AddCtx(ctx, total, item)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
			}
			return total
		}
		if arg.IsTuple() {
			for _, item := range arg.AsTuple().Items {
				total = val.AddCtx(ctx, total, item)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
			}
			return total
		}
		it := iterFor(ctx, arg)
		if it == nil {
			return val.NoneVal()
		}
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			total = val.AddCtx(ctx, total, item)
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
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
					return val.RaiseTypeError(ctx, name+"() got an unexpected keyword argument '"+n+"'")
				}
			}
			if len(pos) == 0 {
				return val.RaiseTypeError(ctx, name+" expected at least 1 argument, got 0")
			}
			var buf [8]val.Value
			var items []val.Value
			if len(pos) == 1 {
				arg := pos[0]
				switch {
				case arg.IsList():
					items = arg.AsList().Items
				case arg.IsTuple():
					items = arg.AsTuple().Items
				case arg.IsGenerator():
					gen := arg.AsGenerator()
					items = buf[:0]
					for {
						v, status := gen.Send(ctx, val.NoneVal())
						if status == val.IterExhausted || status == val.IterError {
							if ctx != nil && ctx.HasException() {
								return val.NoneVal()
							}
							break
						}
						items = append(items, v)
					}
				default:
					it := iterFor(ctx, arg)
					if it == nil {
						return val.NoneVal()
					}
					items = buf[:0]
					for {
						v, ok := it.Next()
						if !ok {
							break
						}
						items = append(items, v)
					}
				}
			} else {
				if hasDef {
					return val.RaiseTypeError(ctx, "Cannot specify a default for "+name+"() with multiple positional arguments")
				}
				items = pos
			}
			if len(items) == 0 {
				if hasDef {
					return defVal
				}
				return val.RaiseValueError(ctx, name+"() iterable argument is empty")
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
			return val.RaiseTypeError(ctx, "enumerate() takes at least 1 argument")
		}
		start := int64(0)
		if len(args) > 1 && args[1].IsInt64() {
			start = args[1].AsInt64()
		}
		it := iterFor(ctx, args[0])
		if it == nil {
			return val.NoneVal()
		}
		return val.NewEnumerateIterVal(val.ObjectVal(&it.Header), start)
	}))

	d.Set(val.InternString("zip"), val.NewBuiltinFuncVal("zip", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		strict := false
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				if kn.AsString() != "strict" {
					return val.RaiseTypeError(ctx, "zip() got an unexpected keyword argument '"+kn.AsString()+"'")
				}
				strict = args[kwStart+i].IsTruthy()
			}
			args = args[:kwStart]
		}
		if len(args) == 0 {
			return val.NewIterVal(val.NewListVal())
		}
		subs := make([]val.Value, len(args))
		for i, a := range args {
			it := iterFor(ctx, a)
			if it == nil {
				return val.NoneVal()
			}
			subs[i] = val.ObjectVal(&it.Header)
		}
		return val.NewZipIterVal(subs, strict)
	}))

	d.Set(val.InternString("reversed"), val.NewBuiltinFuncVal("reversed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "reversed() takes exactly one argument")
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
		return val.RaiseTypeError(ctx, "object is not reversible")
	}))

	d.Set(val.InternString("sorted"), val.NewBuiltinFuncVal("sorted", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "sorted() takes at least 1 argument")
		}
		arg := args[0]
		var items []val.Value
		switch {
		case arg.IsList():
			src := arg.AsList().Items
			items = make([]val.Value, len(src))
			copy(items, src)
		case arg.IsTuple():
			src := arg.AsTuple().Items
			items = make([]val.Value, len(src))
			copy(items, src)
		case arg.IsGenerator():
			gen := arg.AsGenerator()
			for {
				v, status := gen.Send(ctx, val.NoneVal())
				if status == val.IterExhausted || status == val.IterError {
					if ctx != nil && ctx.HasException() {
						return val.NoneVal()
					}
					break
				}
				items = append(items, v)
			}
		default:
			it := iterFor(ctx, arg)
			if it == nil {
				return val.NoneVal()
			}
			for {
				v, ok := it.Next()
				if !ok {
					break
				}
				items = append(items, v)
			}
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
				k := ctx.Invoke1(keyFunc, itm)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				pairs[i] = sortedItem{val: itm, key: k}
			}
			sort.SliceStable(pairs, func(i, j int) bool {
				if ctx != nil && ctx.HasException() {
					return false
				}
				if reverse {
					return val.RichCompare(ctx, pairs[j].key, pairs[i].key, val.CmpLT).IsTruthy()
				}
				return val.RichCompare(ctx, pairs[i].key, pairs[j].key, val.CmpLT).IsTruthy()
			})
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			res := make([]val.Value, len(items))
			for i, p := range pairs {
				res[i] = p.val
			}
			return val.NewListVal(res...)
		}

		sort.SliceStable(items, func(i, j int) bool {
			if ctx != nil && ctx.HasException() {
				return false
			}
			if reverse {
				return val.RichCompare(ctx, items[j], items[i], val.CmpLT).IsTruthy()
			}
			return val.RichCompare(ctx, items[i], items[j], val.CmpLT).IsTruthy()
		})
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		return val.NewListVal(items...)
	}))

	// map & filter
	d.Set(val.InternString("map"), val.NewBuiltinFuncVal("map", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			return val.RaiseTypeError(ctx, "map() must have at least two arguments")
		}
		fn := args[0]
		if len(args) == 2 {
			it := iterFor(ctx, args[1])
			if it == nil {
				return val.NoneVal()
			}
			var results []val.Value
			for {
				item, ok := it.Next()
				if !ok {
					break
				}
				results = append(results, ctx.Invoke1(fn, item))
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
			}
			return val.NewIterVal(val.NewListVal(results...))
		}

		// Multiple iterables
		iters := make([]*val.PyIter, len(args)-1)
		for i := 1; i < len(args); i++ {
			it := iterFor(ctx, args[i])
			if it == nil {
				return val.NoneVal()
			}
			iters[i-1] = it
		}
		var results []val.Value
		callArgs := make([]val.Value, len(iters))
		for {
			exhausted := false
			for i := range iters {
				item, ok := iters[i].Next()
				if !ok {
					exhausted = true
					break
				}
				callArgs[i] = item
			}
			if exhausted {
				break
			}
			results = append(results, ctx.Call(fn, callArgs, nil))
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
		}
		return val.NewIterVal(val.NewListVal(results...))
	}))

	d.Set(val.InternString("filter"), val.NewBuiltinFuncVal("filter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			return val.RaiseTypeError(ctx, "filter expected 2 arguments")
		}
		fn := args[0]
		it := iterFor(ctx, args[1])
		if it == nil {
			return val.NoneVal()
		}
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
				res := ctx.Invoke1(fn, item)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if res.IsTruthy() {
					results = append(results, item)
				}
			}
		}
		return val.NewIterVal(val.NewListVal(results...))
	}))

	// numbers: bin, hex, oct, ord, chr, divmod, round, pow
	d.Set(val.InternString("bin"), val.NewBuiltinFuncVal("bin", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "bin() takes exactly one argument")
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
			return val.RaiseTypeError(ctx, "hex() takes exactly one argument")
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
			return val.RaiseTypeError(ctx, "oct() takes exactly one argument")
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
			return val.RaiseTypeError(ctx, "chr() takes exactly one argument")
		}
		return val.NewStringVal(string(rune(args[0].AsInt64())))
	}))

	d.Set(val.InternString("ord"), val.NewBuiltinFuncVal("ord", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "ord() takes exactly one argument")
		}
		s := args[0].AsString()
		if len([]rune(s)) != 1 {
			return val.RaiseTypeError(ctx, "ord() expected a character, but string of length "+strconv.Itoa(len(s))+" found")
		}
		return val.Int64Val(int64([]rune(s)[0]))
	}))

	d.Set(val.InternString("divmod"), val.NewBuiltinFuncVal("divmod", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			return val.RaiseTypeError(ctx, "divmod expected 2 arguments")
		}
		a := args[0]
		b := args[1]
		q := val.FloorDivCtx(ctx, a, b)
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		r := val.ModCtx(ctx, a, b)
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		return val.NewTupleVal(q, r)
	}))

	d.Set(val.InternString("pow"), val.NewBuiltinFuncVal("pow", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			return val.RaiseTypeError(ctx, "pow expected at least 2 arguments")
		}
		if len(args) > 2 && !args[2].IsNone() {
			base := val.ToBigInt(args[0])
			exp := val.ToBigInt(args[1])
			mod := val.ToBigInt(args[2])
			if mod.Sign() == 0 {
				return val.RaiseValueError(ctx, "pow() 3rd argument cannot be 0")
			}
			res := new(big.Int).Exp(base, exp, mod)
			if res.IsInt64() {
				return val.Int64Val(res.Int64())
			}
			return val.NewBigIntVal(res)
		}
		return val.PowCtx(ctx, args[0], args[1])
	}))

	d.Set(val.InternString("round"), val.NewBuiltinFuncVal("round", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "round() takes at least 1 argument")
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
			if inst.Shape != nil && inst.Shape.Type != nil && (inst.Shape.Type.Flags&val.FlagForbidDynamicAttrs) != 0 {
				if ctx != nil {
					ctx.Raise(val.NewTypeError("vars() argument must have __dict__ attribute"))
					return val.NoneVal()
				}
				ctx.Raise(val.NewException(val.TypeErrorType, "vars() argument must have __dict__ attribute"))
				return val.NoneVal()
			}
			if inst.Shape == nil {
				return val.NewDictVal(0)
			}
			return val.NewInstanceDictProxy(inst)
		}
		if obj.IsDict() || obj.IsList() {
			if d := val.GetAttrCtx(ctx, obj, nameDunderDict); d.IsDict() {
				return d
			}
		}
		if ctx != nil {
			ctx.Raise(val.NewTypeError("vars() argument must have __dict__ attribute"))
			return val.NoneVal()
		}
		ctx.Raise(val.NewException(val.TypeErrorType, "vars() argument must have __dict__ attribute"))
		return val.NoneVal()
	}))

	// Dynamic compile, eval, exec
	d.Set(val.InternString("compile"), val.NewBuiltinFuncVal("compile", builtinCompile))

	d.Set(val.InternString("eval"), val.NewBuiltinFuncVal("eval", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		npos := len(args) - len(kwnames)
		if npos < 1 && len(args) < 1 {
			return val.RaiseTypeError(ctx, "eval() takes at least 1 argument")
		}
		var expr val.Value
		globalsVal := val.NoneVal()
		localsVal := val.NoneVal()
		closureVal := val.NoneVal()

		if npos >= 1 {
			expr = args[0]
		}
		if npos >= 2 {
			globalsVal = args[1]
		}
		if npos >= 3 {
			localsVal = args[2]
		}
		if npos >= 4 {
			closureVal = args[3]
		}
		for i, kn := range kwnames {
			argV := args[npos+i]
			switch kn.AsString() {
			case "source", "expression":
				expr = argV
			case "globals":
				globalsVal = argV
			case "locals":
				localsVal = argV
			case "closure":
				closureVal = argV
			}
		}

		vmInstance := ctx.VM.(*VM)
		var co *CodeObject

		switch {
		case expr.Kind() == val.KindCode:
			co = AsCodeObject(expr)
		case expr.IsAnyString() || expr.IsBytes():
			var srcBytes []byte
			if expr.IsAnyString() {
				srcBytes = []byte(expr.AsString())
			} else {
				srcBytes = expr.AsBytes().Data
			}
			compiled, err := vmInstance.engine.CompileDynamic(srcBytes, "<string>", 1)
			if err != nil {
				return val.RaiseException(ctx, val.SyntaxErrorType, err.Error())
			}
			co = compiled
		default:
			return val.RaiseTypeError(ctx, "eval() arg 1 must be a string, bytes or code object")
		}

		var globalsDict *val.PyDict
		switch {
		case globalsVal.IsNone():
			globalsDict = vmInstance.currentGlobals()
			if localsVal.IsNone() {
				localsVal = vmInstance.currentLocalsVal()
			}
		case globalsVal.IsDict():
			globalsDict = globalsVal.AsDict()
			if localsVal.IsNone() {
				localsVal = globalsVal
			}
		default:
			return val.RaiseTypeError(ctx, "globals must be a dict")
		}

		if _, ok := globalsDict.Get(nameDunderBuiltins); !ok {
			if builtinsMod, ok := vmInstance.modules.Get(nameBuiltins); ok {
				globalsDict.Set(nameDunderBuiltins, builtinsMod)
			} else {
				globalsDict.Set(nameDunderBuiltins, val.DictVal(vmInstance.builtins))
			}
		}

		var cells []val.Value
		if !closureVal.IsNone() && closureVal.IsTuple() {
			cells = closureVal.AsTuple().Items
		}

		res := vmInstance.runNestedCode(co, globalsDict, localsVal, cells)
		if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
			exc := vmInstance.unhandledException
			vmInstance.unhandledException = val.NoneVal()
			return val.RaiseExceptionVal(ctx, exc)
		}
		return res
	}))

	d.Set(val.InternString("exec"), val.NewBuiltinFuncVal("exec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		npos := len(args) - len(kwnames)
		if npos < 1 && len(args) < 1 {
			return val.RaiseTypeError(ctx, "exec() takes at least 1 argument")
		}
		var codeObj val.Value
		globalsVal := val.NoneVal()
		localsVal := val.NoneVal()
		closureVal := val.NoneVal()

		if npos >= 1 {
			codeObj = args[0]
		}
		if npos >= 2 {
			globalsVal = args[1]
		}
		if npos >= 3 {
			localsVal = args[2]
		}
		if npos >= 4 {
			closureVal = args[3]
		}
		for i, kn := range kwnames {
			argV := args[npos+i]
			switch kn.AsString() {
			case "source", "code":
				codeObj = argV
			case "globals":
				globalsVal = argV
			case "locals":
				localsVal = argV
			case "closure":
				closureVal = argV
			}
		}

		vmInstance := ctx.VM.(*VM)
		var co *CodeObject

		switch {
		case codeObj.Kind() == val.KindCode:
			co = AsCodeObject(codeObj)
		case codeObj.IsAnyString() || codeObj.IsBytes():
			var srcBytes []byte
			if codeObj.IsAnyString() {
				srcBytes = []byte(codeObj.AsString())
			} else {
				srcBytes = codeObj.AsBytes().Data
			}
			compiled, err := vmInstance.engine.CompileDynamic(srcBytes, "<string>", 0)
			if err != nil {
				return val.RaiseException(ctx, val.SyntaxErrorType, err.Error())
			}
			co = compiled
		default:
			return val.RaiseTypeError(ctx, "exec() arg 1 must be a string, bytes or code object")
		}

		var globalsDict *val.PyDict
		switch {
		case globalsVal.IsNone():
			globalsDict = vmInstance.currentGlobals()
			if localsVal.IsNone() {
				localsVal = vmInstance.currentLocalsVal()
			}
		case globalsVal.IsDict():
			globalsDict = globalsVal.AsDict()
			if localsVal.IsNone() {
				localsVal = globalsVal
			}
		default:
			return val.RaiseTypeError(ctx, "globals must be a dict")
		}

		if _, ok := globalsDict.Get(nameDunderBuiltins); !ok {
			if builtinsMod, ok := vmInstance.modules.Get(nameBuiltins); ok {
				globalsDict.Set(nameDunderBuiltins, builtinsMod)
			} else {
				globalsDict.Set(nameDunderBuiltins, val.DictVal(vmInstance.builtins))
			}
		}

		var cells []val.Value
		if !closureVal.IsNone() && closureVal.IsTuple() {
			cells = closureVal.AsTuple().Items
		}

		vmInstance.runNestedCode(co, globalsDict, localsVal, cells)
		if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
			exc := vmInstance.unhandledException
			vmInstance.unhandledException = val.NoneVal()
			return val.RaiseExceptionVal(ctx, exc)
		}
		return val.NoneVal()
	}))

	// __import__
	d.Set(val.InternString("__import__"), val.NewBuiltinFuncVal("__import__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.RaiseTypeError(ctx, "__import__() takes at least 1 argument")
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
			return val.RaiseExceptionVal(ctx, exc)
		}
		if err != nil {
			return val.RaiseExceptionVal(ctx, panicToPyException(err.Error()))
		}

		if len(fromList) > 0 {
			for _, sym := range fromList {
				_, _ = vmInstance.ImportFrom(modVal, sym, callerGlobals)
				if !vmInstance.unhandledException.IsNone() && vmInstance.unhandledException.HeaderPtr() != nil {
					exc := vmInstance.unhandledException
					vmInstance.unhandledException = val.NoneVal()
					return val.RaiseExceptionVal(ctx, exc)
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
		prompt := ""
		if len(args) > 0 {
			prompt = args[0].String()
		}
		if vmInstance, ok := ctx.VM.(*VM); ok && vmInstance.engine.inputHook != nil {
			line, err := vmInstance.engine.inputHook(prompt)
			if err != nil {
				return val.RaiseException(ctx, val.EOFErrorType, "EOF when reading a line")
			}
			return val.NewStringVal(line)
		}
		if prompt != "" {
			fmt.Print(prompt)
		}
		var line string
		_, _ = fmt.Scanln(&line)
		return val.NewStringVal(line)
	}))
}

func builtinIsInstance(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 2 {
		return val.RaiseTypeError(ctx, "isinstance expected 2 arguments")
	}
	return val.BoolVal(checkIsInstance(ctx, args[0], args[1]))
}

func checkIsInstance(ctx *val.ExecContext, obj, target val.Value) bool {
	if target.IsType() {
		tp := target.AsType()
		meta := tp.Header.Type
		if meta == nil || meta == val.TypeType {
			if obj.Type() == tp || val.IsSubclass(obj.Type(), tp) {
				return true
			}
			if obj.IsInstance() {
				if spoofedCls, ok := obj.AsInstance().GetAttr(nameClass); ok && spoofedCls.IsType() {
					return val.IsSubclass(spoofedCls.AsType(), tp)
				}
			}
			return false
		}
	}
	if target.IsTuple() {
		for _, item := range target.AsTuple().Items {
			if checkIsInstance(ctx, obj, item) {
				return true
			}
		}
		return false
	}
	targetType := target.Type()
	if fn, _, ok := targetType.LookupMRO(nameInstanceCheck); ok {
		res := ctx.Invoke2(fn, target, obj)
		return res.IsTruthy()
	}
	if target.IsType() {
		if val.IsSubclass(obj.Type(), target.AsType()) {
			return true
		}
		// Support __class__ spoofing (e.g. unittest.mock.Mock)
		if obj.IsInstance() {
			if spoofedCls, ok := obj.AsInstance().GetAttr(nameClass); ok && spoofedCls.IsType() {
				return val.IsSubclass(spoofedCls.AsType(), target.AsType())
			}
		}
		return false
	}
	return false
}

func builtinIsSubclass(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	if len(args) < 2 {
		return val.RaiseTypeError(ctx, "issubclass expected 2 arguments")
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
	if fn, _, ok := targetType.LookupMRO(nameSubclassCheck); ok {
		res := ctx.Invoke2(fn, target, cls)
		return res.IsTruthy()
	}
	if !cls.IsType() {
		val.RaiseTypeError(ctx, "issubclass() arg 1 must be a class")
		return false
	}
	if target.IsType() {
		return val.IsSubclass(cls.AsType(), target.AsType())
	}
	return false
}

func vmGetAttrSafe(ctx *val.ExecContext, obj val.Value, name string) (res val.Value, ok bool) {
	return vmGetAttrSafeVal(ctx, obj, val.InternString(name))
}

func vmGetAttrSafeVal(ctx *val.ExecContext, obj, name val.Value) (val.Value, bool) {
	interned := val.InternValue(name)
	v, found := val.LookupAttrOptional(ctx, obj, interned)
	if !found {
		return val.NoneVal(), false
	}
	if ctx != nil && ctx.HasException() {
		if val.IsInstance(ctx.ActiveException, val.AttributeErrorType) {
			ctx.ClearException()
			return val.NoneVal(), false
		}
		return val.NoneVal(), false
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
		} else if iterFn, _, ok := o.Type().LookupMRO(nameIter); ok && !iterFn.IsNone() && !isPrimitiveIterable(o) {
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
	if nextFn, _, ok := tp.LookupMRO(nameNext); ok {
		return val.NewIterValWithNext(func() (val.Value, bool) {
			v := ctx.Invoke1(nextFn, o)
			if ctx != nil && ctx.HasException() {
				if val.IsInstance(ctx.ActiveException, val.StopIterationType) {
					ctx.ClearException()
				}
				return val.NoneVal(), false
			}
			return v, true
		}).AsIter()
	}
	val.RaiseTypeError(ctx, "'"+tp.Name+"' object is not iterable")
	return nil
}

func isPrimitiveIterable(o val.Value) bool {
	return o.IsList() || o.IsTuple() || o.IsDict() || o.IsSet() || o.IsRange() || o.IsAnyString()
}
