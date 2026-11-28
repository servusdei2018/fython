package vm

import (
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

func init() {
	val.BatchApplyHook1 = applyKeyFuncBatched
}

// batchCall holds VM state hoisted once for a run of repeated calls to the
// same single- or two-argument Python function (sort key functions,
// functools.reduce). One loop state and one stack window serve the whole run:
// per-element work is argument placement plus the lean frame setup mirrored
// from callSimpleFuncFromHook, without per-element arg-slice packing,
// cache resolution misses, stack ensures, active-state flushes, or loop-state
// acquire/release churn. runLoopWithGlobals rebalances the shared loop state
// on every nested call and return, and nested batches acquire their own
// state, so recursion (a key function that recursively sorts) stays safe.
type batchCall struct {
	co          *CodeObject
	fn          val.Value
	fnGlobals   *val.PyDict
	numLocals   int
	nargs       int
	base        int
	spAfterBind int
	nextFrame   int

	st         *loopState
	stack      []val.Value
	wasVMStack bool

	savedActiveState      *loopState
	savedCtxStack         []val.Value
	savedCtxSP            int
	savedActiveCO         *CodeObject
	savedActiveBaseOffset int
	savedActiveCells      []val.Value
	savedActiveCellPtrs   []*val.PyCell
	savedActiveFrameIdx   int

	finished bool
}

// beginBatchCall hoists per-run resolution for repeated calls to fn with
// nargs positional arguments. It returns ok=false for callable shapes outside
// the fast path; callers must fall back to the generic per-element path,
// preserving exact semantics.
func (vm *VM) beginBatchCall(ctx *val.ExecContext, fn val.Value, nargs int) (*batchCall, bool) {
	if !fn.IsFunction() || ctx == nil {
		return nil, false
	}
	if nargs != 1 && nargs != 2 {
		return nil, false
	}
	f := fn.AsFunction()
	co := AsCodeObject(f.Code)
	if co == nil || co.IsGenerator || co.IsCoroutine || co.IsAsyncGenerator {
		return nil, false
	}
	if !co.SimplePositionalOnly || co.KWOnlyArgCount != 0 || co.PositionalParamCount != nargs {
		return nil, false
	}
	if len(f.Defaults) != 0 || f.KwDefaults != nil {
		return nil, false
	}
	if (co.Flags & (CO_VARARGS | CO_VARKEYWORDS)) != 0 {
		return nil, false
	}
	if co.DirectCaptureCount > 0 && len(f.Closure) < co.DirectCaptureCount {
		return nil, false
	}
	fnGlobals := f.Globals
	if fnGlobals == nil {
		fnGlobals = vm.globals
	}
	numLocals := max(co.NumLocals, co.NumParams)
	base := ctx.SP
	need := base + numLocals + co.MaxStackSize + 256
	stack := ctx.Stack
	wasVMStack := len(stack) > 0 && len(vm.stack) > 0 && &stack[0] == &vm.stack[0]
	if need > len(stack) {
		if wasVMStack {
			vm.stack = ensureStack(vm.stack, need)
			stack = vm.stack
			ctx.Stack = stack
		} else {
			stack = ensureStack(stack, need)
			ctx.Stack = stack
		}
	}
	nextFrame := 0
	if vm.activeCO != nil {
		nextFrame = vm.activeFrameIdx + 1
	}
	if nextFrame+4 > len(vm.frames) {
		vm.ensureFrameCapacity(nextFrame + 4)
	}

	b := &batchCall{
		co:          co,
		fn:          fn,
		fnGlobals:   fnGlobals,
		numLocals:   numLocals,
		nargs:       nargs,
		base:        base,
		spAfterBind: base + numLocals,
		nextFrame:   nextFrame,
		stack:       stack,
		wasVMStack:  wasVMStack,
	}
	b.savedActiveState = vm.activeState
	if b.savedActiveState != nil {
		vm.flushActiveFrame()
	}
	b.savedCtxStack = stack
	b.savedCtxSP = ctx.SP
	b.savedActiveCO = vm.activeCO
	b.savedActiveBaseOffset = vm.activeBaseOffset
	b.savedActiveCells = vm.activeCells
	b.savedActiveCellPtrs = vm.activeCellPtrs
	b.savedActiveFrameIdx = vm.activeFrameIdx

	vm.execCtx.Stack = stack
	vm.execCtx.SP = b.spAfterBind

	st := vm.acquireLoopState()
	st.curCO = co
	st.frameIdx = nextFrame
	st.startFrameIdx = nextFrame
	st.genFrameIdx = -1
	if nextFrame > vm.maxFrameIdx {
		vm.maxFrameIdx = nextFrame
	}
	st.base = base
	st.globals = fnGlobals
	st.curLocals = val.NoneVal()
	st.ip = co.EntryIP
	st.sp = b.spAfterBind
	st.periodicTick = 0
	st.stack = stack
	st.stackIsVM = wasVMStack
	if wasVMStack && need > vm.maxSP {
		vm.maxSP = need
	}
	b.st = st

	vm.activeCO = co
	vm.activeBaseOffset = base
	vm.activeCells = nil
	vm.activeCellPtrs = nil
	vm.activeFrameIdx = nextFrame
	vm.activeState = st
	vm.unhandledException = val.NoneVal()
	return b, true
}

// resolveCache refreshes the callee cache window, promoting Tier-2 exactly as
// the single-call path does when the code stream advances mid-batch.
func (vm *VM) resolveBatchCache(b *batchCall) {
	co := b.co
	st := b.st
	cc := vm.fastCalleeCache(co)
	if cc != nil && cc.owner == vm && atomic.LoadInt32(&co.StreamGen) == cc.streamGen {
		st.cache = cc
		st.codePtr = cc.codePtr
		st.codeLen = cc.codeLen
		st.consts = cc.frameConsts(co)
		if cc.tier2Done == 0 {
			maybeOptimizeCodeObject(vm, co, cc)
			if atomic.LoadInt32(&co.StreamGen) != cc.streamGen {
				st.codePtr, st.codeLen = vm.loadWindow(co, cc)
			} else {
				st.codePtr = cc.codePtr
				st.codeLen = cc.codeLen
			}
		}
	} else {
		cc = vm.cacheFor(co)
		st.cache = cc
		st.codePtr, st.codeLen = vm.loadWindow(co, cc)
		st.consts = cc.frameConsts(co)
		if atomic.LoadInt32(&co.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, co, cc)
			if atomic.LoadInt32(&co.StreamGen) != cc.streamGen {
				st.codePtr, st.codeLen = vm.loadWindow(co, cc)
			}
		}
	}
}

