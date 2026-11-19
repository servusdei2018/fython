package vm

import (
	"io"
	"os"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) checkPeriodicSafepoint(frameIdx int) {
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
	if vm.limits.MaxStackDepth > 0 && frameIdx >= vm.limits.MaxStackDepth {
		vm.unhandledException = val.NewRuntimeError("maximum call stack depth exceeded")
		panic(ErrMaxStackDepthExceeded)
	}
}

func (vm *VM) pushCalleeFrame(
	st *loopState,
	callee *CodeObject,
	target val.Value,
	fn *val.PyFunction,
	stack []val.Value,
	newBase, retSP, totalArgs, nextSP, retIP int,
	initInst val.Value,
	fnGlobals *val.PyDict,
	cc *VMCodeCache,
) (int, int, loopCtl) {
	if st.frameIdx+2 > len(vm.frames) {
		vm.ensureFrameCapacity(st.frameIdx + 2)
	}
	saved := &vm.frames[st.frameIdx]
	saved.co = st.curCO
	saved.codePtr = st.codePtr
	saved.codeLen = st.codeLen
	saved.consts = st.consts
	saved.ip = retIP
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
	saved.genForIter = false
	saved.genYieldFrom = false
	saved.initInstance = initInst
	saved.cmpActive = false

	st.curLocals = val.NoneVal()
	st.frameIdx++
	if st.frameIdx > vm.maxFrameIdx {
		vm.maxFrameIdx = st.frameIdx
	}
	st.curCO = callee
	if cc != nil {
		st.cache = cc
	} else {
		st.cache = vm.cacheFor(callee)
	}
	st.codePtr = st.reloadWindow(vm, callee)
	if len(callee.ExceptionTable) == 0 && !callee.IsGenerator && !callee.IsCoroutine && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, callee, st.cache)
		st.codePtr = st.reloadWindow(vm, callee)
	}
	st.consts = callee.Consts
	st.base = newBase
	switch {
	case len(callee.CellVars) == 0 && fn != nil && fn.Closure != nil:
		st.cells = fn.Closure
		st.cellPtrs = fn.CellPtrs
	case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
		st.cells = nil
		st.cellPtrs = nil
	default:
		st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, target, stack, newBase, totalArgs)
	}
	if fnGlobals != nil {
		st.globals = fnGlobals
	}
	vm.activeCO = st.curCO
	vm.activeBaseOffset = st.base
	if vm.activeCellPtrs != nil || st.cellPtrs != nil {
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
	vm.activeFrameIdx = st.frameIdx
	return 0, nextSP, ctlReload
}

func kwDefaultsVersion(d *val.PyDict) (bool, uint64) {
	if d == nil {
		return false, 0
	}
	return true, atomic.LoadUint64(&d.VersionTag)
}

func buildKwPICEntry(
	callee *CodeObject,
	fn *val.PyFunction,
	tp *val.PyType,
	cc *VMCodeCache,
	posCount int,
	totalArgs int,
	kwnames []val.Value,
) (KwPICEntry, bool) {
	kwCount := len(kwnames)
	if kwCount == 0 || kwCount > 8 || callee.IsGenerator || callee.IsCoroutine {
		return KwPICEntry{}, false
	}
	if !callee.ParamShapeComputed {
		callee.computeParamShape()
	}
	if callee.VarArgsIdx >= 0 || callee.VarKWIdx >= 0 || len(callee.ParamNames) > 64 {
		return KwPICEntry{}, false
	}
	paramNames := callee.ParamNames
	posParamCount := callee.PosParamCount
	posBound := min(posCount, posParamCount)
	var kwSlots [8]uint8
	var used [64]bool
	for i := 0; i < posBound && i < len(paramNames); i++ {
		used[i] = true
	}
	for i := range kwCount {
		kwName := kwnames[i]
		slot := -1
		for p := range paramNames {
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				slot = p
				break
			}
		}
		if slot < 0 || slot >= 256 || used[slot] {
			return KwPICEntry{}, false
		}
		used[slot] = true
		kwSlots[i] = uint8(slot)
	}

	var defaults []val.Value
	var kwDefaults *val.PyDict
	if fn != nil {
		defaults = fn.Defaults
		kwDefaults = fn.KwDefaults
	}
	defStart := posParamCount - len(defaults)
	var fills []KwDefaultFill
	for p := range paramNames {
		if used[p] {
			continue
		}
		if p >= defStart && p < posParamCount && len(defaults) > 0 {
			defIdx := p - defStart
			if defIdx >= 0 && defIdx < 128 {
				fills = append(fills, KwDefaultFill{Slot: uint8(p), DefIdx: int8(defIdx)})
				continue
			}
		}
		if kwDefaults != nil {
			if v, ok := kwDefaults.Get(paramNames[p]); ok {
				fills = append(fills, KwDefaultFill{Slot: uint8(p), DefIdx: -1, Val: v})
				continue
			}
		}
		return KwPICEntry{}, false
	}

	hasKwDef, kwDefVer := kwDefaultsVersion(kwDefaults)
	var typeVer uint64
	if tp != nil {
		typeVer = tp.Version()
	}
	numLocals := max(callee.NumLocals, callee.NumParams)
	return KwPICEntry{
		TargetCo:      callee,
		TargetFn:      fn,
		TargetType:    tp,
		TypeVersion:   typeVer,
		DefaultsLen:   len(defaults),
		HasKwDefaults: hasKwDef,
		KwDefaultsVer: kwDefVer,
		NumLocals:     numLocals,
		PosCount:      posCount,
		TotalArgs:     totalArgs,
		CalleeCache:   cc,
		KwSlots:       kwSlots,
		DefaultsFill:  fills,
	}, true
}

