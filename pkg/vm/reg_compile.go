package vm

import (
	"strings"
	"unsafe"
)

// RegInst is a fixed-width 64-bit 3-address register VM instruction:
//
//	[op:8 (56..63) | rd:8 (48..55) | ra:8 (40..47) | rb:8 (32..39) | aux:32 (0..31)]
type RegInst uint64

// RegOp identifies a hot register VM opcode dispatched in VM.runRegLoop.
type RegOp uint8

const (
	REG_MOV RegOp = iota
	REG_LOAD_CONST
	REG_LOAD_GLOBAL
	REG_LOAD_DEREF
	REG_ADD
	REG_SUB
	REG_MUL
	REG_MOD
	REG_FLOOR_DIV
	REG_BIT_AND
	REG_BIT_OR
	REG_BIT_XOR
	REG_CMP_LT
	REG_CMP_EQ
	REG_CMP_OP
	REG_JMP
	REG_JIF
	REG_JIT
	REG_LOAD_ATTR
	REG_STORE_ATTR
	REG_SUBSCR
	REG_STORE_SUBSCR
	REG_GET_ITER
	REG_FOR_ITER
	REG_SETUP_RANGE
	REG_FOR_ITER_RANGE
	REG_UNPACK_SEQUENCE
	REG_LIST_APPEND
	REG_MAP_ADD
	REG_SET_ADD
	REG_CALL_POS
	REG_CALL_METHOD_PIC
	REG_RETURN
	REG_COLD
)

// RegColdOp identifies an out-of-line sub-opcode dispatched via REG_COLD -> dispatchRegCold.
type RegColdOp uint8

const (
	REG_COLD_CHECK_LOCAL RegColdOp = iota
	REG_COLD_STORE_GLOBAL
	REG_COLD_STORE_DEREF
	REG_COLD_LOAD_CLOSURE
	REG_COLD_DELETE_FAST
	REG_COLD_DELETE_GLOBAL
	REG_COLD_DELETE_ATTR
	REG_COLD_DELETE_SUBSCR
	REG_COLD_UNARY_NEG
	REG_COLD_UNARY_POS
	REG_COLD_UNARY_NOT
	REG_COLD_UNARY_INVERT
	REG_COLD_TRUE_DIV
	REG_COLD_POW
	REG_COLD_LSHIFT
	REG_COLD_RSHIFT
	REG_COLD_MATMUL
	REG_COLD_BINARY_SLICE
	REG_COLD_BUILD_SLICE
	REG_COLD_BUILD_LIST
	REG_COLD_BUILD_TUPLE
	REG_COLD_BUILD_SET
	REG_COLD_BUILD_MAP
	REG_COLD_BUILD_CONST_KEY_MAP
	REG_COLD_BUILD_STRING
	REG_COLD_BUILD_MAP_UNPACK
	REG_COLD_LIST_EXTEND
	REG_COLD_LIST_TO_TUPLE
	REG_COLD_LIST_TO_SET
	REG_COLD_FORMAT_VALUE
	REG_COLD_UNPACK_SEQUENCE
	REG_COLD_UNPACK_EX
	REG_COLD_CALL_KW
	REG_COLD_CALL_EX
	REG_COLD_LOAD_METHOD
	REG_COLD_CALL_METHOD
	REG_COLD_MAKE_FUNCTION
	REG_COLD_BUILD_CLASS
	REG_COLD_IMPORT_NAME
	REG_COLD_IMPORT_FROM
	REG_COLD_IMPORT_STAR
	REG_COLD_RAISE
	REG_COLD_CHECK_EXC_MATCH
	REG_COLD_CHECK_EXC_MATCH_STAR
	REG_COLD_PUSH_EXC_INFO
	REG_COLD_POP_EXCEPT
	REG_COLD_CLEANUP_EXC
	REG_COLD_SETUP_RANGE
	REG_COLD_FOR_ITER_RANGE
	REG_COLD_MATCH_SEQUENCE
	REG_COLD_MATCH_MAPPING
	REG_COLD_MATCH_KEYS
	REG_COLD_MATCH_CLASS
)

const (
	regMaxLocals = 200
	regMaxRegs   = 250
	regUnused    = 255
)

func encodeReg(op RegOp, rd, ra, rb uint8, aux uint32) RegInst {
	return (RegInst(op) << 56) | (RegInst(rd) << 48) | (RegInst(ra) << 40) | (RegInst(rb) << 32) | RegInst(aux)
}

func decodeRegOp(inst RegInst) RegOp   { return RegOp(inst >> 56) }
func decodeRegD(inst RegInst) uint8    { return uint8(inst >> 48) }
func decodeRegA(inst RegInst) uint8    { return uint8(inst >> 40) }
func decodeRegB(inst RegInst) uint8    { return uint8(inst >> 32) }
func decodeRegAux(inst RegInst) uint32 { return uint32(inst) }

// RegExceptionEntry maps a protected register IP range to its handler target and exception register.
type RegExceptionEntry struct {
	StartRegIP  uint32
	EndRegIP    uint32
	TargetRegIP uint32
	TargetReg   uint8
	PushLasti   bool
}

type regColdMeta struct {
	subOp    RegColdOp
	argA     uint8
	extraReg uint8
	argBx    uint16
	tier1PC  uint32
	icIdx    uint16
}

// RegCode holds the immutable Tier-2 3-address register bytecode for a CodeObject.
type RegCode struct {
	code         []RegInst
	codePtr      unsafe.Pointer
	regToTier1PC []uint32
	excTable     []RegExceptionEntry
	coldMeta     []regColdMeta
	numIC        int
	maxRegs      uint8
	numLocals    uint8
	hasIter      bool
}

// HasRegCode reports whether co has been promoted to Tier-2 3-address register bytecode.
func (co *CodeObject) HasRegCode() bool {
	return co != nil && co.regCode.Load() != nil
}

// tier1PC maps a register instruction pointer back to its originating Tier-1 bytecode PC.
func (rc *RegCode) tier1PC(regIP int) int {
	if rc == nil || len(rc.regToTier1PC) == 0 {
		return 0
	}
	if regIP < 0 {
		return int(rc.regToTier1PC[0])
	}
	if regIP >= len(rc.regToTier1PC) {
		return int(rc.regToTier1PC[len(rc.regToTier1PC)-1])
	}
	return int(rc.regToTier1PC[regIP])
}

type localBitset [4]uint64

func (b *localBitset) set(idx int) {
	if uint(idx) < 256 {
		b[idx>>6] |= 1 << (uint(idx) & 63)
	}
}

func (b *localBitset) clear(idx int) {
	if uint(idx) < 256 {
		b[idx>>6] &^= 1 << (uint(idx) & 63)
	}
}

func (b *localBitset) has(idx int) bool {
	if uint(idx) >= 256 {
		return false
	}
	return (b[idx>>6] & (1 << (uint(idx) & 63))) != 0
}

func (b localBitset) intersect(other localBitset) localBitset {
	return localBitset{
		b[0] & other[0],
		b[1] & other[1],
		b[2] & other[2],
		b[3] & other[3],
	}
}