// applyBatch1 invokes the batched function with one positional argument.
func (vm *VM) applyBatch1(ctx *val.ExecContext, b *batchCall, a0 val.Value) (val.Value, bool) {
	stack := b.stack
	co := b.co
	st := b.st
	stack[b.base] = a0
	for i := 1; i < b.numLocals; i++ {
		stack[b.base+i] = val.NoneVal()
	}
	if co.DirectCaptureCount > 0 {
		copy(stack[b.base+co.DirectCaptureBase:], b.fn.AsFunction().Closure[:co.DirectCaptureCount])
	}
	if len(co.CellVars) != 0 || (len(co.FreeVars) != 0 && co.DirectCaptureCount == 0) {
		cells, cellPtrs := vm.setupCalleeCellsAndPtrs(co, b.fn, stack, b.base, 1)
		st.cells = cells
		st.cellPtrs = cellPtrs
		vm.activeCells = cells
		vm.activeCellPtrs = cellPtrs
	}
	vm.unhandledException = val.NoneVal()
	vm.resolveBatchCache(b)
	st.curCO = co
	st.frameIdx = b.nextFrame
	st.base = b.base
	st.globals = b.fnGlobals
	st.ip = co.EntryIP
	st.sp = b.spAfterBind
	// A shared loop state persists across elements: clear any nested
	// generator/resume and exit channels the previous element may have
	// touched. A freshly acquired state is zeroed by releaseLoopState,
	// so this mirrors the single-call path exactly.
	st.activeGen = nil
	st.activeCoro = nil
	st.activeAsyncGen = nil
	st.delegatedRoot = nil
	st.raiseExc = val.Value{}
	st.result = val.Value{}
	st.regCode = nil
	res := vm.runLoopWithGlobals(st)
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		ctx.Raise(exc)
		return val.NoneVal(), false
	}
	if ctx.HasException() {
		return val.NoneVal(), false
	}
	return res, true
}

