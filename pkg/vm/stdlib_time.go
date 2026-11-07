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
			d := time.Duration(secs * float64(time.Second))
			if vm, ok := ctx.VM.(*VM); ok && vm.engine.signals.owner.Load() == vm {
				vm.sleepInterruptibly(d)
			} else {
				time.Sleep(d)
			}
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

	parseTimeArg := func(args []val.Value) time.Time {
		if len(args) == 0 || args[0].IsNone() {
			return time.Now()
		}
		secs := toFloat(args[0])
		secInt := int64(secs)
		nsec := int64((secs - float64(secInt)) * 1e9)
		return time.Unix(secInt, nsec)
	}

	timeToStructTime := func(t time.Time) val.Value {
		yday := t.YearDay()
		wday := int(t.Weekday()-1+7) % 7 // Monday = 0
		isdst := 0
		return val.NewTupleVal(
			val.Int64Val(int64(t.Year())),
			val.Int64Val(int64(t.Month())),
			val.Int64Val(int64(t.Day())),
			val.Int64Val(int64(t.Hour())),
			val.Int64Val(int64(t.Minute())),
			val.Int64Val(int64(t.Second())),
			val.Int64Val(int64(wday)),
			val.Int64Val(int64(yday)),
			val.Int64Val(int64(isdst)),
		)
	}

	mod.Dict.Set(val.InternString("localtime"), val.NewBuiltinFuncVal("localtime", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		t := parseTimeArg(args).Local()
		return timeToStructTime(t)
	}))

	mod.Dict.Set(val.InternString("gmtime"), val.NewBuiltinFuncVal("gmtime", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		t := parseTimeArg(args).UTC()
		return timeToStructTime(t)
	}))

	return modVal
}
