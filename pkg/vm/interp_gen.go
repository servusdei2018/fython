package vm

import (
	"fmt"
	"slices"
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
	if cc != nil {
		st.codePtr = cc.codePtr
		st.codeLen = cc.codeLen
	}
	st.consts = cc.frameConsts(co)
	st.base = caller.baseOffset
	aux := &vm.frameAux[callerIdx]
	st.cells = aux.cells
	st.cellPtrs = aux.cellPtrs
	st.globals = caller.globals
	st.curLocals = aux.locals
	st.stack = aux.stack
	st.stackIsVM = caller.stackIsVM()
	vm.execCtx.Stack = aux.stack
	st.activeGen = aux.activeGen
	st.delegatedRoot = aux.delegatedRoot
	st.activeCoro = aux.activeCoro
	st.activeAsyncGen = aux.activeAsyncGen
	st.genFrameIdx = aux.genFrameIdx
	aux.cells = nil
	aux.cellPtrs = nil
	aux.locals = val.Value{}
	aux.genMode = genModeNone
	aux.genDefault = val.Value{}
	aux.stack = nil
	aux.activeGen = nil
	aux.delegatedRoot = nil
	aux.activeCoro = nil
	aux.activeAsyncGen = nil
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
	var aux *frameAux
	if frameIdx < len(vm.frameAux) {
		aux = &vm.frameAux[frameIdx]
		if frameIdx+1 > vm.maxFrameAuxLen {
			vm.maxFrameAuxLen = frameIdx + 1
		}
	} else {
		aux = vm.frameAuxAt(frameIdx)
	}
	aux.cells = st.cells
	aux.cellPtrs = st.cellPtrs
	aux.locals = st.curLocals
	aux.stack = st.stack
	aux.activeGen = st.activeGen
	aux.delegatedRoot = st.delegatedRoot
	aux.activeCoro = st.activeCoro
	aux.activeAsyncGen = st.activeAsyncGen
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
	st.delegatedRoot = nil
	st.activeCoro = coro
	st.activeAsyncGen = nil
	st.stack = childStack
	st.stackIsVM = false
	vm.execCtx.Stack = childStack
	st.curCO = callee
	st.cache = vm.cacheFor(callee)
	st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	if st.cache.tier2Done == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
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

func (vm *VM) switchInlineGeneratorInPlace(st *loopState, gen *val.PyGenerator) {
	if st.frameIdx >= 0 && st.frameIdx < len(vm.frames) && (vm.frames[st.frameIdx].flags&frameFlagHasShadow) != 0 {
		vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
		if st.frameIdx < len(vm.frameAux) && vm.frameAux[st.frameIdx].shadowFrame != nil {
			vm.frameAux[st.frameIdx].shadowFrame.Detach()
			vm.frameAux[st.frameIdx].shadowFrame = nil
		}
	}
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
	st.curLocals = val.NoneVal()
	st.activeGen = gen
	st.activeCoro = nil
	st.stack = gen.Stack
	st.stackIsVM = false
	vm.execCtx.Stack = gen.Stack
	st.curCO = callee
	st.cache = vm.cacheFor(callee)
	st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	if st.cache.tier2Done == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, callee, st.cache)
		st.codePtr, st.codeLen = vm.loadWindow(callee, st.cache)
	}
	st.consts = st.cache.frameConsts(callee)
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
}

func findDelegationParent(root, child *val.PyGenerator) *val.PyGenerator {
	for p := root; p != nil && p != child; p = p.SubGen {
		if p.SubGen == child {
			return p
		}
	}
	return nil
}

func resolveDelegationLeaf(root *val.PyGenerator) (*val.PyGenerator, bool) {
	cur := root
	for cur.SubGen != nil {
		next := cur.SubGen
		if next.State == val.GenRunning {
			return nil, true
		}
		if next.State == val.GenClosed {
			return cur, false
		}
		cur = next
	}
	return cur, false
}

