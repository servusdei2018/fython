package vm

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// deoptInst replaces a Tier-2 fused instruction at ip with its generic bytecode
// sequence, updates st.codePtr / st.codeLen, and returns the new codePtr.
//
//go:noinline
func (vm *VM) deoptInst(st *loopState, ip int, inst uint32) unsafe.Pointer {
	op := Opcode(inst >> 24)
	var nextWord uint32
	if ip+1 < st.codeLen {
		nextWord = *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))
	}
	var ptr unsafe.Pointer
	var length int
	switch op {
	case OP_LOAD_FAST_ATTR_MUL_SELF:
		reg := uint8(inst >> 16)
		attrIdx := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, uint16(reg))
			c[ip+1] = Encode(OP_LOAD_ATTR, 0, attrIdx)
			c[ip+2] = Encode(OP_LOAD_FAST, 0, uint16(reg))
			c[ip+3] = Encode(OP_LOAD_ATTR, 0, attrIdx)
			c[ip+4] = Encode(OP_BINARY_MUL, 0, 0)
		})
	case OP_LOAD_FAST_ATTR:
		reg := uint8(inst >> 16)
		attrIdx := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, uint16(reg))
			c[ip+1] = Encode(OP_LOAD_ATTR, 0, attrIdx)
		})
	case OP_STORE_FAST_ATTR:
		reg := uint8(inst >> 16)
		attrIdx := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, uint16(reg))
			c[ip+1] = Encode(OP_STORE_ATTR, 0, attrIdx)
		})
	case OP_FAST_ADD_INT_LOCAL_TO_LOCAL:
		dstReg, srcRegA, srcRegB := uint8(inst>>16), uint8(inst>>8), uint8(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastAdd(c, ip, dstReg, srcRegA, srcRegB)
		})
	case OP_FAST_INC_INT_LOCAL_CONST:
		reg, constIdx := uint8(inst>>16), uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastInc(c, ip, reg, constIdx)
		})
	case OP_FAST_LOOP_CMP_LT_CONST_JUMP:
		reg, exitTarget := uint8(inst>>16), uint16(inst)
		constIdx := uint16(nextWord)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastLoop(c, ip, reg, constIdx, exitTarget)
		})
	case OP_FAST_SUB_INT_LOCAL_TO_LOCAL:
		dstReg, srcRegA, srcRegB := uint8(inst>>16), uint8(inst>>8), uint8(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastSub(c, ip, dstReg, srcRegA, srcRegB)
		})
	case OP_FAST_ADD_INT_LOCALS:
		srcA, srcB := uint8(inst>>16), uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, uint16(srcA))
			c[ip+1] = Encode(OP_LOAD_FAST, 0, srcB)
			c[ip+2] = Encode(OP_BINARY_ADD, 0, 0)
		})
	case OP_FAST_SUB_INT_LOCALS:
		srcA, srcB := uint8(inst>>16), uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, uint16(srcA))
			c[ip+1] = Encode(OP_LOAD_FAST, 0, srcB)
			c[ip+2] = Encode(OP_BINARY_SUB, 0, 0)
		})
	case OP_FAST_CMP_EQ_JUMP_IF_FALSE:
		srcA, exitTarget := uint8(inst>>16), uint16(inst)
		srcB := uint8(nextWord >> 16)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastCmpEq(c, ip, srcA, srcB, exitTarget)
		})
	case OP_FAST_SUBSCR_DEREF_LIST_LOCAL:
		dstReg, cellIdx, idxReg := uint8(inst>>16), uint8(inst>>8), uint8(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastSubscr(c, ip, dstReg, cellIdx, idxReg)
		})
	case OP_FAST_NEG_INT_LOCAL:
		reg := uint8(inst >> 16)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			DeoptimizeFastNeg(c, ip, reg)
		})
	case OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE:
		cmpOp := uint8(inst >> 16)
		target := uint16(inst)
		jumpOp := OP_JUMP_IF_FALSE
		if op == OP_COMPARE_JUMP_IF_TRUE {
			jumpOp = OP_JUMP_IF_TRUE
		}
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_COMPARE_OP, cmpOp, 0)
			c[ip+1] = Encode(jumpOp, 0, target)
		})
	case OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE:
		cmpOp := uint8(inst >> 16)
		constIdx := uint16(uint8(inst >> 8))
		target := uint16(ip) + uint16(uint8(inst))
		jumpOp := OP_JUMP_IF_FALSE
		if op == OP_CMP_CONST_JUMP_IF_TRUE {
			jumpOp = OP_JUMP_IF_TRUE
		}
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_CONST, 0, constIdx)
			c[ip+1] = Encode(OP_COMPARE_OP, cmpOp, 0)
			c[ip+2] = Encode(jumpOp, 0, target)
		})
	case OP_RETURN_CONST:
		constIdx := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_CONST, 0, constIdx)
			c[ip+1] = Encode(OP_RETURN, 0, 0)
		})
	case OP_RETURN_FAST:
		reg := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, reg)
			c[ip+1] = Encode(OP_RETURN, 0, 0)
		})
	case OP_LOAD_FAST_CONST:
		reg := uint16(uint8(inst >> 16))
		constIdx := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, reg)
			c[ip+1] = Encode(OP_LOAD_CONST, 0, constIdx)
		})
	case OP_LOAD_FAST_2:
		r1, r2 := uint16(uint8(inst>>8)), uint16(uint8(inst))
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, r1)
			c[ip+1] = Encode(OP_LOAD_FAST, 0, r2)
		})
	case OP_STORE_LOAD_FAST:
		dst, srcReg := uint16(uint8(inst>>8)), uint16(uint8(inst))
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_STORE_FAST, 0, dst)
			c[ip+1] = Encode(OP_LOAD_FAST, 0, srcReg)
		})
	case OP_LOAD_GLOBAL_FAST:
		gIdx, reg := uint16(uint8(inst>>8)), uint16(uint8(inst))
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_GLOBAL, 0, gIdx)
			c[ip+1] = Encode(OP_LOAD_FAST, 0, reg)
		})
	case OP_POP_LOAD_FAST:
		reg := uint16(inst)
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_POP_TOP, 0, 0)
			c[ip+1] = Encode(OP_LOAD_FAST, 0, reg)
		})
	case OP_SUBSCR_FAST_FAST:
		r1, r2 := uint16(uint8(inst>>8)), uint16(uint8(inst))
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, r1)
			c[ip+1] = Encode(OP_LOAD_FAST, 0, r2)
			c[ip+2] = Encode(OP_BINARY_SUBSCR, 0, 0)
		})
	case OP_SUBSCR_FAST_CONST:
		reg, constIdx := uint16(uint8(inst>>8)), uint16(uint8(inst))
		ptr, length = deoptimizeAt(st.curCO, ip, func(c []uint32) {
			c[ip] = Encode(OP_LOAD_FAST, 0, reg)
			c[ip+1] = Encode(OP_LOAD_CONST, 0, constIdx)
			c[ip+2] = Encode(OP_BINARY_SUBSCR, 0, 0)
		})
	}
	st.codePtr = ptr
	st.codeLen = length
	return ptr
}

