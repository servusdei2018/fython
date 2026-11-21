package vm

import "os"

// regTierRejectOp reports whether op forces Tier-2 rejection so the
// register tier executes pure computational kernels while call-heavy and
// generator-heavy functions stay on Tier-1 fused superinstructions.
func regTierRejectOp(op Opcode) bool {
	switch op {
	case OP_CALL, OP_CALL_KW, OP_CALL_FUNCTION_EX, OP_CALL_METHOD,
		OP_CALL_METHOD_PIC, OP_CALL_PY_EXACT_ARGS, OP_CALL_PY_DEFAULTS,
		OP_CALL_BUILTIN_FAST, OP_CALL_LEN, OP_CALL_ISINSTANCE,
		OP_CALL_TYPE_INIT, OP_CALL_METHOD_DESCR, OP_CALL_METHOD_DESCR_FAST,
		OP_CALL_BOUND_NATIVE, OP_CALL_HASATTR, OP_CALL_GETATTR,
		OP_CALL_INSTANCE_DUNDER,
		OP_GET_ITER, OP_FOR_ITER, OP_FOR_ITER_GEN, OP_FOR_ITER_RANGE,
		OP_YIELD_VALUE, OP_YIELD_FROM:
		return true
	default:
		return false
	}
}

func hasRegTierRejectOp(code []uint32) bool {
	for _, inst := range code {
		if regTierRejectOp(DecodeOpcode(inst)) {
			return true
		}
	}
	return false
}

// TryCompileRegCode compiles the stack bytecode of co into 3-address
// register form. It returns nil when co is ineligible for register
// execution; the caller then keeps executing the stack stream.
//
// Eligibility is deliberately strict: every opcode in the stream must have a
// register lowering, control flow must reconcile to identical stack depths
// at every merge, and the total register demand must fit the fixed file.
// Anything else fails closed to the stack loop.
func TryCompileRegCode(co *CodeObject) *RegCode {
	if os.Getenv("FYTHON_DISABLE_REG") != "" {
		recordRegReject(co, "bisect-disabled")
		return nil
	}
	if co == nil || len(co.Code) == 0 {
		recordRegReject(co, "empty-code")
		return nil
	}
	// Tier-2 stays restricted to loop-bearing substantial functions so tiny
	// leaf calls never pay nested runner setup. Small or straight-line code
	// runs faster on the fused Tier-1 stack loop.
	if len(co.Code) < 24 {
		recordRegReject(co, "too-short-no-loop")
		return nil
	}
	if !hasBackwardJump(co.Code) {
		recordRegReject(co, "no-backward-jump")
		return nil
	}
	if len(co.ExceptionTable) != 0 {
		recordRegReject(co, "has-exception-table")
		return nil
	}
	if co.IsGenerator || co.IsCoroutine || co.CanSuspend {
		recordRegReject(co, "is-generator-or-coro")
		return nil
	}
	if len(co.CellVars) != 0 || len(co.FreeVars) != 0 {
		if len(co.CellVars) != 0 || len(co.FreeVars) == 0 || len(co.FreeVars) > 4 {
			recordRegReject(co, "has-cellvars-or-freevars")
			return nil
		}
		for _, inst := range co.Code {
			if DecodeOpcode(inst) == OP_STORE_DEREF {
				recordRegReject(co, "has-cellvars-or-freevars")
				return nil
			}
		}
		for _, inst := range co.Code {
			if DecodeOpcode(inst) == OP_LOAD_DEREF {
				idx := int(DecodeArgBx(inst))
				if idx < 0 || idx >= len(co.FreeVars) {
					recordRegReject(co, "has-cellvars-or-freevars")
					return nil
				}
			}
		}
	}
	// Keep the tier to call-free and generator-free loop kernels so
	// call-heavy and generator-heavy workloads stay on Tier-1 in-loop
	// drains and fused superinstructions.
	if hasRegTierRejectOp(co.Code) {
		recordRegReject(co, "has-call-or-iter-yield")
		return nil
	}
	if !co.ParamShapeComputed {
		co.computeParamShape()
	}
	scratchBase := co.EffectiveLocals
	scratchBase = max(scratchBase, co.NumLocals)
	if scratchBase >= regMaxRegs {
		recordRegReject(co, "scratch-base-overflow")
		return nil
	}
	if co.MaxStackSize > 0 && scratchBase+co.MaxStackSize > regMaxRegs {
		recordRegReject(co, "register-overflow")
		return nil
	}
	entry := co.EntryIP
	if entry < 0 || entry >= len(co.Code) {
		recordRegReject(co, "bad-entry-ip")
		return nil
	}
	b := &regBuilder{
		co:          co,
		scratchBase: scratchBase,
		entry:       entry,
		nextReg:     scratchBase + 1,
	}
	b.producer = make([]int32, regMaxRegs)
	b.prodBlock = make([]int32, regMaxRegs)
	b.refcnt = make([]int32, regMaxRegs)
	for i := range b.producer {
		b.producer[i] = -1
		b.prodBlock[i] = -1
	}
	// Reserve one scratch register as the edge-reconciliation temp. It is
	// never allocated for values, so parallel-move conflicts always have a
	// safe intermediate.
	b.moveTemp = b.allocScratch()
	if b.moveTemp < 0 {
		recordRegReject(co, "scratch-temp-exhausted")
		return nil
	}
	if !b.build() {
		if b.rejectReason == "" {
			b.rejectReason = "build-failed"
		}
		recordRegReject(co, b.rejectReason)
		return nil
	}
	return &RegCode{
		Code:        b.code,
		StackPC:     b.stackPC,
		NumRegs:     uint16(b.nextReg),
		ScratchBase: uint16(scratchBase),
		RetLive:     b.retLive,
	}
}

