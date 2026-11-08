package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createFaulthandlerModule() val.Value {
	modVal := val.NewModuleVal("faulthandler", "")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("is_enabled"), val.NewBuiltinFuncVal("is_enabled", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	mod.Dict.Set(val.InternString("enable"), val.NewBuiltinFuncVal("enable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("disable"), val.NewBuiltinFuncVal("disable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("dump_traceback"), val.NewBuiltinFuncVal("dump_traceback", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("dump_traceback_later"), val.NewBuiltinFuncVal("dump_traceback_later", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("cancel_dump_traceback_later"), val.NewBuiltinFuncVal("cancel_dump_traceback_later", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	return modVal
}
