package vm

import (
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

func (vm *VM) instantiateAsyncGenerator(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) val.Value {
	agVal, _ := vm.instantiateAsyncGeneratorSafe(co, callable, defaults, kwDefaults, callerStack, argBase, nargs, kwnames, globals)
	return agVal
}

func (vm *VM) instantiateAsyncGeneratorSafe(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) (val.Value, bool) {
	need := max(64, co.NumLocals+max(co.MaxStackSize, len(co.Code))+16)
	agStack, poolCls := vm.acquireGeneratorStack(need)
	var codeVal val.Value
	if callable.IsFunction() {
		codeVal = callable.AsFunction().Code
	} else {
		codeVal = callable
	}
	qualName := co.QualName
	if qualName == "" {
		qualName = co.Name
	}
	agVal := val.NewAsyncGeneratorWithStack(co.Name, qualName, codeVal, agStack, poolCls, nil, globals)
	ag := agVal.AsAsyncGenerator()
	ag.StepHook = vm.stepAsyncGen
	ag.ReleaseHook = vm.releaseAsyncGenStack

	spAfterBind := vm.bindFunctionArgsToSlice(co, defaults, kwDefaults, ag.Stack, callerStack[argBase:argBase+nargs], kwnames)
	if vm.execCtx.HasException() {
		vm.releaseAsyncGenStack(ag)
		return val.NoneVal(), false
	}
	ag.SP = spAfterBind
	if co.DirectCaptureCount > 0 && callable.IsFunction() {
		copy(ag.Stack[co.DirectCaptureBase:], callable.AsFunction().Closure[:co.DirectCaptureCount])
	}
	if len(co.CellVars) > 0 || (len(co.FreeVars) > 0 && co.DirectCaptureCount == 0) {
		ag.Cells = vm.setupCalleeCells(co, callable, ag.Stack, 0, nargs)
	}
	return agVal, true
}

func (vm *VM) releaseAsyncGenStack(ag *val.PyAsyncGenerator) {
	if ag == nil {
		return
	}
	if ag.Stack != nil {
		clear(ag.Stack)
		vm.releaseStackSlice(ag.Stack)
		ag.Stack = nil
	}
	if ag.CellPtrs != nil {
		vm.releaseCellPtrSlice(ag.CellPtrs)
		ag.CellPtrs = nil
	}
	ag.Cells = nil
	ag.AgAwait = val.NoneVal()
	ag.YieldVal = val.NoneVal()
	ag.LastSent = val.NoneVal()
	ag.ActiveAwaitable = val.Value{}
	ag.SuspendedOnAwait = false
}

// cancelPendingAwaitable cancels/cleans up any pending waiter held by a
// pyQueueAwaitable, PyAsyncGenASend/AThrow (recursively via ag.AgAwait),
// PyFuture, or PyTask.
func cancelPendingAwaitable(v val.Value) {
	if v.IsNone() || v.HeaderPtr() == nil {
		return
	}
	if queueAwaitableType != nil && v.Type() == queueAwaitableType {
		cancelQueueAwaitable(v)
		return
	}
	if val.IsAsyncGenASend(v) {
		if ag := val.AsAsyncGenASend(v).Ag; ag != nil && !ag.AgAwait.IsNone() && ag.AgAwait.HeaderPtr() != nil {
			cancelPendingAwaitable(ag.AgAwait)
		}
		return
	}
	if val.IsAsyncGenAThrow(v) {
		if ag := val.AsAsyncGenAThrow(v).Ag; ag != nil && !ag.AgAwait.IsNone() && ag.AgAwait.HeaderPtr() != nil {
			cancelPendingAwaitable(ag.AgAwait)
		}
		return
	}
	if anextAwaitableType != nil && v.Type() == anextAwaitableType {
		cancelPendingAwaitable((*pyANextAwaitable)(unsafe.Pointer(v.HeaderPtr())).aw)
		return
	}
	if v.IsFuture() {
		f := v.AsFuture()
		if !f.IsDone() {
			f.Cancel(newCancelledError(""))
		}
		return
	}
	if v.IsTask() {
		t := v.AsTask()
		if !t.IsDone() {
			t.Cancel(newCancelledError(""))
		}
		return
	}
}

func (vm *VM) stepAsyncGen(
	ctx *val.ExecContext,
	ag *val.PyAsyncGenerator,
	awaitable val.Value,
	sentVal val.Value,
	thrownExc val.Value,
	isClose bool,
) (val.Value, val.AsyncGenStepResult) {
	if ag.Running && ag.ActiveAwaitable.HeaderPtr() != awaitable.HeaderPtr() {
		opName := "asend"
		if isClose {
			opName = "aclose"
		} else if val.IsAsyncGenAThrow(awaitable) {
			opName = "athrow"
		}
		if ctx != nil {
			ctx.Raise(val.NewRuntimeError(opName + "(): asynchronous generator is already running"))
		}
		return val.NoneVal(), val.AsyncGenStepError
	}

	hasThrown := !thrownExc.IsNone() && thrownExc.HeaderPtr() != nil

	switch ag.State {
	case val.AsyncGenClosed:
		if isClose {
			return val.NoneVal(), val.AsyncGenStepDone
		}
		if hasThrown {
			if ctx != nil {
				ctx.Raise(thrownExc)
			}
			return val.NoneVal(), val.AsyncGenStepError
		}
		return val.NoneVal(), val.AsyncGenStepDone

	case val.AsyncGenCreated:
		if isClose {
			ag.State = val.AsyncGenClosed
			vm.releaseAsyncGenStack(ag)
			return val.NoneVal(), val.AsyncGenStepDone
		}
		if hasThrown {
			ag.State = val.AsyncGenClosed
			vm.releaseAsyncGenStack(ag)
			if ctx != nil {
				ctx.Raise(thrownExc)
			}
			return val.NoneVal(), val.AsyncGenStepError
		}
		if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
			if ctx != nil {
				ctx.Raise(val.NewTypeError("can't send non-None value to a just-started async generator"))
			}
			return val.NoneVal(), val.AsyncGenStepError
		}
		ag.IP = AsCodeObject(ag.Code).EntryIP

	case val.AsyncGenSuspended:
		if isClose && !ag.SuspendedOnAwait && !hasThrown {
			thrownExc = val.NewException(val.GeneratorExitType, "")
			hasThrown = true
		}
		if ag.SuspendedOnAwait {
			ag.LastSent = sentVal
			if hasThrown {
				if !ag.AgAwait.IsNone() && ag.AgAwait.HeaderPtr() != nil {
					cancelPendingAwaitable(ag.AgAwait)
				}
				ag.AgAwait = val.NoneVal()
				ag.SuspendedOnAwait = false
			}
		} else if !hasThrown {
			ag.LastSent = sentVal
			ag.Stack[ag.SP] = sentVal
			ag.SP++
		}
	}

	ag.Running = true
	ag.ActiveAwaitable = awaitable
	ag.State = val.AsyncGenRunning

	co := AsCodeObject(ag.Code)
	nextFrame := 0
	if vm.activeCO != nil {
		nextFrame = vm.activeFrameIdx + 1
	}
	vm.ensureFrameCapacity(nextFrame + 4)
	vm.unhandledException = val.NoneVal()
	if hasThrown {
		vm.execCtx.Raise(thrownExc)
	}

	res := vm.runAsyncGenFrame(co, ag, nextFrame)

	if ag.State == val.AsyncGenSuspended {
		if ag.SuspendedOnAwait {
			return ag.YieldVal, val.AsyncGenStepAwait
		}
		ag.Running = false
		ag.ActiveAwaitable = val.Value{}
		ag.AgAwait = val.NoneVal()
		return res, val.AsyncGenStepYielded
	}

	ag.Running = false
	ag.ActiveAwaitable = val.Value{}
	ag.AgAwait = val.NoneVal()
	ag.State = val.AsyncGenClosed
	vm.releaseAsyncGenStack(ag)

	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		vm.lastException = val.NoneVal()
		if isClose {
			if val.IsInstance(exc, val.GeneratorExitType) || val.IsInstance(exc, val.StopAsyncIterationType) {
				return val.NoneVal(), val.AsyncGenStepDone
			}
			if ctx != nil {
				ctx.Raise(exc)
			}
			return val.NoneVal(), val.AsyncGenStepError
		}
		if val.IsInstance(exc, val.StopIterationType) {
			rtErr := val.NewRuntimeError("async generator raised StopIteration")
			if rtErr.IsInstance() {
				rtErr.AsInstance().SetAttr(nameCause, exc)
				rtErr.AsInstance().SetAttr(nameContext, exc)
			}
			exc = rtErr
		} else if val.IsInstance(exc, val.StopAsyncIterationType) {
			rtErr := val.NewRuntimeError("async generator raised StopAsyncIteration")
			if rtErr.IsInstance() {
				rtErr.AsInstance().SetAttr(nameCause, exc)
				rtErr.AsInstance().SetAttr(nameContext, exc)
			}
			exc = rtErr
		}
		if ctx != nil {
			ctx.Raise(exc)
		}
		return val.NoneVal(), val.AsyncGenStepError
	}

	return val.NoneVal(), val.AsyncGenStepDone
}

