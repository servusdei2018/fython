package vm

import "github.com/servusdei2018/fython/pkg/val"

// GetAttrForRepl reads an attribute the way the interpreter would, turning
// any Python-level fault into None so REPL introspection never crashes.
func (vm *VM) GetAttrForRepl(obj, name val.Value) (res val.Value) {
	defer func() {
		if r := recover(); r != nil {
			res = val.NoneVal()
		}
	}()
	res = val.GetAttrCtx(&vm.execCtx, obj, name)
	if res.HeaderPtr() == nil {
		return val.NoneVal()
	}
	return res
}