func isOnDelegationChain(root, activeLeaf, target *val.PyGenerator) bool {
	for p := root; p != nil; p = p.SubGen {
		if p == target {
			return true
		}
		if p == activeLeaf {
			return false
		}
	}
	return false
}

func (vm *VM) isGeneratorRunning(gen *val.PyGenerator) bool {
	if gen == nil {
		return false
	}
	if gen.State == val.GenRunning {
		return true
	}
	st := vm.activeState
	if st == nil {
		return false
	}
	if st.delegatedRoot != nil && isOnDelegationChain(st.delegatedRoot, st.activeGen, gen) {
		return true
	}
	limit := min(st.frameIdx, len(vm.frameAux))
	for i := range limit {
		if root := vm.frameAux[i].delegatedRoot; root != nil {
			if isOnDelegationChain(root, vm.frameAux[i].activeGen, gen) {
				return true
			}
		}
	}
	return false
}

func stopIterationValue(exc val.Value) val.Value {
	if exc.IsInstance() {
		if v, ok := exc.AsInstance().GetAttr(val.InternString("value")); ok {
			return v
		}
		if args, ok := exc.AsInstance().GetAttr(val.InternString("args")); ok && args.IsTuple() {
			items := args.AsTuple().Items
			if len(items) > 0 {
				return items[0]
			}
		}
	}
	return val.NoneVal()
}

func (vm *VM) closeGenerator(ctx *val.ExecContext, gen *val.PyGenerator) {
	if gen == nil {
		return
	}
	if gen.State == val.GenRunning || vm.isGeneratorRunning(gen) {
		if ctx != nil {
			ctx.Raise(val.NewValueError("generator already executing"))
		}
		return
	}
	if gen.State == val.GenClosed || gen.State == val.GenCreated {
		gen.State = val.GenClosed
		vm.releaseGeneratorStack(gen)
		return
	}

	var chainBuf [16]*val.PyGenerator
	chain := chainBuf[:0]
	for cur := gen; cur != nil; cur = cur.SubGen {
		if cur.State == val.GenRunning {
			if ctx != nil {
				ctx.Raise(val.NewValueError("generator already executing"))
			}
			return
		}
		chain = append(chain, cur)
	}
	for _, g := range chain {
		g.SubGen = nil
		g.LeafGen = nil
		g.LeafReuseGen = 0
	}

	pendingExc := val.NewInstanceOfType(val.GeneratorExitType)
	pendingIsGenExit := true

	for i, g := range slices.Backward(chain) {
		if g.State == val.GenClosed || g.State == val.GenCreated {
			g.State = val.GenClosed
			vm.releaseGeneratorStack(g)
			continue
		}
		if i == len(chain)-1 && g.YieldFrom.HeaderPtr() != nil {
			subiter := g.YieldFrom
			g.YieldFrom = val.Value{}
			if !subiter.IsIter() {
				if closeMethod, ok := vm.lookupSubiterMethod(subiter, "close"); ok {
					_ = vm.Vectorcall(closeMethod, nil, nil)
					if vm.execCtx.HasException() {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						if !val.IsInstance(exc, val.GeneratorExitType) && !val.IsInstance(exc, val.StopIterationType) {
							pendingExc = exc
							pendingIsGenExit = false
						}
					}
				}
			}
		}
		g.YieldFrom = val.Value{}
		if ctx == nil {
			g.State = val.GenClosed
			vm.releaseGeneratorStack(g)
			continue
		}
		ctx.Raise(pendingExc)
		_, status := vm.resumeGenerator(ctx, g, val.NoneVal())
		switch {
		case status == val.IterYielded:
			g.State = val.GenClosed
			vm.releaseGeneratorStack(g)
			pendingExc = val.NewRuntimeError("generator ignored GeneratorExit")
			pendingIsGenExit = false
		case ctx.HasException():
			exc := ctx.ActiveException
			ctx.ClearException()
			if val.IsInstance(exc, val.GeneratorExitType) || val.IsInstance(exc, val.StopIterationType) {
				pendingExc = val.NewInstanceOfType(val.GeneratorExitType)
				pendingIsGenExit = true
			} else {
				pendingExc = exc
				pendingIsGenExit = false
			}
		default:
			g.State = val.GenClosed
			vm.releaseGeneratorStack(g)
			pendingExc = val.NewInstanceOfType(val.GeneratorExitType)
			pendingIsGenExit = true
		}
	}

	if !pendingIsGenExit && ctx != nil {
		ctx.Raise(pendingExc)
	}
}

