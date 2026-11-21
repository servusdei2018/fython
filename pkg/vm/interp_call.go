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

//go:noinline
func (vm *VM) syncCalleeCode(st *loopState, callee *CodeObject, cc *VMCodeCache) {
	st.codePtr, st.codeLen = vm.loadWindow(callee, cc)
	if atomic.LoadInt32(&callee.IsTier2Optimized) == 0 && len(callee.ExceptionTable) == 0 && !callee.IsGenerator && !callee.IsCoroutine {
		maybeOptimizeCodeObject(vm, callee, cc)
		if atomic.LoadInt32(&callee.StreamGen) != cc.streamGen {
			st.codePtr, st.codeLen = vm.loadWindow(callee, cc)
		}
	}
}

//go:noinline
func (vm *VM) setCalleeCells(st *loopState, callee *CodeObject, fn *val.PyFunction, newBase int) {
	if !callee.HasCellVars && fn != nil && fn.Closure != nil {
		st.cells = fn.Closure
		st.cellPtrs = fn.CellPtrs
	} else {
		st.cells, st.cellPtrs = vm.setupCalleeCellsForFn(callee, fn, st.stack, newBase)
	}
	vm.activeCells = st.cells
	vm.activeCellPtrs = st.cellPtrs
}

func (vm *VM) pushCalleeFrame(
	st *loopState,
	callee *CodeObject,
	fn *val.PyFunction,
	cc *VMCodeCache,
	newBase, retSP, nextSP, retIP int,
) (int, int, loopCtl) {
	// Enter register execution for hot callees through a nested frame,
	// keeping the stack loop itself untouched. This must precede any
	// caller-frame bookkeeping below so a nested run leaves the loop
	// state exactly as the call site left it.
	if nip, nsp, nctl, ok := vm.tryNestedRegCall(st, callee, fn, newBase, nextSP, retSP, retIP, -1); ok {
		return nip, nsp, nctl
	}
	frameIdx := st.frameIdx
	if frameIdx+2 > len(vm.frames) {
		vm.ensureFrameCapacity(frameIdx + 2)
	}
	saved := &vm.frames[frameIdx]
	if saved.co != st.curCO || saved.codePtr != st.codePtr {
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		saved.cache = st.cache
	}
	saved.ip = retIP
	saved.baseOffset = st.base
	saved.retSP = retSP
	saved.excStackLen = len(vm.excStack)
	saved.stackIsVM = st.stackIsVM
	if saved.globals != st.globals {
		saved.globals = st.globals
	}
	if saved.cells != nil || st.cells != nil {
		saved.cells = st.cells
	}
	if saved.cellPtrs != nil || st.cellPtrs != nil {
		saved.cellPtrs = st.cellPtrs
	}
	if saved.locals != st.curLocals {
		saved.locals = st.curLocals
	}
	if !st.curLocals.IsNone() {
		st.curLocals = val.NoneVal()
	}

	nextFrame := frameIdx + 1
	st.frameIdx = nextFrame
	if nextFrame > vm.maxFrameIdx {
		vm.maxFrameIdx = nextFrame
	}
	if st.stackIsVM {
		if hw := nextSP + 256; hw > vm.maxSP {
			vm.maxSP = hw
		}
	}
	st.curCO = callee
	if cc == nil {
		cc = vm.cacheFor(callee)
	}
	st.cache = cc
	if atomic.LoadInt32(&callee.StreamGen) == cc.streamGen && atomic.LoadInt32(&callee.IsTier2Optimized) != 0 {
		st.codePtr = cc.codePtr
		st.codeLen = cc.codeLen
	} else {
		vm.syncCalleeCode(st, callee, cc)
	}
	st.consts = callee.Consts
	st.base = newBase
	if !callee.HasCells {
		if st.cells != nil || st.cellPtrs != nil {
			st.cells = nil
			st.cellPtrs = nil
			vm.activeCells = nil
			vm.activeCellPtrs = nil
		}
	} else {
		vm.setCalleeCells(st, callee, fn, newBase)
	}
	if fn != nil && fn.Globals != nil && st.globals != fn.Globals {
		st.globals = fn.Globals
	}
	vm.activeCO = callee
	vm.activeBaseOffset = newBase
	vm.activeFrameIdx = nextFrame
	return callee.EntryIP, nextSP, ctlReload
}

func canFastInstantiate(tp *val.PyType) bool {
	if !tp.UsesDefaultNew || (tp.Flags&val.FlagHeapType) == 0 || val.IsSubclass(tp, val.TypeType) {
		return false
	}
	meta := tp.Header.Type
	return meta == nil || meta == val.TypeType || meta.UsesDefaultTypeCall()
}

func (vm *VM) fastCalleeCache(callee *CodeObject) *VMCodeCache {
	if pc := (*VMCodeCache)(atomic.LoadPointer(&callee.primary)); pc != nil && pc.owner == vm {
		return pc
	}
	return vm.cacheFor(callee)
}

// isEphemeralSuperCallSite reports whether the PySuper returned at ip is consumed
// directly by OP_LOAD_ATTR, OP_LOAD_METHOD, or OP_CALL_METHOD_PIC without escaping.
func isEphemeralSuperCallSite(st *loopState, ip int) bool {
	depth := 1
	limit := min(st.codeLen, ip+16)
	for k := ip + 1; k < limit; k++ {
		wk := *(*uint32)(unsafe.Add(st.codePtr, uintptr(k)*4))
		opk := DecodeOpcode(wk)
		if depth == 1 && (opk == OP_LOAD_METHOD || opk == OP_LOAD_ATTR) {
			return true
		}
		if (opk == OP_CALL_METHOD_PIC || opk == OP_CALL_METHOD_DESCR || opk == OP_CALL_METHOD_DESCR_FAST) && depth == int(DecodeArgA(wk))+1 {
			return true
		}
		switch opk {
		case OP_LOAD_FAST, OP_LOAD_CONST, OP_LOAD_GLOBAL, OP_LOAD_DEREF:
			depth++
		case OP_LOAD_ATTR, OP_UNARY_NEGATIVE, OP_UNARY_POSITIVE, OP_UNARY_NOT, OP_UNARY_INVERT:
			if depth <= 1 {
				return false
			}
		case OP_CALL:
			argc := int(DecodeArgA(wk))
			if DecodeArgBx(wk) == CallFromStack {
				depth -= argc
			} else {
				depth -= argc - 1
			}
			if depth <= 1 {
				return false
			}
		case OP_CALL_LEN:
			depth--
			if depth <= 1 {
				return false
			}
		case OP_CALL_ISINSTANCE:
			depth -= 2
			if depth <= 1 {
				return false
			}
		case OP_CALL_PY_EXACT_ARGS, OP_CALL_PY_DEFAULTS, OP_CALL_BUILTIN_FAST, OP_CALL_TYPE_INIT,
			OP_CALL_METHOD_PIC, OP_CALL_METHOD_DESCR, OP_CALL_METHOD_DESCR_FAST,
			OP_CALL_HASATTR, OP_CALL_GETATTR, OP_CALL_BOUND_NATIVE, OP_CALL_INSTANCE_DUNDER:
			depth -= int(DecodeArgA(wk))
			if depth <= 1 {
				return false
			}
		case OP_BINARY_ADD, OP_BINARY_SUB, OP_BINARY_MUL, OP_BINARY_TRUE_DIV, OP_BINARY_FLOOR_DIV, OP_BINARY_MOD,
			OP_BINARY_ADD_INT, OP_BINARY_ADD_FLOAT, OP_BINARY_SUB_INT, OP_BINARY_SUBSCR,
			OP_COMPARE_OP, OP_COMPARE_LT, OP_COMPARE_LT_INT, OP_COMPARE_DUNDER:
			depth--
			if depth <= 1 {
				return false
			}
		case OP_BUILD_TUPLE, OP_BUILD_LIST, OP_BUILD_SET:
			depth -= int(DecodeArgBx(wk)) - 1
			if depth <= 1 {
				return false
			}
		default:
			return false
		}
	}
	return false
}

func resolveSuperDirect(st *loopState, stack []val.Value, newBase, argc int) (*val.PyType, val.Value, bool) {
	if argc == 2 {
		if !stack[newBase].IsType() {
			return nil, val.Value{}, false
		}
		return stack[newBase].AsType(), stack[newBase+1], true
	}
	if argc != 0 || st.curCO == nil || st.base >= len(stack) {
		return nil, val.Value{}, false
	}
	self := stack[st.base]
	if self.IsNone() && self.HeaderPtr() == nil {
		return nil, val.Value{}, false
	}
	idx := int(st.curCO.ClassCellIdx)
	if idx < 0 || idx >= len(st.cells) {
		return nil, val.Value{}, false
	}
	var c *val.PyCell
	if idx < len(st.cellPtrs) && st.cellPtrs[idx] != nil {
		c = st.cellPtrs[idx]
	} else if st.cells[idx].IsCell() {
		c = st.cells[idx].AsCell()
	}
	if c == nil || c.Value.IsUnbound() || !c.Value.IsType() {
		return nil, val.Value{}, false
	}
	return c.Value.AsType(), self, true
}

//go:noinline
func (vm *VM) dispatchSuperCall(st *loopState, inst uint32, ip, sp, newBase, retSP, argc int, ephemeral, canQuicken bool) (int, int, loopCtl) {
	stack := st.stack
	if canQuicken {
		if isEphemeralSuperCallSite(st, ip) {
			ephemeral = true
		}
		var eFlag int32
		if ephemeral {
			eFlag = 1
		}
		st.cache.ic[ip] = ICSlot{
			CachedVal: vm.builtinSuper,
			NativeFn:  vm.builtinSuper.AsBuiltinFunc().Fn,
			SlotIndex: eFlag,
		}
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
	}
	thisClass, self, ok := resolveSuperDirect(st, stack, newBase, argc)
	if !ok {
		st.stack = stack
		st.ip = ip
		vm.execCtx.SP = newBase + argc
		res := vm.invokeBuiltinSafe(vm.builtinSuper.AsBuiltinFunc().Fn, vm.builtinSuper, stack[newBase:newBase+argc], nil)
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
	if ip+1 < st.codeLen {
		w1 := *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))
		op1 := DecodeOpcode(w1)
		methIdx := uint16(w1)
		if (op1 == OP_LOAD_METHOD || op1 == OP_LOAD_ATTR) && int(methIdx) < len(st.consts) {
			var selfType *val.PyType
			var isClassSuper bool
			if self.IsType() && val.IsSubclass(self.AsType(), thisClass) {
				selfType = self.AsType()
				isClassSuper = true
			} else {
				selfType = self.Type()
			}
			typeVer := selfType.Version()
			thisVer := thisClass.Version()
			entry := &st.cache.ic[ip+1]
			hit := entry.Kind == ICKindSuperMethod && entry.ExpectedTypeVer == typeVer && entry.ExpectedAuxVer == thisVer
			if !hit {
				attrName := val.InternValue(st.consts[methIdx])
				if descr, _, foundMRO := selfType.LookupMROAfter(thisClass, attrName); foundMRO {
					switch {
					case descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind):
						bind := int32(1)
						if isClassSuper {
							bind = 0
						}
						*entry = ICSlot{
							Kind:            ICKindSuperMethod,
							ExpectedTypeVer: typeVer,
							ExpectedAuxVer:  thisVer,
							SlotIndex:       bind,
							CachedVal:       descr,
						}
						hit = true
					case descr.Kind() == val.KindStaticMethod:
						*entry = ICSlot{
							Kind:            ICKindSuperMethod,
							ExpectedTypeVer: typeVer,
							ExpectedAuxVer:  thisVer,
							SlotIndex:       0,
							CachedVal:       descr.AsStaticMethod().Callable,
						}
						hit = true
					case descr.Type().Slots.TpDescrGet == nil || (descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind):
						*entry = ICSlot{
							Kind:            ICKindSuperMethod,
							ExpectedTypeVer: typeVer,
							ExpectedAuxVer:  thisVer,
							SlotIndex:       0,
							CachedVal:       descr,
						}
						hit = true
					}
				}
			}
			if hit {
				if op1 == OP_LOAD_METHOD {
					if retSP+2 > len(stack) {
						stack = vm.growStack(st, stack, retSP+64)
					}
					if entry.SlotIndex == 1 {
						stack[retSP] = self
					} else {
						stack[retSP] = val.UnboundVal()
					}
					stack[retSP+1] = entry.CachedVal
					return ip + 2, retSP + 2, ctlReload
				}
				if entry.SlotIndex == 1 {
					stack[retSP] = val.NewBoundMethodVal(self, entry.CachedVal)
				} else {
					stack[retSP] = entry.CachedVal
				}
				return ip + 2, retSP + 1, ctlReload
			}
		}
	}
	stack[retSP] = vm.acquireSuper(thisClass, self, ephemeral)
	return ip + 1, retSP + 1, ctlReload
}

