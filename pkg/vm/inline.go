package vm

import (
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// canInlineCallee reports whether callee is a tiny leaf safe to splice into
// a caller. The rule is deliberately narrow: straight-line Python code with
// no extra locals, no jumps, no suspensions and no dynamic features.
func canInlineCallee(fn *val.PyFunction, callee *CodeObject, caller *CodeObject) bool {
	if fn == nil || callee == nil || caller == nil {
		return false
	}
	if !callee.SimplePositionalOnly {
		return false
	}
	if len(fn.Defaults) != 0 || fn.KwDefaults != nil {
		return false
	}
	if callee.HasCellVars {
		return false
	}
	allowDeref := false
	if len(callee.FreeVars) != 0 {
		if len(callee.CellVars) != 0 {
			return false
		}
		if fn.Closure == nil || len(fn.Closure) < len(callee.FreeVars) {
			return false
		}
		for _, inst := range callee.Code {
			if DecodeOpcode(inst) == OP_STORE_DEREF {
				return false
			}
		}
		allowDeref = true
	}
	if callee.IsGenerator || callee.IsCoroutine || callee.CanSuspend {
		return false
	}
	if len(callee.ExceptionTable) != 0 {
		return false
	}
	if len(callee.Code) < 2 || len(callee.Code) > 24 {
		return false
	}
	if callee.NumLocals != callee.NumParams {
		return false
	}
	if caller.NumLocals+callee.NumLocals > 220 {
		return false
	}
	start := callee.EntryIP
	if start < 0 || start >= len(callee.Code) {
		start = 0
	}
	n := len(callee.Code)
	returns := 0
	end := n
	for end > start && DecodeOpcode(callee.Code[end-1]) == OP_NOP {
		end--
	}
	if end-start < 2 || end-start > 24 {
		return false
	}
	for i := start; i < end; i++ {
		op := DecodeOpcode(callee.Code[i])
		switch op {
		case OP_RETURN, OP_RETURN_FAST, OP_RETURN_CONST:
			returns++
			if i != end-1 {
				return false
			}
		case OP_YIELD_VALUE, OP_YIELD_FROM, OP_GET_AWAITABLE, OP_AWAIT,
			OP_SETUP_RANGE, OP_FOR_ITER, OP_FOR_ITER_RANGE, OP_FOR_ITER_GEN,
			OP_LOAD_CLOSURE, OP_MAKE_FUNCTION,
			OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE,
			OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE,
			OP_FAST_LOOP_CMP_LT_CONST_JUMP,
			OP_FAST_CMP_EQ_JUMP_IF_FALSE,
			OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE,
			OP_JUMP_IF_BITAND_ZERO,
			OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE,
			OP_STORE_DEREF,
			OP_CALL, OP_CALL_METHOD, OP_CALL_KW, OP_CALL_FUNCTION_EX,
			OP_CALL_PY_EXACT_ARGS, OP_CALL_PY_DEFAULTS,
			OP_CALL_BUILTIN_FAST, OP_CALL_LEN, OP_CALL_ISINSTANCE,
			OP_CALL_TYPE_INIT, OP_CALL_METHOD_PIC,
			OP_CALL_METHOD_DESCR, OP_CALL_METHOD_DESCR_FAST,
			OP_CALL_BOUND_NATIVE, OP_CALL_HASATTR, OP_CALL_GETATTR,
			OP_CALL_INSTANCE_DUNDER, OP_COMPARE_DUNDER,
			OP_GUARD_INLINE_METHOD, OP_GUARD_INLINE_FUNC, OP_INLINE_RETURN:
			return false
		case OP_LOAD_DEREF:
			if !allowDeref {
				return false
			}
			idx := int(DecodeArgBx(callee.Code[i]))
			if idx < 0 || idx >= len(callee.FreeVars) {
				return false
			}
		}
	}
	return returns == 1
}

// inlineConstUsesConst reports whether op reads its Bx field as a const index.
func inlineConstUsesConst(op Opcode) bool {
	switch op {
	case OP_LOAD_CONST,
		OP_LOAD_GLOBAL,
		OP_LOAD_ATTR, OP_STORE_ATTR,
		OP_LOAD_FAST_ATTR, OP_STORE_FAST_ATTR,
		OP_LOAD_FAST_ATTR_MUL_SELF,
		OP_LOAD_METHOD,
		OP_LOAD_GLOBAL_FAST,
		OP_LOAD_FAST_CONST,
		OP_SUBSCR_FAST_CONST,
		OP_LOAD_FROM_DICT_OR_GLOBALS:
		return true
	}
	return false
}

// inlineRemapConsts copies body const references from callee to a per-VM
// private caller consts slice, appending missing entries. It never mutates
// shared CodeObject consts.
func inlineRemapConsts(body []uint32, calleeConsts []val.Value, callerConsts []val.Value) ([]val.Value, bool) {
	newConsts := make([]val.Value, len(callerConsts), len(callerConsts)+8)
	copy(newConsts, callerConsts)
	indexOf := func(want val.Value) (int, bool) {
		for i, c := range newConsts {
			if c == want {
				return i, true
			}
		}
		return 0, false
	}
	for i, inst := range body {
		op := DecodeOpcode(inst)
		if !inlineConstUsesConst(op) {
			continue
		}
		var idx int
		isABC := false
		switch op {
		case OP_LOAD_FAST_CONST, OP_LOAD_GLOBAL_FAST, OP_SUBSCR_FAST_CONST:
			idx = int(DecodeArgC(inst))
			isABC = true
		default:
			idx = int(DecodeArgBx(inst))
		}
		if idx < 0 || idx >= len(calleeConsts) {
			return nil, false
		}
		ni, ok := indexOf(calleeConsts[idx])
		if !ok {
			if len(newConsts) >= 0xFFFF {
				return nil, false
			}
			newConsts = append(newConsts, calleeConsts[idx])
			ni = len(newConsts) - 1
		}
		switch {
		case isABC:
			if ni > 0xFF {
				return nil, false
			}
			body[i] = EncodeABC(op, DecodeArgA(inst), DecodeArgB(inst), uint8(ni))
		default:
			body[i] = Encode(op, DecodeArgA(inst), uint16(ni))
		}
	}
	return newConsts, true
}

// callerHasExceptionRange reports whether ip lies inside any handler range.
func callerHasExceptionRange(co *CodeObject, ip int) bool {
	for _, e := range co.ExceptionTable {
		if ip >= e.StartIP && ip < e.EndIP {
			return true
		}
	}
	return false
}

// tryInlineDunderForCompare is a thin wrapper extracting shapes for compare sites.
func (vm *VM) tryInlineDunderForCompare(st *loopState, ip int, dunder val.Value, dfn *val.PyFunction, dcallee *CodeObject, left, right val.Value) bool {
	lObj, rObj := left.AsInstanceChecked(), right.AsInstanceChecked()
	if lObj == nil || rObj == nil || lObj.Shape == nil || rObj.Shape == nil {
		return false
	}
	if lObj.Header.Type == nil || rObj.Header.Type == nil {
		return false
	}
	return vm.tryInlineDunder(st, ip, dunder, dfn, dcallee, lObj.Header.Type.Version(), rObj.Header.Type.Version(), lObj.Shape.ID, rObj.Shape.ID)
}

// tryInlineMethod attempts per-VM inlining of a monomorphic method call.
// The guard checks KindInstance plus Type version and Shape ID so shadowing
// deopts cleanly to the cold stub.
func (vm *VM) tryInlineMethod(st *loopState, ip, totalArgs int, fn *val.PyFunction, recv val.Value) bool {
	if st == nil || st.cache == nil || st.curCO == nil || fn == nil {
		return false
	}
	if atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 {
		return false
	}
	if ip < 0 || ip >= len(st.cache.active) {
		return false
	}
	if DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_METHOD || DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_FUNC {
		return false
	}
	instObj := recv.AsInstanceChecked()
	if instObj == nil || instObj.Shape == nil || instObj.Header.Type == nil {
		return false
	}
	callee := AsCodeObject(fn.Code)
	if !canInlineCallee(fn, callee, st.curCO) {
		return false
	}
	if callee.NumParams != totalArgs {
		return false
	}
	if callerHasExceptionRange(st.curCO, ip) {
		return false
	}
	postCall := ip + 1
	if postCall < 0 || postCall > 0xFFFF {
		return false
	}
	if !spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_METHOD, callee, fn, totalArgs, 0, postCall) {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	cache.inlineExpect[ip] = InlineExpectEntry{Fn: val.ObjectVal(&fn.Header), TypeVer: instObj.Header.Type.Version(), ShapeID: instObj.Shape.ID}
	return true
}

// tryInlineProperty attempts per-VM inlining of a monomorphic property getter.
func (vm *VM) tryInlineProperty(st *loopState, ip int, fn *val.PyFunction, recv val.Value) bool {
	if st == nil || st.cache == nil || st.curCO == nil || fn == nil {
		return false
	}
	if atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 {
		return false
	}
	if ip < 0 || ip >= len(st.cache.active) {
		return false
	}
	if DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_METHOD || DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_FUNC {
		return false
	}
	instObj := recv.AsInstanceChecked()
	if instObj == nil || instObj.Shape == nil || instObj.Header.Type == nil {
		return false
	}
	callee := AsCodeObject(fn.Code)
	if !canInlineCallee(fn, callee, st.curCO) {
		return false
	}
	if callee.NumParams != 1 {
		return false
	}
	if callerHasExceptionRange(st.curCO, ip) {
		return false
	}
	postCall := ip + 1
	if postCall < 0 || postCall > 0xFFFF {
		return false
	}
	if !spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_METHOD, callee, fn, 1, 0, postCall) {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	cache.inlineExpect[ip] = InlineExpectEntry{Fn: val.ObjectVal(&fn.Header), TypeVer: instObj.Header.Type.Version(), ShapeID: instObj.Shape.ID}
	return true
}

// tryInlineFunc attempts per-VM inlining of a monomorphic direct call.
func (vm *VM) tryInlineFunc(st *loopState, ip, totalArgs int, fn *val.PyFunction) bool {
	if st == nil || st.cache == nil || st.curCO == nil {
		return false
	}
	if atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 {
		return false
	}
	if ip < 0 || ip >= len(st.cache.active) {
		return false
	}
	if DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_METHOD || DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_FUNC {
		return false
	}
	callee := AsCodeObject(fn.Code)
	if !canInlineCallee(fn, callee, st.curCO) {
		return false
	}
	if callee.NumParams != totalArgs {
		return false
	}
	if callerHasExceptionRange(st.curCO, ip) {
		return false
	}
	postCall := ip + 1
	if postCall < 0 || postCall > 0xFFFF {
		return false
	}
	if !spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_FUNC, callee, fn, totalArgs, 1, postCall) {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	cache.inlineExpect[ip] = InlineExpectEntry{Fn: val.ObjectVal(&fn.Header)}
	return true
}

// tryInlineDunderFast triggers inlining from the compare fast path where the
// PIC hit is already verified. It splices for the next hit.
func (vm *VM) tryInlineDunderFast(st *loopState, ip int, entry *ICSlot, fn *val.PyFunction, callee *CodeObject, a, b val.Value, sp int) {
	if st == nil || st.cache == nil || st.curCO == nil {
		return
	}
	if atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 {
		return
	}
	if ip < 0 || ip >= len(st.cache.active) {
		return
	}
	if DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_METHOD || DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_FUNC {
		return
	}
	lObj, rObj := a.AsInstanceChecked(), b.AsInstanceChecked()
	if lObj == nil || rObj == nil || lObj.Shape == nil || rObj.Shape == nil {
		return
	}
	if lObj.Header.Type == nil || rObj.Header.Type == nil {
		return
	}
	// Account for reflected order (SlotIndex 1 swaps operands before the call).
	taVer, tbVer := lObj.Header.Type.Version(), rObj.Header.Type.Version()
	lsID, rsID := lObj.Shape.ID, rObj.Shape.ID
	if entry.SlotIndex == 1 || entry.SlotIndex == 2 {
		// Slot 2 is contains (b in a) with swapped layout; keep versions aligned
		// to the post-swap order used by the fast path.
		if entry.SlotIndex == 1 {
			taVer, tbVer = tbVer, taVer
			lsID, rsID = rsID, lsID
		}
	}
	vm.tryInlineDunder(st, ip, entry.CachedVal, fn, callee, taVer, tbVer, lsID, rsID)
}

// tryInlineDunder attempts per-VM inlining of a monomorphic compare dunder.
// It fails closed on any eligibility miss, leaving the original call intact.
func (vm *VM) tryInlineDunder(st *loopState, ip int, dunder val.Value, dfn *val.PyFunction, dcallee *CodeObject, taVer, tbVer uint64, leftShapeID, rightShapeID uint32) bool {
	if st == nil || st.cache == nil || st.curCO == nil {
		return false
	}
	if atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 {
		return false
	}
	if ip < 0 || ip >= len(st.cache.active) {
		return false
	}
	if DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_METHOD || DecodeOpcode(st.cache.active[ip]) == OP_GUARD_INLINE_FUNC {
		return false
	}
	if !canInlineCallee(dfn, dcallee, st.curCO) {
		return false
	}
	if dcallee.NumParams != 2 {
		return false
	}
	if callerHasExceptionRange(st.curCO, ip) {
		return false
	}
	postCall := ip + 1
	if postCall < 0 || postCall > 0xFFFF {
		return false
	}
	if !spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_METHOD, dcallee, dfn, 2, 0, postCall) {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	cache.inlineExpect[ip] = InlineExpectEntry{Fn: dunder, TypeVer: taVer, AuxVer: tbVer, ShapeID: leftShapeID, Shape2: rightShapeID}
	return true
}

// spliceInlineBody appends a straight-line callee body plus a deopt stub to
// the private active stream and swaps the call site to a guard. It operates
// only on the per-VM private copy, so no shared CodeObject mutation occurs
// except reading shared callee bytecode.
func spliceInlineBody(vm *VM, st *loopState, guardIP int, guardOp Opcode, callee *CodeObject, fn *val.PyFunction, totalArgs, popCallable int, postCall int) bool {
	cache := st.cache
	if cache == nil || guardIP < 0 {
		return false
	}
	start := callee.EntryIP
	if start < 0 || start >= len(callee.Code) {
		start = 0
	}
	fused := callee.Active()
	if len(fused) == 0 {
		fused = callee.Code
	}
	if start >= len(fused) {
		return false
	}
	fend := len(fused)
	for fend > start && DecodeOpcode(fused[fend-1]) == OP_NOP {
		fend--
	}
	bodyLen := fend - start
	if bodyLen < 2 || bodyLen > 24 {
		return false
	}
	body := make([]uint32, bodyLen)
	copy(body, fused[start:])
	// Last instruction must be a plain return; rewrite it to the restore hop.
	lastOp := DecodeOpcode(body[bodyLen-1])
	if lastOp != OP_RETURN && lastOp != OP_RETURN_FAST && lastOp != OP_RETURN_CONST {
		return false
	}
	newConsts, ok := inlineRemapConsts(body, callee.Consts, st.consts)
	if !ok {
		return false
	}
	if len(callee.FreeVars) != 0 {
		if len(callee.CellVars) != 0 {
			return false
		}
		if fn == nil || fn.Closure == nil || len(fn.Closure) < len(callee.FreeVars) {
			return false
		}
		for i := range body {
			if DecodeOpcode(body[i]) != OP_LOAD_DEREF {
				continue
			}
			idx := int(DecodeArgBx(body[i]))
			if idx < 0 || idx >= len(callee.FreeVars) || idx >= len(fn.Closure) {
				return false
			}
			cellVal := fn.Closure[idx]
			var inner val.Value
			if cellVal.HeaderPtr() != nil && cellVal.HeaderPtr().Kind == val.KindCell {
				inner = cellVal.AsCell().Get()
			} else {
				inner = cellVal
			}
			found := -1
			for j, c := range newConsts {
				if c == inner {
					found = j
					break
				}
			}
			if found < 0 {
				if len(newConsts) >= 0xFFFF {
					return false
				}
				newConsts = append(newConsts, inner)
				found = len(newConsts) - 1
			}
			if found > 0xFFFF {
				return false
			}
			body[i] = Encode(OP_LOAD_CONST, 0, uint16(found))
		}
	} else {
		for _, inst := range body {
			if DecodeOpcode(inst) == OP_LOAD_DEREF {
				return false
			}
		}
	}
	argA := uint8(totalArgs & 0x7F)
	if popCallable != 0 {
		argA |= 0x80
	}
	body[bodyLen-1] = Encode(OP_INLINE_RETURN, argA, uint16(postCall))
	oldActive := cache.active
	if guardIP >= len(oldActive) {
		return false
	}
	origCall := oldActive[guardIP]
	bodyStart := len(oldActive)
	stubStart := bodyStart + bodyLen
	newActive := make([]uint32, 0, len(oldActive)+bodyLen+2)
	newActive = append(newActive, oldActive...)
	newActive = append(newActive, body...)
	newActive = append(newActive, origCall, Encode(OP_JUMP, 0, uint16(postCall)))
	if bodyStart > 0xFFFF || stubStart > 0xFFFF || postCall > 0xFFFF || postCall < 0 {
		return false
	}
	// Keep the ic-parallel invariant ip < len(ic): tail body and stub
	// reuse the same index space as active, so extend ic and copy the
	// original call entry to the stub for a faithful deopt.
	needIC := len(newActive)
	if len(cache.ic) < needIC {
		nic := make([]ICSlot, needIC)
		copy(nic, cache.ic)
		cache.ic = nic
	}
	if guardIP < len(cache.ic) && stubStart < len(cache.ic) {
		cache.ic[stubStart] = cache.ic[guardIP]
	}
	if len(cache.methodPIC) != 0 && len(cache.methodPIC) < needIC {
		n := make([][4]val.MethodPICEntry, needIC)
		copy(n, cache.methodPIC)
		cache.methodPIC = n
	}
	if len(cache.attrPIC) != 0 && len(cache.attrPIC) < needIC {
		n := make([][4]AttrPICEntry, needIC)
		copy(n, cache.attrPIC)
		cache.attrPIC = n
	}
	if len(cache.attrQueryPIC) != 0 && len(cache.attrQueryPIC) < needIC {
		n := make([][4]AttrQueryPICEntry, needIC)
		copy(n, cache.attrQueryPIC)
		cache.attrQueryPIC = n
	}
	if len(cache.transitionIC) != 0 && len(cache.transitionIC) < needIC {
		n := make([]TransitionICEntry, needIC)
		copy(n, cache.transitionIC)
		cache.transitionIC = n
	}
	if len(cache.kwPIC) != 0 && len(cache.kwPIC) < needIC {
		n := make([][2]KwPICEntry, needIC)
		copy(n, cache.kwPIC)
		cache.kwPIC = n
	}
	if len(cache.constKeyMaps) != 0 && len(cache.constKeyMaps) < needIC {
		n := make([]ConstKeyMapTemplate, needIC)
		copy(n, cache.constKeyMaps)
		cache.constKeyMaps = n
	}
	cache.active = newActive
	cache.codePtr = unsafe.Pointer(unsafe.SliceData(newActive))
	cache.codeLen = len(newActive)
	cache.privateActive = true
	cache.ensureInlineExpect(len(newActive))
	cache.inlineConsts = newConsts
	// Swap the call site to a guard. ArgA carries body length so the miss
	// path derives the stub without extra state.
	cache.active[guardIP] = Encode(OP_GUARD_INLINE_METHOD, uint8(bodyLen), uint16(bodyStart))
	if guardOp == OP_GUARD_INLINE_FUNC {
		cache.active[guardIP] = Encode(OP_GUARD_INLINE_FUNC, uint8(bodyLen), uint16(bodyStart))
	}
	st.codePtr = cache.codePtr
	st.codeLen = cache.codeLen
	st.consts = newConsts
	_ = stubStart
	return true
}

// opGuardInline executes a spliced guard. On hit it switches the frame base
// to the callee window and jumps to the tail body; on miss it jumps to the
// tail deopt stub holding the original call.
func (vm *VM) opGuardInline(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	bodyLen := int(DecodeArgA(inst))
	bodyStart := int(DecodeArgBx(inst))
	stubStart := bodyStart + bodyLen
	cache := st.cache
	if cache == nil || ip < 0 || bodyStart < 0 || stubStart+1 >= len(cache.active) {
		st.raiseExc = val.NewException(val.SystemErrorType, "bad inline guard")
		return ip, sp, ctlRaise
	}
	exp := InlineExpectEntry{}
	if ip < len(cache.inlineExpect) {
		exp = cache.inlineExpect[ip]
	}
	hit := false
	var totalArgs int
	if op == OP_GUARD_INLINE_FUNC {
		// Direct function identity check.
		if totalArgs = inlineTotalArgsFromStub(cache.active[stubStart]); totalArgs <= 0 {
			return stubStart, sp, ctlNext
		}
		retSP := sp - totalArgs - 1
		if retSP < 0 || retSP >= len(st.stack) {
			return stubStart, sp, ctlNext
		}
		if st.stack[retSP] == exp.Fn && !exp.Fn.IsNone() && exp.Fn.HeaderPtr() != nil {
			hit = true
		}
	} else {
		// Method/property/dunder path.
		if totalArgs = inlineTotalArgsFromStub(cache.active[stubStart]); totalArgs <= 0 {
			return stubStart, sp, ctlNext
		}
		newBase := sp - totalArgs
		if newBase < 0 || newBase >= len(st.stack) {
			return stubStart, sp, ctlNext
		}
		if DecodeOpcode(cache.active[stubStart]) == OP_COMPARE_DUNDER {
			// Dunder: check both operands.
			if totalArgs != 2 || newBase+1 >= len(st.stack) {
				return stubStart, sp, ctlNext
			}
			left, right := st.stack[newBase], st.stack[newBase+1]
			lObj, rObj := left.AsInstanceChecked(), right.AsInstanceChecked()
			if lObj == nil || rObj == nil || lObj.Shape == nil || rObj.Shape == nil {
				return stubStart, sp, ctlNext
			}
			if lObj.Header.Type == nil || rObj.Header.Type == nil {
				return stubStart, sp, ctlNext
			}
			if lObj.Shape.ID == exp.ShapeID && lObj.Header.Type.Version() == exp.TypeVer &&
				rObj.Shape.ID == exp.Shape2 && rObj.Header.Type.Version() == exp.AuxVer && !exp.Fn.IsNone() {
				hit = true
			}
		} else {
			recv := st.stack[newBase]
			instObj := recv.AsInstanceChecked()
			if instObj != nil && instObj.Shape != nil && instObj.Header.Type != nil {
				if instObj.Shape.ID == exp.ShapeID && instObj.Header.Type.Version() == exp.TypeVer && !exp.Fn.IsNone() {
					hit = true
				}
			}
		}
	}
	if !hit {
		return stubStart, sp, ctlNext
	}
	newBase := sp - totalArgs
	if newBase < 0 {
		return stubStart, sp, ctlNext
	}
	// Reserve one slot for the saved caller base above the args.
	need := sp + 1
	if need > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, need)
	}
	// Stash the caller base as an int-carrying value cell.
	st.stack[sp] = val.Int64Val(int64(st.base))
	sp++
	savedBase := st.base
	_ = savedBase
	st.base = newBase
	if cache.inlineConsts != nil {
		st.consts = cache.inlineConsts
	}
	// Zero any extra locals beyond the bound args so spliced bodies never
	// observe stale frame data.
	if !exp.Fn.IsNone() && exp.Fn.IsFunction() {
		if callee := AsCodeObject(exp.Fn.AsFunction().Code); callee != nil {
			if callee.NumLocals > totalArgs {
				needLocals := newBase + callee.NumLocals + 1
				if needLocals > len(st.stack) {
					st.stack = vm.growStack(st, st.stack, needLocals)
				}
				for i := newBase + totalArgs; i < newBase+callee.NumLocals; i++ {
					st.stack[i] = val.NoneVal()
				}
			}
		}
	}
	vm.activeBaseOffset = newBase
	vm.activeFrameIdx = st.frameIdx
	if st.stackIsVM && sp+256 > vm.maxSP {
		vm.maxSP = sp + 256
	}
	return bodyStart, sp, ctlReload
}

