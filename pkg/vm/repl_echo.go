package vm

import (
	"fmt"
	"os"

	"github.com/servusdei2018/fython/pkg/val"
)

// getDisplayhook resolves the currently registered sys.displayhook callable.
func (vm *VM) getDisplayhook() (val.Value, bool) {
	if vm.modules == nil {
		return val.NoneVal(), false
	}
	sysVal, ok := vm.modules.Get(val.InternString("sys"))
	if !ok || !sysVal.IsModule() {
		return val.NoneVal(), false
	}
	hook, ok := sysVal.AsModule().Dict.Get(val.InternString("displayhook"))
	if !ok || hook.IsNone() {
		return val.NoneVal(), false
	}
	return hook, true
}

// invokeDisplayhook calls sys.displayhook(value). A Go panic carrying a
// Python exception is captured as panicValue so the interpreter loop can
// route it through unwindException like any other operation fault.
func (vm *VM) invokeDisplayhook(hook, v val.Value) (res val.Value, panicValue any) {
	defer func() {
		if r := recover(); r != nil {
			panicValue = r
		}
	}()
	return vm.execCtx.Invoke1(hook, v), nil
}

// defaultPrintExpr implements the plain-stdout echo when no hook exists.
func (vm *VM) defaultPrintExpr(v val.Value) {
	_, _ = fmt.Fprintln(os.Stdout, val.PyRepr(&vm.execCtx, v))
}
