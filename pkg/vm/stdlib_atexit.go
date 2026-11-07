package vm

import (
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

type exitCallback struct {
	fn      val.Value
	args    []val.Value
	kwnames []val.Value
}

type atexitState struct {
	mu        sync.Mutex
	callbacks []exitCallback
}

var globalAtexit atexitState

func (e *Engine) createAtexitModule() val.Value {
	modVal := val.NewModuleVal("atexit", "Exit handlers registration")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("register"), val.NewBuiltinFuncVal("register", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: register() takes at least 1 argument")
		}
		fn := args[0]
		cbArgs := make([]val.Value, len(args)-1)
		copy(cbArgs, args[1:])

		globalAtexit.mu.Lock()
		globalAtexit.callbacks = append(globalAtexit.callbacks, exitCallback{
			fn:      fn,
			args:    cbArgs,
			kwnames: kwnames,
		})
		globalAtexit.mu.Unlock()

		return fn
	}))

	mod.Dict.Set(val.InternString("unregister"), val.NewBuiltinFuncVal("unregister", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: unregister() takes at least 1 argument")
		}
		fn := args[0]

		globalAtexit.mu.Lock()
		defer globalAtexit.mu.Unlock()

		filtered := make([]exitCallback, 0, len(globalAtexit.callbacks))
		for _, cb := range globalAtexit.callbacks {
			if !val.Equals(cb.fn, fn) {
				filtered = append(filtered, cb)
			}
		}
		globalAtexit.callbacks = filtered
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("_run_exitfuncs"), val.NewBuiltinFuncVal("_run_exitfuncs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		globalAtexit.mu.Lock()
		cbs := make([]exitCallback, len(globalAtexit.callbacks))
		copy(cbs, globalAtexit.callbacks)
		globalAtexit.callbacks = nil
		globalAtexit.mu.Unlock()

		// Run in LIFO order
		for i := len(cbs) - 1; i >= 0; i-- {
			cb := cbs[i]
			if tpCall := cb.fn.Type().Slots.TpCall; tpCall != nil {
				tpCall(ctx, cb.fn, cb.args, cb.kwnames)
			}
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("_clear"), val.NewBuiltinFuncVal("_clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		globalAtexit.mu.Lock()
		globalAtexit.callbacks = nil
		globalAtexit.mu.Unlock()
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("_ncallbacks"), val.NewBuiltinFuncVal("_ncallbacks", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		globalAtexit.mu.Lock()
		n := len(globalAtexit.callbacks)
		globalAtexit.mu.Unlock()
		return val.Int64Val(int64(n))
	}))

	return modVal
}