// hasBackwardJump reports whether code contains a backward jump edge, the
// marker of a real loop body where register-window savings can cover the
// nested runner cost. Both base and fused jump forms use an absolute Bx
// target, so any target at or before the jump site counts.
func hasBackwardJump(code []uint32) bool {
	for i, inst := range code {
		switch DecodeOpcode(inst) {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_FOR_ITER, OP_FOR_ITER_RANGE, OP_FOR_ITER_GEN,
			OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE,
			OP_FAST_LOOP_CMP_LT_CONST_JUMP,
			OP_FAST_CMP_EQ_JUMP_IF_FALSE,
			OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE,
			OP_JUMP_IF_BITAND_ZERO,
			OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE:
			t := int(DecodeArgBx(inst))
			if t >= 0 && t <= i {
				return true
			}
		}
	}
	return false
}

// regMaxRegs bounds the register file. Locals occupy [0, scratchBase) and
// scratch registers [scratchBase, regMaxRegs).
const regMaxRegs = 240

// regBuilder holds the abstract-interpretation state of the stack to
// register compilation.
type regBuilder struct {
	co          *CodeObject
	scratchBase int
	entry       int

	code    []RegInstr
	stackPC []int32
	retLive []regRetLive

	// nextReg is the high-water mark for scratch allocation.
	nextReg int
	// moveTemp is a reserved scratch register for parallel-move conflicts.
	moveTemp int
	// freeList holds dead scratch registers available for reuse.
	freeList []int
	// refcnt tracks live references per scratch register from the abstract
	// stack and recorded block entries. Locals are pinned and untracked.
	refcnt []int32
	// producer maps a scratch register to the emitted position of the
	// instruction that defined it (-1 when unknown). prodBlock records the
	// stack basic block of that definition for peephole coalescing.
	producer  []int32
	prodBlock []int32

	// leaders marks stack ips that start a basic block. entries records
	// the abstract stack on entry per block start; recorded registers hold
	// one reference each, keeping merge values alive for every path.
	leaders  map[int]bool
	entries  map[int][]uint8
	hasEntry map[int]bool

	// pcOf maps a stack block start to the register pc of its first real
	// instruction, skipping fallthrough reconciliation MOVs that jump
	// arrivals must not execute.
	pcOf map[int]int

	// jumps holds unconditional transfers to backpatch. trampFixups holds
	// conditional transfers whose trampoline pc is assigned later.
	jumps       []regFixup
	trampFixups []regTrampFixup
	trampolines []regTrampoline
	vstack      []uint8
	reachable   bool
	blockID     int32
	failed      bool
	// targeted marks stack ips that are explicit jump targets. Compare
	// fusion refuses to span an instruction others jump to.
	targeted map[int]bool
	// fusedJump reports that lower fused a compare with the following
	// conditional jump, so the driver skips an extra instruction and
	// closes the block.
	fusedJump bool
	// rejectReason records the first failure cause for opstats telemetry.
	rejectReason string
}