func insertKwPICEntry(entries *[2]KwPICEntry, e KwPICEntry) {
	if entries[0].TargetCo == nil || (entries[0].TargetCo == e.TargetCo && entries[0].TargetFn == e.TargetFn && entries[0].TargetType == e.TargetType) {
		entries[0] = e
		return
	}
	if entries[1].TargetCo == nil || (entries[1].TargetCo == e.TargetCo && entries[1].TargetFn == e.TargetFn && entries[1].TargetType == e.TargetType) {
		entries[1] = e
		return
	}
	entries[0] = entries[1]
	entries[1] = e
}

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

unwrapKw:
	for {
		switch target.Kind() {
		case val.KindStaticMethod:
			target = target.AsStaticMethod().Callable
		case val.KindBoundMethod:
			bm := target.AsBoundMethod()
			if newBase+1+totalArgs+64 > len(stack) {
				stack = vm.growStack(st, stack, newBase+1+totalArgs+256)
			}
			copy(stack[newBase+1:newBase+1+totalArgs], stack[newBase:newBase+totalArgs])
			stack[newBase] = bm.Self
			totalArgs++
			target = bm.Func
		case val.KindNative:
			if target.Type() == PartialType {
				part := (*pyPartial)(unsafe.Pointer(target.HeaderPtr()))
				if part.Keywords == nil {
					if shift := len(part.Args); shift > 0 {
						if newBase+shift+totalArgs+64 > len(stack) {
							stack = vm.growStack(st, stack, newBase+shift+totalArgs+256)
						}
						copy(stack[newBase+shift:newBase+shift+totalArgs], stack[newBase:newBase+totalArgs])
						copy(stack[newBase:newBase+shift], part.Args)
						totalArgs += shift
					}
					target = part.Target
					continue
				}
			}
			break unwrapKw
		default:
			break unwrapKw
		}
	}

	kwCount := len(kwnames)
	posCount := totalArgs - kwCount
	retIP := ip + 1

	switch target.Kind() {
	case val.KindFunction:
		fn := target.AsFunction()
		callee := AsCodeObject(fn.Code)
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = st.globals
		}
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
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
			return ip + 1, retSP + 1, ctlReload
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
			return ip + 1, retSP + 1, ctlReload
		}

		// Fast path: OP_CALL_KW PIC hit writes kwargs directly into callee slots with 0 allocations.
		if ip < len(st.cache.kwPIC) {
			pic := &st.cache.kwPIC[ip]
			for k := range 2 {
				e := &pic[k]
				if e.TargetCo == callee && e.TargetFn == fn && e.TargetType == nil &&
					e.PosCount == posCount && e.TotalArgs == totalArgs && e.DefaultsLen == len(fn.Defaults) &&
					e.CalleeCache != nil && e.CalleeCache.owner == vm &&
					((fn.KwDefaults == nil && !e.HasKwDefaults) || (fn.KwDefaults != nil && e.HasKwDefaults && atomic.LoadUint64(&fn.KwDefaults.VersionTag) == e.KwDefaultsVer)) {
					minCap := max(newBase+256, newBase+e.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					copy(vm.kwScratch[:kwCount], stack[newBase+posCount:newBase+totalArgs])
					for i := range kwCount {
						stack[newBase+int(e.KwSlots[i])] = vm.kwScratch[i]
					}
					for _, df := range e.DefaultsFill {
						if df.DefIdx >= 0 {
							stack[newBase+int(df.Slot)] = fn.Defaults[df.DefIdx]
						} else {
							stack[newBase+int(df.Slot)] = df.Val
						}
					}
					return vm.pushCalleeFrame(st, callee, target, fn, stack, newBase, retSP, totalArgs, newBase+e.NumLocals, retIP, val.Value{}, fnGlobals, e.CalleeCache)
				}
			}
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
		cc := vm.cacheFor(callee)
		if kwCount > 0 && kwCount <= 8 {
			if entry, okPIC := buildKwPICEntry(callee, fn, nil, cc, posCount, totalArgs, kwnames); okPIC {
				kwSlab := st.cache.ensureKwPIC(st.codeLen)
				insertKwPICEntry(&kwSlab[ip], entry)
			}
		}
		return vm.pushCalleeFrame(st, callee, target, fn, stack, newBase, retSP, totalArgs, nextSP, retIP, val.Value{}, fnGlobals, cc)

	case val.KindCode:
		callee := AsCodeObject(target)
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, nil, nil, stack, newBase, totalArgs, kwnames, st.globals)
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
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
			return ip + 1, retSP + 1, ctlReload
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
		return vm.pushCalleeFrame(st, callee, target, nil, stack, newBase, retSP, totalArgs, nextSP, retIP, val.Value{}, nil, nil)

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
		return ip + 1, retSP + 1, ctlReload

	case val.KindType:
		tp := target.AsType()
		meta := tp.Header.Type
		if tp.UsesDefaultNew && (tp.Flags&val.FlagHeapType) != 0 && (meta == nil || meta == val.TypeType) && !val.IsSubclass(tp, val.TypeType) {
			if !tp.HasInit() && len(kwnames) == 0 {
				stack[retSP] = val.NewInstanceOfType(tp)
				return ip + 1, retSP + 1, ctlReload
			}
			if tp.HasInit() {
				initFn := tp.DunderInitVal()
				if initFn.IsFunction() {
					initFunc := initFn.AsFunction()
					callee := AsCodeObject(initFunc.Code)
					if !callee.IsGenerator && !callee.IsCoroutine {
						totalInitArgs := totalArgs + 1
						initPosCount := posCount + 1
						// Fast path: OP_CALL_KW PIC hit for class instantiation + __init__(self, ...).
						if ip < len(st.cache.kwPIC) {
							pic := &st.cache.kwPIC[ip]
							for k := range 2 {
								e := &pic[k]
								if e.TargetType == tp && e.TypeVersion == tp.Version() &&
									e.TargetCo == callee && e.TargetFn == initFunc &&
									e.PosCount == initPosCount && e.TotalArgs == totalInitArgs && e.DefaultsLen == len(initFunc.Defaults) &&
									e.CalleeCache != nil && e.CalleeCache.owner == vm &&
									((initFunc.KwDefaults == nil && !e.HasKwDefaults) || (initFunc.KwDefaults != nil && e.HasKwDefaults && atomic.LoadUint64(&initFunc.KwDefaults.VersionTag) == e.KwDefaultsVer)) {
									minCap := max(newBase+1+totalArgs+256, newBase+e.NumLocals+64)
									if minCap > len(stack) {
										stack = vm.growStack(st, stack, minCap)
									}
									copy(vm.kwScratch[:kwCount], stack[newBase+posCount:newBase+totalArgs])
									if posCount > 0 {
										copy(stack[newBase+1:newBase+1+posCount], stack[newBase:newBase+posCount])
									}
									instObj := val.NewInstanceOfType(tp)
									stack[newBase] = instObj
									for i := range kwCount {
										stack[newBase+int(e.KwSlots[i])] = vm.kwScratch[i]
									}
									for _, df := range e.DefaultsFill {
										if df.DefIdx >= 0 {
											stack[newBase+int(df.Slot)] = initFunc.Defaults[df.DefIdx]
										} else {
											stack[newBase+int(df.Slot)] = df.Val
										}
									}
									return vm.pushCalleeFrame(st, callee, initFn, initFunc, stack, newBase, retSP, totalInitArgs, newBase+e.NumLocals, ip+1, instObj, initFunc.Globals, e.CalleeCache)
								}
							}
						}

						minCap := max(newBase+1+totalArgs+256, newBase+1+callee.NumLocals+64)
						if minCap > len(stack) {
							stack = vm.growStack(st, stack, minCap)
						}
						copy(stack[newBase+1:newBase+1+totalArgs], stack[newBase:newBase+totalArgs])
						instObj := val.NewInstanceOfType(tp)
						stack[newBase] = instObj
						var nextSP int
						if len(kwnames) == 0 && totalInitArgs == callee.NumParams && len(initFunc.Defaults) == 0 && initFunc.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && len(callee.CellVars) == 0 {
							nextSP = newBase + max(callee.NumLocals, totalInitArgs)
						} else {
							var ok bool
							nextSP, ok = vm.bindFunctionArgsSafe(callee, initFunc.Defaults, initFunc.KwDefaults, stack, newBase, totalInitArgs, kwnames)
							if !ok {
								exc := vm.execCtx.ActiveException
								vm.execCtx.ClearException()
								st.raiseExc = exc
								return ip, sp, ctlRaise
							}
						}
						cc := vm.cacheFor(callee)
						if kwCount > 0 && kwCount <= 8 {
							if entry, okPIC := buildKwPICEntry(callee, initFunc, tp, cc, initPosCount, totalInitArgs, kwnames); okPIC {
								kwSlab := st.cache.ensureKwPIC(st.codeLen)
								insertKwPICEntry(&kwSlab[ip], entry)
							}
						}
						return vm.pushCalleeFrame(st, callee, initFn, initFunc, stack, newBase, retSP, totalInitArgs, nextSP, ip+1, instObj, initFunc.Globals, cc)
					}
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
		return ip + 1, retSP + 1, ctlReload
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
		return ip + 1, retSP + 1, ctlReload
	}
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

	// Fast in-loop frame push for Python functions and bound methods when
	// positional args are a tuple/list and kwargs are absent or a small local dict.
	target := callable
	hasSelf := false
	var selfVal val.Value
	if target.IsBoundMethod() {
		bm := target.AsBoundMethod()
		if bm.Func.IsFunction() {
			hasSelf = true
			selfVal = bm.Self
			target = bm.Func
		}
	}
	if target.IsFunction() {
		fn := target.AsFunction()
		callee := AsCodeObject(fn.Code)
		if !callee.IsGenerator && !callee.IsCoroutine {
			var posArgs []val.Value
			argsOK := false
			switch {
			case argsVal.IsTuple():
				posArgs = argsVal.AsTuple().Items
				argsOK = true
			case argsVal.IsList():
				posArgs = argsVal.AsList().Items
				argsOK = true
			}
			var kwDict *val.PyDict
			kwOK := false
			kwLen := 0
			if !hasKW || kwDictVal.IsNone() || kwDictVal.HeaderPtr() == nil {
				kwOK = true
			} else {
				kd := kwDictVal
				if kd.IsMappingProxy() {
					kd = kd.AsMappingProxy().Mapping
				}
				if kd.IsDict() {
					d := kd.AsDict()
					if d.Live == nil && !d.Shared && !val.MultiThreadedFlag.Load() {
						kwLen = d.Len()
						if kwLen <= len(vm.kwNamesScratch) {
							kwDict = d
							kwOK = true
						}
					}
				}
			}
			if argsOK && kwOK {
				newBase := sp
				selfCount := 0
				if hasSelf {
					selfCount = 1
				}
				totalArgs := selfCount + len(posArgs) + kwLen
				minCap := max(newBase+totalArgs+256, newBase+callee.NumLocals+64)
				if minCap > len(stack) {
					stack = vm.growStack(st, stack, minCap)
				}
				dst := newBase
				if hasSelf {
					stack[dst] = selfVal
					dst++
				}
				if len(posArgs) > 0 {
					copy(stack[dst:dst+len(posArgs)], posArgs)
					dst += len(posArgs)
				}
				var kwnames []val.Value
				if kwLen > 0 {
					kwCount := 0
					for i := range kwDict.Entries {
						e := &kwDict.Entries[i]
						if !e.Active {
							continue
						}
						if !e.Key.IsAnyString() {
							clear(vm.kwNamesScratch[:kwCount])
							st.raiseExc = val.NewTypeError("keywords must be strings")
							return ip, sp, ctlRaise
						}
						vm.kwNamesScratch[kwCount] = val.InternValue(e.Key)
						stack[dst] = e.Value
						kwCount++
						dst++
					}
					kwnames = vm.kwNamesScratch[:kwCount]
					totalArgs = dst - newBase
				}
				nextSP, ok := vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames)
				if len(kwnames) > 0 {
					clear(vm.kwNamesScratch[:len(kwnames)])
				}
				if !ok {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				fnGlobals := fn.Globals
				if fnGlobals == nil {
					fnGlobals = st.globals
				}
				cc := vm.cacheFor(callee)
				return vm.pushCalleeFrame(st, callee, target, fn, stack, newBase, sp, totalArgs, nextSP, ip+1, val.Value{}, fnGlobals, cc)
			}
		}
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
func (vm *VM) opCallLenSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	target := stack[sp-2]
	if target != vm.builtinLen {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	arg := stack[sp-1]
	if arg.IsByteArray() {
		stack[sp-2] = val.Int64Val(int64(len(arg.AsByteArray().Data)))
		return ip + 1, sp - 1, ctlNext
	}
	if arg.IsMemoryView() {
		mv := arg.AsMemoryView()
		var n int
		switch {
		case mv.NDim > 0:
			n = mv.Dim(0)
		case mv.ItemSize > 1:
			n = len(mv.Buf) / mv.ItemSize
		default:
			n = len(mv.Buf)
		}
		stack[sp-2] = val.Int64Val(int64(n))
		return ip + 1, sp - 1, ctlNext
	}
	st.stack = stack
	vm.syncActiveFrame(st, ip)
	vm.execCtx.SP = sp
	if sqLen := arg.Type().Slots.SqLen; sqLen != nil {
		n := sqLen(&vm.execCtx, arg)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[sp-2] = val.Int64Val(n)
		return ip + 1, sp - 1, ctlReload
	}
	res := vm.invokeBuiltinSafe(target.AsBuiltinFunc().Fn, target, stack[sp-1:sp], nil)
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-2] = res
	return ip + 1, sp - 1, ctlReload
}

//go:noinline
func (vm *VM) opCallIsInstanceSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	target := stack[sp-3]
	if target != vm.builtinIsInstance {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	obj := stack[sp-2]
	cls := stack[sp-1]
	if vm.hasTrackedViews {
		markViewEscaped(obj)
		markViewEscaped(cls)
	}
	if cls.IsType() {
		tp := cls.AsType()
		meta := tp.Header.Type
		if meta == nil || meta == val.TypeType {
			res := val.IsSubclass(obj.Type(), tp)
			if !res && obj.IsInstance() {
				if spoofedCls, ok := obj.AsInstance().GetAttr(nameClass); ok && spoofedCls.IsType() {
					res = val.IsSubclass(spoofedCls.AsType(), tp)
				}
			}
			stack[sp-3] = val.BoolVal(res)
			return ip + 1, sp - 2, ctlNext
		}
	}
	st.stack = stack
	vm.syncActiveFrame(st, ip)
	vm.execCtx.SP = sp
	res := checkIsInstance(&vm.execCtx, obj, cls)
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-3] = val.BoolVal(res)
	return ip + 1, sp - 2, ctlReload
}

//go:noinline
func (vm *VM) opCallBuiltinFast(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc
	retSP := newBase - 1
	target := stack[retSP]
	entry := &st.cache.ic[ip]
	if target != entry.CachedVal {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	if vm.hasTrackedViews && argc > 0 {
		markViewsEscaped(stack[newBase : newBase+argc])
	}
	st.stack = stack
	vm.syncActiveFrame(st, ip)
	vm.execCtx.SP = newBase + argc
	var res val.Value
	if entry.NativeFn != nil {
		res = entry.NativeFn(&vm.execCtx, target, stack[newBase:newBase+argc], nil)
	} else {
		res = vm.callFromHookSafe(target, newBase, argc, nil)
	}
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[retSP] = res
	return ip + 1, retSP + 1, ctlReload
}

//go:noinline
func (vm *VM) opCallMethodDescr(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc - 1
	retSP := newBase
	receiver := stack[newBase]
	totalArgs := argc + 1
	if !receiver.IsInstance() && !receiver.IsSuper() && !receiver.IsModule() && !receiver.IsType() && uint(ip) < uint(len(st.cache.methodPIC)) {
		tp := receiver.Type()
		pic := &st.cache.methodPIC[ip]
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		for k := range 4 {
			if pic[k].ReceiverType == tp && pic[k].NativeFn != nil {
				nativeFn = pic[k].NativeFn
				targetVal = pic[k].TargetVal
				break
			}
		}
		if nativeFn != nil {
			if vm.hasTrackedViews && totalArgs > 0 {
				markViewsEscaped(stack[newBase : newBase+totalArgs])
			}
			st.stack = stack
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = newBase + totalArgs
			res := nativeFn(&vm.execCtx, targetVal, stack[newBase:newBase+totalArgs], nil)
			stack = st.syncStackFromExecCtx(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = res
			return ip + 1, retSP + 1, ctlReload
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_PIC))
	return vm.opCallMethodPICSlow(st, ReplaceOpcode(inst, OP_CALL_METHOD_PIC), ip, sp)
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
	canQuicken := uint16(inst) == CallFromStack

	if bx < len(st.consts) && isCallableConst(st.consts[bx]) {
		target = st.consts[bx]
		newBase = sp - argc
		retSP = newBase
	} else {
		target = stack[sp-argc-1]
		newBase = sp - argc
		retSP = sp - argc - 1
	}

	if vm.hasTrackedViews && argc > 0 {
		markViewsEscaped(stack[newBase : newBase+argc])
	}

	// Unwrap bound methods, staticmethods, and keyword-free partials in-loop so
	// Python targets push directly onto vm.frames without recursive callFromHook.
	unwrapped := false
unwrapCall:
	for {
		switch target.Kind() {
		case val.KindStaticMethod:
			target = target.AsStaticMethod().Callable
			unwrapped = true
		case val.KindBoundMethod:
			bm := target.AsBoundMethod()
			if newBase+1+argc+64 > len(stack) {
				stack = vm.growStack(st, stack, newBase+1+argc+256)
			}
			copy(stack[newBase+1:newBase+1+argc], stack[newBase:newBase+argc])
			stack[newBase] = bm.Self
			argc++
			target = bm.Func
			unwrapped = true
		case val.KindNative:
			if target.Type() == PartialType {
				part := (*pyPartial)(unsafe.Pointer(target.HeaderPtr()))
				if part.Keywords == nil {
					if shift := len(part.Args); shift > 0 {
						if newBase+shift+argc+64 > len(stack) {
							stack = vm.growStack(st, stack, newBase+shift+argc+256)
						}
						copy(stack[newBase+shift:newBase+shift+argc], stack[newBase:newBase+argc])
						copy(stack[newBase:newBase+shift], part.Args)
						argc += shift
					}
					target = part.Target
					unwrapped = true
					continue
				}
			}
			break unwrapCall
		default:
			break unwrapCall
		}
	}
	canQuicken = canQuicken && !unwrapped

	// Builtin range fast-path: 0 allocations
	if target == vm.builtinRange {
		if canQuicken {
			st.cache.ic[ip] = ICSlot{CachedVal: target}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		if argc == 1 && stack[newBase].IsInt64() {
			stop := stack[newBase].AsInt64()
			if stop >= 0 && stop <= 0x7FFFFFFF {
				stack[retSP] = val.RangeVal(0, int32(stop))
				return ip + 1, retSP + 1, ctlReload
			}
		}
	}

	// Builtin len fast-path: quicken to OP_CALL_LEN and execute inline
	if target == vm.builtinLen && argc == 1 {
		if canQuicken {
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_LEN))
		}
		arg := stack[newBase]
		switch arg.Kind() {
		case val.KindString, val.KindInlineStr:
			stack[retSP] = val.Int64Val(int64(arg.StringLen()))
			return ip + 1, retSP + 1, ctlReload
		case val.KindList:
			stack[retSP] = val.Int64Val(int64(len(arg.AsList().Items)))
			return ip + 1, retSP + 1, ctlReload
		case val.KindTuple:
			stack[retSP] = val.Int64Val(int64(len(arg.AsTuple().Items)))
			return ip + 1, retSP + 1, ctlReload
		case val.KindDict:
			stack[retSP] = val.Int64Val(int64(arg.AsDict().Len()))
			return ip + 1, retSP + 1, ctlReload
		case val.KindSet:
			stack[retSP] = val.Int64Val(int64(arg.AsSet().Len()))
			return ip + 1, retSP + 1, ctlReload
		case val.KindBytes:
			stack[retSP] = val.Int64Val(int64(len(arg.AsBytes().Data)))
			return ip + 1, retSP + 1, ctlReload
		case val.KindByteArray:
			stack[retSP] = val.Int64Val(int64(len(arg.AsByteArray().Data)))
			return ip + 1, retSP + 1, ctlReload
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
			return ip + 1, retSP + 1, ctlReload
		}
	}

	// Builtin isinstance fast-path: quicken to OP_CALL_ISINSTANCE
	if target == vm.builtinIsInstance && argc == 2 && canQuicken {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_ISINSTANCE))
		obj := stack[newBase]
		cls := stack[newBase+1]
		if cls.IsType() {
			tp := cls.AsType()
			meta := tp.Header.Type
			if meta == nil || meta == val.TypeType {
				res := val.IsSubclass(obj.Type(), tp)
				if !res && obj.IsInstance() {
					if spoofedCls, ok := obj.AsInstance().GetAttr(nameClass); ok && spoofedCls.IsType() {
						res = val.IsSubclass(spoofedCls.AsType(), tp)
					}
				}
				stack[retSP] = val.BoolVal(res)
				return ip + 1, retSP + 1, ctlReload
			}
		}
	}

	// Builtin str fast-path: 0 allocations for int64 and strings
	if target.IsType() && target.AsType() == val.StrType && argc == 1 {
		arg := stack[newBase]
		if arg.IsInt64() {
			stack[retSP] = val.IntToStringVal(arg.AsInt64())
			return ip + 1, retSP + 1, ctlReload
		}
		if arg.IsAnyString() {
			stack[retSP] = arg
			return ip + 1, retSP + 1, ctlReload
		}
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
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine {
			if !isDirectAwait || callee.CanSuspend {
				coroVal, ok := vm.instantiateCoroutineSafe(callee, target, nil, nil, stack, newBase, argc, nil, st.globals)
				if !ok {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = coroVal
				return ip + 1, retSP + 1, ctlReload
			}
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		nextSP := newBase + max(callee.NumLocals, argc)
		return vm.pushCalleeFrame(st, callee, target, nil, stack, newBase, retSP, argc, nextSP, retIP, val.Value{}, nil, nil)

	case val.KindFunction:
		fn := target.AsFunction()
		callee := AsCodeObject(fn.Code)
		st.periodicTick++
		if (st.periodicTick & 0xFF) == 0 {
			vm.checkPeriodicSafepoint(st.frameIdx)
		}
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = st.globals
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
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine {
			if !isDirectAwait || callee.CanSuspend {
				coroVal, ok := vm.instantiateCoroutineSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil, fnGlobals)
				if !ok {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = coroVal
				return ip + 1, retSP + 1, ctlReload
			}
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		var nextSP int
		exactArgs := callee.SimplePositionalOnly && argc == callee.PositionalParamCount && len(fn.Defaults) == 0 && fn.KwDefaults == nil
		if exactArgs || (argc == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0) {
			nextSP = newBase + max(callee.NumLocals, argc)
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
		cc := vm.cacheFor(callee)
		if canQuicken && !vm.hasTrackedViews && !callee.IsGenerator && !callee.IsCoroutine &&
			exactArgs && len(callee.CellVars) == 0 && (len(callee.FreeVars) == 0 || fn.Closure != nil) {
			if len(callee.ExceptionTable) == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
				maybeOptimizeCodeObject(vm, callee, cc)
			}
			_, _ = vm.loadWindow(callee, cc)
			st.cache.ic[ip] = ICSlot{CachedVal: target, CalleeCache: cc}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_PY_EXACT_ARGS))
		}
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		return vm.pushCalleeFrame(st, callee, target, fn, stack, newBase, retSP, argc, nextSP, retIP, val.Value{}, fnGlobals, cc)

	case val.KindBuiltinFunc:
		builtin := target.AsBuiltinFunc()
		if canQuicken {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: builtin.Fn}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
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
		return ip + 1, retSP + 1, ctlReload

	case val.KindType:
		tp := target.AsType()
		meta := tp.Header.Type
		if tp.UsesDefaultNew && (tp.Flags&val.FlagHeapType) != 0 && (meta == nil || meta == val.TypeType) && !val.IsSubclass(tp, val.TypeType) {
			if !tp.HasInit() {
				stack[retSP] = val.NewInstanceOfType(tp)
				return ip + 1, retSP + 1, ctlReload
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
					instObj := val.NewInstanceOfType(tp)
					stack[newBase] = instObj
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
					return vm.pushCalleeFrame(st, callee, initFn, initFunc, stack, newBase, retSP, totalInitArgs, nextSP, ip+1, instObj, initFunc.Globals, nil)
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
		return ip + 1, retSP + 1, ctlReload
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
		return ip + 1, retSP + 1, ctlReload
	}
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
			saved.stack = st.stack
			saved.activeGen = st.activeGen
			saved.genFrameIdx = st.genFrameIdx
			saved.genForIter = false
			saved.genYieldFrom = false
			saved.initInstance = val.Value{}
			saved.cmpActive = false
			st.curLocals = val.NoneVal()
			st.frameIdx++
			if st.frameIdx > vm.maxFrameIdx {
				vm.maxFrameIdx = st.frameIdx
			}
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
			return ip + 1, retSP + 1, ctlReload
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
			return ip + 1, retSP + 1, ctlReload
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
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		return vm.pushCalleeFrame(st, callee, callable, fn, stack, newBase, retSP, totalArgs, nextSP, retIP, val.Value{}, fnGlobals, nil)

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
			return ip + 1, retSP + 1, ctlReload
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
			return ip + 1, retSP + 1, ctlReload
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		nextSP := newBase + max(callee.NumLocals, totalArgs)
		return vm.pushCalleeFrame(st, callee, callable, nil, stack, newBase, retSP, totalArgs, nextSP, retIP, val.Value{}, nil, nil)

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
	methodPIC := st.cache.ensureMethodPIC()

	if !receiver.IsInstance() && !receiver.IsSuper() && !receiver.IsModule() && !receiver.IsType() {
		tp := receiver.Type()
		attrName := st.consts[methIdx]
		pic := &methodPIC[ip]
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
				res := vm.callMethodPICSlowSafe(receiver, st.consts[methIdx], ip, newBase, argc, st.frameIdx, methodPIC)
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
			if methIdx >= uint16(len(st.consts)) || (attrName != nameExists && attrName != nameRemove && attrName != nameUnlink) {
				st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR))
			}
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
			return ip + 1, retSP + 1, ctlReload
		}
	}
	if receiver.IsInstance() {
		instObj := receiver.AsInstance()
		instShape := instObj.Shape
		instType := instObj.Header.Type
		pic := &methodPIC[ip]
		var targetFn *val.PyFunction
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		switch {
		case pic[0].Shape == instShape && instType.Version() == pic[0].TypeVersion:
			targetFn = pic[0].TargetFn
			nativeFn = pic[0].NativeFn
			targetVal = pic[0].TargetVal
		case pic[1].Shape == instShape && instType.Version() == pic[1].TypeVersion:
			targetFn = pic[1].TargetFn
			nativeFn = pic[1].NativeFn
			targetVal = pic[1].TargetVal
		case pic[2].Shape == instShape && instType.Version() == pic[2].TypeVersion:
			targetFn = pic[2].TargetFn
			nativeFn = pic[2].NativeFn
			targetVal = pic[2].TargetVal
		case pic[3].Shape == instShape && instType.Version() == pic[3].TypeVersion:
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
				return vm.pushCalleeFrame(st, callee, targetVal, targetFn, stack, newBase, retSP, totalArgs, nextSP, ip+1, val.Value{}, targetFn.Globals, nil)
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
	res := vm.callMethodPICSlowSafe(receiver, st.consts[methIdx], ip, newBase, argc, st.frameIdx, methodPIC)
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