func (vm *VM) lookupSubiterMethod(subiter val.Value, name string) (val.Value, bool) {
	attr := val.InternString(name)
	if subiter.IsInstance() {
		if m, ok := subiter.AsInstance().GetAttr(attr); ok {
			return m, true
		}
	}
	if tp := subiter.Type(); tp != nil {
		if m, _, ok := tp.LookupMRO(attr); ok && m.HeaderPtr() != nil && !m.IsUnbound() {
			if m.IsFunction() || m.IsBuiltinFunc() {
				return val.NewBoundMethodVal(subiter, m), true
			}
			return m, true
		}
	}
	return val.Value{}, false
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
	if st.delegatedRoot != nil {
		outermostGen = st.delegatedRoot
	} else if st.frameIdx-1 > callerIdx && callerIdx+1 < len(vm.frameAux) && vm.frameAux[callerIdx+1].activeGen != nil {
		outermostGen = vm.frameAux[callerIdx+1].activeGen
	}
	releaseChain := outermostGen != nil && (outermostGen.Header.Flags&val.FlagEphemeralGen) != 0
	if st.activeGen != nil {
		gen := st.activeGen
		root := st.delegatedRoot
		st.delegatedRoot = nil
		if releaseChain && (gen.Header.Flags&val.FlagEphemeralGen) != 0 {
			if root != nil {
				for cur := root; cur != nil; {
					next := cur.SubGen
					cur.SubGen = nil
					cur.LeafGen = nil
					cur.LeafReuseGen = 0
					if (cur.Header.Flags & val.FlagEphemeralGen) != 0 {
						cur.State = val.GenClosed
						vm.releaseGeneratorStack(cur)
						vm.releaseGenerator(cur)
					} else {
						cur.State = val.GenSuspended
					}
					if cur == gen {
						break
					}
					cur = next
				}
			} else {
				gen.State = val.GenClosed
				vm.releaseGeneratorStack(gen)
				vm.releaseGenerator(gen)
			}
		} else {
			gen.YieldVal = val.NoneVal()
			gen.State = val.GenSuspended
			gen.Stack = st.stack
			gen.SP = sp
			gen.IP = ip + ipDelta
			if root != nil {
				root.YieldVal = val.NoneVal()
				root.State = val.GenSuspended
				root.LeafGen = gen
				root.LeafReuseGen = gen.ReuseGen
			}
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
		root := st.delegatedRoot
		st.delegatedRoot = nil
		if root == nil && (gen.Header.Flags&val.FlagEphemeralGen) != 0 {
			gen.State = val.GenClosed
			vm.releaseGeneratorStack(gen)
		} else {
			gen.YieldVal = badVal
			gen.State = val.GenSuspended
			gen.Stack = st.stack
			gen.SP = sp
			gen.IP = ip + 1
			if root != nil {
				root.YieldVal = badVal
				root.State = val.GenSuspended
				root.LeafGen = gen
				root.LeafReuseGen = gen.ReuseGen
			}
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

//go:noinline
func (vm *VM) pushInlineDelegatedSlow(st *loopState, gen *val.PyGenerator, callerIP, callerSP, retSP, jumpTarget int, mode uint8, defaultVal, sentVal val.Value) (int, int, loopCtl) {
	target, runningErr := resolveDelegationLeaf(gen)
	if runningErr {
		st.raiseExc = val.NewValueError("generator already executing")
		return callerIP, callerSP, ctlRaise
	}
	targetCO := AsCodeObject(target.Code)
	var delegatedRoot *val.PyGenerator
	if target != gen {
		switch target.State {
		case val.GenCreated:
			if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
				st.raiseExc = val.NewTypeError("can't send non-None value to a just-started generator")
				return callerIP, callerSP, ctlRaise
			}
			if target.IP == 0 {
				target.IP = targetCO.EntryIP
			}
		case val.GenSuspended:
			target.LastSent = sentVal
			if target.SubGen == nil && target.YieldFrom.HeaderPtr() == nil && target.IP < len(targetCO.Code) && DecodeOpcode(targetCO.Code[target.IP]) != OP_YIELD_FROM {
				target.Stack[target.SP] = sentVal
				target.SP++
			}
		}
		gen.State = val.GenRunning
		gen.LeafGen = target
		gen.LeafReuseGen = target.ReuseGen
		delegatedRoot = gen
	} else {
		gen.LastSent = sentVal
	}
	target.State = val.GenRunning
	minCap := max(target.Base+64, target.Base+targetCO.NumLocals+targetCO.MaxStackSize+16)
	if minCap > len(target.Stack) {
		s, newCls := vm.acquireGeneratorStack(minCap)
		copy(s, target.Stack)
		old := target.Stack
		target.Stack = s
		target.PoolClass = newCls
		vm.releaseStackSlice(old)
	}
	if len(target.Cells) > 0 && len(target.CellPtrs) == 0 {
		ptrs := vm.acquireGenCellPtrs(len(target.Cells))
		for i, c := range target.Cells {
			ptrs[i] = c.AsCell()
		}
		target.CellPtrs = ptrs
	}
	vm.saveAndPushInlineFrame(
		st, targetCO, target.Stack, target.Base, target.Cells, target.CellPtrs, target.Globals,
		callerIP, retSP, jumpTarget, mode, defaultVal, target, nil,
	)
	st.delegatedRoot = delegatedRoot
	return target.IP, target.SP, ctlReload
}

// pushInlineGenerator pushes gen onto vm.frames directly inside the active
// runLoopWithGlobals without recursing into resumeGenerator/runFrame.
//
//go:noinline
func (vm *VM) pushInlineGenerator(st *loopState, gen *val.PyGenerator, callerIP, callerSP, retSP, jumpTarget int, mode uint8, defaultVal, sentVal val.Value) (int, int, loopCtl) {
	var delegatedRoot *val.PyGenerator
	target := gen
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
		if gen.SubGen != nil {
			leaf := gen.LeafGen
			if leaf == nil || leaf.State != val.GenSuspended || leaf.SubGen != nil || leaf.ReuseGen != gen.LeafReuseGen {
				return vm.pushInlineDelegatedSlow(st, gen, callerIP, callerSP, retSP, jumpTarget, mode, defaultVal, sentVal)
			}
			gen.State = val.GenRunning
			delegatedRoot = gen
			target = leaf
		}
		target.LastSent = sentVal
		callee := AsCodeObject(target.Code)
		if target.YieldFrom.HeaderPtr() == nil && target.IP < len(callee.Code) && DecodeOpcode(callee.Code[target.IP]) != OP_YIELD_FROM {
			target.Stack[target.SP] = sentVal
			target.SP++
		}
	}

	target.State = val.GenRunning
	callee := AsCodeObject(target.Code)
	minCap := max(target.Base+64, target.Base+callee.NumLocals+callee.MaxStackSize+16)
	if minCap > len(target.Stack) {
		s, newCls := vm.acquireGeneratorStack(minCap)
		copy(s, target.Stack)
		old := target.Stack
		target.Stack = s
		target.PoolClass = newCls
		vm.releaseStackSlice(old)
	}
	if len(target.Cells) > 0 && len(target.CellPtrs) == 0 {
		ptrs := vm.acquireGenCellPtrs(len(target.Cells))
		for i, c := range target.Cells {
			ptrs[i] = c.AsCell()
		}
		target.CellPtrs = ptrs
	}

	vm.saveAndPushInlineFrame(
		st, callee, target.Stack, target.Base, target.Cells, target.CellPtrs, target.Globals,
		callerIP, retSP, jumpTarget, mode, defaultVal, target, nil,
	)
	st.delegatedRoot = delegatedRoot
	return target.IP, target.SP, ctlReload
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
	switch {
	case st.activeGen != nil:
		st.activeGen.YieldVal = yieldVal
		st.activeGen.State = val.GenSuspended
		st.activeGen.Stack = st.stack
		st.activeGen.SP = resumeSP
		st.activeGen.IP = resumeIP
		if root := st.delegatedRoot; root != nil {
			root.YieldVal = yieldVal
			root.State = val.GenSuspended
			root.LeafGen = st.activeGen
			root.LeafReuseGen = st.activeGen.ReuseGen
			st.delegatedRoot = nil
		}
	case st.activeCoro != nil:
		st.activeCoro.YieldVal = yieldVal
		st.activeCoro.State = val.CoroSuspended
		st.activeCoro.Stack = st.stack
		st.activeCoro.SP = resumeSP
		st.activeCoro.IP = resumeIP
	case st.activeAsyncGen != nil:
		st.activeAsyncGen.YieldVal = yieldVal
		if resumeSP > 0 && resumeSP <= len(st.stack) {
			st.activeAsyncGen.AgAwait = st.stack[resumeSP-1]
		}
		st.activeAsyncGen.State = val.AsyncGenSuspended
		st.activeAsyncGen.SuspendedOnAwait = true
		st.activeAsyncGen.Stack = st.stack
		st.activeAsyncGen.SP = resumeSP
		st.activeAsyncGen.IP = resumeIP
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
			if st.activeAsyncGen != nil {
				st.activeAsyncGen.YieldVal = yieldVal
				if retSP > 0 && retSP <= len(st.stack) {
					st.activeAsyncGen.AgAwait = st.stack[retSP-1]
				}
				st.activeAsyncGen.State = val.AsyncGenSuspended
				st.activeAsyncGen.SuspendedOnAwait = true
				st.activeAsyncGen.Stack = st.stack
				st.activeAsyncGen.SP = retSP
				st.activeAsyncGen.IP = callerIP
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
		return st.activeGen == vm.activeDrainGen || st.delegatedRoot == vm.activeDrainGen
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
		if vm.activeJoinGen != nil && (vm.activeJoinGen == st.activeGen || vm.activeJoinGen == st.delegatedRoot) && st.frameIdx == st.startFrameIdx {
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
	if st.activeAsyncGen != nil {
		st.activeAsyncGen.YieldVal = yieldVal
		st.activeAsyncGen.AgAwait = val.NoneVal()
		st.activeAsyncGen.State = val.AsyncGenSuspended
		st.activeAsyncGen.SuspendedOnAwait = false
		st.activeAsyncGen.Stack = st.stack
		st.activeAsyncGen.SP = sp
		st.activeAsyncGen.IP = ip + 1
		st.result = yieldVal
		return ip + 1, sp, ctlExit
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
		// Per PEP 380, initial entry into `yield from sub` always primes/advances
		// sub with None even when sub is already GenSuspended.
		if st.activeGen.SubGen == nil && st.activeGen.YieldFrom.HeaderPtr() == nil {
			st.activeGen.LastSent = val.NoneVal()
		} else {
			sentVal = st.activeGen.LastSent
			st.activeGen.LastSent = val.NoneVal()
		}
	} else if st.activeCoro != nil {
		sentVal = st.activeCoro.LastSent
		st.activeCoro.LastSent = val.NoneVal()
	}
	if subiter.IsGenerator() {
		subGen := subiter.AsGenerator()
		if st.activeGen != nil && st.frameIdx == st.genFrameIdx {
			curGen := st.activeGen
			if subGen.State == val.GenClosed {
				curGen.SubGen = nil
				curGen.LeafGen = nil
				curGen.LeafReuseGen = 0
				curGen.YieldFrom = val.Value{}
				stack[sp-1] = subGen.RetVal
				vm.releaseGenerator(subGen)
				return ip + 1, sp, ctlReload
			}
			if subGen.State == val.GenRunning {
				st.raiseExc = val.NewValueError("generator already executing")
				return ip, sp, ctlRaise
			}
			target := subGen
			if subGen.State == val.GenSuspended && subGen.SubGen != nil {
				t, runningErr := resolveDelegationLeaf(subGen)
				if runningErr {
					st.raiseExc = val.NewValueError("generator already executing")
					return ip, sp, ctlRaise
				}
				target = t
			}
			switch target.State {
			case val.GenCreated:
				if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
					st.raiseExc = val.NewTypeError("can't send non-None value to a just-started generator")
					return ip, sp, ctlRaise
				}
				if target.IP == 0 {
					target.IP = AsCodeObject(target.Code).EntryIP
				}
			case val.GenSuspended:
				target.LastSent = sentVal
				tco := AsCodeObject(target.Code)
				if target.SubGen == nil && target.YieldFrom.HeaderPtr() == nil && target.IP < len(tco.Code) && DecodeOpcode(tco.Code[target.IP]) != OP_YIELD_FROM {
					target.Stack[target.SP] = sentVal
					target.SP++
				}
			}
			curGen.Stack = stack
			curGen.SP = sp
			curGen.IP = ip
			curGen.SubGen = subGen
			curGen.YieldFrom = val.Value{}
			if st.delegatedRoot == nil {
				st.delegatedRoot = curGen
				curGen.State = val.GenRunning
			} else {
				curGen.State = val.GenSuspended
			}
			st.delegatedRoot.LeafGen = target
			st.delegatedRoot.LeafReuseGen = target.ReuseGen
			target.State = val.GenRunning
			vm.switchInlineGeneratorInPlace(st, target)
			return target.IP, target.SP, ctlReload
		}
		return vm.pushInlineGenerator(st, subGen, ip, sp, sp, 0, genModeYieldFrom, val.Value{}, sentVal)
	}
	if st.activeGen != nil && vm.activeJoinGen != nil && (vm.activeJoinGen == st.activeGen || vm.activeJoinGen == st.delegatedRoot) && st.frameIdx == st.startFrameIdx {
		for {
			var nextVal val.Value
			var status val.IterStatus
			if subiter.IsCoroutine() {
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
				if subiter.IsCoroutine() {
					retVal = subiter.AsCoroutine().RetVal
					vm.releaseCoroutine(subiter.AsCoroutine())
				}
				st.activeGen.YieldFrom = val.Value{}
				stack[sp-1] = retVal
				break
			}
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.activeGen.YieldFrom = val.Value{}
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			break
		}
		ip++
		return ip, sp, ctlReload
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
				if st.activeGen != nil {
					st.activeGen.YieldFrom = val.Value{}
				}
				stack[sp-1] = val.NoneVal()
				break
			}
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				if st.activeGen != nil {
					st.activeGen.YieldFrom = val.Value{}
				}
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
				if st.activeGen != nil {
					st.activeGen.YieldFrom = val.Value{}
				}
				stack[sp-1] = val.NoneVal()
				break
			}
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				if st.activeGen != nil {
					st.activeGen.YieldFrom = val.Value{}
				}
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
	if !sentVal.IsNone() && sentVal.HeaderPtr() != nil && !subiter.IsIter() {
		if sendMethod, ok := vm.lookupSubiterMethod(subiter, "send"); ok {
			nextVal = vm.Vectorcall(sendMethod, []val.Value{sentVal}, nil)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				if val.IsInstance(exc, val.StopIterationType) {
					if st.activeGen != nil {
						st.activeGen.YieldFrom = val.Value{}
					}
					stack[sp-1] = stopIterationValue(exc)
					return ip + 1, sp, ctlReload
				}
				if st.activeGen != nil {
					st.activeGen.YieldFrom = val.Value{}
				}
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			status = val.IterYielded
		} else {
			if st.activeGen != nil {
				st.activeGen.YieldFrom = val.Value{}
			}
			st.raiseExc = val.NewAttributeError(fmt.Sprintf("'%s' object has no attribute 'send'", subiter.Type().Name))
			return ip, sp, ctlRaise
		}
	} else if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
		nextVal, status = tpIterNext(&vm.execCtx, subiter)
	} else {
		if st.activeGen != nil {
			st.activeGen.YieldFrom = val.Value{}
		}
		ip++
		return ip, sp, ctlReload
	}

	switch {
	case status == val.IterYielded:
		if st.activeGen != nil {
			st.activeGen.YieldFrom = subiter
			return vm.suspendActiveChain(st, nextVal, ip, sp)
		}
		if st.activeCoro != nil {
			return vm.suspendActiveChain(st, nextVal, ip, sp)
		}
	case status == val.IterExhausted:
		retVal := val.NoneVal()
		if subiter.IsInstance() && nextVal.HeaderPtr() != nil {
			retVal = nextVal
		}
		if st.activeGen != nil {
			st.activeGen.YieldFrom = val.Value{}
		}
		stack[sp-1] = retVal
		ip++
	case vm.execCtx.HasException():
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		if st.activeGen != nil {
			st.activeGen.YieldFrom = val.Value{}
		}
		if val.IsInstance(exc, val.StopIterationType) {
			stack[sp-1] = stopIterationValue(exc)
			return ip + 1, sp, ctlReload
		}
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
				if st.activeAsyncGen != nil {
					st.activeAsyncGen.AgAwait = val.NoneVal()
					st.activeAsyncGen.SuspendedOnAwait = false
				}
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			if st.activeCoro != nil {
				st.activeCoro.LastSent = val.NoneVal()
			}
			if st.activeAsyncGen != nil {
				st.activeAsyncGen.LastSent = val.NoneVal()
				st.activeAsyncGen.AgAwait = val.NoneVal()
				st.activeAsyncGen.SuspendedOnAwait = false
			}
			stack[sp-1] = res
			ip++
			return ip, sp, ctlReload
		}
		if st.activeCoro != nil || st.activeAsyncGen != nil {
			return vm.suspendActiveChain(st, aw, ip, sp)
		}
	} else if aw.IsCoroutine() {
		childCoro := aw.AsCoroutine()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		} else if st.activeAsyncGen != nil {
			sentVal = st.activeAsyncGen.LastSent
			st.activeAsyncGen.LastSent = val.NoneVal()
		}
		return vm.pushInlineCoroutine(st, childCoro, ip, sp, sp, genModeAwait, sentVal)
	} else if aw.IsGenerator() {
		subiter := aw.AsGenerator()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		} else if st.activeAsyncGen != nil {
			sentVal = st.activeAsyncGen.LastSent
			st.activeAsyncGen.LastSent = val.NoneVal()
		}
		if vm.activeJoinGen == nil {
			return vm.pushInlineGenerator(st, subiter, ip, sp, sp, 0, genModeAwait, val.Value{}, sentVal)
		}
		nextVal, status := subiter.Send(&vm.execCtx, sentVal)
		switch status {
		case val.IterExhausted:
			if st.activeAsyncGen != nil {
				st.activeAsyncGen.AgAwait = val.NoneVal()
				st.activeAsyncGen.SuspendedOnAwait = false
			}
			stack[sp-1] = subiter.RetVal
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil || st.activeAsyncGen != nil {
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
			if st.activeAsyncGen != nil {
				st.activeAsyncGen.AgAwait = val.NoneVal()
				st.activeAsyncGen.SuspendedOnAwait = false
			}
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
			if st.activeAsyncGen != nil {
				return vm.suspendActiveChain(st, nextVal, ip, sp)
			}
		default:
			if vm.execCtx.HasException() {
				if st.activeAsyncGen != nil {
					st.activeAsyncGen.AgAwait = val.NoneVal()
					st.activeAsyncGen.SuspendedOnAwait = false
				}
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

var (
	nameAIter = val.InternString("__aiter__")
	nameANext = val.InternString("__anext__")
)

//go:noinline
func (vm *VM) opGetAIter(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	obj := stack[sp-1]
	tp := obj.Type()
	fn, _, ok := tp.LookupMRO(nameAIter)
	if !ok || fn.IsNone() || fn.HeaderPtr() == nil {
		st.raiseExc = val.NewTypeError("'async for' requires an object with __aiter__ method, got " + tp.Name)
		return ip, sp, ctlRaise
	}
	res := vm.Vectorcall(fn, []val.Value{obj}, nil)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	resTp := res.Type()
	if anextFn, _, hasANext := resTp.LookupMRO(nameANext); !hasANext || anextFn.IsNone() || anextFn.HeaderPtr() == nil {
		st.raiseExc = val.NewTypeError("'async for' received an object from __aiter__ that does not implement __anext__: " + resTp.Name)
		return ip, sp, ctlRaise
	}
	stack = st.syncStackFromVM(vm, stack)
	stack[sp-1] = res
	return ip + 1, sp, ctlReload
}

//go:noinline
func (vm *VM) opGetANext(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	it := stack[sp-1]
	tp := it.Type()
	fn, _, ok := tp.LookupMRO(nameANext)
	if !ok || fn.IsNone() || fn.HeaderPtr() == nil {
		st.raiseExc = val.NewTypeError("'async for' received an object from __aiter__ that does not implement __anext__: " + tp.Name)
		return ip, sp, ctlRaise
	}
	res := vm.Vectorcall(fn, []val.Value{it}, nil)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack = st.syncStackFromVM(vm, stack)
	aw, okAw := vm.toAwaitableForAsyncFor(res)
	if !okAw {
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		st.raiseExc = val.NewTypeError("'async for' received an invalid object from __anext__: " + res.Type().Name)
		return ip, sp, ctlRaise
	}
	stack[sp-1] = aw
	return ip + 1, sp, ctlReload
}

func (vm *VM) toAwaitableForAsyncFor(obj val.Value) (val.Value, bool) {
	if obj.IsCoroutine() || obj.IsFuture() || obj.IsTask() || val.IsAsyncGenASend(obj) || val.IsAsyncGenAThrow(obj) {
		return obj, true
	}
	tp := obj.Type()
	if awFn, _, ok := tp.LookupMRO(nameAwait); ok && !awFn.IsNone() && awFn.HeaderPtr() != nil {
		res := vm.Vectorcall(awFn, []val.Value{obj}, nil)
		if vm.execCtx.HasException() {
			return val.NoneVal(), false
		}
		if res.IsCoroutine() || res.IsFuture() || res.IsTask() || res.IsGenerator() || res.IsIter() || res.Type().Slots.TpIterNext != nil {
			return res, true
		}
		vm.execCtx.Raise(val.NewTypeError("__await__() returned non-iterator of type '" + res.Type().Name + "'"))
		return val.NoneVal(), false
	}
	if tp.Slots.TpIter != nil && tp.Slots.TpIterNext != nil && !obj.IsGenerator() && !obj.IsIter() {
		return obj, true
	}
	return val.NoneVal(), false
}
