package vm

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// slow dispatches register operations that delegate to synchronous helpers
// or need multi-step native logic. It returns (result, finished): finished
// reports frame completion (a return or an unwind).
func (rf *regFrame) slow(op RegOp, dst, src1, src2 int, aux uint32, sip int) (val.Value, bool) {
	switch op {
	case RegLoadAttr:
		return rf.loadAttr(dst, uint16(aux), sip)
	case RegStoreAttr:
		return rf.storeAttr(dst, src1, uint16(aux), sip)
	case RegLoadMethod:
		return rf.loadMethod(dst, src1, uint16(aux), sip)
	case RegCall:
		return rf.regCall(dst, src1, src2, int(aux&0xFFFF), sip)
	case RegCallMethod:
		return rf.regCallMethod(dst, src1, src2, int(aux&0xFFFF), sip)
	case RegCallKw:
		return rf.regCallKw(dst, src1, src2, aux, sip)
	case RegCallFunctionEx:
		return rf.regCallFunctionEx(dst, src1, src2, aux, sip)
	case RegReturn:
		return rf.regReturn(src1, sip)
	case RegBuildList, RegBuildTuple, RegBuildSet, RegBuildString:
		return rf.buildSeq(op, dst, int(aux), sip)
	case RegBuildDict:
		return rf.buildDict(dst, int(aux), sip)
	case RegBuildSlice:
		return rf.buildSlice(dst, aux, sip)
	case RegListAppend, RegSetAdd, RegListExtend:
		return rf.containerAdd(op, dst, src1, int(aux), sip)
	case RegMapAdd:
		return rf.mapAdd(dst, src1, src2, int(aux), sip)
	case RegUnpack:
		return rf.unpack(dst, int(aux), sip)
	case RegFormatValue:
		return rf.formatValue(dst, src1, src2, aux, sip)
	case RegMakeFunction:
		return rf.makeFunction(dst, aux, sip)
	case RegSetupRange:
		return rf.setupRange(dst, src1, src2, int(aux))
	case RegGetIter:
		return rf.getIter(dst, sip)
	case RegForIter:
		return rf.forIter(dst, src1, int(aux), sip)
	case RegForIterRange:
		return rf.forIterRange(dst, src1, int(aux), sip)
	case RegGetItem:
		return rf.getItem(dst, src1, src2, sip)
	case RegSetItem:
		return rf.setItem(dst, src1, src2, sip)
	case RegPopTop:
		return rf.popTop(src1)
	case RegDupTop:
		return rf.dupTop(dst, src1)
	case RegRaise:
		return rf.raiseOp(int(aux), src1, sip)
	default:
		panic("vm: unhandled register op")
	}
}

// prepSync readies loop state for a delegated helper call.
func (rf *regFrame) prepSync(sip int) {
	rf.st.ip = sip
	rf.vm.execCtx.SP = rf.frameTop
}

