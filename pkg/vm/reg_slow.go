package vm

import (
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

// This file implements the register operations that delegate to the
// synchronous slow-path helpers shared with the stack loop. Every helper
// called here is provably free of interpreter frame pushes: helpers that
// can push frames (in-loop call/property/generator paths) are never
// invoked from register context.

// failCtx funnels a set ExecContext exception into an unwind.
func (rf *regFrame) failCtx(sip int) (val.Value, bool) {
	exc := rf.vm.execCtx.ActiveException
	rf.vm.execCtx.ClearException()
	rf.st.raiseExc = exc
	return rf.fault(sip)
}

// loadGlobal runs the version-guarded global fast path. It reports whether
// it hit.
func (rf *regFrame) loadGlobal(dst int, nameIdx uint16, sip int) bool {
	st := rf.st
	if st.globals.Header.Type != val.DictType && st.globals.Header.Type != nil {
		return false
	}
	globVer := atomic.LoadUint64(&st.globals.VersionTag)
	entry := &rf.cache.ic[sip]
	if entry.ExpectedTypeVer == globVer &&
		(entry.Kind == ICKindGlobal || (entry.Kind == ICKindBuiltin && entry.ExpectedAuxVer == atomic.LoadUint64(&rf.vm.builtins.VersionTag))) {
		rf.regs[dst] = entry.CachedVal
		return true
	}
	return false
}

func (rf *regFrame) slowGlobal(dst int, nameIdx uint16, sip int) (val.Value, bool) {
	st := rf.st
	st.ip = sip
	rf.vm.execCtx.SP = rf.frameTop
	nip, nsp, ctl := rf.vm.opLoadGlobalSlow(st, Encode(OP_LOAD_GLOBAL, 0, nameIdx), sip, rf.base+dst)
	_ = nip
	_ = nsp
	switch ctl {
	case ctlNext, ctlReload:
		rf.refresh()
		rf.pc++
		return val.Value{}, false
	default:
		return rf.fault(sip)
	}
}

func (rf *regFrame) slowStoreGlobal(dst int, nameIdx uint16, sip int) (val.Value, bool) {
	st := rf.st
	st.ip = sip
	rf.vm.execCtx.SP = rf.frameTop
	nip, nsp, ctl := rf.vm.opStoreGlobal(st, Encode(OP_STORE_GLOBAL, 0, nameIdx), sip, rf.base+dst+1)
	_ = nip
	_ = nsp
	switch ctl {
	case ctlNext, ctlReload:
		rf.refresh()
		rf.pc++
		return val.Value{}, false
	default:
		return rf.fault(sip)
	}
}

// arithFast runs the unboxed integer (and float addition) fast paths,
// mirroring the stack loop's overflow-checked arithmetic.
func (rf *regFrame) arithFast(op RegOp, dst, a, b int) bool {
	regs := rf.regs
	av, bv := regs[a], regs[b]
	if av.IsInt64() && bv.IsInt64() {
		ai, bi := av.AsInt64(), bv.AsInt64()
		var r int64
		var ok bool
		switch op {
		case RegAdd:
			r = ai + bi
			ok = ((ai ^ r) & (bi ^ r)) >= 0
		case RegSub:
			r = ai - bi
			ok = ((ai ^ bi) & (ai ^ r)) >= 0
		default:
			r = ai * bi
			ok = ai == 0 || r/ai == bi
		}
		if ok {
			regs[dst] = val.Int64Val(r)
			return true
		}
		return false
	}
	if op == RegAdd && av.IsFloat64() && bv.IsFloat64() {
		regs[dst] = val.Float64Val(av.AsFloat64() + bv.AsFloat64())
		return true
	}
	return false
}

func (rf *regFrame) arithSlow(op RegOp, dst, a, b int, sip int) (val.Value, bool) {
	av, bv := rf.regs[a], rf.regs[b]
	var res val.Value
	switch op {
	case RegAdd:
		res = val.AddCtx(&rf.vm.execCtx, av, bv)
	case RegSub:
		res = val.SubCtx(&rf.vm.execCtx, av, bv)
	case RegMul:
		res = val.MulCtx(&rf.vm.execCtx, av, bv)
	case RegTrueDiv:
		res = val.TrueDivCtx(&rf.vm.execCtx, av, bv)
	case RegFloorDiv:
		res = val.FloorDivCtx(&rf.vm.execCtx, av, bv)
	case RegMod:
		res = val.ModCtx(&rf.vm.execCtx, av, bv)
	case RegPow:
		res = val.PowCtx(&rf.vm.execCtx, av, bv)
	case RegMatMul:
		res = val.MatMul(&rf.vm.execCtx, av, bv)
	case RegBitAnd:
		res = val.BitAnd(&rf.vm.execCtx, av, bv)
	case RegBitOr:
		res = val.BitOr(&rf.vm.execCtx, av, bv)
	case RegBitXor:
		res = val.BitXor(&rf.vm.execCtx, av, bv)
	case RegLShift:
		res = val.LShift(&rf.vm.execCtx, av, bv)
	default:
		res = val.RShift(&rf.vm.execCtx, av, bv)
	}
	if rf.vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) unarySlow(op RegOp, dst, a int, sip int) (val.Value, bool) {
	av := rf.regs[a]
	var res val.Value
	switch op {
	case RegNeg:
		res = val.Negative(&rf.vm.execCtx, av)
	case RegPos:
		res = val.Positive(&rf.vm.execCtx, av)
	default:
		res = val.Invert(&rf.vm.execCtx, av)
	}
	if rf.vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

// cmpFast runs the inline compare fast paths, mirroring the stack loop.
func (rf *regFrame) cmpFast(op RegOp, dst, a, b int, aux uint32) bool {
	regs := rf.regs
	av, bv := regs[a], regs[b]
	switch op {
	case RegEq, RegNe:
		wantEq := op == RegEq
		if av.IsInt64() && bv.IsInt64() {
			regs[dst] = val.BoolVal((av.AsInt64() == bv.AsInt64()) == wantEq)
			return true
		}
		if isRegStr(av) && isRegStr(bv) {
			regs[dst] = val.BoolVal(val.StringEquals(av, bv) == wantEq)
			return true
		}
	case RegLt, RegLe, RegGt, RegGe:
		if av.IsInt64() && bv.IsInt64() {
			ai, bi := av.AsInt64(), bv.AsInt64()
			var c bool
			switch op {
			case RegLt:
				c = ai < bi
			case RegLe:
				c = ai <= bi
			case RegGt:
				c = ai > bi
			default:
				c = ai >= bi
			}
			regs[dst] = val.BoolVal(c)
			return true
		}
	case RegIs:
		if aux == uint32(CMP_IS) {
			regs[dst] = val.BoolVal(av == bv)
		} else {
			regs[dst] = val.BoolVal(av != bv)
		}
		return true
	case RegIn:
		if isRegStr(av) || av.IsInt64() {
			if bv.IsDict() && bv.HeaderPtr().Type == val.DictType {
				_, ok := bv.AsDict().Get(av)
				regs[dst] = val.BoolVal(ok)
				return true
			}
			if bv.IsSet() && bv.HeaderPtr().Type == val.SetType {
				regs[dst] = val.BoolVal(bv.AsSet().Contains(av))
				return true
			}
			if found, handled := val.ContainsSeqOrStrFast(av, bv); handled {
				regs[dst] = val.BoolVal(found)
				return true
			}
		}
	}
	return false
}

func (rf *regFrame) cmpSlow(op RegOp, dst, a, b int, sip int) (val.Value, bool) {
	av, bv := rf.regs[a], rf.regs[b]
	res := rf.vm.compareOpSafe(av, bv, regCmpOp(op))
	if rf.vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

// cmpJmp runs a fused compare-and-branch. Integer pairs compare inline;
// anything else falls back to the generic comparison.
func (rf *regFrame) cmpJmp(op RegOp, a, b, target int, sip int) (val.Value, bool) {
	av, bv := rf.regs[a], rf.regs[b]
	wantTrue := op == RegLtJmpIfTrue || op == RegEqJmpIfTrue
	isEq := op == RegEqJmpIfFalse || op == RegEqJmpIfTrue
	var c bool
	if av.IsInt64() && bv.IsInt64() {
		ai, bi := av.AsInt64(), bv.AsInt64()
		if isEq {
			c = ai == bi
		} else {
			c = ai < bi
		}
	} else {
		cmp := CMP_LT
		if isEq {
			cmp = CMP_EQ
		}
		res := rf.vm.compareOpSafe(av, bv, cmp)
		if rf.vm.execCtx.HasException() {
			return rf.failCtx(sip)
		}
		c = res.IsTruthy()
	}
	if c == wantTrue {
		if target <= rf.pc {
			return rf.backwardTick(sip, target)
		}
		rf.pc = target
		return val.Value{}, false
	}
	rf.pc++
	return val.Value{}, false
}