//go:noinline
func (vm *VM) opLoadFastWide(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := int(uint16(inst))
	stack[sp] = stack[st.base+bx]
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opStoreFastWide(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := int(uint16(inst))
	sp--
	stack[st.base+bx] = stack[sp]
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opJumpSlow(st *loopState, target, ip, sp int) (int, int, loopCtl) {
	if atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, st.curCO, st.cache)
	}
	reloaded := false
	if atomic.LoadInt32(&st.curCO.StreamGen) != st.cache.streamGen {
		st.reloadWindow(vm, st.curCO)
		reloaded = true
	}
	if (st.periodicTick & 0xFF) == 0 {
		interpreterSafepoint()
		if vm.limits.Context != nil {
			if err := vm.limits.Context.Err(); err != nil {
				vm.unhandledException = val.NewRuntimeError("execution timed out or cancelled")
				panic(ErrTimeoutExceeded)
			}
		}
		if vm.limits.MaxSteps > 0 {
			vm.stepCount += 256
			if vm.stepCount >= vm.limits.MaxSteps {
				vm.unhandledException = val.NewRuntimeError("instruction step quota exceeded")
				panic(ErrStepLimitExceeded)
			}
		}
		if vm.limits.MaxStackDepth > 0 && st.frameIdx >= vm.limits.MaxStackDepth {
			vm.unhandledException = val.NewRuntimeError("maximum call stack depth exceeded")
			panic(ErrMaxStackDepthExceeded)
		}
	}
	if vm.signalsPending() {
		st.ip = ip
		vm.execCtx.SP = sp
		vm.deliverSignalsSafe()
		st.syncStackFromVM(vm, st.stack)
		reloaded = true
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
	}
	if reloaded {
		return target, sp, ctlReload
	}
	return target, sp, ctlNext
}

//go:noinline
func (vm *VM) opBinarySubscrSlow(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	key := stack[sp-1]
	obj := stack[sp-2]
	if obj.Kind() == val.KindMemoryView && key.IsInt64() && obj.AsMemoryView().ItemSize <= 1 && obj.AsMemoryView().NDim <= 1 {
		mv := obj.AsMemoryView()
		buf := mv.Buf
		k := key.AsInt64()
		if k < 0 {
			k += int64(len(buf))
		}
		if k >= 0 && k < int64(len(buf)) {
			stack[sp-2] = val.Int64Val(int64(buf[k]))
			sp--
			ip++
			return ip, sp, ctlNext
		}
	}
	switch {
	case obj.IsList() && key.IsInt64() && obj.Type() == val.ListType:
		lst := obj.AsList()
		valItem, ok := lst.Get(int(key.AsInt64()))
		if !ok {
			st.raiseExc = val.NewIndexError("list index out of range")
			return ip, sp, ctlRaise
		}
		stack[sp-2] = valItem
	case obj.IsDict() && obj.Type() == val.DictType:
		var v val.Value
		var ok bool
		if key.IsInt64() || key.IsAnyString() {
			v, ok = obj.AsDict().Get(key)
		} else {
			v, ok = vm.dictGetSafe(obj.AsDict(), key)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.syncStackFromVM(vm, stack)
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
		if !ok {
			st.raiseExc = val.NewKeyError(key)
			return ip, sp, ctlRaise
		}
		stack[sp-2] = v
	default:
		st.ip = ip
		vm.execCtx.SP = sp
		res := vm.getItemSafe(obj, key)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[sp-2] = res
		sp--
		ip++
		return ip, sp, ctlReload
	}
	sp--
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opGetIterSlow(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	top := stack[sp-1]
	st.ip = ip
	vm.execCtx.SP = sp
	itVal := vm.getIterSafe(top)
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = itVal
	ip++
	return ip, sp, ctlReload
}

// nextZipEnumeratePooled advances a concrete zip/enumerate iterator by one
// step, allocating the 2-4 element result tuple from the VM freelist when the
// step tuple is short-lived. Each step acquires a fresh tuple; the consumer
// returns it via releaseEphemeralTupleVal (unpack paths) or strips the marker
// via ClearEphemeral when retained, so no iterator-held reference ever shares
// ownership with tupleFreeList.
func (vm *VM) nextZipEnumeratePooled(pyIter *val.PyIter) (val.Value, bool) {
	ctx := &vm.execCtx
	if pyIter.Kind == val.IterKindEnumerate {
		v, ok := pyIter.Obj.AsIter().NextCtx(ctx)
		if !ok {
			return val.NoneVal(), false
		}
		idx := pyIter.Cur
		pyIter.Cur++
		var buf [2]val.Value
		buf[0] = val.Int64Val(idx)
		buf[1] = v
		if !val.MultiThreadedFlag.Load() {
			return vm.acquireEphemeralTuple(buf[:]), true
		}
		return val.NewTupleFromSlice(buf[:]), true
	}
	if pyIter.Stop != 0 {
		return val.NoneVal(), false
	}
	n := len(pyIter.Seq)
	if n == 0 {
		pyIter.Stop = 1
		return val.NoneVal(), false
	}
	if n <= 4 {
		var buf [4]val.Value
		for i := range n {
			v, ok := pyIter.Seq[i].AsIter().NextCtx(ctx)
			if !ok {
				pyIter.Stop = 1
				if pyIter.Step != 0 {
					raiseZipStrict(ctx, pyIter.Seq, i)
				}
				return val.NoneVal(), false
			}
			buf[i] = v
		}
		if !val.MultiThreadedFlag.Load() {
			return vm.acquireEphemeralTuple(buf[:n]), true
		}
		return val.NewTupleFromSlice(buf[:n]), true
	}
	items := make([]val.Value, n)
	for i := range n {
		v, ok := pyIter.Seq[i].AsIter().NextCtx(ctx)
		if !ok {
			pyIter.Stop = 1
			if pyIter.Step != 0 {
				raiseZipStrict(ctx, pyIter.Seq, i)
			}
			return val.NoneVal(), false
		}
		items[i] = v
	}
	return val.NewTupleFromSlice(items), true
}

// raiseZipStrict records the strict=True length-mismatch error for a zip step
// that exhausted argument i, mirroring the exact CPython-compatible messages.
func raiseZipStrict(ctx *val.ExecContext, seq []val.Value, i int) {
	n := len(seq)
	if i > 0 {
		plural := " "
		if i > 1 {
			plural = "s 1-"
		}
		val.RaiseValueError(ctx, "zip() argument "+strconv.Itoa(i+1)+" is shorter than argument"+plural+strconv.Itoa(i))
		return
	}
	for j := 1; j < n; j++ {
		if _, more := seq[j].AsIter().NextCtx(ctx); more {
			plural := " "
			if j > 1 {
				plural = "s 1-"
			}
			val.RaiseValueError(ctx, "zip() argument "+strconv.Itoa(j+1)+" is longer than argument"+plural+strconv.Itoa(j))
			return
		}
	}
}

//go:noinline
func (vm *VM) opForIterSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	jumpTarget := int(uint16(inst))
	it := stack[sp-1]
	if it.IsIter() {
		pyIter := it.AsIter()
		if pyIter.Kind == val.IterKindInvalid {
			st.raiseExc = val.NewTypeError(fmt.Sprintf("'%s' object is not iterable", pyIter.Obj.Type().Name))
			return ip, sp, ctlRaise
		}
		if pyIter.Kind == val.IterKindZip || pyIter.Kind == val.IterKindEnumerate {
			st.ip = ip
			vm.execCtx.SP = sp
			nextVal, ok := vm.nextZipEnumeratePooled(pyIter)
			stack = st.syncStackFromVM(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			if ok {
				stack[sp] = nextVal
				sp++
				ip++
				return ip, sp, ctlReload
			}
			vm.releaseForIter(pyIter)
			sp--
			return jumpTarget, sp, ctlReload
		}
		st.ip = ip
		vm.execCtx.SP = sp
		nextVal, ok := pyIter.Next()
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		if ok {
			stack[sp] = nextVal
			sp++
			ip++
			return ip, sp, ctlReload
		}
		vm.releaseForIter(pyIter)
		sp--
		return jumpTarget, sp, ctlReload
	}
	if it.IsGenerator() {
		gen := it.AsGenerator()
		if vm.activeJoinGen == nil {
			st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_FOR_ITER_GEN))
			return vm.pushInlineGenerator(st, gen, ip, sp, sp, jumpTarget, genModeForIter, val.Value{}, val.NoneVal())
		}
		st.ip = ip
		vm.execCtx.SP = sp
		nextVal, status := vm.resumeGenerator(&vm.execCtx, gen, val.NoneVal())
		stack = st.syncStackFromVM(vm, stack)
		if status == val.IterYielded {
			stack[sp] = nextVal
			sp++
			ip++
			return ip, sp, ctlReload
		}
		if status == val.IterExhausted {
			vm.releaseGenerator(gen)
			sp--
			return jumpTarget, sp, ctlReload
		}
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		sp--
		return jumpTarget, sp, ctlReload
	}
	if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
		st.ip = ip
		vm.execCtx.SP = sp
		nextVal, status := vm.iterNextSafe(tpIterNext, it)
		stack = st.syncStackFromVM(vm, stack)
		if status == val.IterYielded {
			stack[sp] = nextVal
			sp++
			ip++
			return ip, sp, ctlReload
		}
		if status == val.IterExhausted {
			sp--
			return jumpTarget, sp, ctlReload
		}
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		panic(fmt.Sprintf("vm: RuntimeError: iterator '%s' returned status %d without active exception in %s", it.Type().Name, status, st.curCO.Name))
	}
	st.raiseExc = val.NewTypeError(fmt.Sprintf("'%s' object is not an iterator", it.Type().Name))
	return ip, sp, ctlRaise
}

