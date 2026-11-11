package vm

import (
	"runtime"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

// The tracemalloc accelerator keeps the tracing state of the process.
var (
	tracemallocTracing atomic.Bool
	tracemallocLimit   atomic.Int64
	tracemallocPeak    atomic.Uint64
	tracemallocCurrent atomic.Uint64
)

// tracemallocCurrentBytes returns the number of bytes currently allocated by
// the Go runtime.
func tracemallocCurrentBytes() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// createTracemallocModule builds the _tracemalloc accelerator.
func (e *Engine) createTracemallocModule() val.Value {
	modVal := val.NewModuleVal("_tracemalloc", "memory allocation tracing")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("start"), val.NewBuiltinFuncVal("start", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		limit := int64(1)
		if len(args) > 0 && args[0].IsInt64() {
			limit = args[0].AsInt64()
		}
		tracemallocLimit.Store(limit)
		tracemallocCurrent.Store(tracemallocCurrentBytes())
		tracemallocPeak.Store(tracemallocCurrentBytes())
		tracemallocTracing.Store(true)
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("stop"), val.NewBuiltinFuncVal("stop", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tracemallocTracing.Store(false)
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("is_tracing"), val.NewBuiltinFuncVal("is_tracing", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(tracemallocTracing.Load())
	}))

	mod.Dict.Set(val.InternString("clear_traces"), val.NewBuiltinFuncVal("clear_traces", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tracemallocCurrent.Store(tracemallocCurrentBytes())
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("get_traced_memory"), val.NewBuiltinFuncVal("get_traced_memory", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		current := tracemallocCurrentBytes()
		tracemallocCurrent.Store(current)
		for {
			peak := tracemallocPeak.Load()
			if current <= peak || tracemallocPeak.CompareAndSwap(peak, current) {
				break
			}
		}
		return val.NewTupleVal(val.Int64Val(int64(current)), val.Int64Val(int64(tracemallocPeak.Load())))
	}))

	mod.Dict.Set(val.InternString("reset_peak"), val.NewBuiltinFuncVal("reset_peak", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tracemallocPeak.Store(tracemallocCurrentBytes())
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("get_traceback_limit"), val.NewBuiltinFuncVal("get_traceback_limit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(tracemallocLimit.Load())
	}))

	mod.Dict.Set(val.InternString("_get_traces"), val.NewBuiltinFuncVal("_get_traces", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// The runtime does not track the allocation site of every object, so
		// the traces report the current allocation of the process.
		current := tracemallocCurrentBytes()
		traceback := val.NewTupleVal()
		trace := val.NewTupleVal(val.Int64Val(0), val.Int64Val(int64(current)), traceback)
		return val.NewTupleVal(trace)
	}))

	mod.Dict.Set(val.InternString("_get_object_traceback"), val.NewBuiltinFuncVal("_get_object_traceback", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	return modVal
}
