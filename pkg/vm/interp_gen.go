package vm

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

func (vm *VM) restoreCallerFrame(st *loopState, caller *CallFrame) {
	st.curCO = caller.co
	if caller.cache != nil {
		st.cache = caller.cache
	} else {
		st.cache = vm.cacheFor(st.curCO)
	}
	if st.cache != nil && len(st.cache.active) > 0 {
		st.codePtr = unsafe.Pointer(unsafe.SliceData(st.cache.active))
		st.codeLen = len(st.cache.active)
	} else {
		st.codePtr = caller.codePtr
		st.codeLen = caller.codeLen
	}
	st.consts = caller.consts
	st.base = caller.baseOffset
	st.cells = caller.cells
	st.cellPtrs = caller.cellPtrs
	st.globals = caller.globals
	st.curLocals = caller.locals
	st.stack = caller.stack
	st.stackIsVM = caller.stackIsVM
	vm.execCtx.Stack = caller.stack
	st.activeGen = caller.activeGen
	st.genFrameIdx = caller.genFrameIdx
	caller.genForIter = false
	caller.genYieldFrom = false
	caller.stack = nil
	caller.activeGen = nil
	vm.activeCO = st.curCO
	vm.activeBaseOffset = st.base
	vm.activeCells = st.cells
	vm.activeCellPtrs = st.cellPtrs
	vm.activeFrameIdx = st.frameIdx
}

// pushInlineGenerator pushes gen onto vm.frames directly inside the active
// runLoopWithGlobals without recursing into resumeGenerator/runFrame.
//
//go:noinline
func (vm *VM) pushInlineGenerator(st *loopState, gen *val.PyGenerator, callerIP, callerSP, retSP, jumpTarget int, isYieldFrom bool, sentVal val.Value) (int, int, loopCtl) {
	switch gen.State {
	case val.GenClosed:
		if isYieldFrom {
			st.stack[callerSP-1] = gen.RetVal
			return callerIP + 1, callerSP, ctlReload
		}
		return jumpTarget, callerSP - 1, ctlNext
	case val.GenRunning:
		st.raiseExc = val.NewValueError("generator already executing")
		return callerIP, callerSP, ctlRaise
	case val.GenCreated:
		if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
			st.raiseExc = val.NewTypeError("can't send non-None value to a just-started generator")
			return callerIP, callerSP, ctlRaise
		}
	case val.GenSuspended:
		gen.LastSent = sentVal
		callee := AsCodeObject(gen.Code)
		if gen.IP < len(callee.Code) && DecodeOpcode(callee.Code[gen.IP]) != OP_YIELD_FROM {
			gen.Stack[gen.SP] = sentVal
			gen.SP++
		}
	}

	gen.State = val.GenRunning
	callee := AsCodeObject(gen.Code)
	minCap := max(gen.Base+256, gen.Base+callee.NumLocals+callee.MaxStackSize+64)
	if minCap > len(gen.Stack) {
		gen.Stack = ensureStack(gen.Stack, minCap)
	}
	if len(gen.Cells) > 0 && len(gen.CellPtrs) == 0 {
		ptrs := vm.acquireGenCellPtrs(len(gen.Cells))
		for i, c := range gen.Cells {
			ptrs[i] = c.AsCell()
		}
		gen.CellPtrs = ptrs
	}

	if st.frameIdx+2 > len(vm.frames) {
		vm.ensureFrameCapacity(st.frameIdx + 2)
	}
	saved := &vm.frames[st.frameIdx]
	saved.co = st.curCO
	saved.codePtr = st.codePtr
	saved.codeLen = st.codeLen
	saved.consts = st.consts
	if isYieldFrom {
		saved.ip = callerIP
	} else {
		saved.ip = callerIP + 1
	}
	saved.baseOffset = st.base
	saved.retSP = retSP
	saved.cells = st.cells
	saved.cellPtrs = st.cellPtrs
	saved.globals = st.globals
	saved.locals = st.curLocals
	saved.cache = st.cache
	saved.excStackLen = len(vm.excStack)
	saved.stackIsVM = st.stackIsVM
	saved.stack = st.stack
	saved.activeGen = st.activeGen
	saved.genFrameIdx = st.genFrameIdx
	saved.genForIter = !isYieldFrom
	saved.genYieldFrom = isYieldFrom
	saved.genExitIP = jumpTarget
	saved.initInstance = val.Value{}
	saved.cmpActive = false

	st.curLocals = val.NoneVal()
	st.frameIdx++
	if st.frameIdx > vm.maxFrameIdx {
		vm.maxFrameIdx = st.frameIdx
	}
	st.genFrameIdx = st.frameIdx
	st.activeGen = gen
	st.stack = gen.Stack
	st.stackIsVM = false
	vm.execCtx.Stack = gen.Stack
	st.curCO = callee
	st.cache = vm.cacheFor(callee)
	st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, callee, st.cache)
		st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	}
	st.consts = callee.Consts
	st.base = gen.Base
	st.cells = gen.Cells
	st.cellPtrs = gen.CellPtrs
	if gen.Globals != nil {
		st.globals = gen.Globals
	} else {
		st.globals = vm.globals
	}
	vm.activeCO = st.curCO
	vm.activeBaseOffset = st.base
	vm.activeCells = st.cells
	vm.activeCellPtrs = st.cellPtrs
	vm.activeFrameIdx = st.frameIdx
	return gen.IP, gen.SP, ctlReload
}

