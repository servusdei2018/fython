package vm

import (
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// canInlineCallee reports whether callee is a small leaf safe to splice into
// a caller. Supports straight-line and forward-only branching Python code
// (2 <= len <= 48, zero loops, zero exception handlers, no suspensions).
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
	if len(callee.Code) < 2 || len(callee.Code) > 48 {
		return false
	}
	if callee.NumLocals < callee.NumParams || callee.NumLocals > 127 || callee.NumParams > 127 {
		return false
	}
	if caller.NumLocals+callee.NumLocals > 220 {
		return false
	}
	fused := callee.Active()
	if len(fused) == 0 {
		fused = callee.Code
	}
	start := callee.EntryIP
	if start < 0 || start >= len(fused) {
		start = 0
	}
	end := len(fused)
	for end > start && DecodeOpcode(fused[end-1]) == OP_NOP {
		end--
	}
	if end-start < 2 || end-start > 48 {
		return false
	}
	returns := 0
	for i := start; i < end; i++ {
		inst := fused[i]
		op := DecodeOpcode(inst)
		switch op {
		case OP_RETURN, OP_RETURN_FAST, OP_RETURN_CONST:
			returns++
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE:
			target := int(DecodeArgBx(inst))
			if target <= i || target > end {
				return false
			}
		case OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE:
			rel := int(DecodeArgC(inst))
			target := i + rel
			if rel <= 0 || target <= i || target >= end {
				return false
			}
		case OP_YIELD_VALUE, OP_YIELD_FROM, OP_GET_AWAITABLE, OP_AWAIT,
			OP_SETUP_RANGE, OP_FOR_ITER, OP_FOR_ITER_RANGE, OP_FOR_ITER_GEN,
			OP_LOAD_CLOSURE, OP_MAKE_FUNCTION,
			OP_FAST_LOOP_CMP_LT_CONST_JUMP,
			OP_FAST_CMP_EQ_JUMP_IF_FALSE,
			OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE,
			OP_JUMP_IF_BITAND_ZERO,
			OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE,
			OP_STORE_DEREF,
			OP_CALL, OP_CALL_METHOD, OP_CALL_KW, OP_CALL_FUNCTION_EX,
			OP_CALL_PY_EXACT_ARGS, OP_CALL_PY_DEFAULTS,
			OP_CALL_BUILTIN_FAST,
			OP_CALL_TYPE_INIT, OP_CALL_METHOD_PIC,
			OP_CALL_METHOD_DESCR,
			OP_CALL_FAST_ATTR_DESCR,
			OP_CALL_BOUND_NATIVE, OP_CALL_HASATTR, OP_CALL_GETATTR,
			OP_CALL_INSTANCE_DUNDER, OP_COMPARE_DUNDER,
			OP_GUARD_INLINE_METHOD, OP_GUARD_INLINE_FUNC, OP_INLINE_RETURN:
			return false
		case OP_LOAD_DEREF:
			if !allowDeref {
				return false
			}
			idx := int(DecodeArgBx(inst))
			if idx < 0 || idx >= len(callee.FreeVars) {
				return false
			}
		}
	}
	if returns < 1 {
		return false
	}
	lastOp := DecodeOpcode(fused[end-1])
	return lastOp == OP_RETURN || lastOp == OP_RETURN_FAST || lastOp == OP_RETURN_CONST || lastOp == OP_JUMP
}