//go:noinline
func (vm *VM) opForIterRangeWriteLoop(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	targetReg := uint8(inst >> 16)
	jumpTarget := int(uint16(inst))
	it := stack[sp-1]
	cur := it.RangeStart()
	stop := it.RangeStop()
	var isWriteLoop bool
	var localIdx uint32
	var constIdx uint32
	op1 := DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
	if op1 == OP_FAST_STREAM_WRITE_LOCAL_CONST {
		writeInst := *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))
		localIdx = uint32(uint8(writeInst >> 16))
		constIdx = uint32(uint16(writeInst))
		isWriteLoop = true
	} else if (op1 == OP_LOAD_FAST || op1 == OP_LOAD_FAST_CONST) &&
		(op1 == OP_LOAD_FAST_CONST || DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))) == OP_LOAD_CONST) &&
		(DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+3)*4))) == OP_CALL_METHOD_PIC || DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+3)*4))) == OP_CALL_METHOD) &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))) == OP_POP_TOP {
		w3 := *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+3)*4))
		methIdx := DecodeArgBx(w3)
		argc := DecodeArgA(w3)
		if argc == 1 && int(methIdx) < len(st.consts) && st.consts[methIdx] == nameWrite {
			w1 := *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))
			if op1 == OP_LOAD_FAST_CONST {
				localIdx = uint32(DecodeArgA(w1))
				constIdx = uint32(DecodeArgBx(w1))
			} else {
				localIdx = uint32(DecodeArgBx(w1))
				constIdx = uint32(DecodeArgBx(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4))))
			}
			isWriteLoop = true
		}
	}
	if isWriteLoop && int(localIdx) < 256 && int(constIdx) < len(st.consts) {
		receiver := locals[localIdx]
		arg := st.consts[constIdx]
		if receiver.IsInstance() && arg.Kind() == val.KindString {
			data := arg.AsStringObj().Data
			rem := int(stop - cur)
			switch receiver.Type() {
			case StringIOType:
				sio := (*pyStringIO)(val.AsPointer(receiver))
				if sio.pos == len(sio.buf) {
					needed := rem * len(data)
					oldLen := len(sio.buf)
					if cap(sio.buf)-oldLen < needed {
						newCap := cap(sio.buf) + needed + 1024
						newBuf := make([]byte, oldLen+needed, newCap)
						copy(newBuf, sio.buf)
						sio.buf = newBuf
					} else {
						sio.buf = sio.buf[:oldLen+needed]
					}
					dst := sio.buf[oldLen:]
					copy(dst, data)
					for done := len(data); done < needed; {
						copied := copy(dst[done:], dst[:done])
						done += copied
					}
					sio.pos = len(sio.buf)
					locals[targetReg] = val.Int64Val(int64(stop - 1))
					sp--
					return jumpTarget, sp, ctlNext
				}
			case FileType:
				pf := (*pyFile)(val.AsPointer(receiver))
				if !pf.isClosed && !pf.binary {
					needed := rem * len(data)
					oldLen := len(pf.writeBuf)
					if cap(pf.writeBuf)-oldLen >= needed {
						pf.writeBuf = pf.writeBuf[:oldLen+needed]
						dst := pf.writeBuf[oldLen:]
						copy(dst, data)
						for done := len(data); done < needed; {
							copied := copy(dst[done:], dst[:done])
							done += copied
						}
					} else {
						for range rem {
							pf.writeBuf = append(pf.writeBuf, data...)
							if len(pf.writeBuf) >= 32768 && pf.f != nil {
								_, _ = pf.f.Write(pf.writeBuf)
								pf.writeBuf = pf.writeBuf[:0]
							}
						}
					}
					locals[targetReg] = val.Int64Val(int64(stop - 1))
					sp--
					return jumpTarget, sp, ctlNext
				}
			}
		}
	}
	stack[sp-1] = val.RangeVal(cur+1, stop)
	locals[targetReg] = val.Int64Val(int64(cur))
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBinarySlow(st *loopState, op Opcode, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	b := stack[sp-1]
	a := stack[sp-2]
	if (op == OP_BINARY_ADD || op == OP_BINARY_ADD_INT || op == OP_BINARY_ADD_FLOAT) && a.IsAnyString() && b.IsAnyString() {
		if a.IsString() {
			if res, ok := val.TryConcatInPlace(a, b); ok {
				stack[sp-2] = res
				sp--
				ip++
				return ip, sp, ctlNext
			}
		}
		stack[sp-2] = val.ConcatStrings(a, b)
		sp--
		ip++
		return ip, sp, ctlNext
	}
	vm.execCtx.SP = sp
	vm.activeFrameIdx = st.frameIdx
	var res val.Value
	switch op {
	case OP_BINARY_ADD, OP_BINARY_ADD_INT, OP_BINARY_ADD_FLOAT:
		res = val.AddCtx(&vm.execCtx, a, b)
	case OP_BINARY_SUB, OP_BINARY_SUB_INT:
		res = val.SubCtx(&vm.execCtx, a, b)
	case OP_BINARY_MUL:
		res = val.MulCtx(&vm.execCtx, a, b)
	case OP_BINARY_TRUE_DIV:
		res = val.TrueDivCtx(&vm.execCtx, a, b)
	case OP_BINARY_FLOOR_DIV:
		res = val.FloorDivCtx(&vm.execCtx, a, b)
	case OP_BINARY_MOD:
		res = val.ModCtx(&vm.execCtx, a, b)
	case OP_BINARY_POW:
		res = val.PowCtx(&vm.execCtx, a, b)
	case OP_BINARY_AND:
		res = val.BitAnd(&vm.execCtx, a, b)
	case OP_BINARY_OR:
		res = val.BitOr(&vm.execCtx, a, b)
	case OP_BINARY_XOR:
		res = val.BitXor(&vm.execCtx, a, b)
	case OP_BINARY_LSHIFT:
		res = val.LShift(&vm.execCtx, a, b)
	case OP_BINARY_RSHIFT:
		res = val.RShift(&vm.execCtx, a, b)
	case OP_BINARY_MATMUL:
		res = val.MatMul(&vm.execCtx, a, b)
	}
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.syncStackFromVM(vm, stack)
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-2] = res
	sp--
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opUnarySlow(st *loopState, op Opcode, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	v := stack[sp-1]
	vm.execCtx.SP = sp
	vm.activeFrameIdx = st.frameIdx
	var res val.Value
	switch op {
	case OP_UNARY_NEGATIVE:
		res = val.Negative(&vm.execCtx, v)
	case OP_UNARY_POSITIVE:
		res = val.Positive(&vm.execCtx, v)
	case OP_UNARY_INVERT:
		res = val.Invert(&vm.execCtx, v)
	}
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.syncStackFromVM(vm, stack)
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = res
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opCompareLtSlow(st *loopState, ip, sp int) (int, int, loopCtl) {
	return vm.opCompareSlow(st, Encode(OP_COMPARE_OP, CMP_LT, 0), ip, sp)
}

//go:noinline
func (vm *VM) finishCompareTupleSlow(st *loopState, ip, sp int, res val.Value) (int, int, loopCtl) {
	stack := st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-2] = res
	return ip + 1, sp - 1, ctlReload
}