// isEphemeralGenCallSite reports whether the generator returned by a call at ip
// is consumed immediately by a for-loop, yield-from, or str.join without ever escaping.
func isEphemeralGenCallSite(st *loopState, ip, retSP int) bool {
	if ip+1 >= st.codeLen {
		return false
	}
	w1 := *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))
	op1 := DecodeOpcode(w1)
	if op1 == OP_YIELD_FROM {
		return true
	}
	if op1 == OP_GET_ITER && ip+2 < st.codeLen {
		op2 := DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4)))
		return op2 == OP_FOR_ITER || op2 == OP_FOR_ITER_GEN || op2 == OP_YIELD_FROM
	}
	if DecodeArgA(w1) == 1 && retSP > st.base && ip+1 < len(st.cache.ic) {
		entry := &st.cache.ic[ip+1]
		switch op1 {
		case OP_CALL_BUILTIN_FAST:
			cv := entry.CachedVal
			if (cv == val.TypeVal(val.ListType) || cv == val.TypeVal(val.TupleType) || cv == val.TypeVal(val.SetType)) &&
				st.stack[retSP-1] == cv {
				return true
			}
		case OP_CALL:
			if DecodeArgBx(w1) == CallFromStack {
				cv := st.stack[retSP-1]
				if cv == val.TypeVal(val.ListType) || cv == val.TypeVal(val.TupleType) || cv == val.TypeVal(val.SetType) {
					return true
				}
			}
		case OP_CALL_METHOD_DESCR_FAST:
			if entry.Kind == ICKindMethodDescr {
				if entry.CachedVal == val.StrJoinBuiltin {
					rec := st.stack[retSP-1]
					return rec.IsAnyString() && !rec.IsInstance()
				}
				if entry.CachedVal == val.ListExtendBuiltin {
					rec := st.stack[retSP-1]
					return rec.IsList()
				}
			}
		case OP_CALL_METHOD_PIC:
			if entry.Kind == ICKindClassAttr && entry.CachedVal.IsBoundMethod() {
				bm := entry.CachedVal.AsBoundMethod()
				if bm.Func == val.StrJoinBuiltin && bm.Self.IsAnyString() {
					rec := st.stack[retSP-1]
					if instObj := rec.AsInstanceChecked(); instObj != nil && instObj.Shape != nil &&
						instObj.Shape.ID == entry.ExpectedShapeID && instObj.Header.Type.Version() == entry.ExpectedTypeVer {
						return true
					}
				}
			}
		}
	}
	return false
}

func isDirectAwaitCallSite(st *loopState, ip int) bool {
	return ip+2 < st.codeLen &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_GET_AWAITABLE &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))) == OP_AWAIT
}

func isEphemeralCoroCallSite(st *loopState, ip int) bool {
	if isDirectAwaitCallSite(st, ip) {
		return true
	}
	return ip+1 < st.codeLen &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_YIELD_FROM
}

func (vm *VM) dispatchCoroutineCall(
	st *loopState,
	co *CodeObject,
	callable val.Value,
	defaults []val.Value,
	kwDefaults *val.PyDict,
	stack []val.Value,
	ip, sp, argBase, retSP, nargs int,
	kwnames []val.Value,
	globals *val.PyDict,
	isDirectAwait bool,
) (int, int, loopCtl) {
	ephemeral := isDirectAwait || isEphemeralCoroCallSite(st, ip)
	coroVal, ok := vm.instantiateCoroutineSafe(co, callable, defaults, kwDefaults, stack, argBase, nargs, kwnames, globals, ephemeral)
	if !ok {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[retSP] = coroVal
	if isDirectAwait {
		return vm.pushInlineCoroutine(st, coroVal.AsCoroutine(), ip+2, retSP+1, retSP+1, genModeAwait, val.NoneVal())
	}
	return ip + 1, retSP + 1, ctlReload
}

func (vm *VM) pushFuncPositional(
	st *loopState,
	fn *val.PyFunction,
	cc *VMCodeCache,
	stack []val.Value,
	ip, sp, argBase, retSP, argCount, retIP int,
) (int, int, loopCtl, bool) {
	callee := AsCodeObject(fn.Code)
	if callee.IsGenerator {
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = st.globals
		}
		genVal, ok := vm.instantiateGeneratorSafe(callee, val.ObjectVal(&fn.Header), fn.Defaults, fn.KwDefaults, stack, argBase, argCount, nil, fnGlobals, isEphemeralGenCallSite(st, ip, retSP))
		if !ok {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise, true
		}
		stack[retSP] = genVal
		return retIP, retSP + 1, ctlReload, true
	}
	if callee.IsCoroutine {
		isDirectAwait := isDirectAwaitCallSite(st, ip)
		if !isDirectAwait || callee.CanSuspend {
			fnGlobals := fn.Globals
			if fnGlobals == nil {
				fnGlobals = st.globals
			}
			rIP, rSP, rCtl := vm.dispatchCoroutineCall(st, callee, val.ObjectVal(&fn.Header), fn.Defaults, fn.KwDefaults, stack, ip, sp, argBase, retSP, argCount, nil, fnGlobals, isDirectAwait)
			return rIP, rSP, rCtl, true
		}
		retIP = ip + 3
	}
	minCap := max(argBase+256, argBase+callee.NumLocals+64)
	if minCap > len(stack) {
		stack = vm.growStack(st, stack, minCap)
	}
	var nextSP int
	switch {
	case callee.SimplePositionalOnly && fn.KwDefaults == nil && argCount >= callee.PositionalParamCount-len(fn.Defaults) && argCount <= callee.PositionalParamCount:
		if argCount < callee.PositionalParamCount {
			defStart := callee.PositionalParamCount - len(fn.Defaults)
			copy(stack[argBase+argCount:argBase+callee.PositionalParamCount], fn.Defaults[argCount-defStart:])
		}
		nextSP = argBase + callee.EffectiveLocals
	case argCount == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0:
		nextSP = argBase + max(callee.NumLocals, argCount)
	default:
		var ok bool
		nextSP, ok = vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, argBase, argCount, nil)
		if !ok {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise, true
		}
	}
	if cc == nil || cc.owner != vm || cc.co != callee {
		cc = vm.fastCalleeCache(callee)
	}
	rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, fn, cc, argBase, retSP, nextSP, retIP)
	return rIP, rSP, rCtl, true
}

