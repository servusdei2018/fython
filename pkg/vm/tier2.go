package vm

import "sync/atomic"

// maybeOptimizeCodeObject atomically increments co.Hotness and triggers Tier-2 optimization
// once the hotness threshold (1,000) is exceeded.
func maybeOptimizeCodeObject(co *CodeObject) {
	if atomic.LoadInt32(&co.IsTier2Optimized) != 0 {
		return
	}
	if atomic.AddInt32(&co.Hotness, 1) >= 8 {
		OptimizeCodeObject(co)
	}
}

// OptimizeCodeObject scans the instruction stream of co and fuses common 3- and 4-instruction
// idioms into single Macro-Opcodes that execute in a single switch dispatch with register-passed
// values. Jump targets and exception boundaries are protected from cross-boundary fusion.
func OptimizeCodeObject(co *CodeObject) {
	if co == nil || !atomic.CompareAndSwapInt32(&co.IsTier2Optimized, 0, 1) {
		return
	}
	code := co.Code
	if len(code) < 4 {
		return
	}

	// Step 1: Build jump target and exception boundary map
	isJumpTarget := make([]bool, len(code)+1)
	for i := range code {
		inst := atomic.LoadUint32(&code[i])
		op := DecodeOpcode(inst)
		switch op {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP, OP_FOR_ITER:
			target := int(DecodeArgBx(inst))
			if target >= 0 && target < len(isJumpTarget) {
				isJumpTarget[target] = true
			}
		}
	}
	for _, e := range co.ExceptionTable {
		if e.TargetIP >= 0 && e.TargetIP < len(isJumpTarget) {
			isJumpTarget[e.TargetIP] = true
		}
		if e.StartIP >= 0 && e.StartIP < len(isJumpTarget) {
			isJumpTarget[e.StartIP] = true
		}
		if e.EndIP >= 0 && e.EndIP < len(isJumpTarget) {
			isJumpTarget[e.EndIP] = true
		}
	}

	// Step 2: Linear scan and pattern fusion
	for i := 0; i+3 < len(code); {
		// Never fuse across internal jump targets
		if isJumpTarget[i+1] || isJumpTarget[i+2] || isJumpTarget[i+3] {
			i++
			continue
		}

		w0 := atomic.LoadUint32(&code[i])
		w1 := atomic.LoadUint32(&code[i+1])
		w2 := atomic.LoadUint32(&code[i+2])
		w3 := atomic.LoadUint32(&code[i+3])

		op0 := DecodeOpcode(w0)
		op1 := DecodeOpcode(w1)
		op2 := DecodeOpcode(w2)
		op3 := DecodeOpcode(w3)

		if i+5 < len(code) && !isJumpTarget[i+4] && !isJumpTarget[i+5] {
			w4 := atomic.LoadUint32(&code[i+4])
			w5 := atomic.LoadUint32(&code[i+5])
			op4 := DecodeOpcode(w4)
			op5 := DecodeOpcode(w5)

			// Pattern 13: OP_FAST_SELF_RECURSE_INC_LOCAL disabled for isolation
			if false && op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST && op2 == OP_LOAD_CONST &&
				(op3 == OP_BINARY_ADD || op3 == OP_BINARY_ADD_INT) &&
				op4 == OP_CALL && DecodeArgA(w4) == 1 &&
				op5 == OP_POP_TOP {

				cellIdx := DecodeArgBx(w0)
				localReg := DecodeArgBx(w1)
				constIdx := DecodeArgBx(w2)

				if localReg < 256 {
					atomic.StoreUint32(&code[i], EncodeABC(OP_FAST_SELF_RECURSE_INC_LOCAL, uint8(localReg), 0, 0))
					atomic.StoreUint32(&code[i+1], Encode(OP_LOAD_DEREF, 0, cellIdx))
					atomic.StoreUint32(&code[i+2], Encode(OP_LOAD_CONST, 0, constIdx))
					atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+4], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+5], Encode(OP_NOP, 0, 0))
					i += 6
					continue
				}
			}

			// Pattern 14: OP_FAST_SUB_LOCAL_ADD_DEREF_TO_LOCAL
			// OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_SUB | OP_BINARY_SUB_INT) + OP_LOAD_DEREF + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_FAST
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
				(op2 == OP_BINARY_SUB || op2 == OP_BINARY_SUB_INT) &&
				op3 == OP_LOAD_DEREF &&
				(op4 == OP_BINARY_ADD || op4 == OP_BINARY_ADD_INT) &&
				op5 == OP_STORE_FAST {

				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				cellIdx := DecodeArgBx(w3)
				dst := DecodeArgBx(w5)

				if srcA < 256 && srcB < 256 && dst < 256 {
					atomic.StoreUint32(&code[i], EncodeABC(OP_FAST_SUB_LOCAL_ADD_DEREF_TO_LOCAL, uint8(dst), uint8(srcA), uint8(srcB)))
					atomic.StoreUint32(&code[i+1], Encode(OP_LOAD_DEREF, 0, cellIdx))
					atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+4], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+5], Encode(OP_NOP, 0, 0))
					i += 6
					continue
				}
			}
		}

		// Pattern 6B: OP_LOAD_FAST + OP_LOAD_DEREF + OP_COMPARE_OP (CMP_EQ) + OP_JUMP_IF_FALSE
		// -> OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_DEREF &&
			op2 == OP_COMPARE_OP && DecodeArgA(w2) == CMP_EQ &&
			op3 == OP_JUMP_IF_FALSE {

			srcA := DecodeArgBx(w0)
			cellIdx := DecodeArgBx(w1)
			exitTarget := DecodeArgBx(w3)

			if srcA < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE, uint8(srcA), exitTarget))
				atomic.StoreUint32(&code[i+1], Encode(OP_LOAD_DEREF, 0, cellIdx))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 3: OP_LOAD_FAST + OP_LOAD_CONST + (OP_COMPARE_LT | OP_COMPARE_LT_INT) + OP_JUMP_IF_FALSE
		// -> OP_FAST_LOOP_CMP_LT_CONST_JUMP
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_CONST &&
			(op2 == OP_COMPARE_LT || op2 == OP_COMPARE_LT_INT) &&
			op3 == OP_JUMP_IF_FALSE {

			reg := DecodeArgBx(w0)
			constIdx := DecodeArgBx(w1)
			exitTarget := DecodeArgBx(w3)

			if reg < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_LOOP_CMP_LT_CONST_JUMP, uint8(reg), exitTarget))
				atomic.StoreUint32(&code[i+1], Encode(OP_LOAD_CONST, 0, constIdx))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 2: OP_LOAD_FAST + OP_LOAD_CONST + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_FAST
		// -> OP_FAST_INC_INT_LOCAL_CONST (same register)
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_CONST &&
			(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
			op3 == OP_STORE_FAST &&
			DecodeArgBx(w0) == DecodeArgBx(w3) {

			reg := DecodeArgBx(w0)
			constIdx := DecodeArgBx(w1)

			if reg < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_INC_INT_LOCAL_CONST, uint8(reg), constIdx))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 4: OP_LOAD_DEREF + OP_LOAD_CONST + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_DEREF
		// -> OP_INC_DEREF_INT (when const is 1 and same cell)
		if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_CONST &&
			(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
			op3 == OP_STORE_DEREF &&
			DecodeArgBx(w0) == DecodeArgBx(w3) {

			cellIdx := DecodeArgBx(w0)
			constIdx := DecodeArgBx(w1)
			if int(constIdx) < len(co.Consts) && co.Consts[constIdx].IsInt64() && co.Consts[constIdx].AsInt64() == 1 {
				atomic.StoreUint32(&code[i], Encode(OP_INC_DEREF_INT, 0, cellIdx))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 1: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_FAST
		// -> OP_FAST_ADD_INT_LOCAL_TO_LOCAL
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
			(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
			op3 == OP_STORE_FAST {

			srcA := DecodeArgBx(w0)
			srcB := DecodeArgBx(w1)
			dst := DecodeArgBx(w3)

			if srcA < 256 && srcB < 256 && dst < 256 {
				atomic.StoreUint32(&code[i], EncodeABC(OP_FAST_ADD_INT_LOCAL_TO_LOCAL, uint8(dst), uint8(srcA), uint8(srcB)))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 5: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_SUB | OP_BINARY_SUB_INT) + OP_STORE_FAST
		// -> OP_FAST_SUB_INT_LOCAL_TO_LOCAL
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
			(op2 == OP_BINARY_SUB || op2 == OP_BINARY_SUB_INT) &&
			op3 == OP_STORE_FAST {

			srcA := DecodeArgBx(w0)
			srcB := DecodeArgBx(w1)
			dst := DecodeArgBx(w3)

			if srcA < 256 && srcB < 256 && dst < 256 {
				atomic.StoreUint32(&code[i], EncodeABC(OP_FAST_SUB_INT_LOCAL_TO_LOCAL, uint8(dst), uint8(srcA), uint8(srcB)))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 9: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_ADD | OP_BINARY_ADD_INT)
		// -> OP_FAST_ADD_INT_LOCALS (when not followed by OP_STORE_FAST)
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
			(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
			op3 != OP_STORE_FAST {
			srcA := DecodeArgBx(w0)
			srcB := DecodeArgBx(w1)
			if srcA < 256 && srcB < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_ADD_INT_LOCALS, uint8(srcA), srcB))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				i += 3
				continue
			}
		}

		// Pattern 10: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_SUB | OP_BINARY_SUB_INT)
		// -> OP_FAST_SUB_INT_LOCALS (when not followed by OP_STORE_FAST)
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
			(op2 == OP_BINARY_SUB || op2 == OP_BINARY_SUB_INT) &&
			op3 != OP_STORE_FAST {
			srcA := DecodeArgBx(w0)
			srcB := DecodeArgBx(w1)
			if srcA < 256 && srcB < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_SUB_INT_LOCALS, uint8(srcA), srcB))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				i += 3
				continue
			}
		}

		// Pattern 6: OP_LOAD_FAST + OP_LOAD_FAST + OP_COMPARE_OP (CMP_EQ) + OP_JUMP_IF_FALSE
		// -> OP_FAST_CMP_EQ_JUMP_IF_FALSE
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
			op2 == OP_COMPARE_OP && DecodeArgA(w2) == CMP_EQ &&
			op3 == OP_JUMP_IF_FALSE {

			srcA := DecodeArgBx(w0)
			srcB := DecodeArgBx(w1)
			exitTarget := DecodeArgBx(w3)

			if srcA < 256 && srcB < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_CMP_EQ_JUMP_IF_FALSE, uint8(srcA), exitTarget))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, uint8(srcB), 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 7: OP_LOAD_DEREF + OP_LOAD_FAST + OP_BINARY_SUBSCR + OP_STORE_FAST
		// -> OP_FAST_SUBSCR_DEREF_LIST_LOCAL
		if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST &&
			op2 == OP_BINARY_SUBSCR &&
			op3 == OP_STORE_FAST {

			cellIdx := DecodeArgBx(w0)
			idxReg := DecodeArgBx(w1)
			dstReg := DecodeArgBx(w3)

			if cellIdx < 256 && idxReg < 256 && dstReg < 256 {
				atomic.StoreUint32(&code[i], EncodeABC(OP_FAST_SUBSCR_DEREF_LIST_LOCAL, uint8(dstReg), uint8(cellIdx), uint8(idxReg)))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 7A: OP_LOAD_DEREF + OP_LOAD_FAST + OP_BINARY_SUBSCR + OP_JUMP_IF_TRUE
		// -> OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE
		if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST &&
			op2 == OP_BINARY_SUBSCR &&
			op3 == OP_JUMP_IF_TRUE {

			cellIdx := DecodeArgBx(w0)
			idxReg := DecodeArgBx(w1)
			exitTarget := DecodeArgBx(w3)

			if cellIdx < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE, uint8(cellIdx), exitTarget))
				atomic.StoreUint32(&code[i+1], Encode(OP_LOAD_FAST, 0, idxReg))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 7C: OP_LOAD_CONST + OP_LOAD_DEREF + OP_LOAD_FAST + OP_STORE_SUBSCR
		// -> OP_FAST_STORE_DEREF_LIST_LOCAL_CONST
		if op0 == OP_LOAD_CONST && op1 == OP_LOAD_DEREF &&
			op2 == OP_LOAD_FAST && op3 == OP_STORE_SUBSCR {

			constIdx := DecodeArgBx(w0)
			cellIdx := DecodeArgBx(w1)
			idxReg := DecodeArgBx(w2)

			if cellIdx < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_STORE_DEREF_LIST_LOCAL_CONST, uint8(cellIdx), idxReg))
				atomic.StoreUint32(&code[i+1], Encode(OP_LOAD_CONST, 0, constIdx))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
				i += 4
				continue
			}
		}

		// Pattern 7B: OP_LOAD_DEREF + OP_LOAD_FAST + OP_BINARY_SUBSCR (not followed by OP_STORE_FAST)
		// -> OP_FAST_SUBSCR_DEREF_LIST_LOCAL_PUSH
		if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST &&
			op2 == OP_BINARY_SUBSCR &&
			op3 != OP_STORE_FAST {

			cellIdx := DecodeArgBx(w0)
			idxReg := DecodeArgBx(w1)

			if cellIdx < 256 && idxReg < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_SUBSCR_DEREF_LIST_LOCAL_PUSH, uint8(cellIdx), idxReg))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				i += 3
				continue
			}
		}

		// Pattern 11: OP_LOAD_FAST + OP_LOAD_FAST + (BITWISE_OP) + OP_STORE_FAST
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST && op3 == OP_STORE_FAST {
			var fusedOp Opcode
			switch op2 {
			case OP_BINARY_AND:
				fusedOp = OP_FAST_AND_INT_LOCAL_TO_LOCAL
			case OP_BINARY_OR:
				fusedOp = OP_FAST_OR_INT_LOCAL_TO_LOCAL
			case OP_BINARY_XOR:
				fusedOp = OP_FAST_XOR_INT_LOCAL_TO_LOCAL
			case OP_BINARY_LSHIFT:
				fusedOp = OP_FAST_LSHIFT_INT_LOCAL_TO_LOCAL
			case OP_BINARY_RSHIFT:
				fusedOp = OP_FAST_RSHIFT_INT_LOCAL_TO_LOCAL
			}
			if fusedOp != 0 {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				dst := DecodeArgBx(w3)
				if srcA < 256 && srcB < 256 && dst < 256 {
					atomic.StoreUint32(&code[i], EncodeABC(fusedOp, uint8(dst), uint8(srcA), uint8(srcB)))
					atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+3], Encode(OP_NOP, 0, 0))
					i += 4
					continue
				}
			}
		}

		// Pattern 12: OP_LOAD_FAST + OP_LOAD_FAST + (BITWISE_OP) (not followed by OP_STORE_FAST)
		if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST && op3 != OP_STORE_FAST {
			var fusedOp Opcode
			switch op2 {
			case OP_BINARY_AND:
				fusedOp = OP_FAST_AND_INT_LOCALS
			case OP_BINARY_OR:
				fusedOp = OP_FAST_OR_INT_LOCALS
			case OP_BINARY_XOR:
				fusedOp = OP_FAST_XOR_INT_LOCALS
			case OP_BINARY_LSHIFT:
				fusedOp = OP_FAST_LSHIFT_INT_LOCALS
			case OP_BINARY_RSHIFT:
				fusedOp = OP_FAST_RSHIFT_INT_LOCALS
			}
			if fusedOp != 0 {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				if srcA < 256 && srcB < 256 {
					atomic.StoreUint32(&code[i], Encode(fusedOp, uint8(srcA), srcB))
					atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
					atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
					i += 3
					continue
				}
			}
		}

		// Pattern 13: OP_BINARY_AND + OP_UNARY_NOT + OP_JUMP_IF_FALSE
		// -> OP_JUMP_IF_BITAND_ZERO
		if op0 == OP_BINARY_AND && op1 == OP_UNARY_NOT && op2 == OP_JUMP_IF_FALSE {
			exitTarget := DecodeArgBx(w2)
			atomic.StoreUint32(&code[i], Encode(OP_JUMP_IF_BITAND_ZERO, 0, exitTarget))
			atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
			atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
			i += 3
			continue
		}

		// Pattern 8: OP_LOAD_FAST + OP_UNARY_NEGATIVE + OP_STORE_FAST (same register)
		// -> OP_FAST_NEG_INT_LOCAL
		if op0 == OP_LOAD_FAST && op1 == OP_UNARY_NEGATIVE &&
			op2 == OP_STORE_FAST &&
			DecodeArgBx(w0) == DecodeArgBx(w2) {

			reg := DecodeArgBx(w0)
			if reg < 256 {
				atomic.StoreUint32(&code[i], Encode(OP_FAST_NEG_INT_LOCAL, uint8(reg), 0))
				atomic.StoreUint32(&code[i+1], Encode(OP_NOP, 0, 0))
				atomic.StoreUint32(&code[i+2], Encode(OP_NOP, 0, 0))
				if op3 == OP_NOP {
					i += 4
				} else {
					i += 3
				}
				continue
			}
		}

		i++
	}
}

