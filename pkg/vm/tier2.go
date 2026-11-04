package vm

import "sync/atomic"

// maybeOptimizeCodeObject atomically increments co.Hotness and triggers Tier-2 optimization
// once the hotness threshold (1,000) is exceeded.
func maybeOptimizeCodeObject(co *CodeObject) {
	if atomic.AddInt32(&co.Hotness, 1) >= 1000 && atomic.LoadInt32(&co.IsTier2Optimized) == 0 {
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

	// Phase 1: Build jump target and exception boundary map
	isJumpTarget := make([]bool, len(code)+1)
	for i := 0; i < len(code); i++ {
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

	// Phase 2: Linear scan and pattern fusion
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
