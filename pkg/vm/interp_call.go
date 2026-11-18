package vm

import (
	"io"
	"os"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opCallKw(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	totalArgs := int(uint8(inst >> 16))
	bx := int(uint16(inst))
	kwnames := st.consts[bx].AsTuple().Items
	isDirectAwait := ip+2 < st.codeLen &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_GET_AWAITABLE &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))) == OP_AWAIT

	target := stack[sp-totalArgs-1]
	newBase := sp - totalArgs
	retSP := sp - totalArgs - 1

	if vm.hasTrackedViews && totalArgs > 0 {
		markViewsEscaped(stack[newBase : newBase+totalArgs])
	}
	switch target.Kind() {
	case val.KindFunction:
		fn := target.AsFunction()
		callee := AsCodeObject(fn.Code)
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = st.globals
		}
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames, fnGlobals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if callee.IsCoroutine {
			coroVal, ok := vm.instantiateCoroutineSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames, fnGlobals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = coroVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		nextSP, ok := vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames)
		if !ok {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		if st.frameIdx+2 > len(vm.frames) {
			vm.ensureFrameCapacity(st.frameIdx + 2)
		}
		saved := &vm.frames[st.frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		if isDirectAwait && callee.IsCoroutine {
			saved.ip = ip + 3
		} else {
			saved.ip = ip + 1
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
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		st.curLocals = val.NoneVal()
		st.frameIdx++
		st.curCO = callee
		sp = nextSP
		st.cache = vm.cacheFor(st.curCO)
		st.codePtr = st.reloadWindow(vm, callee)
		st.consts = callee.Consts
		st.base = newBase
		if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, callee, st.cache)
		}
		st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, target, stack, newBase, totalArgs)
		if fn.Globals != nil {
			st.globals = fn.Globals
		}
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		if vm.activeCellPtrs != nil || st.cellPtrs != nil {
			vm.activeCells = st.cells
			vm.activeCellPtrs = st.cellPtrs
		}
		vm.activeFrameIdx = st.frameIdx
		ip = 0
		return ip, sp, ctlReload

	case val.KindCode:
		callee := AsCodeObject(target)
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, nil, nil, stack, newBase, totalArgs, kwnames, st.globals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if callee.IsCoroutine {
			coroVal, ok := vm.instantiateCoroutineSafe(callee, target, nil, nil, stack, newBase, totalArgs, kwnames, st.globals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = coroVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		nextSP, ok := vm.bindFunctionArgsSafe(callee, nil, nil, stack, newBase, totalArgs, kwnames)
		if !ok {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		if st.frameIdx+2 > len(vm.frames) {
			vm.ensureFrameCapacity(st.frameIdx + 2)
		}
		saved := &vm.frames[st.frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		if isDirectAwait && callee.IsCoroutine {
			saved.ip = ip + 3
		} else {
			saved.ip = ip + 1
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
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		st.curLocals = val.NoneVal()
		st.frameIdx++
		st.curCO = callee
		sp = nextSP
		st.cache = vm.cacheFor(st.curCO)
		st.codePtr = st.reloadWindow(vm, callee)
		st.consts = callee.Consts
		st.base = newBase
		if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, callee, st.cache)
		}
		st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, target, stack, newBase, totalArgs)
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		if vm.activeCellPtrs != nil || st.cellPtrs != nil {
			vm.activeCells = st.cells
			vm.activeCellPtrs = st.cellPtrs
		}
		vm.activeFrameIdx = st.frameIdx
		ip = 0
		return ip, sp, ctlReload

	case val.KindBuiltinFunc:
		builtin := target.AsBuiltinFunc()
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + totalArgs
		res := vm.invokeBuiltinSafe(builtin.Fn, target, stack[newBase:newBase+totalArgs], kwnames)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++

	case val.KindType:
		tp := target.AsType()
		meta := tp.Header.Type
		if tp.UsesDefaultNew && (tp.Flags&val.FlagHeapType) != 0 && (meta == nil || meta == val.TypeType) && !val.IsSubclass(tp, val.TypeType) && len(kwnames) == 0 {
			if !tp.HasInit() {
				stack[retSP] = val.NewInstanceOfType(tp)
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
			initFn := tp.DunderInitVal()
			if initFn.IsFunction() {
				initFunc := initFn.AsFunction()
				callee := AsCodeObject(initFunc.Code)
				if !callee.IsGenerator && !callee.IsCoroutine {
					minCap := max(newBase+1+totalArgs+256, newBase+1+callee.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					copy(stack[newBase+1:newBase+1+totalArgs], stack[newBase:newBase+totalArgs])
					inst := val.NewInstanceOfType(tp)
					stack[newBase] = inst
					totalInitArgs := totalArgs + 1
					var nextSP int
					if totalInitArgs == callee.NumParams && len(initFunc.Defaults) == 0 && initFunc.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && len(callee.CellVars) == 0 {
						nextSP = newBase + max(callee.NumLocals, totalInitArgs)
					} else {
						var ok bool
						nextSP, ok = vm.bindFunctionArgsSafe(callee, initFunc.Defaults, initFunc.KwDefaults, stack, newBase, totalInitArgs, nil)
						if !ok {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, sp, ctlRaise
						}
					}
					if st.frameIdx+2 > len(vm.frames) {
						vm.ensureFrameCapacity(st.frameIdx + 2)
					}
					saved := &vm.frames[st.frameIdx]
					saved.co = st.curCO
					saved.codePtr = st.codePtr
					saved.codeLen = st.codeLen
					saved.consts = st.consts
					saved.ip = ip + 1
					saved.baseOffset = st.base
					saved.retSP = retSP
					saved.cells = st.cells
					saved.cellPtrs = st.cellPtrs
					saved.globals = st.globals
					saved.locals = st.curLocals
					saved.cache = st.cache
					saved.excStackLen = len(vm.excStack)
					saved.stackIsVM = st.stackIsVM
					saved.initInstance = inst
					saved.cmpActive = false
					st.curLocals = val.NoneVal()
					st.frameIdx++
					st.curCO = callee
					st.cache = vm.cacheFor(callee)
					st.codePtr = st.reloadWindow(vm, callee)
					st.consts = callee.Consts
					st.base = newBase
					switch {
					case len(callee.CellVars) == 0 && initFunc.Closure != nil:
						st.cells = initFunc.Closure
						st.cellPtrs = initFunc.CellPtrs
					case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
						st.cells = nil
						st.cellPtrs = nil
					default:
						st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, initFn, stack, newBase, totalInitArgs)
					}
					if initFunc.Globals != nil {
						st.globals = initFunc.Globals
					}
					vm.activeCO = st.curCO
					vm.activeBaseOffset = st.base
					if vm.activeCellPtrs != nil || st.cellPtrs != nil {
						vm.activeCells = st.cells
						vm.activeCellPtrs = st.cellPtrs
					}
					vm.activeFrameIdx = st.frameIdx
					sp = nextSP
					ip = 0
					return ip, sp, ctlReload
				}
			}
		}
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + totalArgs
		res := vm.callFromHookSafe(target, newBase, totalArgs, kwnames)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++
	default:
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + totalArgs
		res := vm.callFromHookSafe(target, newBase, totalArgs, kwnames)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++
	}
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opCallFunctionEx(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	hasKW := (inst>>16)&0x01 != 0
	var kwDictVal val.Value
	var argsVal val.Value
	var callable val.Value
	if hasKW {
		kwDictVal = stack[sp-1]
		argsVal = stack[sp-2]
		callable = stack[sp-3]
		sp -= 3
	} else {
		argsVal = stack[sp-1]
		callable = stack[sp-2]
		sp -= 2
	}
	st.stack = stack
	vm.syncActiveFrame(st, ip)
	vm.execCtx.SP = sp
	res := vm.callFunctionExSafe(callable, argsVal, kwDictVal, hasKW)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = res
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opCallSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	bx := int(uint16(inst))
	isDirectAwait := ip+2 < st.codeLen &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_GET_AWAITABLE &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))) == OP_AWAIT

	var target val.Value
	var newBase int
	var retSP int

	if bx < len(st.consts) && isCallableConst(st.consts[bx]) {
		target = st.consts[bx]
		newBase = sp - argc
		retSP = newBase
	} else {
		target = stack[sp-argc-1]
		newBase = sp - argc
		retSP = sp - argc - 1
	}

	// Builtin range fast-path: 0 allocations
	if target == vm.builtinRange && argc == 1 && stack[newBase].IsInt64() {
		stop := stack[newBase].AsInt64()
		if stop >= 0 && stop <= 0x7FFFFFFF {
			stack[retSP] = val.RangeVal(0, int32(stop))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
	}

	// Builtin len fast-path: 0 allocations
	if (target == vm.builtinLen || (target.IsBuiltinFunc() && target.AsBuiltinFunc().Name == "len")) && argc == 1 {
		if vm.builtinLen.IsNone() {
			vm.builtinLen = target
		}
		arg := stack[newBase]
		switch arg.Kind() {
		case val.KindMemoryView:
			mv := arg.AsMemoryView()
			switch {
			case mv.NDim > 0:
				stack[retSP] = val.Int64Val(int64(mv.Dim(0)))
			case mv.ItemSize > 1:
				stack[retSP] = val.Int64Val(int64(len(mv.Buf) / mv.ItemSize))
			default:
				stack[retSP] = val.Int64Val(int64(len(mv.Buf)))
			}
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindList:
			stack[retSP] = val.Int64Val(int64(len(arg.AsList().Items)))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindTuple:
			stack[retSP] = val.Int64Val(int64(len(arg.AsTuple().Items)))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindBytes:
			stack[retSP] = val.Int64Val(int64(len(arg.AsBytes().Data)))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindByteArray:
			stack[retSP] = val.Int64Val(int64(len(arg.AsByteArray().Data)))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindString, val.KindInlineStr:
			stack[retSP] = val.Int64Val(int64(arg.StringLen()))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindDict:
			stack[retSP] = val.Int64Val(int64(arg.AsDict().Len()))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		case val.KindSet:
			stack[retSP] = val.Int64Val(int64(arg.AsSet().Len()))
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
	}

	// Builtin str fast-path: 0 allocations for int64 and strings
	if target.IsType() && target.AsType() == val.StrType && argc == 1 {
		arg := stack[newBase]
		if arg.IsInt64() {
			stack[retSP] = val.IntToStringVal(arg.AsInt64())
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if arg.IsAnyString() {
			stack[retSP] = arg
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
	}

	// Builtin open fast-path: fast-path for "w" and "r" modes
	if (target == vm.builtinOpen || (target.IsBuiltinFunc() && target.AsBuiltinFunc().Name == "open")) && argc == 2 {
		if vm.builtinOpen.IsNone() {
			vm.builtinOpen = target
		}
		pathVal := stack[newBase]
		modeVal := stack[newBase+1]
		if pathVal.IsAnyString() && modeVal.IsAnyString() {
			switch modeVal.AsString() {
			case "w":
				pathStr := pathVal.AsString()
				f, err := os.OpenFile(pathStr, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
				if err == nil {
					pooledBuf := fileBufferPool.Get().(*[]byte)
					pf := &pyFile{
						inst:      val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
						f:         f,
						writeBuf:  (*pooledBuf)[:0],
						pooledBuf: pooledBuf,
					}
					stack[retSP] = val.ObjectVal(&pf.inst.Header)
					sp = retSP + 1
					ip++
					return ip, sp, ctlReload
				}
			case "r":
				pathStr := pathVal.AsString()
				f, err := os.OpenFile(pathStr, os.O_RDONLY, 0666)
				if err == nil {
					pf := &pyFile{
						inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
						f:    f,
					}
					stack[retSP] = val.ObjectVal(&pf.inst.Header)
					sp = retSP + 1
					ip++
					return ip, sp, ctlReload
				}
			}
		}
	}

	if vm.hasTrackedViews && argc > 0 {
		markViewsEscaped(stack[newBase : newBase+argc])
	}
	switch target.Kind() {
	case val.KindCode:
		callee := AsCodeObject(target)
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, nil, nil, stack, newBase, argc, nil, st.globals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if callee.IsCoroutine {
			if isDirectAwait && !callee.CanSuspend {
				// Inlined direct-await of synchronous coroutine
			} else {
				coroVal, ok := vm.instantiateCoroutineSafe(callee, target, nil, nil, stack, newBase, argc, nil, st.globals)
				if !ok {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = coroVal
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		}
		if st.frameIdx+2 > len(vm.frames) {
			vm.ensureFrameCapacity(st.frameIdx + 2)
		}
		saved := &vm.frames[st.frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		if isDirectAwait && callee.IsCoroutine {
			saved.ip = ip + 3
		} else {
			if isDirectAwait && callee.IsCoroutine {
				saved.ip = ip + 3
			} else {
				saved.ip = ip + 1
			}
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
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		st.curLocals = val.NoneVal()
		st.frameIdx++
		st.curCO = callee
		st.cache = vm.cacheFor(st.curCO)
		st.codePtr = st.reloadWindow(vm, callee)
		st.consts = callee.Consts
		st.base = newBase
		minCap := max(st.base+256, st.base+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, callee, st.cache)
		}
		st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, target, stack, newBase, argc)
		numLocalsCallee := max(callee.NumLocals, argc)
		sp = newBase + numLocalsCallee
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		if vm.activeCellPtrs != nil || st.cellPtrs != nil {
			vm.activeCells = st.cells
			vm.activeCellPtrs = st.cellPtrs
		}
		vm.activeFrameIdx = st.frameIdx
		ip = 0
		return ip, sp, ctlReload

	case val.KindFunction:
		fn := target.AsFunction()
		callee := AsCodeObject(fn.Code)
		if callee == st.curCO {
			// The recursive fast path below keeps the current code
			// object, so its cache still holds the counter.
			if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
				maybeOptimizeCodeObject(vm, callee, st.cache)
			}
		}
		st.periodicTick++
		if (st.periodicTick & 0xFF) == 0 {
			interpreterSafepoint()
			if vm.limits.Context != nil {
				if err := vm.limits.Context.Err(); err != nil {
					vm.unhandledException = val.NewRuntimeError("execution timed out or cancelled")
					panic(ErrTimeoutExceeded)
				}
			}
			if vm.limits.MaxSteps > 0 {
				vm.stepCount += 256
				if vm.stepCount >= vm.limits.MaxSteps {
					vm.unhandledException = val.NewRuntimeError("instruction step quota exceeded")
					panic(ErrStepLimitExceeded)
				}
			}
			if vm.limits.MaxStackDepth > 0 && st.frameIdx >= vm.limits.MaxStackDepth {
				vm.unhandledException = val.NewRuntimeError("maximum call stack depth exceeded")
				panic(ErrMaxStackDepthExceeded)
			}
		}
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = st.globals
		}
		if callee == st.curCO && !callee.IsGenerator && (!callee.IsCoroutine || (isDirectAwait && !callee.CanSuspend)) && argc == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && len(callee.CellVars) == 0 {
			if st.frameIdx+2 > len(vm.frames) {
				vm.ensureFrameCapacity(st.frameIdx + 2)
			}
			saved := &vm.frames[st.frameIdx]
			saved.co = st.curCO
			// Pick up a stream that the Tier-2 optimizer published for this
			// code object since the last reload.
			st.codePtr = st.reloadWindow(vm, callee)
			saved.codePtr = st.codePtr
			saved.codeLen = st.codeLen
			saved.consts = st.consts
			if isDirectAwait && callee.IsCoroutine {
				saved.ip = ip + 3
			} else {
				if isDirectAwait && callee.IsCoroutine {
					saved.ip = ip + 3
				} else {
					saved.ip = ip + 1
				}
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
			saved.initInstance = val.Value{}
			saved.cmpActive = false
			st.curLocals = val.NoneVal()
			st.frameIdx++
			st.base = newBase
			minCap := max(st.base+256, st.base+callee.NumLocals+64)
			if minCap > len(stack) {
				vm.growStack(st, stack, minCap)
			}
			numLocalsCallee := max(callee.NumLocals, argc)
			sp = newBase + numLocalsCallee
			vm.activeBaseOffset = st.base
			vm.activeFrameIdx = st.frameIdx
			ip = 0
			return ip, sp, ctlReload
		}
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil, fnGlobals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if callee.IsCoroutine {
			if isDirectAwait && !callee.CanSuspend {
				// Inlined direct-await of synchronous coroutine
			} else {
				coroVal, ok := vm.instantiateCoroutineSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil, fnGlobals)
				if !ok {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = coroVal
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		var nextSP int
		if argc == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 {
			numLocalsCallee := max(callee.NumLocals, argc)
			nextSP = newBase + numLocalsCallee
		} else {
			var ok bool
			nextSP, ok = vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
		if st.frameIdx+2 > len(vm.frames) {
			vm.ensureFrameCapacity(st.frameIdx + 2)
		}
		saved := &vm.frames[st.frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		if isDirectAwait && callee.IsCoroutine {
			saved.ip = ip + 3
		} else {
			saved.ip = ip + 1
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
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		st.curLocals = val.NoneVal()
		st.frameIdx++
		st.curCO = callee
		sp = nextSP
		st.cache = vm.cacheFor(st.curCO)
		st.codePtr = st.reloadWindow(vm, callee)
		st.consts = callee.Consts
		st.base = newBase
		if len(callee.ExceptionTable) == 0 && !callee.IsGenerator && !callee.IsCoroutine && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, callee, st.cache)
			st.codePtr = st.reloadWindow(vm, callee)
		}
		switch {
		case len(callee.CellVars) == 0 && fn.Closure != nil:
			st.cells = fn.Closure
			st.cellPtrs = fn.CellPtrs
		case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
			st.cells = nil
			st.cellPtrs = nil
		default:
			st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, target, stack, newBase, argc)
		}
		if fn.Globals != nil {
			st.globals = fn.Globals
		}
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		if vm.activeCellPtrs != nil || st.cellPtrs != nil {
			vm.activeCells = st.cells
			vm.activeCellPtrs = st.cellPtrs
		}
		vm.activeFrameIdx = st.frameIdx
		ip = 0
		return ip, sp, ctlReload

	case val.KindBuiltinFunc:
		builtin := target.AsBuiltinFunc()
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + argc
		res := vm.invokeBuiltinSafe(builtin.Fn, target, stack[newBase:newBase+argc], nil)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++

	case val.KindType:
		tp := target.AsType()
		meta := tp.Header.Type
		if tp.UsesDefaultNew && (tp.Flags&val.FlagHeapType) != 0 && (meta == nil || meta == val.TypeType) && !val.IsSubclass(tp, val.TypeType) {
			if !tp.HasInit() {
				stack[retSP] = val.NewInstanceOfType(tp)
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
			initFn := tp.DunderInitVal()
			if initFn.IsFunction() {
				initFunc := initFn.AsFunction()
				callee := AsCodeObject(initFunc.Code)
				if !callee.IsGenerator && !callee.IsCoroutine {
					minCap := max(newBase+1+argc+256, newBase+1+callee.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					copy(stack[newBase+1:newBase+1+argc], stack[newBase:newBase+argc])
					inst := val.NewInstanceOfType(tp)
					stack[newBase] = inst
					totalInitArgs := argc + 1
					var nextSP int
					if totalInitArgs == callee.NumParams && len(initFunc.Defaults) == 0 && initFunc.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && len(callee.CellVars) == 0 {
						nextSP = newBase + max(callee.NumLocals, totalInitArgs)
					} else {
						var ok bool
						nextSP, ok = vm.bindFunctionArgsSafe(callee, initFunc.Defaults, initFunc.KwDefaults, stack, newBase, totalInitArgs, nil)
						if !ok {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, sp, ctlRaise
						}
					}
					if st.frameIdx+2 > len(vm.frames) {
						vm.ensureFrameCapacity(st.frameIdx + 2)
					}
					saved := &vm.frames[st.frameIdx]
					saved.co = st.curCO
					saved.codePtr = st.codePtr
					saved.codeLen = st.codeLen
					saved.consts = st.consts
					saved.ip = ip + 1
					saved.baseOffset = st.base
					saved.retSP = retSP
					saved.cells = st.cells
					saved.cellPtrs = st.cellPtrs
					saved.globals = st.globals
					saved.locals = st.curLocals
					saved.cache = st.cache
					saved.excStackLen = len(vm.excStack)
					saved.stackIsVM = st.stackIsVM
					saved.initInstance = inst
					saved.cmpActive = false
					st.curLocals = val.NoneVal()
					st.frameIdx++
					st.curCO = callee
					st.cache = vm.cacheFor(callee)
					st.codePtr = st.reloadWindow(vm, callee)
					st.consts = callee.Consts
					st.base = newBase
					switch {
					case len(callee.CellVars) == 0 && initFunc.Closure != nil:
						st.cells = initFunc.Closure
						st.cellPtrs = initFunc.CellPtrs
					case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
						st.cells = nil
						st.cellPtrs = nil
					default:
						st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, initFn, stack, newBase, totalInitArgs)
					}
					if initFunc.Globals != nil {
						st.globals = initFunc.Globals
					}
					vm.activeCO = st.curCO
					vm.activeBaseOffset = st.base
					if vm.activeCellPtrs != nil || st.cellPtrs != nil {
						vm.activeCells = st.cells
						vm.activeCellPtrs = st.cellPtrs
					}
					vm.activeFrameIdx = st.frameIdx
					sp = nextSP
					ip = 0
					return ip, sp, ctlReload
				}
			}
		}
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + argc
		res := vm.callFromHookSafe(target, newBase, argc, nil)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++
	default:
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + argc
		res := vm.callFromHookSafe(target, newBase, argc, nil)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++
	}
	return ip, sp, ctlReload

}

//go:noinline
func (vm *VM) opCallMethodSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	retSP := sp - argc - 2
	receiver := stack[retSP]
	callable := stack[retSP+1]
	isDirectAwait := ip+2 < st.codeLen &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_GET_AWAITABLE &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))) == OP_AWAIT

	var newBase int
	var totalArgs int

	if !receiver.IsUnbound() {
		// Bound method call fast path: Receiver is already at stack[retSP].
		// Shift positional arguments down by 1 slot over Callable so [Receiver, arg1, ..., argN] are contiguous.
		newBase = retSP
		totalArgs = argc + 1
		if argc == 1 {
			stack[retSP+1] = stack[retSP+2]
		} else if argc > 1 {
			copy(stack[retSP+1:retSP+1+argc], stack[retSP+2:retSP+2+argc])
		}
	} else {
		newBase = retSP + 2
		totalArgs = argc
	}

	if argc == 1 && !receiver.IsUnbound() && receiver.IsInstance() && callable.IsBuiltinFunc() && callable.AsBuiltinFunc().Name == "write" {
		switch receiver.Type() {
		case StringIOType:
			sio := (*pyStringIO)(val.AsPointer(receiver))
			arg := stack[retSP+1]
			oldLen := len(sio.buf)
			if sio.pos == oldLen {
				var ok bool
				sio.buf, ok = val.AppendValueBytes(sio.buf, arg)
				if !ok {
					s := arg.AsString()
					view := unsafe.Slice(unsafe.StringData(s), len(s))
					sio.buf = append(sio.buf, view...)
				}
				written := len(sio.buf) - oldLen
				sio.pos = len(sio.buf)
				stack[retSP] = val.Int64Val(int64(written))
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		case FileType:
			pf := (*pyFile)(val.AsPointer(receiver))
			if !pf.isClosed && !pf.binary {
				arg := stack[retSP+1]
				oldLen := len(pf.writeBuf)
				var ok bool
				pf.writeBuf, ok = val.AppendValueBytes(pf.writeBuf, arg)
				if !ok {
					s := arg.AsString()
					view := unsafe.Slice(unsafe.StringData(s), len(s))
					pf.writeBuf = append(pf.writeBuf, view...)
				}
				written := len(pf.writeBuf) - oldLen
				if pf.unbuffered || len(pf.writeBuf) >= 32768 {
					if pf.f != nil {
						_, _ = pf.f.Write(pf.writeBuf)
						pf.writeBuf = pf.writeBuf[:0]
					}
				}
				stack[retSP] = val.Int64Val(int64(written))
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		}
	}

	if vm.hasTrackedViews && totalArgs > 0 {
		markViewsEscaped(stack[newBase : newBase+totalArgs])
	}
	switch callable.Kind() {
	case val.KindFunction:
		fn := callable.AsFunction()
		callee := AsCodeObject(fn.Code)
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = st.globals
		}
		if callee == st.curCO && !callee.IsGenerator && (!callee.IsCoroutine || (isDirectAwait && !callee.CanSuspend)) && argc == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && len(callee.CellVars) == 0 {
			if st.frameIdx+2 > len(vm.frames) {
				vm.ensureFrameCapacity(st.frameIdx + 2)
			}
			saved := &vm.frames[st.frameIdx]
			saved.co = st.curCO
			saved.codePtr = st.codePtr
			saved.codeLen = st.codeLen
			saved.consts = st.consts
			if isDirectAwait && callee.IsCoroutine {
				saved.ip = ip + 3
			} else {
				if isDirectAwait && callee.IsCoroutine {
					saved.ip = ip + 3
				} else {
					saved.ip = ip + 1
				}
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
			saved.initInstance = val.Value{}
			saved.cmpActive = false
			st.curLocals = val.NoneVal()
			st.frameIdx++
			st.base = newBase
			minCap := max(st.base+256, st.base+callee.NumLocals+64)
			if minCap > len(stack) {
				vm.growStack(st, stack, minCap)
			}
			numLocalsCallee := max(callee.NumLocals, argc)
			sp = newBase + numLocalsCallee
			vm.activeBaseOffset = st.base
			vm.activeFrameIdx = st.frameIdx
			ip = 0
			return ip, sp, ctlReload
		}
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, callable, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil, fnGlobals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if callee.IsCoroutine {
			coroVal, ok := vm.instantiateCoroutineSafe(callee, callable, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil, fnGlobals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = coroVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		var nextSP int
		if totalArgs == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 {
			numLocalsCallee := max(callee.NumLocals, totalArgs)
			nextSP = newBase + numLocalsCallee
		} else {
			var ok bool
			nextSP, ok = vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
		if st.frameIdx+2 > len(vm.frames) {
			vm.ensureFrameCapacity(st.frameIdx + 2)
		}
		saved := &vm.frames[st.frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		if isDirectAwait && callee.IsCoroutine {
			saved.ip = ip + 3
		} else {
			saved.ip = ip + 1
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
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		st.curLocals = val.NoneVal()
		st.frameIdx++
		st.curCO = callee
		sp = nextSP
		st.cache = vm.cacheFor(st.curCO)
		st.codePtr = st.reloadWindow(vm, callee)
		st.consts = callee.Consts
		st.base = newBase
		if len(callee.ExceptionTable) == 0 && !callee.IsGenerator && !callee.IsCoroutine && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, callee, st.cache)
			st.codePtr = st.reloadWindow(vm, callee)
		}
		switch {
		case len(callee.CellVars) == 0 && fn.Closure != nil:
			st.cells = fn.Closure
			st.cellPtrs = fn.CellPtrs
		case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
			st.cells = nil
			st.cellPtrs = nil
		default:
			st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, callable, stack, newBase, totalArgs)
		}
		if fn.Globals != nil {
			st.globals = fn.Globals
		}
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		if vm.activeCellPtrs != nil || st.cellPtrs != nil {
			vm.activeCells = st.cells
			vm.activeCellPtrs = st.cellPtrs
		}
		vm.activeFrameIdx = st.frameIdx
		ip = 0
		return ip, sp, ctlReload

	case val.KindCode:
		callee := AsCodeObject(callable)
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, callable, nil, nil, stack, newBase, totalArgs, nil, st.globals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if callee.IsCoroutine {
			coroVal, ok := vm.instantiateCoroutineSafe(callee, callable, nil, nil, stack, newBase, totalArgs, nil, st.globals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = coroVal
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if st.frameIdx+2 > len(vm.frames) {
			vm.ensureFrameCapacity(st.frameIdx + 2)
		}
		saved := &vm.frames[st.frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		if isDirectAwait && callee.IsCoroutine {
			saved.ip = ip + 3
		} else {
			saved.ip = ip + 1
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
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		st.curLocals = val.NoneVal()
		st.frameIdx++
		st.curCO = callee
		st.cache = vm.cacheFor(st.curCO)
		st.codePtr = st.reloadWindow(vm, callee)
		st.consts = callee.Consts
		st.base = newBase
		minCap := max(st.base+256, st.base+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
			maybeOptimizeCodeObject(vm, callee, st.cache)
		}
		st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, callable, stack, newBase, totalArgs)
		numLocalsCallee := max(callee.NumLocals, totalArgs)
		sp = newBase + numLocalsCallee
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		if vm.activeCellPtrs != nil || st.cellPtrs != nil {
			vm.activeCells = st.cells
			vm.activeCellPtrs = st.cellPtrs
		}
		vm.activeFrameIdx = st.frameIdx
		ip = 0
		return ip, sp, ctlReload

	case val.KindBuiltinFunc:
		builtin := callable.AsBuiltinFunc()
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + totalArgs
		res := vm.invokeBuiltinSafe(builtin.Fn, callable, stack[newBase:newBase+totalArgs], nil)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++

	default:
		st.stack = stack
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = newBase + totalArgs
		res := vm.callFromHookSafe(callable, newBase, totalArgs, nil)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[retSP] = res
		sp = retSP + 1
		ip++
	}
	return ip, sp, ctlReload

}

//go:noinline
func (vm *VM) opCallMethodPICSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	methIdx := uint16(inst)
	newBase := sp - argc - 1
	receiver := stack[newBase]
	retSP := newBase
	totalArgs := argc + 1

	if argc == 1 && receiver.IsInstance() && methIdx < uint16(len(st.consts)) && st.consts[methIdx] == nameWrite {
		switch receiver.Type() {
		case StringIOType:
			sio := (*pyStringIO)(val.AsPointer(receiver))
			arg := stack[newBase+1]
			oldLen := len(sio.buf)
			if sio.pos == oldLen {
				var ok bool
				sio.buf, ok = val.AppendValueBytes(sio.buf, arg)
				if !ok {
					s := arg.AsString()
					view := unsafe.Slice(unsafe.StringData(s), len(s))
					sio.buf = append(sio.buf, view...)
				}
				written := len(sio.buf) - oldLen
				sio.pos = len(sio.buf)
				stack[retSP] = val.Int64Val(int64(written))
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		case FileType:
			pf := (*pyFile)(val.AsPointer(receiver))
			if !pf.isClosed && !pf.binary {
				arg := stack[newBase+1]
				oldLen := len(pf.writeBuf)
				var ok bool
				pf.writeBuf, ok = val.AppendValueBytes(pf.writeBuf, arg)
				if !ok {
					s := arg.AsString()
					view := unsafe.Slice(unsafe.StringData(s), len(s))
					pf.writeBuf = append(pf.writeBuf, view...)
				}
				written := len(pf.writeBuf) - oldLen
				if pf.unbuffered || len(pf.writeBuf) >= 32768 {
					if pf.f != nil {
						_, _ = pf.f.Write(pf.writeBuf)
						pf.writeBuf = pf.writeBuf[:0]
					}
				}
				stack[retSP] = val.Int64Val(int64(written))
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		}
	}

	switch {
	case argc == 0 && methIdx < uint16(len(st.consts)):
		mName := st.consts[methIdx]
		if mName == nameStringIO && (receiver.IsModule() || receiver.IsType()) {
			sio := &pyStringIO{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: StringIOType},
					Shape:  StringIOType.RootShape,
				},
				buf: make([]byte, 0, 131072),
			}
			stack[retSP] = val.ObjectVal(&sio.inst.Header)
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
		if receiver.IsInstance() {
			tp := receiver.Type()
			switch mName {
			case nameEnter:
				if tp == FileType || tp == StringIOType {
					stack[retSP] = receiver
					sp = retSP + 1
					ip++
					return ip, sp, ctlReload
				}
			case nameGetValue:
				if tp == StringIOType {
					sio := (*pyStringIO)(val.AsPointer(receiver))
					s := string(sio.buf)
					ps := &val.PyString{
						Header: val.ObjectHeader{
							Kind: val.KindString,
							Type: val.StrType,
						},
						Data: s,
					}
					stack[retSP] = val.ObjectVal(&ps.Header)
					sp = retSP + 1
					ip++
					return ip, sp, ctlReload
				}
			case nameRead:
				if tp == FileType {
					pf := (*pyFile)(val.AsPointer(receiver))
					if !pf.isClosed && pf.f != nil {
						buf := make([]byte, 65536)
						n, err := pf.f.Read(buf)
						if (err == nil || err == io.EOF) && n < len(buf) {
							if pf.binary {
								stack[retSP] = val.NewBytesVal(buf[:n])
							} else {
								s := unsafe.String(unsafe.SliceData(buf), n)
								ps := &val.PyString{
									Header: val.ObjectHeader{
										Kind: val.KindString,
										Type: val.StrType,
									},
									Data: s,
								}
								stack[retSP] = val.ObjectVal(&ps.Header)
							}
							sp = retSP + 1
							ip++
							return ip, sp, ctlReload
						}
						stat, err := pf.f.Stat()
						if err == nil && stat.Mode().IsRegular() {
							remSize := stat.Size()
							if remSize == 0 {
								if pf.binary {
									stack[retSP] = val.NewBytesVal(nil)
								} else {
									stack[retSP] = val.NewStringVal("")
								}
								sp = retSP + 1
								ip++
								return ip, sp, ctlReload
							}
							buf2 := make([]byte, remSize)
							readN, _ := io.ReadFull(pf.f, buf2)
							if pf.binary {
								stack[retSP] = val.NewBytesVal(buf2[:readN])
							} else {
								s := unsafe.String(unsafe.SliceData(buf2), readN)
								ps := &val.PyString{
									Header: val.ObjectHeader{
										Kind: val.KindString,
										Type: val.StrType,
									},
									Data: s,
								}
								stack[retSP] = val.ObjectVal(&ps.Header)
							}
							sp = retSP + 1
							ip++
							return ip, sp, ctlReload
						}
					}
				}
			}
		}
	case argc == 3 && receiver.IsInstance() && methIdx < uint16(len(st.consts)) && st.consts[methIdx] == nameExit:
		tp := receiver.Type()
		if tp == FileType {
			pf := (*pyFile)(val.AsPointer(receiver))
			if !pf.isClosed && pf.f != nil {
				if len(pf.writeBuf) > 0 {
					_, _ = pf.f.Write(pf.writeBuf)
					pf.writeBuf = pf.writeBuf[:0]
				}
				if pf.pooledBuf != nil {
					fileBufferPool.Put(pf.pooledBuf)
					pf.pooledBuf = nil
					pf.writeBuf = nil
				}
				if pf.f != os.Stdin && pf.f != os.Stdout && pf.f != os.Stderr {
					_ = pf.f.Close()
					pf.isClosed = true
				}
			}
			stack[retSP] = val.NoneVal()
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
	case argc == 1 && methIdx < uint16(len(st.consts)):
		switch st.consts[methIdx] {
		case nameExists:
			pathVal := stack[newBase+1]
			if pathVal.IsAnyString() {
				_, err := os.Stat(pathVal.AsString())
				stack[retSP] = val.BoolVal(err == nil)
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		case nameRemove, nameUnlink:
			pathVal := stack[newBase+1]
			if pathVal.IsAnyString() {
				_ = os.Remove(pathVal.AsString())
				stack[retSP] = val.NoneVal()
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
		}
	}
	if vm.hasTrackedViews && totalArgs > 0 {
		markViewsEscaped(stack[newBase : newBase+totalArgs])
	}

	if !receiver.IsInstance() && !receiver.IsSuper() && !receiver.IsModule() && !receiver.IsType() {
		tp := receiver.Type()
		attrName := st.consts[methIdx]
		pic := &st.cache.methodPIC[ip]
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		found := false
		for k := range 4 {
			if pic[k].ReceiverType == tp && pic[k].NativeFn != nil {
				nativeFn = pic[k].NativeFn
				targetVal = pic[k].TargetVal
				found = true
				break
			}
		}
		if !found {
			descr, _, ok := tp.LookupMRO(attrName)
			if ok && descr.IsBuiltinFunc() {
				bf := descr.AsBuiltinFunc()
				nativeFn = bf.Fn
				targetVal = descr
			} else {
				st.stack = stack
				vm.syncActiveFrame(st, ip)
				vm.execCtx.SP = newBase + totalArgs
				res := vm.callMethodPICSlowSafe(receiver, st.consts[methIdx], ip, newBase, argc, st.frameIdx, st.cache.methodPIC)
				stack = st.syncStackFromExecCtx(vm, stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = res
				sp = retSP + 1
				ip++
				return ip, sp, ctlReload
			}
			inserted := false
			for k := range 4 {
				if pic[k].ReceiverType == nil || pic[k].ReceiverType == tp {
					pic[k] = val.MethodPICEntry{ReceiverType: tp, NativeFn: nativeFn, TargetVal: targetVal}
					inserted = true
					break
				}
			}
			if !inserted {
				pic[0] = pic[1]
				pic[1] = pic[2]
				pic[2] = pic[3]
				pic[3] = val.MethodPICEntry{ReceiverType: tp, NativeFn: nativeFn, TargetVal: targetVal}
			}
		}
		if nativeFn != nil {
			st.stack = stack
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = newBase + totalArgs
			res := vm.invokeBuiltinSafe(nativeFn, targetVal, stack[newBase:newBase+totalArgs], nil)
			stack = st.syncStackFromExecCtx(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = res
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
	}
	if receiver.IsInstance() {
		instObj := receiver.AsInstance()
		instShape := instObj.Shape
		instType := instObj.Header.Type
		pic := &st.cache.methodPIC[ip]
		var targetFn *val.PyFunction
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		switch {
		case pic[0].Shape == instShape && instType.VersionTag == pic[0].TypeVersion:
			targetFn = pic[0].TargetFn
			nativeFn = pic[0].NativeFn
			targetVal = pic[0].TargetVal
		case pic[1].Shape == instShape && instType.VersionTag == pic[1].TypeVersion:
			targetFn = pic[1].TargetFn
			nativeFn = pic[1].NativeFn
			targetVal = pic[1].TargetVal
		case pic[2].Shape == instShape && instType.VersionTag == pic[2].TypeVersion:
			targetFn = pic[2].TargetFn
			nativeFn = pic[2].NativeFn
			targetVal = pic[2].TargetVal
		case pic[3].Shape == instShape && instType.VersionTag == pic[3].TypeVersion:
			targetFn = pic[3].TargetFn
			nativeFn = pic[3].NativeFn
			targetVal = pic[3].TargetVal
		}

		if targetFn != nil {
			callee := AsCodeObject(targetFn.Code)
			if !callee.IsGenerator && !callee.IsCoroutine {
				minCap := max(newBase+256, newBase+callee.NumLocals+64)
				if minCap > len(stack) {
					stack = vm.growStack(st, stack, minCap)
				}
				var nextSP int
				if totalArgs == callee.NumParams && len(targetFn.Defaults) == 0 && targetFn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 {
					numLocals := max(callee.NumLocals, totalArgs)
					nextSP = newBase + numLocals
				} else {
					var ok bool
					nextSP, ok = vm.bindFunctionArgsSafe(callee, targetFn.Defaults, targetFn.KwDefaults, stack, newBase, totalArgs, nil)
					if !ok {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						st.raiseExc = exc
						return ip, sp, ctlRaise
					}
				}
				if st.frameIdx+2 > len(vm.frames) {
					vm.ensureFrameCapacity(st.frameIdx + 2)
				}
				saved := &vm.frames[st.frameIdx]
				saved.co = st.curCO
				saved.codePtr = st.codePtr
				saved.codeLen = st.codeLen
				saved.consts = st.consts
				if callee.IsCoroutine && ip+2 < st.codeLen &&
					DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_GET_AWAITABLE &&
					DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))) == OP_AWAIT {
					saved.ip = ip + 3
				} else {
					saved.ip = ip + 1
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
				saved.initInstance = val.Value{}
				saved.cmpActive = false
				st.curLocals = val.NoneVal()
				st.frameIdx++
				st.curCO = callee
				sp = nextSP
				st.cache = vm.cacheFor(callee)
				if len(callee.ExceptionTable) == 0 && !callee.IsGenerator && !callee.IsCoroutine && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
					maybeOptimizeCodeObject(vm, callee, st.cache)
				}
				if gen := atomic.LoadInt32(&callee.StreamGen); gen != st.cache.streamGen {
					st.cache.streamGen = gen
					st.cache.active = callee.Active()
				}
				st.codePtr = st.setWindow(unsafe.Pointer(unsafe.SliceData(st.cache.active)), len(st.cache.active))
				st.consts = callee.Consts
				st.base = newBase
				switch {
				case len(callee.CellVars) == 0 && targetFn.Closure != nil:
					st.cells = targetFn.Closure
					st.cellPtrs = targetFn.CellPtrs
				case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
					st.cells = nil
					st.cellPtrs = nil
				default:
					st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, targetVal, stack, newBase, totalArgs)
				}
				if targetFn.Globals != nil {
					st.globals = targetFn.Globals
				}
				vm.activeCO = st.curCO
				vm.activeBaseOffset = st.base
				if vm.activeCellPtrs != nil || st.cellPtrs != nil {
					vm.activeCells = st.cells
					vm.activeCellPtrs = st.cellPtrs
				}
				vm.activeFrameIdx = st.frameIdx
				ip = 0
				return ip, sp, ctlReload
			}
		} else if nativeFn != nil {
			st.stack = stack
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = newBase + totalArgs
			res := vm.invokeBuiltinSafe(nativeFn, targetVal, stack[newBase:newBase+totalArgs], nil)
			stack = st.syncStackFromVM(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = res
			sp = retSP + 1
			ip++
			return ip, sp, ctlReload
		}
	}

	st.stack = stack
	vm.syncActiveFrame(st, ip)
	vm.execCtx.SP = newBase + totalArgs
	res := vm.callMethodPICSlowSafe(receiver, st.consts[methIdx], ip, newBase, argc, st.frameIdx, st.cache.methodPIC)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[retSP] = res
	sp = retSP + 1
	ip++
	return ip, sp, ctlReload

}