func allLocalsBitset() localBitset {
	return localBitset{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
}

type absSlot struct {
	reg     uint8
	isLocal bool
}

type regJumpFixup struct {
	regIP    int
	targetPC int
}

type regForRangeFixup struct {
	metaIdx  int
	targetPC int
}

// isPureRetargetableRegOp reports whether inst writes exclusively to rd after reading ra/rb
// so a following OP_STORE_FAST can rewrite inst.rd directly to the destination local.
func isPureRetargetableRegOp(op RegOp) bool {
	switch op {
	case REG_LOAD_CONST, REG_LOAD_GLOBAL, REG_LOAD_DEREF,
		REG_ADD, REG_SUB, REG_MUL, REG_MOD, REG_FLOOR_DIV,
		REG_BIT_AND, REG_BIT_OR, REG_BIT_XOR,
		REG_CMP_LT, REG_CMP_EQ, REG_CMP_OP,
		REG_LOAD_ATTR, REG_SUBSCR,
		REG_CALL_POS, REG_CALL_METHOD_PIC:
		return true
	}
	return false
}

// compileRegCode translates an immutable Tier-1 CodeObject.Code stream into a 3-address RegCode.
// Returns nil if the function is ineligible or cannot be cleanly translated (falling back to Tier-1).
func compileRegCode(co *CodeObject) *RegCode {
	if co == nil || len(co.Code) == 0 || len(co.Code) > 65535 {
		return nil
	}
	if co.IsGenerator || co.IsCoroutine || co.CanSuspend ||
		(co.Flags&(CO_GENERATOR|CO_COROUTINE|CO_ASYNC_GENERATOR)) != 0 {
		return nil
	}
	// Preserve Tier-1 bespoke zero-alloc paths for super() methods and VarKWSafe **kwargs forwarders.
	if co.ClassCellIdx >= 0 || co.VarKWSafe {
		return nil
	}
	entryIP := co.EntryIP
	if entryIP < 0 || entryIP >= len(co.Code) {
		return nil
	}
	numLocals := max(co.EffectiveLocals, co.NumLocals, co.NumParams)
	if numLocals > regMaxLocals {
		return nil
	}

	code := co.Code
	n := len(code)

	// Pass 1: discover basic block starts, validate EntryIP is never a jump/exception target,
	// and check opcode eligibility.
	isBlockStart := make([]bool, n+1)
	isBlockStart[entryIP] = true

	for _, e := range co.ExceptionTable {
		if e.StartIP < 0 || e.EndIP > n || e.TargetIP < entryIP || e.TargetIP >= n {
			return nil
		}
		if e.TargetIP == entryIP {
			return nil
		}
		isBlockStart[e.StartIP] = true
		isBlockStart[e.EndIP] = true
		isBlockStart[e.TargetIP] = true
	}

	for pc := entryIP; pc < n; {
		inst := code[pc]
		op := DecodeOpcode(inst)
		step := 1
		switch op {
		case OP_LOAD_FAST_ATTR, OP_RETURN_CONST, OP_RETURN_FAST, OP_LOAD_FAST_2, OP_STORE_LOAD_FAST:
			if pc+1 < n && DecodeOpcode(code[pc+1]) == OP_NOP {
				step = 2
			}
		case OP_COMPARE_JUMP_IF_FALSE:
			target := int(DecodeArgBx(inst))
			if target <= entryIP || target >= n {
				return nil
			}
			isBlockStart[target] = true
			if pc+1 < n && DecodeOpcode(code[pc+1]) == OP_NOP {
				step = 2
			}
			if pc+step < n {
				isBlockStart[pc+step] = true
			}
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_FOR_ITER, OP_FOR_ITER_RANGE:
			target := int(DecodeArgBx(inst))
			if target <= entryIP || target >= n {
				return nil
			}
			isBlockStart[target] = true
			if pc+1 < n {
				isBlockStart[pc+1] = true
			}
		case OP_RETURN, OP_RAISE:
			if pc+1 < n {
				isBlockStart[pc+1] = true
			}
		case OP_LOAD_GLOBAL:
			if int(DecodeArgBx(inst)) < len(co.Consts) && co.Consts[DecodeArgBx(inst)].IsAnyString() &&
				co.Consts[DecodeArgBx(inst)].AsString() == "super" {
				return nil
			}
		case OP_YIELD_VALUE, OP_YIELD_FROM, OP_AWAIT, OP_GET_AWAITABLE,
			OP_LOAD_NAME, OP_STORE_NAME, OP_DELETE_NAME, OP_LOAD_LOCALS,
			OP_LOAD_FROM_DICT_OR_GLOBALS, OP_LOAD_FROM_DICT_OR_DEREF,
			OP_BUILD_TEMPLATE, OP_PRINT_EXPR, OP_STORE_FAST_VIEW:
			return nil
		}
		pc += step
	}

	// Build compact basic block index table for definite-assignment dataflow.
	pcToBlock := make([]int, n+1)
	for i := range pcToBlock {
		pcToBlock[i] = -1
	}
	var blockStarts []int
	var blockEnds []int
	for pc := entryIP; pc < n; pc++ {
		if isBlockStart[pc] {
			if len(blockStarts) > 0 {
				blockEnds = append(blockEnds, pc)
			}
			pcToBlock[pc] = len(blockStarts)
			blockStarts = append(blockStarts, pc)
		}
	}
	if len(blockStarts) == 0 {
		return nil
	}
	blockEnds = append(blockEnds, n)
	numBlocks := len(blockStarts)

	// Pass 2: forward definite-assignment dataflow for UnboundLocalError soundness.
	var initAssigned localBitset
	paramCount := max(co.NumParams, len(co.ParamNames))
	for p := range paramCount {
		initAssigned.set(p)
	}
	for d := range co.DirectCaptureCount {
		initAssigned.set(co.DirectCaptureBase + d)
	}
	if co.VarArgsIdx >= 0 && co.VarArgsIdx < numLocals {
		initAssigned.set(co.VarArgsIdx)
	}
	if co.VarKWIdx >= 0 && co.VarKWIdx < numLocals {
		initAssigned.set(co.VarKWIdx)
	}

	// Build successor lists per block.
	preds := make([][]int, numBlocks)
	addEdge := func(from, toPC int) {
		if toPC >= 0 && toPC < n {
			if tb := pcToBlock[toPC]; tb >= 0 {
				preds[tb] = append(preds[tb], from)
			}
		}
	}
	for b := range numBlocks {
		startPC := blockStarts[b]
		endPC := blockEnds[b]
		lastPC := startPC
		for pc := startPC; pc < endPC; {
			lastPC = pc
			op := DecodeOpcode(code[pc])
			step := 1
			switch op {
			case OP_LOAD_FAST_ATTR, OP_RETURN_CONST, OP_RETURN_FAST, OP_LOAD_FAST_2, OP_STORE_LOAD_FAST, OP_COMPARE_JUMP_IF_FALSE:
				if pc+1 < endPC && DecodeOpcode(code[pc+1]) == OP_NOP {
					step = 2
				}
			}
			pc += step
		}
		lastInst := code[lastPC]
		lastOp := DecodeOpcode(lastInst)
		switch lastOp {
		case OP_JUMP:
			addEdge(b, int(DecodeArgBx(lastInst)))
		case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_FOR_ITER, OP_FOR_ITER_RANGE, OP_COMPARE_JUMP_IF_FALSE:
			addEdge(b, int(DecodeArgBx(lastInst)))
			if endPC < n {
				addEdge(b, endPC)
			}
		case OP_RETURN, OP_RETURN_CONST, OP_RETURN_FAST, OP_RAISE:
			// no normal successor
		default:
			if endPC < n {
				addEdge(b, endPC)
			}
		}
		// Exception edges: any exception table entry overlapping [startPC, endPC)
		// can jump from anywhere inside the block to e.TargetIP.
		for _, e := range co.ExceptionTable {
			if e.StartIP < endPC && e.EndIP > startPC {
				addEdge(b, e.TargetIP)
			}
		}
	}

	blockIn := make([]localBitset, numBlocks)
	blockOut := make([]localBitset, numBlocks)
	blockVisited := make([]bool, numBlocks)
	for b := range numBlocks {
		blockIn[b] = allLocalsBitset()
		blockOut[b] = allLocalsBitset()
	}
	blockIn[0] = initAssigned
	blockVisited[0] = true

	// Note: for exception handlers, an exception may be raised BEFORE any store
	// inside the try block completes, so exception edges conservatively intersect
	// with blockIn[pred] rather than blockOut[pred].
	isExcTarget := make([]bool, numBlocks)
	for _, e := range co.ExceptionTable {
		if tb := pcToBlock[e.TargetIP]; tb >= 0 {
			isExcTarget[tb] = true
		}
	}

	for changed := true; changed; {
		changed = false
		for b := range numBlocks {
			if b > 0 {
				var merged localBitset
				hasPred := false
				for _, p := range preds[b] {
					if !blockVisited[p] {
						continue
					}
					src := blockOut[p]
					if isExcTarget[b] {
						src = src.intersect(blockIn[p])
					}
					if !hasPred {
						merged = src
						hasPred = true
					} else {
						merged = merged.intersect(src)
					}
				}
				if !hasPred {
					continue
				}
				if !blockVisited[b] || merged != blockIn[b] {
					blockIn[b] = merged
					blockVisited[b] = true
					changed = true
				}
			}
			cur := blockIn[b]
			for pc := blockStarts[b]; pc < blockEnds[b]; {
				inst := code[pc]
				op := DecodeOpcode(inst)
				step := 1
				switch op {
				case OP_STORE_FAST, OP_STORE_FAST_VIEW:
					cur.set(int(DecodeArgBx(inst)))
				case OP_STORE_LOAD_FAST:
					cur.set(int(DecodeArgB(inst)))
					if pc+1 < blockEnds[b] && DecodeOpcode(code[pc+1]) == OP_NOP {
						step = 2
					}
				case OP_FOR_ITER_RANGE:
					cur.set(int(DecodeArgA(inst)))
				case OP_DELETE_FAST:
					cur.clear(int(DecodeArgBx(inst)))
				case OP_LOAD_FAST_ATTR, OP_RETURN_CONST, OP_RETURN_FAST, OP_LOAD_FAST_2, OP_COMPARE_JUMP_IF_FALSE:
					if pc+1 < blockEnds[b] && DecodeOpcode(code[pc+1]) == OP_NOP {
						step = 2
					}
				}
				pc += step
			}
			if cur != blockOut[b] {
				blockOut[b] = cur
				changed = true
			}
		}
	}

	// Pass 3: abstract-stack translation to 3-address RegInst.
	var (
		out          []RegInst
		regToTier1PC []uint32
		coldMeta     []regColdMeta
		jumpFixups   []regJumpFixup
		forRFixups   []regForRangeFixup
		nextIC       int
		maxRegsUsed  = numLocals
	)

	pcToRegIP := make([]int, n+1)
	for i := range pcToRegIP {
		pcToRegIP[i] = -1
	}

	entryDepth := make([]int, n+1)
	for i := range entryDepth {
		entryDepth[i] = -1
	}
	entryDepth[entryIP] = 0
	for _, e := range co.ExceptionTable {
		d := e.TargetStackDepth + 1
		if e.PushLasti {
			d++
		}
		if entryDepth[e.TargetIP] >= 0 && entryDepth[e.TargetIP] != d {
			return nil
		}
		entryDepth[e.TargetIP] = d
	}

	tempReg := func(depth int) (uint8, bool) {
		r := numLocals + depth
		if r > regMaxRegs {
			return 0, false
		}
		if r+1 > maxRegsUsed {
			maxRegsUsed = r + 1
		}
		return uint8(r), true
	}

	allocIC := func() (uint16, bool) {
		if nextIC >= 65535 {
			return 0, false
		}
		idx := uint16(nextIC)
		nextIC++
		return idx, true
	}

	emit := func(pc int, op RegOp, rd, ra, rb uint8, aux uint32) int {
		idx := len(out)
		out = append(out, encodeReg(op, rd, ra, rb, aux))
		regToTier1PC = append(regToTier1PC, uint32(pc))
		return idx
	}

	emitCold := func(pc int, subOp RegColdOp, rd, ra, rb uint8, argA uint8, argBx uint16, extraReg uint8, icIdx uint16) (int, int) {
		mIdx := len(coldMeta)
		coldMeta = append(coldMeta, regColdMeta{
			subOp:    subOp,
			argA:     argA,
			extraReg: extraReg,
			argBx:    argBx,
			tier1PC:  uint32(pc),
			icIdx:    icIdx,
		})
		idx := emit(pc, REG_COLD, rd, ra, rb, uint32(mIdx))
		return idx, mIdx
	}

	var stack []absSlot
	blockEmitStart := 0

	// materializeLocal protects any live local alias on the abstract stack before writing local x.
	materializeLocal := func(pc int, x uint8) bool {
		for k := range stack {
			if stack[k].isLocal && stack[k].reg == x {
				tr, ok := tempReg(k)
				if !ok {
					return false
				}
				emit(pc, REG_MOV, tr, x, regUnused, 0)
				stack[k] = absSlot{reg: tr, isLocal: false}
			}
		}
		return true
	}

	// spillAll canonicalizes every abstract stack slot k into tempReg(k) before a block exit.
	spillAll := func(pc int) bool {
		for k := range stack {
			tr, ok := tempReg(k)
			if !ok {
				return false
			}
			if stack[k].reg != tr {
				emit(pc, REG_MOV, tr, stack[k].reg, regUnused, 0)
				stack[k] = absSlot{reg: tr, isLocal: false}
			}
		}
		return true
	}

	// materializeWindow ensures stack[baseDepth : baseDepth+count] occupies contiguous
	// canonical registers tempReg(baseDepth) .. tempReg(baseDepth+count-1).
	materializeWindow := func(pc, baseDepth, count int) (uint8, bool) {
		if baseDepth < 0 || baseDepth+count > len(stack) {
			return 0, false
		}
		baseReg, ok := tempReg(baseDepth)
		if !ok {
			return 0, false
		}
		if count > 0 {
			if _, ok := tempReg(baseDepth + count - 1); !ok {
				return 0, false
			}
		}
		for i := range count {
			tr := uint8(int(baseReg) + i)
			if stack[baseDepth+i].reg != tr {
				emit(pc, REG_MOV, tr, stack[baseDepth+i].reg, regUnused, 0)
				stack[baseDepth+i] = absSlot{reg: tr, isLocal: false}
			}
		}
		return baseReg, true
	}

	recordTargetDepth := func(targetPC, depth int) bool {
		if targetPC < entryIP || targetPC >= n {
			return false
		}
		if entryDepth[targetPC] >= 0 && entryDepth[targetPC] != depth {
			return false
		}
		entryDepth[targetPC] = depth
		return true
	}

	// regReferencedOnStack reports whether r is still live on the abstract stack.
	regReferencedOnStack := func(r uint8) bool {
		for i := range stack {
			if stack[i].reg == r {
				return true
			}
		}
		return false
	}

	// Pre-scan VarNames for PEP 709 comprehension save slots (.save<id>.<name>).
	// Any local with a corresponding .save* slot may be read before assignment by
	// the compiler's save/restore sequence without raising UnboundLocalError.
	var compSavedLocals localBitset
	for _, vn := range co.VarNames {
		if !vn.IsAnyString() {
			continue
		}
		s := vn.AsString()
		if strings.HasPrefix(s, ".save") {
			if dot := strings.IndexByte(s[5:], '.'); dot >= 0 {
				origName := s[5+dot+1:]
				for idx, origVN := range co.VarNames {
					if origVN.IsAnyString() && origVN.AsString() == origName {
						compSavedLocals.set(idx)
					}
				}
			}
		}
	}

	var curAssigned localBitset
	dead := false

	for pc := entryIP; pc < n; {
		switch {
		case isBlockStart[pc]:
			if !dead {
				if !spillAll(pc) {
					return nil
				}
				if !recordTargetDepth(pc, len(stack)) {
					return nil
				}
			}
			pcToRegIP[pc] = len(out)
			blockEmitStart = len(out)
			d := entryDepth[pc]
			if d < 0 {
				// Unreachable block (e.g. dead code after return): skip until next reachable block.
				dead = true
				pc++
				continue
			}
			dead = false
			stack = stack[:0]
			for k := range d {
				tr, ok := tempReg(k)
				if !ok {
					return nil
				}
				stack = append(stack, absSlot{reg: tr, isLocal: false})
			}
			if b := pcToBlock[pc]; b >= 0 {
				curAssigned = blockIn[b]
			}
		case dead:
			pc++
			continue
		default:
			pcToRegIP[pc] = len(out)
		}

		inst := code[pc]
		op := DecodeOpcode(inst)
		argA := DecodeArgA(inst)
		argBx := DecodeArgBx(inst)

		isCompSaveVar := func(idx int) bool {
			return idx >= 0 && idx < len(co.VarNames) && co.VarNames[idx].IsAnyString() &&
				strings.HasPrefix(co.VarNames[idx].AsString(), ".save")
		}

		loadLocal := func(localIdx int) (absSlot, bool) {
			if localIdx < 0 || localIdx >= numLocals {
				return absSlot{}, false
			}
			if !curAssigned.has(localIdx) && !isCompSaveVar(localIdx) && !compSavedLocals.has(localIdx) {
				emitCold(pc, REG_COLD_CHECK_LOCAL, regUnused, uint8(localIdx), regUnused, 0, uint16(localIdx), regUnused, 0)
			}
			return absSlot{reg: uint8(localIdx), isLocal: true}, true
		}

		storeLocal := func(dstIdx int, src absSlot) bool {
			if dstIdx < 0 || dstIdx >= numLocals {
				return false
			}
			dst := uint8(dstIdx)
			if !materializeLocal(pc, dst) {
				return false
			}
			// Direct destination retargeting: if src was just produced by the
			// immediately preceding pure 3-address instruction in this block and
			// is not referenced elsewhere on the abstract stack, write directly to dst.
			if !src.isLocal && len(out) > blockEmitStart && !regReferencedOnStack(src.reg) {
				lastIdx := len(out) - 1
				lastInst := out[lastIdx]
				if isPureRetargetableRegOp(decodeRegOp(lastInst)) && decodeRegD(lastInst) == src.reg {
					out[lastIdx] = encodeReg(
						decodeRegOp(lastInst),
						dst,
						decodeRegA(lastInst),
						decodeRegB(lastInst),
						decodeRegAux(lastInst),
					)
					curAssigned.set(dstIdx)
					return true
				}
			}
			if src.reg != dst {
				emit(pc, REG_MOV, dst, src.reg, regUnused, 0)
			}
			curAssigned.set(dstIdx)
			return true
		}

		switch op {
		case OP_NOP:
			pc++

		case OP_LOAD_CONST:
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_LOAD_CONST, tr, regUnused, regUnused, uint32(argBx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_LOAD_FAST:
			slot, ok := loadLocal(int(argBx))
			if !ok {
				return nil
			}
			stack = append(stack, slot)
			pc++

		case OP_LOAD_FAST_2:
			s1, ok := loadLocal(int(DecodeArgB(inst)))
			if !ok {
				return nil
			}
			s2, ok := loadLocal(int(DecodeArgC(inst)))
			if !ok {
				return nil
			}
			stack = append(stack, s1, s2)
			pc++
			if pc < n && DecodeOpcode(code[pc]) == OP_NOP {
				pc++
			}

		case OP_STORE_FAST, OP_STORE_FAST_VIEW:
			if len(stack) < 1 {
				return nil
			}
			src := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !storeLocal(int(argBx), src) {
				return nil
			}
			pc++

		case OP_STORE_LOAD_FAST:
			if len(stack) < 1 {
				return nil
			}
			src := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !storeLocal(int(DecodeArgB(inst)), src) {
				return nil
			}
			slot, ok := loadLocal(int(DecodeArgC(inst)))
			if !ok {
				return nil
			}
			stack = append(stack, slot)
			pc++
			if pc < n && DecodeOpcode(code[pc]) == OP_NOP {
				pc++
			}

		case OP_POP_TOP:
			if len(stack) < 1 {
				return nil
			}
			stack = stack[:len(stack)-1]
			pc++

		case OP_DUP_TOP:
			if len(stack) < 1 {
				return nil
			}
			top := stack[len(stack)-1]
			stack = append(stack, top)
			pc++

		case OP_LOAD_GLOBAL:
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			emit(pc, REG_LOAD_GLOBAL, tr, regUnused, regUnused, (uint32(argBx)<<16)|uint32(icIdx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_STORE_GLOBAL:
			if len(stack) < 1 {
				return nil
			}
			valSlot := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			emitCold(pc, REG_COLD_STORE_GLOBAL, regUnused, valSlot.reg, regUnused, 0, argBx, regUnused, 0)
			pc++

		case OP_LOAD_DEREF:
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_LOAD_DEREF, tr, regUnused, regUnused, uint32(argBx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_STORE_DEREF:
			if len(stack) < 1 {
				return nil
			}
			valSlot := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			emitCold(pc, REG_COLD_STORE_DEREF, regUnused, valSlot.reg, regUnused, 0, argBx, regUnused, 0)
			pc++

		case OP_LOAD_CLOSURE:
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_LOAD_CLOSURE, tr, regUnused, regUnused, 0, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_DELETE_FAST:
			localIdx := int(argBx)
			if localIdx < 0 || localIdx >= numLocals {
				return nil
			}
			if !materializeLocal(pc, uint8(localIdx)) {
				return nil
			}
			emitCold(pc, REG_COLD_DELETE_FAST, regUnused, uint8(localIdx), regUnused, 0, argBx, regUnused, 0)
			curAssigned.clear(localIdx)
			pc++

		case OP_DELETE_GLOBAL:
			emitCold(pc, REG_COLD_DELETE_GLOBAL, regUnused, regUnused, regUnused, 0, argBx, regUnused, 0)
			pc++

		case OP_DELETE_ATTR:
			if len(stack) < 1 {
				return nil
			}
			objSlot := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			emitCold(pc, REG_COLD_DELETE_ATTR, regUnused, objSlot.reg, regUnused, 0, argBx, regUnused, 0)
			pc++

		case OP_DELETE_SUBSCR:
			if len(stack) < 2 {
				return nil
			}
			keySlot := stack[len(stack)-1]
			objSlot := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			emitCold(pc, REG_COLD_DELETE_SUBSCR, regUnused, objSlot.reg, keySlot.reg, 0, 0, regUnused, 0)
			pc++

		case OP_BINARY_ADD, OP_BINARY_ADD_INT, OP_BINARY_ADD_FLOAT,
			OP_BINARY_SUB, OP_BINARY_SUB_INT,
			OP_BINARY_MUL, OP_BINARY_MOD, OP_BINARY_FLOOR_DIV,
			OP_BINARY_AND, OP_BINARY_OR, OP_BINARY_XOR:
			if len(stack) < 2 {
				return nil
			}
			rb := stack[len(stack)-1].reg
			ra := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			var rop RegOp
			switch op {
			case OP_BINARY_ADD, OP_BINARY_ADD_INT, OP_BINARY_ADD_FLOAT:
				rop = REG_ADD
			case OP_BINARY_SUB, OP_BINARY_SUB_INT:
				rop = REG_SUB
			case OP_BINARY_MUL:
				rop = REG_MUL
			case OP_BINARY_MOD:
				rop = REG_MOD
			case OP_BINARY_FLOOR_DIV:
				rop = REG_FLOOR_DIV
			case OP_BINARY_AND:
				rop = REG_BIT_AND
			case OP_BINARY_OR:
				rop = REG_BIT_OR
			default:
				rop = REG_BIT_XOR
			}
			emit(pc, rop, tr, ra, rb, uint32(DecodeArgA(inst)))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_BINARY_TRUE_DIV, OP_BINARY_POW, OP_BINARY_LSHIFT, OP_BINARY_RSHIFT, OP_BINARY_MATMUL:
			if len(stack) < 2 {
				return nil
			}
			rb := stack[len(stack)-1].reg
			ra := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			var sub RegColdOp
			switch op {
			case OP_BINARY_TRUE_DIV:
				sub = REG_COLD_TRUE_DIV
			case OP_BINARY_POW:
				sub = REG_COLD_POW
			case OP_BINARY_LSHIFT:
				sub = REG_COLD_LSHIFT
			case OP_BINARY_RSHIFT:
				sub = REG_COLD_RSHIFT
			default:
				sub = REG_COLD_MATMUL
			}
			emitCold(pc, sub, tr, ra, rb, 0, 0, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_UNARY_NEGATIVE, OP_UNARY_POSITIVE, OP_UNARY_NOT, OP_UNARY_INVERT:
			if len(stack) < 1 {
				return nil
			}
			ra := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			var sub RegColdOp
			switch op {
			case OP_UNARY_NEGATIVE:
				sub = REG_COLD_UNARY_NEG
			case OP_UNARY_POSITIVE:
				sub = REG_COLD_UNARY_POS
			case OP_UNARY_NOT:
				sub = REG_COLD_UNARY_NOT
			default:
				sub = REG_COLD_UNARY_INVERT
			}
			emitCold(pc, sub, tr, ra, regUnused, 0, 0, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_COMPARE_LT, OP_COMPARE_LT_INT, OP_COMPARE_OP:
			if len(stack) < 2 {
				return nil
			}
			rb := stack[len(stack)-1].reg
			ra := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			cmpOp := argA
			if op == OP_COMPARE_LT || op == OP_COMPARE_LT_INT {
				cmpOp = CMP_LT
			}
			switch cmpOp {
			case CMP_LT:
				emit(pc, REG_CMP_LT, tr, ra, rb, 0)
			case CMP_EQ:
				emit(pc, REG_CMP_EQ, tr, ra, rb, 0)
			default:
				emit(pc, REG_CMP_OP, tr, ra, rb, uint32(cmpOp))
			}
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_COMPARE_JUMP_IF_FALSE:
			if len(stack) < 2 {
				return nil
			}
			rb := stack[len(stack)-1].reg
			ra := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			if !spillAll(pc) {
				return nil
			}
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			switch argA {
			case CMP_LT:
				emit(pc, REG_CMP_LT, tr, ra, rb, 0)
			case CMP_EQ:
				emit(pc, REG_CMP_EQ, tr, ra, rb, 0)
			default:
				emit(pc, REG_CMP_OP, tr, ra, rb, uint32(argA))
			}
			targetPC := int(argBx)
			if !recordTargetDepth(targetPC, len(stack)) {
				return nil
			}
			jIdx := emit(pc, REG_JIF, regUnused, tr, regUnused, 0)
			jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
			pc++
			if pc < n && DecodeOpcode(code[pc]) == OP_NOP {
				pc++
			}

		case OP_JUMP:
			if !spillAll(pc) {
				return nil
			}
			targetPC := int(argBx)
			if !recordTargetDepth(targetPC, len(stack)) {
				return nil
			}
			jIdx := emit(pc, REG_JMP, regUnused, regUnused, regUnused, 0)
			jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
			dead = true
			pc++

		case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE:
			if len(stack) < 1 {
				return nil
			}
			condReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			if !spillAll(pc) {
				return nil
			}
			targetPC := int(argBx)
			if !recordTargetDepth(targetPC, len(stack)) {
				return nil
			}
			rop := REG_JIF
			if op == OP_JUMP_IF_TRUE {
				rop = REG_JIT
			}
			jIdx := emit(pc, rop, regUnused, condReg, regUnused, 0)
			jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
			pc++

		case OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP:
			if len(stack) < 1 {
				return nil
			}
			if !spillAll(pc) {
				return nil
			}
			condReg := stack[len(stack)-1].reg
			targetPC := int(argBx)
			if !recordTargetDepth(targetPC, len(stack)) {
				return nil
			}
			rop := REG_JIF
			if op == OP_JUMP_IF_TRUE_OR_POP {
				rop = REG_JIT
			}
			jIdx := emit(pc, rop, regUnused, condReg, regUnused, 0)
			jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
			// Fallthrough pops the condition.
			stack = stack[:len(stack)-1]
			pc++

		case OP_LOAD_ATTR:
			if len(stack) < 1 {
				return nil
			}
			objReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			emit(pc, REG_LOAD_ATTR, tr, objReg, regUnused, (uint32(argBx)<<16)|uint32(icIdx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_LOAD_FAST_ATTR:
			objSlot, ok := loadLocal(int(argA))
			if !ok {
				return nil
			}
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			emit(pc, REG_LOAD_ATTR, tr, objSlot.reg, regUnused, (uint32(argBx)<<16)|uint32(icIdx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++
			if pc < n && DecodeOpcode(code[pc]) == OP_NOP {
				pc++
			}

		case OP_STORE_ATTR:
			if len(stack) < 2 {
				return nil
			}
			objReg := stack[len(stack)-1].reg
			valReg := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			emit(pc, REG_STORE_ATTR, regUnused, objReg, valReg, (uint32(argBx)<<16)|uint32(icIdx))
			pc++

		case OP_BINARY_SUBSCR:
			if len(stack) < 2 {
				return nil
			}
			keyReg := stack[len(stack)-1].reg
			objReg := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_SUBSCR, tr, objReg, keyReg, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_STORE_SUBSCR:
			if len(stack) < 3 {
				return nil
			}
			keyReg := stack[len(stack)-1].reg
			objReg := stack[len(stack)-2].reg
			valReg := stack[len(stack)-3].reg
			stack = stack[:len(stack)-3]
			emit(pc, REG_STORE_SUBSCR, valReg, objReg, keyReg, 0)
			pc++

		case OP_BINARY_SLICE:
			if len(stack) < 3 {
				return nil
			}
			stopReg := stack[len(stack)-1].reg
			startReg := stack[len(stack)-2].reg
			objReg := stack[len(stack)-3].reg
			stack = stack[:len(stack)-3]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_BINARY_SLICE, tr, objReg, startReg, 0, 0, stopReg, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_BUILD_SLICE:
			count := int(argBx)
			if count != 2 && count != 3 || len(stack) < count {
				return nil
			}
			baseDepth := len(stack) - count
			baseReg, ok := materializeWindow(pc, baseDepth, count)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_BUILD_SLICE, tr, baseReg, uint8(count), 0, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_GET_ITER:
			if len(stack) < 1 {
				return nil
			}
			objReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_GET_ITER, tr, objReg, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_FOR_ITER:
			if len(stack) < 1 {
				return nil
			}
			if !spillAll(pc) {
				return nil
			}
			iterReg := stack[len(stack)-1].reg
			targetPC := int(argBx)
			// On exhaustion, iterator is popped.
			if !recordTargetDepth(targetPC, len(stack)-1) {
				return nil
			}
			// Fuse FOR_ITER + STORE_FAST into direct destination register when safe.
			if pc+1 < n && !isBlockStart[pc+1] {
				nextInst := code[pc+1]
				nextOp := DecodeOpcode(nextInst)
				if (nextOp == OP_STORE_FAST || nextOp == OP_STORE_FAST_VIEW) && int(DecodeArgBx(nextInst)) < numLocals {
					dst := uint8(DecodeArgBx(nextInst))
					if !materializeLocal(pc, dst) {
						return nil
					}
					jIdx := emit(pc, REG_FOR_ITER, dst, iterReg, regUnused, 0)
					jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
					curAssigned.set(int(dst))
					pc += 2
					continue
				}
			}
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			jIdx := emit(pc, REG_FOR_ITER, tr, iterReg, regUnused, 0)
			jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_SETUP_RANGE:
			count := int(argBx)
			if (count != 1 && count != 2) || len(stack) < count {
				return nil
			}
			baseDepth := len(stack) - count
			baseReg, ok := materializeWindow(pc, baseDepth, count)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_SETUP_RANGE, tr, baseReg, uint8(count), uint32(argBx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_FOR_ITER_RANGE:
			if len(stack) < 1 || int(argA) >= numLocals {
				return nil
			}
			if !spillAll(pc) {
				return nil
			}
			iterReg := stack[len(stack)-1].reg
			targetPC := int(argBx)
			if !recordTargetDepth(targetPC, len(stack)-1) {
				return nil
			}
			if !materializeLocal(pc, argA) {
				return nil
			}
			jIdx := emit(pc, REG_FOR_ITER_RANGE, argA, iterReg, regUnused, 0)
			jumpFixups = append(jumpFixups, regJumpFixup{regIP: jIdx, targetPC: targetPC})
			curAssigned.set(int(argA))
			pc++

		case OP_LIST_APPEND:
			depth := int(argBx)
			if len(stack) < depth+1 {
				return nil
			}
			itemReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			listIdx := len(stack) - depth
			if listIdx < 0 {
				return nil
			}
			listReg := stack[listIdx].reg
			iterReg := uint8(regUnused)
			if listIdx+1 < len(stack) {
				iterReg = stack[listIdx+1].reg
			}
			emit(pc, REG_LIST_APPEND, listReg, itemReg, iterReg, uint32(argA))
			pc++

		case OP_MAP_ADD:
			depth := int(argBx)
			if len(stack) < depth+1 || len(stack) < 2 {
				return nil
			}
			valReg := stack[len(stack)-1].reg
			keyReg := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			dictIdx := len(stack) - depth + 1
			if dictIdx < 0 || dictIdx >= len(stack) {
				return nil
			}
			dictReg := stack[dictIdx].reg
			iterReg := uint8(regUnused)
			if dictIdx+1 < len(stack) {
				iterReg = stack[dictIdx+1].reg
			}
			emit(pc, REG_MAP_ADD, dictReg, keyReg, valReg, (uint32(argA)<<8)|uint32(iterReg))
			pc++

		case OP_SET_ADD:
			depth := int(argBx)
			if len(stack) < depth+1 {
				return nil
			}
			itemReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			setIdx := len(stack) - depth
			if setIdx < 0 {
				return nil
			}
			setReg := stack[setIdx].reg
			iterReg := uint8(regUnused)
			if setIdx+1 < len(stack) {
				iterReg = stack[setIdx+1].reg
			}
			emit(pc, REG_SET_ADD, setReg, itemReg, iterReg, uint32(argA))
			pc++

		case OP_BUILD_LIST, OP_BUILD_TUPLE, OP_BUILD_SET, OP_BUILD_STRING, OP_BUILD_MAP_UNPACK:
			count := int(argBx)
			if count > 200 || len(stack) < count {
				return nil
			}
			baseDepth := len(stack) - count
			baseReg, ok := materializeWindow(pc, baseDepth, count)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			var sub RegColdOp
			switch op {
			case OP_BUILD_LIST:
				sub = REG_COLD_BUILD_LIST
			case OP_BUILD_TUPLE:
				sub = REG_COLD_BUILD_TUPLE
			case OP_BUILD_SET:
				sub = REG_COLD_BUILD_SET
			case OP_BUILD_STRING:
				sub = REG_COLD_BUILD_STRING
			default:
				sub = REG_COLD_BUILD_MAP_UNPACK
			}
			emitCold(pc, sub, tr, baseReg, uint8(count), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_BUILD_MAP:
			count := int(argBx)
			items := count * 2
			if items > 200 || len(stack) < items {
				return nil
			}
			baseDepth := len(stack) - items
			baseReg, ok := materializeWindow(pc, baseDepth, items)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_BUILD_MAP, tr, baseReg, uint8(count), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_BUILD_CONST_KEY_MAP:
			count := int(argBx)
			items := count + 1
			if items > 200 || len(stack) < items {
				return nil
			}
			baseDepth := len(stack) - items
			baseReg, ok := materializeWindow(pc, baseDepth, items)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_BUILD_CONST_KEY_MAP, tr, baseReg, uint8(count), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_LIST_EXTEND:
			if len(stack) < 2 {
				return nil
			}
			iterReg := stack[len(stack)-1].reg
			listReg := stack[len(stack)-2].reg
			stack = stack[:len(stack)-1]
			emitCold(pc, REG_COLD_LIST_EXTEND, listReg, iterReg, regUnused, argA, argBx, regUnused, 0)
			pc++

		case OP_LIST_TO_TUPLE, OP_LIST_TO_SET:
			if len(stack) < 1 {
				return nil
			}
			srcReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			sub := REG_COLD_LIST_TO_TUPLE
			if op == OP_LIST_TO_SET {
				sub = REG_COLD_LIST_TO_SET
			}
			emitCold(pc, sub, tr, srcReg, regUnused, 0, 0, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_FORMAT_VALUE:
			hasSpec := argBx != 0
			inCount := 1
			if hasSpec {
				inCount = 2
			}
			if len(stack) < inCount {
				return nil
			}
			var valReg, specReg uint8
			if hasSpec {
				specReg = stack[len(stack)-1].reg
				valReg = stack[len(stack)-2].reg
				stack = stack[:len(stack)-2]
			} else {
				specReg = regUnused
				valReg = stack[len(stack)-1].reg
				stack = stack[:len(stack)-1]
			}
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_FORMAT_VALUE, tr, valReg, specReg, argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_UNPACK_SEQUENCE:
			count := int(argBx)
			if count == 0 || count > 100 || len(stack) < 1 {
				return nil
			}
			seqReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			baseDepth := len(stack)
			baseReg, ok := tempReg(baseDepth)
			if !ok {
				return nil
			}
			if _, ok := tempReg(baseDepth + count - 1); !ok {
				return nil
			}
			emit(pc, REG_UNPACK_SEQUENCE, baseReg, seqReg, uint8(count), uint32(argBx))
			// Tier-1 pushes unpacked items in reverse order so top of stack is item[0].
			for i := range count {
				stack = append(stack, absSlot{reg: uint8(int(baseReg) + i), isLocal: false})
			}
			pc++

		case OP_UNPACK_EX:
			before := int(argA)
			after := int(argBx)
			total := before + 1 + after
			if total > 100 || len(stack) < 1 {
				return nil
			}
			seqReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			baseDepth := len(stack)
			baseReg, ok := tempReg(baseDepth)
			if !ok {
				return nil
			}
			if _, ok := tempReg(baseDepth + total - 1); !ok {
				return nil
			}
			emitCold(pc, REG_COLD_UNPACK_EX, baseReg, seqReg, uint8(total), argA, argBx, regUnused, 0)
			for i := range total {
				stack = append(stack, absSlot{reg: uint8(int(baseReg) + i), isLocal: false})
			}
			pc++

		case OP_CALL:
			argc := int(argA)
			if argBx != CallFromStack {
				// Callee is in consts[argBx]; only argc args are on stack.
				// Bail to Tier-1 for rare constant-callee encoding.
				return nil
			}
			total := argc + 1
			if len(stack) < total {
				return nil
			}
			baseDepth := len(stack) - total
			baseReg, ok := materializeWindow(pc, baseDepth, total)
			if !ok {
				return nil
			}
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_CALL_POS, tr, baseReg, uint8(argc), uint32(icIdx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_CALL_METHOD_PIC:
			argc := int(argA)
			total := argc + 1
			if len(stack) < total {
				return nil
			}
			baseDepth := len(stack) - total
			baseReg, ok := materializeWindow(pc, baseDepth, total)
			if !ok {
				return nil
			}
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_CALL_METHOD_PIC, tr, baseReg, uint8(argc), (uint32(argBx)<<16)|uint32(icIdx))
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_LOAD_METHOD:
			if len(stack) < 1 {
				return nil
			}
			baseDepth := len(stack) - 1
			baseReg, ok := materializeWindow(pc, baseDepth, 1)
			if !ok {
				return nil
			}
			if _, ok := tempReg(baseDepth + 1); !ok {
				return nil
			}
			icIdx, ok := allocIC()
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_LOAD_METHOD, baseReg, baseReg, regUnused, 0, argBx, regUnused, icIdx)
			stack = append(stack, absSlot{reg: uint8(int(baseReg) + 1), isLocal: false})
			pc++

		case OP_CALL_METHOD:
			argc := int(argA)
			total := argc + 2 // [receiver_or_unbound, method, arg0..arg(argc-1)]
			if len(stack) < total {
				return nil
			}
			baseDepth := len(stack) - total
			baseReg, ok := materializeWindow(pc, baseDepth, total)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_CALL_METHOD, tr, baseReg, uint8(argc), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_CALL_KW:
			totalArgs := int(argA)
			total := totalArgs + 1 // [callable, arg0..arg(totalArgs-1)]
			if len(stack) < total {
				return nil
			}
			baseDepth := len(stack) - total
			baseReg, ok := materializeWindow(pc, baseDepth, total)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_CALL_KW, tr, baseReg, uint8(totalArgs), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_CALL_FUNCTION_EX:
			hasKW := (argA & 0x01) != 0
			total := 2
			if hasKW {
				total = 3
			}
			if len(stack) < total {
				return nil
			}
			baseDepth := len(stack) - total
			baseReg, ok := materializeWindow(pc, baseDepth, total)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_CALL_EX, tr, baseReg, uint8(total), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_MAKE_FUNCTION:
			flags := argA
			nPop := 1 // codeVal
			if (flags&0x01) != 0 || (flags&0x10) != 0 {
				nPop += int(argBx)
			}
			if (flags & 0x08) != 0 {
				nPop++
			}
			if (flags & 0x02) != 0 {
				nPop++
			}
			if (flags & 0x04) != 0 {
				nPop++
			}
			if len(stack) < nPop {
				return nil
			}
			baseDepth := len(stack) - nPop
			baseReg, ok := materializeWindow(pc, baseDepth, nPop)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_MAKE_FUNCTION, tr, baseReg, uint8(nPop), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_BUILD_CLASS:
			nPop := 3
			if argA == 1 {
				nPop = 4
			}
			if len(stack) < nPop {
				return nil
			}
			baseDepth := len(stack) - nPop
			baseReg, ok := materializeWindow(pc, baseDepth, nPop)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_BUILD_CLASS, tr, baseReg, uint8(nPop), argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_IMPORT_NAME:
			if len(stack) < 2 {
				return nil
			}
			fromListReg := stack[len(stack)-1].reg
			levelReg := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_IMPORT_NAME, tr, levelReg, fromListReg, argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_IMPORT_FROM:
			if len(stack) < 1 {
				return nil
			}
			modReg := stack[len(stack)-1].reg
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_IMPORT_FROM, tr, modReg, regUnused, argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_IMPORT_STAR:
			if len(stack) < 1 {
				return nil
			}
			modReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			emitCold(pc, REG_COLD_IMPORT_STAR, regUnused, modReg, regUnused, argA, argBx, regUnused, 0)
			pc++

		case OP_RAISE:
			argc := int(argA)
			if argc > 2 || len(stack) < argc {
				return nil
			}
			var ra, rb uint8 = regUnused, regUnused
			switch argc {
			case 1:
				ra = stack[len(stack)-1].reg
				stack = stack[:len(stack)-1]
			case 2:
				rb = stack[len(stack)-1].reg
				ra = stack[len(stack)-2].reg
				stack = stack[:len(stack)-2]
			}
			emitCold(pc, REG_COLD_RAISE, regUnused, ra, rb, argA, argBx, regUnused, 0)
			dead = true
			pc++

		case OP_CHECK_EXC_MATCH:
			if len(stack) < 2 {
				return nil
			}
			targetReg := stack[len(stack)-1].reg
			excReg := stack[len(stack)-2].reg
			stack = stack[:len(stack)-2]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_CHECK_EXC_MATCH, tr, excReg, targetReg, 0, 0, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_CHECK_EXC_MATCH_STAR:
			if len(stack) < 2 {
				return nil
			}
			baseDepth := len(stack) - 2
			baseReg, ok := materializeWindow(pc, baseDepth, 2)
			if !ok {
				return nil
			}
			if _, ok := tempReg(baseDepth + 2); !ok {
				return nil
			}
			// CHECK_EXC_MATCH_STAR has variable stack delta (+1 on match, 0 on miss);
			// bail to Tier-1 for except* blocks to keep abstract stack height static.
			_ = baseReg
			return nil

		case OP_PUSH_EXC_INFO:
			if len(stack) < 1 {
				return nil
			}
			excReg := stack[len(stack)-1].reg
			emitCold(pc, REG_COLD_PUSH_EXC_INFO, regUnused, excReg, regUnused, 0, 0, regUnused, 0)
			pc++

		case OP_POP_EXCEPT:
			emitCold(pc, REG_COLD_POP_EXCEPT, regUnused, regUnused, regUnused, 0, 0, regUnused, 0)
			pc++

		case OP_CLEANUP_EXC:
			if len(stack) < 1 {
				return nil
			}
			stack = stack[:len(stack)-1]
			pc++

		case OP_RETURN:
			if len(stack) < 1 {
				return nil
			}
			retReg := stack[len(stack)-1].reg
			stack = stack[:len(stack)-1]
			emit(pc, REG_RETURN, regUnused, retReg, regUnused, 0)
			dead = true
			pc++

		case OP_RETURN_CONST:
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emit(pc, REG_LOAD_CONST, tr, regUnused, regUnused, uint32(argBx))
			emit(pc, REG_RETURN, regUnused, tr, regUnused, 0)
			dead = true
			pc++
			if pc < n && DecodeOpcode(code[pc]) == OP_NOP {
				pc++
			}

		case OP_RETURN_FAST:
			slot, ok := loadLocal(int(argBx))
			if !ok {
				return nil
			}
			emit(pc, REG_RETURN, regUnused, slot.reg, regUnused, 0)
			dead = true
			pc++
			if pc < n && DecodeOpcode(code[pc]) == OP_NOP {
				pc++
			}

		case OP_MATCH_SEQUENCE, OP_MATCH_MAPPING:
			if len(stack) < 1 {
				return nil
			}
			subjReg := stack[len(stack)-1].reg
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			sub := REG_COLD_MATCH_SEQUENCE
			if op == OP_MATCH_MAPPING {
				sub = REG_COLD_MATCH_MAPPING
			}
			emitCold(pc, sub, tr, subjReg, regUnused, 0, 0, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_MATCH_KEYS:
			if len(stack) < 2 {
				return nil
			}
			keysReg := stack[len(stack)-1].reg
			subjReg := stack[len(stack)-2].reg
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_MATCH_KEYS, tr, subjReg, keysReg, 0, 0, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		case OP_MATCH_CLASS:
			if len(stack) < 3 {
				return nil
			}
			baseDepth := len(stack) - 3
			baseReg, ok := materializeWindow(pc, baseDepth, 3)
			if !ok {
				return nil
			}
			stack = stack[:baseDepth]
			tr, ok := tempReg(len(stack))
			if !ok {
				return nil
			}
			emitCold(pc, REG_COLD_MATCH_CLASS, tr, baseReg, regUnused, argA, argBx, regUnused, 0)
			stack = append(stack, absSlot{reg: tr, isLocal: false})
			pc++

		default:
			// Unknown or unsupported opcode: bail out cleanly to Tier-1.
			return nil
		}
	}

	if len(out) == 0 {
		return nil
	}
	pcToRegIP[n] = len(out)
	// Backfill pcToRegIP for any skipped trailing NOPs or dead PCs so ExceptionTable
	// EndIP/StartIP ranges map cleanly to the next emitted regIP.
	nextReg := len(out)
	for pc := n; pc >= 0; pc-- {
		if pcToRegIP[pc] >= 0 {
			nextReg = pcToRegIP[pc]
		} else {
			pcToRegIP[pc] = nextReg
		}
	}

	// Patch all jump targets and detect back-edges for the static promotion cost model.
	hasBackEdge := false
	for _, f := range jumpFixups {
		tRegIP := pcToRegIP[f.targetPC]
		if tRegIP < 0 || tRegIP >= len(out) {
			return nil
		}
		if tRegIP <= f.regIP {
			hasBackEdge = true
		}
		inst := out[f.regIP]
		out[f.regIP] = encodeReg(decodeRegOp(inst), decodeRegD(inst), decodeRegA(inst), decodeRegB(inst), uint32(tRegIP))
	}
	for _, f := range forRFixups {
		tRegIP := pcToRegIP[f.targetPC]
		if tRegIP < 0 || tRegIP >= len(out) {
			return nil
		}
		coldMeta[f.metaIdx].argBx = uint16(tRegIP)
	}

	// General static promotion cost model: only promote functions containing a loop
	// (back-edge) without heavy Tier-1 call-glue (CALL_EX / BUILD_MAP_UNPACK / CALL_KW)
	// or high static cold-op density (>20%), so register execution stays in the zero-spill
	// inner loop instead of bouncing across the Tier-1 boundary on every iteration.
	if !hasBackEdge {
		return nil
	}
	for i := range coldMeta {
		sub := coldMeta[i].subOp
		if sub == REG_COLD_CALL_EX || sub == REG_COLD_BUILD_MAP_UNPACK || sub == REG_COLD_CALL_KW {
			return nil
		}
	}
	if len(coldMeta)*5 > len(out) {
		return nil
	}

	hasIter := false
	for _, inst := range out {
		if decodeRegOp(inst) == REG_GET_ITER {
			hasIter = true
			break
		}
	}

	// Translate ExceptionTable entries to register IP ranges.
	var excTable []RegExceptionEntry
	if len(co.ExceptionTable) > 0 {
		excTable = make([]RegExceptionEntry, 0, len(co.ExceptionTable))
		for _, e := range co.ExceptionTable {
			sReg := pcToRegIP[e.StartIP]
			eReg := pcToRegIP[e.EndIP]
			tReg := pcToRegIP[e.TargetIP]
			if sReg < 0 || eReg < 0 || tReg < 0 || tReg >= len(out) {
				return nil
			}
			excSlot := numLocals + e.TargetStackDepth
			if e.PushLasti {
				excSlot++
			}
			if excSlot > regMaxRegs {
				return nil
			}
			if excSlot+1 > maxRegsUsed {
				maxRegsUsed = excSlot + 1
			}
			excTable = append(excTable, RegExceptionEntry{
				StartRegIP:  uint32(sReg),
				EndRegIP:    uint32(eReg),
				TargetRegIP: uint32(tReg),
				TargetReg:   uint8(excSlot),
				PushLasti:   e.PushLasti,
			})
		}
	}

	if maxRegsUsed > regMaxRegs {
		return nil
	}

	return &RegCode{
		code:         out,
		codePtr:      unsafe.Pointer(unsafe.SliceData(out)),
		regToTier1PC: regToTier1PC,
		excTable:     excTable,
		coldMeta:     coldMeta,
		numIC:        nextIC,
		maxRegs:      uint8(maxRegsUsed),
		numLocals:    uint8(numLocals),
		hasIter:      hasIter,
	}
}
