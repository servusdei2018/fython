package vm

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

func (vm *VM) restoreCallerFrame(st *loopState, caller *CallFrame) {
	callerIdx := st.frameIdx
	cc := caller.cache
	co := cc.co
	st.curCO = co
	st.cache = cc
	if cc != nil && len(cc.active) > 0 {
		st.codePtr = unsafe.Pointer(unsafe.SliceData(cc.active))
		st.codeLen = len(cc.active)
	} else if cc != nil {
		st.codePtr = cc.codePtr
		st.codeLen = cc.codeLen
	}
	st.consts = cc.frameConsts(co)
	st.base = caller.baseOffset
	aux := vm.frameAuxAt(callerIdx)
	st.cells = aux.cells
	st.cellPtrs = aux.cellPtrs
	st.globals = caller.globals
	st.curLocals = aux.locals
	st.stack = aux.stack
	st.stackIsVM = caller.stackIsVM()
	vm.execCtx.Stack = aux.stack
	st.activeGen = aux.activeGen
	st.activeCoro = aux.activeCoro
	st.genFrameIdx = aux.genFrameIdx
	aux.cells = nil
	aux.cellPtrs = nil
	aux.locals = val.Value{}
	aux.genMode = genModeNone
	aux.genDefault = val.Value{}
	aux.stack = nil
	aux.activeGen = nil
	aux.activeCoro = nil
	caller.flags &^= frameFlagGenOrCoro | frameFlagHasAux
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
	frameIdx := st.frameIdx
	saved := &vm.frames[frameIdx]
	saved.cache = st.cache
	if mode == genModeYieldFrom || mode == genModeAwait {
		saved.ip = callerIP
	} else {
		saved.ip = callerIP + 1
	}
	saved.baseOffset = st.base
	saved.retSP = retSP
	saved.globals = st.globals
	saved.excStackLen = int32(len(vm.excStack))
	saved.initInstance = val.Value{}
	flags := (saved.flags & frameFlagHasShadow) | frameFlagHasAux | frameFlagGenOrCoro
	if st.stackIsVM {
		flags |= frameFlagStackIsVM
	}
	saved.flags = flags
	aux := vm.frameAuxAt(frameIdx)
	aux.cells = st.cells
	aux.cellPtrs = st.cellPtrs
	aux.locals = st.curLocals
	aux.stack = st.stack
	aux.activeGen = st.activeGen
	aux.activeCoro = st.activeCoro
	aux.genFrameIdx = st.genFrameIdx
	aux.genMode = mode
	aux.genDefault = defaultVal
	aux.genExitIP = jumpTarget

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
	st.consts = st.cache.frameConsts(callee)
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

const maxStrJoinScratchCap = 65536

func (vm *VM) acquireStrJoinState(sep val.Value) *strJoinState {
	var js *strJoinState
	if n := len(vm.strJoinPool); n > 0 {
		js = vm.strJoinPool[n-1]
		vm.strJoinPool[n-1] = nil
		vm.strJoinPool = vm.strJoinPool[:n-1]
	} else {
		js = &strJoinState{}
	}
	if vm.strJoinScratchLen > 0 {
		vm.strJoinScratchLen--
		js.buf = vm.strJoinScratch[vm.strJoinScratchLen][:0]
		vm.strJoinScratch[vm.strJoinScratchLen] = nil
	} else {
		js.buf = nil
	}
	js.sep = sep
	js.sepLen = uint8(min(sep.StringByteLen(), 255))
	js.first = true
	js.count = 0
	return js
}

func (vm *VM) releaseStrJoinState(js *strJoinState) {
	if js == nil {
		return
	}
	if buf := js.buf; cap(buf) > 0 && cap(buf) <= maxStrJoinScratchCap && vm.strJoinScratchLen < uint8(len(vm.strJoinScratch)) {
		vm.strJoinScratch[vm.strJoinScratchLen] = buf[:0]
		vm.strJoinScratchLen++
	}
	js.buf = nil
	js.sep = val.Value{}
	js.sepLen = 0
	js.first = true
	js.count = 0
	if len(vm.strJoinPool) < 32 {
		vm.strJoinPool = append(vm.strJoinPool, js)
	}
}

func (js *strJoinState) appendValue(v val.Value) bool {
	if !v.IsAnyString() {
		return false
	}
	if cap(js.buf) == 0 {
		js.buf = make([]byte, 0, 256)
	}
	if !js.first && js.sepLen > 0 {
		js.buf, _ = val.AppendValueBytes(js.buf, js.sep)
	}
	js.first = false
	js.count++
	js.buf, _ = val.AppendValueBytes(js.buf, v)
	return true
}

func (js *strJoinState) materialize() val.Value {
	if len(js.buf) == 0 {
		return val.SmallStringVal("")
	}
	return val.NewStringFromBytes(js.buf)
}

func (vm *VM) acquireCollectState(mode uint8) *collectState {
	var cs *collectState
	if n := len(vm.collectPool); n > 0 {
		cs = vm.collectPool[n-1]
		vm.collectPool[n-1] = nil
		vm.collectPool = vm.collectPool[:n-1]
	} else {
		cs = &collectState{}
	}
	switch mode {
	case genModeCollectSet:
		cs.set = val.NewSetVal().AsSet()
	case genModeCollectDict:
		cs.dict = val.NewDictVal(8).AsDict()
	case genModeCollectList, genModeCollectTuple:
		if cap(cs.items) == 0 {
			cs.items = make([]val.Value, 0, 16)
		}
	}
	return cs
}

func (vm *VM) releaseCollectState(cs *collectState) {
	if cs == nil {
		return
	}
	cs.set = nil
	cs.list = nil
	cs.dict = nil
	cs.acc = val.Value{}
	cs.hasAcc = false
	cs.isSlice = false
	cs.sliceCur = 0
	cs.sliceNext = 0
	cs.sliceStop = 0
	cs.sliceStep = 0
	cs.isliceIter = nil
	if cs.items != nil {
		if cap(cs.items) <= 4096 {
			clear(cs.items[:cap(cs.items)])
			cs.items = cs.items[:0]
		} else {
			cs.items = nil
		}
	}
	if len(vm.collectPool) < 32 {
		vm.collectPool = append(vm.collectPool, cs)
	}
}

func (cs *collectState) appendValue(ctx *val.ExecContext, v val.Value, mode uint8, hasTrackedViews bool) (val.Value, val.Value, bool, bool) {
	if cs.isSlice {
		if cs.sliceCur < cs.sliceNext {
			cs.sliceCur++
			if cs.isliceIter != nil {
				cs.isliceIter.Cur = cs.sliceCur
			}
			return val.Value{}, val.Value{}, false, true
		}
		cs.sliceCur++
		const maxInt64 = int64(^uint64(0) >> 1)
		if cs.sliceNext > maxInt64-cs.sliceStep {
			cs.sliceNext = maxInt64
			cs.sliceStop = cs.sliceCur
		} else {
			cs.sliceNext += cs.sliceStep
		}
		if cs.isliceIter != nil {
			cs.isliceIter.Cur = cs.sliceCur
			cs.isliceIter.NextIdx = cs.sliceNext
		}
	}

	switch mode {
	case genModeAny:
		if v.IsTruthy() {
			if cs.isliceIter != nil && cs.sliceStop >= 0 && cs.sliceNext >= cs.sliceStop {
				cs.isliceIter.Cur = cs.isliceIter.Stop
			}
			return val.Value{}, val.BoolVal(true), true, true
		}
	case genModeAll:
		if !v.IsTruthy() {
			if cs.isliceIter != nil && cs.sliceStop >= 0 && cs.sliceNext >= cs.sliceStop {
				cs.isliceIter.Cur = cs.isliceIter.Stop
			}
			return val.Value{}, val.BoolVal(false), true, true
		}
	case genModeSum:
		if cs.acc.IsInt64() && v.IsInt64() {
			ai, bi := cs.acc.AsInt64(), v.AsInt64()
			sum := ai + bi
			if ((ai ^ sum) & (bi ^ sum)) >= 0 {
				cs.acc = val.Int64Val(sum)
				break
			}
		} else if (cs.acc.IsFloat64() || cs.acc.IsInt64()) && (v.IsFloat64() || v.IsInt64()) {
			var af, bf float64
			if cs.acc.IsFloat64() {
				af = cs.acc.AsFloat64()
			} else {
				af = float64(cs.acc.AsInt64())
			}
			if v.IsFloat64() {
				bf = v.AsFloat64()
			} else {
				bf = float64(v.AsInt64())
			}
			cs.acc = val.Float64Val(af + bf)
			break
		}
		res := val.AddCtx(ctx, cs.acc, v)
		if ctx.HasException() {
			exc := ctx.ActiveException
			ctx.ClearException()
			return exc, val.Value{}, false, false
		}
		cs.acc = res
	case genModeMin, genModeMax:
		switch {
		case !cs.hasAcc:
			val.ClearEphemeral(v)
			cs.acc = v
			cs.hasAcc = true
		case v.IsInt64() && cs.acc.IsInt64():
			if (mode == genModeMin && v.AsInt64() < cs.acc.AsInt64()) ||
				(mode == genModeMax && v.AsInt64() > cs.acc.AsInt64()) {
				cs.acc = v
			}
		case v.IsFloat64() && cs.acc.IsFloat64():
			if (mode == genModeMin && v.AsFloat64() < cs.acc.AsFloat64()) ||
				(mode == genModeMax && v.AsFloat64() > cs.acc.AsFloat64()) {
				cs.acc = v
			}
		default:
			cmpOp := val.CmpLT
			if mode == genModeMax {
				cmpOp = val.CmpGT
			}
			cmp := val.RichCompare(ctx, v, cs.acc, cmpOp)
			if ctx.HasException() {
				exc := ctx.ActiveException
				ctx.ClearException()
				return exc, val.Value{}, false, false
			}
			if cmp.IsTruthy() {
				val.ClearEphemeral(v)
				cs.acc = v
			}
		}
	case genModeCollectDict:
		var k, valItem val.Value
		if v.IsTuple() {
			items := v.AsTuple().Items
			if len(items) != 2 {
				return val.NewValueError(fmt.Sprintf("dictionary update sequence element #%d has length %d; 2 is required", cs.dict.Len(), len(items))), val.Value{}, false, false
			}
			k, valItem = items[0], items[1]
		} else {
			items := val.ExtractIterableItems(ctx, v)
			if ctx.HasException() {
				exc := ctx.ActiveException
				ctx.ClearException()
				return exc, val.Value{}, false, false
			}
			if len(items) != 2 {
				return val.NewValueError(fmt.Sprintf("dictionary update sequence element #%d has length %d; 2 is required", cs.dict.Len(), len(items))), val.Value{}, false, false
			}
			k, valItem = items[0], items[1]
		}
		if !k.IsInt64() && !k.IsAnyString() && !k.IsBool() && !k.IsFloat64() && !k.IsNone() {
			if badType, bad := val.IsUnhashableValue(k); bad {
				return val.NewTypeError("unhashable type: '" + badType + "'"), val.Value{}, false, false
			}
		}
		val.ClearEphemeral(k)
		val.DetachStringBuf(k)
		val.ClearEphemeral(valItem)
		val.DetachStringBuf(valItem)
		if hasTrackedViews {
			markViewEscaped(k)
			markViewEscaped(valItem)
		}
		cs.dict.Set(k, valItem)
	case genModeCollectSet:
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		if hasTrackedViews {
			markViewEscaped(v)
		}
		if errExc, ok := cs.set.AddChecked(v); !ok {
			return errExc, val.Value{}, false, false
		}
	case genModeListExtend:
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		if hasTrackedViews {
			markViewEscaped(v)
		}
		cs.list.Append(v)
	default:
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		if hasTrackedViews {
			markViewEscaped(v)
		}
		cs.items = append(cs.items, v)
	}

	if cs.isSlice && cs.sliceStop >= 0 && cs.sliceNext >= cs.sliceStop {
		if cs.isliceIter != nil {
			cs.isliceIter.Cur = cs.isliceIter.Stop
		}
		out, errExc, ok := cs.materializeChecked(mode)
		if !ok {
			return errExc, val.Value{}, false, false
		}
		return val.Value{}, out, true, true
	}
	return val.Value{}, val.Value{}, false, true
}

func (cs *collectState) materializeChecked(mode uint8) (val.Value, val.Value, bool) {
	switch mode {
	case genModeCollectList:
		if len(cs.items) == 0 {
			return val.NewListVal(), val.Value{}, true
		}
		items := cs.items
		cs.items = nil
		return val.NewListFromOwnedSlice(items), val.Value{}, true
	case genModeCollectTuple:
		if len(cs.items) == 0 {
			return val.EmptyTupleVal(), val.Value{}, true
		}
		return val.NewTupleFromSlice(cs.items), val.Value{}, true
	case genModeCollectSet:
		if cs.set == nil {
			return val.NewSetVal(), val.Value{}, true
		}
		out := val.SetVal(cs.set)
		cs.set = nil
		return out, val.Value{}, true
	case genModeCollectDict:
		if cs.dict == nil {
			return val.NewDictVal(0), val.Value{}, true
		}
		out := val.ObjectVal(&cs.dict.Header)
		cs.dict = nil
		return out, val.Value{}, true
	case genModeListExtend:
		cs.list = nil
		return val.NoneVal(), val.Value{}, true
	case genModeAny:
		return val.BoolVal(false), val.Value{}, true
	case genModeAll:
		return val.BoolVal(true), val.Value{}, true
	case genModeSum:
		out := cs.acc
		cs.acc = val.Value{}
		return out, val.Value{}, true
	case genModeMin:
		if !cs.hasAcc {
			return val.Value{}, val.NewValueError("min() iterable argument is empty"), false
		}
		out := cs.acc
		cs.acc = val.Value{}
		return out, val.Value{}, true
	case genModeMax:
		if !cs.hasAcc {
			return val.Value{}, val.NewValueError("max() iterable argument is empty"), false
		}
		out := cs.acc
		cs.acc = val.Value{}
		return out, val.Value{}, true
	}
	return val.NoneVal(), val.Value{}, true
}

// pushListExtendGenerator pushes gen onto vm.frames in genModeListExtend so all
// yielded items append directly to targetList inside runLoopWithGlobals.
//
//go:noinline
func (vm *VM) pushListExtendGenerator(st *loopState, targetList *val.PyList, gen *val.PyGenerator, callerIP, callerSP, retSP int) (int, int, loopCtl) {
	switch gen.State {
	case val.GenClosed:
		st.stack[retSP] = val.NoneVal()
		vm.releaseGenerator(gen)
		return callerIP + 1, retSP + 1, ctlReload
	case val.GenRunning:
		st.raiseExc = val.NewValueError("generator already executing")
		return callerIP, callerSP, ctlRaise
	}
	cs := vm.acquireCollectState(genModeListExtend)
	cs.list = targetList
	ip, sp, ctl := vm.pushInlineGenerator(st, gen, callerIP, callerSP, retSP, 0, genModeListExtend, val.Value{}, val.NoneVal())
	if ctl == ctlRaise {
		vm.releaseCollectState(cs)
		return ip, sp, ctl
	}
	if st.frameIdx > st.startFrameIdx {
		vm.frameAuxAt(st.frameIdx - 1).collectState = cs
	} else {
		vm.releaseCollectState(cs)
	}
	return ip, sp, ctl
}

// pushCollectGenerator pushes gen onto vm.frames in a collection or reduction
// genMode so yielded values drain directly inside runLoopWithGlobals without
// host-stack re-entrancy. If isliceIt != nil, it configures bounded/stepped
// islice(gen, ...) draining inline as well.
//
//go:noinline
func (vm *VM) pushCollectGenerator(st *loopState, mode uint8, gen *val.PyGenerator, callerIP, callerSP, retSP int, defaultVal val.Value, isliceIt *val.PyIter) (int, int, loopCtl) {
	if isliceIt != nil && isliceIt.Stop >= 0 && isliceIt.NextIdx >= isliceIt.Stop {
		isliceIt.Cur = isliceIt.Stop
		switch mode {
		case genModeCollectList:
			st.stack[retSP] = val.NewListVal()
		case genModeCollectTuple:
			st.stack[retSP] = val.EmptyTupleVal()
		case genModeCollectSet:
			st.stack[retSP] = val.NewSetVal()
		case genModeCollectDict:
			st.stack[retSP] = val.NewDictVal(0)
		case genModeAny:
			st.stack[retSP] = val.BoolVal(false)
		case genModeAll:
			st.stack[retSP] = val.BoolVal(true)
		case genModeSum:
			st.stack[retSP] = defaultVal
		case genModeMin:
			st.raiseExc = val.NewValueError("min() iterable argument is empty")
			return callerIP, callerSP, ctlRaise
		case genModeMax:
			st.raiseExc = val.NewValueError("max() iterable argument is empty")
			return callerIP, callerSP, ctlRaise
		}
		return callerIP + 1, retSP + 1, ctlReload
	}
	switch gen.State {
	case val.GenClosed:
		if isliceIt != nil && isliceIt.Stop >= 0 {
			isliceIt.Cur = isliceIt.Stop
		}
		switch mode {
		case genModeCollectList:
			st.stack[retSP] = val.NewListVal()
		case genModeCollectTuple:
			st.stack[retSP] = val.EmptyTupleVal()
		case genModeCollectSet:
			st.stack[retSP] = val.NewSetVal()
		case genModeCollectDict:
			st.stack[retSP] = val.NewDictVal(0)
		case genModeAny:
			st.stack[retSP] = val.BoolVal(false)
		case genModeAll:
			st.stack[retSP] = val.BoolVal(true)
		case genModeSum:
			st.stack[retSP] = defaultVal
		case genModeMin:
			st.raiseExc = val.NewValueError("min() iterable argument is empty")
			return callerIP, callerSP, ctlRaise
		case genModeMax:
			st.raiseExc = val.NewValueError("max() iterable argument is empty")
			return callerIP, callerSP, ctlRaise
		}
		vm.releaseGenerator(gen)
		return callerIP + 1, retSP + 1, ctlReload
	case val.GenRunning:
		st.raiseExc = val.NewValueError("generator already executing")
		return callerIP, callerSP, ctlRaise
	}
	cs := vm.acquireCollectState(mode)
	if mode == genModeSum {
		cs.acc = defaultVal
		cs.hasAcc = true
	}
	if isliceIt != nil {
		cs.isSlice = true
		cs.sliceCur = isliceIt.Cur
		cs.sliceNext = isliceIt.NextIdx
		cs.sliceStop = isliceIt.Stop
		cs.sliceStep = isliceIt.Step
		cs.isliceIter = isliceIt
	}
	ip, sp, ctl := vm.pushInlineGenerator(st, gen, callerIP, callerSP, retSP, 0, mode, defaultVal, val.NoneVal())
	if ctl == ctlRaise {
		vm.releaseCollectState(cs)
		return ip, sp, ctl
	}
	if st.frameIdx > st.startFrameIdx {
		vm.frameAuxAt(st.frameIdx - 1).collectState = cs
	} else {
		vm.releaseCollectState(cs)
	}
	return ip, sp, ctl
}

// pushStrJoinGenerator pushes gen onto vm.frames in genModeStrJoin so all
// yielded strings drain directly into a pooled strings.Builder inside runLoopWithGlobals.
//
//go:noinline
func (vm *VM) pushStrJoinGenerator(st *loopState, sepVal val.Value, gen *val.PyGenerator, callerIP, callerSP, retSP int) (int, int, loopCtl) {
	switch gen.State {
	case val.GenClosed:
		st.stack[retSP] = val.NewStringVal("")
		vm.releaseGenerator(gen)
		return callerIP + 1, retSP + 1, ctlReload
	case val.GenRunning:
		st.raiseExc = val.NewValueError("generator already executing")
		return callerIP, callerSP, ctlRaise
	}
	js := vm.acquireStrJoinState(sepVal)
	ip, sp, ctl := vm.pushInlineGenerator(st, gen, callerIP, callerSP, retSP, 0, genModeStrJoin, val.Value{}, val.NoneVal())
	if ctl == ctlRaise {
		vm.releaseStrJoinState(js)
		return ip, sp, ctl
	}
	if st.frameIdx > st.startFrameIdx {
		vm.frameAuxAt(st.frameIdx - 1).joinState = js
	} else {
		vm.releaseStrJoinState(js)
	}
	return ip, sp, ctl
}

func (vm *VM) findGenDrainCaller(st *loopState) (int, uint8) {
	if st.frameIdx != st.genFrameIdx || st.frameIdx <= st.startFrameIdx {
		return -1, genModeNone
	}
	k := st.frameIdx - 1
	for {
		if k >= len(vm.frameAux) {
			return -1, genModeNone
		}
		mode := vm.frameAux[k].genMode
		switch mode {
		case genModeStrJoin, genModeCollectList, genModeCollectTuple, genModeCollectSet, genModeListExtend,
			genModeCollectDict, genModeAny, genModeAll, genModeSum, genModeMin, genModeMax:
			return k, mode
		case genModeYieldFrom, genModeAwait:
			if k > st.startFrameIdx {
				k--
				continue
			}
		}
		return -1, genModeNone
	}
}

func (vm *VM) earlyStopGenDrainCaller(st *loopState, callerIdx int, resultVal val.Value, ip, sp, ipDelta int) (int, int, loopCtl) {
	outermostGen := st.activeGen
	if st.frameIdx-1 > callerIdx && callerIdx+1 < len(vm.frameAux) && vm.frameAux[callerIdx+1].activeGen != nil {
		outermostGen = vm.frameAux[callerIdx+1].activeGen
	}
	releaseChain := outermostGen != nil && (outermostGen.Header.Flags&val.FlagEphemeralGen) != 0
	if st.activeGen != nil {
		gen := st.activeGen
		if releaseChain && (gen.Header.Flags&val.FlagEphemeralGen) != 0 {
			gen.State = val.GenClosed
			vm.releaseGeneratorStack(gen)
			vm.releaseGenerator(gen)
		} else {
			gen.YieldVal = val.NoneVal()
			gen.State = val.GenSuspended
			gen.Stack = st.stack
			gen.SP = sp
			gen.IP = ip + ipDelta
		}
	}
	for st.frameIdx > callerIdx {
		if (vm.frames[st.frameIdx].flags & frameFlagHasShadow) != 0 {
			vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
			if st.frameIdx < len(vm.frameAux) && vm.frameAux[st.frameIdx].shadowFrame != nil {
				vm.frameAux[st.frameIdx].shadowFrame.Detach()
				vm.frameAux[st.frameIdx].shadowFrame = nil
			}
		}
		st.frameIdx--
		caller := &vm.frames[st.frameIdx]
		if len(vm.excStack) > int(caller.excStackLen) {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		if st.frameIdx == callerIdx {
			callerIP := caller.ip
			retSP := caller.retSP
			aux := vm.frameAuxAt(callerIdx)
			if aux.joinState != nil {
				vm.releaseStrJoinState(aux.joinState)
				aux.joinState = nil
			}
			if aux.collectState != nil {
				vm.releaseCollectState(aux.collectState)
				aux.collectState = nil
			}
			vm.restoreCallerFrame(st, caller)
			st.stack[retSP] = resultVal
			return callerIP, retSP + 1, ctlReload
		}
		retSP := caller.retSP
		callerIP := caller.ip
		vm.restoreCallerFrame(st, caller)
		if st.activeGen != nil {
			st.activeGen.YieldVal = val.NoneVal()
			st.activeGen.State = val.GenSuspended
			st.activeGen.Stack = st.stack
			st.activeGen.SP = retSP
			st.activeGen.IP = callerIP
		} else if st.activeCoro != nil {
			st.activeCoro.YieldVal = val.NoneVal()
			st.activeCoro.State = val.CoroSuspended
			st.activeCoro.Stack = st.stack
			st.activeCoro.SP = retSP
			st.activeCoro.IP = callerIP
		}
	}
	st.stack[sp] = resultVal
	return ip + 1, sp + 1, ctlReload
}

func (vm *VM) failGenDrainCaller(st *loopState, callerIdx int, badVal, errExc val.Value, ip, sp int) (int, int, loopCtl) {
	if st.activeGen != nil {
		gen := st.activeGen
		if (gen.Header.Flags & val.FlagEphemeralGen) != 0 {
			gen.State = val.GenClosed
			vm.releaseGeneratorStack(gen)
		} else {
			gen.YieldVal = badVal
			gen.State = val.GenSuspended
			gen.Stack = st.stack
			gen.SP = sp
			gen.IP = ip + 1
		}
	}
	for st.frameIdx > callerIdx {
		if (vm.frames[st.frameIdx].flags & frameFlagHasShadow) != 0 {
			vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
			if st.frameIdx < len(vm.frameAux) && vm.frameAux[st.frameIdx].shadowFrame != nil {
				vm.frameAux[st.frameIdx].shadowFrame.Detach()
				vm.frameAux[st.frameIdx].shadowFrame = nil
			}
		}
		st.frameIdx--
		caller := &vm.frames[st.frameIdx]
		if len(vm.excStack) > int(caller.excStackLen) {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		if st.frameIdx == callerIdx {
			callerIP := caller.ip
			aux := vm.frameAuxAt(callerIdx)
			if aux.joinState != nil {
				vm.releaseStrJoinState(aux.joinState)
				aux.joinState = nil
			}
			if aux.collectState != nil {
				vm.releaseCollectState(aux.collectState)
				aux.collectState = nil
			}
			vm.restoreCallerFrame(st, caller)
			st.raiseExc = errExc
			return callerIP - 1, caller.retSP, ctlRaise
		}
		retSP := caller.retSP
		callerIP := caller.ip
		vm.restoreCallerFrame(st, caller)
		if st.activeGen != nil {
			st.activeGen.YieldVal = badVal
			st.activeGen.State = val.GenSuspended
			st.activeGen.Stack = st.stack
			st.activeGen.SP = retSP
			st.activeGen.IP = callerIP
		} else if st.activeCoro != nil {
			st.activeCoro.YieldVal = badVal
			st.activeCoro.State = val.CoroSuspended
			st.activeCoro.Stack = st.stack
			st.activeCoro.SP = retSP
			st.activeCoro.IP = callerIP
		}
	}
	st.raiseExc = errExc
	return ip, sp, ctlRaise
}

func (vm *VM) failStrJoinNonString(st *loopState, js *strJoinState, callerIdx int, badVal val.Value, ip, sp int) (int, int, loopCtl) {
	errMsg := fmt.Sprintf("sequence item %d: expected str instance, %s found", js.count, badVal.Type().Name)
	return vm.failGenDrainCaller(st, callerIdx, badVal, val.NewTypeError(errMsg), ip, sp)
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
		case genModeStrJoin:
			st.stack[retSP] = val.NewStringVal("")
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeCollectList:
			st.stack[retSP] = val.NewListVal()
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeCollectTuple:
			st.stack[retSP] = val.EmptyTupleVal()
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeCollectSet:
			st.stack[retSP] = val.NewSetVal()
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeListExtend:
			st.stack[retSP] = val.NoneVal()
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeCollectDict:
			st.stack[retSP] = val.NewDictVal(0)
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeAny:
			st.stack[retSP] = val.BoolVal(false)
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeAll:
			st.stack[retSP] = val.BoolVal(true)
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeSum:
			st.stack[retSP] = defaultVal
			vm.releaseGenerator(gen)
			return callerIP + 1, retSP + 1, ctlReload
		case genModeMin:
			st.raiseExc = val.NewValueError("min() iterable argument is empty")
			return callerIP, callerSP, ctlRaise
		case genModeMax:
			st.raiseExc = val.NewValueError("max() iterable argument is empty")
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
		if gen.IP == 0 {
			gen.IP = AsCodeObject(gen.Code).EntryIP
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
	minCap := max(gen.Base+64, gen.Base+callee.NumLocals+callee.MaxStackSize+16)
	if minCap > len(gen.Stack) {
		s, newCls := vm.acquireGeneratorStack(minCap)
		copy(s, gen.Stack)
		old := gen.Stack
		gen.Stack = s
		gen.PoolClass = newCls
		vm.releaseStackSlice(old)
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
		if coro.IP == 0 {
			coro.IP = AsCodeObject(coro.Code).EntryIP
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
		return vm.pushInlineGenerator(st, gen, ip, sp, sp, jumpTarget, genModeForIter, val.Value{}, val.NoneVal())
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
		if (vm.frames[st.frameIdx].flags & frameFlagHasShadow) != 0 {
			vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
			if st.frameIdx < len(vm.frameAux) && vm.frameAux[st.frameIdx].shadowFrame != nil {
				vm.frameAux[st.frameIdx].shadowFrame.Detach()
				vm.frameAux[st.frameIdx].shadowFrame = nil
			}
		}
		st.frameIdx--
		caller := &vm.frames[st.frameIdx]
		if len(vm.excStack) > int(caller.excStackLen) {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		switch vm.frameAuxAt(st.frameIdx).genMode {
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

func (vm *VM) isDrainingActiveGen(st *loopState) bool {
	if vm.activeDrainGen == nil || st.activeGen == nil {
		return false
	}
	if st.frameIdx == st.startFrameIdx {
		return st.activeGen == vm.activeDrainGen
	}
	if st.frameIdx != st.genFrameIdx {
		return false
	}
	for k := st.frameIdx - 1; k >= st.startFrameIdx; k-- {
		if k >= len(vm.frameAux) || vm.frameAux[k].genMode != genModeYieldFrom {
			return false
		}
		if k == st.startFrameIdx {
			return vm.frameAux[k].activeGen == vm.activeDrainGen
		}
	}
	return false
}

func (vm *VM) emitDrainItem(v val.Value) {
	if vm.hasTrackedViews {
		markViewEscaped(v)
	}
	val.ClearEphemeral(v)
	val.DetachStringBuf(v)
	if vm.drainItems != nil {
		*vm.drainItems = append(*vm.drainItems, v)
		return
	}
	if vm.drainAppend != nil {
		vm.drainAppend(v)
	}
}

// drainGeneratorRun drains all yielded items from gen in a single runFrame
// invocation, routing yields either directly into itemsSink or appendFn.
func (vm *VM) drainGeneratorRun(ctx *val.ExecContext, gen *val.PyGenerator, itemsSink *[]val.Value, appendFn func(val.Value)) bool {
	if gen.State == val.GenClosed {
		return true
	}
	if gen.State == val.GenRunning {
		if ctx != nil {
			ctx.Raise(val.NewValueError("generator already executing"))
		}
		return false
	}
	var thrownExc val.Value
	if ctx != nil && ctx.HasException() {
		thrownExc = ctx.ActiveException
		ctx.ClearException()
		if gen.State == val.GenCreated {
			gen.State = val.GenClosed
			vm.releaseGeneratorStack(gen)
			ctx.Raise(thrownExc)
			return false
		}
	}
	co := AsCodeObject(gen.Code)
	switch {
	case !thrownExc.IsNone() && thrownExc.HeaderPtr() != nil:
		vm.execCtx.Raise(thrownExc)
	case gen.State == val.GenSuspended:
		if gen.IP < len(co.Code) && Opcode(co.Code[gen.IP]>>24) != OP_YIELD_FROM {
			if gen.SP >= len(gen.Stack) {
				newStack := make([]val.Value, len(gen.Stack)*2+16)
				copy(newStack, gen.Stack)
				gen.Stack = newStack
			}
			gen.Stack[gen.SP] = val.NoneVal()
			gen.SP++
		}
	case gen.State != val.GenCreated:
		return false
	}

	savedGen, savedItems, savedAppend := vm.activeDrainGen, vm.drainItems, vm.drainAppend
	vm.activeDrainGen = gen
	vm.drainItems = itemsSink
	vm.drainAppend = appendFn
	restoreDrain := func() {
		vm.activeDrainGen, vm.drainItems, vm.drainAppend = savedGen, savedItems, savedAppend
	}

	nextFrame := 0
	if vm.activeCO != nil {
		nextFrame = vm.activeFrameIdx + 1
	}
	vm.ensureFrameCapacity(nextFrame + 4)
	vm.unhandledException = val.NoneVal()
	gen.State = val.GenRunning
	_ = vm.runFrame(co, gen.Base, gen.SP, nextFrame, gen.Cells, gen.Globals, val.NoneVal(), gen.Stack, gen, nil)

	if gen.State == val.GenClosed && (vm.unhandledException.IsNone() || vm.unhandledException.HeaderPtr() == nil) {
		vm.releaseGeneratorStack(gen)
		restoreDrain()
		return true
	}
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		vm.releaseGeneratorStack(gen)
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		restoreDrain()
		if ctx != nil {
			ctx.Raise(exc)
		} else {
			panic(exc)
		}
		return false
	}
	restoreDrain()
	return true
}

func (vm *VM) drainGeneratorSlice(ctx *val.ExecContext, gen *val.PyGenerator) ([]val.Value, bool) {
	if gen.State == val.GenClosed {
		return nil, true
	}
	var items []val.Value
	if !vm.drainGeneratorRun(ctx, gen, &items, nil) {
		return nil, false
	}
	return items, true
}

func (vm *VM) drainGeneratorInto(ctx *val.ExecContext, gen *val.PyGenerator, appendFn func(val.Value)) bool {
	if gen.State == val.GenClosed {
		return true
	}
	return vm.drainGeneratorRun(ctx, gen, nil, appendFn)
}

//go:noinline
func (vm *VM) opYieldValue(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	sp--
	yieldVal := stack[sp]
	if st.activeGen != nil {
		if callerIdx, mode := vm.findGenDrainCaller(st); mode != genModeNone {
			if mode == genModeStrJoin {
				js := vm.frameAux[callerIdx].joinState
				if !js.appendValue(yieldVal) {
					return vm.failStrJoinNonString(st, js, callerIdx, yieldVal, ip, sp)
				}
			} else {
				origData := unsafe.SliceData(stack)
				st.stack = stack
				st.ip = ip
				vm.frames[st.frameIdx].ip = ip
				vm.execCtx.SP = sp
				cs := vm.frameAux[callerIdx].collectState
				errExc, earlyRes, doneEarly, ok := cs.appendValue(&vm.execCtx, yieldVal, mode, vm.hasTrackedViews)
				stack = st.syncStackFromExecCtx(vm, stack)
				if !ok {
					return vm.failGenDrainCaller(st, callerIdx, yieldVal, errExc, ip, sp)
				}
				if doneEarly {
					return vm.earlyStopGenDrainCaller(st, callerIdx, earlyRes, ip, sp, 1)
				}
				stack[sp] = val.NoneVal()
				if unsafe.SliceData(stack) != origData {
					return ip + 1, sp + 1, ctlReload
				}
				return ip + 1, sp + 1, ctlNext
			}
			stack[sp] = val.NoneVal()
			return ip + 1, sp + 1, ctlNext
		}
		if vm.activeJoinGen == st.activeGen && st.frameIdx == st.startFrameIdx {
			vm.appendJoinItem(yieldVal)
			stack[sp] = val.NoneVal()
			sp++
			ip++
			return ip, sp, ctlNext
		}
		if vm.isDrainingActiveGen(st) {
			vm.emitDrainItem(yieldVal)
			stack[sp] = val.NoneVal()
			return ip + 1, sp + 1, ctlNext
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
	if subiter.IsGenerator() {
		return vm.pushInlineGenerator(st, subiter.AsGenerator(), ip, sp, sp, 0, genModeYieldFrom, val.Value{}, sentVal)
	}
	if subiter.IsCoroutine() {
		return vm.pushInlineCoroutine(st, subiter.AsCoroutine(), ip, sp, sp, genModeYieldFrom, sentVal)
	}
	if vm.isDrainingActiveGen(st) {
		for {
			var nextVal val.Value
			var status val.IterStatus
			if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status = tpIterNext(&vm.execCtx, subiter)
			} else {
				break
			}
			if status == val.IterYielded {
				vm.emitDrainItem(nextVal)
				continue
			}
			if status == val.IterExhausted {
				stack[sp-1] = val.NoneVal()
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
		return ip + 1, sp, ctlReload
	}
	if callerIdx, mode := vm.findGenDrainCaller(st); mode != genModeNone {
		js := vm.frameAux[callerIdx].joinState
		cs := vm.frameAux[callerIdx].collectState
		for {
			var nextVal val.Value
			var status val.IterStatus
			if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status = tpIterNext(&vm.execCtx, subiter)
			} else {
				break
			}
			if status == val.IterYielded {
				if mode == genModeStrJoin {
					if !js.appendValue(nextVal) {
						return vm.failStrJoinNonString(st, js, callerIdx, nextVal, ip, sp)
					}
				} else {
					st.stack = stack
					st.ip = ip
					vm.frames[st.frameIdx].ip = ip
					vm.execCtx.SP = sp
					errExc, earlyRes, doneEarly, ok := cs.appendValue(&vm.execCtx, nextVal, mode, vm.hasTrackedViews)
					stack = st.syncStackFromExecCtx(vm, stack)
					if !ok {
						return vm.failGenDrainCaller(st, callerIdx, nextVal, errExc, ip, sp)
					}
					if doneEarly {
						return vm.earlyStopGenDrainCaller(st, callerIdx, earlyRes, ip, sp, 0)
					}
				}
				continue
			}
			if status == val.IterExhausted {
				stack[sp-1] = val.NoneVal()
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
				if vm.activeTask != nil {
					if nextVal.IsFuture() {
						vm.activeTask.SetWaiter(nextVal.AsFuture(), aw)
					} else if nextVal.IsTask() {
						vm.activeTask.SetWaiter(&nextVal.AsTask().Future, aw)
					}
				}
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
	} else {
		ip++
		return ip, sp, ctlReload
	}
	return ip, sp, ctlReload
}
