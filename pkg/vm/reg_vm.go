package vm

import (
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// tryNestedRegCall attempts register execution for an in-loop call to
// callee whose arguments are already bound in st.stack[newBase:nextSP]. On
// success it returns the stack-loop continuation with the result at
// stack[retSP]. It returns handled=false when the frame must execute on the
// stack loop. callIP is the call-site ip for exact tracebacks; pass a
// negative callIP to derive and verify it from retIP (bailing when the
// resume slot does not decode to a call).
func (vm *VM) tryNestedRegCall(st *loopState, callee *CodeObject, fn *val.PyFunction, newBase, nextSP, retSP, retIP, callIP int) (int, int, loopCtl, bool) {
	if !vm.regCallEligible(st, callee) {
		return 0, 0, ctlNext, false
	}
	if callIP < 0 {
		callIP = retIP - 1
		if callIP < 0 || st.codePtr == nil || !isRegCallSite(st, callIP) {
			return 0, 0, ctlNext, false
		}
	}
	if need := newBase + int(callee.RegCode().NumRegs); need > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, need)
	}
	res, faulted := vm.nestedRegRun(st, callee, fn, newBase, nextSP)
	if faulted {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		st.raiseExc = exc
		return callIP, st.sp, ctlRaise, true
	}
	st.stack = vm.stack
	st.stack[retSP] = res
	return retIP, retSP + 1, ctlReload, true
}

// regCallEligible reports whether an in-loop call may enter register
// execution. Type-construction __init__ calls and dunder comparisons with
// caller fallback state keep the stack path.
func (vm *VM) regCallEligible(st *loopState, callee *CodeObject) bool {
	rc := callee.RegCode()
	if rc == nil || len(callee.Code) < 16 || callee.Name == "__init__" {
		return false
	}
	if st.activeGen != nil || st.activeCoro != nil || !st.stackIsVM {
		return false
	}
	if st.frameIdx < 0 || st.frameIdx >= len(vm.frames) || vm.frames[st.frameIdx].cmpActive {
		return false
	}
	if len(st.stack) == 0 || len(vm.stack) == 0 || &st.stack[0] != &vm.stack[0] {
		return false
	}
	return true
}

// nestedRegRun executes callee in a nested frame, returning (result, false)
// on success. On fault it returns (zero, true) with the exception pending
// in vm.unhandledException and st.stack refreshed.
func (vm *VM) nestedRegRun(st *loopState, callee *CodeObject, fn *val.PyFunction, newBase, nextSP int) (val.Value, bool) {
	fnGlobals := st.globals
	if fn != nil && fn.Globals != nil {
		fnGlobals = fn.Globals
	}
	nextFrame := st.frameIdx + 1
	vm.ensureFrameCapacity(nextFrame + 4)
	res := vm.runFrame(callee, newBase, nextSP, nextFrame, nil, fnGlobals, val.NoneVal(), st.stack, nil, nil)
	st.stack = vm.stack
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		return val.Value{}, true
	}
	return res, false
}

// isRegCallSite reports whether the instruction at stack ip decodes to a
// call, validating a derived call-site ip.
func isRegCallSite(st *loopState, ip int) bool {
	if ip < 0 || ip >= st.codeLen {
		return false
	}
	op := Opcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip)*4)) >> 24)
	switch op {
	case OP_CALL, OP_CALL_KW, OP_CALL_FUNCTION_EX, OP_CALL_METHOD,
		OP_CALL_METHOD_PIC, OP_CALL_PY_EXACT_ARGS, OP_CALL_PY_DEFAULTS,
		OP_CALL_BUILTIN_FAST, OP_CALL_LEN, OP_CALL_ISINSTANCE,
		OP_CALL_TYPE_INIT, OP_CALL_METHOD_DESCR, OP_CALL_METHOD_DESCR_FAST,
		OP_CALL_BOUND_NATIVE, OP_CALL_HASATTR, OP_CALL_GETATTR,
		OP_CALL_INSTANCE_DUNDER, OP_COMPARE_DUNDER:
		return true
	default:
		return false
	}
}