func compareDunderForType(tp *val.PyType, cmpOp uint8) val.Value {
	switch cmpOp {
	case CMP_EQ:
		return tp.DunderEqVal()
	case CMP_NEQ:
		return tp.DunderNeVal()
	case CMP_LT:
		return tp.DunderLtVal()
	case CMP_LTE:
		return tp.DunderLeVal()
	case CMP_GT:
		return tp.DunderGtVal()
	case CMP_GTE:
		return tp.DunderGeVal()
	}
	return val.Value{}
}

func reflectedCmpOp(cmpOp uint8) uint8 {
	switch cmpOp {
	case CMP_LT:
		return CMP_GT
	case CMP_LTE:
		return CMP_GTE
	case CMP_GT:
		return CMP_LT
	case CMP_GTE:
		return CMP_LTE
	default:
		return cmpOp
	}
}

func isSimpleCompareDunder(dunder val.Value) (*val.PyFunction, *CodeObject, bool) {
	if !dunder.IsFunction() {
		return nil, nil, false
	}
	dfn := dunder.AsFunction()
	dcallee := AsCodeObject(dfn.Code)
	if !dcallee.IsGenerator && !dcallee.IsCoroutine &&
		dcallee.SimplePositionalOnly && dcallee.PositionalParamCount == 2 && dcallee.NumParams == 2 &&
		len(dfn.Defaults) == 0 && dfn.KwDefaults == nil &&
		dcallee.KWOnlyArgCount == 0 && (dcallee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 {
		return dfn, dcallee, true
	}
	return nil, nil, false
}

func (vm *VM) quickenCompareDunder(st *loopState, inst uint32, ip int, dunder val.Value, dfn *val.PyFunction, dcallee *CodeObject, taVer, tbVer uint64, mode int32) {
	if st.cache == nil || uint(ip) >= uint(len(st.cache.ic)) || vm.hasTrackedViews {
		return
	}
	if len(dcallee.CellVars) != 0 || (dcallee.HasCells && dfn.Closure == nil) {
		return
	}
	cc := vm.cacheFor(dcallee)
	if len(dcallee.ExceptionTable) == 0 && atomic.LoadInt32(&dcallee.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, dcallee, cc)
	}
	_, _ = vm.loadWindow(dcallee, cc)
	st.cache.ic[ip] = ICSlot{
		CachedVal:       dunder,
		CalleeCache:     cc,
		ExpectedTypeVer: taVer,
		ExpectedAuxVer:  tbVer,
		SlotIndex:       mode,
		Kind:            ICKindCompareDunder,
	}
	if DecodeOpcode(inst) != OP_COMPARE_DUNDER {
		st.codePtr = st.cache.quickenAt(ip, ReplaceOpcode(inst, OP_COMPARE_DUNDER))
	}
}