// applyBatch2 invokes the batched function with two positional arguments.
func (vm *VM) applyBatch2(ctx *val.ExecContext, b *batchCall, a0, a1 val.Value) (val.Value, bool) {
	stack := b.stack
	co := b.co
	st := b.st
	stack[b.base] = a0
	stack[b.base+1] = a1
	for i := 2; i < b.numLocals; i++ {
		stack[b.base+i] = val.NoneVal()
	}
	if co.DirectCaptureCount > 0 {
		copy(stack[b.base+co.DirectCaptureBase:], b.fn.AsFunction().Closure[:co.DirectCaptureCount])
	}
	if len(co.CellVars) != 0 || (len(co.FreeVars) != 0 && co.DirectCaptureCount == 0) {
		cells, cellPtrs := vm.setupCalleeCellsAndPtrs(co, b.fn, stack, b.base, 2)
		st.cells = cells
		st.cellPtrs = cellPtrs
		vm.activeCells = cells
		vm.activeCellPtrs = cellPtrs
	}
	vm.unhandledException = val.NoneVal()
	vm.resolveBatchCache(b)
	st.curCO = co
	st.frameIdx = b.nextFrame
	st.base = b.base
	st.globals = b.fnGlobals
	st.ip = co.EntryIP
	st.sp = b.spAfterBind
	st.activeGen = nil
	st.activeCoro = nil
	st.activeAsyncGen = nil
	st.delegatedRoot = nil
	st.raiseExc = val.Value{}
	st.result = val.Value{}
	st.regCode = nil
	res := vm.runLoopWithGlobals(st)
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		ctx.Raise(exc)
		return val.NoneVal(), false
	}
	if ctx.HasException() {
		return val.NoneVal(), false
	}
	return res, true
}

// finishBatch clears the reused stack window and restores the pre-batch VM
// state. It is idempotent so entry points can defer it as a panic backstop
// while also calling it explicitly on completion.
func (vm *VM) finishBatch(ctx *val.ExecContext, b *batchCall) {
	if b == nil || b.finished {
		return
	}
	b.finished = true
	st := b.st
	if st != nil {
		if st.frameIdx > vm.maxFrameIdx {
			vm.maxFrameIdx = st.frameIdx
		}
		if st.stackIsVM {
			hw := st.base + 256
			if st.curCO != nil && st.curCO.NumLocals+st.curCO.MaxStackSize > 256 {
				hw = st.base + st.curCO.NumLocals + st.curCO.MaxStackSize
			}
			if hw > vm.maxSP {
				vm.maxSP = hw
			}
		}
	}
	clear(ctx.Stack[b.base : b.base+b.numLocals])
	if b.wasVMStack {
		vm.execCtx.Stack = vm.stack
	} else {
		vm.execCtx.Stack = b.savedCtxStack
	}
	vm.execCtx.SP = b.savedCtxSP
	vm.activeCO = b.savedActiveCO
	vm.activeBaseOffset = b.savedActiveBaseOffset
	vm.activeCells = b.savedActiveCells
	vm.activeCellPtrs = b.savedActiveCellPtrs
	vm.activeFrameIdx = b.savedActiveFrameIdx
	vm.activeState = b.savedActiveState
	if st != nil {
		st.cellPtrs = nil
		vm.releaseLoopState(st)
		b.st = nil
	}
}

