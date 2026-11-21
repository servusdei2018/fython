package vm

import (
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// tier2HotnessThreshold is the number of executions after which a code object
// is handed to the Tier-2 optimizer.
const tier2HotnessThreshold = 8

// maybeOptimizeCodeObject counts how often the VM executed co and triggers the
// Tier-2 optimizer once the hotness threshold is exceeded.
//
// The counter is VM-local whenever the runtime is single-threaded, so the hot
// loop never touches a cache line shared with another VM. In a multi-threaded
// runtime the shared atomic counter of the code object is used instead.
//
// The caller passes the code cache of co when it already has it at hand: the
// cache holds the counter, and looking it up again would cost more than the
// counter itself.
func maybeOptimizeCodeObject(vm *VM, co *CodeObject, cache *VMCodeCache) {
	if co == nil {
		return
	}
	if atomic.LoadInt32(&co.IsTier2Optimized) != 0 {
		return
	}
	if val.IsMultiThreaded() {
		if atomic.AddInt32(&co.Hotness, 1) >= tier2HotnessThreshold {
			OptimizeCodeObject(co)
		}
		return
	}
	if cache == nil {
		cache = vm.cacheFor(co)
	}
	cache.hotness++
	if cache.hotness >= tier2HotnessThreshold {
		OptimizeCodeObject(co)
		if rc := co.RegCode(); rc != nil && rc.jit.Load() == nil && rc.jitState.Load() == 0 && rc.jitState.CompareAndSwap(0, 1) {
			if jc := jitCompileRegCode(vm.engine, rc, co.Consts); jc != nil {
				rc.jit.Store(jc)
			} else {
				rc.jitState.Store(2)
			}
		}
	}
}

// OptimizeCodeObject scans the instruction stream of co and fuses common 3- and 4-instruction
// idioms into single Macro-Opcodes that execute in a single switch dispatch with register-passed
// values. Jump targets and exception boundaries are protected from cross-boundary fusion.
//
// The fusion runs on a private copy of the instruction stream which is
// published with a single atomic store. A VM that is already executing the
// previous stream therefore never observes a half-rewritten instruction
// window.
func OptimizeCodeObject(co *CodeObject) {
	if co == nil {
		return
	}
	if co.stream == nil {
		// The code object was never prepared for sharing, so there is
		// nowhere to publish the fused instruction stream.
		return
	}
	if !atomic.CompareAndSwapInt32(&co.IsTier2Optimized, 0, 1) {
		return
	}
	if len(co.Code) < 2 {
		return
	}
	if co.EntryIP == 0 {
		co.computeEntryIP()
	}
	// The copy is made lazily: a code object without a fusible idiom is
	// published unchanged and costs no allocation at all.
	var code []uint32

	// Step 1: Build jump target and exception boundary map
	isJumpTarget := make([]bool, len(co.Code)+1)
	for i := range co.Code {
		inst := co.Code[i]
		op := DecodeOpcode(inst)
		switch op {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP, OP_FOR_ITER, OP_FOR_ITER_GEN:
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

	// Step 2: Ahead-of-time call-site quickening for common builtin calls
	// (len(x), isinstance(x, T), hasattr(x, k), getattr(x, k[, d])) before
	// superinstruction fusion so surrounding loads can still fuse cleanly.
	view := co.Code
	for i := co.EntryIP; i+2 < len(view); i++ {
		w0 := view[i]
		if DecodeOpcode(w0) != OP_LOAD_GLOBAL {
			continue
		}
		nameIdx := int(DecodeArgBx(w0))
		if nameIdx >= len(co.Consts) {
			continue
		}
		gName := co.Consts[nameIdx]
		switch gName {
		case nameLen:
			if !isJumpTarget[i+1] && !isJumpTarget[i+2] {
				op1 := DecodeOpcode(view[i+1])
				w2 := view[i+2]
				if isSimpleLoadOp(op1) && DecodeOpcode(w2) == OP_CALL &&
					DecodeArgA(w2) == 1 && DecodeArgBx(w2) == CallFromStack {
					code = fusionCopy(co, code)
					code[i+2] = ReplaceOpcode(w2, OP_CALL_LEN)
					view = code
				}
			}
		case nameIsInstance, nameHasattr, nameGetattr:
			if i+3 < len(view) && !isJumpTarget[i+1] && !isJumpTarget[i+2] && !isJumpTarget[i+3] {
				op1 := DecodeOpcode(view[i+1])
				op2 := DecodeOpcode(view[i+2])
				if isSimpleLoadOp(op1) && isSimpleLoadOp(op2) {
					w3 := view[i+3]
					if DecodeOpcode(w3) == OP_CALL && DecodeArgA(w3) == 2 && DecodeArgBx(w3) == CallFromStack {
						var qOp Opcode
						switch gName {
						case nameIsInstance:
							qOp = OP_CALL_ISINSTANCE
						case nameHasattr:
							qOp = OP_CALL_HASATTR
						default:
							qOp = OP_CALL_GETATTR
						}
						code = fusionCopy(co, code)
						code[i+3] = ReplaceOpcode(w3, qOp)
						view = code
					} else if gName == nameGetattr && i+4 < len(view) && !isJumpTarget[i+4] {
						op3 := DecodeOpcode(view[i+3])
						w4 := view[i+4]
						if isSimpleLoadOp(op3) && DecodeOpcode(w4) == OP_CALL &&
							DecodeArgA(w4) == 3 && DecodeArgBx(w4) == CallFromStack {
							code = fusionCopy(co, code)
							code[i+4] = ReplaceOpcode(w4, OP_CALL_GETATTR)
							view = code
						}
					}
				}
			}
		}
	}

	// Step 3: Linear scan and pattern fusion. Jump-target checks are enforced per
	// pattern length so 2- and 3-op patterns still fuse before a branch target.
	src := co.Code
	if code != nil {
		src = code
	}
	for i := co.EntryIP; i < len(src); {
		if i+5 < len(src) && !isJumpTarget[i+1] && !isJumpTarget[i+2] && !isJumpTarget[i+3] && !isJumpTarget[i+4] && !isJumpTarget[i+5] {
			w0, w1, w2, w3, w4, w5 := src[i], src[i+1], src[i+2], src[i+3], src[i+4], src[i+5]
			op0, op1, op2, op3, op4, op5 := DecodeOpcode(w0), DecodeOpcode(w1), DecodeOpcode(w2), DecodeOpcode(w3), DecodeOpcode(w4), DecodeOpcode(w5)

			// Pattern 13: OP_FAST_SELF_RECURSE_INC_LOCAL disabled for isolation
			if false && op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST && op2 == OP_LOAD_CONST &&
				(op3 == OP_BINARY_ADD || op3 == OP_BINARY_ADD_INT) &&
				op4 == OP_CALL && DecodeArgA(w4) == 1 &&
				op5 == OP_POP_TOP {

				cellIdx := DecodeArgBx(w0)
				localReg := DecodeArgBx(w1)
				constIdx := DecodeArgBx(w2)

				if localReg < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_FAST_SELF_RECURSE_INC_LOCAL, uint8(localReg), 0, 0)
					code[i+1] = Encode(OP_LOAD_DEREF, 0, cellIdx)
					code[i+2] = Encode(OP_LOAD_CONST, 0, constIdx)
					code[i+3] = Encode(OP_NOP, 0, 0)
					code[i+4] = Encode(OP_NOP, 0, 0)
					code[i+5] = Encode(OP_NOP, 0, 0)
					i += 6
					continue
				}
			}

			// Pattern 14: OP_FAST_SUB_LOCAL_ADD_DEREF_TO_LOCAL
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
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_FAST_SUB_LOCAL_ADD_DEREF_TO_LOCAL, uint8(dst), uint8(srcA), uint8(srcB))
					code[i+1] = Encode(OP_LOAD_DEREF, 0, cellIdx)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					code[i+4] = Encode(OP_NOP, 0, 0)
					code[i+5] = Encode(OP_NOP, 0, 0)
					i += 6
					continue
				}
			}
		}

		if i+4 < len(src) && !isJumpTarget[i+1] && !isJumpTarget[i+2] && !isJumpTarget[i+3] && !isJumpTarget[i+4] {
			w0, w1, w2, w3, w4 := src[i], src[i+1], src[i+2], src[i+3], src[i+4]
			if DecodeOpcode(w0) == OP_LOAD_FAST && DecodeOpcode(w1) == OP_LOAD_ATTR &&
				DecodeOpcode(w2) == OP_LOAD_FAST && DecodeOpcode(w3) == OP_LOAD_ATTR &&
				DecodeOpcode(w4) == OP_BINARY_MUL {
				reg := DecodeArgBx(w0)
				attrIdx := DecodeArgBx(w1)
				if reg < 256 && DecodeArgBx(w2) == reg && DecodeArgBx(w3) == attrIdx {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_LOAD_FAST_ATTR_MUL_SELF, uint8(reg), attrIdx)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					code[i+4] = Encode(OP_NOP, 0, 0)
					i += 5
					continue
				}
			}
		}

		if i+3 < len(src) && !isJumpTarget[i+1] && !isJumpTarget[i+2] && !isJumpTarget[i+3] {
			w0, w1, w2, w3 := src[i], src[i+1], src[i+2], src[i+3]
			op0, op1, op2, op3 := DecodeOpcode(w0), DecodeOpcode(w1), DecodeOpcode(w2), DecodeOpcode(w3)

			// Pattern 6B: OP_LOAD_FAST + OP_LOAD_DEREF + OP_COMPARE_OP (CMP_EQ) + OP_JUMP_IF_FALSE
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_DEREF &&
				op2 == OP_COMPARE_OP && DecodeArgA(w2) == CMP_EQ &&
				op3 == OP_JUMP_IF_FALSE {
				srcA := DecodeArgBx(w0)
				cellIdx := DecodeArgBx(w1)
				exitTarget := DecodeArgBx(w3)
				if srcA < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE, uint8(srcA), exitTarget)
					code[i+1] = Encode(OP_LOAD_DEREF, 0, cellIdx)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 3: OP_LOAD_FAST + OP_LOAD_CONST + (OP_COMPARE_LT | OP_COMPARE_LT_INT) + OP_JUMP_IF_FALSE
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_CONST &&
				(op2 == OP_COMPARE_LT || op2 == OP_COMPARE_LT_INT) &&
				op3 == OP_JUMP_IF_FALSE {
				reg := DecodeArgBx(w0)
				constIdx := DecodeArgBx(w1)
				exitTarget := DecodeArgBx(w3)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_LOOP_CMP_LT_CONST_JUMP, uint8(reg), exitTarget)
					code[i+1] = Encode(OP_LOAD_CONST, 0, constIdx)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 2: OP_LOAD_FAST + OP_LOAD_CONST + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_FAST (same register)
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_CONST &&
				(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
				op3 == OP_STORE_FAST &&
				DecodeArgBx(w0) == DecodeArgBx(w3) {
				reg := DecodeArgBx(w0)
				constIdx := DecodeArgBx(w1)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_INC_INT_LOCAL_CONST, uint8(reg), constIdx)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 4: OP_LOAD_DEREF + OP_LOAD_CONST + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_DEREF (const == 1)
			if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_CONST &&
				(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
				op3 == OP_STORE_DEREF &&
				DecodeArgBx(w0) == DecodeArgBx(w3) {
				cellIdx := DecodeArgBx(w0)
				constIdx := DecodeArgBx(w1)
				if int(constIdx) < len(co.Consts) && co.Consts[constIdx].IsInt64() && co.Consts[constIdx].AsInt64() == 1 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_INC_DEREF_INT, 0, cellIdx)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 1: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_ADD | OP_BINARY_ADD_INT) + OP_STORE_FAST
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
				(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
				op3 == OP_STORE_FAST {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				dst := DecodeArgBx(w3)
				if srcA < 256 && srcB < 256 && dst < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_FAST_ADD_INT_LOCAL_TO_LOCAL, uint8(dst), uint8(srcA), uint8(srcB))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 5: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_SUB | OP_BINARY_SUB_INT) + OP_STORE_FAST
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
				(op2 == OP_BINARY_SUB || op2 == OP_BINARY_SUB_INT) &&
				op3 == OP_STORE_FAST {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				dst := DecodeArgBx(w3)
				if srcA < 256 && srcB < 256 && dst < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_FAST_SUB_INT_LOCAL_TO_LOCAL, uint8(dst), uint8(srcA), uint8(srcB))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 6: OP_LOAD_FAST + OP_LOAD_FAST + OP_COMPARE_OP (CMP_EQ) + OP_JUMP_IF_FALSE
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
				op2 == OP_COMPARE_OP && DecodeArgA(w2) == CMP_EQ &&
				op3 == OP_JUMP_IF_FALSE {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				exitTarget := DecodeArgBx(w3)
				if srcA < 256 && srcB < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_CMP_EQ_JUMP_IF_FALSE, uint8(srcA), exitTarget)
					code[i+1] = Encode(OP_NOP, uint8(srcB), 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 7: OP_LOAD_DEREF + OP_LOAD_FAST + OP_BINARY_SUBSCR + OP_STORE_FAST
			if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST &&
				op2 == OP_BINARY_SUBSCR &&
				op3 == OP_STORE_FAST {
				cellIdx := DecodeArgBx(w0)
				idxReg := DecodeArgBx(w1)
				dstReg := DecodeArgBx(w3)
				if cellIdx < 256 && idxReg < 256 && dstReg < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_FAST_SUBSCR_DEREF_LIST_LOCAL, uint8(dstReg), uint8(cellIdx), uint8(idxReg))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 7A: OP_LOAD_DEREF + OP_LOAD_FAST + OP_BINARY_SUBSCR + OP_JUMP_IF_TRUE
			if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST &&
				op2 == OP_BINARY_SUBSCR &&
				op3 == OP_JUMP_IF_TRUE {
				cellIdx := DecodeArgBx(w0)
				idxReg := DecodeArgBx(w1)
				exitTarget := DecodeArgBx(w3)
				if cellIdx < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE, uint8(cellIdx), exitTarget)
					code[i+1] = Encode(OP_LOAD_FAST, 0, idxReg)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Pattern 7C: OP_LOAD_CONST + OP_LOAD_DEREF + OP_LOAD_FAST + OP_STORE_SUBSCR
			if op0 == OP_LOAD_CONST && op1 == OP_LOAD_DEREF &&
				op2 == OP_LOAD_FAST && op3 == OP_STORE_SUBSCR {
				constIdx := DecodeArgBx(w0)
				cellIdx := DecodeArgBx(w1)
				idxReg := DecodeArgBx(w2)
				if cellIdx < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_STORE_DEREF_LIST_LOCAL_CONST, uint8(cellIdx), idxReg)
					code[i+1] = Encode(OP_LOAD_CONST, 0, constIdx)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
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
						code = fusionCopy(co, code)
						code[i] = EncodeABC(fusedOp, uint8(dst), uint8(srcA), uint8(srcB))
						code[i+1] = Encode(OP_NOP, 0, 0)
						code[i+2] = Encode(OP_NOP, 0, 0)
						code[i+3] = Encode(OP_NOP, 0, 0)
						i += 4
						continue
					}
				}
			}

			// Pattern 29: OP_UNPACK_SEQUENCE(3) + 3 * OP_STORE_FAST -> OP_UNPACK_3_FAST
			if op0 == OP_UNPACK_SEQUENCE && DecodeArgBx(w0) == 3 &&
				op1 == OP_STORE_FAST && op2 == OP_STORE_FAST && op3 == OP_STORE_FAST {
				r0, r1, r2 := DecodeArgBx(w1), DecodeArgBx(w2), DecodeArgBx(w3)
				if r0 < 256 && r1 < 256 && r2 < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_UNPACK_3_FAST, uint8(r0), uint8(r1), uint8(r2))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}

			// Fused receiver load + 2 pure arg loads + OP_CALL_METHOD_PIC(2) -> w1, w2, OP_CALL_FAST_ATTR_DESCR, OP_NOP.
			if op0 == OP_LOAD_FAST && (op1 == OP_LOAD_FAST || op1 == OP_LOAD_CONST) &&
				(op2 == OP_LOAD_FAST || op2 == OP_LOAD_CONST) && op3 == OP_CALL_METHOD_PIC &&
				DecodeArgA(w3) == 2 {
				reg := DecodeArgBx(w0)
				methodIdx := DecodeArgBx(w3)
				if reg < 256 && methodIdx < 256 && isFastAttrDescrEligible(co, int(methodIdx)) {
					code = fusionCopy(co, code)
					code[i] = w1
					code[i+1] = w2
					code[i+2] = EncodeABC(OP_CALL_FAST_ATTR_DESCR, uint8(reg), uint8(methodIdx), 2)
					code[i+3] = Encode(OP_NOP, 0, 0)
					i += 4
					continue
				}
			}
		}

		if i+2 < len(src) && !isJumpTarget[i+1] && !isJumpTarget[i+2] {
			w0, w1, w2 := src[i], src[i+1], src[i+2]
			op0, op1, op2 := DecodeOpcode(w0), DecodeOpcode(w1), DecodeOpcode(w2)
			nextOpIsStoreFast := i+3 < len(src) && DecodeOpcode(src[i+3]) == OP_STORE_FAST

			// Pattern 9: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_ADD | OP_BINARY_ADD_INT)
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
				(op2 == OP_BINARY_ADD || op2 == OP_BINARY_ADD_INT) &&
				!nextOpIsStoreFast {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				if srcA < 256 && srcB < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_ADD_INT_LOCALS, uint8(srcA), srcB)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Pattern 10: OP_LOAD_FAST + OP_LOAD_FAST + (OP_BINARY_SUB | OP_BINARY_SUB_INT)
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST &&
				(op2 == OP_BINARY_SUB || op2 == OP_BINARY_SUB_INT) &&
				!nextOpIsStoreFast {
				srcA := DecodeArgBx(w0)
				srcB := DecodeArgBx(w1)
				if srcA < 256 && srcB < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_SUB_INT_LOCALS, uint8(srcA), srcB)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Pattern 7B: OP_LOAD_DEREF + OP_LOAD_FAST + OP_BINARY_SUBSCR
			if op0 == OP_LOAD_DEREF && op1 == OP_LOAD_FAST &&
				op2 == OP_BINARY_SUBSCR && !nextOpIsStoreFast {
				cellIdx := DecodeArgBx(w0)
				idxReg := DecodeArgBx(w1)
				if cellIdx < 256 && idxReg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_SUBSCR_DEREF_LIST_LOCAL_PUSH, uint8(cellIdx), idxReg)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Pattern 12: OP_LOAD_FAST + OP_LOAD_FAST + (BITWISE_OP)
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST && !nextOpIsStoreFast {
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
						code = fusionCopy(co, code)
						code[i] = Encode(fusedOp, uint8(srcA), srcB)
						code[i+1] = Encode(OP_NOP, 0, 0)
						code[i+2] = Encode(OP_NOP, 0, 0)
						i += 3
						continue
					}
				}
			}

			// Pattern 13B: OP_BINARY_AND + OP_UNARY_NOT + OP_JUMP_IF_FALSE
			if op0 == OP_BINARY_AND && op1 == OP_UNARY_NOT && op2 == OP_JUMP_IF_FALSE {
				exitTarget := DecodeArgBx(w2)
				code = fusionCopy(co, code)
				code[i] = Encode(OP_JUMP_IF_BITAND_ZERO, 0, exitTarget)
				code[i+1] = Encode(OP_NOP, 0, 0)
				code[i+2] = Encode(OP_NOP, 0, 0)
				i += 3
				continue
			}

			// Pattern 8: OP_LOAD_FAST + OP_UNARY_NEGATIVE + OP_STORE_FAST (same register)
			if op0 == OP_LOAD_FAST && op1 == OP_UNARY_NEGATIVE &&
				op2 == OP_STORE_FAST &&
				DecodeArgBx(w0) == DecodeArgBx(w2) {
				reg := DecodeArgBx(w0)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_FAST_NEG_INT_LOCAL, uint8(reg), 0)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					if i+3 < len(src) && DecodeOpcode(src[i+3]) == OP_NOP {
						i += 4
					} else {
						i += 3
					}
					continue
				}
			}

			// Pattern 17: OP_LOAD_CONST + (OP_COMPARE_OP | OP_COMPARE_LT) + (OP_JUMP_IF_FALSE | OP_JUMP_IF_TRUE)
			if op0 == OP_LOAD_CONST && (op1 == OP_COMPARE_OP || op1 == OP_COMPARE_LT) &&
				(op2 == OP_JUMP_IF_FALSE || op2 == OP_JUMP_IF_TRUE) {
				constIdx := DecodeArgBx(w0)
				target := int(DecodeArgBx(w2))
				rel := target - i
				if constIdx < 256 && int(constIdx) < len(co.Consts) && rel > 0 && rel < 256 &&
					isPrimitiveCompareConst(co.Consts[constIdx]) {
					cmpOp := CMP_LT
					if op1 == OP_COMPARE_OP {
						cmpOp = DecodeArgA(w1)
					}
					fusedOp := OP_CMP_CONST_JUMP_IF_FALSE
					if op2 == OP_JUMP_IF_TRUE {
						fusedOp = OP_CMP_CONST_JUMP_IF_TRUE
					}
					code = fusionCopy(co, code)
					code[i] = EncodeABC(fusedOp, cmpOp, uint8(constIdx), uint8(rel))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Pattern 18: OP_LOAD_FAST + OP_LOAD_FAST + OP_BINARY_SUBSCR
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST && op2 == OP_BINARY_SUBSCR {
				r1 := DecodeArgBx(w0)
				r2 := DecodeArgBx(w1)
				if r1 < 256 && r2 < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_SUBSCR_FAST_FAST, 0, uint8(r1), uint8(r2))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Pattern 19: OP_LOAD_FAST + OP_LOAD_CONST + OP_BINARY_SUBSCR
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_CONST && op2 == OP_BINARY_SUBSCR {
				reg := DecodeArgBx(w0)
				constIdx := DecodeArgBx(w1)
				if reg < 256 && constIdx < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_SUBSCR_FAST_CONST, 0, uint8(reg), uint8(constIdx))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Pattern 30: OP_UNPACK_SEQUENCE(2) + 2 * OP_STORE_FAST -> OP_UNPACK_2_FAST
			if op0 == OP_UNPACK_SEQUENCE && DecodeArgBx(w0) == 2 &&
				op1 == OP_STORE_FAST && op2 == OP_STORE_FAST {
				r0, r1 := DecodeArgBx(w1), DecodeArgBx(w2)
				if r0 < 256 && r1 < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_UNPACK_2_FAST, uint8(r0), uint8(r1), 0)
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Fused compare of two locals without stack traffic.
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST && op2 == OP_COMPARE_OP {
				r1 := DecodeArgBx(w0)
				r2 := DecodeArgBx(w1)
				cmpOp := DecodeArgA(w2)
				if r1 < 256 && r2 < 256 && cmpOp != CMP_IN && cmpOp != CMP_NOT_IN {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_COMPARE_FAST_FAST, cmpOp, uint8(r1), uint8(r2))
					code[i+1] = Encode(OP_NOP, 0, 0)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}

			// Fused receiver load + 1 pure arg load + OP_CALL_METHOD_PIC(1) -> w1, OP_CALL_FAST_ATTR_DESCR, OP_NOP.
			if op0 == OP_LOAD_FAST && (op1 == OP_LOAD_FAST || op1 == OP_LOAD_CONST) &&
				op2 == OP_CALL_METHOD_PIC && DecodeArgA(w2) == 1 {
				reg := DecodeArgBx(w0)
				methodIdx := DecodeArgBx(w2)
				if reg < 256 && methodIdx < 256 && isFastAttrDescrEligible(co, int(methodIdx)) {
					code = fusionCopy(co, code)
					code[i] = w1
					code[i+1] = EncodeABC(OP_CALL_FAST_ATTR_DESCR, uint8(reg), uint8(methodIdx), 1)
					code[i+2] = Encode(OP_NOP, 0, 0)
					i += 3
					continue
				}
			}
		}

		if i+1 < len(src) && !isJumpTarget[i+1] {
			w0, w1 := src[i], src[i+1]
			op0, op1 := DecodeOpcode(w0), DecodeOpcode(w1)

			// Pattern 15: OP_LOAD_FAST + OP_LOAD_ATTR -> OP_LOAD_FAST_ATTR
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_ATTR {
				reg := DecodeArgBx(w0)
				attrIdx := DecodeArgBx(w1)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_LOAD_FAST_ATTR, uint8(reg), attrIdx)
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 16: OP_LOAD_FAST + OP_STORE_ATTR -> OP_STORE_FAST_ATTR
			if op0 == OP_LOAD_FAST && op1 == OP_STORE_ATTR {
				reg := DecodeArgBx(w0)
				attrIdx := DecodeArgBx(w1)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_STORE_FAST_ATTR, uint8(reg), attrIdx)
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 20: OP_LOAD_CONST + OP_POP_TOP -> OP_JUMP (i+2)
			if op0 == OP_LOAD_CONST && op1 == OP_POP_TOP && i+2 < 65536 {
				constIdx := int(DecodeArgBx(w0))
				if constIdx < len(co.Consts) && co.Consts[constIdx].Kind() != val.KindMemoryView {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_JUMP, 0, uint16(i+2))
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 21: (OP_COMPARE_OP | OP_COMPARE_LT) + (OP_JUMP_IF_FALSE | OP_JUMP_IF_TRUE)
			// Keep CMP_IN / CMP_NOT_IN unfused so OP_COMPARE_OP's in-loop Dict/Set fast paths stay active.
			if (op0 == OP_COMPARE_OP || op0 == OP_COMPARE_LT) &&
				(op1 == OP_JUMP_IF_FALSE || op1 == OP_JUMP_IF_TRUE) {
				cmpOp := CMP_LT
				if op0 == OP_COMPARE_OP {
					cmpOp = DecodeArgA(w0)
				}
				if cmpOp != CMP_IN && cmpOp != CMP_NOT_IN {
					target := DecodeArgBx(w1)
					fusedOp := OP_COMPARE_JUMP_IF_FALSE
					if op1 == OP_JUMP_IF_TRUE {
						fusedOp = OP_COMPARE_JUMP_IF_TRUE
					}
					code = fusionCopy(co, code)
					code[i] = Encode(fusedOp, cmpOp, target)
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 22: OP_LOAD_CONST + OP_RETURN -> OP_RETURN_CONST
			if op0 == OP_LOAD_CONST && op1 == OP_RETURN {
				code = fusionCopy(co, code)
				code[i] = Encode(OP_RETURN_CONST, 0, DecodeArgBx(w0))
				code[i+1] = Encode(OP_NOP, 0, 0)
				i += 2
				continue
			}

			// Pattern 23: OP_LOAD_FAST + OP_RETURN -> OP_RETURN_FAST
			if op0 == OP_LOAD_FAST && op1 == OP_RETURN {
				reg := DecodeArgBx(w0)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_RETURN_FAST, 0, reg)
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 24: OP_LOAD_GLOBAL + OP_LOAD_FAST -> OP_LOAD_GLOBAL_FAST
			if op0 == OP_LOAD_GLOBAL && op1 == OP_LOAD_FAST {
				gIdx := DecodeArgBx(w0)
				reg := DecodeArgBx(w1)
				if gIdx < 256 && reg < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_LOAD_GLOBAL_FAST, 0, uint8(gIdx), uint8(reg))
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 25: OP_LOAD_FAST + OP_LOAD_CONST -> OP_LOAD_FAST_CONST
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_CONST {
				reg := DecodeArgBx(w0)
				if reg < 256 {
					code = fusionCopy(co, code)
					code[i] = Encode(OP_LOAD_FAST_CONST, uint8(reg), DecodeArgBx(w1))
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 26: OP_LOAD_FAST + OP_LOAD_FAST -> OP_LOAD_FAST_2
			if op0 == OP_LOAD_FAST && op1 == OP_LOAD_FAST {
				r1 := DecodeArgBx(w0)
				r2 := DecodeArgBx(w1)
				if r1 < 256 && r2 < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_LOAD_FAST_2, 0, uint8(r1), uint8(r2))
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 27: OP_STORE_FAST + OP_LOAD_FAST -> OP_STORE_LOAD_FAST
			if op0 == OP_STORE_FAST && op1 == OP_LOAD_FAST {
				dst := DecodeArgBx(w0)
				srcReg := DecodeArgBx(w1)
				if dst < 256 && srcReg < 256 {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_STORE_LOAD_FAST, 0, uint8(dst), uint8(srcReg))
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

			// Pattern 28: OP_POP_TOP + OP_LOAD_FAST -> OP_POP_LOAD_FAST
			if op0 == OP_POP_TOP && op1 == OP_LOAD_FAST {
				code = fusionCopy(co, code)
				code[i] = Encode(OP_POP_LOAD_FAST, 0, DecodeArgBx(w1))
				code[i+1] = Encode(OP_NOP, 0, 0)
				i += 2
				continue
			}

			// Fused receiver load + OP_CALL_METHOD_PIC(0) -> OP_CALL_FAST_ATTR_DESCR, OP_NOP.
			if op0 == OP_LOAD_FAST && op1 == OP_CALL_METHOD_PIC && DecodeArgA(w1) == 0 {
				reg := DecodeArgBx(w0)
				methodIdx := DecodeArgBx(w1)
				if reg < 256 && methodIdx < 256 && isFastAttrDescrEligible(co, int(methodIdx)) {
					code = fusionCopy(co, code)
					code[i] = EncodeABC(OP_CALL_FAST_ATTR_DESCR, uint8(reg), uint8(methodIdx), 0)
					code[i+1] = Encode(OP_NOP, 0, 0)
					i += 2
					continue
				}
			}

		}

		i++
	}

	if code != nil {
		// Publish the fused instruction stream atomically: every VM picks it up
		// the next time it reloads the active slice.
		co.publish(code)
	}
	// Attempt a register compilation on top of the original instruction
	// stream. The stack stream stays authoritative; RegCode is consulted only
	// when a frame is entered through runRegLoop.
	if rc := TryCompileRegCode(co); rc != nil {
		co.publishRegCode(rc)
	}
}

func isPrimitiveCompareConst(v val.Value) bool {
	return v.IsNone() || v.IsBool() || v.IsInt64() || v.IsFloat64() ||
		v.IsInlineStr() || (v.IsString() && v.AsStringObj().Header.Type == val.StrType)
}

func isSimpleLoadOp(op Opcode) bool {
	return op == OP_LOAD_FAST || op == OP_LOAD_CONST || op == OP_LOAD_DEREF || op == OP_LOAD_GLOBAL
}

// isFastAttrDescrEligible reports whether a method name constant is a hot
// native descriptor method suitable for OP_CALL_FAST_ATTR_DESCR fusion.
// Python methods stay on OP_CALL_METHOD_PIC so in-loop method PIC and
// tryInlineMethod remain active, and stream-I/O names stay unfused to
// preserve opForIterRangeWriteLoop.
func isFastAttrDescrEligible(co *CodeObject, methodIdx int) bool {
	if methodIdx < 0 || methodIdx >= len(co.Consts) {
		return false
	}
	name := co.Consts[methodIdx]
	if !name.IsAnyString() {
		return false
	}
	switch name.AsString() {
	case "startswith", "endswith", "split", "rsplit", "strip", "lstrip", "rstrip",
		"lower", "upper", "replace", "join", "find", "rfind", "count", "encode", "decode",
		"append", "extend", "pop", "copy", "clear", "insert",
		"get", "keys", "values", "items", "update", "setdefault",
		"add", "discard",
		"group", "groups", "start", "end", "span", "match", "search", "sub":
		return true
	}
	return false
}

// fusionCopy returns a private copy of the instruction stream of co, so that
// the fusion can be applied without touching the published stream.
func fusionCopy(co *CodeObject, code []uint32) []uint32 {
	if code != nil {
		return code
	}
	c := make([]uint32, len(co.Code))
	copy(c, co.Code)
	return c
}

// deoptimizeAt publishes an unfused copy of the instruction stream of co so
// that the generic opcodes take over at ip.
//
// The published instruction stream is never modified in place: a private copy
// is patched and then published atomically, so no VM ever observes a
// half-rewritten instruction window. The patched stream is returned so that
// the calling VM can continue with it.
func deoptimizeAt(co *CodeObject, ip int, patch func([]uint32)) (unsafe.Pointer, int) {
	if co == nil {
		return nil, 0
	}
	base := co.Active()
	if len(base) == 0 {
		return unsafe.Pointer(unsafe.SliceData(base)), 0
	}
	newCode := make([]uint32, len(base))
	copy(newCode, base)
	patch(newCode)
	co.publish(newCode)
	return unsafe.Pointer(unsafe.SliceData(newCode)), len(newCode)
}

// DeoptimizeFastAdd restores the original 4 instructions for OP_FAST_ADD_INT_LOCAL_TO_LOCAL.
//
// The code slice it patches must be a private copy: the published instruction
// stream is never modified in place.
func DeoptimizeFastAdd(code []uint32, ip int, dst, srcA, srcB uint8) {
	code[ip] = Encode(OP_LOAD_FAST, 0, uint16(srcA))
	code[ip+1] = Encode(OP_LOAD_FAST, 0, uint16(srcB))
	code[ip+2] = Encode(OP_BINARY_ADD, 0, 0)
	code[ip+3] = Encode(OP_STORE_FAST, 0, uint16(dst))
}

// DeoptimizeFastInc restores the original 4 instructions for OP_FAST_INC_INT_LOCAL_CONST.
func DeoptimizeFastInc(code []uint32, ip int, reg uint8, constIdx uint16) {
	code[ip] = Encode(OP_LOAD_FAST, 0, uint16(reg))
	code[ip+1] = Encode(OP_LOAD_CONST, 0, constIdx)
	code[ip+2] = Encode(OP_BINARY_ADD, 0, 0)
	code[ip+3] = Encode(OP_STORE_FAST, 0, uint16(reg))
}

// DeoptimizeFastLoop restores the original 4 instructions for OP_FAST_LOOP_CMP_LT_CONST_JUMP.
func DeoptimizeFastLoop(code []uint32, ip int, reg uint8, constIdx uint16, exitTarget uint16) {
	code[ip] = Encode(OP_LOAD_FAST, 0, uint16(reg))
	code[ip+1] = Encode(OP_LOAD_CONST, 0, constIdx)
	code[ip+2] = Encode(OP_COMPARE_LT, 0, 0)
	code[ip+3] = Encode(OP_JUMP_IF_FALSE, 0, exitTarget)
}

// DeoptimizeFastSub restores original instructions for OP_FAST_SUB_INT_LOCAL_TO_LOCAL.
func DeoptimizeFastSub(code []uint32, ip int, dst, srcA, srcB uint8) {
	code[ip] = Encode(OP_LOAD_FAST, 0, uint16(srcA))
	code[ip+1] = Encode(OP_LOAD_FAST, 0, uint16(srcB))
	code[ip+2] = Encode(OP_BINARY_SUB, 0, 0)
	code[ip+3] = Encode(OP_STORE_FAST, 0, uint16(dst))
}

// DeoptimizeFastCmpEq restores original instructions for OP_FAST_CMP_EQ_JUMP_IF_FALSE.
func DeoptimizeFastCmpEq(code []uint32, ip int, srcA, srcB uint8, exitTarget uint16) {
	code[ip] = Encode(OP_LOAD_FAST, 0, uint16(srcA))
	code[ip+1] = Encode(OP_LOAD_FAST, 0, uint16(srcB))
	code[ip+2] = Encode(OP_COMPARE_OP, CMP_EQ, 0)
	code[ip+3] = Encode(OP_JUMP_IF_FALSE, 0, exitTarget)
}

// DeoptimizeFastSubscr restores original instructions for OP_FAST_SUBSCR_DEREF_LIST_LOCAL.
func DeoptimizeFastSubscr(code []uint32, ip int, dst, cellIdx, idxReg uint8) {
	code[ip] = Encode(OP_LOAD_DEREF, 0, uint16(cellIdx))
	code[ip+1] = Encode(OP_LOAD_FAST, 0, uint16(idxReg))
	code[ip+2] = Encode(OP_BINARY_SUBSCR, 0, 0)
	code[ip+3] = Encode(OP_STORE_FAST, 0, uint16(dst))
}

// DeoptimizeFastNeg restores original instructions for OP_FAST_NEG_INT_LOCAL.
func DeoptimizeFastNeg(code []uint32, ip int, reg uint8) {
	code[ip] = Encode(OP_LOAD_FAST, 0, uint16(reg))
	code[ip+1] = Encode(OP_UNARY_NEGATIVE, 0, 0)
	code[ip+2] = Encode(OP_STORE_FAST, 0, uint16(reg))
}