func (vm *VM) pushTypeInitPositional(
	st *loopState,
	tp *val.PyType,
	stack []val.Value,
	ip, sp, argBase, retSP, argCount int,
	overwritePrevSlot bool,
) (int, int, loopCtl, bool) {
	if !canFastInstantiate(tp) {
		return 0, 0, ctlNext, false
	}
	if !tp.HasInit() {
		stack[retSP] = val.NewInstanceOfType(tp)
		return ip + 1, retSP + 1, ctlReload, true
	}
	initFn := tp.DunderInitVal()
	if !initFn.IsFunction() {
		return 0, 0, ctlNext, false
	}
	initFunc := initFn.AsFunction()
	callee := AsCodeObject(initFunc.Code)
	if callee.IsGenerator || callee.IsCoroutine {
		return 0, 0, ctlNext, false
	}
	initBase := argBase
	if overwritePrevSlot {
		initBase = argBase - 1
	}
	totalInitArgs := argCount + 1
	minCap := max(initBase+totalInitArgs+256, initBase+callee.NumLocals+64)
	if minCap > len(stack) {
		stack = vm.growStack(st, stack, minCap)
	}
	if !overwritePrevSlot && argCount > 0 {
		copy(stack[initBase+1:initBase+1+argCount], stack[initBase:initBase+argCount])
	}
	instObj := val.NewInstanceOfType(tp)
	stack[initBase] = instObj
	var nextSP int
	switch {
	case totalInitArgs == callee.NumParams && len(initFunc.Defaults) == 0 && initFunc.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && !callee.HasCellVars:
		nextSP = initBase + max(callee.NumLocals, totalInitArgs)
	case callee.SimplePositionalOnly && initFunc.KwDefaults == nil && totalInitArgs >= callee.PositionalParamCount-len(initFunc.Defaults) && totalInitArgs <= callee.PositionalParamCount:
		if totalInitArgs < callee.PositionalParamCount {
			defStart := callee.PositionalParamCount - len(initFunc.Defaults)
			copy(stack[initBase+totalInitArgs:initBase+callee.PositionalParamCount], initFunc.Defaults[totalInitArgs-defStart:])
		}
		nextSP = initBase + callee.EffectiveLocals
	default:
		var ok bool
		nextSP, ok = vm.bindFunctionArgsSafe(callee, initFunc.Defaults, initFunc.KwDefaults, stack, initBase, totalInitArgs, nil)
		if !ok {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise, true
		}
	}
	cc := vm.fastCalleeCache(callee)
	rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, initFunc, cc, initBase, retSP, nextSP, ip+1)
	vm.frames[st.frameIdx-1].initInstance = instObj
	return rIP, rSP, rCtl, true
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
	isDirectAwait := isDirectAwaitCallSite(st, ip)

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
			if CacheWrapper != nil && target.Type() == CacheWrapper {
				return vm.callLRUCacheSlow(st, target, ip, sp, newBase, retSP, totalArgs, kwnames)
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
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames, fnGlobals, isEphemeralGenCallSite(st, ip, retSP))
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine && (!isDirectAwait || callee.CanSuspend) {
			return vm.dispatchCoroutineCall(st, callee, target, fn.Defaults, fn.KwDefaults, stack, ip, sp, newBase, retSP, totalArgs, kwnames, fnGlobals, isDirectAwait)
		}

		// Fast path: OP_CALL_KW PIC hit writes kwargs directly into callee slots with 0 allocations.
		// Monomorphic entry uses pushFrameFast setup without pushCalleeFrame dispatch.
		if ip < len(st.cache.kwPIC) {
			pic := &st.cache.kwPIC[ip]
			if e := &pic[0]; e.TargetCo == callee && e.TargetFn == fn && e.TargetType == nil &&
				e.PosCount == posCount && e.TotalArgs == totalArgs && e.DefaultsLen == len(fn.Defaults) &&
				e.CalleeCache != nil && e.CalleeCache.owner == vm &&
				((fn.KwDefaults == nil && !e.HasKwDefaults) || (fn.KwDefaults != nil && e.HasKwDefaults && atomic.LoadUint64(&fn.KwDefaults.VersionTag) == e.KwDefaultsVer)) &&
				!callee.IsGenerator && !callee.IsCoroutine &&
				!callee.HasCellVars && (!callee.HasCells || fn.Closure != nil) &&
				callee.NumLocals >= callee.NumParams &&
				atomic.LoadInt32(&callee.StreamGen) == e.CalleeCache.streamGen {
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
				if st.frameIdx+2 <= len(vm.frames) {
					newIP, newSP := vm.pushFrameFast(st, fn, e.CalleeCache, newBase+totalArgs, ^totalArgs, ip)
					return newIP, newSP, ctlReload
				}
				return vm.pushCalleeFrame(st, callee, fn, e.CalleeCache, newBase, retSP, newBase+e.NumLocals, retIP)
			}
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
					return vm.pushCalleeFrame(st, callee, fn, e.CalleeCache, newBase, retSP, newBase+e.NumLocals, retIP)
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
		return vm.pushCalleeFrame(st, callee, fn, cc, newBase, retSP, nextSP, retIP)

	case val.KindCode:
		callee := AsCodeObject(target)
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, nil, nil, stack, newBase, totalArgs, kwnames, st.globals, isEphemeralGenCallSite(st, ip, retSP))
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine && (!isDirectAwait || callee.CanSuspend) {
			return vm.dispatchCoroutineCall(st, callee, target, nil, nil, stack, ip, sp, newBase, retSP, totalArgs, kwnames, st.globals, isDirectAwait)
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
		return vm.pushCalleeFrame(st, callee, nil, nil, newBase, retSP, nextSP, retIP)

	case val.KindBuiltinFunc:
		builtin := target.AsBuiltinFunc()
		st.stack = stack
		st.ip = ip
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
		if canFastInstantiate(tp) {
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
									rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, initFunc, e.CalleeCache, newBase, retSP, newBase+e.NumLocals, ip+1)
									vm.frames[st.frameIdx-1].initInstance = instObj
									return rIP, rSP, rCtl
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
						rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, initFunc, cc, newBase, retSP, nextSP, ip+1)
						vm.frames[st.frameIdx-1].initInstance = instObj
						return rIP, rSP, rCtl
					}
				}
			}
		}
		st.stack = stack
		st.ip = ip
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
		if target.Kind() == val.KindNative {
			return vm.callNativeSlow(st, target, ip, sp, newBase, retSP, totalArgs, kwnames)
		}
		if target.IsInstance() {
			if dc := target.Type().DunderCallVal(); dc.IsFunction() {
				fn := dc.AsFunction()
				callee := AsCodeObject(fn.Code)
				if !callee.IsGenerator && !callee.IsCoroutine {
					minCap := max(newBase+1+totalArgs+256, newBase+1+callee.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					if totalArgs > 0 {
						copy(stack[newBase+1:newBase+1+totalArgs], stack[newBase:newBase+totalArgs])
					}
					stack[newBase] = target
					nextSP, ok := vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs+1, kwnames)
					if !ok {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						st.raiseExc = exc
						return ip, sp, ctlRaise
					}
					cc := vm.fastCalleeCache(callee)
					return vm.pushCalleeFrame(st, callee, fn, cc, newBase, retSP, nextSP, retIP)
				}
			}
		}
		st.stack = stack
		st.ip = ip
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

	// Fast in-loop frame push for Python functions, bound methods, and class
	// instantiations when positional args are a tuple/list and kwargs are absent
	// or a small local dict.
	target := callable
	hasSelf := false
	var selfVal val.Value
	var initType *val.PyType
	if target.IsBoundMethod() {
		bm := target.AsBoundMethod()
		if bm.Func.IsFunction() {
			hasSelf = true
			selfVal = bm.Self
			target = bm.Func
		}
	} else if target.IsType() {
		tp := target.AsType()
		if canFastInstantiate(tp) {
			if !tp.HasInit() && (!hasKW || kwDictVal.IsNone() || kwDictVal.HeaderPtr() == nil) {
				if (argsVal.IsTuple() && len(argsVal.AsTuple().Items) == 0) ||
					(argsVal.IsList() && len(argsVal.AsList().Items) == 0) {
					stack[sp] = val.NewInstanceOfType(tp)
					return ip + 1, sp + 1, ctlReload
				}
			} else if tp.HasInit() {
				if initFn := tp.DunderInitVal(); initFn.IsFunction() {
					initType = tp
					hasSelf = true
					target = initFn
				}
			}
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
				var initInst val.Value
				if hasSelf {
					if initType != nil {
						initInst = val.NewInstanceOfType(initType)
						selfVal = initInst
					}
					stack[dst] = selfVal
					dst++
				}
				if len(posArgs) > 0 {
					copy(stack[dst:dst+len(posArgs)], posArgs)
					dst += len(posArgs)
				}
				vm.releaseEphemeralTupleVal(argsVal)
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
							vm.releaseSmallDict(kwDict)
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
				if kwDict != nil {
					vm.releaseSmallDict(kwDict)
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
				cc := vm.fastCalleeCache(callee)
				rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, fn, cc, newBase, sp, nextSP, ip+1)
				if initType != nil {
					vm.frames[st.frameIdx-1].initInstance = initInst
				}
				return rIP, rSP, rCtl
			}
		}
	}

	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	val.ClearEphemeral(argsVal)
	res := vm.callFunctionExSafe(callable, argsVal, kwDictVal, hasKW)
	if hasKW {
		vm.releaseSmallDictVal(kwDictVal)
	}
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
	st.ip = ip
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
	if cls.IsTuple() {
		tup := cls.AsTuple()
		if (tup.Header.Flags&val.FlagEphemeralTuple) == 0 && (tup.Header.Type == nil || tup.Header.Type == val.TupleType) {
			cacheable := true
			for _, item := range tup.Items {
				if !item.IsType() {
					cacheable = false
					break
				}
				meta := item.AsType().Header.Type
				if meta != nil && meta != val.TypeType {
					cacheable = false
					break
				}
			}
			if cacheable {
				hasSpoof := false
				if inst := obj.AsInstanceChecked(); inst != nil {
					if _, ok := inst.GetAttr(nameClass); ok {
						hasSpoof = true
					}
				}
				if !hasSpoof {
					lhs := obj.Type()
					if lhs != nil {
						matched := false
						for _, item := range tup.Items {
							if val.IsSubclass(lhs, item.AsType()) {
								matched = true
								break
							}
						}
						var sid uint32
						if inst := obj.AsInstanceChecked(); inst != nil && inst.Shape != nil {
							sid = inst.Shape.ID
						}
						var slot int32
						if matched {
							slot = 1
						}
						st.cache.ic[ip] = ICSlot{
							Kind:            ICKindIsinstanceTuple,
							CachedVal:       cls,
							ExpectedTypeVer: lhs.Version(),
							ExpectedShapeID: sid,
							SlotIndex:       slot,
						}
						stack[sp-3] = val.BoolVal(matched)
						return ip + 1, sp - 2, ctlNext
					}
				}
			}
		}
	}
	st.stack = stack
	st.ip = ip
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
func (vm *VM) opCallInstanceDunderSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	argc := int(uint8(inst >> 16))
	retSP := sp - argc - 1
	stack := st.stack
	obj := stack[retSP]
	if !obj.IsInstance() {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	instObj := obj.AsInstance()
	if instObj == nil || instObj.Shape == nil {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	tp := instObj.Header.Type
	if tp == nil {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	attr, _, ok := tp.LookupMRO(val.DunderCall)
	if !ok || !attr.IsFunction() {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	fn := attr.AsFunction()
	co := AsCodeObject(fn.Code)
	if co == nil || !co.SimplePositionalOnly || co.NumParams != argc+1 || co.HasCellVars || co.IsGenerator || co.IsCoroutine || (len(co.FreeVars) != 0 && fn.Closure == nil) {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	cc := vm.fastCalleeCache(co)
	st.cache.ic[ip] = ICSlot{
		Kind:            ICKindInstanceCall,
		CachedVal:       attr,
		CalleeCache:     cc,
		ExpectedTypeVer: tp.Version(),
		ExpectedShapeID: instObj.Shape.ID,
		SlotIndex:       int32(argc + 1),
	}
	return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
}

func (vm *VM) lookupAttrQueryPIC(st *loopState, ip int, obj, rawName val.Value, wantVal bool) (res val.Value, found bool, handled bool, interned val.Value) {
	if !rawName.IsAnyString() || val.MultiThreadedFlag.Load() {
		return val.Value{}, false, false, val.Value{}
	}
	tp := obj.Type()
	if tp == nil || tp.HasCustomGetAttrOrGetAttribute() {
		return val.Value{}, false, false, val.Value{}
	}
	var instObj *val.PyInstance
	var shapeID uint32
	switch {
	case obj.IsInstance():
		instObj = obj.AsInstance()
		if instObj.Shape == nil {
			return val.Value{}, false, false, val.Value{}
		}
		shapeID = instObj.Shape.ID
	case obj.IsType() || obj.IsSuper() || obj.IsModule() || obj.Kind() == val.KindBoundMethod:
		return val.Value{}, false, false, val.Value{}
	case (tp.Flags & val.FlagHeapType) != 0:
		if obj.Kind() != val.KindString && obj.Kind() != val.KindInlineStr {
			return val.Value{}, false, false, val.Value{}
		}
	case !isNativeAttrCacheableType(tp) && tp != val.TupleType:
		return val.Value{}, false, false, val.Value{}
	}
	typeVer := tp.Version()
	pic := &st.cache.ensureAttrQueryPIC()[ip]
	for k := range 4 {
		e := &pic[k]
		if e.Kind != attrQueryNone && e.TypeVer == typeVer && e.ShapeID == shapeID && e.Name == rawName {
			switch e.Kind {
			case attrQueryMiss:
				return val.Value{}, false, true, e.Name
			case attrQueryClassAttr:
				return e.CachedVal, true, true, e.Name
			case attrQueryClassFunc:
				if wantVal {
					return val.NewBoundMethodVal(obj, e.CachedVal), true, true, e.Name
				}
				return val.Value{}, true, true, e.Name
			case attrQuerySlot:
				if instObj != nil && uint(e.SlotIndex) < uint(len(instObj.Slots)) {
					v := instObj.Slots[e.SlotIndex]
					if !v.IsUnbound() {
						return v, true, true, e.Name
					}
				}
				return val.Value{}, false, false, e.Name
			}
		}
	}

	interned = val.InternValue(rawName)
	if interned == nameDunderDict || interned == nameAnnotations || interned == nameDunderDoc || interned == nameClass {
		return val.Value{}, false, false, interned
	}
	for k := range 4 {
		e := &pic[k]
		if e.Kind != attrQueryNone && e.TypeVer == typeVer && e.ShapeID == shapeID && e.Name == interned {
			switch e.Kind {
			case attrQueryMiss:
				return val.Value{}, false, true, interned
			case attrQueryClassAttr:
				return e.CachedVal, true, true, interned
			case attrQueryClassFunc:
				if wantVal {
					return val.NewBoundMethodVal(obj, e.CachedVal), true, true, interned
				}
				return val.Value{}, true, true, interned
			case attrQuerySlot:
				if instObj != nil && uint(e.SlotIndex) < uint(len(instObj.Slots)) {
					v := instObj.Slots[e.SlotIndex]
					if !v.IsUnbound() {
						return v, true, true, interned
					}
				}
				return val.Value{}, false, false, interned
			}
		}
	}

	var slotKind uint8
	var cachedVal val.Value
	var slotIdx int32
	descr, _, foundInClass := tp.LookupMRO(interned)
	if foundInClass {
		if descr.IsDataDescriptor() || descr.Type().Slots.TpDescrSet != nil {
			return val.Value{}, false, false, interned
		}
		if instObj != nil {
			if idx, okSlot := instObj.Shape.Lookup(interned); okSlot && uint(idx) < uint(len(instObj.Slots)) && !instObj.Slots[idx].IsUnbound() {
				slotKind = attrQuerySlot
				slotIdx = int32(idx)
				res = instObj.Slots[idx]
				found = true
			}
		}
		if slotKind == attrQueryNone {
			switch {
			case descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind):
				slotKind = attrQueryClassFunc
				cachedVal = descr
				if wantVal {
					res = val.NewBoundMethodVal(obj, descr)
				}
				found = true
			case descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind:
				slotKind = attrQueryClassAttr
				cachedVal = descr
				res = descr
				found = true
			case descr.Kind() == val.KindStaticMethod:
				sm := descr.AsStaticMethod().Callable
				slotKind = attrQueryClassAttr
				cachedVal = sm
				res = sm
				found = true
			case descr.Kind() == val.KindClassMethod:
				bm := val.NewBoundMethodVal(val.TypeVal(tp), descr.AsClassMethod().Callable)
				slotKind = attrQueryClassAttr
				cachedVal = bm
				res = bm
				found = true
			case descr.Type().Slots.TpDescrGet == nil:
				slotKind = attrQueryClassAttr
				cachedVal = descr
				res = descr
				found = true
			default:
				return val.Value{}, false, false, interned
			}
		}
	} else {
		if instObj != nil {
			if idx, okSlot := instObj.Shape.Lookup(interned); okSlot && uint(idx) < uint(len(instObj.Slots)) {
				v := instObj.Slots[idx]
				if v.IsUnbound() {
					return val.Value{}, false, false, interned
				}
				slotKind = attrQuerySlot
				slotIdx = int32(idx)
				res = v
				found = true
			} else {
				slotKind = attrQueryMiss
			}
		} else {
			slotKind = attrQueryMiss
		}
	}

	insertIdx := -1
	for k := range 4 {
		if pic[k].Kind == attrQueryNone {
			insertIdx = k
			break
		}
	}
	if insertIdx < 0 {
		pic[3] = pic[2]
		pic[2] = pic[1]
		pic[1] = pic[0]
		insertIdx = 0
	}
	pic[insertIdx] = AttrQueryPICEntry{
		Name:      interned,
		CachedVal: cachedVal,
		TypeVer:   typeVer,
		ShapeID:   shapeID,
		SlotIndex: slotIdx,
		Kind:      slotKind,
	}
	return res, found, true, interned
}

//go:noinline
func (vm *VM) opCallHasattr(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc
	retSP := newBase - 1
	target := stack[retSP]
	if target != vm.builtinHasattr || argc != 2 {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	obj := stack[newBase]
	rawName := stack[newBase+1]
	if _, found, handled, interned := vm.lookupAttrQueryPIC(st, ip, obj, rawName, false); handled {
		recordHasAttrCall(obj, interned, found)
		stack[retSP] = val.BoolVal(found)
		return ip + 1, retSP + 1, ctlNext
	}
	if vm.hasTrackedViews {
		markViewEscaped(obj)
		markViewEscaped(rawName)
	}
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	interned := val.InternValue(rawName)
	ok := val.HasAttrFast(&vm.execCtx, obj, interned)
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	recordHasAttrCall(obj, interned, ok)
	stack[retSP] = val.BoolVal(ok)
	return ip + 1, retSP + 1, ctlReload
}

//go:noinline
func (vm *VM) opCallGetattr(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc
	retSP := newBase - 1
	target := stack[retSP]
	if target != vm.builtinGetattr || (argc != 2 && argc != 3) {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
		return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
	}
	obj := stack[newBase]
	rawName := stack[newBase+1]
	hasDefault := argc == 3
	if res, found, handled, interned := vm.lookupAttrQueryPIC(st, ip, obj, rawName, true); handled {
		recordGetAttrCall(obj, interned, found, hasDefault)
		if found {
			stack[retSP] = res
			return ip + 1, retSP + 1, ctlNext
		}
		if hasDefault {
			stack[retSP] = stack[newBase+2]
			return ip + 1, retSP + 1, ctlNext
		}
		st.raiseExc = val.NewLazyAttributeErrorCtx(&vm.execCtx, obj.Type(), interned)
		return ip, sp, ctlRaise
	}
	if vm.hasTrackedViews {
		markViewsEscaped(stack[newBase : newBase+argc])
	}
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	interned := val.InternValue(rawName)
	res, ok := val.LookupAttrOptional(&vm.execCtx, obj, interned)
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	recordGetAttrCall(obj, interned, ok, hasDefault)
	if ok {
		stack[retSP] = res
		return ip + 1, retSP + 1, ctlReload
	}
	if hasDefault {
		stack[retSP] = stack[newBase+2]
		return ip + 1, retSP + 1, ctlReload
	}
	st.raiseExc = val.NewLazyAttributeErrorCtx(&vm.execCtx, obj.Type(), interned)
	return ip, sp, ctlRaise
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
	if target == vm.builtinNext && (argc == 1 || argc == 2) && stack[newBase].IsGenerator() {
		mode := genModeNext
		var defVal val.Value
		if argc == 2 {
			mode = genModeNextDefault
			defVal = stack[newBase+1]
		}
		return vm.pushInlineGenerator(st, stack[newBase].AsGenerator(), ip, sp, retSP, 0, mode, defVal, val.NoneVal())
	}
	if target == vm.builtinSuper && (argc == 0 || argc == 2) {
		return vm.dispatchSuperCall(st, inst, ip, sp, newBase, retSP, argc, entry.SlotIndex == 1, false)
	}
	if target == val.StrJoinBuiltin && argc == 2 && stack[newBase].IsAnyString() && stack[newBase+1].IsGenerator() {
		return vm.pushStrJoinGenerator(st, stack[newBase], stack[newBase+1].AsGenerator(), ip, sp, retSP)
	}
	if target == val.ListExtendBuiltin && argc == 2 && stack[newBase].IsList() && stack[newBase+1].IsGenerator() {
		return vm.pushListExtendGenerator(st, stack[newBase].AsList(), stack[newBase+1].AsGenerator(), ip, sp, retSP)
	}
	if mode, defVal, gen, isliceIt, _, matched := vm.matchInlineGenConsumer(target, stack, newBase, argc); matched {
		return vm.pushCollectGenerator(st, mode, gen, ip, sp, retSP, defVal, isliceIt)
	}
	if target == val.TypeVal(val.StrType) && argc == 1 {
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
	st.stack = stack
	st.ip = ip
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
func (vm *VM) opCallTypeInit(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc
	retSP := newBase - 1
	target := stack[retSP]
	entry := &st.cache.ic[ip]
	if target == entry.CachedVal {
		tp := target.AsType()
		meta := tp.Header.Type
		metaOK := (meta == nil || meta == val.TypeType) && entry.ExpectedAuxVer == 0
		if !metaOK && meta != nil && meta != val.TypeType {
			metaOK = meta.Version() == entry.ExpectedAuxVer
		}
		if tp.Version() == entry.ExpectedTypeVer && metaOK {
			if entry.SlotIndex == 0 && entry.CalleeCache == nil && argc == 0 && !tp.HasInit() {
				stack[retSP] = val.NewInstanceOfType(tp)
				return ip + 1, retSP + 1, ctlReload
			}
			var targetFn val.Value
			isNewOnly := entry.SlotIndex == 1
			if isNewOnly {
				targetFn = tp.DunderNewVal()
			} else {
				targetFn = tp.DunderInitVal()
			}
			cc := entry.CalleeCache
			if targetFn.IsFunction() && cc != nil && cc.owner == vm && cc.co != nil {
				fn := targetFn.AsFunction()
				callee := AsCodeObject(fn.Code)
				if cc.co == callee && atomic.LoadInt32(&callee.StreamGen) == cc.streamGen &&
					!callee.HasCellVars && (!callee.HasCells || fn.Closure != nil) {
					totalArgs := argc + 1
					initBase := retSP
					var nextSP int
					matched := false
					if totalArgs == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 {
						nextSP = initBase + max(callee.NumLocals, totalArgs)
						matched = true
					} else if callee.SimplePositionalOnly && fn.KwDefaults == nil && totalArgs >= callee.PositionalParamCount-len(fn.Defaults) && totalArgs <= callee.PositionalParamCount {
						nextSP = initBase + callee.EffectiveLocals
						matched = true
					}
					if matched {
						st.periodicTick++
						if (st.periodicTick & 0xFF) == 0 {
							vm.checkPeriodicSafepoint(st.frameIdx)
						}
						if vm.hasTrackedViews && argc > 0 {
							markViewsEscaped(stack[newBase : newBase+argc])
						}
						minCap := max(initBase+totalArgs+256, initBase+callee.NumLocals+64)
						if minCap > len(stack) {
							stack = vm.growStack(st, stack, minCap)
						}
						var instObj val.Value
						if !isNewOnly {
							instObj = val.NewInstanceOfType(tp)
							stack[initBase] = instObj
						}
						if callee.SimplePositionalOnly && totalArgs < callee.PositionalParamCount {
							defStart := callee.PositionalParamCount - len(fn.Defaults)
							copy(stack[initBase+totalArgs:initBase+callee.PositionalParamCount], fn.Defaults[totalArgs-defStart:])
						}
						rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, fn, cc, initBase, retSP, nextSP, ip+1)
						if !isNewOnly {
							vm.frames[st.frameIdx-1].initInstance = instObj
						}
						return rIP, rSP, rCtl
					}
				}
			}
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
	return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
}

//go:noinline
func (vm *VM) opCallMethodDescr(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc - 1
	retSP := newBase
	receiver := stack[newBase]
	totalArgs := argc + 1
	if uint(ip) < uint(len(st.cache.methodPIC)) {
		pic := &st.cache.methodPIC[ip]
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		if receiver.IsInstance() {
			instObj := receiver.AsInstance()
			instShape := instObj.Shape
			instVer := instObj.Header.Type.Version()
			switch {
			case pic[0].Shape == instShape && instVer == pic[0].TypeVersion && pic[0].NativeFn != nil:
				nativeFn = pic[0].NativeFn
				targetVal = pic[0].TargetVal
			case pic[1].Shape == instShape && instVer == pic[1].TypeVersion && pic[1].NativeFn != nil:
				nativeFn = pic[1].NativeFn
				targetVal = pic[1].TargetVal
			case pic[2].Shape == instShape && instVer == pic[2].TypeVersion && pic[2].NativeFn != nil:
				nativeFn = pic[2].NativeFn
				targetVal = pic[2].TargetVal
			case pic[3].Shape == instShape && instVer == pic[3].TypeVersion && pic[3].NativeFn != nil:
				nativeFn = pic[3].NativeFn
				targetVal = pic[3].TargetVal
			}
		} else if !receiver.IsSuper() && !receiver.IsModule() && !receiver.IsType() {
			tp := receiver.Type()
			switch {
			case pic[0].ReceiverType == tp && pic[0].NativeFn != nil:
				nativeFn = pic[0].NativeFn
				targetVal = pic[0].TargetVal
			case pic[1].ReceiverType == tp && pic[1].NativeFn != nil:
				nativeFn = pic[1].NativeFn
				targetVal = pic[1].TargetVal
			case pic[2].ReceiverType == tp && pic[2].NativeFn != nil:
				nativeFn = pic[2].NativeFn
				targetVal = pic[2].TargetVal
			case pic[3].ReceiverType == tp && pic[3].NativeFn != nil:
				nativeFn = pic[3].NativeFn
				targetVal = pic[3].TargetVal
			}
		}
		if nativeFn != nil {
			if argc == 1 && receiver.IsGenerator() && targetVal == val.GenSendBuiltin {
				return vm.pushInlineGenerator(st, receiver.AsGenerator(), ip, sp, retSP, 0, genModeSend, val.Value{}, stack[newBase+1])
			}
			if argc == 1 && receiver.IsCoroutine() && targetVal == val.CoroSendBuiltin {
				return vm.pushInlineCoroutine(st, receiver.AsCoroutine(), ip, sp, retSP, genModeCoroSend, stack[newBase+1])
			}
			if argc == 1 && targetVal == val.StrJoinBuiltin && receiver.IsAnyString() && stack[newBase+1].IsGenerator() {
				return vm.pushStrJoinGenerator(st, receiver, stack[newBase+1].AsGenerator(), ip, sp, retSP)
			}
			if argc == 1 && targetVal == val.ListExtendBuiltin && receiver.IsList() && stack[newBase+1].IsGenerator() {
				return vm.pushListExtendGenerator(st, receiver.AsList(), stack[newBase+1].AsGenerator(), ip, sp, retSP)
			}
			if vm.hasTrackedViews && totalArgs > 0 {
				markViewsEscaped(stack[newBase : newBase+totalArgs])
			}
			origData := unsafe.SliceData(stack)
			st.stack = stack
			st.ip = ip
			vm.frames[st.frameIdx].ip = ip
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
			if unsafe.SliceData(stack) == origData {
				return ip + 1, retSP + 1, ctlNext
			}
			return ip + 1, retSP + 1, ctlReload
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_PIC))
	return vm.opCallMethodPICSlow(st, ReplaceOpcode(inst, OP_CALL_METHOD_PIC), ip, sp)
}

//go:noinline
func (vm *VM) finishMethodDescrFast(st *loopState, ip, sp, newBase int, res val.Value) (int, int, loopCtl) {
	stack := st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[newBase] = res
	return ip + 1, newBase + 1, ctlReload
}

//go:noinline
func (vm *VM) opCallBoundNative(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	retSP := sp - argc - 1
	target := stack[retSP]
	entry := &st.cache.ic[ip]
	if target.Kind() == val.KindBoundMethod && entry.NativeFn != nil {
		bm := target.AsBoundMethod()
		if bm.Func == entry.CachedVal && bm.Self.Type().Version() == entry.ExpectedTypeVer {
			if vm.hasTrackedViews && argc > 0 {
				markViewsEscaped(stack[retSP+1 : retSP+1+argc])
			}
			origTarget := target
			origData := unsafe.SliceData(stack)
			stack[retSP] = bm.Self
			st.stack = stack
			st.ip = ip
			vm.frames[st.frameIdx].ip = ip
			vm.execCtx.SP = sp
			res := entry.NativeFn(&vm.execCtx, bm.Func, stack[retSP:sp], nil)
			stack = st.syncStackFromExecCtx(vm, stack)
			if vm.execCtx.HasException() {
				stack[retSP] = origTarget
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = res
			if unsafe.SliceData(stack) == origData {
				return ip + 1, retSP + 1, ctlNext
			}
			return ip + 1, retSP + 1, ctlReload
		}
	}
	deoptInst := ReplaceOpcode(inst, OP_CALL)
	st.codePtr = st.cache.quickenAt(ip, deoptInst)
	return vm.opCallSlow(st, deoptInst, ip, sp)
}

//go:noinline
func (vm *VM) opCallMethodDescrFast(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	newBase := sp - argc - 1
	receiver := stack[newBase]
	entry := &st.cache.ic[ip]
	if entry.Kind == ICKindMethodDescr && entry.NativeFn != nil {
		matched := false
		if entry.ExpectedShapeID == 0 {
			if hdr := receiver.HeaderPtr(); hdr != nil &&
				hdr.Kind != val.KindInstance && hdr.Kind != val.KindSuper && hdr.Kind != val.KindModule && hdr.Kind != val.KindType &&
				receiver.Type().Version() == entry.ExpectedTypeVer {
				matched = true
			}
		} else if instObj := receiver.AsInstanceChecked(); instObj != nil && instObj.Shape != nil &&
			instObj.Shape.ID == entry.ExpectedShapeID && instObj.Header.Type.Version() == entry.ExpectedTypeVer {
			matched = true
		}
		if matched {
			totalArgs := argc + 1
			if argc == 1 && entry.CachedVal == val.StrJoinBuiltin && receiver.IsAnyString() && stack[newBase+1].IsGenerator() {
				return vm.pushStrJoinGenerator(st, receiver, stack[newBase+1].AsGenerator(), ip, sp, newBase)
			}
			if argc == 1 && entry.CachedVal == val.ListExtendBuiltin && receiver.IsList() && stack[newBase+1].IsGenerator() {
				return vm.pushListExtendGenerator(st, receiver.AsList(), stack[newBase+1].AsGenerator(), ip, sp, newBase)
			}
			if vm.hasTrackedViews && totalArgs > 0 {
				markViewsEscaped(stack[newBase : newBase+totalArgs])
			}
			origData := unsafe.SliceData(stack)
			st.stack = stack
			st.ip = ip
			vm.frames[st.frameIdx].ip = ip
			vm.execCtx.SP = sp
			res := entry.NativeFn(&vm.execCtx, entry.CachedVal, stack[newBase:newBase+totalArgs], nil)
			stack = st.syncStackFromExecCtx(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[newBase] = res
			if unsafe.SliceData(stack) == origData {
				return ip + 1, newBase + 1, ctlNext
			}
			return ip + 1, newBase + 1, ctlReload
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR))
	return vm.opCallMethodDescr(st, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR), ip, sp)
}

//go:noinline
func (vm *VM) opCallSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	bx := int(uint16(inst))
	isDirectAwait := isDirectAwaitCallSite(st, ip)

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
			if !unwrapped && uint16(inst) == CallFromStack && !vm.hasTrackedViews && bm.Func.IsBuiltinFunc() {
				bf := bm.Func.AsBuiltinFunc()
				if bf.Fn != nil && bm.Func != val.GenSendBuiltin && bm.Func != val.CoroSendBuiltin &&
					bm.Func != val.StrJoinBuiltin && bm.Func != val.ListExtendBuiltin {
					if selfType := bm.Self.Type(); selfType != nil {
						st.cache.ic[ip] = ICSlot{
							Kind:            ICKindMethodDescr,
							ExpectedTypeVer: selfType.Version(),
							CachedVal:       bm.Func,
							NativeFn:        bf.Fn,
						}
						st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BOUND_NATIVE))
						origTarget := target
						origData := unsafe.SliceData(stack)
						stack[retSP] = bm.Self
						st.stack = stack
						st.ip = ip
						vm.frames[st.frameIdx].ip = ip
						vm.execCtx.SP = sp
						res := bf.Fn(&vm.execCtx, bm.Func, stack[retSP:sp], nil)
						stack = st.syncStackFromExecCtx(vm, stack)
						if vm.execCtx.HasException() {
							stack[retSP] = origTarget
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, sp, ctlRaise
						}
						stack[retSP] = res
						if unsafe.SliceData(stack) == origData {
							return ip + 1, retSP + 1, ctlNext
						}
						return ip + 1, retSP + 1, ctlReload
					}
				}
			}
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
			if CacheWrapper != nil && target.Type() == CacheWrapper {
				return vm.callLRUCacheSlow(st, target, ip, sp, newBase, retSP, argc, nil)
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

	// Builtin hasattr(obj, name) & getattr(obj, name[, default]) fast-paths
	if target == vm.builtinHasattr && argc == 2 && canQuicken {
		qInst := ReplaceOpcode(inst, OP_CALL_HASATTR)
		st.codePtr = st.cache.quickenAt(ip, qInst)
		return vm.opCallHasattr(st, qInst, ip, sp)
	}
	if target == vm.builtinGetattr && (argc == 2 || argc == 3) && canQuicken {
		qInst := ReplaceOpcode(inst, OP_CALL_GETATTR)
		st.codePtr = st.cache.quickenAt(ip, qInst)
		return vm.opCallGetattr(st, qInst, ip, sp)
	}

	// Builtin next(gen[, default]) fast-path: resume generator inline without Go recursion.
	if target == vm.builtinNext && (argc == 1 || argc == 2) && stack[newBase].IsGenerator() {
		if canQuicken {
			var nativeFn val.VectorcallFunc
			if target.IsBuiltinFunc() {
				nativeFn = target.AsBuiltinFunc().Fn
			}
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: nativeFn}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		mode := genModeNext
		var defVal val.Value
		if argc == 2 {
			mode = genModeNextDefault
			defVal = stack[newBase+1]
		}
		return vm.pushInlineGenerator(st, stack[newBase].AsGenerator(), ip, sp, retSP, 0, mode, defVal, val.NoneVal())
	}

	// Builtin super() / super(Cls, self) fast-path: elide PySuper allocation and fuse immediate attr/method loads.
	if target == vm.builtinSuper && (argc == 0 || argc == 2) {
		return vm.dispatchSuperCall(st, inst, ip, sp, newBase, retSP, argc, false, canQuicken)
	}

	// Builtin str.join(gen) and list.extend(gen) fast-paths: drain generator inline in runLoopWithGlobals.
	if target == val.StrJoinBuiltin && argc == 2 && stack[newBase].IsAnyString() && stack[newBase+1].IsGenerator() {
		if canQuicken {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: target.AsBuiltinFunc().Fn}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		return vm.pushStrJoinGenerator(st, stack[newBase], stack[newBase+1].AsGenerator(), ip, sp, retSP)
	}
	if target == val.ListExtendBuiltin && argc == 2 && stack[newBase].IsList() && stack[newBase+1].IsGenerator() {
		if canQuicken {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: target.AsBuiltinFunc().Fn}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		return vm.pushListExtendGenerator(st, stack[newBase].AsList(), stack[newBase+1].AsGenerator(), ip, sp, retSP)
	}

	// Builtin list/tuple/set/dict/any/all/sum/min/max over generators and islice(gen, ...):
	// drain yielded values directly inside runLoopWithGlobals without resumeGenerator re-entry.
	if mode, defVal, gen, isliceIt, nativeFn, matched := vm.matchInlineGenConsumer(target, stack, newBase, argc); matched {
		if canQuicken {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: nativeFn}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		return vm.pushCollectGenerator(st, mode, gen, ip, sp, retSP, defVal, isliceIt)
	}

	// Builtin str fast-path: 0 allocations for int64 and strings
	if target.IsType() && target.AsType() == val.StrType && argc == 1 {
		if canQuicken && val.StrType.Slots.TpCall != nil {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: val.StrType.Slots.TpCall}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
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
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, nil, nil, stack, newBase, argc, nil, st.globals, isEphemeralGenCallSite(st, ip, retSP))
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine && (!isDirectAwait || callee.CanSuspend) {
			return vm.dispatchCoroutineCall(st, callee, target, nil, nil, stack, ip, sp, newBase, retSP, argc, nil, st.globals, isDirectAwait)
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		if callee.HasCellVars && argc < len(callee.ParamNames) {
			for i := argc; i < len(callee.ParamNames); i++ {
				stack[newBase+i] = val.NoneVal()
			}
		}
		nextSP := newBase + max(callee.NumLocals, argc)
		return vm.pushCalleeFrame(st, callee, nil, nil, newBase, retSP, nextSP, retIP)

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
			genVal, ok := vm.instantiateGeneratorSafe(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil, fnGlobals, isEphemeralGenCallSite(st, ip, retSP))
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine && (!isDirectAwait || callee.CanSuspend) {
			return vm.dispatchCoroutineCall(st, callee, target, fn.Defaults, fn.KwDefaults, stack, ip, sp, newBase, retSP, argc, nil, fnGlobals, isDirectAwait)
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		var nextSP int
		exactArgs := callee.SimplePositionalOnly && argc == callee.PositionalParamCount && len(fn.Defaults) == 0 && fn.KwDefaults == nil
		defaultsArgs := !exactArgs && callee.SimplePositionalOnly && fn.KwDefaults == nil && len(fn.Defaults) > 0 && argc >= callee.PositionalParamCount-len(fn.Defaults) && argc <= callee.PositionalParamCount
		switch {
		case exactArgs:
			nextSP = newBase + callee.EffectiveLocals
		case callee.SimplePositionalOnly && fn.KwDefaults == nil && argc >= callee.PositionalParamCount-len(fn.Defaults) && argc <= callee.PositionalParamCount:
			if argc < callee.PositionalParamCount {
				defStart := callee.PositionalParamCount - len(fn.Defaults)
				copy(stack[newBase+argc:newBase+callee.PositionalParamCount], fn.Defaults[argc-defStart:])
			}
			nextSP = newBase + callee.EffectiveLocals
		case argc == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0:
			nextSP = newBase + max(callee.NumLocals, argc)
		default:
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
			!callee.HasCellVars && (!callee.HasCells || fn.Closure != nil) {
			if exactArgs {
				if len(callee.ExceptionTable) == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
					maybeOptimizeCodeObject(vm, callee, cc)
				}
				_, _ = vm.loadWindow(callee, cc)
				st.cache.ic[ip] = ICSlot{CachedVal: target, CalleeCache: cc}
				st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_PY_EXACT_ARGS))
			} else if defaultsArgs && callee.NumLocals >= callee.PositionalParamCount {
				if len(callee.ExceptionTable) == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
					maybeOptimizeCodeObject(vm, callee, cc)
				}
				_, _ = vm.loadWindow(callee, cc)
				st.cache.ic[ip] = ICSlot{CachedVal: target, CalleeCache: cc}
				st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_PY_DEFAULTS))
			}
		}
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		return vm.pushCalleeFrame(st, callee, fn, cc, newBase, retSP, nextSP, retIP)

	case val.KindBuiltinFunc:
		builtin := target.AsBuiltinFunc()
		if canQuicken {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: builtin.Fn}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		st.stack = stack
		st.ip = ip
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
		if canFastInstantiate(tp) {
			if !tp.HasInit() {
				if argc == 0 && canQuicken {
					var metaVer uint64
					if meta := tp.Header.Type; meta != nil && meta != val.TypeType {
						metaVer = meta.Version()
					}
					st.cache.ic[ip] = ICSlot{
						CachedVal:       target,
						ExpectedTypeVer: tp.Version(),
						ExpectedAuxVer:  metaVer,
					}
					st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_TYPE_INIT))
				}
				stack[retSP] = val.NewInstanceOfType(tp)
				return ip + 1, retSP + 1, ctlReload
			}
			initFn := tp.DunderInitVal()
			if initFn.IsFunction() {
				initFunc := initFn.AsFunction()
				callee := AsCodeObject(initFunc.Code)
				if !callee.IsGenerator && !callee.IsCoroutine {
					initBase := retSP
					totalInitArgs := argc + 1
					minCap := max(initBase+totalInitArgs+256, initBase+callee.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					instObj := val.NewInstanceOfType(tp)
					stack[initBase] = instObj
					var nextSP int
					simplePos := false
					switch {
					case totalInitArgs == callee.NumParams && len(initFunc.Defaults) == 0 && initFunc.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && !callee.HasCellVars:
						nextSP = initBase + max(callee.NumLocals, totalInitArgs)
						simplePos = true
					case callee.SimplePositionalOnly && initFunc.KwDefaults == nil && totalInitArgs >= callee.PositionalParamCount-len(initFunc.Defaults) && totalInitArgs <= callee.PositionalParamCount:
						if totalInitArgs < callee.PositionalParamCount {
							defStart := callee.PositionalParamCount - len(initFunc.Defaults)
							copy(stack[initBase+totalInitArgs:initBase+callee.PositionalParamCount], initFunc.Defaults[totalInitArgs-defStart:])
						}
						nextSP = initBase + callee.EffectiveLocals
						simplePos = true
					default:
						var ok bool
						nextSP, ok = vm.bindFunctionArgsSafe(callee, initFunc.Defaults, initFunc.KwDefaults, stack, initBase, totalInitArgs, nil)
						if !ok {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, sp, ctlRaise
						}
					}
					cc := vm.fastCalleeCache(callee)
					if canQuicken && simplePos && !callee.HasCellVars && (!callee.HasCells || initFunc.Closure != nil) {
						if len(callee.ExceptionTable) == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
							maybeOptimizeCodeObject(vm, callee, cc)
						}
						_, _ = vm.loadWindow(callee, cc)
						var metaVer uint64
						if meta := tp.Header.Type; meta != nil && meta != val.TypeType {
							metaVer = meta.Version()
						}
						st.cache.ic[ip] = ICSlot{
							CachedVal:       target,
							CalleeCache:     cc,
							ExpectedTypeVer: tp.Version(),
							ExpectedAuxVer:  metaVer,
						}
						st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_TYPE_INIT))
					}
					rIP, rSP, rCtl := vm.pushCalleeFrame(st, callee, initFunc, cc, initBase, retSP, nextSP, ip+1)
					vm.frames[st.frameIdx-1].initInstance = instObj
					return rIP, rSP, rCtl
				}
			}
		} else if (tp.Flags&val.FlagHeapType) != 0 && (tp.Flags&val.FlagHasCExtBase) == 0 && !val.IsSubclass(tp, val.TypeType) && !tp.HasInit() {
			meta := tp.Header.Type
			if meta == nil || meta == val.TypeType || meta.UsesDefaultTypeCall() {
				if newFn := tp.DunderNewVal(); newFn.IsFunction() {
					newFunc := newFn.AsFunction()
					callee := AsCodeObject(newFunc.Code)
					if !callee.IsGenerator && !callee.IsCoroutine {
						initBase := retSP
						totalNewArgs := argc + 1
						minCap := max(initBase+totalNewArgs+256, initBase+callee.NumLocals+64)
						if minCap > len(stack) {
							stack = vm.growStack(st, stack, minCap)
						}
						var nextSP int
						simplePos := false
						switch {
						case totalNewArgs == callee.NumParams && len(newFunc.Defaults) == 0 && newFunc.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && !callee.HasCellVars:
							nextSP = initBase + max(callee.NumLocals, totalNewArgs)
							simplePos = true
						case callee.SimplePositionalOnly && newFunc.KwDefaults == nil && totalNewArgs >= callee.PositionalParamCount-len(newFunc.Defaults) && totalNewArgs <= callee.PositionalParamCount:
							if totalNewArgs < callee.PositionalParamCount {
								defStart := callee.PositionalParamCount - len(newFunc.Defaults)
								copy(stack[initBase+totalNewArgs:initBase+callee.PositionalParamCount], newFunc.Defaults[totalNewArgs-defStart:])
							}
							nextSP = initBase + callee.EffectiveLocals
							simplePos = true
						default:
							var ok bool
							nextSP, ok = vm.bindFunctionArgsSafe(callee, newFunc.Defaults, newFunc.KwDefaults, stack, initBase, totalNewArgs, nil)
							if !ok {
								exc := vm.execCtx.ActiveException
								vm.execCtx.ClearException()
								st.raiseExc = exc
								return ip, sp, ctlRaise
							}
						}
						cc := vm.fastCalleeCache(callee)
						if canQuicken && simplePos && !callee.HasCellVars && (!callee.HasCells || newFunc.Closure != nil) {
							if len(callee.ExceptionTable) == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
								maybeOptimizeCodeObject(vm, callee, cc)
							}
							_, _ = vm.loadWindow(callee, cc)
							var metaVer uint64
							if meta != nil && meta != val.TypeType {
								metaVer = meta.Version()
							}
							st.cache.ic[ip] = ICSlot{
								CachedVal:       target,
								CalleeCache:     cc,
								ExpectedTypeVer: tp.Version(),
								ExpectedAuxVer:  metaVer,
								SlotIndex:       1,
							}
							st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_TYPE_INIT))
						}
						return vm.pushCalleeFrame(st, callee, newFunc, cc, initBase, retSP, nextSP, ip+1)
					}
				}
			}
		}
		isBuiltinTypeCall := (tp.Flags&val.FlagHeapType) == 0 && tp.Slots.TpCall != nil && (tp.Header.Type == nil || tp.Header.Type == val.TypeType)
		if canQuicken && isBuiltinTypeCall {
			st.cache.ic[ip] = ICSlot{CachedVal: target, NativeFn: tp.Slots.TpCall}
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_BUILTIN_FAST))
		}
		st.stack = stack
		st.ip = ip
		vm.execCtx.SP = newBase + argc
		var res val.Value
		if isBuiltinTypeCall {
			res = vm.invokeBuiltinSafe(tp.Slots.TpCall, target, stack[newBase:newBase+argc], nil)
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
	default:
		if target.Kind() == val.KindNative {
			return vm.callNativeSlow(st, target, ip, sp, newBase, retSP, argc, nil)
		}
		if target.IsInstance() {
			if canQuicken && !val.MultiThreadedFlag.Load() && !vm.hasTrackedViews {
				if instQ := target.AsInstance(); instQ != nil && instQ.Shape != nil {
					if tpQ := instQ.Header.Type; tpQ != nil {
						if dcQ := tpQ.DunderCallVal(); dcQ.IsFunction() {
							fnQ := dcQ.AsFunction()
							coQ := AsCodeObject(fnQ.Code)
							if coQ != nil && coQ.SimplePositionalOnly && coQ.NumParams == argc+1 && !coQ.HasCellVars && !coQ.IsGenerator && !coQ.IsCoroutine && (len(coQ.FreeVars) == 0 || fnQ.Closure != nil) {
								ccQ := vm.fastCalleeCache(coQ)
								st.cache.ic[ip] = ICSlot{
									Kind:            ICKindInstanceCall,
									CachedVal:       dcQ,
									CalleeCache:     ccQ,
									ExpectedTypeVer: tpQ.Version(),
									ExpectedShapeID: instQ.Shape.ID,
									SlotIndex:       int32(argc + 1),
								}
								st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_INSTANCE_DUNDER))
							}
						}
					}
				}
			}
			if dc := target.Type().DunderCallVal(); dc.IsFunction() {
				fn := dc.AsFunction()
				callee := AsCodeObject(fn.Code)
				if !callee.IsGenerator && !callee.IsCoroutine {
					minCap := max(newBase+1+argc+256, newBase+1+callee.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					if argc > 0 {
						copy(stack[newBase+1:newBase+1+argc], stack[newBase:newBase+argc])
					}
					stack[newBase] = target
					totalCallArgs := argc + 1
					var nextSP int
					switch {
					case callee.SimplePositionalOnly && fn.KwDefaults == nil && totalCallArgs >= callee.PositionalParamCount-len(fn.Defaults) && totalCallArgs <= callee.PositionalParamCount:
						if totalCallArgs < callee.PositionalParamCount {
							defStart := callee.PositionalParamCount - len(fn.Defaults)
							copy(stack[newBase+totalCallArgs:newBase+callee.PositionalParamCount], fn.Defaults[totalCallArgs-defStart:])
						}
						nextSP = newBase + callee.EffectiveLocals
					case totalCallArgs == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0:
						nextSP = newBase + max(callee.NumLocals, totalCallArgs)
					default:
						var ok bool
						nextSP, ok = vm.bindFunctionArgsSafe(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalCallArgs, nil)
						if !ok {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, sp, ctlRaise
						}
					}
					cc := vm.fastCalleeCache(callee)
					return vm.pushCalleeFrame(st, callee, fn, cc, newBase, retSP, nextSP, ip+1)
				}
			}
		}
		st.stack = stack
		st.ip = ip
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
func (vm *VM) opCallPyDefaults(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	bx := int(uint16(inst))
	isDirectAwait := isDirectAwaitCallSite(st, ip)
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
	if vm.hasTrackedViews && argc > 0 {
		markViewsEscaped(stack[newBase : newBase+argc])
	}
	entry := &st.cache.ic[ip]
	if target == entry.CachedVal && entry.CalleeCache != nil && entry.CalleeCache.owner == vm {
		if target.IsFunction() {
			fn := target.AsFunction()
			cc := entry.CalleeCache
			if cc.co != nil && fn.Code.HeaderPtr() == &cc.co.Header &&
				atomic.LoadInt32(&cc.co.StreamGen) == cc.streamGen {
				callee := cc.co
				if !callee.IsGenerator && !callee.IsCoroutine &&
					callee.SimplePositionalOnly && fn.KwDefaults == nil &&
					len(fn.Defaults) > 0 && argc >= callee.PositionalParamCount-len(fn.Defaults) &&
					argc <= callee.PositionalParamCount && !callee.HasCellVars &&
					(!callee.HasCells || fn.Closure != nil) &&
					callee.NumLocals >= callee.PositionalParamCount {
					if vm.hasTrackedViews && argc > 0 {
						markViewsEscaped(stack[newBase : newBase+argc])
					}
					minCap := max(newBase+256, newBase+callee.NumLocals+64)
					if minCap > len(stack) {
						stack = vm.growStack(st, stack, minCap)
					}
					if argc < callee.PositionalParamCount {
						defStart := callee.PositionalParamCount - len(fn.Defaults)
						copy(stack[newBase+argc:newBase+callee.PositionalParamCount], fn.Defaults[argc-defStart:])
					}
					nextSP := newBase + callee.EffectiveLocals
					retIP := ip + 1
					if isDirectAwait && callee.IsCoroutine {
						retIP = ip + 3
					}
					return vm.pushCalleeFrame(st, callee, fn, cc, newBase, retSP, nextSP, retIP)
				}
			}
		}
	}
	st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL))
	return vm.opCallSlow(st, ReplaceOpcode(inst, OP_CALL), ip, sp)
}

