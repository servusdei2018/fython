package vm

import "github.com/servusdei2018/fython/pkg/val"

func (e *Engine) createPdbModule() val.Value {
	modVal := val.NewModuleVal("pdb", "The Python Debugger (stub)")
	mod := modVal.AsModule()
	pdbClass := val.MustNewType("Pdb", []*val.PyType{val.ObjectType}, nil)
	pdbClass.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("Pdb"), val.TypeVal(pdbClass))
	mod.Dict.Set(val.InternString("set_trace"), val.NewBuiltinFuncVal("set_trace", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("post_mortem"), val.NewBuiltinFuncVal("post_mortem", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	return modVal
}
