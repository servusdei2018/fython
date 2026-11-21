package vm

// lower compiles a single stack instruction at ip. It returns false when the
// opcode has no register lowering, failing the whole compilation closed to
// the stack loop. Transfers terminate the current block; the caller advances
// ip and ends the block.
func (b *regBuilder) lower(op Opcode, inst uint32, ip int) bool {
	switch op {
	case OP_NOP:
		return true
	case OP_LOAD_CONST:
		c := int(DecodeArgBx(inst))
		if c < 0 || c >= len(b.co.Consts) {
			return false
		}
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		b.emit(RegLoadConst, d, 0, 0, uint32(c), ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		return b.push(d)
	case OP_LOAD_FAST:
		x := int(DecodeArgBx(inst))
		if x < 0 || x >= b.scratchBase {
			return false
		}
		return b.push(x)
	case OP_STORE_FAST:
		x := int(DecodeArgBx(inst))
		if x < 0 || x >= b.scratchBase || len(b.vstack) == 0 {
			return false
		}
		r := b.vstack[len(b.vstack)-1]
		coalesced := false
		if b.isScratch(int(r)) && b.refcnt[r] == 1 {
			if p := b.producer[r]; p >= 0 && b.prodBlock[r] == b.blockID {
				def := b.code[p]
				if b.singleDef(DecodeRegOp(def)) {
					b.code[p] = EncodeReg(DecodeRegOp(def), uint8(x), DecodeRegSrc1(def), DecodeRegSrc2(def), DecodeRegAux(def))
					coalesced = true
				}
			}
		}
		b.pop()
		if b.failed {
			return false
		}
		if !coalesced {
			b.emit(RegMov, x, int(r), 0, 0, ip)
		}
		return true
	case OP_LOAD_GLOBAL:
		c := int(DecodeArgBx(inst))
		if c < 0 || c >= len(b.co.Consts) {
			return false
		}
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		b.emit(RegLoadGlobal, d, 0, 0, uint32(c), ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		return b.push(d)
	case OP_STORE_GLOBAL:
		c := int(DecodeArgBx(inst))
		if c < 0 || c >= len(b.co.Consts) {
			return false
		}
		v := b.pop()
		if b.failed {
			return false
		}
		w, ok := b.allocWindow(1)
		if !ok {
			return false
		}
		b.emit(RegMov, w, v, 0, 0, ip)
		b.emit(RegStoreGlobal, w, 0, 0, uint32(c), ip)
		b.freeWindow(w, 1)
		return true
	case OP_LOAD_ATTR, OP_STORE_ATTR:
		return b.lowerAttr(op, inst, ip)
	case OP_LOAD_METHOD:
		return b.lowerLoadMethod(inst, ip)
	case OP_CALL_METHOD:
		return b.lowerCallMethod(inst, ip)
	case OP_CALL:
		return b.lowerCall(inst, ip)
	case OP_CALL_KW:
		return b.lowerCallKw(inst, ip)
	case OP_CALL_FUNCTION_EX:
		return b.lowerCallFunctionEx(inst, ip)
	case OP_BINARY_ADD:
		return b.lowerBinary(RegAdd, ip)
	case OP_BINARY_SUB:
		return b.lowerBinary(RegSub, ip)
	case OP_BINARY_MUL:
		return b.lowerBinary(RegMul, ip)
	case OP_BINARY_TRUE_DIV:
		return b.lowerBinary(RegTrueDiv, ip)
	case OP_BINARY_FLOOR_DIV:
		return b.lowerBinary(RegFloorDiv, ip)
	case OP_BINARY_MOD:
		return b.lowerBinary(RegMod, ip)
	case OP_BINARY_POW:
		return b.lowerBinary(RegPow, ip)
	case OP_BINARY_MATMUL:
		return b.lowerBinary(RegMatMul, ip)
	case OP_BINARY_AND:
		return b.lowerBinary(RegBitAnd, ip)
	case OP_BINARY_OR:
		return b.lowerBinary(RegBitOr, ip)
	case OP_BINARY_XOR:
		return b.lowerBinary(RegBitXor, ip)
	case OP_BINARY_LSHIFT:
		return b.lowerBinary(RegLShift, ip)
	case OP_BINARY_RSHIFT:
		return b.lowerBinary(RegRShift, ip)
	case OP_BINARY_SUBSCR:
		return b.lowerBinary(RegGetItem, ip)
	case OP_STORE_SUBSCR:
		// Helper layout is [value, obj, key]: opStoreSubscr reads
		// val=stack[sp-3], obj=stack[sp-2], key=stack[sp-1].
		vals, ok := b.peekWindow(3)
		if !ok {
			return false
		}
		// Bottom-to-top: [val, obj, key].
		v, o, k := vals[0], vals[1], vals[2]
		w, ok := b.allocWindow(3)
		if !ok {
			return false
		}
		b.emit(RegMov, w, v, 0, 0, ip)
		b.emit(RegMov, w+1, o, 0, 0, ip)
		b.emit(RegMov, w+2, k, 0, 0, ip)
		b.emit(RegSetItem, w, w+1, w+2, 0, ip)
		if !b.drop(3) {
			return false
		}
		b.freeWindow(w, 3)
		return true
	case OP_BUILD_SLICE:
		count := int(DecodeArgBx(inst))
		if count != 2 && count != 3 {
			return false
		}
		parts, ok := b.peekWindow(count)
		if !ok {
			return false
		}
		w, ok := b.allocWindow(count)
		if !ok {
			return false
		}
		for i := range count {
			b.emit(RegMov, w+i, parts[i], 0, 0, ip)
		}
		b.emit(RegBuildSlice, w, w, 0, uint32(count), ip)
		b.producer[w] = int32(len(b.code) - 1)
		b.prodBlock[w] = b.blockID
		if !b.drop(count) {
			return false
		}
		if !b.push(w) {
			return false
		}
		b.freeWindow(w+1, count-1)
		return true
	case OP_BINARY_SLICE:
		// Slicing a container: [container, start, stop]. Bit 15 of aux
		// marks the binary form for the runtime.
		parts, ok := b.peekWindow(3)
		if !ok {
			return false
		}
		cont, start, stop := parts[0], parts[1], parts[2]
		w, ok := b.allocWindow(3)
		if !ok {
			return false
		}
		b.emit(RegMov, w, cont, 0, 0, ip)
		b.emit(RegMov, w+1, start, 0, 0, ip)
		b.emit(RegMov, w+2, stop, 0, 0, ip)
		b.emit(RegBuildSlice, w, w, 0, 0x8000, ip)
		b.producer[w] = int32(len(b.code) - 1)
		b.prodBlock[w] = b.blockID
		if !b.drop(3) {
			return false
		}
		if !b.push(w) {
			return false
		}
		b.freeWindow(w+1, 2)
		return true
	case OP_UNARY_NEGATIVE:
		return b.lowerUnary(RegNeg, ip)
	case OP_UNARY_POSITIVE:
		return b.lowerUnary(RegPos, ip)
	case OP_UNARY_INVERT:
		return b.lowerUnary(RegInvert, ip)
	case OP_UNARY_NOT:
		return b.lowerUnary(RegNot, ip)
	case OP_COMPARE_OP:
		return b.lowerCompare(inst, ip, false)
	case OP_COMPARE_LT:
		return b.lowerCompare(inst, ip, true)
	case OP_JUMP:
		t := int(DecodeArgBx(inst))
		if !b.edgeTo(ip, t, false) {
			return false
		}
		b.jumps = append(b.jumps, regFixup{pos: len(b.code), target: t})
		b.emit(RegJmp, 0, 0, 0, 0, ip)
		return true
	case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE:
		t := int(DecodeArgBx(inst))
		c := b.pop()
		if b.failed {
			return false
		}
		// The condition stays live until the jump is emitted: edgeTo may
		// record the target entry here, and its local normalization must
		// not recycle the condition register mid-shuffle.
		b.hold(c)
		if !b.edgeTo(ip, t, true) {
			b.unhold(c)
			return false
		}
		trampIdx := len(b.trampolines) - 1
		rop := RegJmpIfFalse
		if op == OP_JUMP_IF_TRUE {
			rop = RegJmpIfTrue
		}
		b.trampFixups = append(b.trampFixups, regTrampFixup{pos: len(b.code), trampIdx: trampIdx})
		b.emit(rop, 0, c, 0, 0, ip)
		b.unhold(c)
		return true
	case OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP:
		t := int(DecodeArgBx(inst))
		if len(b.vstack) == 0 {
			return false
		}
		// The jump edge keeps the condition; the fallthrough pops it.
		have := append([]uint8(nil), b.vstack...)
		if !b.edgeToHave(ip, t, true, have) {
			return false
		}
		c := b.pop()
		if b.failed {
			return false
		}
		trampIdx := len(b.trampolines) - 1
		rop := RegJmpIfFalse
		if op == OP_JUMP_IF_TRUE_OR_POP {
			rop = RegJmpIfTrue
		}
		b.trampFixups = append(b.trampFixups, regTrampFixup{pos: len(b.code), trampIdx: trampIdx})
		b.emit(rop, 0, c, 0, 0, ip)
		return true
	case OP_POP_TOP:
		r := b.pop()
		if b.failed {
			return false
		}
		b.emit(RegPopTop, 0, r, 0, 0, ip)
		return true
	case OP_DUP_TOP:
		if len(b.vstack) == 0 {
			return false
		}
		r := b.vstack[len(b.vstack)-1]
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		b.emit(RegDupTop, d, int(r), 0, 0, ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		return b.push(d)
	case OP_GET_ITER:
		o := b.pop()
		if b.failed {
			return false
		}
		w, ok := b.allocWindow(1)
		if !ok {
			return false
		}
		b.emit(RegMov, w, o, 0, 0, ip)
		b.emit(RegGetIter, w, w, 0, 0, ip)
		b.producer[w] = int32(len(b.code) - 1)
		b.prodBlock[w] = b.blockID
		return b.push(w)
	case OP_FOR_ITER:
		t := int(DecodeArgBx(inst))
		if len(b.vstack) == 0 {
			return false
		}
		it := int(b.vstack[len(b.vstack)-1])
		have := append([]uint8(nil), b.vstack[:len(b.vstack)-1]...)
		if !b.edgeToHave(ip, t, true, have) {
			return false
		}
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		trampIdx := len(b.trampolines) - 1
		b.trampFixups = append(b.trampFixups, regTrampFixup{pos: len(b.code), trampIdx: trampIdx})
		b.emit(RegForIter, d, it, 0, 0, ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		return b.push(d)
	case OP_FOR_ITER_RANGE:
		t := int(DecodeArgBx(inst))
		targetLocal := int(uint8(inst >> 16))
		if targetLocal < 0 || targetLocal >= b.scratchBase {
			return false
		}
		if len(b.vstack) == 0 {
			return false
		}
		it := int(b.vstack[len(b.vstack)-1])
		if !b.edgeTo(ip, t, true) {
			return false
		}
		trampIdx := len(b.trampolines) - 1
		b.trampFixups = append(b.trampFixups, regTrampFixup{pos: len(b.code), trampIdx: trampIdx})
		b.emit(RegForIterRange, targetLocal, it, 0, 0, ip)
		return true
	case OP_SETUP_RANGE:
		count := int(DecodeArgBx(inst))
		if count != 1 && count != 2 {
			return false
		}
		var s1, s2 int
		if count == 2 {
			s2 = b.pop()
			s1 = b.pop()
		} else {
			s1 = b.pop()
		}
		if b.failed {
			return false
		}
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		b.emit(RegSetupRange, d, s1, s2, uint32(count), ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		return b.push(d)
	case OP_BUILD_LIST:
		return b.lowerBuild(RegBuildList, int(DecodeArgBx(inst)), ip, 1)
	case OP_BUILD_TUPLE:
		return b.lowerBuild(RegBuildTuple, int(DecodeArgBx(inst)), ip, 1)
	case OP_BUILD_SET:
		return b.lowerBuild(RegBuildSet, int(DecodeArgBx(inst)), ip, 1)
	case OP_BUILD_MAP:
		return b.lowerBuild(RegBuildDict, int(DecodeArgBx(inst)), ip, 2)
	case OP_BUILD_STRING:
		return b.lowerBuild(RegBuildString, int(DecodeArgBx(inst)), ip, 1)
	case OP_LIST_APPEND:
		return b.lowerAppend(RegListAppend, int(DecodeArgBx(inst)), ip, 1)
	case OP_MAP_ADD:
		return b.lowerAppend(RegMapAdd, int(DecodeArgBx(inst)), ip, 2)
	case OP_SET_ADD:
		return b.lowerAppend(RegSetAdd, int(DecodeArgBx(inst)), ip, 1)
	case OP_LIST_EXTEND:
		return b.lowerAppend(RegListExtend, int(DecodeArgBx(inst)), ip, 1)
	case OP_UNPACK_SEQUENCE:
		count := int(DecodeArgBx(inst))
		if count <= 0 || count >= regMaxRegs {
			return false
		}
		seq := b.pop()
		if b.failed {
			return false
		}
		w, ok := b.allocWindow(count)
		if !ok {
			return false
		}
		b.emit(RegMov, w, seq, 0, 0, ip)
		b.emit(RegUnpack, w, w, 0, uint32(count), ip)
		for i := range count {
			if !b.push(w + i) {
				return false
			}
		}
		return true
	case OP_FORMAT_VALUE:
		conv := uint8(inst >> 16)
		hasSpec := uint16(inst) != 0
		nv := 1
		if hasSpec {
			nv = 2
		}
		vals, ok := b.peekWindow(nv)
		if !ok {
			return false
		}
		// Bottom-to-top: [value] or [value, spec].
		v := vals[0]
		var spec int
		if hasSpec {
			spec = vals[1]
		}
		w, ok := b.allocWindow(2)
		if !ok {
			return false
		}
		b.emit(RegMov, w, v, 0, 0, ip)
		aux := uint32(conv)
		s2 := 0
		if hasSpec {
			b.emit(RegMov, w+1, spec, 0, 0, ip)
			aux |= 1 << 8
			s2 = w + 1
		}
		b.emit(RegFormatValue, w, w, s2, aux, ip)
		b.producer[w] = int32(len(b.code) - 1)
		b.prodBlock[w] = b.blockID
		if !b.drop(nv) {
			return false
		}
		if !b.push(w) {
			return false
		}
		b.freeWindow(w+1, 1)
		return true
	case OP_MAKE_FUNCTION:
		return b.lowerMakeFunction(inst, ip)
	case OP_RETURN:
		v := b.pop()
		if b.failed {
			return false
		}
		var bits [4]uint64
		mark := func(r int) {
			if b.isScratch(r) {
				bits[r/64] |= 1 << (uint(r) % 64)
			}
		}
		mark(v)
		for _, r := range b.vstack {
			mark(int(r))
		}
		b.retLive = append(b.retLive, regRetLive{pos: len(b.code), bits: bits})
		b.emit(RegReturn, 0, v, 0, 0, ip)
		return true
	case OP_RAISE:
		argc := int(DecodeArgA(inst))
		if argc < 0 || argc > 2 {
			return false
		}
		var e, c int
		if argc > 0 {
			vals, ok := b.peekWindow(argc)
			if !ok {
				return false
			}
			// Bottom-to-top: [exc] or [exc, cause].
			e = vals[0]
			if argc == 2 {
				c = vals[1]
			}
		}
		w, w2 := 0, 0
		if argc > 0 {
			var ok bool
			w, ok = b.allocWindow(argc)
			if !ok {
				return false
			}
			b.emit(RegMov, w, e, 0, 0, ip)
			w2 = w
			if argc == 2 {
				b.emit(RegMov, w+1, c, 0, 0, ip)
				w2 = w + 1
			}
		}
		b.emit(RegRaise, 0, w, w2, uint32(argc), ip)
		if argc > 0 {
			if !b.drop(argc) {
				return false
			}
			b.freeWindow(w, argc)
		}
		return true
	default:
		return false
	}
}

// edgeToHave records a conditional transfer against an explicit abstract
// stack, used when the jump edge observes a different stack than the
// fallthrough (OR_POP, FOR_ITER).
func (b *regBuilder) edgeToHave(ip, target int, conditional bool, have []uint8) bool {
	if b.failed {
		return false
	}
	saved := b.vstack
	b.vstack = have
	ok := b.edgeTo(ip, target, conditional)
	b.vstack = saved
	return ok && !b.failed
}

func (b *regBuilder) lowerBinary(op RegOp, ip int) bool {
	rhs := b.pop()
	lhs := b.pop()
	if b.failed {
		return false
	}
	d := b.allocScratch()
	if d < 0 {
		return false
	}
	b.emit(op, d, lhs, rhs, 0, ip)
	b.producer[d] = int32(len(b.code) - 1)
	b.prodBlock[d] = b.blockID
	return b.push(d)
}

func (b *regBuilder) lowerUnary(op RegOp, ip int) bool {
	a := b.pop()
	if b.failed {
		return false
	}
	d := b.allocScratch()
	if d < 0 {
		return false
	}
	b.emit(op, d, a, 0, 0, ip)
	b.producer[d] = int32(len(b.code) - 1)
	b.prodBlock[d] = b.blockID
	return b.push(d)
}

func (b *regBuilder) lowerCompare(inst uint32, ip int, isLT bool) bool {
	cmpOp := CMP_LT
	if !isLT {
		cmpOp = DecodeArgA(inst)
	}
	rhs := b.pop()
	lhs := b.pop()
	if b.failed {
		return false
	}
	// Fuse compare-and-branch when the next instruction is a plain
	// conditional jump that no other edge targets.
	if ip+1 < len(b.co.Code) && !b.targeted[ip+1] {
		next := b.co.Code[ip+1]
		nop := DecodeOpcode(next)
		if nop == OP_JUMP_IF_FALSE || nop == OP_JUMP_IF_TRUE {
			t := int(DecodeArgBx(next))
			wantTrue := nop == OP_JUMP_IF_TRUE
			var fused RegOp
			fa, fb := lhs, rhs
			switch {
			case cmpOp == CMP_LT && !wantTrue:
				fused = RegLtJmpIfFalse
			case cmpOp == CMP_LT && wantTrue:
				fused = RegLtJmpIfTrue
			case cmpOp == CMP_EQ && !wantTrue:
				fused = RegEqJmpIfFalse
			case cmpOp == CMP_EQ && wantTrue:
				fused = RegEqJmpIfTrue
			case cmpOp == CMP_IS && b.isNoneConst(rhs):
				fused = RegIsNoneJmpIfFalse
				if wantTrue {
					fused = RegIsNoneJmpIfTrue
				}
				fa, fb = lhs, 0
			case cmpOp == CMP_IS && b.isNoneConst(lhs):
				fused = RegIsNoneJmpIfFalse
				if wantTrue {
					fused = RegIsNoneJmpIfTrue
				}
				fa, fb = rhs, 0
			}
			if fused != RegInvalid {
				// Skip the fused jump instruction. The popped sources stay
				// live until the fused jump is emitted: edgeTo may record
				// the target entry here, and its local normalization must
				// not recycle a source register mid-shuffle.
				b.hold(lhs)
				b.hold(rhs)
				if !b.edgeTo(ip, t, true) {
					b.unhold(lhs)
					b.unhold(rhs)
					return false
				}
				trampIdx := len(b.trampolines) - 1
				b.trampFixups = append(b.trampFixups, regTrampFixup{pos: len(b.code), trampIdx: trampIdx})
				b.emit(fused, 0, fa, fb, 0, ip)
				b.unhold(lhs)
				b.unhold(rhs)
				b.fusedJump = true
				return true
			}
		}
	}
	emitCmp := func(op RegOp, aux uint32) bool {
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		b.emit(op, d, lhs, rhs, aux, ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		return b.push(d)
	}
	switch cmpOp {
	case CMP_LT:
		return emitCmp(RegLt, 0)
	case CMP_LTE:
		return emitCmp(RegLe, 0)
	case CMP_EQ:
		return emitCmp(RegEq, 0)
	case CMP_NEQ:
		return emitCmp(RegNe, 0)
	case CMP_GT:
		return emitCmp(RegGt, 0)
	case CMP_GTE:
		return emitCmp(RegGe, 0)
	case CMP_IN:
		return emitCmp(RegIn, 0)
	case CMP_NOT_IN:
		t := b.allocScratch()
		if t < 0 {
			return false
		}
		b.emit(RegIn, t, lhs, rhs, 0, ip)
		d := b.allocScratch()
		if d < 0 {
			return false
		}
		b.emit(RegNot, d, t, 0, 0, ip)
		b.producer[d] = int32(len(b.code) - 1)
		b.prodBlock[d] = b.blockID
		b.release(t)
		return b.push(d)
	case CMP_IS:
		return emitCmp(RegIs, uint32(CMP_IS))
	case CMP_IS_NOT:
		return emitCmp(RegIs, uint32(CMP_IS_NOT))
	default:
		return false
	}
}

// isNoneConst reports whether r was defined by loading a None constant.
func (b *regBuilder) isNoneConst(r int) bool {
	if !b.isScratch(r) {
		return false
	}
	p := b.producer[r]
	if p < 0 || int(p) >= len(b.code) {
		return false
	}
	def := b.code[p]
	if DecodeRegOp(def) != RegLoadConst {
		return false
	}
	c := int(DecodeRegAux(def))
	if c < 0 || c >= len(b.co.Consts) {
		return false
	}
	return b.co.Consts[c].IsNone()
}

func (b *regBuilder) lowerAttr(op Opcode, inst uint32, ip int) bool {
	nameIdx := int(DecodeArgBx(inst))
	if nameIdx < 0 || nameIdx >= len(b.co.Consts) {
		return false
	}
	if op == OP_LOAD_ATTR {
		o := b.pop()
		if b.failed {
			return false
		}
		w, ok := b.allocWindow(1)
		if !ok {
			return false
		}
		b.emit(RegMov, w, o, 0, 0, ip)
		b.emit(RegLoadAttr, w, w, 0, uint32(nameIdx), ip)
		b.producer[w] = int32(len(b.code) - 1)
		b.prodBlock[w] = b.blockID
		return b.push(w)
	}
	// STORE_ATTR stack order is value-below, object-on-top.
	vals, ok := b.peekWindow(2)
	if !ok {
		return false
	}
	// Bottom-to-top: [val, obj].
	v, o := vals[0], vals[1]
	w, ok := b.allocWindow(2)
	if !ok {
		return false
	}
	b.emit(RegMov, w, o, 0, 0, ip)
	b.emit(RegMov, w+1, v, 0, 0, ip)
	b.emit(RegStoreAttr, w, w+1, 0, uint32(nameIdx), ip)
	if !b.drop(2) {
		return false
	}
	b.freeWindow(w, 2)
	return true
}

func (b *regBuilder) lowerLoadMethod(inst uint32, ip int) bool {
	nameIdx := int(DecodeArgBx(inst))
	if nameIdx < 0 || nameIdx >= len(b.co.Consts) {
		return false
	}
	o := b.pop()
	if b.failed {
		return false
	}
	w, ok := b.allocWindow(2)
	if !ok {
		return false
	}
	b.emit(RegMov, w, o, 0, 0, ip)
	b.emit(RegLoadMethod, w, w+1, 0, uint32(nameIdx), ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.push(w) {
		return false
	}
	return b.push(w + 1)
}

func (b *regBuilder) lowerCallMethod(inst uint32, ip int) bool {
	argc := int(DecodeArgA(inst))
	if argc < 0 || argc > 250 {
		return false
	}
	// Snapshot the window sources before allocating: allocation must not
	// recycle a live source into the window it is shuffled into.
	vals, ok := b.peekWindow(argc + 2)
	if !ok {
		return false
	}
	// Layout bottom-to-top: [recv, meth, arg0 .. argN-1].
	recv, meth := vals[0], vals[1]
	args := vals[2:]
	w, ok := b.allocWindow(argc + 2)
	if !ok {
		return false
	}
	b.emit(RegMov, w, recv, 0, 0, ip)
	b.emit(RegMov, w+1, meth, 0, 0, ip)
	for i := range argc {
		b.emit(RegMov, w+2+i, args[i], 0, 0, ip)
	}
	if !b.drop(argc + 2) {
		return false
	}
	b.emit(RegCallMethod, w, w+1, w+2, uint32(argc), ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.push(w) {
		return false
	}
	b.freeWindow(w+1, argc+1)
	return true
}

func (b *regBuilder) lowerCall(inst uint32, ip int) bool {
	argc := int(DecodeArgA(inst))
	bx := int(DecodeArgBx(inst))
	if argc < 0 || argc > 250 {
		return false
	}
	if bx < len(b.co.Consts) && isCallableConst(b.co.Consts[bx]) {
		// Constant callee: load it into the window head, which doubles
		// as the result slot.
		vals, ok := b.peekWindow(argc)
		if !ok {
			return false
		}
		w, ok := b.allocWindow(argc + 1)
		if !ok {
			return false
		}
		b.emit(RegLoadConst, w, 0, 0, uint32(bx), ip)
		for i := range argc {
			b.emit(RegMov, w+1+i, vals[i], 0, 0, ip)
		}
		b.emit(RegCall, w, w, w+1, uint32(argc), ip)
		b.producer[w] = int32(len(b.code) - 1)
		b.prodBlock[w] = b.blockID
		if !b.drop(argc) {
			return false
		}
		if !b.push(w) {
			return false
		}
		b.freeWindow(w+1, argc)
		return true
	}
	vals, ok := b.peekWindow(argc + 1)
	if !ok {
		return false
	}
	// Layout bottom-to-top: [callee, arg0 .. argN-1].
	callee := vals[0]
	args := vals[1:]
	w, ok := b.allocWindow(argc + 1)
	if !ok {
		return false
	}
	b.emit(RegMov, w, callee, 0, 0, ip)
	for i := range argc {
		b.emit(RegMov, w+1+i, args[i], 0, 0, ip)
	}
	b.emit(RegCall, w, w, w+1, uint32(argc), ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.drop(argc + 1) {
		return false
	}
	if !b.push(w) {
		return false
	}
	b.freeWindow(w+1, argc)
	return true
}

func (b *regBuilder) lowerCallKw(inst uint32, ip int) bool {
	total := int(DecodeArgA(inst))
	kwIdx := int(DecodeArgBx(inst))
	if total < 0 || total > 250 || kwIdx < 0 || kwIdx >= len(b.co.Consts) {
		return false
	}
	if !b.co.Consts[kwIdx].IsTuple() {
		return false
	}
	vals, ok := b.peekWindow(total + 1)
	if !ok {
		return false
	}
	// Layout bottom-to-top: [callee, arg0 .. argN-1].
	callee := vals[0]
	args := vals[1:]
	w, ok := b.allocWindow(total + 1)
	if !ok {
		return false
	}
	b.emit(RegMov, w, callee, 0, 0, ip)
	for i := range total {
		b.emit(RegMov, w+1+i, args[i], 0, 0, ip)
	}
	aux := uint32(total) | uint32(kwIdx)<<16
	b.emit(RegCallKw, w, w, w+1, aux, ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.drop(total + 1) {
		return false
	}
	if !b.push(w) {
		return false
	}
	b.freeWindow(w+1, total)
	return true
}

func (b *regBuilder) lowerCallFunctionEx(inst uint32, ip int) bool {
	hasKW := DecodeArgA(inst)&1 != 0
	size := 2
	if hasKW {
		size = 3
	}
	vals, ok := b.peekWindow(size)
	if !ok {
		return false
	}
	// Layout bottom-to-top: [callee, args, kwdict?].
	callee, argsv := vals[0], vals[1]
	var kwd int
	if hasKW {
		kwd = vals[2]
	}
	w, ok := b.allocWindow(size)
	if !ok {
		return false
	}
	b.emit(RegMov, w, callee, 0, 0, ip)
	b.emit(RegMov, w+1, argsv, 0, 0, ip)
	aux := uint32(0)
	if hasKW {
		b.emit(RegMov, w+2, kwd, 0, 0, ip)
		aux = 1 | uint32(w+2)<<8
	}
	b.emit(RegCallFunctionEx, w, w, w+1, aux, ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.drop(size) {
		return false
	}
	if !b.push(w) {
		return false
	}
	b.freeWindow(w+1, size-1)
	return true
}

// lowerBuild compiles container displays. Each popped value is shuffled
// into a consecutive window whose head is the result slot.
func (b *regBuilder) lowerBuild(op RegOp, count, ip, width int) bool {
	total := count * width
	if count < 0 || total <= 0 || total > 250 {
		// Empty displays still need a result slot.
		if total == 0 && (op == RegBuildList || op == RegBuildTuple || op == RegBuildSet || op == RegBuildString || (op == RegBuildDict && count == 0)) {
			w, ok := b.allocWindow(1)
			if !ok {
				return false
			}
			b.emit(op, w, w, 0, uint32(count), ip)
			b.producer[w] = int32(len(b.code) - 1)
			b.prodBlock[w] = b.blockID
			return b.push(w)
		}
		return false
	}
	vals, ok := b.peekWindow(total)
	if !ok {
		return false
	}
	w, ok := b.allocWindow(total)
	if !ok {
		return false
	}
	for i := range total {
		b.emit(RegMov, w+i, vals[i], 0, 0, ip)
	}
	b.emit(op, w, w, 0, uint32(count), ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.drop(total) {
		return false
	}
	if !b.push(w) {
		return false
	}
	b.freeWindow(w+1, total-1)
	return true
}

// lowerAppend compiles in-place container updates. The container stays live
// in the abstract stack; only the popped values move into the window.
func (b *regBuilder) lowerAppend(op RegOp, depth, ip, width int) bool {
	if depth < 1 || depth > 250 {
		return false
	}
	if len(b.vstack) < depth+width {
		return false
	}
	// Snapshot the window values before allocating so a reused window can
	// never alias a live shuffle source.
	vals, ok := b.peekWindow(width)
	if !ok {
		return false
	}
	cont := int(b.vstack[len(b.vstack)-width-depth])
	// Window layout mirrors the stack helper's view: the container at the
	// head, values at head+depth. Middle slots are never read.
	size := depth + width
	w, ok := b.allocWindow(size)
	if !ok {
		return false
	}
	b.emit(RegMov, w, cont, 0, 0, ip)
	if width == 2 {
		b.emit(RegMov, w+depth, vals[0], 0, 0, ip)
		b.emit(RegMov, w+depth+1, vals[1], 0, 0, ip)
		b.emit(op, w, w+depth, w+depth+1, uint32(depth), ip)
	} else {
		b.emit(RegMov, w+depth, vals[0], 0, 0, ip)
		b.emit(op, w, w+depth, 0, uint32(depth), ip)
	}
	if !b.drop(width) {
		return false
	}
	b.freeWindow(w, size)
	return true
}

func (b *regBuilder) lowerMakeFunction(inst uint32, ip int) bool {
	flags := DecodeArgA(inst)
	count := 1
	for _, bit := range []uint8{0x01, 0x02, 0x04, 0x08} {
		if flags&bit != 0 {
			count++
		}
	}
	vals, ok := b.peekWindow(count)
	if !ok {
		return false
	}
	w, ok := b.allocWindow(count)
	if !ok {
		return false
	}
	for i := range count {
		b.emit(RegMov, w+i, vals[i], 0, 0, ip)
	}
	b.emit(RegMakeFunction, w, w, 0, uint32(flags), ip)
	b.producer[w] = int32(len(b.code) - 1)
	b.prodBlock[w] = b.blockID
	if !b.drop(count) {
		return false
	}
	if !b.push(w) {
		return false
	}
	b.freeWindow(w+1, count-1)
	return true
}