//go:noinline
func (vm *VM) opCompareSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	cmpOp := uint8(inst >> 16)
	b := stack[sp-1]
	a := stack[sp-2]
	if cmpOp <= CMP_GTE && (a.IsInstance() || b.IsInstance()) &&
		a.Kind() != val.KindMappingProxy && b.Kind() != val.KindMappingProxy &&
		a.Kind() != val.KindComplex && b.Kind() != val.KindComplex {
		ta := a.Type()
		tb := b.Type()
		if ta != nil && tb != nil {
			var dunder val.Value
			var mode int32
			isStrictSub := ta != tb && val.IsSubclass(tb, ta)
			if isStrictSub {
				if rFn := compareDunderForType(tb, reflectedCmpOp(cmpOp)); rFn.HeaderPtr() != nil {
					dunder = rFn
					mode = 1
				}
			}
			if dunder.HeaderPtr() == nil {
				if fn := compareDunderForType(ta, cmpOp); fn.HeaderPtr() != nil {
					dunder = fn
					mode = 0
				} else if !isStrictSub {
					if rFn := compareDunderForType(tb, reflectedCmpOp(cmpOp)); rFn.HeaderPtr() != nil {
						dunder = rFn
						mode = 1
					}
				}
			}
			if dunder.HeaderPtr() != nil {
				if dfn, dcallee, ok := isSimpleCompareDunder(dunder); ok {
					vm.quickenCompareDunder(st, inst, ip, dunder, dfn, dcallee, ta.Version(), tb.Version(), mode)
					if mode != 0 {
						stack[sp-2] = b
						stack[sp-1] = a
					}
					newIP, newSP := vm.inlineDunderCompare(dunder, sp-2, sp-2, a, b, cmpOp, st, ip)
					return newIP, newSP, ctlReload
				}
			}
		}
	}
	if (cmpOp == CMP_IN || cmpOp == CMP_NOT_IN) && b.IsInstance() {
		if bt := b.Type(); bt != nil {
			if dunder := bt.DunderContainsVal(); dunder.HeaderPtr() != nil {
				if dfn, dcallee, ok := isSimpleCompareDunder(dunder); ok {
					vm.quickenCompareDunder(st, inst, ip, dunder, dfn, dcallee, 0, bt.Version(), 2)
					stack[sp-2] = b
					stack[sp-1] = a
					newIP, newSP := vm.inlineDunderCompare(dunder, sp-2, sp-2, a, b, cmpOp, st, ip)
					return newIP, newSP, ctlReload
				}
			}
		}
	}
	st.ip = ip
	vm.execCtx.SP = sp
	res := vm.compareOpSafe(a, b, cmpOp)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-2] = res
	sp--
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opFastSubscrPushSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	cellIdx := uint8(inst >> 16)
	localIdx := uint16(inst)
	var cVal val.Value
	if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
		cVal = st.cellPtrs[cellIdx].Value
	} else if int(cellIdx) < len(st.cells) {
		cVal = (*val.PyCell)(unsafe.Pointer(st.cells[cellIdx].HeaderPtr())).Value
	}
	st.ip = ip
	vm.execCtx.SP = sp
	res := vm.getItemSafe(cVal, locals[uint8(localIdx)])
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = res
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opFastJumpSubscrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	cellIdx := uint8(inst >> 16)
	exitTarget := int(uint16(inst))
	localIdx := uint16(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
	var cVal val.Value
	if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
		cVal = st.cellPtrs[cellIdx].Value
	} else if int(cellIdx) < len(st.cells) {
		cVal = (*val.PyCell)(unsafe.Pointer(st.cells[cellIdx].HeaderPtr())).Value
	}
	subVal := vm.getItemSafe(cVal, locals[uint8(localIdx)])
	if subVal.AsBool() {
		return exitTarget, sp, ctlNext
	}
	return ip + 4, sp, ctlNext
}

//go:noinline
func (vm *VM) opFastStoreSubscrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	cellIdx := uint8(inst >> 16)
	localIdx := uint16(inst)
	constIdx := uint16(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
	var cVal val.Value
	if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
		cVal = st.cellPtrs[cellIdx].Value
	} else if int(cellIdx) < len(st.cells) {
		cVal = (*val.PyCell)(unsafe.Pointer(st.cells[cellIdx].HeaderPtr())).Value
	}
	vm.setItemSafe(cVal, locals[uint8(localIdx)], st.consts[constIdx])
	return ip + 4, sp, ctlNext
}

//go:noinline
func (vm *VM) opFastCmpEqDerefSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	localIdx := uint8(inst >> 16)
	exitTarget := int(uint16(inst))
	cellIdx := uint16(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
	var cVal val.Value
	if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
		cVal = st.cellPtrs[cellIdx].Value
	} else if int(cellIdx) < len(st.cells) {
		cVal = st.cells[cellIdx]
	}
	if !val.Equals(locals[localIdx], cVal) {
		return exitTarget, sp, ctlNext
	}
	return ip + 4, sp, ctlNext
}

//go:noinline
func (vm *VM) opFastSubAddDerefSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	dst := uint8(inst >> 16)
	srcA := uint8(inst >> 8)
	srcB := uint8(inst)
	cellIdx := uint16(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
	var cVal val.Value
	if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
		cVal = st.cellPtrs[cellIdx].Value
	} else if int(cellIdx) < len(st.cells) {
		cVal = st.cells[cellIdx]
	}
	diff := val.SubCtx(&vm.execCtx, locals[srcA], locals[srcB])
	locals[dst] = val.Add(diff, cVal)
	return ip + 6, sp, ctlNext
}