func (vm *VM) runAsyncGenFrame(co *CodeObject, ag *val.PyAsyncGenerator, startFrameIdx int) val.Value {
	savedActiveState := vm.activeState
	if savedActiveState != nil {
		vm.flushActiveFrame()
	}
	savedCtxStack := vm.execCtx.Stack
	savedCtxSP := vm.execCtx.SP
	wasVMStack := len(savedCtxStack) > 0 && len(vm.stack) > 0 && &savedCtxStack[0] == &vm.stack[0]
	vm.execCtx.Stack = ag.Stack
	vm.execCtx.SP = ag.SP
	savedActiveCO := vm.activeCO
	savedActiveBaseOffset := vm.activeBaseOffset
	savedActiveCells := vm.activeCells
	savedActiveCellPtrs := vm.activeCellPtrs
	savedActiveFrameIdx := vm.activeFrameIdx

	st := vm.acquireLoopState()
	var cellPtrs []*val.PyCell
	if len(ag.CellPtrs) > 0 {
		cellPtrs = ag.CellPtrs
	} else if len(ag.Cells) > 0 {
		cellPtrs = vm.acquireGenCellPtrs(len(ag.Cells))
		for i, c := range ag.Cells {
			cellPtrs[i] = c.AsCell()
		}
		ag.CellPtrs = cellPtrs
	}

	defer func() {
		if st.frameIdx > vm.maxFrameIdx {
			vm.maxFrameIdx = st.frameIdx
		}
		if wasVMStack {
			vm.execCtx.Stack = vm.stack
		} else {
			vm.execCtx.Stack = savedCtxStack
		}
		vm.execCtx.SP = savedCtxSP
		vm.activeCO = savedActiveCO
		vm.activeBaseOffset = savedActiveBaseOffset
		vm.activeCells = savedActiveCells
		vm.activeCellPtrs = savedActiveCellPtrs
		vm.activeFrameIdx = savedActiveFrameIdx
		vm.activeState = savedActiveState
		st.cellPtrs = nil
		vm.releaseLoopState(st)
	}()

	globals := ag.Globals
	if globals == nil {
		globals = vm.globals
	}

	st.curCO = co
	st.consts = co.Consts
	st.frameIdx = startFrameIdx
	st.startFrameIdx = startFrameIdx
	st.genFrameIdx = startFrameIdx
	if startFrameIdx > vm.maxFrameIdx {
		vm.maxFrameIdx = startFrameIdx
	}
	st.base = ag.Base
	st.cells = ag.Cells
	st.cellPtrs = cellPtrs
	st.globals = globals
	st.curLocals = val.NoneVal()
	st.activeAsyncGen = ag
	st.ip = ag.IP
	st.sp = ag.SP
	st.periodicTick = 0

	vm.activeCO = co
	vm.activeBaseOffset = ag.Base
	vm.activeCells = ag.Cells
	vm.activeCellPtrs = cellPtrs
	vm.activeFrameIdx = startFrameIdx

	stack := ag.Stack
	minStack := ag.Base + max(256, co.NumLocals+max(co.MaxStackSize, len(co.Code))+64)
	if minStack > len(stack) {
		stack = vm.growStack(st, stack, minStack)
		ag.Stack = stack
	}
	st.stack = stack
	st.stackIsVM = false

	st.cache = vm.cacheFor(co)
	st.codePtr, st.codeLen = vm.loadWindow(co, st.cache)
	st.consts = st.cache.frameConsts(co)
	vm.activeState = st

	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		unwindIP := ag.IP
		if unwindIP > 0 && (unwindIP >= st.codeLen || Opcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(unwindIP-1)*4))>>24) == OP_YIELD_VALUE) {
			unwindIP = ag.IP - 1
		}
		st.raiseExc = exc
		newIP, newSP, ctl := vm.unwind(st, unwindIP)
		if ctl == ctlExit {
			return st.result
		}
		st.ip = newIP
		st.sp = newSP
	}

	return vm.runLoopWithGlobals(st)
}