// runRegLoop executes the register compilation rc of co for the frame at
// base. It returns (result, true) on success and (zero, false) when the
// frame must run on the stack loop instead. The false path is reserved for
// pre-execution entry checks only: once pc advances, execution always runs
// to completion, a return, or an unwind.
//
// The register file aliases the frame's stack slice: regs[i] and
// st.stack[base+i] are the same memory. Locals live in [0, ScratchBase) and
// scratch registers above. Complex operations delegate to the same
// synchronous helpers the stack loop uses on its slow paths; helpers that
// can push interpreter frames are never called from here.
func (vm *VM) runRegLoop(fn *val.PyFunction, co *CodeObject, rc *RegCode, base int) (val.Value, bool) {
	_ = fn // The frame was already bound by the caller.
	st := vm.activeState
	if st == nil || st.curCO != co || st.cache == nil {
		return val.Value{}, false
	}
	if st.activeGen != nil || st.activeCoro != nil {
		return val.Value{}, false
	}
	if !st.stackIsVM || len(st.stack) == 0 || len(vm.stack) == 0 {
		return val.Value{}, false
	}
	if &st.stack[0] != &vm.stack[0] {
		return val.Value{}, false
	}
	nregs := int(rc.NumRegs)
	frameTop := base + nregs
	if frameTop > len(st.stack) {
		return val.Value{}, false
	}
	rf := vm.acquireRegFrame()
	rf.vm = vm
	rf.st = st
	rf.co = co
	rf.rc = rc
	rf.base = base
	rf.frameTop = frameTop
	rf.scratchBase = int(rc.ScratchBase)
	rf.code = rc.Code
	rf.pcs = rc.StackPC
	rf.consts = co.Consts
	rf.cache = st.cache
	rf.regs = st.stack[base:]
	rf.pc = 0
	res, ok := rf.run()
	vm.releaseRegFrame(rf)
	return res, ok
}

// regFrame holds the mutable execution state of one runRegLoop invocation.
type regFrame struct {
	vm          *VM
	st          *loopState
	co          *CodeObject
	rc          *RegCode
	base        int
	frameTop    int
	scratchBase int
	code        []RegInstr
	pcs         []int32
	consts      []val.Value
	cache       *VMCodeCache
	regs        []val.Value
	pc          int
}

// fault unwinds the register frame at mapped stack ip. Register functions
// carry no exception tables, so unwind always runs to the nested-call
// protocol: vm.unhandledException carries the exception to the caller.
func (rf *regFrame) fault(sip int) (val.Value, bool) {
	rf.st.ip = sip
	rf.st.sp = rf.frameTop
	_, _, ctl := rf.vm.unwind(rf.st, sip)
	if ctl == ctlExit {
		return rf.st.result, true
	}
	panic("vm: register loop resumed after unwind")
}

// refresh re-derives the register slice after helpers that may grow the
// stack.
func (rf *regFrame) refresh() {
	rf.regs = rf.st.stack[rf.base:]
}

// callTick mirrors the stack loop's periodic limit checks on calls.
func (rf *regFrame) callTick() {
	rf.st.periodicTick++
	if (rf.st.periodicTick & 0xFF) == 0 {
		rf.vm.checkPeriodicSafepoint(rf.st.frameIdx)
	}
}

// backwardTick mirrors the stack loop's backward-edge safepoint. On
// continuation it sets the next register pc; on a fault it finishes the
// frame with the unwind result.
func (rf *regFrame) backwardTick(sip, targetPC int) (val.Value, bool) {
	st := rf.st
	co := rf.co
	st.periodicTick++
	if (st.periodicTick&0xFF) != 0 && atomic.LoadInt32(&co.IsTier2Optimized) != 0 &&
		atomic.LoadInt32(&co.StreamGen) == st.cache.streamGen && !rf.vm.signalsPending() {
		rf.pc = targetPC
		return val.Value{}, false
	}
	stackTarget := int(rf.pcs[targetPC])
	_, _, ctl := rf.vm.opJumpSlow(st, stackTarget, sip, rf.frameTop)
	switch ctl {
	case ctlNext, ctlReload:
		rf.refresh()
		rf.pc = targetPC
		return val.Value{}, false
	default:
		return rf.fault(sip)
	}
}