// DeoptimizeFastAdd restores the original 4 instructions for OP_FAST_ADD_INT_LOCAL_TO_LOCAL.
func DeoptimizeFastAdd(code []uint32, ip int, dst, srcA, srcB uint8) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_FAST, 0, uint16(srcA)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_LOAD_FAST, 0, uint16(srcB)))
	atomic.StoreUint32(&code[ip+2], Encode(OP_BINARY_ADD, 0, 0))
	atomic.StoreUint32(&code[ip+3], Encode(OP_STORE_FAST, 0, uint16(dst)))
}

// DeoptimizeFastInc restores the original 4 instructions for OP_FAST_INC_INT_LOCAL_CONST.
func DeoptimizeFastInc(code []uint32, ip int, reg uint8, constIdx uint16) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_FAST, 0, uint16(reg)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_LOAD_CONST, 0, constIdx))
	atomic.StoreUint32(&code[ip+2], Encode(OP_BINARY_ADD, 0, 0))
	atomic.StoreUint32(&code[ip+3], Encode(OP_STORE_FAST, 0, uint16(reg)))
}

// DeoptimizeFastLoop restores the original 4 instructions for OP_FAST_LOOP_CMP_LT_CONST_JUMP.
func DeoptimizeFastLoop(code []uint32, ip int, reg uint8, constIdx uint16, exitTarget uint16) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_FAST, 0, uint16(reg)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_LOAD_CONST, 0, constIdx))
	atomic.StoreUint32(&code[ip+2], Encode(OP_COMPARE_LT, 0, 0))
	atomic.StoreUint32(&code[ip+3], Encode(OP_JUMP_IF_FALSE, 0, exitTarget))
}