var (
	anextAwaitableOnce sync.Once
	anextAwaitableType *val.PyType
)

// pyANextAwaitable wraps anet(it, default) to catch StopAsyncIteration and return default.
// Header MUST be at offset 0.
type pyANextAwaitable struct {
	Header     val.ObjectHeader
	aw         val.Value
	defaultVal val.Value
	done       bool
}

var _ [0 - unsafe.Offsetof(pyANextAwaitable{}.Header)]byte

func initANextAwaitableType() {
	anextAwaitableOnce.Do(func() {
		anextAwaitableType = val.MustNewType("anext_awaitable", []*val.PyType{val.ObjectType}, nil)
		anextAwaitableType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value { return o }
		anextAwaitableType.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			return (*pyANextAwaitable)(unsafe.Pointer(o.HeaderPtr())).step(ctx)
		}
		anextAwaitableType.Dict.Set(val.InternString("__await__"), val.NewBuiltinFuncVal("__await__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__await__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return args[0]
		}))
	})
}

func newANextAwaitableVal(aw, defaultVal val.Value) val.Value {
	initANextAwaitableType()
	a := &pyANextAwaitable{
		Header:     val.ObjectHeader{Kind: val.KindNative, Type: anextAwaitableType},
		aw:         aw,
		defaultVal: defaultVal,
	}
	return val.ObjectVal(&a.Header)
}

