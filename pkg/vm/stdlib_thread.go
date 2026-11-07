package vm

import (
	"sync"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	threadLockType  *val.PyType
	threadRLockType *val.PyType
	threadOnce      sync.Once
	threadCounter   int64 = 1
)

func initThreadModule() {
	threadOnce.Do(func() {
		threadLockType = val.MustNewType("LockType", []*val.PyType{val.ObjectType}, nil)
		threadLockType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(true)
		}))
		threadLockType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		threadLockType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(false)
		}))
		threadLockType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				return args[0]
			}
			return val.NoneVal()
		}))
		threadLockType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))

		threadRLockType = val.MustNewType("RLock", []*val.PyType{val.ObjectType}, nil)
		threadRLockType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewInstanceOfType(threadRLockType)
		}
		for _, it := range threadLockType.Dict.Items() {
			threadRLockType.Dict.Set(it.Key, it.Value)
		}
	})
}

func (e *Engine) createThreadModule() val.Value {
	initThreadModule()
	modVal := val.NewModuleVal("_thread", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("LockType"), val.TypeVal(threadLockType))
	mod.Dict.Set(val.InternString("RLock"), val.TypeVal(threadRLockType))
	mod.Dict.Set(val.InternString("allocate_lock"), val.NewBuiltinFuncVal("allocate_lock", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewInstanceOfType(threadLockType)
	}))
	mod.Dict.Set(val.InternString("get_ident"), val.NewBuiltinFuncVal("get_ident", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		id := atomic.LoadInt64(&threadCounter)
		return val.Int64Val(id)
	}))
	mod.Dict.Set(val.InternString("TIMEOUT_MAX"), val.Int64Val(9223372036))
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.RuntimeErrorType))

	return modVal
}

func (e *Engine) createThreadingModule() val.Value {
	initThreadModule()
	modVal := val.NewModuleVal("threading", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Lock"), val.TypeVal(threadLockType))
	mod.Dict.Set(val.InternString("RLock"), val.TypeVal(threadRLockType))
	mod.Dict.Set(val.InternString("get_ident"), val.NewBuiltinFuncVal("get_ident", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		id := atomic.LoadInt64(&threadCounter)
		return val.Int64Val(id)
	}))
	mod.Dict.Set(val.InternString("TIMEOUT_MAX"), val.Int64Val(9223372036))

	dummyThreadType := val.MustNewType("Thread", []*val.PyType{val.ObjectType}, nil)
	mod.Dict.Set(val.InternString("Thread"), val.TypeVal(dummyThreadType))
	mod.Dict.Set(val.InternString("current_thread"), val.NewBuiltinFuncVal("current_thread", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(dummyThreadType)
		inst.AsInstance().SetAttr(val.InternString("name"), val.NewStringVal("MainThread"))
		inst.AsInstance().SetAttr(val.InternString("ident"), val.Int64Val(atomic.LoadInt64(&threadCounter)))
		return inst
	}))
	mod.Dict.Set(val.InternString("main_thread"), val.NewBuiltinFuncVal("main_thread", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(dummyThreadType)
		inst.AsInstance().SetAttr(val.InternString("name"), val.NewStringVal("MainThread"))
		inst.AsInstance().SetAttr(val.InternString("ident"), val.Int64Val(1))
		return inst
	}))
	mod.Dict.Set(val.InternString("local"), val.TypeVal(val.ObjectType))

	return modVal
}