// inlineLocalsDefinitelyAssigned reports whether every non-parameter local
// slot read in body is guaranteed to have been written on all forward control
// flow paths from entry, allowing the inline guard to skip None zeroing.
func inlineLocalsDefinitelyAssigned(body []uint32, start, numParams, numLocals int) bool {
	if numLocals <= numParams {
		return true
	}
	if numLocals > 128 {
		return false
	}
	var initMask [2]uint64
	for r := range numParams {
		initMask[r>>6] |= 1 << uint(r&63)
	}
	isSet := func(m [2]uint64, r int) bool {
		if r < numParams {
			return true
		}
		if r < 0 || r >= 128 {
			return false
		}
		return (m[r>>6] & (1 << uint(r&63))) != 0
	}
	setBit := func(m *[2]uint64, r int) {
		if r >= 0 && r < 128 {
			m[r>>6] |= 1 << uint(r&63)
		}
	}
	n := len(body)
	entryMask := make([][2]uint64, n)
	hasEntry := make([]bool, n)
	mergeTo := func(target int, m [2]uint64) {
		if target < 0 || target >= n {
			return
		}
		if !hasEntry[target] {
			entryMask[target] = m
			hasEntry[target] = true
		} else {
			entryMask[target][0] &= m[0]
			entryMask[target][1] &= m[1]
		}
	}
	assigned := initMask
	reachable := true
	for i, inst := range body {
		if hasEntry[i] {
			if reachable {
				assigned[0] &= entryMask[i][0]
				assigned[1] &= entryMask[i][1]
			} else {
				assigned = entryMask[i]
				reachable = true
			}
		}
		if !reachable {
			continue
		}
		op := DecodeOpcode(inst)
		switch op {
		case OP_LOAD_FAST, OP_POP_LOAD_FAST:
			if !isSet(assigned, int(DecodeArgBx(inst))) {
				return false
			}
		case OP_LOAD_FAST_CONST, OP_LOAD_FAST_ATTR, OP_LOAD_FAST_ATTR_MUL_SELF,
			OP_STORE_FAST_ATTR, OP_SUBSCR_FAST_CONST:
			if !isSet(assigned, int(DecodeArgA(inst))) {
				return false
			}
		case OP_LOAD_FAST_2, OP_COMPARE_FAST_FAST:
			if !isSet(assigned, int(DecodeArgB(inst))) || !isSet(assigned, int(DecodeArgC(inst))) {
				return false
			}
		case OP_LOAD_GLOBAL_FAST:
			if !isSet(assigned, int(DecodeArgC(inst))) {
				return false
			}
		case OP_FAST_ADD_INT_LOCALS, OP_FAST_SUB_INT_LOCALS:
			if !isSet(assigned, int(DecodeArgA(inst))) || !isSet(assigned, int(DecodeArgC(inst))) {
				return false
			}
		case OP_FAST_ADD_INT_LOCAL_TO_LOCAL:
			if !isSet(assigned, int(DecodeArgB(inst))) || !isSet(assigned, int(DecodeArgC(inst))) {
				return false
			}
			setBit(&assigned, int(DecodeArgA(inst)))
		case OP_STORE_FAST, OP_STORE_FAST_VIEW:
			setBit(&assigned, int(DecodeArgBx(inst)))
		case OP_STORE_LOAD_FAST:
			setBit(&assigned, int(DecodeArgB(inst)))
			if !isSet(assigned, int(DecodeArgC(inst))) {
				return false
			}
		case OP_UNPACK_2_FAST:
			setBit(&assigned, int(DecodeArgA(inst)))
			setBit(&assigned, int(DecodeArgB(inst)))
		case OP_UNPACK_3_FAST:
			setBit(&assigned, int(DecodeArgA(inst)))
			setBit(&assigned, int(DecodeArgB(inst)))
			setBit(&assigned, int(DecodeArgC(inst)))
		case OP_RETURN_FAST:
			if !isSet(assigned, int(DecodeArgBx(inst))) {
				return false
			}
			reachable = false
		case OP_RETURN, OP_RETURN_CONST:
			reachable = false
		case OP_JUMP:
			mergeTo(int(DecodeArgBx(inst))-start, assigned)
			reachable = false
		case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE:
			mergeTo(int(DecodeArgBx(inst))-start, assigned)
		case OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE:
			mergeTo(i+int(DecodeArgC(inst)), assigned)
		case OP_NOP, OP_LOAD_CONST, OP_LOAD_GLOBAL, OP_STORE_GLOBAL, OP_DELETE_GLOBAL,
			OP_LOAD_NAME, OP_STORE_NAME, OP_DELETE_NAME, OP_LOAD_FROM_DICT_OR_GLOBALS, OP_LOAD_DEREF,
			OP_POP_TOP, OP_DUP_TOP,
			OP_BINARY_ADD, OP_BINARY_SUB, OP_BINARY_MUL, OP_BINARY_TRUE_DIV, OP_BINARY_FLOOR_DIV,
			OP_BINARY_MOD, OP_BINARY_POW, OP_BINARY_LSHIFT, OP_BINARY_RSHIFT,
			OP_BINARY_AND, OP_BINARY_OR, OP_BINARY_XOR, OP_BINARY_MATMUL,
			OP_BINARY_SUBSCR, OP_BINARY_SLICE,
			OP_BINARY_ADD_INT, OP_BINARY_ADD_FLOAT, OP_BINARY_SUB_INT,
			OP_UNARY_POSITIVE, OP_UNARY_NEGATIVE, OP_UNARY_NOT, OP_UNARY_INVERT,
			OP_COMPARE_OP, OP_COMPARE_LT, OP_COMPARE_LT_INT,
			OP_LOAD_ATTR, OP_STORE_ATTR, OP_DELETE_ATTR, OP_STORE_SUBSCR, OP_DELETE_SUBSCR, OP_LOAD_METHOD,
			OP_BUILD_TUPLE, OP_BUILD_LIST, OP_BUILD_SET, OP_BUILD_MAP, OP_BUILD_CONST_KEY_MAP,
			OP_BUILD_SLICE, OP_BUILD_STRING, OP_FORMAT_VALUE, OP_UNPACK_SEQUENCE, OP_UNPACK_EX,
			OP_LIST_APPEND, OP_SET_ADD, OP_MAP_ADD, OP_LIST_EXTEND, OP_LIST_TO_TUPLE, OP_LIST_TO_SET,
			OP_BUILD_MAP_UNPACK, OP_CALL_LEN, OP_CALL_ISINSTANCE, OP_CALL_METHOD_DESCR_FAST, OP_RAISE:
			// Does not read or write local slots.
		default:
			return false
		}
	}
	return true
}

