package vm

import (
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

var startTime = time.Now()

func (e *Engine) createTimeModule() val.Value {
	modVal := val.NewModuleVal("time", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("time"), val.NewBuiltinFuncVal("time", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		now := time.Now()
		sec := float64(now.UnixNano()) / 1e9
		return val.Float64Val(sec)
	}))

	mod.Dict.Set(val.InternString("time_ns"), val.NewBuiltinFuncVal("time_ns", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(time.Now().UnixNano())
	}))

	mod.Dict.Set(val.InternString("sleep"), val.NewBuiltinFuncVal("sleep", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: sleep() takes exactly one argument")
		}
		secs := toFloat(args[0])
		if secs > 0 {
			time.Sleep(time.Duration(secs * float64(time.Second)))
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("monotonic"), val.NewBuiltinFuncVal("monotonic", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		elapsed := time.Since(startTime)
		return val.Float64Val(elapsed.Seconds())
	}))

	mod.Dict.Set(val.InternString("monotonic_ns"), val.NewBuiltinFuncVal("monotonic_ns", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		elapsed := time.Since(startTime)
		return val.Int64Val(elapsed.Nanoseconds())
	}))

	mod.Dict.Set(val.InternString("perf_counter"), val.NewBuiltinFuncVal("perf_counter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		elapsed := time.Since(startTime)
		return val.Float64Val(elapsed.Seconds())
	}))

	mod.Dict.Set(val.InternString("perf_counter_ns"), val.NewBuiltinFuncVal("perf_counter_ns", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		elapsed := time.Since(startTime)
		return val.Int64Val(elapsed.Nanoseconds())
	}))

	return modVal
}