func (a *pyANextAwaitable) step(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	if a.done {
		ctx.Raise(val.NewRuntimeError("cannot reuse already awaited anext()"))
		return val.NoneVal(), val.IterError
	}
	aw := a.aw
	if aw.IsFuture() || aw.IsTask() {
		fut := aw.AsFuture()
		if aw.IsTask() {
			fut = &aw.AsTask().Future
		}
		if !fut.IsDone() {
			return aw, val.IterYielded
		}
		res, exc := fut.Result()
		a.done = true
		if !exc.IsNone() && exc.HeaderPtr() != nil {
			if val.IsInstance(exc, val.StopAsyncIterationType) {
				return a.defaultVal, val.IterExhausted
			}
			ctx.Raise(exc)
			return val.NoneVal(), val.IterError
		}
		return res, val.IterExhausted
	}
	var res val.Value
	var status val.IterStatus
	switch {
	case aw.IsCoroutine():
		res, status = aw.AsCoroutine().Send(ctx, val.NoneVal())
		if status == val.IterExhausted {
			res = aw.AsCoroutine().RetVal
		}
	case aw.IsGenerator():
		res, status = aw.AsGenerator().Send(ctx, val.NoneVal())
		if status == val.IterExhausted {
			res = aw.AsGenerator().RetVal
		}
	case aw.Type().Slots.TpIterNext != nil:
		res, status = aw.Type().Slots.TpIterNext(ctx, aw)
	default:
		a.done = true
		return aw, val.IterExhausted
	}
	switch status {
	case val.IterYielded:
		return res, val.IterYielded
	case val.IterExhausted:
		a.done = true
		return res, val.IterExhausted
	default:
		a.done = true
		if ctx != nil && ctx.HasException() {
			if val.IsInstance(ctx.ActiveException, val.StopAsyncIterationType) {
				ctx.ClearException()
				return a.defaultVal, val.IterExhausted
			}
		}
		return val.NoneVal(), val.IterError
	}
}