// inlineConstUsesConst reports whether op reads a constant index from callee.Consts.
func inlineConstUsesConst(op Opcode) bool {
	switch op {
	case OP_LOAD_CONST,
		OP_RETURN_CONST,
		OP_LOAD_GLOBAL,
		OP_LOAD_ATTR, OP_STORE_ATTR,
		OP_LOAD_FAST_ATTR, OP_STORE_FAST_ATTR,
		OP_LOAD_FAST_ATTR_MUL_SELF,
		OP_LOAD_METHOD,
		OP_LOAD_GLOBAL_FAST,
		OP_LOAD_FAST_CONST,
		OP_SUBSCR_FAST_CONST,
		OP_CMP_CONST_JUMP_IF_FALSE,
		OP_CMP_CONST_JUMP_IF_TRUE,
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
		constField := 0 // 0=Bx, 1=B, 2=C
		switch op {
		case OP_LOAD_GLOBAL_FAST, OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE:
			idx = int(DecodeArgB(inst))
			constField = 1
		case OP_SUBSCR_FAST_CONST:
			idx = int(DecodeArgC(inst))
			constField = 2
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
		switch constField {
		case 1:
			if ni > 0xFF {
				return nil, false
			}
			body[i] = EncodeABC(op, DecodeArgA(inst), uint8(ni), DecodeArgC(inst))
		case 2:
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
	if DecodeOpcode(st.cache.active[ip]) != OP_CALL_METHOD_PIC {
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
	zeroFrom, ok := spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_METHOD, st.cache.active[ip], callee, fn, totalArgs, 0, postCall)
	if !ok {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	savedSlot := uint8(max(totalArgs, callee.NumLocals))
	cache.inlineExpect[ip] = InlineExpectEntry{
		Fn:        val.ObjectVal(&fn.Header),
		TypeVer:   instObj.Header.Type.Version(),
		ShapeID:   instObj.Shape.ID,
		TotalArgs: uint8(totalArgs),
		SavedSlot: savedSlot,
		ZeroFrom:  zeroFrom,
	}
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
	curOp := DecodeOpcode(st.cache.active[ip])
	isFastAttrProp := false
	var origCall uint32
	switch {
	case curOp == OP_LOAD_ATTR:
		origCall = st.cache.active[ip]
	case curOp == OP_NOP && ip > 0 && DecodeOpcode(st.cache.active[ip-1]) == OP_LOAD_FAST_ATTR:
		isFastAttrProp = true
		origCall = Encode(OP_LOAD_ATTR, 0, DecodeArgBx(st.cache.active[ip-1]))
	default:
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
	if callerHasExceptionRange(st.curCO, ip) || (isFastAttrProp && callerHasExceptionRange(st.curCO, ip-1)) {
		return false
	}
	postCall := ip + 1
	if postCall < 0 || postCall > 0xFFFF {
		return false
	}
	zeroFrom, ok := spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_METHOD, origCall, callee, fn, 1, 0, postCall)
	if !ok {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	savedSlot := uint8(max(1, callee.NumLocals))
	cache.inlineExpect[ip] = InlineExpectEntry{
		Fn:             val.ObjectVal(&fn.Header),
		TypeVer:        instObj.Header.Type.Version(),
		ShapeID:        instObj.Shape.ID,
		TotalArgs:      1,
		SavedSlot:      savedSlot,
		ZeroFrom:       zeroFrom,
		IsFastAttrProp: isFastAttrProp,
	}
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
	curOp := DecodeOpcode(st.cache.active[ip])
	if curOp != OP_CALL_PY_EXACT_ARGS && curOp != OP_CALL {
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
	zeroFrom, ok := spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_FUNC, st.cache.active[ip], callee, fn, totalArgs, 1, postCall)
	if !ok {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	savedSlot := uint8(max(totalArgs, callee.NumLocals))
	cache.inlineExpect[ip] = InlineExpectEntry{
		Fn:        val.ObjectVal(&fn.Header),
		TotalArgs: uint8(totalArgs),
		SavedSlot: savedSlot,
		ZeroFrom:  zeroFrom,
	}
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
	curOp := DecodeOpcode(st.cache.active[ip])
	if curOp != OP_COMPARE_DUNDER && curOp != OP_COMPARE_OP && curOp != OP_COMPARE_LT {
		return false
	}
	if !canInlineCallee(dfn, dcallee, st.curCO) {
		return false
	}
	if dcallee.NumParams != 2 {
		return false
	}
	// Do not inline compare dunders that may return NotImplemented, since
	// OP_INLINE_RETURN bypasses the reflected-dunder / TypeError fallback.
	for _, c := range dcallee.Consts {
		if c.IsNotImplemented() || (c.IsAnyString() && c.AsString() == "NotImplemented") {
			return false
		}
	}
	for _, cv := range dfn.Closure {
		if cv.IsNotImplemented() || (cv.IsCell() && cv.AsCell().Get().IsNotImplemented()) {
			return false
		}
	}
	if callerHasExceptionRange(st.curCO, ip) {
		return false
	}
	postCall := ip + 1
	if postCall < 0 || postCall > 0xFFFF {
		return false
	}
	zeroFrom, ok := spliceInlineBody(vm, st, ip, OP_GUARD_INLINE_METHOD, st.cache.active[ip], dcallee, dfn, 2, 0, postCall)
	if !ok {
		return false
	}
	cache := st.cache
	cache.ensureInlineExpect(ip + 1)
	savedSlot := uint8(max(2, dcallee.NumLocals))
	cache.inlineExpect[ip] = InlineExpectEntry{
		Fn:        dunder,
		TypeVer:   taVer,
		AuxVer:    tbVer,
		ShapeID:   leftShapeID,
		Shape2:    rightShapeID,
		TotalArgs: 2,
		SavedSlot: savedSlot,
		ZeroFrom:  zeroFrom,
		Dunder:    true,
	}
	return true
}

// spliceInlineBody appends a leaf callee body (straight-line or forward-only
// branching) plus a deopt stub to the private active stream and swaps the call
// site to a guard. It operates only on the per-VM private copy, so no shared
// CodeObject mutation occurs except reading shared callee bytecode.
func spliceInlineBody(vm *VM, st *loopState, guardIP int, guardOp Opcode, origCall uint32, callee *CodeObject, fn *val.PyFunction, totalArgs, popCallable int, postCall int) (uint8, bool) {
	cache := st.cache
	if cache == nil || guardIP < 0 {
		return 0, false
	}
	fused := callee.Active()
	if len(fused) == 0 {
		fused = callee.Code
	}
	start := callee.EntryIP
	if start < 0 || start >= len(fused) {
		start = 0
	}
	fend := len(fused)
	for fend > start && DecodeOpcode(fused[fend-1]) == OP_NOP {
		fend--
	}
	bodyLen := fend - start
	if bodyLen < 2 || bodyLen > 48 {
		return 0, false
	}
	body := make([]uint32, bodyLen)
	copy(body, fused[start:fend])
	lastOp := DecodeOpcode(body[bodyLen-1])
	if lastOp != OP_RETURN && lastOp != OP_RETURN_FAST && lastOp != OP_RETURN_CONST && lastOp != OP_JUMP {
		return 0, false
	}
	if fn != nil && fn.Globals != st.globals {
		for _, inst := range body {
			switch DecodeOpcode(inst) {
			case OP_LOAD_GLOBAL, OP_LOAD_GLOBAL_FAST, OP_LOAD_FROM_DICT_OR_GLOBALS,
				OP_STORE_GLOBAL, OP_DELETE_GLOBAL,
				OP_LOAD_NAME, OP_STORE_NAME, OP_DELETE_NAME:
				return 0, false
			}
		}
	}
	baseConsts := st.consts
	if len(cache.inlineConsts) > len(baseConsts) {
		baseConsts = cache.inlineConsts
	}
	newConsts, ok := inlineRemapConsts(body, callee.Consts, baseConsts)
	if !ok {
		return 0, false
	}
	if len(callee.FreeVars) != 0 {
		if len(callee.CellVars) != 0 {
			return 0, false
		}
		if fn == nil || fn.Closure == nil || len(fn.Closure) < len(callee.FreeVars) {
			return 0, false
		}
		for i := range body {
			if DecodeOpcode(body[i]) != OP_LOAD_DEREF {
				continue
			}
			idx := int(DecodeArgBx(body[i]))
			if idx < 0 || idx >= len(callee.FreeVars) || idx >= len(fn.Closure) {
				return 0, false
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
					return 0, false
				}
				newConsts = append(newConsts, inner)
				found = len(newConsts) - 1
			}
			if found > 0xFFFF {
				return 0, false
			}
			body[i] = Encode(OP_LOAD_CONST, 0, uint16(found))
		}
	} else {
		for _, inst := range body {
			if DecodeOpcode(inst) == OP_LOAD_DEREF {
				return 0, false
			}
		}
	}
	savedSlotOffset := max(totalArgs, callee.NumLocals)
	if savedSlotOffset > 0x7F {
		return 0, false
	}
	zeroFrom := uint8(totalArgs)
	if savedSlotOffset <= totalArgs || inlineLocalsDefinitelyAssigned(body, start, totalArgs, callee.NumLocals) {
		zeroFrom = uint8(savedSlotOffset)
	}
	argA := uint8(savedSlotOffset & 0x7F)
	if popCallable != 0 {
		argA |= 0x80
	}
	oldActive := cache.active
	if guardIP >= len(oldActive) {
		return 0, false
	}
	bodyStart := len(oldActive)

	// Expand every return into OP_INLINE_RETURN (prepending LOAD_FAST /
	// LOAD_CONST for fused returns) while recording oldToNew for jump relocation
	// and ipMap for exception traceback mapping.
	expanded := make([]uint32, 0, bodyLen+8)
	ipMap := make([]uint16, 0, bodyLen+8)
	oldToNew := make([]int, bodyLen+1)
	for i, inst := range body {
		oldToNew[i] = len(expanded)
		origIP := uint16(start + i)
		switch DecodeOpcode(inst) {
		case OP_RETURN:
			expanded = append(expanded, Encode(OP_INLINE_RETURN, argA, uint16(postCall)))
			ipMap = append(ipMap, origIP)
		case OP_RETURN_FAST:
			expanded = append(expanded,
				Encode(OP_LOAD_FAST, 0, DecodeArgBx(inst)),
				Encode(OP_INLINE_RETURN, argA, uint16(postCall)),
			)
			ipMap = append(ipMap, origIP, origIP)
		case OP_RETURN_CONST:
			expanded = append(expanded,
				Encode(OP_LOAD_CONST, 0, DecodeArgBx(inst)),
				Encode(OP_INLINE_RETURN, argA, uint16(postCall)),
			)
			ipMap = append(ipMap, origIP, origIP)
		default:
			expanded = append(expanded, inst)
			ipMap = append(ipMap, origIP)
		}
	}
	oldToNew[bodyLen] = len(expanded)
	expandedLen := len(expanded)
	if expandedLen > 255 {
		return 0, false
	}

	// Relocate all internal forward jump targets to the spliced tail.
	for i := range body {
		newIdx := oldToNew[i]
		inst := expanded[newIdx]
		op := DecodeOpcode(inst)
		switch op {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE:
			origTarget := int(DecodeArgBx(inst))
			relOrig := origTarget - start
			if relOrig <= i || relOrig > bodyLen {
				return 0, false
			}
			var newTarget int
			if relOrig == bodyLen {
				newTarget = postCall
			} else {
				newTarget = bodyStart + oldToNew[relOrig]
			}
			if newTarget < 0 || newTarget > 0xFFFF {
				return 0, false
			}
			expanded[newIdx] = Encode(op, DecodeArgA(inst), uint16(newTarget))
		case OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE:
			origRel := int(DecodeArgC(inst))
			relOrig := i + origRel
			if origRel <= 0 || relOrig <= i || relOrig >= bodyLen {
				return 0, false
			}
			newRel := oldToNew[relOrig] - newIdx
			if newRel <= 0 || newRel > 0xFF {
				return 0, false
			}
			expanded[newIdx] = EncodeABC(op, DecodeArgA(inst), DecodeArgB(inst), uint8(newRel))
		}
	}

	stubStart := bodyStart + expandedLen
	newActive := make([]uint32, 0, len(oldActive)+expandedLen+2)
	newActive = append(newActive, oldActive...)
	newActive = append(newActive, expanded...)
	newActive = append(newActive, origCall, Encode(OP_JUMP, 0, uint16(postCall)))
	if bodyStart > 0xFFFF || stubStart > 0xFFFF || postCall > 0xFFFF || postCall < 0 {
		return 0, false
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
	if len(cache.attrQueryPICExt) != 0 && len(cache.attrQueryPICExt) < needIC {
		n := make([][4]AttrQueryPICEntry, needIC)
		copy(n, cache.attrQueryPICExt)
		cache.attrQueryPICExt = n
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
	cache.inlineSites = append(cache.inlineSites, inlineSite{
		GuardIP:   guardIP,
		BodyStart: bodyStart,
		StubStart: stubStart,
		SavedSlot: savedSlotOffset,
		Callee:    callee,
		Fn:        fn,
		IPMap:     ipMap,
	})
	// Swap the call site to a guard. ArgA carries expanded body length so the
	// miss path derives the stub without extra state.
	cache.active[guardIP] = Encode(OP_GUARD_INLINE_METHOD, uint8(expandedLen), uint16(bodyStart))
	if guardOp == OP_GUARD_INLINE_FUNC {
		cache.active[guardIP] = Encode(OP_GUARD_INLINE_FUNC, uint8(expandedLen), uint16(bodyStart))
	}
	st.codePtr = cache.codePtr
	st.codeLen = cache.codeLen
	st.consts = newConsts
	return zeroFrom, true
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
		totalArgs = int(exp.TotalArgs)
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
		stubOp := DecodeOpcode(cache.active[stubStart])
		if stubOp == OP_COMPARE_DUNDER || stubOp == OP_COMPARE_OP || stubOp == OP_COMPARE_LT {
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
		// Restore the original instruction at ip so polymorphic sites revert
		// to their native PIC instead of paying guard-miss overhead repeatedly.
		if exp.IsFastAttrProp {
			cache.active[ip] = Encode(OP_NOP, 0, 0)
		} else {
			cache.active[ip] = cache.active[stubStart]
		}
		return stubStart, sp, ctlNext
	}
	newBase := sp - totalArgs
	if newBase < 0 {
		return stubStart, sp, ctlNext
	}
	savedSlotOffset := max(int(exp.SavedSlot), totalArgs)
	// Reserve callee locals plus one slot for the saved caller base above them.
	need := newBase + savedSlotOffset + 1
	if need > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, need)
	}
	zeroFrom := max(int(exp.ZeroFrom), totalArgs)
	for i := newBase + zeroFrom; i < newBase+savedSlotOffset; i++ {
		st.stack[i] = val.NoneVal()
	}
	// Stash the caller base above all callee locals as an int-carrying value cell.
	st.stack[newBase+savedSlotOffset] = val.Int64Val(int64(st.base))
	sp = newBase + savedSlotOffset + 1
	st.base = newBase
	if cache.inlineConsts != nil {
		st.consts = cache.inlineConsts
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
	case OP_CALL:
		if uint16(stubCall) == CallFromStack {
			return int(uint8(stubCall >> 16))
		}
	case OP_COMPARE_DUNDER, OP_COMPARE_OP, OP_COMPARE_LT:
		return 2
	case OP_LOAD_ATTR:
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
