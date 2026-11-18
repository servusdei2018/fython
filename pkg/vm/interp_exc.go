package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opRaise(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := DecodeArgA(inst)
	var exc val.Value
	switch argc {
	case 0:
		exc = vm.lastException
		if exc.IsNone() || exc.HeaderPtr() == nil {
			exc = val.NewRuntimeError("No active exception to reraise")
		}
	case 1:
		sp--
		exc = stack[sp]
		if exc.IsType() {
			exc = val.NewInstanceOfType(exc.AsType())
		}
	case 2:
		cause := stack[sp-1]
		exc = stack[sp-2]
		sp -= 2
		if exc.IsType() {
			exc = val.NewInstanceOfType(exc.AsType())
		}
		if exc.IsInstance() {
			exc.AsInstance().SetAttr(nameCause, cause)
			exc.AsInstance().SetAttr(nameSuppressContext, val.BoolVal(true))
		}
	}
	st.raiseExc = exc
	return ip, sp, ctlRaise
}

//go:noinline
func (vm *VM) opCheckExcMatch(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	target := stack[sp-1]
	exc := stack[sp-2]
	match := false
	if target.IsType() {
		match = val.IsSubclass(exc.Type(), target.AsType())
	} else if target.IsTuple() {
		for _, item := range target.AsTuple().Items {
			if item.IsType() && val.IsSubclass(exc.Type(), item.AsType()) {
				match = true
				break
			}
		}
	}
	stack[sp-2] = val.BoolVal(match)
	sp--
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opCleanupExc(st *loopState, ip, sp int) (int, int, loopCtl) {
	sp--
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opPushExcInfo(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	vm.excStack = append(vm.excStack, vm.lastException)
	vm.lastException = stack[sp-1]
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opPopExcept(st *loopState, ip, sp int) (int, int, loopCtl) {
	n := len(vm.excStack)
	if n > 0 {
		vm.lastException = vm.excStack[n-1]
		vm.excStack = vm.excStack[:n-1]
	} else {
		vm.lastException = val.NoneVal()
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opCheckExcMatchStar(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	targetType := stack[sp-1]
	exc := stack[sp-2]
	if exc.IsExceptionGroup() {
		eg := exc.AsExceptionGroup()
		var targetCls *val.PyType
		if targetType.IsType() {
			targetCls = targetType.AsType()
		}
		matched, rest := eg.Split(targetCls)
		if !matched.IsNone() {
			stack[sp-2] = rest
			stack[sp-1] = matched
			stack[sp] = val.BoolVal(true)
			sp++
		} else {
			stack[sp-1] = val.BoolVal(false)
		}
	} else {
		matches := false
		if targetType.IsType() {
			matches = val.IsSubclass(exc.Type(), targetType.AsType())
		}
		if matches {
			stack[sp-2] = val.NoneVal()
			stack[sp-1] = exc
			stack[sp] = val.BoolVal(true)
			sp++
		} else {
			stack[sp-1] = val.BoolVal(false)
		}
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opReturnSlow(st *loopState, ip, sp int, retVal val.Value) (int, int, loopCtl) {
	stack := st.stack
	if curShadow := vm.frames[st.frameIdx].shadowFrame; curShadow != nil {
		curShadow.Detach()
		vm.frames[st.frameIdx].shadowFrame = nil
	}
	if st.activeGen != nil && st.frameIdx == st.startFrameIdx {
		st.activeGen.RetVal = retVal
		st.activeGen.State = val.GenClosed
		st.activeGen.SP = sp
		st.result = retVal
		return ip, sp, ctlExit
	}
	if st.activeCoro != nil && st.frameIdx == st.startFrameIdx {
		st.activeCoro.RetVal = retVal
		st.activeCoro.State = val.CoroClosed
		st.activeCoro.SP = sp
		st.result = retVal
		return ip, sp, ctlExit
	}
	if st.frameIdx == st.startFrameIdx {
		st.result = retVal
		return ip, sp, ctlExit
	}
	st.frameIdx--
	caller := &vm.frames[st.frameIdx]
	if caller.initInstance.HeaderPtr() != nil {
		inst := caller.initInstance
		caller.initInstance = val.Value{}
		if !retVal.IsNone() {
			st.raiseExc = val.NewTypeError("__init__() should return None, not '" + retVal.Type().Name + "'")
			return ip, sp, ctlRaise
		}
		retVal = inst
	}
	if caller.cmpActive {
		cmpLeft, cmpRight, cmpOp := caller.cmpLeft, caller.cmpRight, caller.cmpOp
		caller.cmpActive = false
		caller.cmpLeft = val.Value{}
		caller.cmpRight = val.Value{}
		if retVal.IsNotImplemented() {
			if len(vm.excStack) > caller.excStackLen {
				vm.excStack = vm.excStack[:caller.excStackLen]
			}
			st.curCO = caller.co
			st.codePtr = caller.codePtr
			st.codeLen = caller.codeLen
			st.consts = caller.consts
			ip = caller.ip
			st.base = caller.baseOffset
			st.cells = caller.cells
			st.cellPtrs = caller.cellPtrs
			st.globals = caller.globals
			st.curLocals = caller.locals
			vm.activeCO = st.curCO
			vm.activeBaseOffset = st.base
			if vm.activeCellPtrs != nil || st.cellPtrs != nil {
				vm.activeCells = st.cells
				vm.activeCellPtrs = st.cellPtrs
			}
			vm.activeFrameIdx = st.frameIdx
			if caller.cache != nil {
				st.cache = caller.cache
			} else {
				st.cache = vm.cacheFor(st.curCO)
			}
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = sp
			res := vm.compareOpSafe(cmpLeft, cmpRight, cmpOp)
			stack = st.syncStackFromExecCtx(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[caller.retSP] = res
			return ip, caller.retSP + 1, ctlReload
		}
		if cmpOp == CMP_IN || cmpOp == CMP_NOT_IN {
			if cmpOp == CMP_NOT_IN {
				retVal = val.BoolVal(!retVal.IsTruthy())
			} else {
				retVal = val.BoolVal(retVal.IsTruthy())
			}
		}
	}
	if len(vm.excStack) > caller.excStackLen {
		vm.excStack = vm.excStack[:caller.excStackLen]
	}
	if caller.co == st.curCO {
		ip = caller.ip
		st.base = caller.baseOffset
		vm.activeBaseOffset = st.base
		vm.activeFrameIdx = st.frameIdx
		stack[caller.retSP] = retVal
		return ip, caller.retSP + 1, ctlReload
	}
	st.curCO = caller.co
	st.codePtr = caller.codePtr
	st.codeLen = caller.codeLen
	st.consts = caller.consts
	ip = caller.ip
	st.base = caller.baseOffset
	st.cells = caller.cells
	st.cellPtrs = caller.cellPtrs
	st.globals = caller.globals
	st.curLocals = caller.locals
	vm.activeCO = st.curCO
	vm.activeBaseOffset = st.base
	if vm.activeCellPtrs != nil || st.cellPtrs != nil {
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
	vm.activeFrameIdx = st.frameIdx
	stack[caller.retSP] = retVal
	if caller.cache != nil {
		st.cache = caller.cache
	} else {
		st.cache = vm.cacheFor(st.curCO)
	}
	return ip, caller.retSP + 1, ctlReload
}