//go:noinline
func (vm *VM) opFastSelfRecurseSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	localReg := uint8(inst >> 16)
	cellIdx := uint16(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
	constIdx := uint16(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+2)*4)))
	if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
		fnVal := st.cellPtrs[cellIdx].Value
		if fnVal.IsFunction() {
			fn := fnVal.AsFunction()
			callee := AsCodeObject(fn.Code)
			if callee == st.curCO {
				argVal := locals[localReg]
				cVal := st.consts[constIdx]
				var nextArg val.Value
				if argVal.IsInt64() && cVal.IsInt64() {
					nextArg = val.Int64Val(argVal.AsInt64() + cVal.AsInt64())
				} else {
					nextArg = val.Add(argVal, cVal)
				}
				newBase := sp
				if st.frameIdx+2 > len(vm.frames) {
					vm.ensureFrameCapacity(st.frameIdx + 2)
				}
				saved := &vm.frames[st.frameIdx]
				saved.co = st.curCO
				saved.codePtr = st.codePtr
				saved.codeLen = st.codeLen
				saved.consts = st.consts
				saved.ip = ip + 6
				saved.baseOffset = st.base
				saved.retSP = sp
				saved.cells = st.cells
				saved.cellPtrs = st.cellPtrs
				saved.globals = st.globals
				saved.locals = st.curLocals
				saved.cache = st.cache
				saved.excStackLen = len(vm.excStack)
				saved.stackIsVM = st.stackIsVM
				saved.stack = st.stack
				saved.activeGen = st.activeGen
				saved.genFrameIdx = st.genFrameIdx
				saved.genMode = genModeNone
				saved.genDefault = val.Value{}
				saved.initInstance = val.Value{}
				saved.cmpActive = false
				st.curLocals = val.NoneVal()
				st.frameIdx++
				st.base = newBase
				minCap := st.base + 256
				if minCap > len(stack) {
					stack = vm.growStack(st, stack, minCap)
				}
				stack[st.base] = nextArg
				sp = st.base + st.curCO.NumLocals
				vm.activeBaseOffset = st.base
				vm.activeFrameIdx = st.frameIdx
				return 0, sp, ctlReload
			}
		}
	}
	return ip + 6, sp, ctlNext
}

//go:noinline
func (vm *VM) opFastBitSlow(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	switch op {
	case OP_JUMP_IF_BITAND_ZERO:
		jumpTarget := int(uint16(inst))
		b := stack[sp-1]
		a := stack[sp-2]
		sp -= 2
		res := val.BitAnd(&vm.execCtx, a, b)
		if !res.AsBool() {
			return jumpTarget, sp, ctlNext
		}
		return ip + 1, sp, ctlNext
	case OP_FAST_AND_INT_LOCAL_TO_LOCAL, OP_FAST_OR_INT_LOCAL_TO_LOCAL, OP_FAST_XOR_INT_LOCAL_TO_LOCAL, OP_FAST_LSHIFT_INT_LOCAL_TO_LOCAL, OP_FAST_RSHIFT_INT_LOCAL_TO_LOCAL:
		dst := uint8(inst >> 16)
		vA := locals[uint8(inst>>8)]
		vB := locals[uint8(inst)]
		switch op {
		case OP_FAST_AND_INT_LOCAL_TO_LOCAL:
			locals[dst] = val.BitAnd(&vm.execCtx, vA, vB)
		case OP_FAST_OR_INT_LOCAL_TO_LOCAL:
			locals[dst] = val.BitOr(&vm.execCtx, vA, vB)
		case OP_FAST_XOR_INT_LOCAL_TO_LOCAL:
			locals[dst] = val.BitXor(&vm.execCtx, vA, vB)
		case OP_FAST_LSHIFT_INT_LOCAL_TO_LOCAL:
			locals[dst] = val.LShift(&vm.execCtx, vA, vB)
		case OP_FAST_RSHIFT_INT_LOCAL_TO_LOCAL:
			locals[dst] = val.RShift(&vm.execCtx, vA, vB)
		}
		return ip + 1, sp, ctlNext
	default:
		vA := locals[uint8(inst>>16)]
		vB := locals[uint8(uint16(inst))]
		switch op {
		case OP_FAST_AND_INT_LOCALS:
			stack[sp] = val.BitAnd(&vm.execCtx, vA, vB)
		case OP_FAST_OR_INT_LOCALS:
			stack[sp] = val.BitOr(&vm.execCtx, vA, vB)
		case OP_FAST_XOR_INT_LOCALS:
			stack[sp] = val.BitXor(&vm.execCtx, vA, vB)
		case OP_FAST_LSHIFT_INT_LOCALS:
			stack[sp] = val.LShift(&vm.execCtx, vA, vB)
		case OP_FAST_RSHIFT_INT_LOCALS:
			stack[sp] = val.RShift(&vm.execCtx, vA, vB)
		}
		return ip + 1, sp + 1, ctlNext
	}
}

// recordAttrPICMRU inserts or promotes an attribute PIC entry into slot 0 for fast-path hits.
func recordAttrPICMRU(pic *[4]AttrPICEntry, shape *val.Shape, slotIdx int, tv uint32) {
	for k := range 4 {
		if pic[k].Shape == nil || pic[k].Shape == shape {
			pic[k] = AttrPICEntry{Shape: shape, SlotIndex: int32(slotIdx), TypeVer: tv}
			if k > 0 {
				pic[0], pic[k] = pic[k], pic[0]
			}
			return
		}
	}
	pic[3] = pic[2]
	pic[2] = pic[1]
	pic[1] = pic[0]
	pic[0] = AttrPICEntry{Shape: shape, SlotIndex: int32(slotIdx), TypeVer: tv}
}