// buildSeq runs the counted container displays. Values occupy the window at
// regs[dst:dst+count]; the result lands in regs[dst].
func (rf *regFrame) buildSeq(op RegOp, dst, count int, sip int) (val.Value, bool) {
	var stackOp Opcode
	switch op {
	case RegBuildList:
		stackOp = OP_BUILD_LIST
	case RegBuildTuple:
		stackOp = OP_BUILD_TUPLE
	case RegBuildSet:
		stackOp = OP_BUILD_SET
	default:
		stackOp = OP_BUILD_STRING
	}
	rf.prepSync(sip)
	sp := rf.base + dst + count
	var ctl loopCtl
	vm := rf.vm
	st := rf.st
	switch stackOp {
	case OP_BUILD_LIST:
		_, _, ctl = vm.opBuildList(st, Encode(stackOp, 0, uint16(count)), sip, sp)
	case OP_BUILD_TUPLE:
		_, _, ctl = vm.opBuildTuple(st, Encode(stackOp, 0, uint16(count)), sip, sp)
	case OP_BUILD_SET:
		_, _, ctl = vm.opBuildSet(st, Encode(stackOp, 0, uint16(count)), sip, sp)
	default:
		_, _, ctl = vm.opBuildString(st, Encode(stackOp, 0, uint16(count)), sip, sp)
	}
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) buildDict(dst, pairs int, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	nip, _, ctl := rf.vm.opBuildMap(rf.st, Encode(OP_BUILD_MAP, 0, uint16(pairs)), sip, rf.base+dst+2*pairs)
	_ = nip
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) buildSlice(dst int, aux uint32, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	vm := rf.vm
	st := rf.st
	if aux&0x8000 != 0 {
		nip, _, ctl := vm.opBinarySlice(st, sip, rf.base+dst+3)
		_ = nip
		if ctl == ctlRaise {
			return rf.fault(sip)
		}
		rf.refresh()
		rf.pc++
		return val.Value{}, false
	}
	nip, _, ctl := vm.opBuildSlice(st, Encode(OP_BUILD_SLICE, 0, uint16(aux)), sip, rf.base+dst+int(aux))
	_ = nip
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

// containerAdd runs LIST_APPEND / SET_ADD / LIST_EXTEND. The container sits
// at regs[dst], the item at regs[dst+depth].
func (rf *regFrame) containerAdd(op RegOp, dst, item, depth int, sip int) (val.Value, bool) {
	var stackOp Opcode
	switch op {
	case RegListAppend:
		stackOp = OP_LIST_APPEND
	case RegSetAdd:
		stackOp = OP_SET_ADD
	default:
		stackOp = OP_LIST_EXTEND
	}
	rf.prepSync(sip)
	sp := rf.base + dst + depth + 1
	var nip int
	var ctl loopCtl
	vm := rf.vm
	st := rf.st
	inst := Encode(stackOp, 0, uint16(depth))
	switch stackOp {
	case OP_LIST_APPEND:
		nip, _, ctl = vm.opListAppend(st, inst, sip, sp)
	case OP_SET_ADD:
		nip, _, ctl = vm.opSetAdd(st, inst, sip, sp)
	default:
		nip, _, ctl = vm.opListExtend(st, inst, sip, sp)
	}
	_ = nip
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

// mapAdd runs MAP_ADD. The dict sits at regs[dst], key and value at
// regs[dst+depth] and regs[dst+depth+1].
func (rf *regFrame) mapAdd(dst, key, value, depth int, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	nip, _, ctl := rf.vm.opMapAdd(rf.st, Encode(OP_MAP_ADD, 0, uint16(depth)), sip, rf.base+dst+depth+2)
	_ = nip
	_ = key
	_ = value
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) unpack(dst, count int, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	nip, _, ctl := rf.vm.opUnpackSequence(rf.st, Encode(OP_UNPACK_SEQUENCE, 0, uint16(count)), sip, rf.base+dst+1)
	_ = nip
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) formatValue(dst, v, spec int, aux uint32, sip int) (val.Value, bool) {
	conv := uint8(aux)
	var hasSpec uint16
	sp := rf.base + dst + 1
	_ = spec
	if aux&(1<<8) != 0 {
		hasSpec = 1
		sp = rf.base + dst + 2
	}
	rf.prepSync(sip)
	nip, _, ctl := rf.vm.opFormatValue(rf.st, Encode(OP_FORMAT_VALUE, conv, hasSpec), sip, sp)
	_ = nip
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	_ = v
	return val.Value{}, false
}

func (rf *regFrame) makeFunction(dst int, aux uint32, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	flags := uint8(aux)
	count := 1
	for _, bit := range []uint8{0x01, 0x02, 0x04, 0x08} {
		if flags&bit != 0 {
			count++
		}
	}
	nip, _, ctl := rf.vm.opMakeFunction(rf.st, Encode(OP_MAKE_FUNCTION, flags, 0), sip, rf.base+dst+count)
	_ = nip
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) setItem(dst, obj, key int, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	nip, _, ctl := rf.vm.opStoreSubscr(rf.st, sip, rf.base+dst+3)
	_ = nip
	_ = obj
	_ = key
	if ctl == ctlRaise {
		return rf.fault(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) raiseOp(argc, w int, sip int) (val.Value, bool) {
	rf.prepSync(sip)
	nip, _, ctl := rf.vm.opRaise(rf.st, Encode(OP_RAISE, uint8(argc), 0), sip, rf.base+w+argc)
	_ = nip
	_ = ctl
	return rf.fault(sip)
}

func (rf *regFrame) popTop(src int) (val.Value, bool) {
	if rf.vm.hasTrackedViews {
		rf.vm.opReclaimPopTop(rf.regs[src])
	}
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) dupTop(dst, src int) (val.Value, bool) {
	top := rf.regs[src]
	if top.IsString() || top.Kind() == val.KindMemoryView {
		rf.vm.opDetachDupTop(top)
		top = rf.regs[src]
	} else if hdr := top.HeaderPtr(); hdr != nil {
		if (hdr.Flags & val.FlagEphemeralAll) != 0 {
			hdr.Flags &^= val.FlagEphemeralAll
		}
		if hdr.Kind == val.KindInstance && hdr == rf.vm.lastException.HeaderPtr() {
			hdr.Flags &^= val.FlagEphemeralAttrErr
		}
	}
	rf.regs[dst] = top
	rf.pc++
	return val.Value{}, false
}

// regReturn releases dead ephemeral iterators over the statically live
// scratch set, mirrors the return-value preparation, and finishes.
func (rf *regFrame) regReturn(src int, sip int) (val.Value, bool) {
	retVal := rf.regs[src]
	// Collect into a stack buffer so hot returns stay allocation-free;
	// append spills to the heap only for pathologically large live sets.
	var buf [16]val.Value
	live := buf[:0]
	for _, e := range rf.rc.RetLive {
		if e.pos != rf.pc {
			continue
		}
		for w := range 4 {
			bits := e.bits[w]
			for bits != 0 {
				b := bits & (-bits)
				idx := w*64 + bitIndex(b)
				if idx < len(rf.regs) {
					live = append(live, rf.regs[idx])
				}
				bits &^= b
			}
		}
	}
	if len(live) > 0 {
		rf.vm.releaseDeadEphemeralIters(live)
	}
	if retVal.IsString() || retVal.Kind() == val.KindMemoryView {
		rf.vm.prepareReturnValue(retVal)
	}
	_ = sip
	return retVal, true
}

func bitIndex(b uint64) int {
	n := 0
	for b >>= 1; b != 0; b >>= 1 {
		n++
	}
	return n
}

// loadAttr runs LOAD_ATTR with the instance fast paths inline and the
// generic lookup as fallback.
func (rf *regFrame) loadAttr(dst int, nameIdx uint16, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	regs := rf.regs
	obj := regs[dst]
	name := rf.consts[nameIdx]
	if instObj := obj.AsInstanceChecked(); instObj != nil {
		instShape := instObj.Shape
		instType := instObj.Header.Type
		if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
			typeVer := instType.Version()
			entry := &rf.cache.ic[sip]
			if entry.ExpectedTypeVer == typeVer {
				switch entry.Kind {
				case ICKindSlotMember:
					if slotIdx := entry.SlotIndex; uint(slotIdx) < uint(len(instObj.Slots)) {
						if v := instObj.Slots[slotIdx]; !v.IsUnbound() {
							regs[dst] = v
							rf.pc++
							return val.Value{}, false
						}
					}
				case ICKindProperty:
					return rf.loadAttrProperty(dst, entry, obj, sip)
				case ICKindClassAttr:
					if instShape.ID == entry.ExpectedShapeID {
						regs[dst] = entry.CachedVal
						rf.pc++
						return val.Value{}, false
					}
				}
			}
			if uint(sip) < uint(len(rf.cache.attrPIC)) {
				pic := &rf.cache.attrPIC[sip]
				for k := range 4 {
					if pic[k].Shape == instShape && pic[k].TypeVer == uint32(typeVer) {
						if slotIdx := pic[k].SlotIndex; uint(slotIdx) < uint(len(instObj.Slots)) {
							if v := instObj.Slots[slotIdx]; !v.IsUnbound() {
								regs[dst] = v
								rf.pc++
								return val.Value{}, false
							}
						}
					}
				}
			}
			if instShape.ID == entry.ExpectedShapeID && typeVer == entry.ExpectedTypeVer && entry.Kind == ICKindInstanceSlot {
				if slotIdx := entry.SlotIndex; uint(slotIdx) < uint(len(instObj.Slots)) {
					if v := instObj.Slots[slotIdx]; !v.IsUnbound() {
						regs[dst] = v
						rf.pc++
						return val.Value{}, false
					}
				}
			}
		}
		st.ip = sip
		vm.execCtx.SP = rf.frameTop
		res := vm.getAttrSafe(obj, name)
		if vm.execCtx.HasException() {
			return rf.failCtx(sip)
		}
		rf.refresh()
		rf.regs[dst] = res
		rf.pc++
		return val.Value{}, false
	}
	res, hit := rf.loadAttrNonInstance(obj, dst, sip, name)
	if hit {
		regs[dst] = res
		rf.pc++
		return val.Value{}, false
	}
	st.ip = sip
	vm.execCtx.SP = rf.frameTop
	res = vm.getAttrSafe(obj, name)
	if vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.refresh()
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

// loadAttrProperty emulates a property getter through a nested call,
// replacing the stack loop's in-loop frame push.
func (rf *regFrame) loadAttrProperty(dst int, entry *ICSlot, obj val.Value, sip int) (val.Value, bool) {
	vm := rf.vm
	fget := entry.CachedVal
	rf.st.ip = sip
	vm.execCtx.SP = rf.frameTop
	var res val.Value
	if fget.IsBuiltinFunc() {
		res = fget.AsBuiltinFunc().Fn(&vm.execCtx, fget, rf.regs[dst:dst+1], nil)
	} else {
		// Run the getter frame above the register file: its locals and
		// temporaries must not disturb live caller registers, and the
		// result returns by value.
		top := rf.frameTop
		if need := top + 2; need > len(rf.st.stack) {
			rf.st.stack = vm.growStack(rf.st, rf.st.stack, need)
		}
		rf.refresh()
		vm.execCtx.SP = top + 2
		rf.st.stack[top] = fget
		rf.st.stack[top+1] = obj
		res = vm.callFromHookSafe(fget, top+1, 1, nil)
		rf.refresh()
	}
	if vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.refresh()
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) loadAttrNonInstance(obj val.Value, dst int, sip int, name val.Value) (val.Value, bool) {
	entry := &rf.cache.ic[sip]
	switch {
	case obj.IsTuple():
		tp := obj.HeaderPtr().Type
		if tp != nil && (tp.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
			if entry.ExpectedTypeVer == tp.Version() {
				switch entry.Kind {
				case ICKindTupleGetter:
					t := obj.AsTuple()
					if uint(entry.SlotIndex) < uint(len(t.Items)) {
						return t.Items[entry.SlotIndex], true
					}
				case ICKindProperty:
					return rf.loadAttrProperty(dst, entry, obj, sip)
				}
			}
		}
	case obj.IsType():
		cls := obj.AsType()
		meta := cls.Header.Type
		if meta == nil {
			meta = val.TypeType
		}
		if entry.Kind == ICKindTypeAttr && entry.SlotIndex == 0 &&
			entry.ExpectedTypeVer == cls.Version() && entry.ExpectedAuxVer == meta.Version() {
			return entry.CachedVal, true
		}
	case obj.IsModule() && obj.AsModule().Header.Type == val.ModuleType:
		mod := obj.AsModule()
		if entry.Kind == ICKindModuleAttr && entry.ExpectedTypeVer == atomic.LoadUint64(&mod.Dict.VersionTag) {
			return entry.CachedVal, true
		}
	}
	return val.Value{}, false
}

// storeAttr runs STORE_ATTR with the instance fast paths inline and the
// generic setter as fallback.
func (rf *regFrame) storeAttr(dst, src int, nameIdx uint16, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	regs := rf.regs
	obj := regs[dst]
	v := regs[src]
	name := rf.consts[nameIdx]
	if vhdr := v.HeaderPtr(); (vhdr == nil || ((vhdr.Flags&val.FlagEphemeralAll) == 0 && vhdr.Kind != val.KindMemoryView && (vhdr.Kind != val.KindString || (*val.PyString)(unsafe.Pointer(vhdr)).Buf == nil))) && !val.MultiThreadedFlag.Load() {
		if instObj := obj.AsInstanceChecked(); instObj != nil {
			instShape := instObj.Shape
			instType := instObj.Header.Type
			if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomSetAttr) == 0 {
				typeVer := instType.Version()
				if uint(sip) < uint(len(rf.cache.attrPIC)) {
					pic := &rf.cache.attrPIC[sip]
					for k := range 4 {
						if pic[k].Shape == instShape && pic[k].TypeVer == uint32(typeVer) {
							if slotIdx := pic[k].SlotIndex; uint(slotIdx) < uint(len(instObj.Slots)) {
								instObj.Slots[slotIdx] = v
								rf.pc++
								return val.Value{}, false
							}
						}
					}
				}
				entry := &rf.cache.ic[sip]
				if typeVer == entry.ExpectedTypeVer {
					if entry.Kind == ICKindSlotMember {
						if slotIdx := entry.SlotIndex; uint(slotIdx) < uint(len(instObj.Slots)) {
							instObj.Slots[slotIdx] = v
							rf.pc++
							return val.Value{}, false
						}
					} else if instShape.ID == entry.ExpectedShapeID {
						if entry.Kind == ICKindInstanceSlot {
							if slotIdx := entry.SlotIndex; uint(slotIdx) < uint(len(instObj.Slots)) {
								instObj.Slots[slotIdx] = v
								rf.pc++
								return val.Value{}, false
							}
						} else if entry.Kind == ICKindInstanceTransition && uint(sip) < uint(len(rf.cache.transitionIC)) {
							tc := &rf.cache.transitionIC[sip]
							if tc.OldShape == instShape && tc.NewShape != nil && instObj.ExtendBootstrapSlot(int(tc.SlotIdx), v) {
								instObj.Shape = tc.NewShape
								rf.pc++
								return val.Value{}, false
							}
						}
					}
				}
			}
		}
	}
	st.ip = sip
	vm.execCtx.SP = rf.frameTop
	vm.setAttrSafe(obj, name, v)
	if vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.refresh()
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) loadMethod(dst, meth int, nameIdx uint16, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	regs := rf.regs
	obj := regs[dst]
	name := rf.consts[nameIdx]
	if instObj := obj.AsInstanceChecked(); instObj != nil {
		instShape := instObj.Shape
		instType := instObj.Header.Type
		if uint(sip) < uint(len(rf.cache.methodPIC)) && instShape != nil && instType != nil {
			pic := &rf.cache.methodPIC[sip]
			for k := range 4 {
				if pic[k].Shape == instShape && instType.Version() == pic[k].TypeVersion {
					regs[dst] = obj
					regs[meth] = pic[k].TargetVal
					rf.pc++
					return val.Value{}, false
				}
			}
		}
		if instShape != nil && instType != nil {
			entry := rf.cache.ic[sip]
			if instShape.ID == entry.ExpectedShapeID && instType.Version() == entry.ExpectedTypeVer && entry.Kind == ICKindClassMethod {
				regs[dst] = obj
				regs[meth] = entry.CachedVal
				rf.pc++
				return val.Value{}, false
			}
		}
	}
	st.ip = sip
	vm.execCtx.SP = rf.frameTop
	// The slow path may execute nested code; scratch runs above the
	// register file so live caller registers are never disturbed.
	recv, methVal := vm.loadMethodSlowSafe(obj, name, sip, rf.frameTop, st.frameIdx, st.cache.ic, st.cache.methodPIC)
	if vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.refresh()
	rf.regs[dst] = recv
	rf.regs[meth] = methVal
	rf.pc++
	return val.Value{}, false
}

// stageCall copies a [callee, args...] window of total slots starting at
// absolute stack position src into scratch slots above the register file
// and returns the absolute arg base (callee slot + 1). Callee frames
// committed through callFromHookSafe own [argBase, ...) and must never
// overlap live caller registers: the register window sits inside the
// live file, so the call is restaged above frameTop and the result
// returns by value.
func (rf *regFrame) stageCall(src, total int) int {
	vm := rf.vm
	st := rf.st
	top := rf.frameTop
	need := top + total
	if need > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, need)
	}
	rf.refresh()
	copy(st.stack[top:need], st.stack[src:src+total])
	vm.execCtx.SP = need
	return top + 1
}

// regCall runs a positional call. The callee sits at regs[callee] with argc
// contiguous arguments following it; the result lands in regs[dst].
func (rf *regFrame) regCall(dst, callee, args int, argc int, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	target := rf.regs[callee]
	rf.callTick()
	if vm.hasTrackedViews && argc > 0 {
		markViewsEscaped(rf.regs[args : args+argc])
	}
	st.ip = sip
	argBase := rf.stageCall(rf.base+callee, argc+1)
	res := vm.callFromHookSafe(target, argBase, argc, nil)
	rf.refresh()
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return rf.fault(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

// regCallMethod runs a method call. The receiver, callable, and argc
// arguments occupy regs[dst:dst+argc+2].
func (rf *regFrame) regCallMethod(dst, meth, args int, argc int, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	regs := rf.regs
	recv := regs[dst]
	callable := regs[meth]
	rf.callTick()
	if vm.hasTrackedViews && argc > 0 {
		markViewsEscaped(regs[args : args+argc])
	}
	st.ip = sip
	var target val.Value
	var argBase, nargs int
	if recv.IsUnbound() {
		// Stage [callable, args...] above the register file.
		argBase = rf.stageCall(rf.base+meth, argc+1)
		target, nargs = callable, argc
	} else {
		// Stage [recv, args...] above the register file.
		top := rf.frameTop
		need := top + argc + 1
		if need > len(st.stack) {
			st.stack = vm.growStack(st, st.stack, need)
		}
		rf.refresh()
		regs = rf.regs
		st.stack[top] = recv
		copy(st.stack[top+1:need], regs[args:args+argc])
		if vm.hasTrackedViews && argc+1 > 0 {
			markViewsEscaped(st.stack[top:need])
		}
		vm.execCtx.SP = need
		target, argBase, nargs = callable, top, argc+1
	}
	res := vm.callFromHookSafe(target, argBase, nargs, nil)
	rf.refresh()
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return rf.fault(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) regCallKw(dst, callee, args int, aux uint32, sip int) (val.Value, bool) {
	total := int(aux & 0xFFFF)
	kwIdx := int(aux >> 16)
	kwnames := rf.consts[kwIdx].AsTuple().Items
	vm := rf.vm
	st := rf.st
	target := rf.regs[callee]
	rf.callTick()
	if vm.hasTrackedViews && total > 0 {
		markViewsEscaped(rf.regs[args : args+total])
	}
	st.ip = sip
	argBase := rf.stageCall(rf.base+callee, total+1)
	res := vm.callFromHookSafe(target, argBase, total, kwnames)
	rf.refresh()
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return rf.fault(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

func (rf *regFrame) regCallFunctionEx(dst, callee, args int, aux uint32, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	hasKW := aux&1 != 0
	target := rf.regs[callee]
	argsVal := rf.regs[args]
	kwVal := val.NoneVal()
	if hasKW {
		kwVal = rf.regs[int(aux>>8)&0xFF]
	}
	rf.callTick()
	st.ip = sip
	vm.execCtx.SP = rf.frameTop
	res := vm.callFunctionExSafe(target, argsVal, kwVal, hasKW)
	rf.refresh()
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return rf.fault(sip)
	}
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

// setupRange mirrors OP_SETUP_RANGE exactly, including the silent no-op on
// non-integer bounds.
func (rf *regFrame) setupRange(dst, s1, s2, count int) (val.Value, bool) {
	regs := rf.regs
	if count == 1 {
		stop := regs[s1]
		if stop.IsInt64() {
			regs[dst] = val.RangeVal(0, int32(stop.AsInt64()))
		} else {
			regs[dst] = stop
		}
	} else {
		start, stop := regs[s1], regs[s2]
		if start.IsInt64() && stop.IsInt64() {
			regs[dst] = val.RangeVal(int32(start.AsInt64()), int32(stop.AsInt64()))
		} else {
			regs[dst] = start
		}
	}
	rf.pc++
	return val.Value{}, false
}

// getIter mirrors OP_GET_ITER: pooled iterators for lists and tuples, the
// generic protocol otherwise.
func (rf *regFrame) getIter(dst int, sip int) (val.Value, bool) {
	vm := rf.vm
	obj := rf.regs[dst]
	if k := obj.Kind(); k == val.KindList || k == val.KindTuple {
		rf.regs[dst] = vm.acquireForIter(obj)
		rf.pc++
		return val.Value{}, false
	}
	rf.st.ip = sip
	vm.execCtx.SP = rf.frameTop
	res := vm.getIterSafe(obj)
	if vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	rf.refresh()
	rf.regs[dst] = res
	rf.pc++
	return val.Value{}, false
}

// forIter mirrors OP_FOR_ITER. The iterator stays in regs[src]; the item
// lands in regs[dst] or control jumps to target.
func (rf *regFrame) forIter(dst, src, target int, sip int) (val.Value, bool) {
	vm := rf.vm
	st := rf.st
	it := rf.regs[src]
	if it.IsRange() {
		cur, stop := it.RangeStart(), it.RangeStop()
		if cur < stop {
			rf.regs[src] = val.RangeVal(cur+1, stop)
			rf.regs[dst] = val.Int64Val(int64(cur))
			rf.pc++
			return val.Value{}, false
		}
		rf.pc = target
		return val.Value{}, false
	}
	if it.IsIter() {
		pyIter := it.AsIter()
		switch pyIter.Kind {
		case val.IterKindList:
			items := pyIter.Obj.AsList().Items
			if uint(pyIter.Idx) < uint(len(items)) {
				rf.regs[dst] = items[pyIter.Idx]
				pyIter.Idx++
				rf.pc++
				return val.Value{}, false
			}
			vm.releaseForIter(pyIter)
			rf.pc = target
			return val.Value{}, false
		case val.IterKindTuple:
			items := pyIter.Obj.AsTuple().Items
			if uint(pyIter.Idx) < uint(len(items)) {
				rf.regs[dst] = items[pyIter.Idx]
				pyIter.Idx++
				rf.pc++
				return val.Value{}, false
			}
			vm.releaseForIter(pyIter)
			rf.pc = target
			return val.Value{}, false
		case val.IterKindInvalid:
			st.raiseExc = val.NewTypeError(fmt.Sprintf("'%s' object is not iterable", pyIter.Obj.Type().Name))
			return rf.fault(sip)
		case val.IterKindZip, val.IterKindEnumerate:
			st.ip = sip
			vm.execCtx.SP = rf.frameTop
			nextVal, ok := vm.nextZipEnumeratePooled(pyIter)
			rf.refresh()
			if ok {
				rf.regs[dst] = nextVal
				rf.pc++
				return val.Value{}, false
			}
			vm.releaseForIter(pyIter)
			rf.pc = target
			return val.Value{}, false
		default:
			st.ip = sip
			vm.execCtx.SP = rf.frameTop
			nextVal, ok := pyIter.Next()
			rf.refresh()
			if vm.execCtx.HasException() {
				return rf.failCtx(sip)
			}
			if ok {
				rf.regs[dst] = nextVal
				rf.pc++
				return val.Value{}, false
			}
			vm.releaseForIter(pyIter)
			rf.pc = target
			return val.Value{}, false
		}
	}
	if it.IsGenerator() {
		gen := it.AsGenerator()
		st.ip = sip
		vm.execCtx.SP = rf.frameTop
		nextVal, status := vm.resumeGenerator(&vm.execCtx, gen, val.NoneVal())
		rf.refresh()
		switch status {
		case val.IterYielded:
			rf.regs[dst] = nextVal
			rf.pc++
			return val.Value{}, false
		case val.IterExhausted:
			vm.releaseGenerator(gen)
			rf.pc = target
			return val.Value{}, false
		default:
			if vm.execCtx.HasException() {
				return rf.failCtx(sip)
			}
			rf.pc = target
			return val.Value{}, false
		}
	}
	if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
		st.ip = sip
		vm.execCtx.SP = rf.frameTop
		nextVal, status := vm.iterNextSafe(tpIterNext, it)
		rf.refresh()
		switch status {
		case val.IterYielded:
			rf.regs[dst] = nextVal
			rf.pc++
			return val.Value{}, false
		case val.IterExhausted:
			rf.pc = target
			return val.Value{}, false
		default:
			if vm.execCtx.HasException() {
				return rf.failCtx(sip)
			}
			panic(fmt.Sprintf("vm: RuntimeError: iterator '%s' returned status %d without active exception", it.Type().Name, status))
		}
	}
	st.raiseExc = val.NewTypeError(fmt.Sprintf("'%s' object is not an iterator", it.Type().Name))
	return rf.fault(sip)
}

// forIterRange mirrors OP_FOR_ITER_RANGE without the stream-write fusion:
// a range iterator advances in place and writes the loop variable, or
// control jumps to target.
func (rf *regFrame) forIterRange(dst, src, target int, sip int) (val.Value, bool) {
	_ = sip
	regs := rf.regs
	it := regs[src]
	if it.IsRange() {
		cur, stop := it.RangeStart(), it.RangeStop()
		if cur < stop {
			regs[src] = val.RangeVal(cur+1, stop)
			regs[dst] = val.Int64Val(int64(cur))
			rf.pc++
			return val.Value{}, false
		}
	}
	rf.pc = target
	return val.Value{}, false
}

// getItem mirrors OP_BINARY_SUBSCR: inline container fast paths with the
// generic subscript protocol as fallback.
func (rf *regFrame) getItem(dst, objR, keyR int, sip int) (val.Value, bool) {
	vm := rf.vm
	regs := rf.regs
	obj, key := regs[objR], regs[keyR]
	switch {
	case obj.IsList() && key.IsInt64() && obj.AsList().Header.Type == val.ListType:
		items := obj.AsList().Items
		if k := key.AsInt64(); uint64(k) < uint64(len(items)) {
			regs[dst] = items[k]
			rf.pc++
			return val.Value{}, false
		}
	case obj.IsTuple() && key.IsInt64() && obj.AsTuple().Header.Type == val.TupleType:
		items := obj.AsTuple().Items
		if k := key.AsInt64(); uint64(k) < uint64(len(items)) {
			regs[dst] = items[k]
			rf.pc++
			return val.Value{}, false
		}
	case val.IsInlineStrASCII(obj) && key.IsInt64():
		raw := obj.RawNum()
		if k := key.AsInt64(); uint64(k) < (raw & 0xFF) {
			regs[dst] = val.InlineByteCharVal(byte(raw >> (uint(k) + 1) * 8))
			rf.pc++
			return val.Value{}, false
		}
	case obj.IsDict() && obj.AsDict().Header.Type == val.DictType &&
		(key.IsInt64() || key.IsInlineStr() || (key.IsString() && key.AsStringObj().Header.Type == val.StrType)):
		if v, ok := obj.AsDict().Get(key); ok {
			regs[dst] = v
			rf.pc++
			return val.Value{}, false
		}
	}
	rf.st.ip = sip
	vm.execCtx.SP = rf.frameTop
	res := val.GetItemCtx(&vm.execCtx, obj, key)
	if vm.execCtx.HasException() {
		return rf.failCtx(sip)
	}
	regs[dst] = res
	rf.pc++
	return val.Value{}, false
}