type regFixup struct {
	// code index of the jump instruction to backpatch.
	pos int
	// target stack ip to resolve to a register pc.
	target int
}

type regTrampFixup struct {
	pos      int
	trampIdx int
}

type regTrampoline struct {
	target int
	moves  [][2]int
	pc     int
}

type regRetLive struct {
	// code position of the REG_RETURN instruction.
	pos int
	// live scratch registers possibly holding ephemeral iterators.
	bits [4]uint64
}

func (b *regBuilder) fail() bool {
	b.failed = true
	return false
}

// rejectf records the first rejection reason for telemetry.
func (b *regBuilder) rejectf(reason string) {
	if b.rejectReason == "" {
		b.rejectReason = reason
	}
}

func (b *regBuilder) allocScratch() int {
	if len(b.freeList) > 0 {
		r := b.freeList[len(b.freeList)-1]
		b.freeList = b.freeList[:len(b.freeList)-1]
		return r
	}
	if b.nextReg >= regMaxRegs {
		return -1
	}
	r := b.nextReg
	b.nextReg++
	return r
}

func (b *regBuilder) isScratch(r int) bool {
	return r >= b.scratchBase && r < regMaxRegs
}

// retain records an additional live reference to a scratch register.
func (b *regBuilder) retain(r int) {
	if b.isScratch(r) {
		b.refcnt[r]++
	}
}

// release drops a live reference. A scratch register with no remaining
// references returns to the free list.
func (b *regBuilder) release(r int) {
	if !b.isScratch(r) || b.refcnt[r] <= 0 {
		return
	}
	b.refcnt[r]--
	if b.refcnt[r] == 0 {
		b.producer[r] = -1
		b.prodBlock[r] = -1
		b.freeList = append(b.freeList, r)
	}
}

// hold protects a popped scratch register across edge recording. Popping
// releases the register into the free list, but the condition of a
// conditional jump (or the sources of a fused compare-jump) stays live
// until its jump is emitted; recordEntry's local normalization must not
// recycle the register mid-shuffle. hold takes it back out of the free
// list and retains it; unhold releases it once the jump is emitted.
func (b *regBuilder) hold(r int) {
	if !b.isScratch(r) {
		return
	}
	b.retain(r)
	for i, v := range b.freeList {
		if v == r {
			b.freeList = append(b.freeList[:i], b.freeList[i+1:]...)
			return
		}
	}
}

// unhold releases a register previously protected by hold.
func (b *regBuilder) unhold(r int) {
	b.release(r)
}

// peekWindow returns the top n abstract-stack registers bottom-to-top
// without popping. It lets callers allocate a helper window before the
// shuffle sources are released, so a reused window can never alias a live
// source mid-shuffle.
func (b *regBuilder) peekWindow(n int) ([]int, bool) {
	if n <= 0 || n > len(b.vstack) {
		b.fail()
		return nil, false
	}
	out := make([]int, n)
	for i := range n {
		out[i] = int(b.vstack[len(b.vstack)-n+i])
	}
	return out, true
}

// drop pops n entries, releasing their references.
func (b *regBuilder) drop(n int) bool {
	for range n {
		b.pop()
		if b.failed {
			return false
		}
	}
	return true
}