// run executes the register stream to completion, a return, or an unwind.
func (rf *regFrame) run() (val.Value, bool) {
	regs := rf.regs
	consts := rf.consts
	code := rf.code
	pcs := rf.pcs
	for rf.pc >= 0 && rf.pc < len(code) {
		inst := code[rf.pc]
		op := DecodeRegOp(inst)
		recordRegOp(op)
		dst := int(DecodeRegDst(inst))
		src1 := int(DecodeRegSrc1(inst))
		src2 := int(DecodeRegSrc2(inst))
		aux := DecodeRegAux(inst)
		sip := int(pcs[rf.pc])
		switch op {
		case RegMov:
			regs[dst] = regs[src1]
			rf.pc++
		case RegLoadConst:
			regs[dst] = consts[aux]
			rf.pc++
		case RegLoadGlobal:
			if rf.loadGlobal(dst, uint16(aux), sip) {
				rf.pc++
				continue
			}
			if res, finished := rf.slowGlobal(dst, uint16(aux), sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegStoreGlobal:
			if res, finished := rf.slowStoreGlobal(dst, uint16(aux), sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegAdd, RegSub, RegMul:
			if rf.arithFast(op, dst, src1, src2) {
				rf.pc++
				continue
			}
			if res, finished := rf.arithSlow(op, dst, src1, src2, sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegTrueDiv, RegFloorDiv, RegMod, RegPow, RegMatMul,
			RegBitAnd, RegBitOr, RegBitXor, RegLShift, RegRShift:
			if res, finished := rf.arithSlow(op, dst, src1, src2, sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegNeg, RegPos, RegInvert:
			if res, finished := rf.unarySlow(op, dst, src1, sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegNot:
			regs[dst] = val.BoolVal(!regs[src1].IsTruthy())
			rf.pc++
		case RegEq, RegNe, RegLt, RegLe, RegGt, RegGe, RegIn, RegIs:
			if rf.cmpFast(op, dst, src1, src2, aux) {
				rf.pc++
				continue
			}
			if res, finished := rf.cmpSlow(op, dst, src1, src2, sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegJmp:
			t := int(aux)
			if t <= rf.pc {
				if res, finished := rf.backwardTick(sip, t); finished {
					return res, true
				}
				regs = rf.regs
				continue
			}
			rf.pc = t
		case RegJmpIfFalse, RegJmpIfTrue:
			cond := regs[src1].IsTruthy()
			take := !cond
			if op == RegJmpIfTrue {
				take = cond
			}
			t := int(aux)
			if take {
				if t <= rf.pc {
					if res, finished := rf.backwardTick(sip, t); finished {
						return res, true
					}
					regs = rf.regs
					continue
				}
				rf.pc = t
				continue
			}
			rf.pc++
		case RegLtJmpIfFalse, RegLtJmpIfTrue, RegEqJmpIfFalse, RegEqJmpIfTrue:
			if res, finished := rf.cmpJmp(op, src1, src2, int(aux), sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegIsNoneJmpIfFalse, RegIsNoneJmpIfTrue:
			c := regs[src1].IsNone()
			wantTrue := op == RegIsNoneJmpIfTrue
			t := int(aux)
			if c == wantTrue {
				if t <= rf.pc {
					if res, finished := rf.backwardTick(sip, t); finished {
						return res, true
					}
					regs = rf.regs
					continue
				}
				rf.pc = t
				continue
			}
			rf.pc++
		default:
			if res, finished := rf.slow(op, dst, src1, src2, aux, sip); finished {
				return res, true
			}
			regs = rf.regs
		}
	}
	return val.Value{}, false
}

// isRegStr reports whether v is a plain string value eligible for the
// inline compare fast paths.
func isRegStr(v val.Value) bool {
	return v.IsInlineStr() || (v.IsString() && v.AsStringObj().Header.Type == val.StrType)
}

// regCmpOp maps a register compare op to its CMP_* code.
func regCmpOp(op RegOp) uint8 {
	switch op {
	case RegLt:
		return CMP_LT
	case RegLe:
		return CMP_LTE
	case RegEq:
		return CMP_EQ
	case RegNe:
		return CMP_NEQ
	case RegGt:
		return CMP_GT
	case RegGe:
		return CMP_GTE
	case RegIn:
		return CMP_IN
	default:
		return CMP_EQ
	}
}