//go:noinline
func (vm *VM) opForIterGen(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	it := st.stack[sp-1]
	if it.IsGenerator() {
		jumpTarget := int(uint16(inst))
		gen := it.AsGenerator()
		if gen.State == val.GenClosed {
			return jumpTarget, sp - 1, ctlNext
		}
		if vm.activeJoinGen == nil {
			return vm.pushInlineGenerator(st, gen, ip, sp, sp, jumpTarget, false, val.NoneVal())
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_FOR_ITER))
	return vm.opForIterSlow(st, ReplaceOpcode(inst, OP_FOR_ITER), ip, sp)
}

func (vm *VM) suspendActiveGen(st *loopState, yieldVal val.Value, resumeIP, resumeSP int) (int, int, loopCtl) {
	st.activeGen.YieldVal = yieldVal
	st.activeGen.State = val.GenSuspended
	st.activeGen.Stack = st.stack
	st.activeGen.SP = resumeSP
	st.activeGen.IP = resumeIP
	for st.frameIdx == st.genFrameIdx && st.frameIdx > st.startFrameIdx {
		if curShadow := vm.frames[st.frameIdx].shadowFrame; curShadow != nil {
			curShadow.Detach()
			vm.frames[st.frameIdx].shadowFrame = nil
		}
		st.frameIdx--
		caller := &vm.frames[st.frameIdx]
		if len(vm.excStack) > caller.excStackLen {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		if caller.genForIter {
			retSP := caller.retSP
			callerIP := caller.ip
			vm.restoreCallerFrame(st, caller)
			st.stack[retSP] = yieldVal
			return callerIP, retSP + 1, ctlReload
		}
		if caller.genYieldFrom {
			retSP := caller.retSP
			callerIP := caller.ip
			vm.restoreCallerFrame(st, caller)
			if st.activeGen != nil {
				st.activeGen.YieldVal = yieldVal
				st.activeGen.State = val.GenSuspended
				st.activeGen.Stack = st.stack
				st.activeGen.SP = retSP
				st.activeGen.IP = callerIP
				continue
			}
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = yieldVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = retSP
				st.activeCoro.IP = callerIP
				st.result = yieldVal
				return callerIP, retSP, ctlExit
			}
		}
		break
	}
	st.result = yieldVal
	return resumeIP, resumeSP, ctlExit
}