// copyForwardedKwDict copies a caller-owned kwargs mapping for *args/**kwargs
// forwarding without ever handing the caller's dict to the callee, preserving
// mutability isolation. A VarKWSafe callee receives a pooled VM-owned dict
// (recycled at teardown, transferable on chained forwards); any other callee
// receives a split-cache dict so terminal kwargs stay small.
func (vm *VM) copyForwardedKwDict(kwDict *val.PyDict, kwLen int, calleeVarKWSafe bool, st *loopState, ip int) val.Value {
	switch {
	case calleeVarKWSafe:
		return vm.copyForwardedKwDictPooled(kwDict)
	case kwLen <= 16 && !val.MultiThreadedFlag.Load() && st.cache != nil:
		var extraKeysBuf [16]val.Value
		var extraValsBuf [16]val.Value
		extraKeys := extraKeysBuf[:0]
		extraVals := extraValsBuf[:0]
		kwDict.EnsureCombined()
		for i := range kwDict.Entries {
			e := &kwDict.Entries[i]
			if e.Active {
				extraKeys = append(extraKeys, val.InternValue(e.Key))
				extraVals = append(extraVals, e.Value)
			}
		}
		splitDict := vm.buildSplitOrCombinedKwDict(extraKeys, extraVals, st.cache, ip)
		vm.releaseSmallDict(kwDict)
		return val.DictVal(splitDict)
	default:
		return vm.copyForwardedKwDictPooled(kwDict)
	}
}

// copyForwardedKwDictPooled copies kwDict into a pooled VM-owned dict flagged
// with both ephemeral bits. Chained forwards transfer it with zero further
// copies; a VarKWSafe callee recycles it at teardown; any escape clears the
// flags via ClearEphemeral.
func (vm *VM) copyForwardedKwDictPooled(kwDict *val.PyDict) val.Value {
	cp := vm.acquireSmallDictFlags(kwDict.Len(), val.FlagEphemeralDict|val.FlagEphemeralFrameDict)
	cd := cp.AsDict()
	for i := range kwDict.Entries {
		e := &kwDict.Entries[i]
		if e.Active {
			cd.Set(val.InternValue(e.Key), e.Value)
		}
	}
	vm.releaseSmallDict(kwDict)
	return cp
}

// foldBatched streams it through the two-argument batched function,
// preserving reduce's lazy consumption (no upfront materialization).
// An element-function exception is raised on ctx and reported as failure.
func foldBatched(vm *VM, ctx *val.ExecContext, b *batchCall, it *val.PyIter, acc val.Value) (val.Value, bool) {
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		acc, ok = vm.applyBatch2(ctx, b, acc, item)
		if !ok {
			return val.NoneVal(), false
		}
		if ctx.HasException() {
			return val.NoneVal(), false
		}
	}
	return acc, true
}

// applyKeyFuncBatched implements val.BatchApplyHook1 for list.sort(key=...),
// reporting each key through setKey so no intermediate keys array is
// allocated. Unsupported shapes decline without touching ctx so evalSortKeys
// falls back to the generic per-element loop; key-function exceptions are
// raised on ctx and reported as failure so the list is left in a valid state.
func applyKeyFuncBatched(ctx *val.ExecContext, fn val.Value, items []val.Value, setKey func(int, val.Value)) bool {
	if !fn.IsFunction() || ctx == nil {
		return false
	}
	vm, ok := ctx.VM.(*VM)
	if !ok || vm == nil {
		return false
	}
	b, ok := vm.beginBatchCall(ctx, fn, 1)
	if !ok {
		return false
	}
	defer vm.finishBatch(ctx, b)
	n := len(items)
	if n == 0 {
		vm.finishBatch(ctx, b)
		return true
	}
	for i, itm := range items {
		k, ok := vm.applyBatch1(ctx, b, itm)
		if !ok {
			vm.finishBatch(ctx, b)
			return false
		}
		setKey(i, k)
	}
	vm.finishBatch(ctx, b)
	return true
}