// DeoptimizeFastSub restores original instructions for OP_FAST_SUB_INT_LOCAL_TO_LOCAL.
func DeoptimizeFastSub(code []uint32, ip int, dst, srcA, srcB uint8) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_FAST, 0, uint16(srcA)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_LOAD_FAST, 0, uint16(srcB)))
	atomic.StoreUint32(&code[ip+2], Encode(OP_BINARY_SUB, 0, 0))
	atomic.StoreUint32(&code[ip+3], Encode(OP_STORE_FAST, 0, uint16(dst)))
}

// DeoptimizeFastCmpEq restores original instructions for OP_FAST_CMP_EQ_JUMP_IF_FALSE.
func DeoptimizeFastCmpEq(code []uint32, ip int, srcA, srcB uint8, exitTarget uint16) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_FAST, 0, uint16(srcA)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_LOAD_FAST, 0, uint16(srcB)))
	atomic.StoreUint32(&code[ip+2], Encode(OP_COMPARE_OP, CMP_EQ, 0))
	atomic.StoreUint32(&code[ip+3], Encode(OP_JUMP_IF_FALSE, 0, exitTarget))
}

// DeoptimizeFastSubscr restores original instructions for OP_FAST_SUBSCR_DEREF_LIST_LOCAL.
func DeoptimizeFastSubscr(code []uint32, ip int, dst, cellIdx, idxReg uint8) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_DEREF, 0, uint16(cellIdx)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_LOAD_FAST, 0, uint16(idxReg)))
	atomic.StoreUint32(&code[ip+2], Encode(OP_BINARY_SUBSCR, 0, 0))
	atomic.StoreUint32(&code[ip+3], Encode(OP_STORE_FAST, 0, uint16(dst)))
}

// DeoptimizeFastNeg restores original instructions for OP_FAST_NEG_INT_LOCAL.
func DeoptimizeFastNeg(code []uint32, ip int, reg uint8) {
	atomic.StoreUint32(&code[ip], Encode(OP_LOAD_FAST, 0, uint16(reg)))
	atomic.StoreUint32(&code[ip+1], Encode(OP_UNARY_NEGATIVE, 0, 0))
	atomic.StoreUint32(&code[ip+2], Encode(OP_STORE_FAST, 0, uint16(reg)))
}