// allocWindow reserves count consecutive scratch registers for helper-call
// windows. The window start doubles as the result slot. Freed windows are
// reused LIFO when the tail run fits, else the high-water mark advances.
func (b *regBuilder) allocWindow(count int) (int, bool) {
	if count <= 0 || count > regMaxRegs {
		return -1, false
	}
	if len(b.freeList) >= count {
		base := len(b.freeList) - count
		w := b.freeList[base]
		ok := true
		for i := 1; i < count; i++ {
			if b.freeList[base+i] != w+i {
				ok = false
				break
			}
		}
		if ok {
			b.freeList = b.freeList[:base]
			if w+count > b.nextReg {
				b.nextReg = w + count
			}
			return w, true
		}
	}
	if b.nextReg+count > regMaxRegs {
		return -1, false
	}
	w := b.nextReg
	b.nextReg += count
	return w, true
}

// freeWindow returns a dead helper-call window to the free list, keeping
// the consecutive run intact for the next window.
func (b *regBuilder) freeWindow(w, count int) {
	for i := w; i < w+count; i++ {
		b.producer[i] = -1
		b.prodBlock[i] = -1
		b.freeList = append(b.freeList, i)
	}
}

func (b *regBuilder) emit(op RegOp, dst, src1, src2 int, aux uint32, stackIP int) int {
	pos := len(b.code)
	b.code = append(b.code, EncodeReg(op, uint8(dst), uint8(src1), uint8(src2), aux))
	b.stackPC = append(b.stackPC, int32(stackIP))
	return pos
}

func (b *regBuilder) push(r int) bool {
	if r < 0 || r >= regMaxRegs {
		return b.fail()
	}
	b.vstack = append(b.vstack, uint8(r))
	b.retain(r)
	return true
}

func (b *regBuilder) pop() int {
	if len(b.vstack) == 0 {
		b.fail()
		return -1
	}
	r := b.vstack[len(b.vstack)-1]
	b.vstack = b.vstack[:len(b.vstack)-1]
	b.release(int(r))
	return int(r)
}

func (b *regBuilder) endBlock() {
	for _, r := range b.vstack {
		b.release(int(r))
	}
	b.vstack = b.vstack[:0]
	b.blockID++
	for i := range b.producer {
		b.producer[i] = -1
		b.prodBlock[i] = -1
	}
	b.reachable = false
}

// recordEntry normalizes the arriving abstract stack into the recorded
// entry for target. Bare locals in surviving slots are copied to fresh
// scratch so later edge reconciliation never writes into a variable. MOVs
// are emitted at posIP; the entry is keyed by target.
func (b *regBuilder) recordEntry(posIP, target int, vstack []uint8) ([]uint8, bool) {
	rec := make([]uint8, len(vstack))
	copy(rec, vstack)
	for i, r := range rec {
		if int(r) < b.scratchBase {
			s := b.allocScratch()
			if s < 0 {
				b.rejectf("register-overflow")
				return nil, false
			}
			b.emit(RegMov, s, int(r), 0, 0, posIP)
			b.producer[s] = int32(len(b.code) - 1)
			b.prodBlock[s] = b.blockID
			rec[i] = uint8(s)
			b.retain(s)
		} else {
			b.retain(int(r))
		}
	}
	b.entries[target] = rec
	b.hasEntry[target] = true
	return rec, true
}

// reconcilePlans the register copies needed to move from have to want.
// Transfers may abandon values: have is allowed to be longer than want,
// in which case only the shared bottom prefix is reconciled and the dead
// top is dropped by the caller. A shorter have fails.
func (b *regBuilder) reconcile(want, have []uint8) ([][2]int, bool) {
	if len(have) < len(want) {
		return nil, false
	}
	var mv [][2]int
	for i := range want {
		if want[i] == have[i] {
			continue
		}
		part, ok := b.reconcileMove(int(want[i]), int(have[i]), have)
		if !ok {
			return nil, false
		}
		mv = append(mv, part...)
	}
	return mv, true
}

// reconcileMove plans copying src into dst for an edge transfer, routing
// through the reserved temp when dst is also a source on this path.
func (b *regBuilder) reconcileMove(dst, src int, have []uint8) ([][2]int, bool) {
	if dst == src {
		return nil, true
	}
	if !b.isScratch(dst) {
		// Reconciliation must never write into a variable.
		return nil, false
	}
	for _, r := range have {
		if int(r) == dst {
			return [][2]int{{b.moveTemp, src}, {dst, b.moveTemp}}, true
		}
	}
	return [][2]int{{dst, src}}, true
}

