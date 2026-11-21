package vm

import (
	"os"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// regAbort aborts Go control flow to the enclosing runFrame when a nested
// register call faults and the stack state has already been unwound to a
// handler (or to the top with vm.unhandledException set). The loopState
// carries the resume state, so the panic value itself is empty.
type regAbort struct{}

// tryNestedRegCall attempts register execution for an in-loop call to
// callee whose arguments are already bound in st.stack[newBase:nextSP]. On
// success it returns the stack-loop continuation with the result at
// stack[retSP]. It returns handled=false when the frame must execute on the
// stack loop. callIP is the call-site ip for exact tracebacks; pass a
// negative callIP to derive and verify it from retIP (bailing when the
// resume slot does not decode to a call).
func (vm *VM) tryNestedRegCall(st *loopState, callee *CodeObject, fn *val.PyFunction, newBase, nextSP, retSP, retIP, callIP int) (int, int, loopCtl, bool) {
	if os.Getenv("FYTHON_DISABLE_NESTED") != "" {
		return 0, 0, ctlNext, false
	}
	if !vm.regCallEligible(st, callee) {
		return 0, 0, ctlNext, false
	}
	// Constructor __init__ calls carry the freshly allocated receiver in the
	// caller slot's initInstance for opReturnSlow's return-self protocol.
	// They stay on the stack path; direct __init__ calls (no marker) may
	// still enter register execution.
	if vm.frames[st.frameIdx].initInstance.HeaderPtr() != nil {
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
// execution. Only loop-bearing substantial callees qualify: the tier must
// cover its nested-runner cost with real loop bodies. Dunder comparisons
// with caller fallback state keep the stack path; everything else is
// governed by the callee's register compilation.
func (vm *VM) regCallEligible(st *loopState, callee *CodeObject) bool {
	rc := callee.RegCode()
	if rc == nil || len(callee.Code) < 24 || callee.Name == "__init__" {
		return false
	}
	if !hasBackwardJump(callee.Code) {
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
// in vm.unhandledException and st.stack refreshed. Cell environments are
// wired exactly like the stack call path so closures execute.
func (vm *VM) nestedRegRun(st *loopState, callee *CodeObject, fn *val.PyFunction, newBase, nextSP int) (val.Value, bool) {
	fnGlobals := st.globals
	if fn != nil && fn.Globals != nil {
		fnGlobals = fn.Globals
	}
	nextFrame := st.frameIdx + 1
	vm.ensureFrameCapacity(nextFrame + 4)
	var cells []val.Value
	if callee.HasCells {
		if !callee.HasCellVars && fn != nil && fn.Closure != nil {
			cells = fn.Closure
		} else {
			cells, _ = vm.setupCalleeCellsForFn(callee, fn, st.stack, newBase)
		}
	}
	res := vm.runFrame(callee, newBase, nextSP, nextFrame, cells, fnGlobals, val.NoneVal(), st.stack, nil, nil)
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
	if len(co.CellVars) == 0 && len(co.FreeVars) > 0 {
		if len(co.FreeVars) > 4 {
			return val.Value{}, false
		}
		if len(st.cells) < len(co.FreeVars) {
			if fn == nil || fn.Closure == nil || len(fn.Closure) < len(co.FreeVars) {
				return val.Value{}, false
			}
		}
	}
	nregs := int(rc.NumRegs)
	frameTop := base + nregs
	if frameTop > len(st.stack) {
		return val.Value{}, false
	}
	// Slow helpers shared with the stack loop (notably loadMethodSlow) write
	// the method PIC unconditionally; make sure the slab is sized. The
	// per-(VM, code) slabs allocate once and the checks are lenient after
	// warmup.
	st.cache.ensureMethodPIC()
	if os.Getenv("FYTHON_DEBUG_REGENTRY") != "" {
		println("regentry", co.Name)
	}
	savedArenaTop := vm.regArenaTop
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
	res, ok := func() (res val.Value, ok bool) {
		defer func() {
			vm.releaseRegFrame(rf)
			if r := recover(); r != nil {
				if _, isAbort := r.(regAbort); isAbort {
					res, ok = val.Value{}, false
					return
				}
				panic(r)
			}
		}()
		return rf.run()
	}()
	vm.discardRegArena(savedArenaTop)
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
	// nested marks an inline register-to-register frame pushed without a
	// fresh loopState. Faults unwind through the shared st and abort with
	// regAbort instead of resuming.
	nested bool
}

// fault unwinds the register frame at mapped stack ip. Register functions
// carry no exception tables, so unwind always runs to the nested-call
// protocol: vm.unhandledException carries the exception to the caller.
// Inline nested frames (nested=true) instead stash the handler continuation
// in st and abort with regAbort: the outer runRegLoop recovers it and the
// enclosing runFrame resumes the stack loop (or returns on top-level exit).
func (rf *regFrame) fault(sip int) (val.Value, bool) {
	rf.st.ip = sip
	rf.st.sp = rf.frameTop
	nip, nsp, ctl := rf.vm.unwind(rf.st, sip)
	if ctl == ctlExit {
		return rf.st.result, true
	}
	if rf.nested {
		rf.st.ip = nip
		rf.st.sp = nsp
		panic(regAbort{})
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
			avC, bvC := regs[src1], regs[src2]
			if avC.IsInt64() && bvC.IsInt64() {
				ai, bi := avC.AsInt64(), bvC.AsInt64()
				var cb bool
				switch op {
				case RegEq:
					cb = ai == bi
				case RegNe:
					cb = ai != bi
				case RegLt:
					cb = ai < bi
				case RegLe:
					cb = ai <= bi
				case RegGt:
					cb = ai > bi
				default:
					cb = ai >= bi
				}
				regs[dst] = val.BoolVal(cb)
				rf.pc++
				continue
			}
			if isRegStr(avC) && isRegStr(bvC) {
				switch op {
				case RegEq, RegNe:
					eq := val.StringEquals(avC, bvC)
					if op == RegNe {
						eq = !eq
					}
					regs[dst] = val.BoolVal(eq)
					rf.pc++
					continue
				case RegLt, RegLe, RegGt, RegGe:
					c := val.StringCompare(avC, bvC)
					var cb bool
					switch op {
					case RegLt:
						cb = c < 0
					case RegLe:
						cb = c <= 0
					case RegGt:
						cb = c > 0
					default:
						cb = c >= 0
					}
					regs[dst] = val.BoolVal(cb)
					rf.pc++
					continue
				}
			}
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
			avJ, bvJ := regs[src1], regs[src2]
			if avJ.IsInt64() && bvJ.IsInt64() {
				ai, bi := avJ.AsInt64(), bvJ.AsInt64()
				var cj bool
				if op == RegEqJmpIfFalse || op == RegEqJmpIfTrue {
					cj = ai == bi
				} else {
					cj = ai < bi
				}
				wantTrueJ := op == RegLtJmpIfTrue || op == RegEqJmpIfTrue
				if cj == wantTrueJ {
					tj := int(aux)
					if tj <= rf.pc {
						if res, finished := rf.backwardTick(sip, tj); finished {
							return res, true
						}
						regs = rf.regs
						continue
					}
					rf.pc = tj
					continue
				}
				rf.pc++
				continue
			}
			if isRegStr(avJ) && isRegStr(bvJ) {
				var cj bool
				if op == RegEqJmpIfFalse || op == RegEqJmpIfTrue {
					cj = val.StringEquals(avJ, bvJ)
				} else {
					cj = val.StringCompare(avJ, bvJ) < 0
				}
				wantTrueJ := op == RegLtJmpIfTrue || op == RegEqJmpIfTrue
				if cj == wantTrueJ {
					tj := int(aux)
					if tj <= rf.pc {
						if res, finished := rf.backwardTick(sip, tj); finished {
							return res, true
						}
						regs = rf.regs
						continue
					}
					rf.pc = tj
					continue
				}
				rf.pc++
				continue
			}
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
		case RegLoadAttr:
			hitAttr := false
			if !val.MultiThreadedFlag.Load() {
				if instObjA := regs[dst].AsInstanceChecked(); instObjA != nil {
					if ishA := instObjA.Shape; ishA != nil {
						if itpA := instObjA.Header.Type; itpA != nil && (itpA.Flags&val.FlagCustomGetAttribute) == 0 {
							tvA := itpA.Version()
							if uint(sip) < uint(len(rf.cache.attrPIC)) {
								if picA := &rf.cache.attrPIC[sip]; picA[0].Shape == ishA && picA[0].TypeVer == uint32(tvA) {
									if idxA := picA[0].SlotIndex; uint(idxA) < uint(len(instObjA.Slots)) {
										if vA := instObjA.Slots[idxA]; !vA.IsUnbound() {
											regs[dst] = vA
											rf.pc++
											hitAttr = true
										}
									}
								}
							}
							if !hitAttr && uint(sip) < uint(len(rf.cache.ic)) {
								if eA := &rf.cache.ic[sip]; eA.ExpectedTypeVer == tvA && eA.Kind == ICKindSlotMember {
									if idxA := eA.SlotIndex; uint(idxA) < uint(len(instObjA.Slots)) {
										if vA := instObjA.Slots[idxA]; !vA.IsUnbound() {
											regs[dst] = vA
											rf.pc++
											hitAttr = true
										}
									}
								}
							}
						}
					}
				}
			}
			if hitAttr {
				continue
			}
			if res, finished := rf.loadAttr(dst, uint16(aux), sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegCallMethod:
			hitCall := false
			argcM := int(aux & 0xFFFF)
			if uint(sip) < uint(len(rf.cache.methodPIC)) {
				if recvM := regs[dst]; !recvM.IsUnbound() {
					if instM := recvM.AsInstanceChecked(); instM != nil {
						if ishM := instM.Shape; ishM != nil {
							if itpM := instM.Header.Type; itpM != nil {
								if picM := &rf.cache.methodPIC[sip]; picM[0].Shape == ishM && picM[0].Shape != nil && itpM.Version() == picM[0].TypeVersion && picM[0].TargetFn != nil {
									if rf.runDirectReg(dst, picM[0].TargetVal, recvM, true, src2, argcM, sip) {
										regs = rf.regs
										hitCall = true
									}
								}
							}
						}
					}
				}
			}
			if hitCall {
				continue
			}
			if uint(sip) < uint(len(rf.cache.ic)) {
				if eM := &rf.cache.ic[sip]; eM.CalleeCache != nil {
					if recvM := regs[dst]; !recvM.IsUnbound() {
						if callM := regs[src1]; callM == eM.CachedVal && !callM.IsNone() && callM.HeaderPtr() != nil {
							if rf.runDirectReg(dst, callM, recvM, true, src2, argcM, sip) {
								regs = rf.regs
								hitCall = true
							}
						}
					} else {
						if callM := regs[src1]; callM == eM.CachedVal && !callM.IsNone() && callM.HeaderPtr() != nil {
							if rf.runDirectReg(dst, callM, val.Value{}, false, src2, argcM, sip) {
								regs = rf.regs
								hitCall = true
							}
						}
					}
				}
			}
			if hitCall {
				continue
			}
			if res, finished := rf.regCallMethod(dst, src1, src2, argcM, sip); finished {
				return res, true
			}
			regs = rf.regs
		case RegCall:
			hitCallC := false
			argcC := int(aux & 0xFFFF)
			if uint(sip) < uint(len(rf.cache.ic)) {
				if eC := &rf.cache.ic[sip]; eC.CalleeCache != nil {
					if tgtC := regs[src1]; tgtC == eC.CachedVal && !tgtC.IsNone() && tgtC.HeaderPtr() != nil {
						if rf.runDirectReg(dst, tgtC, val.Value{}, false, src2, argcC, sip) {
							regs = rf.regs
							hitCallC = true
						}
					}
				}
			}
			if hitCallC {
				continue
			}
			if res, finished := rf.regCall(dst, src1, src2, argcC, sip); finished {
				return res, true
			}
			regs = rf.regs
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