// inlineTotalArgsFromStub decodes totalArgs from a tail stub original call.
func inlineTotalArgsFromStub(stubCall uint32) int {
	switch DecodeOpcode(stubCall) {
	case OP_CALL_METHOD_PIC:
		return int(uint8(stubCall>>16)) + 1
	case OP_CALL_PY_EXACT_ARGS:
		return int(uint8(stubCall >> 16))
	case OP_COMPARE_DUNDER:
		return 2
	case OP_LOAD_FAST_ATTR:
		return 1
	}
	return 0
}

// opInlineReturn restores the caller base, places the result and jumps back.
func (vm *VM) opInlineReturn(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	argA := DecodeArgA(inst)
	totalArgs := int(argA & 0x7F)
	pop := 0
	if argA&0x80 != 0 {
		pop = 1
	}
	postCall := int(DecodeArgBx(inst))
	calleeBase := st.base
	if sp <= calleeBase {
		st.raiseExc = val.NewException(val.SystemErrorType, "bad inline return")
		return ip, sp, ctlRaise
	}
	sp--
	retVal := st.stack[sp]
	savedSlot := calleeBase + totalArgs
	if savedSlot < 0 || savedSlot >= len(st.stack) {
		st.raiseExc = val.NewException(val.SystemErrorType, "bad inline base")
		return ip, sp, ctlRaise
	}
	oldBase := int(st.stack[savedSlot].AsInt64())
	retSP := calleeBase - pop
	if retSP < 0 || retSP >= len(st.stack) {
		st.raiseExc = val.NewException(val.SystemErrorType, "bad inline ret")
		return ip, sp, ctlRaise
	}
	st.stack[retSP] = retVal
	sp = retSP + 1
	st.base = oldBase
	vm.activeBaseOffset = oldBase
	vm.activeFrameIdx = st.frameIdx
	return postCall, sp, ctlReload
}
