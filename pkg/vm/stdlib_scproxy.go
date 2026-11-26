package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

// createScproxyModule builds the native macOS _scproxy module expected by
// urllib.request on Darwin. It returns empty system proxy settings so
// CGO_ENABLED=0 is preserved and environment proxy variables remain honored.
func (e *Engine) createScproxyModule() val.Value {
	modVal := val.NewModuleVal("_scproxy", "macOS SystemConfiguration proxy helper")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("_get_proxy_settings"), val.NewBuiltinFuncVal("_get_proxy_settings", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := val.NewDictVal(2).AsDict()
		d.Set(val.InternString("exclude_simple"), val.BoolVal(false))
		d.Set(val.InternString("exceptions"), val.NewTupleVal())
		return val.DictVal(d)
	}))

	mod.Dict.Set(val.InternString("_get_proxies"), val.NewBuiltinFuncVal("_get_proxies", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewDictVal(0)
	}))

	return modVal
}