//go:noinline
func (vm *VM) opFastAttrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	op := Opcode(inst >> 24)
	reg := uint8(inst >> 16)
	obj := locals[reg]
	switch op {
	case OP_LOAD_FAST_ATTR_MUL_SELF:
		if instObj := obj.AsInstanceChecked(); instObj != nil {
			instShape := instObj.Shape
			instType := instObj.Header.Type
			if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
				if slotIdx, ok := instShape.Lookup(st.consts[uint16(inst)]); ok {
					if _, _, foundInClass := instType.LookupMRO(st.consts[uint16(inst)]); !foundInClass {
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							v := instObj.Slots[slotIdx]
							if v.IsInt64() {
								vi := v.AsInt64()
								if int64(int32(vi)) == vi {
									pic := &st.cache.ensureAttrPIC()[ip+1]
									if pic[0].Shape == nil || pic[0].Shape == instShape {
										pic[0] = AttrPICEntry{Shape: instShape, SlotIndex: int32(slotIdx), TypeVer: uint32(instType.Version())}
									}
									stack[sp] = val.Int64Val(vi * vi)
									return ip + 5, sp + 1, ctlNext
								}
							}
						}
					}
				}
			}
		}
	case OP_LOAD_FAST_ATTR:
		if instObj := obj.AsInstanceChecked(); instObj != nil {
			instShape := instObj.Shape
			instType := instObj.Header.Type
			if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
				tv := uint32(instType.Version())
				if uint(ip+1) < uint(len(st.cache.attrPIC)) {
					pic := &st.cache.attrPIC[ip+1]
					for k := 1; k < 4; k++ {
						if pic[k].Shape == instShape && pic[k].TypeVer == tv {
							slotIdx := pic[k].SlotIndex
							if uint(slotIdx) < uint(len(instObj.Slots)) {
								v := instObj.Slots[slotIdx]
								if !v.IsUnbound() {
									pic[0], pic[k] = pic[k], pic[0]
									stack[sp] = v
									return ip + 2, sp + 1, ctlNext
								}
							}
						}
					}
				}
				if slotIdx, ok := instShape.Lookup(st.consts[uint16(inst)]); ok {
					descr, _, foundInClass := instType.LookupMRO(st.consts[uint16(inst)])
					if !foundInClass || descr.Kind() == val.KindMemberDescr {
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							v := instObj.Slots[slotIdx]
							if !v.IsUnbound() {
								pic := &st.cache.ensureAttrPIC()[ip+1]
								recordAttrPICMRU(pic, instShape, slotIdx, tv)
								stack[sp] = v
								return ip + 2, sp + 1, ctlNext
							}
						}
					}
				}
			}
		}
		stack[sp] = obj
		newIP, newSP, ctl := vm.opLoadAttrSlow(st, ReplaceOpcode(inst, OP_LOAD_ATTR), ip+1, sp+1)
		// Restore OP_LOAD_FAST + OP_LOAD_ATTR per-VM for tuples/modules/types
		// so subsequent hits use runLoopWithGlobals's inline tuple/module fast paths.
		// @property sites stay fused and dispatch directly from OP_LOAD_FAST_ATTR.
		if st.cache != nil && uint(ip+1) < uint(len(st.cache.ic)) {
			k := st.cache.ic[ip+1].Kind
			if k == ICKindTupleGetter || k == ICKindModuleAttr || k == ICKindTypeAttr {
				st.cache.quickenAt(ip, Encode(OP_LOAD_FAST, 0, uint16(reg)))
				ptr := st.cache.quickenAt(ip+1, ReplaceOpcode(inst, OP_LOAD_ATTR))
				if st.curCO == st.cache.co {
					st.codePtr = ptr
				}
			}
		}
		return newIP, newSP, ctl
	case OP_STORE_FAST_ATTR:
		v := stack[sp-1]
		if !v.IsString() && v.Kind() != val.KindMemoryView && !val.MultiThreadedFlag.Load() {
			if instObj := obj.AsInstanceChecked(); instObj != nil {
				instShape := instObj.Shape
				instType := instObj.Header.Type
				if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomSetAttr) == 0 {
					tv := uint32(instType.Version())
					if uint(ip+1) < uint(len(st.cache.attrPIC)) {
						pic := &st.cache.attrPIC[ip+1]
						for k := 1; k < 4; k++ {
							if pic[k].Shape == instShape && pic[k].TypeVer == tv {
								slotIdx := pic[k].SlotIndex
								if uint(slotIdx) < uint(len(instObj.Slots)) {
									instObj.Slots[slotIdx] = v
									pic[0], pic[k] = pic[k], pic[0]
									return ip + 2, sp - 1, ctlNext
								}
							}
						}
					}
					if slotIdx, ok := instShape.Lookup(st.consts[uint16(inst)]); ok {
						descr, _, foundInClass := instType.LookupMRO(st.consts[uint16(inst)])
						if !foundInClass || descr.Kind() == val.KindMemberDescr {
							if uint(slotIdx) < uint(len(instObj.Slots)) {
								instObj.Slots[slotIdx] = v
								pic := &st.cache.ensureAttrPIC()[ip+1]
								recordAttrPICMRU(pic, instShape, slotIdx, tv)
								return ip + 2, sp - 1, ctlNext
							}
						}
					}
				}
			}
		}
		stack[sp] = obj
		newIP, newSP, ctl := vm.opStoreAttrSlow(st, ReplaceOpcode(inst, OP_STORE_ATTR), ip+1, sp+1)
		if st.cache != nil && uint(ip+1) < uint(len(st.cache.ic)) && st.cache.ic[ip+1].Kind == ICKindPropertySet {
			st.cache.quickenAt(ip, Encode(OP_LOAD_FAST, 0, uint16(reg)))
			ptr := st.cache.quickenAt(ip+1, ReplaceOpcode(inst, OP_STORE_ATTR))
			if st.curCO == st.cache.co {
				st.codePtr = ptr
			}
		}
		return newIP, newSP, ctl
	}
	vm.deoptInst(st, ip, inst)
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opLoadGlobalFastSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	gIdx := uint16(uint8(inst >> 8))
	reg := uint8(inst)
	newIP, newSP, ctl := vm.opLoadGlobalSlow(st, Encode(OP_LOAD_GLOBAL, 0, gIdx), ip, sp)
	if ctl == ctlRaise || ctl == ctlExit {
		return newIP, newSP, ctl
	}
	st.stack[newSP] = st.stack[st.base+int(reg)]
	return newIP + 1, newSP + 1, ctl
}