// edgeTo records the transfer from the current abstract stack to target.
// Unconditional transfers emit reconciliation MOVs inline; conditional
// transfers defer them to a trampoline so the fallthrough path never
// executes them.
func (b *regBuilder) edgeTo(ip, target int, conditional bool) bool {
	if b.failed {
		return false
	}
	if !b.hasEntry[target] {
		rec, ok := b.recordEntry(ip, target, b.vstack)
		if !ok {
			return b.fail()
		}
		if conditional {
			// Normalization copies live in the trampoline.
			var mv [][2]int
			for i, r := range b.vstack {
				if int(r) < b.scratchBase {
					mv = append(mv, [2]int{int(rec[i]), int(r)})
				}
			}
			b.trampolines = append(b.trampolines, regTrampoline{target: target, moves: mv})
		}
		return true
	}
	mv, ok := b.reconcile(b.entries[target], b.vstack)
	if !ok {
		b.rejectf("merge-depth-mismatch")
		return b.fail()
	}
	if conditional {
		b.trampolines = append(b.trampolines, regTrampoline{target: target, moves: mv})
		return true
	}
	for _, m := range mv {
		b.emit(RegMov, m[0], m[1], 0, 0, ip)
	}
	return true
}

// build runs the abstract interpretation and emission.
func (b *regBuilder) build() bool {
	n := len(b.co.Code)
	b.leaders = make(map[int]bool, n/4+2)
	b.entries = make(map[int][]uint8)
	b.hasEntry = make(map[int]bool)
	b.pcOf = make(map[int]int, n/4+2)
	b.leaders[b.entry] = true
	b.targeted = make(map[int]bool, n/4+2)
	for ip := b.entry; ip < n; ip++ {
		inst := b.co.Code[ip]
		switch DecodeOpcode(inst) {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_FOR_ITER, OP_FOR_ITER_RANGE:
			t := int(DecodeArgBx(inst))
			if t < b.entry || t >= n {
				b.rejectf("bad-jump-target")
				return false
			}
			b.leaders[t] = true
			b.targeted[t] = true
			switch DecodeOpcode(inst) {
			case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
				OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
				OP_FOR_ITER, OP_FOR_ITER_RANGE:
				if ip+1 < n {
					b.leaders[ip+1] = true
				}
			}
		}
	}

	// The entry block is reachable by definition with an empty stack.
	b.reachable = true
	for ip := b.entry; ip < n; {
		inst := b.co.Code[ip]
		op := DecodeOpcode(inst)
		if b.leaders[ip] {
			switch {
			case !b.reachable:
				// Unreachable fallthrough into a targeted block: adopt
				// the recorded entry with no reconciliation MOVs.
				want, ok := b.entries[ip]
				if !ok {
					b.rejectf("missing-entry-unreachable")
					return false
				}
				b.vstack = append(b.vstack[:0], want...)
				for _, r := range b.vstack {
					b.retain(int(r))
				}
				b.reachable = true
			case !b.hasEntry[ip]:
				if _, ok := b.recordEntry(ip, ip, b.vstack); !ok {
					b.rejectf("record-entry-failed")
					return false
				}
			default:
				// Later fallthrough arrival: reconcile to the entry.
				want := b.entries[ip]
				mv, ok := b.reconcile(want, b.vstack)
				if !ok {
					b.rejectf("merge-depth-mismatch")
					return false
				}
				for _, m := range mv {
					b.emit(RegMov, m[0], m[1], 0, 0, ip)
				}
				for _, r := range b.vstack {
					b.release(int(r))
				}
				b.vstack = append(b.vstack[:0], want...)
				for _, r := range b.vstack {
					b.retain(int(r))
				}
			}
			// Jump arrivals skip the fallthrough MOVs above.
			b.pcOf[ip] = len(b.code)
		} else if !b.reachable {
			// Dead code between a transfer and the next leader.
			ip++
			continue
		}
		if !b.lower(op, inst, ip) || b.failed {
			if b.rejectReason == "" {
				b.rejectReason = "no-lowering:" + op.String()
			}
			return false
		}
		switch op {
		case OP_JUMP, OP_RETURN, OP_RAISE:
			b.endBlock()
			ip++
		case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE,
			OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_FOR_ITER, OP_FOR_ITER_RANGE:
			b.blockID++
			for i := range b.producer {
				b.producer[i] = -1
				b.prodBlock[i] = -1
			}
			ip++
		default:
			if b.fusedJump {
				b.fusedJump = false
				b.blockID++
				for i := range b.producer {
					b.producer[i] = -1
					b.prodBlock[i] = -1
				}
				ip += 2
			} else {
				ip++
			}
		}
	}

	// Emit trampolines for conditional edges.
	for i := range b.trampolines {
		t := &b.trampolines[i]
		t.pc = len(b.code)
		for _, m := range t.moves {
			b.emit(RegMov, m[0], m[1], 0, 0, -1)
		}
		b.jumps = append(b.jumps, regFixup{pos: len(b.code), target: t.target})
		b.emit(RegJmp, 0, 0, 0, 0, -1)
	}
	for _, f := range b.trampFixups {
		t := &b.trampolines[f.trampIdx]
		inst := b.code[f.pos]
		b.code[f.pos] = EncodeReg(DecodeRegOp(inst), DecodeRegDst(inst), DecodeRegSrc1(inst), DecodeRegSrc2(inst), uint32(t.pc))
	}
	for _, j := range b.jumps {
		pc, ok := b.pcOf[j.target]
		if !ok {
			b.rejectf("missing-jump-target")
			return false
		}
		inst := b.code[j.pos]
		b.code[j.pos] = EncodeReg(DecodeRegOp(inst), DecodeRegDst(inst), DecodeRegSrc1(inst), DecodeRegSrc2(inst), uint32(pc))
	}

	// The entry states hold references; drop them now that compilation is
	// complete. The runtime register file starts clean.
	for _, rec := range b.entries {
		for _, r := range rec {
			b.release(int(r))
		}
	}
	if b.failed {
		if b.rejectReason == "" {
			b.rejectReason = "build-failed"
		}
		return false
	}
	if !b.verify() {
		b.rejectf("verify-failed")
		return false
	}
	return true
}

