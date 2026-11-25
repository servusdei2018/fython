package vm

import (
	"unsafe"

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
		val.ClearEphemeral(exc)
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
	exc := stack[sp-1]
	vm.lastException = exc
	if exc == vm.deferredTBExc && ip+1 < st.codeLen {
		nextOp := DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
		if nextOp != OP_POP_TOP && nextOp != OP_POP_LOAD_FAST {
			vm.deferredTBBound = true
		}
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opPopExcept(st *loopState, ip, sp int) (int, int, loopCtl) {
	discarded := vm.lastException
	n := len(vm.excStack)
	if n > 0 {
		vm.lastException = vm.excStack[n-1]
		vm.excStack = vm.excStack[:n-1]
	} else {
		vm.lastException = val.NoneVal()
	}
	if discarded == vm.deferredTBExc && vm.deferredTBExc.HeaderPtr() != nil {
		if vm.deferredTBBound {
			vm.materializeDeferredTB(discarded)
		} else {
			vm.clearDeferredTB(discarded)
		}
	}
	if hdr := discarded.HeaderPtr(); hdr != nil && hdr.Kind == val.KindInstance && (hdr.Flags&val.FlagEphemeralAttrErr) != 0 {
		val.ReleaseLazyAttributeErrorCtx(&vm.execCtx, discarded)
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
			val.ClearEphemeral(exc)
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
	if st.curCO != nil && st.frameIdx != st.genFrameIdx && sp > st.base+st.curCO.EffectiveLocals {
		vm.releaseDeadEphemeralIters(stack[st.base+st.curCO.EffectiveLocals : sp])
	}
	if st.curCO != nil && st.curCO.VarKWSafe && st.frameIdx != st.genFrameIdx && (vm.frames[st.frameIdx].flags&frameFlagHasShadow) == 0 {
		idx := st.base + st.curCO.VarKWIdx
		if idx >= 0 && idx < len(stack) {
			vm.releaseFrameSmallDictVal(stack[idx])
		}
	}
	if (vm.frames[st.frameIdx].flags & frameFlagHasShadow) != 0 {
		vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
		if st.frameIdx < len(vm.frameAux) {
			if curShadow := vm.frameAux[st.frameIdx].shadowFrame; curShadow != nil {
				curShadow.Detach()
				vm.frameAux[st.frameIdx].shadowFrame = nil
			}
		}
	}
	if st.activeGen != nil && st.frameIdx == st.genFrameIdx {
		gen := st.activeGen
		gen.RetVal = retVal
		gen.State = val.GenClosed
		gen.SP = sp
		vm.releaseGeneratorStack(gen)
		if st.frameIdx == st.startFrameIdx {
			st.result = retVal
			return ip, sp, ctlExit
		}
		st.frameIdx--
		callerIdx := st.frameIdx
		caller := &vm.frames[callerIdx]
		if len(vm.excStack) > int(caller.excStackLen) {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		aux := vm.frameAuxAt(callerIdx)
		mode := aux.genMode
		defVal := aux.genDefault
		exitIP := aux.genExitIP
		callerIP := caller.ip
		retSP := caller.retSP
		js := aux.joinState
		aux.joinState = nil
		cs := aux.collectState
		aux.collectState = nil
		vm.restoreCallerFrame(st, caller)
		if mode == genModeForIter || mode == genModeYieldFrom || mode == genModeAwait || mode == genModeStrJoin ||
			mode == genModeCollectList || mode == genModeCollectTuple || mode == genModeCollectSet || mode == genModeListExtend ||
			mode == genModeCollectDict || mode == genModeAny || mode == genModeAll || mode == genModeSum || mode == genModeMin || mode == genModeMax {
			vm.releaseGenerator(gen)
		}
		switch mode {
		case genModeStrJoin:
			var out val.Value
			if js != nil {
				out = js.materialize()
				vm.releaseStrJoinState(js)
			} else {
				out = val.NewStringVal("")
			}
			st.stack[retSP] = out
			return callerIP, retSP + 1, ctlReload
		case genModeCollectList, genModeCollectTuple, genModeCollectSet, genModeListExtend,
			genModeCollectDict, genModeAny, genModeAll, genModeSum, genModeMin, genModeMax:
			var out val.Value
			if cs != nil {
				if cs.isliceIter != nil && cs.isliceIter.Stop >= 0 {
					cs.isliceIter.Cur = cs.isliceIter.Stop
				}
				var errExc val.Value
				var ok bool
				out, errExc, ok = cs.materializeChecked(mode)
				vm.releaseCollectState(cs)
				if !ok {
					st.raiseExc = errExc
					return callerIP - 1, retSP, ctlRaise
				}
			} else {
				switch mode {
				case genModeCollectList:
					out = val.NewListVal()
				case genModeCollectTuple:
					out = val.EmptyTupleVal()
				case genModeCollectSet:
					out = val.NewSetVal()
				case genModeCollectDict:
					out = val.NewDictVal(0)
				case genModeAny:
					out = val.BoolVal(false)
				case genModeAll:
					out = val.BoolVal(true)
				case genModeSum:
					out = defVal
				case genModeMin:
					st.raiseExc = val.NewValueError("min() iterable argument is empty")
					return callerIP - 1, retSP, ctlRaise
				case genModeMax:
					st.raiseExc = val.NewValueError("max() iterable argument is empty")
					return callerIP - 1, retSP, ctlRaise
				default:
					out = val.NoneVal()
				}
			}
			st.stack[retSP] = out
			return callerIP, retSP + 1, ctlReload
		case genModeYieldFrom, genModeAwait:
			st.stack[retSP-1] = retVal
			return callerIP + 1, retSP, ctlReload
		case genModeForIter:
			return exitIP, retSP - 1, ctlReload
		case genModeNextDefault:
			st.stack[retSP] = defVal
			return callerIP, retSP + 1, ctlReload
		case genModeNext, genModeSend:
			st.raiseExc = val.NewStopIteration(retVal)
			return callerIP - 1, retSP, ctlRaise
		}
		return exitIP, retSP - 1, ctlReload
	}
	if st.activeCoro != nil && st.frameIdx == st.genFrameIdx {
		coro := st.activeCoro
		coro.RetVal = retVal
		coro.State = val.CoroClosed
		coro.SP = sp
		vm.releaseCoroutineStack(coro)
		if st.frameIdx == st.startFrameIdx {
			st.result = retVal
			return ip, sp, ctlExit
		}
		st.frameIdx--
		callerIdx := st.frameIdx
		caller := &vm.frames[callerIdx]
		if len(vm.excStack) > int(caller.excStackLen) {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		aux := vm.frameAuxAt(callerIdx)
		mode := aux.genMode
		callerIP := caller.ip
		retSP := caller.retSP
		vm.restoreCallerFrame(st, caller)
		if mode == genModeAwait || mode == genModeYieldFrom {
			vm.releaseEphemeralCoroutine(coro)
			st.stack[retSP-1] = retVal
			return callerIP + 1, retSP, ctlReload
		}
		if mode == genModeCoroSend {
			st.stack[retSP] = retVal
			return callerIP, retSP + 1, ctlReload
		}
		st.stack[retSP-1] = retVal
		return callerIP + 1, retSP, ctlReload
	}
	if st.frameIdx == st.startFrameIdx {
		st.result = retVal
		return ip, sp, ctlExit
	}
	st.frameIdx--
	callerIdx := st.frameIdx
	caller := &vm.frames[callerIdx]
	if (caller.flags & frameFlagInit) != 0 {
		inst := caller.initInstance
		caller.flags &^= frameFlagInit
		caller.initInstance = val.Value{}
		if !retVal.IsNone() {
			st.raiseExc = val.NewTypeError("__init__() should return None, not '" + retVal.Type().Name + "'")
			return ip, sp, ctlRaise
		}
		retVal = inst
	}
	if (caller.flags & frameFlagCmp) != 0 {
		aux := &vm.frameAux[callerIdx]
		cmpLeft, cmpRight, cmpOp := aux.cmpLeft, aux.cmpRight, aux.cmpOp
		caller.flags &^= frameFlagCmp
		aux.cmpLeft = val.Value{}
		aux.cmpRight = val.Value{}
		if retVal.IsNotImplemented() {
			if len(vm.excStack) > int(caller.excStackLen) {
				vm.excStack = vm.excStack[:caller.excStackLen]
			}
			vm.restoreNormalCallerFrame(st, callerIdx, caller)
			ip = caller.ip
			st.ip = ip
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
	if len(vm.excStack) > int(caller.excStackLen) {
		vm.excStack = vm.excStack[:caller.excStackLen]
	}
	if caller.cache == st.cache && (caller.flags&frameFlagHasAux) == 0 && st.cells == nil && st.curLocals.IsNone() {
		if (caller.flags & frameFlagSameGlobals) == 0 {
			st.globals = caller.globals
		}
		ip = caller.ip
		st.base = caller.baseOffset
		vm.activeBaseOffset = st.base
		vm.activeFrameIdx = st.frameIdx
		stack[caller.retSP] = retVal
		return ip, caller.retSP + 1, ctlReload
	}
	vm.restoreNormalCallerFrame(st, callerIdx, caller)
	ip = caller.ip
	stack[caller.retSP] = retVal
	return ip, caller.retSP + 1, ctlReload
}

func (vm *VM) restoreNormalCallerFrame(st *loopState, callerIdx int, caller *CallFrame) {
	cc := caller.cache
	co := cc.co
	st.curCO = co
	st.cache = cc
	st.codePtr = cc.codePtr
	st.codeLen = cc.codeLen
	st.consts = cc.frameConsts(co)
	st.base = caller.baseOffset
	if (caller.flags & frameFlagHasAux) != 0 {
		aux := &vm.frameAux[callerIdx]
		st.cells = aux.cells
		st.cellPtrs = aux.cellPtrs
		st.curLocals = aux.locals
		aux.cells = nil
		aux.cellPtrs = nil
		aux.locals = val.Value{}
		caller.flags &^= frameFlagHasAux
	} else {
		st.cells = nil
		st.cellPtrs = nil
		st.curLocals = val.NoneVal()
	}
	if (caller.flags & frameFlagSameGlobals) == 0 {
		st.globals = caller.globals
	}
	vm.activeCO = co
	vm.activeBaseOffset = st.base
	if vm.activeCellPtrs != nil || st.cellPtrs != nil {
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
	vm.activeFrameIdx = st.frameIdx
}