//go:noinline
func (vm *VM) opCallMethodSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argc := int(uint8(inst >> 16))
	retSP := sp - argc - 2
	receiver := stack[retSP]
	callable := stack[retSP+1]
	isDirectAwait := isDirectAwaitCallSite(st, ip)

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

	if argc == 1 && !receiver.IsUnbound() && receiver.IsGenerator() && callable == val.GenSendBuiltin {
		return vm.pushInlineGenerator(st, receiver.AsGenerator(), ip, sp, retSP, 0, genModeSend, val.Value{}, stack[newBase+1])
	}
	if argc == 1 && !receiver.IsUnbound() && receiver.IsCoroutine() && callable == val.CoroSendBuiltin {
		return vm.pushInlineCoroutine(st, receiver.AsCoroutine(), ip, sp, retSP, genModeCoroSend, stack[newBase+1])
	}
	if totalArgs == 2 && callable == val.StrJoinBuiltin && stack[newBase].IsAnyString() && stack[newBase+1].IsGenerator() {
		return vm.pushStrJoinGenerator(st, stack[newBase], stack[newBase+1].AsGenerator(), ip, sp, retSP)
	}
	if totalArgs == 2 && callable == val.ListExtendBuiltin && stack[newBase].IsList() && stack[newBase+1].IsGenerator() {
		return vm.pushListExtendGenerator(st, stack[newBase].AsList(), stack[newBase+1].AsGenerator(), ip, sp, retSP)
	}
	if totalArgs == 1 && callable.IsBoundMethod() {
		bm := callable.AsBoundMethod()
		if bm.Func == val.StrJoinBuiltin && bm.Self.IsAnyString() && stack[newBase].IsGenerator() {
			return vm.pushStrJoinGenerator(st, bm.Self, stack[newBase].AsGenerator(), ip, sp, retSP)
		}
		if bm.Func == val.ListExtendBuiltin && bm.Self.IsList() && stack[newBase].IsGenerator() {
			return vm.pushListExtendGenerator(st, bm.Self.AsList(), stack[newBase].AsGenerator(), ip, sp, retSP)
		}
	}

	if argc == 1 && !receiver.IsUnbound() && receiver.IsInstance() && callable.IsBuiltinFunc() && callable.AsBuiltinFunc().Name == "write" {
		switch receiver.Type() {
		case StringIOType:
			sio := (*pyStringIO)(val.AsPointer(receiver))
			arg := stack[retSP+1]
			if sio.pos == len(sio.buf) {
				written := appendStringIOWrite(sio, arg)
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
			if saved.cells != nil || st.cells != nil {
				saved.cells = st.cells
			}
			if saved.cellPtrs != nil || st.cellPtrs != nil {
				saved.cellPtrs = st.cellPtrs
			}
			saved.globals = st.globals
			if saved.locals != st.curLocals {
				saved.locals = st.curLocals
			}
			saved.cache = st.cache
			saved.excStackLen = len(vm.excStack)
			saved.stackIsVM = st.stackIsVM
			if !st.curLocals.IsNone() {
				st.curLocals = val.NoneVal()
			}
			st.frameIdx++
			if st.frameIdx > vm.maxFrameIdx {
				vm.maxFrameIdx = st.frameIdx
			}
			st.base = newBase
			minCap := max(st.base+256, st.base+callee.NumLocals+64)
			if minCap > len(stack) {
				vm.growStack(st, stack, minCap)
			}
			if st.stackIsVM && minCap > vm.maxSP {
				vm.maxSP = minCap
			}
			numLocalsCallee := max(callee.NumLocals, argc)
			sp = newBase + numLocalsCallee
			vm.activeBaseOffset = st.base
			vm.activeFrameIdx = st.frameIdx
			ip = callee.EntryIP
			return ip, sp, ctlReload
		}
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, callable, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil, fnGlobals, isEphemeralGenCallSite(st, ip, retSP))
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine && (!isDirectAwait || callee.CanSuspend) {
			return vm.dispatchCoroutineCall(st, callee, callable, fn.Defaults, fn.KwDefaults, stack, ip, sp, newBase, retSP, totalArgs, nil, fnGlobals, isDirectAwait)
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		var nextSP int
		switch {
		case callee.SimplePositionalOnly && fn.KwDefaults == nil && totalArgs >= callee.PositionalParamCount-len(fn.Defaults) && totalArgs <= callee.PositionalParamCount:
			if totalArgs < callee.PositionalParamCount {
				defStart := callee.PositionalParamCount - len(fn.Defaults)
				copy(stack[newBase+totalArgs:newBase+callee.PositionalParamCount], fn.Defaults[totalArgs-defStart:])
			}
			nextSP = newBase + callee.EffectiveLocals
		case totalArgs == callee.NumParams && len(fn.Defaults) == 0 && fn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0:
			numLocalsCallee := max(callee.NumLocals, totalArgs)
			nextSP = newBase + numLocalsCallee
		default:
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
		return vm.pushCalleeFrame(st, callee, fn, vm.fastCalleeCache(callee), newBase, retSP, nextSP, retIP)

	case val.KindCode:
		callee := AsCodeObject(callable)
		if callee.IsGenerator {
			genVal, ok := vm.instantiateGeneratorSafe(callee, callable, nil, nil, stack, newBase, totalArgs, nil, st.globals, isEphemeralGenCallSite(st, ip, retSP))
			if !ok {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[retSP] = genVal
			return ip + 1, retSP + 1, ctlReload
		}
		if callee.IsCoroutine && (!isDirectAwait || callee.CanSuspend) {
			return vm.dispatchCoroutineCall(st, callee, callable, nil, nil, stack, ip, sp, newBase, retSP, totalArgs, nil, st.globals, isDirectAwait)
		}
		minCap := max(newBase+256, newBase+callee.NumLocals+64)
		if minCap > len(stack) {
			stack = vm.growStack(st, stack, minCap)
		}
		retIP := ip + 1
		if isDirectAwait && callee.IsCoroutine {
			retIP = ip + 3
		}
		if callee.HasCellVars && totalArgs < len(callee.ParamNames) {
			for i := totalArgs; i < len(callee.ParamNames); i++ {
				stack[newBase+i] = val.NoneVal()
			}
		}
		nextSP := newBase + max(callee.NumLocals, totalArgs)
		return vm.pushCalleeFrame(st, callee, nil, nil, newBase, retSP, nextSP, retIP)

	case val.KindBuiltinFunc:
		builtin := callable.AsBuiltinFunc()
		st.stack = stack
		st.ip = ip
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

	case val.KindType:
		if rIP, rSP, rCtl, handled := vm.pushTypeInitPositional(st, callable.AsType(), stack, ip, sp, newBase, retSP, totalArgs, false); handled {
			return rIP, rSP, rCtl
		}
		st.stack = stack
		st.ip = ip
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

	default:
		st.stack = stack
		st.ip = ip
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
			if sio.pos == len(sio.buf) {
				written := appendStringIOWrite(sio, arg)
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
			sio := newStringIO(StringIOType)
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
		attrName := val.InternValue(st.consts[methIdx])
		pic := &methodPIC[ip]
		var targetFn *val.PyFunction
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		found := false
		for k := range 4 {
			if pic[k].ReceiverType == tp && (pic[k].NativeFn != nil || pic[k].TargetFn != nil) {
				targetFn = pic[k].TargetFn
				nativeFn = pic[k].NativeFn
				targetVal = pic[k].TargetVal
				found = true
				break
			}
		}
		if !found {
			descr, _, ok := tp.LookupMRO(attrName)
			switch {
			case ok && descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind:
				bf := descr.AsBuiltinFunc()
				nativeFn = bf.Fn
				targetVal = descr
			case ok && descr.IsFunction():
				targetFn = descr.AsFunction()
				targetVal = descr
			default:
				st.stack = stack
				st.ip = ip
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
					pic[k] = val.MethodPICEntry{ReceiverType: tp, TargetFn: targetFn, NativeFn: nativeFn, TargetVal: targetVal}
					inserted = true
					break
				}
			}
			if !inserted {
				pic[0] = pic[1]
				pic[1] = pic[2]
				pic[2] = pic[3]
				pic[3] = val.MethodPICEntry{ReceiverType: tp, TargetFn: targetFn, NativeFn: nativeFn, TargetVal: targetVal}
			}
		}
		if targetFn != nil {
			if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, targetFn, nil, stack, ip, sp, newBase, retSP, totalArgs, ip+1); handled {
				return rIP, rSP, rCtl
			}
		}
		if nativeFn != nil {
			if argc == 1 && receiver.IsGenerator() && targetVal == val.GenSendBuiltin {
				st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR))
				return vm.pushInlineGenerator(st, receiver.AsGenerator(), ip, sp, retSP, 0, genModeSend, val.Value{}, stack[newBase+1])
			}
			if argc == 1 && receiver.IsCoroutine() && targetVal == val.CoroSendBuiltin {
				st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR))
				return vm.pushInlineCoroutine(st, receiver.AsCoroutine(), ip, sp, retSP, genModeCoroSend, stack[newBase+1])
			}
			if methIdx >= uint16(len(st.consts)) || (attrName != nameExists && attrName != nameRemove && attrName != nameUnlink) {
				if pic[0].ReceiverType == tp && pic[1].ReceiverType == nil && pic[1].Shape == nil {
					st.cache.ic[ip] = ICSlot{
						Kind:            ICKindMethodDescr,
						ExpectedTypeVer: tp.Version(),
						CachedVal:       targetVal,
						NativeFn:        nativeFn,
					}
					st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR_FAST))
				} else {
					st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR))
				}
			}
			if argc == 1 && targetVal == val.StrJoinBuiltin && receiver.IsAnyString() && stack[newBase+1].IsGenerator() {
				return vm.pushStrJoinGenerator(st, receiver, stack[newBase+1].AsGenerator(), ip, sp, retSP)
			}
			if argc == 1 && targetVal == val.ListExtendBuiltin && receiver.IsList() && stack[newBase+1].IsGenerator() {
				return vm.pushListExtendGenerator(st, receiver.AsList(), stack[newBase+1].AsGenerator(), ip, sp, retSP)
			}
			st.stack = stack
			st.ip = ip
			vm.frames[st.frameIdx].ip = ip
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
	if receiver.IsModule() && receiver.AsModule().Header.Type == val.ModuleType {
		mod := receiver.AsModule()
		modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
		modPtr := uint64(uintptr(unsafe.Pointer(mod)))
		entry := &st.cache.ic[ip]
		var callable val.Value
		var cc *VMCodeCache
		found := false
		if entry.Kind == ICKindModuleAttr && entry.ExpectedTypeVer == modVer && entry.ExpectedAuxVer == modPtr {
			callable = entry.CachedVal
			cc = entry.CalleeCache
			found = true
		} else {
			attrName := val.InternValue(st.consts[methIdx])
			if v, slot, ok := mod.Dict.GetWithSlotHint(attrName, int(entry.SlotIndex)); ok && !v.IsLazyImport() {
				callable = v
				found = true
				if v.IsFunction() {
					cc = vm.fastCalleeCache(AsCodeObject(v.AsFunction().Code))
				}
				*entry = ICSlot{
					Kind:            ICKindModuleAttr,
					ExpectedTypeVer: modVer,
					ExpectedAuxVer:  modPtr,
					CachedVal:       v,
					CalleeCache:     cc,
					SlotIndex:       int32(slot),
				}
			}
		}
		if found {
			argBase := newBase + 1
			switch callable.Kind() {
			case val.KindFunction:
				if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, callable.AsFunction(), cc, stack, ip, sp, argBase, retSP, argc, ip+1); handled {
					return rIP, rSP, rCtl
				}
			case val.KindType:
				if rIP, rSP, rCtl, handled := vm.pushTypeInitPositional(st, callable.AsType(), stack, ip, sp, argBase, retSP, argc, true); handled {
					return rIP, rSP, rCtl
				}
			case val.KindBuiltinFunc:
				bf := callable.AsBuiltinFunc()
				origData := unsafe.SliceData(stack)
				st.stack = stack
				st.ip = ip
				vm.execCtx.SP = argBase + argc
				res := vm.invokeBuiltinSafe(bf.Fn, callable, stack[argBase:argBase+argc], nil)
				stack = st.syncStackFromExecCtx(vm, stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = res
				if unsafe.SliceData(stack) == origData {
					return ip + 1, retSP + 1, ctlNext
				}
				return ip + 1, retSP + 1, ctlReload
			case val.KindNative:
				return vm.callNativeSlow(st, callable, ip, sp, argBase, retSP, argc, nil)
			}
		}
	}

	if receiver.IsType() {
		tp := receiver.AsType()
		typeVer := tp.Version()
		tpPtr := uint64(uintptr(unsafe.Pointer(tp)))
		entry := &st.cache.ic[ip]
		var callable val.Value
		var cc *VMCodeCache
		var nativeFn val.VectorcallFunc
		var bind bool
		found := false
		if entry.Kind == ICKindTypeMethod && entry.ExpectedTypeVer == typeVer && entry.ExpectedAuxVer == tpPtr {
			callable = entry.CachedVal
			cc = entry.CalleeCache
			nativeFn = entry.NativeFn
			bind = entry.SlotIndex == 1
			found = true
		} else if methIdx < uint16(len(st.consts)) && (tp.Header.Type == nil || (tp.Header.Type.Flags&val.FlagCustomGetAttribute) == 0) {
			attrName := val.InternValue(st.consts[methIdx])
			if descr, _, ok := tp.LookupMRO(attrName); ok {
				switch {
				case descr.Kind() == val.KindClassMethod:
					bind = true
					callable = descr.AsClassMethod().Callable
					if callable.IsFunction() {
						cc = vm.fastCalleeCache(AsCodeObject(callable.AsFunction().Code))
					} else if callable.IsBuiltinFunc() {
						nativeFn = callable.AsBuiltinFunc().Fn
					}
					found = true
				case descr.Kind() == val.KindStaticMethod:
					bind = false
					callable = descr.AsStaticMethod().Callable
					if callable.IsFunction() {
						cc = vm.fastCalleeCache(AsCodeObject(callable.AsFunction().Code))
					} else if callable.IsBuiltinFunc() {
						nativeFn = callable.AsBuiltinFunc().Fn
					}
					found = true
				case descr.IsFunction():
					bind = false
					callable = descr
					cc = vm.fastCalleeCache(AsCodeObject(descr.AsFunction().Code))
					found = true
				case descr.IsBuiltinFunc():
					bf := descr.AsBuiltinFunc()
					// Class-bound builtin descriptors on Type.Dict (like dict.fromkeys) are unbound in LookupMRO
					// unless wrapped as ClassMethod, so bind=false passes explicit args after cls.
					bind = false
					callable = descr
					nativeFn = bf.Fn
					found = true
				case descr.Type() != nil && descr.Type().Slots.TpDescrGet == nil:
					bind = false
					callable = descr
					found = true
				}
				if found {
					var bindFlag int32
					if bind {
						bindFlag = 1
					}
					*entry = ICSlot{
						Kind:            ICKindTypeMethod,
						ExpectedTypeVer: typeVer,
						ExpectedAuxVer:  tpPtr,
						SlotIndex:       bindFlag,
						CachedVal:       callable,
						CalleeCache:     cc,
						NativeFn:        nativeFn,
					}
				}
			}
		}
		if found {
			argBase := newBase + 1
			argCount := argc
			if bind {
				argBase = newBase
				argCount = totalArgs
			}
			switch callable.Kind() {
			case val.KindFunction:
				if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, callable.AsFunction(), cc, stack, ip, sp, argBase, retSP, argCount, ip+1); handled {
					return rIP, rSP, rCtl
				}
			case val.KindType:
				if rIP, rSP, rCtl, handled := vm.pushTypeInitPositional(st, callable.AsType(), stack, ip, sp, argBase, retSP, argCount, !bind); handled {
					return rIP, rSP, rCtl
				}
			case val.KindBuiltinFunc:
				if callable == val.StrJoinBuiltin && argCount == 2 && stack[argBase].IsAnyString() && stack[argBase+1].IsGenerator() {
					return vm.pushStrJoinGenerator(st, stack[argBase], stack[argBase+1].AsGenerator(), ip, sp, retSP)
				}
				origData := unsafe.SliceData(stack)
				st.stack = stack
				st.ip = ip
				vm.frames[st.frameIdx].ip = ip
				vm.execCtx.SP = argBase + argCount
				res := vm.invokeBuiltinSafe(nativeFn, callable, stack[argBase:argBase+argCount], nil)
				stack = st.syncStackFromExecCtx(vm, stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = res
				if unsafe.SliceData(stack) == origData {
					return ip + 1, retSP + 1, ctlNext
				}
				return ip + 1, retSP + 1, ctlReload
			case val.KindNative:
				return vm.callNativeSlow(st, callable, ip, sp, argBase, retSP, argCount, nil)
			}
			if bind {
				stack[newBase] = receiver
			}
		}
	}

	if receiver.IsInstance() {
		instObj := receiver.AsInstance()
		instShape := instObj.Shape
		instType := instObj.Header.Type
		instVer := instType.Version()
		pic := &methodPIC[ip]
		var targetFn *val.PyFunction
		var nativeFn val.VectorcallFunc
		var targetVal val.Value
		switch {
		case pic[0].Shape == instShape && instVer == pic[0].TypeVersion:
			targetFn = pic[0].TargetFn
			nativeFn = pic[0].NativeFn
			targetVal = pic[0].TargetVal
		case pic[1].Shape == instShape && instVer == pic[1].TypeVersion:
			targetFn = pic[1].TargetFn
			nativeFn = pic[1].NativeFn
			targetVal = pic[1].TargetVal
		case pic[2].Shape == instShape && instVer == pic[2].TypeVersion:
			targetFn = pic[2].TargetFn
			nativeFn = pic[2].NativeFn
			targetVal = pic[2].TargetVal
		case pic[3].Shape == instShape && instVer == pic[3].TypeVersion:
			targetFn = pic[3].TargetFn
			nativeFn = pic[3].NativeFn
			targetVal = pic[3].TargetVal
		}

		switch {
		case targetFn != nil:
			if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, targetFn, nil, stack, ip, sp, newBase, retSP, totalArgs, ip+1); handled {
				return rIP, rSP, rCtl
			}
		case nativeFn != nil:
			if methIdx >= uint16(len(st.consts)) || (st.consts[methIdx] != nameWrite && st.consts[methIdx] != nameEnter &&
				st.consts[methIdx] != nameGetValue && st.consts[methIdx] != nameRead && st.consts[methIdx] != nameExit &&
				st.consts[methIdx] != nameExists && st.consts[methIdx] != nameRemove && st.consts[methIdx] != nameUnlink) {
				if pic[0].Shape == instShape && pic[1].Shape == nil && pic[1].ReceiverType == nil && instShape != nil && instShape.ID != 0 {
					st.cache.ic[ip] = ICSlot{
						Kind:            ICKindMethodDescr,
						ExpectedTypeVer: instVer,
						ExpectedShapeID: instShape.ID,
						CachedVal:       targetVal,
						NativeFn:        nativeFn,
					}
					st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR_FAST))
				} else {
					st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_CALL_METHOD_DESCR))
				}
			}
			origData := unsafe.SliceData(stack)
			st.stack = stack
			st.ip = ip
			vm.frames[st.frameIdx].ip = ip
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
			if unsafe.SliceData(stack) == origData {
				return ip + 1, retSP + 1, ctlNext
			}
			return ip + 1, retSP + 1, ctlReload
		case instType.Flags&val.FlagCustomGetAttribute == 0 && instShape != nil:
			entry := &st.cache.ic[ip]
			var callable val.Value
			hasCallable := false
			switch {
			case entry.Kind == ICKindInstanceSlot && instShape.ID == entry.ExpectedShapeID && instVer == entry.ExpectedTypeVer:
				if c, okSlot := instObj.GetSlot(int(entry.SlotIndex)); okSlot && !c.IsUnbound() {
					callable = c
					hasCallable = true
				}
			case entry.Kind == ICKindClassAttr && instShape.ID == entry.ExpectedShapeID && instVer == entry.ExpectedTypeVer:
				callable = entry.CachedVal
				hasCallable = true
			default:
				attrName := val.InternValue(st.consts[methIdx])
				if sIdx, ok := instShape.Lookup(attrName); ok {
					descr, _, foundInClass := instType.LookupMRO(attrName)
					if !foundInClass || !descr.IsDataDescriptor() {
						*entry = ICSlot{
							Kind:            ICKindInstanceSlot,
							ExpectedShapeID: instShape.ID,
							ExpectedTypeVer: instVer,
							SlotIndex:       int32(sIdx),
						}
						if c, okSlot := instObj.GetSlot(sIdx); okSlot && !c.IsUnbound() {
							callable = c
							hasCallable = true
						}
					}
				} else if descr, _, foundInClass := instType.LookupMRO(attrName); foundInClass {
					switch {
					case descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind):
						var tFn *val.PyFunction
						var nFn val.VectorcallFunc
						if descr.IsFunction() {
							tFn = descr.AsFunction()
						} else {
							nFn = descr.AsBuiltinFunc().Fn
						}
						newPic := val.MethodPICEntry{
							Shape:       instShape,
							TypeVersion: instVer,
							TargetFn:    tFn,
							NativeFn:    nFn,
							TargetVal:   descr,
						}
						inserted := false
						for k := range 4 {
							if pic[k].Shape == nil || pic[k].Shape == instShape {
								pic[k] = newPic
								inserted = true
								break
							}
						}
						if !inserted {
							pic[0] = pic[1]
							pic[1] = pic[2]
							pic[2] = pic[3]
							pic[3] = newPic
						}
						if tFn != nil {
							if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, tFn, nil, stack, ip, sp, newBase, retSP, totalArgs, ip+1); handled {
								return rIP, rSP, rCtl
							}
						} else {
							origData := unsafe.SliceData(stack)
							st.stack = stack
							st.ip = ip
							vm.frames[st.frameIdx].ip = ip
							vm.execCtx.SP = newBase + totalArgs
							res := vm.invokeBuiltinSafe(nFn, descr, stack[newBase:newBase+totalArgs], nil)
							stack = st.syncStackFromVM(vm, stack)
							if vm.execCtx.HasException() {
								exc := vm.execCtx.ActiveException
								vm.execCtx.ClearException()
								st.raiseExc = exc
								return ip, sp, ctlRaise
							}
							stack[retSP] = res
							if unsafe.SliceData(stack) == origData {
								return ip + 1, retSP + 1, ctlNext
							}
							return ip + 1, retSP + 1, ctlReload
						}
					case descr.Kind() == val.KindStaticMethod:
						callable = descr.AsStaticMethod().Callable
						hasCallable = true
					case descr.Kind() == val.KindClassMethod:
						callable = val.NewBoundMethodVal(val.TypeVal(instType), descr.AsClassMethod().Callable)
						hasCallable = true
					case descr.Kind() == val.KindNative && CacheWrapper != nil && descr.Type() == CacheWrapper:
						return vm.callLRUCacheSlow(st, descr, ip, sp, newBase, retSP, totalArgs, nil)
					case descr.Type().Slots.TpDescrGet == nil || (descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind):
						*entry = ICSlot{
							Kind:            ICKindClassAttr,
							ExpectedShapeID: instShape.ID,
							ExpectedTypeVer: instVer,
							CachedVal:       descr,
						}
						callable = descr
						hasCallable = true
					}
				}
			}
			if hasCallable {
				argBase := newBase + 1
				argCount := argc
				if callable.IsBoundMethod() {
					bm := callable.AsBoundMethod()
					stack[newBase] = bm.Self
					argBase = newBase
					argCount = argc + 1
					callable = bm.Func
				}
				switch callable.Kind() {
				case val.KindFunction:
					if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, callable.AsFunction(), nil, stack, ip, sp, argBase, retSP, argCount, ip+1); handled {
						return rIP, rSP, rCtl
					}
				case val.KindType:
					if rIP, rSP, rCtl, handled := vm.pushTypeInitPositional(st, callable.AsType(), stack, ip, sp, argBase, retSP, argCount, argBase == newBase+1); handled {
						return rIP, rSP, rCtl
					}
				case val.KindBuiltinFunc:
					if callable == val.StrJoinBuiltin && argCount == 2 && stack[argBase].IsAnyString() && stack[argBase+1].IsGenerator() {
						return vm.pushStrJoinGenerator(st, stack[argBase], stack[argBase+1].AsGenerator(), ip, sp, retSP)
					}
					bf := callable.AsBuiltinFunc()
					origData := unsafe.SliceData(stack)
					st.stack = stack
					st.ip = ip
					vm.execCtx.SP = argBase + argCount
					res := vm.invokeBuiltinSafe(bf.Fn, callable, stack[argBase:argBase+argCount], nil)
					stack = st.syncStackFromExecCtx(vm, stack)
					if vm.execCtx.HasException() {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						st.raiseExc = exc
						return ip, sp, ctlRaise
					}
					stack[retSP] = res
					if unsafe.SliceData(stack) == origData {
						return ip + 1, retSP + 1, ctlNext
					}
					return ip + 1, retSP + 1, ctlReload
				case val.KindNative:
					return vm.callNativeSlow(st, callable, ip, sp, argBase, retSP, argCount, nil)
				}
				stack[newBase] = receiver
			}
		}
	} else if receiver.IsSuper() {
		sup := receiver.AsSuper()
		var selfType *val.PyType
		var isClassSuper bool
		if sup.Self.IsType() && val.IsSubclass(sup.Self.AsType(), sup.ThisClass) {
			selfType = sup.Self.AsType()
			isClassSuper = true
		} else {
			selfType = sup.Self.Type()
		}
		typeVer := selfType.Version()
		thisVer := sup.ThisClass.Version()
		entry := &st.cache.ic[ip]
		var callable val.Value
		var cc *VMCodeCache
		var nativeFn val.VectorcallFunc
		var bind int32
		found := false
		if entry.Kind == ICKindSuperMethod && entry.ExpectedTypeVer == typeVer && entry.ExpectedAuxVer == thisVer {
			callable = entry.CachedVal
			cc = entry.CalleeCache
			nativeFn = entry.NativeFn
			bind = entry.SlotIndex
			found = true
		} else if methIdx < uint16(len(st.consts)) {
			attrName := val.InternValue(st.consts[methIdx])
			if descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, attrName); ok {
				switch {
				case descr.IsFunction():
					if !isClassSuper {
						bind = 1
					}
					callable = descr
					cc = vm.fastCalleeCache(AsCodeObject(descr.AsFunction().Code))
					found = true
				case descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind:
					if !isClassSuper {
						bind = 1
					}
					callable = descr
					nativeFn = descr.AsBuiltinFunc().Fn
					found = true
				case descr.Kind() == val.KindStaticMethod:
					bind = 0
					callable = descr.AsStaticMethod().Callable
					if callable.IsFunction() {
						cc = vm.fastCalleeCache(AsCodeObject(callable.AsFunction().Code))
					} else if callable.IsBuiltinFunc() {
						nativeFn = callable.AsBuiltinFunc().Fn
					}
					found = true
				case descr.Kind() == val.KindClassMethod:
					bind = 2
					callable = descr.AsClassMethod().Callable
					if callable.IsFunction() {
						cc = vm.fastCalleeCache(AsCodeObject(callable.AsFunction().Code))
					} else if callable.IsBuiltinFunc() {
						nativeFn = callable.AsBuiltinFunc().Fn
					}
					found = true
				case descr.Type().Slots.TpDescrGet == nil || (descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind):
					bind = 0
					callable = descr
					if callable.IsBuiltinFunc() {
						nativeFn = callable.AsBuiltinFunc().Fn
					}
					found = true
				}
				if found {
					*entry = ICSlot{
						Kind:            ICKindSuperMethod,
						ExpectedTypeVer: typeVer,
						ExpectedAuxVer:  thisVer,
						SlotIndex:       bind,
						CachedVal:       callable,
						CalleeCache:     cc,
						NativeFn:        nativeFn,
					}
				}
			}
		}
		if found {
			selfVal := sup.Self
			if bind == 2 {
				selfVal = val.TypeVal(selfType)
			}
			vm.releaseSuper(sup)
			argBase := newBase + 1
			argCount := argc
			if bind != 0 {
				stack[newBase] = selfVal
				argBase = newBase
				argCount = argc + 1
			}
			switch callable.Kind() {
			case val.KindFunction:
				if rIP, rSP, rCtl, handled := vm.pushFuncPositional(st, callable.AsFunction(), cc, stack, ip, sp, argBase, retSP, argCount, ip+1); handled {
					return rIP, rSP, rCtl
				}
			case val.KindType:
				if rIP, rSP, rCtl, handled := vm.pushTypeInitPositional(st, callable.AsType(), stack, ip, sp, argBase, retSP, argCount, bind == 0); handled {
					return rIP, rSP, rCtl
				}
			case val.KindBuiltinFunc:
				if callable == val.StrJoinBuiltin && argCount == 2 && stack[argBase].IsAnyString() && stack[argBase+1].IsGenerator() {
					return vm.pushStrJoinGenerator(st, stack[argBase], stack[argBase+1].AsGenerator(), ip, sp, retSP)
				}
				origData := unsafe.SliceData(stack)
				st.stack = stack
				st.ip = ip
				vm.frames[st.frameIdx].ip = ip
				vm.execCtx.SP = argBase + argCount
				res := vm.invokeBuiltinSafe(nativeFn, callable, stack[argBase:argBase+argCount], nil)
				stack = st.syncStackFromExecCtx(vm, stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
				stack[retSP] = res
				if unsafe.SliceData(stack) == origData {
					return ip + 1, retSP + 1, ctlNext
				}
				return ip + 1, retSP + 1, ctlReload
			case val.KindNative:
				return vm.callNativeSlow(st, callable, ip, sp, argBase, retSP, argCount, nil)
			}
			st.stack = stack
			st.ip = ip
			vm.execCtx.SP = argBase + argCount
			res := vm.callFromHookSafe(callable, argBase, argCount, nil)
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

	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = newBase + totalArgs
	res := vm.callMethodPICSlowSafe(receiver, st.consts[methIdx], ip, newBase, argc, st.frameIdx, methodPIC)
	if receiver.IsSuper() {
		vm.releaseSuper(receiver.AsSuper())
	}
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

func (vm *VM) matchInlineGenConsumer(target val.Value, stack []val.Value, newBase, argc int) (uint8, val.Value, *val.PyGenerator, *val.PyIter, val.VectorcallFunc, bool) {
	if argc == 1 {
		arg := stack[newBase]
		var gen *val.PyGenerator
		var isliceIt *val.PyIter
		if arg.IsGenerator() {
			gen = arg.AsGenerator()
		} else if arg.IsIter() {
			it := arg.AsIter()
			if it.Kind == val.IterKindISlice && it.Obj.IsGenerator() {
				isliceIt = it
				gen = it.Obj.AsGenerator()
			}
		}
		if gen == nil {
			return genModeNone, val.Value{}, nil, nil, nil, false
		}
		switch {
		case target == val.TypeVal(val.ListType):
			return genModeCollectList, val.Value{}, gen, isliceIt, val.ListType.Slots.TpCall, true
		case target == val.TypeVal(val.TupleType):
			return genModeCollectTuple, val.Value{}, gen, isliceIt, val.TupleType.Slots.TpCall, true
		case target == val.TypeVal(val.SetType):
			return genModeCollectSet, val.Value{}, gen, isliceIt, val.SetType.Slots.TpCall, true
		case target == val.TypeVal(val.DictType):
			return genModeCollectDict, val.Value{}, gen, isliceIt, val.DictType.Slots.TpCall, true
		case target == vm.builtinAny:
			return genModeAny, val.Value{}, gen, isliceIt, vm.builtinAny.AsBuiltinFunc().Fn, true
		case target == vm.builtinAll:
			return genModeAll, val.Value{}, gen, isliceIt, vm.builtinAll.AsBuiltinFunc().Fn, true
		case target == vm.builtinSum:
			return genModeSum, val.Int64Val(0), gen, isliceIt, vm.builtinSum.AsBuiltinFunc().Fn, true
		case target == vm.builtinMin:
			return genModeMin, val.Value{}, gen, isliceIt, vm.builtinMin.AsBuiltinFunc().Fn, true
		case target == vm.builtinMax:
			return genModeMax, val.Value{}, gen, isliceIt, vm.builtinMax.AsBuiltinFunc().Fn, true
		}
		return genModeNone, val.Value{}, nil, nil, nil, false
	}
	if argc == 2 && target == vm.builtinSum {
		arg := stack[newBase]
		startVal := stack[newBase+1]
		if startVal.IsAnyString() || startVal.IsBytes() || startVal.IsByteArray() {
			return genModeNone, val.Value{}, nil, nil, nil, false
		}
		var gen *val.PyGenerator
		var isliceIt *val.PyIter
		if arg.IsGenerator() {
			gen = arg.AsGenerator()
		} else if arg.IsIter() {
			it := arg.AsIter()
			if it.Kind == val.IterKindISlice && it.Obj.IsGenerator() {
				isliceIt = it
				gen = it.Obj.AsGenerator()
			}
		}
		if gen != nil {
			return genModeSum, startVal, gen, isliceIt, vm.builtinSum.AsBuiltinFunc().Fn, true
		}
	}
	return genModeNone, val.Value{}, nil, nil, nil, false
}

//go:noinline
func (vm *VM) callLRUCacheSlow(st *loopState, target val.Value, ip, sp, newBase, retSP, argc int, kwnames []val.Value) (int, int, loopCtl) {
	stack := st.stack
	origData := unsafe.SliceData(stack)
	st.stack = stack
	st.ip = ip
	vm.frames[st.frameIdx].ip = ip
	vm.execCtx.SP = newBase + argc
	cache := (*pyLRUCache)(unsafe.Pointer(target.HeaderPtr()))
	cacheKey, ok := makeLRUCacheKey(&vm.execCtx, stack[newBase:newBase+argc], kwnames, cache.Typed)
	stack = st.syncStackFromExecCtx(vm, stack)
	if !ok {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	locked := val.MultiThreadedFlag.Load()
	if locked {
		cache.Mu.Lock()
	}
	if res, hit := cache.probeLocked(cacheKey); hit {
		if locked {
			cache.Mu.Unlock()
		}
		stack[retSP] = res
		if unsafe.SliceData(stack) == origData {
			return ip + 1, retSP + 1, ctlNext
		}
		return ip + 1, retSP + 1, ctlReload
	}
	cache.Misses++
	startGen := cache.clearGen
	if locked {
		cache.Mu.Unlock()
	}

	vm.execCtx.SP = newBase + argc
	res := vm.callFromHookSafe(cache.UserFunction, newBase, argc, kwnames)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	locked = val.MultiThreadedFlag.Load()
	if locked {
		cache.Mu.Lock()
	}
	cache.insertAfterMiss(cacheKey, res, startGen)
	if locked {
		cache.Mu.Unlock()
	}
	stack[retSP] = res
	if unsafe.SliceData(stack) == origData {
		return ip + 1, retSP + 1, ctlNext
	}
	return ip + 1, retSP + 1, ctlReload
}

//go:noinline
func (vm *VM) callNativeSlow(st *loopState, target val.Value, ip, sp, newBase, retSP, argc int, kwnames []val.Value) (int, int, loopCtl) {
	if CacheWrapper != nil && target.Type() == CacheWrapper {
		return vm.callLRUCacheSlow(st, target, ip, sp, newBase, retSP, argc, kwnames)
	}
	stack := st.stack
	st.stack = stack
	st.ip = ip
	vm.frames[st.frameIdx].ip = ip
	vm.execCtx.SP = newBase + argc
	res := vm.callFromHookSafe(target, newBase, argc, kwnames)
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