//go:noinline
func (vm *VM) opYieldValue(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	sp--
	yieldVal := stack[sp]
	if st.activeGen != nil {
		if vm.activeJoinGen == st.activeGen && st.frameIdx == st.startFrameIdx {
			vm.appendJoinItem(yieldVal)
			stack[sp] = val.NoneVal()
			sp++
			ip++
			return ip, sp, ctlNext
		}
		return vm.suspendActiveGen(st, yieldVal, ip+1, sp)
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opYieldFrom(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	subiter := stack[sp-1]
	if !subiter.IsIter() && !subiter.IsGenerator() && !subiter.IsCoroutine() {
		if tpIter := subiter.Type().Slots.TpIter; tpIter != nil {
			subiter = tpIter(&vm.execCtx, subiter)
			stack[sp-1] = subiter
		}
	}
	sentVal := val.NoneVal()
	if st.activeGen != nil {
		sentVal = st.activeGen.LastSent
		st.activeGen.LastSent = val.NoneVal()
	} else if st.activeCoro != nil {
		sentVal = st.activeCoro.LastSent
		st.activeCoro.LastSent = val.NoneVal()
	}
	if st.activeGen != nil && vm.activeJoinGen == st.activeGen && st.frameIdx == st.startFrameIdx {
		for {
			var nextVal val.Value
			var status val.IterStatus
			if subiter.IsGenerator() {
				nextVal, status = subiter.AsGenerator().Send(&vm.execCtx, val.NoneVal())
			} else if subiter.IsCoroutine() {
				nextVal, status = vm.resumeCoroutine(&vm.execCtx, subiter.AsCoroutine(), val.NoneVal())
			} else if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status = tpIterNext(&vm.execCtx, subiter)
			} else {
				break
			}
			if status == val.IterYielded {
				vm.appendJoinItem(nextVal)
				continue
			}
			if status == val.IterExhausted {
				retVal := val.NoneVal()
				if subiter.IsGenerator() {
					retVal = subiter.AsGenerator().RetVal
				} else if subiter.IsCoroutine() {
					retVal = subiter.AsCoroutine().RetVal
					vm.releaseCoroutine(subiter.AsCoroutine())
				}
				stack[sp-1] = retVal
				break
			}
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			break
		}
		ip++
		return ip, sp, ctlReload
	}
	if subiter.IsGenerator() && vm.activeJoinGen == nil {
		return vm.pushInlineGenerator(st, subiter.AsGenerator(), ip, sp, sp, 0, true, sentVal)
	}
	var nextVal val.Value
	var status val.IterStatus
	if subiter.IsGenerator() {
		nextVal, status = subiter.AsGenerator().Send(&vm.execCtx, sentVal)
	} else if subiter.IsCoroutine() {
		nextVal, status = vm.resumeCoroutine(&vm.execCtx, subiter.AsCoroutine(), sentVal)
	} else if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
		nextVal, status = tpIterNext(&vm.execCtx, subiter)
	} else {
		ip++
		return ip, sp, ctlReload
	}

	switch {
	case status == val.IterYielded:
		if st.activeGen != nil {
			return vm.suspendActiveGen(st, nextVal, ip, sp)
		}
		if st.activeCoro != nil {
			st.activeCoro.YieldVal = nextVal
			st.activeCoro.State = val.CoroSuspended
			st.activeCoro.SP = sp
			st.activeCoro.IP = ip
			st.result = nextVal
			return ip, sp, ctlExit
		}
	case status == val.IterExhausted:
		retVal := val.NoneVal()
		if subiter.IsGenerator() {
			retVal = subiter.AsGenerator().RetVal
		} else if subiter.IsCoroutine() {
			retVal = subiter.AsCoroutine().RetVal
			vm.releaseCoroutine(subiter.AsCoroutine())
		}
		stack[sp-1] = retVal
		ip++
	case vm.execCtx.HasException():
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opGetAwaitable(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	top := stack[sp-1]
	if top.IsCoroutine() || top.IsFuture() || top.IsTask() {
		ip++
		return ip, sp, ctlNext
	}
	if top.IsInstance() {
		inst := top.AsInstance()
		if m, ok := inst.GetAttr(nameAwait); ok {
			stack[sp-1] = vm.Vectorcall(m, nil, nil)
			ip++
			return ip, sp, ctlNext
		}
	}
	if tpIter := top.Type().Slots.TpIter; tpIter != nil {
		stack[sp-1] = tpIter(&vm.execCtx, top)
		ip++
		return ip, sp, ctlNext
	}
	exc := val.NewTypeError(fmt.Sprintf("object %s can't be used in 'await' expression", top.Type().Name))
	st.raiseExc = exc
	return ip, sp, ctlRaise

}

//go:noinline
func (vm *VM) opAwait(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	aw := stack[sp-1]
	if aw.IsFuture() || aw.IsTask() {
		fut := aw.AsFuture()
		if aw.IsTask() {
			fut = &aw.AsTask().Future
		}
		if fut.IsDone() {
			res, exc := fut.Result()
			if !exc.IsNone() && exc.HeaderPtr() != nil {
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[sp-1] = res
			ip++
			return ip, sp, ctlReload
		}
		if st.activeCoro != nil {
			st.activeCoro.YieldVal = aw
			st.activeCoro.State = val.CoroSuspended
			st.activeCoro.SP = sp
			st.activeCoro.IP = ip
			st.result = aw
			return ip, sp, ctlExit
		}
	} else if aw.IsCoroutine() {
		childCoro := aw.AsCoroutine()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		}
		_, status := vm.resumeCoroutine(&vm.execCtx, childCoro, sentVal)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = childCoro.RetVal
			vm.releaseCoroutine(childCoro)
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = childCoro.YieldVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = sp
				st.activeCoro.IP = ip
				st.result = childCoro.YieldVal
				return ip, sp, ctlExit
			}
		default:
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
	} else if aw.IsGenerator() {
		subiter := aw.AsGenerator()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		}
		nextVal, status := subiter.Send(&vm.execCtx, sentVal)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = subiter.RetVal
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = nextVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = sp
				st.activeCoro.IP = ip
				st.result = nextVal
				return ip, sp, ctlExit
			}
		default:
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
	} else if tpIterNext := aw.Type().Slots.TpIterNext; tpIterNext != nil {
		nextVal, status := tpIterNext(&vm.execCtx, aw)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = nextVal
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = nextVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = sp
				st.activeCoro.IP = ip
				st.result = nextVal
				return ip, sp, ctlExit
			}
		}
	} else {
		ip++
		return ip, sp, ctlReload
	}
	return ip, sp, ctlReload
}
