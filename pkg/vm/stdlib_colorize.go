package vm

import "github.com/servusdei2018/fython/pkg/val"

func (e *Engine) createColorizeModule() val.Value {
	modVal := val.NewModuleVal("_colorize", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("can_colorize"), val.NewBuiltinFuncVal("can_colorize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	mod.Dict.Set(val.InternString("get_theme"), val.NewBuiltinFuncVal("get_theme", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return colorizeTheme()
	}))
	return modVal
}

func colorizeTheme() val.Value {
	unit := val.NewInstanceOfType(val.ObjectType)
	inst := unit.AsInstance()
	blank := val.NewStringVal("")
	for _, name := range []string{"fail", "fail_info", "reset", "warn", "passed"} {
		inst.SetAttr(val.InternString(name), blank)
	}
	astTheme := val.NewInstanceOfType(val.ObjectType)
	for _, name := range []string{"node", "field", "attribute", "string", "number", "keyword", "reset"} {
		astTheme.AsInstance().SetAttr(val.InternString(name), blank)
	}
	theme := val.NewInstanceOfType(val.ObjectType)
	theme.AsInstance().SetAttr(val.InternString("unittest"), unit)
	theme.AsInstance().SetAttr(val.InternString("ast"), astTheme)
	return theme
}
