package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

func getSeqLen(ctx *val.ExecContext, a val.Value) int {
	if a.IsList() {
		return len(a.AsList().Items)
	}
	if a.IsTuple() {
		return len(a.AsTuple().Items)
	}
	if a.IsAnyString() {
		return len([]rune(a.AsString()))
	}
	if sqLen := a.Type().Slots.SqLen; sqLen != nil {
		return int(sqLen(ctx, a))
	}
	return 0
}

func parseBisectArgs(ctx *val.ExecContext, args []val.Value, kwnames []val.Value, name string) (a, x val.Value, lo, hi int, keyFn val.Value) {
	if len(args) < 2 {
		panic("TypeError: " + name + "() takes at least 2 arguments")
	}
	a = args[0]
	x = args[1]

	seqLen := getSeqLen(ctx, a)
	lo = 0
	hi = seqLen
	keyFn = val.NoneVal()

	numPos := len(args) - len(kwnames)
	if numPos > 2 && !args[2].IsNone() {
		lo = int(args[2].AsInt64())
	}
	if numPos > 3 && !args[3].IsNone() {
		hi = int(args[3].AsInt64())
	}

	for i, kw := range kwnames {
		kwStr := kw.AsString()
		valIdx := numPos + i
		v := args[valIdx]
		switch kwStr {
		case "lo":
			if !v.IsNone() {
				lo = int(v.AsInt64())
			}
		case "hi":
			if !v.IsNone() {
				hi = int(v.AsInt64())
			}
		case "key":
			keyFn = v
		}
	}

	if lo < 0 {
		panic(val.NewException(val.ValueErrorType, "lo must be non-negative"))
	}
	if hi < 0 {
		hi = 0
	}
	if hi > seqLen {
		hi = seqLen
	}
	return
}

func getSeqItem(ctx *val.ExecContext, a val.Value, idx int) val.Value {
	if a.IsList() {
		return a.AsList().Items[idx]
	}
	return val.GetItemCtx(ctx, a, val.Int64Val(int64(idx)))
}

func bisectRightCore(ctx *val.ExecContext, a, x val.Value, lo, hi int, keyFn val.Value) int {
	if keyFn.IsNone() {
		for lo < hi {
			mid := (lo + hi) / 2
			elem := getSeqItem(ctx, a, mid)
			if val.LessThan(x, elem).IsTruthy() {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
	} else {
		subcall := keyFn.Type().Slots.TpCall
		var kx val.Value
		if subcall != nil {
			kx = subcall(ctx, keyFn, []val.Value{x}, nil)
		} else {
			kx = x
		}
		for lo < hi {
			mid := (lo + hi) / 2
			elem := getSeqItem(ctx, a, mid)
			var kelem val.Value
			if subcall != nil {
				kelem = subcall(ctx, keyFn, []val.Value{elem}, nil)
			} else {
				kelem = elem
			}
			if val.LessThan(kx, kelem).IsTruthy() {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
	}
	return lo
}

func bisectLeftCore(ctx *val.ExecContext, a, x val.Value, lo, hi int, keyFn val.Value) int {
	if keyFn.IsNone() {
		for lo < hi {
			mid := (lo + hi) / 2
			elem := getSeqItem(ctx, a, mid)
			if val.LessThan(elem, x).IsTruthy() {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
	} else {
		subcall := keyFn.Type().Slots.TpCall
		var kx val.Value
		if subcall != nil {
			kx = subcall(ctx, keyFn, []val.Value{x}, nil)
		} else {
			kx = x
		}
		for lo < hi {
			mid := (lo + hi) / 2
			elem := getSeqItem(ctx, a, mid)
			var kelem val.Value
			if subcall != nil {
				kelem = subcall(ctx, keyFn, []val.Value{elem}, nil)
			} else {
				kelem = elem
			}
			if val.LessThan(kelem, kx).IsTruthy() {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
	}
	return lo
}

func seqInsert(ctx *val.ExecContext, a, x val.Value, idx int) {
	if a.IsList() {
		lst := a.AsList()
		if idx >= len(lst.Items) {
			lst.Items = append(lst.Items, x)
		} else {
			lst.Items = append(lst.Items[:idx], append([]val.Value{x}, lst.Items[idx:]...)...)
		}
		return
	}
	if insertFn, _, ok := a.Type().LookupMRO(val.InternString("insert")); ok {
		ctx.Invoke2(insertFn, val.Int64Val(int64(idx)), x)
		return
	}
	panic("TypeError: sequence does not support insert")
}

func (e *Engine) createBisectModule() val.Value {
	modVal := val.NewModuleVal("_bisect", "Native bisection algorithms")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("bisect_right"), val.NewBuiltinFuncVal("bisect_right", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a, x, lo, hi, keyFn := parseBisectArgs(ctx, args, kwnames, "bisect_right")
		res := bisectRightCore(ctx, a, x, lo, hi, keyFn)
		return val.Int64Val(int64(res))
	}))

	mod.Dict.Set(val.InternString("bisect_left"), val.NewBuiltinFuncVal("bisect_left", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a, x, lo, hi, keyFn := parseBisectArgs(ctx, args, kwnames, "bisect_left")
		res := bisectLeftCore(ctx, a, x, lo, hi, keyFn)
		return val.Int64Val(int64(res))
	}))

	mod.Dict.Set(val.InternString("insort_right"), val.NewBuiltinFuncVal("insort_right", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a, x, lo, hi, keyFn := parseBisectArgs(ctx, args, kwnames, "insort_right")
		idx := bisectRightCore(ctx, a, x, lo, hi, keyFn)
		seqInsert(ctx, a, x, idx)
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("insort_left"), val.NewBuiltinFuncVal("insort_left", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a, x, lo, hi, keyFn := parseBisectArgs(ctx, args, kwnames, "insort_left")
		idx := bisectLeftCore(ctx, a, x, lo, hi, keyFn)
		seqInsert(ctx, a, x, idx)
		return val.NoneVal()
	}))

	return modVal
}