//go:noinline
func (vm *VM) opCompareJumpSlow(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	cmpOp := uint8(inst >> 16)
	target := uint16(inst)
	jumpOp := OP_JUMP_IF_FALSE
	wantTrue := op == OP_COMPARE_JUMP_IF_TRUE
	if wantTrue {
		jumpOp = OP_JUMP_IF_TRUE
	}
	cmpInst := Encode(OP_COMPARE_OP, cmpOp, 0)
	b := st.stack[sp-1]
	a := st.stack[sp-2]
	if cmpOp <= CMP_GTE &&
		(a.IsInlineStr() || (a.IsString() && a.AsStringObj().Header.Type == val.StrType)) &&
		(b.IsInlineStr() || (b.IsString() && b.AsStringObj().Header.Type == val.StrType)) {
		var cmpRes bool
		switch cmpOp {
		case CMP_EQ:
			cmpRes = val.StringEquals(a, b)
		case CMP_NEQ:
			cmpRes = !val.StringEquals(a, b)
		case CMP_LT:
			cmpRes = val.StringCompare(a, b) < 0
		case CMP_LTE:
			cmpRes = val.StringCompare(a, b) <= 0
		case CMP_GT:
			cmpRes = val.StringCompare(a, b) > 0
		case CMP_GTE:
			cmpRes = val.StringCompare(a, b) >= 0
		}
		sp -= 2
		if cmpRes == wantTrue {
			return int(target), sp, ctlNext
		}
		return ip + 2, sp, ctlNext
	}
	if cmpOp <= CMP_GTE && a.IsTuple() && b.IsTuple() &&
		a.HeaderPtr().Type == val.TupleType && b.HeaderPtr().Type == val.TupleType {
		st.ip = ip
		vm.execCtx.SP = sp
		res := val.CompareTuplesFast(&vm.execCtx, a, b, cmpOp)
		if !vm.execCtx.HasException() &&
			len(vm.execCtx.Stack) > 0 && len(st.stack) > 0 &&
			&vm.execCtx.Stack[0] == &st.stack[0] {
			sp -= 2
			if res.IsTruthy() == wantTrue {
				return int(target), sp, ctlNext
			}
			return ip + 2, sp, ctlNext
		}
		newIP, newSP, ctl := vm.finishCompareTupleSlow(st, ip, sp, res)
		if ctl == ctlRaise || ctl == ctlExit {
			return newIP, newSP, ctl
		}
		cond := st.stack[newSP-1]
		newSP--
		if cond.IsTruthy() == wantTrue {
			return int(target), newSP, ctl
		}
		return ip + 2, newSP, ctl
	}
	callerCache := st.cache
	newIP, newSP, ctl := vm.opCompareSlow(st, cmpInst, ip, sp)
	if ctl == ctlRaise || ctl == ctlExit {
		return newIP, newSP, ctl
	}
	if callerCache != nil && callerCache.privateActive && ip+1 < len(callerCache.active) &&
		DecodeOpcode(callerCache.active[ip]) == OP_COMPARE_DUNDER {
		ptr := callerCache.quickenAt(ip+1, Encode(jumpOp, 0, target))
		if st.cache == callerCache {
			st.codePtr = ptr
		}
		if newIP == 0 {
			return newIP, newSP, ctl
		}
	} else if newIP == 0 {
		return newIP, newSP, ctl
	}
	cond := st.stack[newSP-1]
	newSP--
	if cond.IsTruthy() == wantTrue {
		return int(target), newSP, ctl
	}
	return ip + 2, newSP, ctl
}

//go:noinline
func (vm *VM) opCmpConstJumpSlow(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	cmpOp := uint8(inst >> 16)
	constIdx := uint16(uint8(inst >> 8))
	target := ip + int(uint8(inst))
	jumpOp := OP_JUMP_IF_FALSE
	wantTrue := op == OP_CMP_CONST_JUMP_IF_TRUE
	if wantTrue {
		jumpOp = OP_JUMP_IF_TRUE
	}
	a := st.stack[sp-1]
	b := st.consts[constIdx]
	if cmpOp <= CMP_GTE &&
		(a.IsInlineStr() || (a.IsString() && a.AsStringObj().Header.Type == val.StrType)) &&
		(b.IsInlineStr() || (b.IsString() && b.AsStringObj().Header.Type == val.StrType)) {
		var cmpRes bool
		switch cmpOp {
		case CMP_EQ:
			cmpRes = val.StringEquals(a, b)
		case CMP_NEQ:
			cmpRes = !val.StringEquals(a, b)
		case CMP_LT:
			cmpRes = val.StringCompare(a, b) < 0
		case CMP_LTE:
			cmpRes = val.StringCompare(a, b) <= 0
		case CMP_GT:
			cmpRes = val.StringCompare(a, b) > 0
		case CMP_GTE:
			cmpRes = val.StringCompare(a, b) >= 0
		}
		sp--
		if cmpRes == wantTrue {
			return target, sp, ctlNext
		}
		return ip + 3, sp, ctlNext
	}
	st.stack[sp] = b
	callerCache := st.cache
	cmpInst := Encode(OP_COMPARE_OP, cmpOp, 0)
	newIP, newSP, ctl := vm.opCompareSlow(st, cmpInst, ip+1, sp+1)
	if ctl == ctlRaise || ctl == ctlExit {
		return newIP, newSP, ctl
	}
	if callerCache != nil && callerCache.privateActive && ip+2 < len(callerCache.active) &&
		DecodeOpcode(callerCache.active[ip+1]) == OP_COMPARE_DUNDER {
		callerCache.quickenAt(ip, Encode(OP_LOAD_CONST, 0, constIdx))
		ptr := callerCache.quickenAt(ip+2, Encode(jumpOp, 0, uint16(target)))
		if st.cache == callerCache {
			st.codePtr = ptr
		}
		if newIP == 0 {
			return newIP, newSP, ctl
		}
	} else if newIP == 0 {
		return newIP, newSP, ctl
	}
	cond := st.stack[newSP-1]
	newSP--
	if cond.IsTruthy() == wantTrue {
		return target, newSP, ctl
	}
	return ip + 3, newSP, ctl
}

//go:noinline
func (vm *VM) opSubscrFastSlow(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	obj := stack[st.base+int(uint8(inst>>8))]
	var key val.Value
	if op == OP_SUBSCR_FAST_CONST {
		key = st.consts[uint8(inst)]
	} else {
		key = stack[st.base+int(uint8(inst))]
	}
	stack[sp] = obj
	stack[sp+1] = key
	newIP, newSP, ctl := vm.opBinarySubscrSlow(st, ip+2, sp+2)
	if ctl == ctlRaise || ctl == ctlExit {
		return newIP, newSP, ctl
	}
	return ip + 3, newSP, ctl
}

//go:noinline
func (vm *VM) opPopLoadFastSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	sp--
	if vm.hasTrackedViews {
		vm.opReclaimPopTop(stack[sp])
	}
	bx := int(uint16(inst))
	stack[sp] = stack[st.base+bx]
	return ip + 2, sp + 1, ctlNext
}

//go:noinline
func (vm *VM) opIncDerefSlow(st *loopState, cell *val.PyCell, ip, sp int) (int, int, loopCtl) {
	cell.Value = val.Add(cell.Value, val.Int64Val(1))
	return ip + 4, sp, ctlNext
}

//go:noinline
func (vm *VM) opDetachDupTop(v val.Value) {
	val.DetachStringBuf(v)
	markViewEscaped(v)
}

//go:noinline
func (vm *VM) opDetachStoreDeref(v val.Value) {
	val.DetachStringBuf(v)
}

//go:noinline
func (vm *VM) opReclaimPopTop(v val.Value) {
	vm.reclaimViewSlot(v, val.NoneVal())
}
