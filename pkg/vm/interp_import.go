package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opImportName(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := uint16(inst)
	nameVal := st.consts[bx]
	argA := uint8(inst >> 16)
	level := int(argA & 0x7F)
	returnLeaf := (argA & 0x80) != 0

	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	modVal, err := vm.ImportModule(nameVal.AsString(), level, returnLeaf, st.globals)
	stack = st.syncStackFromExecCtx(vm, stack)
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	if err != nil {
		exc := panicToPyException(err)
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = modVal
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opImportFrom(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := uint16(inst)
	symVal := st.consts[bx]
	modVal := stack[sp-1]

	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	v, err := vm.ImportFrom(modVal, symVal.AsString(), st.globals)
	stack = st.syncStackFromExecCtx(vm, stack)
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	if err != nil {
		exc := panicToPyException(err)
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = v
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opImportStar(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	sp--
	modVal := stack[sp]
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	err := vm.ImportStar(modVal, st.globals)
	st.syncStackFromExecCtx(vm, stack)
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	if err != nil {
		exc := panicToPyException(err)
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	ip++
	return ip, sp, ctlReload
}
