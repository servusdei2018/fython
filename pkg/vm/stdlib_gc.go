package vm

import (
	"runtime"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createGCModule() val.Value {
	modVal := val.NewModuleVal("gc", "")
	mod := modVal.AsModule()

	// gc.collect(generation=2) -> int
	mod.Dict.Set(val.InternString("collect"), val.NewBuiltinFuncVal("collect", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if ctx != nil {
			if ctx.Stack != nil {
				for i := ctx.SP; i < len(ctx.Stack); i++ {
					ctx.Stack[i] = val.Value{}
				}
			}
			if vmInstance, ok := ctx.VM.(*VM); ok {
				for i := range vmInstance.rootCache.ic {
					vmInstance.rootCache.ic[i] = ICSlot{}
				}
				for _, cache := range vmInstance.codeCaches {
					for i := range cache.ic {
						cache.ic[i] = ICSlot{}
					}
				}
			}
		}
		runtime.GC()
		runtime.Gosched()
		// Brief pause to allow finalizer/cleanup goroutines to schedule and complete
		time.Sleep(20 * time.Millisecond)
		runtime.GC()
		runtime.Gosched()
		time.Sleep(20 * time.Millisecond)
		return val.Int64Val(0)
	}))

	// gc.enable()
	mod.Dict.Set(val.InternString("enable"), val.NewBuiltinFuncVal("enable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	// gc.disable()
	mod.Dict.Set(val.InternString("disable"), val.NewBuiltinFuncVal("disable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	// gc.isenabled() -> bool
	mod.Dict.Set(val.InternString("isenabled"), val.NewBuiltinFuncVal("isenabled", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(true)
	}))

	// gc.get_count() -> (0, 0, 0)
	mod.Dict.Set(val.InternString("get_count"), val.NewBuiltinFuncVal("get_count", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(val.Int64Val(0), val.Int64Val(0), val.Int64Val(0))
	}))

	// gc.get_threshold() -> (700, 10, 10)
	mod.Dict.Set(val.InternString("get_threshold"), val.NewBuiltinFuncVal("get_threshold", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(val.Int64Val(700), val.Int64Val(10), val.Int64Val(10))
	}))

	// gc.set_threshold()
	mod.Dict.Set(val.InternString("set_threshold"), val.NewBuiltinFuncVal("set_threshold", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	return modVal
}