// singleDef reports whether op defines exactly one destination register
// independent of its inputs, making STORE_FAST dst coalescing sound.
// Window and in-place ops (calls, builds, attributes, iteration) are
// excluded: their inputs are positionally tied to the result slot.
func (b *regBuilder) singleDef(op RegOp) bool {
	switch op {
	case RegLoadConst, RegLoadGlobal, RegLoadDeref,
		RegAdd, RegSub, RegMul, RegTrueDiv, RegFloorDiv, RegMod,
		RegPow, RegMatMul, RegBitAnd, RegBitOr, RegBitXor,
		RegLShift, RegRShift, RegNeg, RegPos, RegInvert, RegNot,
		RegEq, RegNe, RegLt, RegLe, RegGt, RegGe, RegIn, RegIs,
		RegSetupRange, RegGetItem, RegDupTop:
		return true
	default:
		return false
	}
}

// verify re-checks the emitted stream: every register in range and every
// jump target in range.
func (b *regBuilder) verify() bool {
	nregs := b.nextReg
	for _, inst := range b.code {
		for _, r := range []uint8{DecodeRegDst(inst), DecodeRegSrc1(inst), DecodeRegSrc2(inst)} {
			if int(r) >= nregs {
				b.rejectf("verify-reg-range")
				return false
			}
		}
		switch DecodeRegOp(inst) {
		case RegJmp, RegJmpIfFalse, RegJmpIfTrue,
			RegLtJmpIfFalse, RegLtJmpIfTrue,
			RegEqJmpIfFalse, RegEqJmpIfTrue,
			RegIsNoneJmpIfFalse, RegIsNoneJmpIfTrue,
			RegForIter, RegForIterRange:
			if int(DecodeRegAux(inst)) >= len(b.code) {
				b.rejectf("verify-jump-range")
				return false
			}
		}
	}
	return true
}
