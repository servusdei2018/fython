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
	st.activeCoro = caller.activeCoro
	st.genFrameIdx = caller.genFrameIdx
	caller.genMode = genModeNone
	caller.genDefault = val.Value{}
	caller.stack = nil
	caller.activeGen = nil
	caller.activeCoro = nil
	vm.activeCO = st.curCO
	vm.activeBaseOffset = st.base
	vm.activeCells = st.cells
	vm.activeCellPtrs = st.cellPtrs
	vm.activeFrameIdx = st.frameIdx
}

func (vm *VM) saveAndPushInlineFrame(
	st *loopState,
	callee *CodeObject,
	childStack []val.Value,
	childBase int,
	childCells []val.Value,
	childCellPtrs []*val.PyCell,
	childGlobals *val.PyDict,
	callerIP, retSP, jumpTarget int,
	mode uint8,
	defaultVal val.Value,
	gen *val.PyGenerator,
	coro *val.PyCoroutine,
) {
	if st.frameIdx+2 > len(vm.frames) {
		vm.ensureFrameCapacity(st.frameIdx + 2)
	}
	saved := &vm.frames[st.frameIdx]
	saved.co = st.curCO
	saved.codePtr = st.codePtr
	saved.codeLen = st.codeLen
	saved.consts = st.consts
	if mode == genModeYieldFrom || mode == genModeAwait {
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
	saved.activeCoro = st.activeCoro
	saved.genFrameIdx = st.genFrameIdx
	saved.genMode = mode
	saved.genDefault = defaultVal
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
	st.activeCoro = coro
	st.stack = childStack
	st.stackIsVM = false
	vm.execCtx.Stack = childStack
	st.curCO = callee
	st.cache = vm.cacheFor(callee)
	st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, callee, st.cache)
		st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	}
	st.consts = callee.Consts
	st.base = childBase
	st.cells = childCells
	st.cellPtrs = childCellPtrs
	if childGlobals != nil {
		st.globals = childGlobals
	} else {
		st.globals = vm.globals
	}
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
func (vm *VM) pushInlineGenerator(st *loopState, gen *val.PyGenerator, callerIP, callerSP, retSP, jumpTarget int, mode uint8, defaultVal, sentVal val.Value) (int, int, loopCtl) {
	switch gen.State {
	case val.GenClosed:
		switch mode {
		case genModeYieldFrom, genModeAwait:
			st.stack[callerSP-1] = gen.RetVal
			vm.releaseGenerator(gen)
			return callerIP + 1, callerSP, ctlReload
		case genModeForIter:
			vm.releaseGenerator(gen)
			return jumpTarget, callerSP - 1, ctlNext
		case genModeNextDefault:
			st.stack[retSP] = defaultVal
			return callerIP + 1, retSP + 1, ctlReload
		case genModeNext, genModeSend:
			st.raiseExc = val.NewStopIteration(val.NoneVal())
			return callerIP, callerSP, ctlRaise
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

	vm.saveAndPushInlineFrame(
		st, callee, gen.Stack, gen.Base, gen.Cells, gen.CellPtrs, gen.Globals,
		callerIP, retSP, jumpTarget, mode, defaultVal, gen, nil,
	)
	return gen.IP, gen.SP, ctlReload
}

// pushInlineCoroutine pushes coro onto vm.frames directly inside the active
// runLoopWithGlobals without recursing into resumeCoroutine/runFrame.
//
//go:noinline
func (vm *VM) pushInlineCoroutine(st *loopState, coro *val.PyCoroutine, callerIP, callerSP, retSP int, mode uint8, sentVal val.Value) (int, int, loopCtl) {
	switch coro.State {
	case val.CoroClosed:
		switch mode {
		case genModeAwait, genModeYieldFrom:
			st.stack[retSP-1] = coro.RetVal
			vm.releaseEphemeralCoroutine(coro)
			return callerIP + 1, retSP, ctlReload
		case genModeCoroSend:
			st.stack[retSP] = val.NoneVal()
			return callerIP + 1, retSP + 1, ctlReload
		}
		return callerIP + 1, retSP, ctlReload
	case val.CoroRunning:
		st.raiseExc = val.NewValueError("coroutine already executing")
		return callerIP, callerSP, ctlRaise
	case val.CoroCreated:
		if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
			st.raiseExc = val.NewTypeError("can't send non-None value to a just-started coroutine")
			return callerIP, callerSP, ctlRaise
		}
	case val.CoroSuspended:
		coro.LastSent = sentVal
		callee := AsCodeObject(coro.Code)
		if coro.IP < len(callee.Code) {
			op := DecodeOpcode(callee.Code[coro.IP])
			if op != OP_AWAIT && op != OP_YIELD_FROM {
				coro.Stack[coro.SP] = sentVal
				coro.SP++
			}
		}
	}

	coro.State = val.CoroRunning
	callee := AsCodeObject(coro.Code)
	minCap := max(coro.Base+256, coro.Base+callee.NumLocals+max(callee.MaxStackSize, len(callee.Code))+64)
	if minCap > len(coro.Stack) {
		s, newCls := vm.acquireGeneratorStack(minCap)
		copy(s, coro.Stack)
		old := coro.Stack
		coro.Stack = s
		coro.PoolClass = newCls
		vm.releaseStackSlice(old)
	}
	if len(coro.Cells) > 0 && len(coro.CellPtrs) == 0 {
		ptrs := vm.acquireGenCellPtrs(len(coro.Cells))
		for i, c := range coro.Cells {
			ptrs[i] = c.AsCell()
		}
		coro.CellPtrs = ptrs
	}

	vm.saveAndPushInlineFrame(
		st, callee, coro.Stack, coro.Base, coro.Cells, coro.CellPtrs, coro.Globals,
		callerIP, retSP, 0, mode, val.Value{}, nil, coro,
	)
	return coro.IP, coro.SP, ctlReload
}

//go:noinline
func (vm *VM) opForIterGen(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	it := st.stack[sp-1]
	if it.IsGenerator() {
		jumpTarget := int(uint16(inst))
		gen := it.AsGenerator()
		if gen.State == val.GenClosed {
			vm.releaseGenerator(gen)
			return jumpTarget, sp - 1, ctlNext
		}
		if vm.activeJoinGen == nil {
			return vm.pushInlineGenerator(st, gen, ip, sp, sp, jumpTarget, genModeForIter, val.Value{}, val.NoneVal())
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_FOR_ITER))
	return vm.opForIterSlow(st, ReplaceOpcode(inst, OP_FOR_ITER), ip, sp)
}

func (vm *VM) suspendActiveChain(st *loopState, yieldVal val.Value, resumeIP, resumeSP int) (int, int, loopCtl) {
	if st.activeGen != nil {
		st.activeGen.YieldVal = yieldVal
		st.activeGen.State = val.GenSuspended
		st.activeGen.Stack = st.stack
		st.activeGen.SP = resumeSP
		st.activeGen.IP = resumeIP
	} else if st.activeCoro != nil {
		st.activeCoro.YieldVal = yieldVal
		st.activeCoro.State = val.CoroSuspended
		st.activeCoro.Stack = st.stack
		st.activeCoro.SP = resumeSP
		st.activeCoro.IP = resumeIP
	}
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
		switch caller.genMode {
		case genModeForIter, genModeNext, genModeNextDefault, genModeSend, genModeCoroSend:
			retSP := caller.retSP
			callerIP := caller.ip
			vm.restoreCallerFrame(st, caller)
			st.stack[retSP] = yieldVal
			return callerIP, retSP + 1, ctlReload
		case genModeYieldFrom, genModeAwait:
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
				st.activeCoro.Stack = st.stack
				st.activeCoro.SP = retSP
				st.activeCoro.IP = callerIP
				continue
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
		return vm.suspendActiveChain(st, yieldVal, ip+1, sp)
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
					subGen := subiter.AsGenerator()
					retVal = subGen.RetVal
					vm.releaseGenerator(subGen)
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
		return vm.pushInlineGenerator(st, subiter.AsGenerator(), ip, sp, sp, 0, genModeYieldFrom, val.Value{}, sentVal)
	}
	if subiter.IsCoroutine() && vm.activeJoinGen == nil {
		return vm.pushInlineCoroutine(st, subiter.AsCoroutine(), ip, sp, sp, genModeYieldFrom, sentVal)
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
		if st.activeGen != nil || st.activeCoro != nil {
			return vm.suspendActiveChain(st, nextVal, ip, sp)
		}
	case status == val.IterExhausted:
		retVal := val.NoneVal()
		if subiter.IsGenerator() {
			subGen := subiter.AsGenerator()
			retVal = subGen.RetVal
			vm.releaseGenerator(subGen)
		} else if subiter.IsCoroutine() {
			coro := subiter.AsCoroutine()
			retVal = coro.RetVal
			vm.releaseEphemeralCoroutine(coro)
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
			if st.activeCoro != nil {
				st.activeCoro.LastSent = val.NoneVal()
			}
			stack[sp-1] = res
			ip++
			return ip, sp, ctlReload
		}
		if st.activeCoro != nil {
			return vm.suspendActiveChain(st, aw, ip, sp)
		}
	} else if aw.IsCoroutine() {
		childCoro := aw.AsCoroutine()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		}
		return vm.pushInlineCoroutine(st, childCoro, ip, sp, sp, genModeAwait, sentVal)
	} else if aw.IsGenerator() {
		subiter := aw.AsGenerator()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		}
		if vm.activeJoinGen == nil {
			return vm.pushInlineGenerator(st, subiter, ip, sp, sp, 0, genModeAwait, val.Value{}, sentVal)
		}
		nextVal, status := subiter.Send(&vm.execCtx, sentVal)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = subiter.RetVal
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				return vm.suspendActiveChain(st, nextVal, ip, sp)
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
				return vm.suspendActiveChain(st, nextVal, ip, sp)
			}
		}
	} else {
		ip++
		return ip, sp, ctlReload
	}
	return ip, sp, ctlReload
}
