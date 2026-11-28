package vm

import (
	"math"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// enterRegFromTier1 is invoked by dispatchColdOpcode on OP_ENTER_REG to execute
// a promoted RegCode function inside VM.runRegLoop without modifying VM.runLoopWithGlobals.
//
//go:noinline
func (vm *VM) enterRegFromTier1(st *loopState, ip, sp int) (int, int, loopCtl) {
	co := st.curCO
	cc := st.cache
	if co == nil || cc == nil {
		return ip, sp, ctlExit
	}
	rc := cc.regCode
	if rc == nil {
		rc = co.regCode.Load()
		if rc != nil {
			cc.regCode = rc
		}
	}
	// Guard: only enter runRegLoop at function entry with empty operand stack and standard frame state.
	expectedLocals := max(co.EffectiveLocals, max(co.NumLocals, co.NumParams))
	if rc == nil || ip != co.EntryIP || sp != st.base+expectedLocals ||
		!st.curLocals.IsNone() || st.activeGen != nil || st.activeCoro != nil || vm.hasTrackedViews {
		// Fallback: restore original Tier-1 instruction word at ip and reload into runLoopWithGlobals.
		origStream := co.Active()
		if ip >= 0 && ip < len(origStream) {
			st.codePtr = cc.quickenAt(ip, origStream[ip])
			cc.regEntryInstalled = false
		}
		return ip, sp, ctlReload
	}

	need := st.base + max(256, int(rc.maxRegs)+64)
	if need > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, need)
	}
	// Initialize non-parameter, non-direct-capture locals to UnboundVal() so
	// definite-assignment REG_COLD_CHECK_LOCAL raises UnboundLocalError cleanly.
	paramEnd := max(co.NumParams, len(co.ParamNames))
	unboundEnd := int(rc.numLocals)
	if co.DirectCaptureCount > 0 && co.DirectCaptureBase < unboundEnd {
		unboundEnd = co.DirectCaptureBase
	}
	for i := paramEnd; i < unboundEnd; i++ {
		if i == co.VarArgsIdx || i == co.VarKWIdx {
			continue
		}
		st.stack[st.base+i] = val.UnboundVal()
	}
	if int(rc.maxRegs) > int(rc.numLocals) {
		clear(st.stack[st.base+int(rc.numLocals) : st.base+int(rc.maxRegs)])
	}
	if rc.numIC > 0 && len(cc.regIC) < rc.numIC {
		cc.ensureRegIC(rc.numIC)
	}

	entryFrameIdx := st.frameIdx
	savedRegCode := st.regCode
	savedExecSP := vm.execCtx.SP
	st.regCode = rc
	vm.execCtx.SP = st.base + int(rc.maxRegs)
	retVal, raised, nextIP, nextSP, ctl := vm.runRegLoop(st, rc, entryFrameIdx)
	st.regCode = savedRegCode
	vm.execCtx.SP = savedExecSP

	if raised {
		return nextIP, nextSP, ctl
	}
	if st.frameIdx == st.startFrameIdx {
		st.result = retVal
		return 0, 0, ctlExit
	}
	return vm.opReturnSlow(st, ip, sp, retVal)
}

const (
	regSlowCold uint8 = iota
	regSlowPushFrame
	regSlowReturn
	regSlowMethodNative
	regSlowCallBuiltin
	regSlowCallTier1
	regSlowSafepoint
	regSlowForIterExhaust
	regSlowDictGet
	regSlowDictSet
	regSlowStoreGlobal
)

// runRegLoop executes Tier-2 3-address register bytecode directly on vm.stack[st.base:].
// Uses a two-level loop: the inner hot loop contains zero Go CALLs and keeps
// codePtr/regsPtr/constsPtr/rc strictly loop-invariant so Go's arm64 register
// allocator emits zero per-instruction (RSP) spill stores at the dispatch header.
//
//go:noinline
func (vm *VM) runRegLoop(st *loopState, rc *RegCode, entryFrameIdx int) (val.Value, bool, int, int, loopCtl) {
	var (
		ip            = 0
		codePtr       = rc.codePtr
		regsPtr       = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
		constsPtr     = unsafe.Pointer(unsafe.SliceData(st.consts))
		pushFn        *val.PyFunction
		pushCC        *VMCodeCache
		pushRC        *RegCode
		pushNewBase   int
		pushDstReg    int
		needSafepoint bool
		nativeFn      val.VectorcallFunc
		nativeSelf    val.Value
		nativeArgReg  uint8
		nativeArgc    int
		nativeDstReg  uint8
		exhaustIter   *val.PyIter
		retVal        val.Value
		slowKind      uint8
	)

	for {
		slowKind = regSlowCold
	innerLoop:
		for {
			inst := *(*RegInst)(unsafe.Add(codePtr, uintptr(ip)*8))
			op := RegOp(inst >> 56)

			switch op {
			case REG_MOV:
				rd := uint8(inst >> 48)
				ra := uint8(inst >> 40)
				*(*val.Value)(unsafe.Add(regsPtr, uintptr(rd)*16)) = *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
				ip++
				continue

			case REG_LOAD_CONST:
				rd := uint8(inst >> 48)
				*(*val.Value)(unsafe.Add(regsPtr, uintptr(rd)*16)) = *(*val.Value)(unsafe.Add(constsPtr, uintptr(uint32(inst))*16))
				ip++
				continue

			case REG_LOAD_GLOBAL:
				if (st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil) && st.cache.regGlobals == st.globals {
					icIdx := uint32(inst) & 0xFFFF
					if int(icIdx) < len(st.cache.regIC) {
						entry := &st.cache.regIC[icIdx]
						globVer := atomic.LoadUint64(&st.globals.VersionTag)
						if entry.ExpectedTypeVer == globVer &&
							(entry.Kind == ICKindGlobal || (entry.Kind == ICKindBuiltin && entry.ExpectedAuxVer == atomic.LoadUint64(&vm.builtins.VersionTag))) {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = entry.CachedVal
							ip++
							continue
						}
					}
				}
				break innerLoop

			case REG_STORE_GLOBAL:
				slowKind = regSlowStoreGlobal
				break innerLoop

			case REG_LOAD_DEREF:
				idx := uint32(inst)
				activePtrs := vm.activeCellPtrs
				if uint(idx) < uint(len(activePtrs)) && activePtrs[idx] != nil {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = activePtrs[idx].Value
					ip++
					continue
				}
				if uint(idx) < uint(len(st.cells)) {
					cell := (*val.PyCell)(unsafe.Pointer(st.cells[idx].HeaderPtr()))
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = cell.Value
					ip++
					continue
				}
				break innerLoop

			case REG_ADD:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					ai, bi := a.AsInt64(), b.AsInt64()
					sum := ai + bi
					if ((ai ^ sum) & (bi ^ sum)) >= 0 {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(sum)
						ip++
						continue
					}
				} else if a.IsFloat64() && b.IsFloat64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Float64Val(a.AsFloat64() + b.AsFloat64())
					ip++
					continue
				}
				break innerLoop

			case REG_SUB:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					ai, bi := a.AsInt64(), b.AsInt64()
					diff := ai - bi
					if ((ai ^ bi) & (ai ^ diff)) >= 0 {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(diff)
						ip++
						continue
					}
				} else if a.IsFloat64() && b.IsFloat64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Float64Val(a.AsFloat64() - b.AsFloat64())
					ip++
					continue
				}
				break innerLoop

			case REG_MUL:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					ai, bi := a.AsInt64(), b.AsInt64()
					if int64(int32(ai)) == ai && int64(int32(bi)) == bi {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(ai * bi)
						ip++
						continue
					}
				} else if a.IsFloat64() && b.IsFloat64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Float64Val(a.AsFloat64() * b.AsFloat64())
					ip++
					continue
				}
				break innerLoop

			case REG_MOD:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					ai, bi := a.AsInt64(), b.AsInt64()
					if bi != 0 && (ai != -1<<63 || bi != -1) {
						r := ai % bi
						if r != 0 && (r^bi) < 0 {
							r += bi
						}
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(r)
						ip++
						continue
					}
				}
				break innerLoop

			case REG_FLOOR_DIV:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					ai, bi := a.AsInt64(), b.AsInt64()
					if bi != 0 && (ai != -1<<63 || bi != -1) {
						q := ai / bi
						if ai%bi != 0 && (ai^bi) < 0 {
							q--
						}
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(q)
						ip++
						continue
					}
				}
				break innerLoop

			case REG_BIT_AND:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(a.AsInt64() & b.AsInt64())
					ip++
					continue
				}
				break innerLoop

			case REG_BIT_OR:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(a.AsInt64() | b.AsInt64())
					ip++
					continue
				}
				break innerLoop

			case REG_BIT_XOR:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(a.AsInt64() ^ b.AsInt64())
					ip++
					continue
				}
				break innerLoop

			case REG_CMP_LT:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(a.AsInt64() < b.AsInt64())
					ip++
					continue
				}
				break innerLoop

			case REG_CMP_EQ:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				if a.IsInt64() && b.IsInt64() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(a.AsInt64() == b.AsInt64())
					ip++
					continue
				}
				if a.IsInlineStr() && b.IsInlineStr() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(a.RawNum() == b.RawNum())
					ip++
					continue
				}
				break innerLoop

			case REG_CMP_OP:
				a := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				b := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				cmpOp := uint8(inst)
				if cmpOp == CMP_IS {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(a == b)
					ip++
					continue
				}
				if cmpOp == CMP_IS_NOT {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(a != b)
					ip++
					continue
				}
				if a.IsInt64() && b.IsInt64() && cmpOp <= CMP_GTE {
					ai, bi := a.AsInt64(), b.AsInt64()
					var res bool
					switch cmpOp {
					case CMP_EQ:
						res = ai == bi
					case CMP_NEQ:
						res = ai != bi
					case CMP_LT:
						res = ai < bi
					case CMP_LTE:
						res = ai <= bi
					case CMP_GT:
						res = ai > bi
					case CMP_GTE:
						res = ai >= bi
					}
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(res)
					ip++
					continue
				}
				if cmpOp == CMP_NEQ && a.IsInlineStr() && b.IsInlineStr() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.BoolVal(a.RawNum() != b.RawNum())
					ip++
					continue
				}
				break innerLoop

			case REG_JMP:
				target := int(uint32(inst))
				if target <= ip {
					st.periodicTick++
					if (st.periodicTick & 0x3FF) == 0 {
						ip = target
						slowKind = regSlowSafepoint
						break innerLoop
					}
				}
				ip = target
				continue

			case REG_JIF:
				cond := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				var truthy bool
				switch {
				case cond.IsBool():
					truthy = cond.AsBool()
				case cond.IsInt64():
					truthy = cond.AsInt64() != 0
				case cond.IsNone():
					truthy = false
				case cond.IsInlineStr():
					truthy = (cond.RawNum() & 0xFF) != 0
				case cond.IsString() && cond.AsStringObj().Header.Type == val.StrType:
					truthy = len(cond.AsStringObj().Data) != 0
				case cond.IsList() && cond.AsList().Header.Type == val.ListType:
					truthy = len(cond.AsList().Items) != 0
				case cond.IsTuple() && cond.AsTuple().Header.Type == val.TupleType:
					truthy = len(cond.AsTuple().Items) != 0
				default:
					break innerLoop
				}
				if !truthy {
					ip = int(uint32(inst))
				} else {
					ip++
				}
				continue

			case REG_JIT:
				cond := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				var truthy bool
				switch {
				case cond.IsBool():
					truthy = cond.AsBool()
				case cond.IsInt64():
					truthy = cond.AsInt64() != 0
				case cond.IsNone():
					truthy = false
				case cond.IsInlineStr():
					truthy = (cond.RawNum() & 0xFF) != 0
				case cond.IsString() && cond.AsStringObj().Header.Type == val.StrType:
					truthy = len(cond.AsStringObj().Data) != 0
				case cond.IsList() && cond.AsList().Header.Type == val.ListType:
					truthy = len(cond.AsList().Items) != 0
				case cond.IsTuple() && cond.AsTuple().Header.Type == val.TupleType:
					truthy = len(cond.AsTuple().Items) != 0
				default:
					break innerLoop
				}
				if truthy {
					ip = int(uint32(inst))
				} else {
					ip++
				}
				continue

			case REG_LOAD_ATTR:
				obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				icIdx := int(uint16(inst))
				if instObj := obj.AsInstanceChecked(); instObj != nil {
					instShape := instObj.Shape
					instType := instObj.Header.Type
					if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
						typeVer := instType.Version()
						if uint(icIdx) < uint(len(st.cache.regAttrPIC)) {
							pic := &st.cache.regAttrPIC[icIdx]
							tv32 := uint32(typeVer)
							for k := range 4 {
								if pic[k].Shape == instShape && pic[k].TypeVer == tv32 {
									slotIdx := pic[k].SlotIndex
									if uint(slotIdx) < uint(len(instObj.Slots)) {
										if v := instObj.Slots[slotIdx]; !v.IsUnbound() {
											*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = v
											ip++
											continue innerLoop
										}
									}
								}
							}
						}
						if uint(icIdx) < uint(len(st.cache.regIC)) {
							entry := &st.cache.regIC[icIdx]
							if entry.ExpectedTypeVer == typeVer {
								switch {
								case entry.Kind == ICKindSlotMember:
									if uint(entry.SlotIndex) < uint(len(instObj.Slots)) {
										if v := instObj.Slots[entry.SlotIndex]; !v.IsUnbound() {
											*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = v
											ip++
											continue
										}
									}
								case entry.Kind == ICKindInstanceSlot && instShape.ID == entry.ExpectedShapeID:
									if uint(entry.SlotIndex) < uint(len(instObj.Slots)) {
										if v := instObj.Slots[entry.SlotIndex]; !v.IsUnbound() {
											*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = v
											ip++
											continue
										}
									}
								case entry.Kind == ICKindClassAttr && instShape.ID == entry.ExpectedShapeID:
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = entry.CachedVal
									ip++
									continue
								case entry.Kind == ICKindProperty && !vm.hasTrackedViews && entry.CalleeCache != nil:
									cc := entry.CalleeCache
									calleeRC := cc.regCode
									if calleeRC == nil && cc.co != nil {
										if loaded := cc.co.regCode.Load(); loaded != nil {
											cc.regCode = loaded
											calleeRC = loaded
										}
									}
									if calleeRC != nil && cc.owner == vm && entry.CachedVal.IsFunction() {
										fn := entry.CachedVal.AsFunction()
										if fn.Code.HeaderPtr() == &cc.co.Header && len(fn.Defaults) == 0 && fn.KwDefaults == nil &&
											cc.co.SimplePositionalOnly && cc.co.PositionalParamCount == 1 && !cc.co.HasCellVars {
											newBase := st.base + int(rc.maxRegs)
											st.periodicTick++
											if st.frameIdx+1 < 1000 &&
												st.frameIdx+2 <= len(vm.frames) &&
												newBase+max(256, int(calleeRC.maxRegs)+64) <= len(st.stack) {
												st.stack[newBase] = obj
												pushFn = fn
												pushCC = cc
												pushRC = calleeRC
												pushNewBase = newBase
												pushDstReg = int(uint8(inst >> 48))
												needSafepoint = (st.periodicTick & 0xFF) == 0
												slowKind = regSlowPushFrame
												break innerLoop
											}
										}
									}
								}
							}
						}
					}
				} else if obj.IsTuple() {
					tp := obj.HeaderPtr().Type
					if tp != nil && (tp.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() && uint(icIdx) < uint(len(st.cache.regIC)) {
						entry := &st.cache.regIC[icIdx]
						if entry.ExpectedTypeVer == tp.Version() {
							if entry.Kind == ICKindTupleGetter {
								t := obj.AsTuple()
								if uint(entry.SlotIndex) < uint(len(t.Items)) {
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = t.Items[entry.SlotIndex]
									ip++
									continue
								}
							} else if entry.Kind == ICKindProperty && !vm.hasTrackedViews && entry.CalleeCache != nil {
								cc := entry.CalleeCache
								calleeRC := cc.regCode
								if calleeRC == nil && cc.co != nil {
									if loaded := cc.co.regCode.Load(); loaded != nil {
										cc.regCode = loaded
										calleeRC = loaded
									}
								}
								if calleeRC != nil && cc.owner == vm && entry.CachedVal.IsFunction() {
									fn := entry.CachedVal.AsFunction()
									if fn.Code.HeaderPtr() == &cc.co.Header && len(fn.Defaults) == 0 && fn.KwDefaults == nil &&
										cc.co.SimplePositionalOnly && cc.co.PositionalParamCount == 1 && !cc.co.HasCellVars {
										newBase := st.base + int(rc.maxRegs)
										st.periodicTick++
										if st.frameIdx+1 < 1000 &&
											st.frameIdx+2 <= len(vm.frames) &&
											newBase+max(256, int(calleeRC.maxRegs)+64) <= len(st.stack) {
											st.stack[newBase] = obj
											pushFn = fn
											pushCC = cc
											pushRC = calleeRC
											pushNewBase = newBase
											pushDstReg = int(uint8(inst >> 48))
											needSafepoint = (st.periodicTick & 0xFF) == 0
											slowKind = regSlowPushFrame
											break innerLoop
										}
									}
								}
							}
						}
					}
				} else if obj.IsModule() && obj.AsModule().Header.Type == val.ModuleType {
					mod := obj.AsModule()
					if uint(icIdx) < uint(len(st.cache.regIC)) {
						entry := &st.cache.regIC[icIdx]
						if entry.Kind == ICKindModuleAttr && entry.ExpectedTypeVer == atomic.LoadUint64(&mod.Dict.VersionTag) {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = entry.CachedVal
							ip++
							continue
						}
					}
				}
				break innerLoop

			case REG_STORE_ATTR:
				obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				v := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				vhdr := v.HeaderPtr()
				if (vhdr == nil || ((vhdr.Flags&val.FlagEphemeralAll) == 0 && vhdr.Kind != val.KindMemoryView && (vhdr.Kind != val.KindString || (*val.PyString)(unsafe.Pointer(vhdr)).Buf == nil))) && !val.MultiThreadedFlag.Load() {
					if instObj := obj.AsInstanceChecked(); instObj != nil {
						instShape := instObj.Shape
						instType := instObj.Header.Type
						if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomSetAttr) == 0 {
							typeVer := instType.Version()
							icIdx := int(uint16(inst))
							if uint(icIdx) < uint(len(st.cache.regAttrPIC)) {
								pic := &st.cache.regAttrPIC[icIdx]
								tv32 := uint32(typeVer)
								for k := range 4 {
									if pic[k].Shape == instShape && pic[k].TypeVer == tv32 {
										slotIdx := pic[k].SlotIndex
										if uint(slotIdx) < uint(len(instObj.Slots)) {
											instObj.Slots[slotIdx] = v
											ip++
											continue innerLoop
										}
									}
								}
							}
							if uint(icIdx) < uint(len(st.cache.regIC)) {
								entry := &st.cache.regIC[icIdx]
								if entry.ExpectedTypeVer == typeVer {
									if entry.Kind == ICKindSlotMember {
										if uint(entry.SlotIndex) < uint(len(instObj.Slots)) {
											instObj.Slots[entry.SlotIndex] = v
											ip++
											continue
										}
									} else if entry.Kind == ICKindInstanceSlot && instShape.ID == entry.ExpectedShapeID {
										if uint(entry.SlotIndex) < uint(len(instObj.Slots)) {
											instObj.Slots[entry.SlotIndex] = v
											ip++
											continue
										}
									}
								}
							}
						}
					}
				}
				break innerLoop

			case REG_SUBSCR:
				obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				key := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				switch {
				case obj.IsList() && key.IsInt64() && obj.AsList().Header.Type == val.ListType:
					items := obj.AsList().Items
					k := key.AsInt64()
					if uint64(k) < uint64(len(items)) {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = items[k]
						ip++
						continue
					}
				case obj.IsTuple() && key.IsInt64() && obj.AsTuple().Header.Type == val.TupleType:
					items := obj.AsTuple().Items
					k := key.AsInt64()
					if uint64(k) < uint64(len(items)) {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = items[k]
						ip++
						continue
					}
				case val.IsInlineStrASCII(obj) && key.IsInt64():
					raw := obj.RawNum()
					k := key.AsInt64()
					if uint64(k) < (raw & 0xFF) {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.InlineByteCharVal(byte(raw >> ((uint(k) + 1) * 8)))
						ip++
						continue
					}
				case obj.IsDict() && obj.AsDict().Header.Type == val.DictType &&
					(key.IsInt64() || key.IsInlineStr() || (key.IsString() && key.AsStringObj().Header.Type == val.StrType)):
					slowKind = regSlowDictGet
					break innerLoop
				}
				break innerLoop

			case REG_STORE_SUBSCR:
				v := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16))
				obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				key := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
				vhdr := v.HeaderPtr()
				if vhdr == nil || ((vhdr.Flags&val.FlagEphemeralAll) == 0 && vhdr.Kind != val.KindMemoryView && (vhdr.Kind != val.KindString || (*val.PyString)(unsafe.Pointer(vhdr)).Buf == nil)) {
					if obj.IsList() && key.IsInt64() && obj.AsList().Header.Type == val.ListType {
						items := obj.AsList().Items
						k := key.AsInt64()
						if uint64(k) < uint64(len(items)) {
							items[k] = v
							ip++
							continue
						}
					} else if obj.IsDict() && obj.AsDict().Header.Type == val.DictType &&
						(key.IsInt64() || key.IsInlineStr() || (key.IsString() && key.AsStringObj().Header.Type == val.StrType)) {
						slowKind = regSlowDictSet
						break innerLoop
					}
				}
				break innerLoop

			case REG_GET_ITER:
				obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				if obj.IsRange() {
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = obj
					ip++
					continue
				}
				break innerLoop

			case REG_FOR_ITER:
				ra := uint8(inst >> 40)
				it := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
				if it.IsRange() {
					cur := it.RangeStart()
					stop := it.RangeStop()
					if cur < stop {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16)) = val.RangeVal(cur+1, stop)
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(int64(cur))
						ip++
						continue
					}
					ip = int(uint32(inst))
					continue
				}
				if it.IsIter() {
					pyIter := it.AsIter()
					if pyIter.Kind == val.IterKindList {
						items := pyIter.Obj.AsList().Items
						if uint(pyIter.Idx) < uint(len(items)) {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = items[pyIter.Idx]
							pyIter.Idx++
							ip++
							continue
						}
						exhaustIter = pyIter
						slowKind = regSlowForIterExhaust
						break innerLoop
					}
					if pyIter.Kind == val.IterKindTuple {
						items := pyIter.Obj.AsTuple().Items
						if uint(pyIter.Idx) < uint(len(items)) {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = items[pyIter.Idx]
							pyIter.Idx++
							ip++
							continue
						}
						exhaustIter = pyIter
						slowKind = regSlowForIterExhaust
						break innerLoop
					}
				}
				break innerLoop

			case REG_FOR_ITER_UNPACK2:
				ra := uint8(inst >> 40)
				it := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
				if it.IsIter() {
					pyIter := it.AsIter()
					if pyIter.Kind == val.IterKindEnumerate {
						if pyIter.Sub.HeaderPtr() == nil && pyIter.Cur < math.MaxInt64 && pyIter.Obj.IsIter() {
							sub := pyIter.Obj.AsIter()
							if sub.Kind == val.IterKindList {
								items := sub.Obj.AsList().Items
								if uint(sub.Idx) < uint(len(items)) {
									idx := pyIter.Cur
									pyIter.Cur = idx + 1
									v := items[sub.Idx]
									sub.Idx++
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(idx)
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16)) = v
									ip++
									continue
								}
								exhaustIter = pyIter
								slowKind = regSlowForIterExhaust
								break innerLoop
							}
							if sub.Kind == val.IterKindTuple {
								items := sub.Obj.AsTuple().Items
								if uint(sub.Idx) < uint(len(items)) {
									idx := pyIter.Cur
									pyIter.Cur = idx + 1
									v := items[sub.Idx]
									sub.Idx++
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(idx)
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16)) = v
									ip++
									continue
								}
								exhaustIter = pyIter
								slowKind = regSlowForIterExhaust
								break innerLoop
							}
						}
					} else if pyIter.Kind == val.IterKindZip && pyIter.Stop == 0 && pyIter.Step == 0 && len(pyIter.Seq) == 2 {
						s0, s1 := pyIter.Seq[0], pyIter.Seq[1]
						if s0.IsIter() && s1.IsIter() {
							it0, it1 := s0.AsIter(), s1.AsIter()
							if it0.Kind == val.IterKindList && it1.Kind == val.IterKindList {
								items0 := it0.Obj.AsList().Items
								items1 := it1.Obj.AsList().Items
								if uint(it0.Idx) < uint(len(items0)) && uint(it1.Idx) < uint(len(items1)) {
									v0 := items0[it0.Idx]
									v1 := items1[it1.Idx]
									it0.Idx++
									it1.Idx++
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = v0
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16)) = v1
									ip++
									continue
								}
							} else if it0.Kind == val.IterKindTuple && it1.Kind == val.IterKindTuple {
								items0 := it0.Obj.AsTuple().Items
								items1 := it1.Obj.AsTuple().Items
								if uint(it0.Idx) < uint(len(items0)) && uint(it1.Idx) < uint(len(items1)) {
									v0 := items0[it0.Idx]
									v1 := items1[it1.Idx]
									it0.Idx++
									it1.Idx++
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = v0
									*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16)) = v1
									ip++
									continue
								}
							}
						}
					}
				}
				break innerLoop

			case REG_SETUP_RANGE:
				ra := uint8(inst >> 40)
				count := uint8(inst >> 32)
				if count == 1 {
					stopVal := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
					if stopVal.IsInt64() {
						stop := stopVal.AsInt64()
						if stop >= 0 && stop <= 0x7FFFFFFF {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.RangeVal(0, int32(stop))
							ip++
							continue
						}
					}
				} else if count == 2 {
					startVal := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
					stopVal := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra+1)*16))
					if startVal.IsInt64() && stopVal.IsInt64() {
						start := startVal.AsInt64()
						stop := stopVal.AsInt64()
						if int64(int32(start)) == start && int64(int32(stop)) == stop {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.RangeVal(int32(start), int32(stop))
							ip++
							continue
						}
					}
				}
				break innerLoop

			case REG_FOR_ITER_RANGE:
				ra := uint8(inst >> 40)
				it := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
				if it.IsRange() {
					cur := it.RangeStart()
					stop := it.RangeStop()
					if cur < stop {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16)) = val.RangeVal(cur+1, stop)
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(int64(cur))
						ip++
						continue
					}
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16)) = val.Value{}
					ip = int(uint32(inst))
					continue
				}
				break innerLoop

			case REG_UNPACK_SEQUENCE:
				rd := uint8(inst >> 48)
				seq := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				count := int(uint8(inst >> 32))
				if seq.IsTuple() {
					items := seq.AsTuple().Items
					if len(items) == count {
						for i := range count {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(int(rd)+i)*16)) = items[count-1-i]
						}
						ip++
						continue
					}
				} else if seq.IsList() {
					items := seq.AsList().Items
					if len(items) == count {
						for i := range count {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(int(rd)+i)*16)) = items[count-1-i]
						}
						ip++
						continue
					}
				}
				break innerLoop

			case REG_LIST_APPEND:
				// Strictly comprehension-private: OP_LIST_APPEND only targets anonymous newly-created lists.
				lst := (*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)).AsList()
				item := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				ihdr := item.HeaderPtr()
				if len(lst.Items) > 0 && len(lst.Items) < cap(lst.Items) && (ihdr == nil || ((ihdr.Flags&val.FlagEphemeralAll) == 0 && ihdr.Kind != val.KindMemoryView && (ihdr.Kind != val.KindString || (*val.PyString)(unsafe.Pointer(ihdr)).Buf == nil))) {
					n := len(lst.Items)
					lst.Items = lst.Items[:n+1]
					lst.Items[n] = item
					ip++
					continue
				}
				break innerLoop

			case REG_MAP_ADD, REG_SET_ADD:
				break innerLoop

			case REG_CALL_POS:
				ra := uint8(inst >> 40)
				argc := int(uint8(inst >> 32))
				target := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
				if argc == 1 && target == vm.builtinLen {
					arg := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra+1)*16))
					var n int
					lenHit := true
					switch {
					case arg.IsList() && arg.AsList().Header.Type == val.ListType:
						n = len(arg.AsList().Items)
					case arg.IsTuple() && arg.AsTuple().Header.Type == val.TupleType:
						n = len(arg.AsTuple().Items)
					case val.IsInlineStrASCII(arg):
						n = int(arg.RawNum() & 0xFF)
					default:
						lenHit = false
					}
					if lenHit {
						*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.Int64Val(int64(n))
						ip++
						continue
					}
				}
				if argc == 1 && target == vm.builtinRange {
					arg0 := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra+1)*16))
					if arg0.IsInt64() {
						stop := arg0.AsInt64()
						if stop >= 0 && stop <= 0x7FFFFFFF {
							*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = val.RangeVal(0, int32(stop))
							ip++
							continue
						}
					}
				}
				if target.IsBuiltinFunc() && !vm.hasTrackedViews {
					if bf := target.AsBuiltinFunc(); bf.Fn != nil {
						nativeFn = bf.Fn
						nativeSelf = target
						nativeArgReg = ra + 1
						nativeArgc = argc
						nativeDstReg = uint8(inst >> 48)
						slowKind = regSlowCallBuiltin
						break innerLoop
					}
				}
				icIdx := uint32(inst)
				if int(icIdx) < len(st.cache.regIC) && !vm.hasTrackedViews {
					entry := &st.cache.regIC[icIdx]
					if target == entry.CachedVal && entry.CalleeCache != nil {
						cc := entry.CalleeCache
						if cc.owner == vm && cc.co != nil {
							fn := target.AsFunction()
							if fn.Code.HeaderPtr() == &cc.co.Header && len(fn.Defaults) == 0 && fn.KwDefaults == nil &&
								cc.co.SimplePositionalOnly && argc == cc.co.PositionalParamCount && !cc.co.HasCellVars {
								newBase := st.base + int(ra) + 1
								st.periodicTick++
								if calleeRC := cc.regCode; calleeRC != nil {
									if st.frameIdx+1 < 1000 &&
										st.frameIdx+2 <= len(vm.frames) &&
										newBase+max(256, int(calleeRC.maxRegs)+64) <= len(st.stack) {
										pushFn = fn
										pushCC = cc
										pushRC = calleeRC
										pushNewBase = newBase
										pushDstReg = int(uint8(inst >> 48))
										needSafepoint = (st.periodicTick & 0xFF) == 0
										slowKind = regSlowPushFrame
										break innerLoop
									}
								} else if st.frameIdx+1 < 1000 &&
									st.frameIdx+2 <= len(vm.frames) &&
									newBase+max(256, max(cc.co.EffectiveLocals, cc.co.NumLocals)+64) <= len(st.stack) {
									pushFn = fn
									pushCC = cc
									pushNewBase = newBase
									pushDstReg = int(uint8(inst >> 48))
									nativeArgc = argc
									slowKind = regSlowCallTier1
									break innerLoop
								}
							}
						}
					}
				}
				break innerLoop

			case REG_CALL_METHOD_PIC:
				ra := uint8(inst >> 40)
				argc := int(uint8(inst >> 32))
				receiver := *(*val.Value)(unsafe.Add(regsPtr, uintptr(ra)*16))
				icIdx := int(uint16(inst))
				if !vm.hasTrackedViews {
					if receiver.IsInstance() {
						if uint(icIdx) < uint(len(st.cache.regMethodPIC)) {
							instObj := receiver.AsInstance()
							if instShape := instObj.Shape; instShape != nil {
								instID := instShape.ID
								instVer := instObj.Header.Type.Version()
								pic := &st.cache.regMethodPIC[icIdx]
								var targetFn *val.PyFunction
								if e := &pic[0]; e.Shape != nil && e.Shape.ID == instID && instVer == e.TypeVersion && e.TargetFn != nil {
									targetFn = e.TargetFn
								} else if e := &pic[1]; e.Shape != nil && e.Shape.ID == instID && instVer == e.TypeVersion && e.TargetFn != nil {
									targetFn = e.TargetFn
								}
								if targetFn != nil {
									callee := AsCodeObject(targetFn.Code)
									totalArgs := argc + 1
									if !callee.IsGenerator && !callee.IsCoroutine && !callee.IsAsyncGenerator &&
										callee.SimplePositionalOnly && totalArgs == callee.PositionalParamCount &&
										!callee.HasCellVars && (len(callee.FreeVars) == 0 || targetFn.Closure != nil) {
										cc := st.cache
										if callee != st.curCO {
											cc = (*VMCodeCache)(atomic.LoadPointer(&callee.primary))
										}
										if cc != nil && cc.owner == vm && cc.regCode != nil {
											calleeRC := cc.regCode
											newBase := st.base + int(ra)
											st.periodicTick++
											if st.frameIdx+1 < 1000 &&
												st.frameIdx+2 <= len(vm.frames) &&
												newBase+max(256, int(calleeRC.maxRegs)+64) <= len(st.stack) {
												pushFn = targetFn
												pushCC = cc
												pushRC = calleeRC
												pushNewBase = newBase
												pushDstReg = int(uint8(inst >> 48))
												needSafepoint = (st.periodicTick & 0xFF) == 0
												slowKind = regSlowPushFrame
												break innerLoop
											}
										}
									}
								}
							}
						}
					} else if uint(icIdx) < uint(len(st.cache.regIC)) {
						entry := &st.cache.regIC[icIdx]
						if entry.Kind == ICKindMethodDescr && entry.NativeFn != nil && entry.ExpectedShapeID == 0 {
							if hdr := receiver.HeaderPtr(); hdr != nil &&
								hdr.Kind != val.KindSuper && hdr.Kind != val.KindModule && hdr.Kind != val.KindType {
								tp := hdr.Type
								if tp == nil && hdr.Kind == val.KindInlineStr {
									tp = val.StrType
								}
								if tp != nil && entry.ExpectedAuxVer == uint64(uintptr(unsafe.Pointer(tp))) && tp.Version() == entry.ExpectedTypeVer {
									nativeFn = entry.NativeFn
									nativeSelf = entry.CachedVal
									nativeArgReg = ra
									nativeArgc = argc + 1
									nativeDstReg = uint8(inst >> 48)
									slowKind = regSlowMethodNative
									break innerLoop
								}
							}
						} else if entry.Kind == ICKindModuleAttr && entry.NativeFn != nil && receiver.IsModule() {
							mod := receiver.AsModule()
							if mod.Header.Type == val.ModuleType && mod.Dict != nil &&
								entry.ExpectedAuxVer == uint64(uintptr(unsafe.Pointer(mod))) &&
								entry.ExpectedTypeVer == atomic.LoadUint64(&mod.Dict.VersionTag) {
								nativeFn = entry.NativeFn
								nativeSelf = entry.CachedVal
								nativeArgReg = ra + 1
								nativeArgc = argc
								nativeDstReg = uint8(inst >> 48)
								slowKind = regSlowCallBuiltin
								break innerLoop
							}
						}
					}
				}
				break innerLoop

			case REG_RETURN:
				retVal = *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
				slowKind = regSlowReturn
				break innerLoop

			case REG_COLD:
				break innerLoop
			}
			continue
		}

		// Outer slow/call loop: executes Go calls, frame transitions, and cold paths.
		switch slowKind {
		case regSlowPushFrame:
			if needSafepoint {
				vm.checkPeriodicSafepoint(st.frameIdx + 1)
			}
			ip, rc = vm.pushRegFrameFast(st, rc, pushFn, pushCC, pushRC, pushNewBase, pushDstReg, ip)
			codePtr = rc.codePtr
			regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
			constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
			continue

		case regSlowMethodNative, regSlowCallBuiltin:
			argStart := st.base + int(nativeArgReg)
			vm.execCtx.SP = st.base + int(rc.maxRegs)
			res := vm.invokeBuiltinSafe(nativeFn, nativeSelf, st.stack[argStart:argStart+nativeArgc], nil)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				goto doRaise
			}
			*(*val.Value)(unsafe.Add(regsPtr, uintptr(nativeDstReg)*16)) = res
			ip++
			continue

		case regSlowCallTier1:
			res, raised := vm.callTier1SimpleFromRegWithCache(st, rc, pushFn, pushCC, pushNewBase, nativeArgc, ip)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
			if raised {
				goto doRaise
			}
			*(*val.Value)(unsafe.Add(regsPtr, uintptr(pushDstReg)*16)) = res
			ip++
			continue

		case regSlowReturn:
			if int(rc.maxRegs) > int(rc.numLocals) {
				dead := st.stack[st.base+int(rc.numLocals) : st.base+int(rc.maxRegs)]
				if rc.hasIter {
					vm.releaseDeadEphemeralIters(dead)
				}
				clear(dead)
			}
			if retVal.IsString() || retVal.Kind() == val.KindMemoryView {
				vm.prepareReturnValue(retVal)
			}
			if st.frameIdx > entryFrameIdx && (vm.frames[st.frameIdx].flags&frameFlagHasShadow) == 0 {
				caller := &vm.frames[st.frameIdx-1]
				if (caller.flags&frameFlagReg) != 0 && (caller.flags&frameFlagSlowReturn) == 0 &&
					!st.curCO.VarKWSafe && st.cells == nil &&
					len(vm.excStack) <= int(caller.excStackLen) && caller.cache != nil && caller.cache.regCode != nil {
					if (caller.flags & frameFlagInit) != 0 {
						if !retVal.IsNone() {
							goto regReturnSlowPath
						}
						retVal = caller.initInstance
						caller.flags &^= frameFlagInit
						caller.initInstance = val.Value{}
					}
					st.frameIdx--
					if (caller.flags & frameFlagSameGlobals) == 0 {
						st.globals = caller.globals
					}
					cc := caller.cache
					rc = cc.regCode
					st.cache = cc
					st.curCO = cc.co
					st.regCode = rc
					st.codePtr = cc.codePtr
					st.codeLen = cc.codeLen
					st.consts = cc.co.Consts
					st.base = caller.baseOffset
					vm.activeCO = cc.co
					vm.activeBaseOffset = st.base
					vm.activeFrameIdx = st.frameIdx
					vm.execCtx.SP = st.base + int(rc.maxRegs)
					ip = caller.ip
					codePtr = rc.codePtr
					regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
					constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
					*(*val.Value)(unsafe.Add(regsPtr, uintptr(caller.retSP)*16)) = retVal
					continue
				}
			}
		regReturnSlowPath:
			{
				var nextRC *RegCode
				var ctl loopCtl
				var done bool
				ip, nextRC, ctl, retVal, done = vm.regReturnSlow(st, rc, ip, entryFrameIdx, retVal)
				if done {
					return retVal, false, 0, 0, ctlNext
				}
				if ctl == ctlRaise {
					goto doRaise
				}
				rc = nextRC
				st.regCode = rc
				codePtr = rc.codePtr
				regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
				constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
				continue
			}

		case regSlowSafepoint:
			vm.checkPeriodicSafepoint(st.frameIdx)
			continue

		case regSlowForIterExhaust:
			inst := *(*RegInst)(unsafe.Add(codePtr, uintptr(ip)*8))
			vm.releaseForIter(exhaustIter)
			*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16)) = val.Value{}
			ip = int(uint32(inst))
			continue

		case regSlowDictGet:
			inst := *(*RegInst)(unsafe.Add(codePtr, uintptr(ip)*8))
			obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
			key := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
			if v, ok := obj.AsDict().Get(key); ok {
				*(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16)) = v
				ip++
				continue
			}

		case regSlowDictSet:
			inst := *(*RegInst)(unsafe.Add(codePtr, uintptr(ip)*8))
			v := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>48))*16))
			obj := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
			key := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>32))*16))
			obj.AsDict().Set(key, v)
			ip++
			continue

		case regSlowStoreGlobal:
			inst := *(*RegInst)(unsafe.Add(codePtr, uintptr(ip)*8))
			v := *(*val.Value)(unsafe.Add(regsPtr, uintptr(uint8(inst>>40))*16))
			vhdr := v.HeaderPtr()
			if (st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil) &&
				(vhdr == nil || ((vhdr.Flags&val.FlagEphemeralAll) == 0 && vhdr.Kind != val.KindMemoryView && (vhdr.Kind != val.KindString || (*val.PyString)(unsafe.Pointer(vhdr)).Buf == nil))) {
				aux := uint32(inst)
				name := *(*val.Value)(unsafe.Add(constsPtr, uintptr(aux>>16)*16))
				icIdx := int(aux & 0xFFFF)
				slotHint := -1
				if st.cache.regGlobals == st.globals && icIdx < len(st.cache.regIC) {
					slotHint = int(st.cache.regIC[icIdx].SlotIndex)
				}
				newSlot := st.globals.SetWithSlotHint(name, v, slotHint)
				if newSlot >= 0 {
					ic := st.cache.ensureRegIC(rc.numIC)
					if st.cache.regGlobals != st.globals {
						clear(ic)
						st.cache.regGlobals = st.globals
					}
					ic[icIdx].SlotIndex = int32(newSlot)
				}
				ip++
				continue
			}
		}

		// Default / fallback: dispatchRegCold.
		{
			inst := *(*RegInst)(unsafe.Add(codePtr, uintptr(ip)*8))
			var nextRC *RegCode
			var ctl loopCtl
			ip, nextRC, ctl = vm.dispatchRegCold(st, rc, inst, ip, entryFrameIdx)
			switch ctl {
			case ctlNext:
				continue
			case ctlReload:
				rc = nextRC
				st.regCode = rc
				codePtr = rc.codePtr
				regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
				constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
				continue
			case ctlRaise:
				goto doRaise
			case ctlExit:
				return st.result, false, 0, 0, ctlExit
			}
		}
		continue

	doRaise:
		{
			var nextRC *RegCode
			var raisedOut bool
			var outIP, outSP int
			var outCtl loopCtl
			ip, nextRC, raisedOut, outIP, outSP, outCtl = vm.unwindReg(st, rc, ip, entryFrameIdx)
			if raisedOut {
				return val.NoneVal(), true, outIP, outSP, outCtl
			}
			rc = nextRC
			st.regCode = rc
			codePtr = rc.codePtr
			regsPtr = unsafe.Add(unsafe.Pointer(unsafe.SliceData(st.stack)), uintptr(st.base)*16)
			constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
			continue
		}
	}
}

// pushRegFrameFast switches directly from a Reg caller to a Reg callee in-loop.
//
//go:noinline
func (vm *VM) pushRegFrameFast(
	st *loopState,
	callerRC *RegCode,
	fn *val.PyFunction,
	cc *VMCodeCache,
	calleeRC *RegCode,
	newBase, dstReg, callRegIP int,
) (int, *RegCode) {
	callee := cc.co
	frameIdx := st.frameIdx
	saved := &vm.frames[frameIdx]
	saved.cache = st.cache
	saved.ip = callRegIP + 1
	saved.baseOffset = st.base
	saved.retSP = dstReg
	saved.excStackLen = int32(len(vm.excStack))
	flags := (saved.flags & frameFlagHasShadow) | frameFlagReg
	if st.stackIsVM {
		flags |= frameFlagStackIsVM
	}
	if st.cells != nil || st.cellPtrs != nil || !st.curLocals.IsNone() {
		vm.saveFrameAux(st, frameIdx)
		flags |= frameFlagHasAux
	}
	if fn.Globals == nil || fn.Globals == st.globals {
		flags |= frameFlagSameGlobals
	} else {
		saved.globals = st.globals
		st.globals = fn.Globals
	}
	saved.flags = flags

	nextFrame := frameIdx + 1
	st.frameIdx = nextFrame
	if nextFrame > vm.maxFrameIdx {
		vm.maxFrameIdx = nextFrame
	}
	if st.stackIsVM {
		if hw := newBase + max(256, int(calleeRC.maxRegs)+64); hw > vm.maxSP {
			vm.maxSP = hw
		}
	}
	// Initialize non-parameter locals and temp registers to Unbound/zero for UnboundLocalError and GC safety.
	if callee.NumParams < int(calleeRC.numLocals) {
		for i := callee.NumParams; i < int(calleeRC.numLocals); i++ {
			st.stack[newBase+i] = val.UnboundVal()
		}
	}
	if int(calleeRC.maxRegs) > int(calleeRC.numLocals) {
		clear(st.stack[newBase+int(calleeRC.numLocals) : newBase+int(calleeRC.maxRegs)])
	}
	if calleeRC.numIC > 0 && len(cc.regIC) < calleeRC.numIC {
		cc.ensureRegIC(calleeRC.numIC)
	}

	st.curCO = callee
	st.cache = cc
	st.regCode = calleeRC
	st.codePtr = cc.codePtr
	st.codeLen = cc.codeLen
	st.consts = callee.Consts
	st.base = newBase
	vm.activeCO = callee
	vm.activeBaseOffset = newBase
	vm.activeFrameIdx = nextFrame
	vm.execCtx.SP = newBase + int(calleeRC.maxRegs)
	if callee.HasCells || st.cells != nil || st.cellPtrs != nil {
		vm.switchCalleeCells(st, callee, fn)
	}
	return 0, calleeRC
}

// regReturnSlow handles non-fast returns from a RegCode frame (top-of-reg-entry exit,
// __init__ return checks, shadow frame detach, VarKWSafe dict release, or aux restoration).
//
//go:noinline
func (vm *VM) regReturnSlow(st *loopState, rc *RegCode, ip, entryFrameIdx int, retVal val.Value) (int, *RegCode, loopCtl, val.Value, bool) {
	if st.curCO != nil && st.curCO.VarKWSafe && (vm.frames[st.frameIdx].flags&frameFlagHasShadow) == 0 {
		idx := st.base + st.curCO.VarKWIdx
		if idx >= 0 && idx < len(st.stack) {
			vm.releaseFrameSmallDictVal(st.stack[idx])
		}
	}
	if (vm.frames[st.frameIdx].flags & frameFlagHasShadow) != 0 {
		vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
		if st.frameIdx < len(vm.frameAux) {
			if curShadow := vm.frameAux[st.frameIdx].shadowFrame; curShadow != nil {
				curShadow.Detach()
				vm.frameAux[st.frameIdx].shadowFrame = nil
			}
		}
	}
	if st.frameIdx == entryFrameIdx {
		return 0, rc, ctlNext, retVal, true
	}
	st.frameIdx--
	callerIdx := st.frameIdx
	caller := &vm.frames[callerIdx]
	if (caller.flags & frameFlagInit) != 0 {
		inst := caller.initInstance
		caller.flags &^= frameFlagInit
		caller.initInstance = val.Value{}
		if !retVal.IsNone() {
			st.frameIdx++
			st.raiseExc = val.NewTypeError("__init__() should return None, not '" + retVal.Type().Name + "'")
			return ip, rc, ctlRaise, val.Value{}, false
		}
		retVal = inst
	}
	if len(vm.excStack) > int(caller.excStackLen) {
		vm.excStack = vm.excStack[:caller.excStackLen]
	}
	vm.restoreNormalCallerFrame(st, callerIdx, caller)
	callerRC := caller.cache.regCode
	st.regCode = callerRC
	st.consts = caller.cache.co.Consts
	vm.execCtx.SP = st.base + int(callerRC.maxRegs)
	st.stack[st.base+caller.retSP] = retVal
	return caller.ip, callerRC, ctlReload, val.Value{}, false
}

// unwindReg unwinds exceptions through RegCode frames, matching RegExceptionTable entries
// and propagating seamlessly to Tier-1 callers when unwinding past entryFrameIdx.
//
//go:noinline
func (vm *VM) unwindReg(st *loopState, rc *RegCode, faultRegIP, entryFrameIdx int) (int, *RegCode, bool, int, int, loopCtl) {
	exc := st.raiseExc
	st.raiseExc = val.Value{}

	if vm.deferredTBExc.HeaderPtr() != nil && vm.deferredTBExc != exc {
		vm.materializeDeferredTB(vm.deferredTBExc)
	}
	deferTB := exc.IsInstance() && !val.MultiThreadedFlag.Load()
	if deferTB && vm.deferredTBExc != exc {
		vm.deferredTBExc = exc
		vm.deferredTBBound = false
		clear(vm.deferredTBs)
		vm.deferredTBs = vm.deferredTBs[:0]
	}

	curTB := val.NoneVal()
	if exc.IsInstance() {
		curTB, _ = exc.AsInstance().GetAttr(nameTraceback)
	}

	curIP := faultRegIP
	curRC := rc

	for {
		co := st.curCO
		if curRC != nil && len(curRC.excTable) > 0 {
			var matched *RegExceptionEntry
			minSpan := ^uint32(0)
			uip := uint32(curIP)
			for i := range curRC.excTable {
				e := &curRC.excTable[i]
				if uip >= e.StartRegIP && uip < e.EndRegIP {
					span := e.EndRegIP - e.StartRegIP
					if span < minSpan {
						minSpan = span
						matched = e
					}
				}
			}
			if matched != nil {
				t1PC := curRC.tier1PC(curIP)
				if matched.PushLasti && matched.TargetReg > 0 {
					st.stack[st.base+int(matched.TargetReg)-1] = val.Int64Val(int64(t1PC))
				}
				st.stack[st.base+int(matched.TargetReg)] = exc
				// Clear dead temporary registers above TargetReg for GC safety.
				clearFrom := int(matched.TargetReg) + 1
				if clearFrom < int(curRC.maxRegs) {
					dead := st.stack[st.base+clearFrom : st.base+int(curRC.maxRegs)]
					vm.releaseDeadEphemeralIters(dead)
					clear(dead)
				}
				vm.activeCO = co
				vm.activeBaseOffset = st.base
				vm.activeCells = st.cells
				vm.activeCellPtrs = st.cellPtrs
				vm.activeFrameIdx = st.frameIdx
				vm.execCtx.SP = st.base + int(curRC.maxRegs)
				vm.frames[st.frameIdx].initInstance = val.Value{}
				vm.frames[st.frameIdx].flags &^= frameFlagInit | frameFlagCmp
				return int(matched.TargetRegIP), curRC, false, 0, 0, ctlReload
			}
		}

		// Record traceback entry for this Reg frame before popping.
		if co != nil {
			t1PC := curRC.tier1PC(curIP)
			if deferTB {
				vm.deferredTBs = append(vm.deferredTBs, deferredTBEntry{
					co:      co,
					ip:      t1PC,
					globals: st.globals,
					cells:   st.cells,
				})
			} else {
				fSlot := &vm.frames[st.frameIdx]
				fSlot.cache = st.cache
				fSlot.ip = t1PC
				fSlot.baseOffset = st.base
				fSlot.globals = st.globals
				fSlot.flags &^= frameFlagSameGlobals | frameFlagReg
				line := co.LineAt(t1PC)
				frameVal := vm.materializeFrame(st.frameIdx)
				curTB = val.NewTracebackVal(frameVal, line, t1PC, curTB)
				if exc.IsInstance() {
					exc.AsInstance().SetAttr(nameTraceback, curTB)
				}
			}
		}

		// Cleanup dead ephemeral iters, temp registers, VarKWSafe dict, and shadow frame.
		if curRC != nil && int(curRC.maxRegs) > int(curRC.numLocals) {
			dead := st.stack[st.base+int(curRC.numLocals) : st.base+int(curRC.maxRegs)]
			vm.releaseDeadEphemeralIters(dead)
			clear(dead)
		}
		if co != nil && co.VarKWSafe && (vm.frames[st.frameIdx].flags&frameFlagHasShadow) == 0 {
			idx := st.base + co.VarKWIdx
			if idx >= 0 && idx < len(st.stack) {
				vm.releaseFrameSmallDictVal(st.stack[idx])
			}
		}
		if (vm.frames[st.frameIdx].flags & frameFlagHasShadow) != 0 {
			vm.frames[st.frameIdx].flags &^= frameFlagHasShadow
			if st.frameIdx < len(vm.frameAux) {
				if curShadow := vm.frameAux[st.frameIdx].shadowFrame; curShadow != nil {
					curShadow.Detach()
					vm.frameAux[st.frameIdx].shadowFrame = nil
				}
			}
		}

		if st.frameIdx > entryFrameIdx {
			st.frameIdx--
			callerIdx := st.frameIdx
			caller := &vm.frames[callerIdx]
			if (caller.flags & frameFlagInit) != 0 {
				caller.flags &^= frameFlagInit
				caller.initInstance = val.Value{}
			}
			if len(vm.excStack) > int(caller.excStackLen) {
				vm.excStack = vm.excStack[:caller.excStackLen]
			}
			vm.restoreNormalCallerFrame(st, callerIdx, caller)
			curRC = caller.cache.regCode
			st.regCode = curRC
			st.consts = caller.cache.co.Consts
			curIP = max(caller.ip-1, 0)
			continue
		}

		// Unwinding past entryFrameIdx.
		if entryFrameIdx == st.startFrameIdx {
			if deferTB {
				vm.materializeDeferredTB(exc)
			}
			vm.unhandledException = exc
			val.ClearEphemeral(exc)
			vm.lastException = exc
			st.result = val.NoneVal()
			return 0, curRC, true, 0, 0, ctlExit
		}

		// Hand off to Tier-1 caller at entryFrameIdx - 1.
		st.frameIdx--
		callerIdx := st.frameIdx
		caller := &vm.frames[callerIdx]
		if (caller.flags & frameFlagInit) != 0 {
			caller.flags &^= frameFlagInit
			caller.initInstance = val.Value{}
		}
		if (caller.flags & frameFlagCmp) != 0 {
			caller.flags &^= frameFlagCmp
			if callerIdx < len(vm.frameAux) {
				vm.frameAux[callerIdx].cmpLeft = val.Value{}
				vm.frameAux[callerIdx].cmpRight = val.Value{}
			}
		}
		if len(vm.excStack) > int(caller.excStackLen) {
			vm.excStack = vm.excStack[:caller.excStackLen]
		}
		callerIP := max(caller.ip-1, 0)
		vm.restoreNormalCallerFrame(st, callerIdx, caller)
		st.regCode = nil
		// Record the Tier-1 caller frame in TB (matches Tier-1 unwind's caller-record before handler check).
		if callerCO := st.curCO; callerCO != nil {
			if deferTB {
				vm.deferredTBs = append(vm.deferredTBs, deferredTBEntry{
					co:      callerCO,
					ip:      callerIP,
					globals: st.globals,
					cells:   st.cells,
				})
			} else {
				line := callerCO.LineAt(callerIP)
				frameVal := vm.materializeFrame(callerIdx)
				curTB = val.NewTracebackVal(frameVal, line, callerIP, curTB)
				if exc.IsInstance() {
					exc.AsInstance().SetAttr(nameTraceback, curTB)
				}
			}
		}
		st.raiseExc = exc
		return 0, curRC, true, callerIP, caller.retSP, ctlRaise
	}
}

// dispatchRegCold handles all REG_COLD sub-opcodes and slow-path misses from VM.runRegLoop.
//
//go:noinline
func (vm *VM) dispatchRegCold(st *loopState, rc *RegCode, inst RegInst, ip, entryFrameIdx int) (int, *RegCode, loopCtl) {
	op := decodeRegOp(inst)
	rd := decodeRegD(inst)
	ra := decodeRegA(inst)
	rb := decodeRegB(inst)
	aux := decodeRegAux(inst)
	base := st.base
	st.ip = ip

	// Helper to run a Tier-1 stack helper on a temporary scratch window above maxRegs.
	scratchBase := base + int(rc.maxRegs)
	if scratchBase+64 > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, scratchBase+128)
	}

	switch op {
	case REG_LOAD_GLOBAL:
		constIdx := aux >> 16
		icIdx := int(aux & 0xFFFF)
		name := st.consts[constIdx]
		slotHint := -1
		if (st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil) &&
			st.cache.regGlobals == st.globals && icIdx < len(st.cache.regIC) {
			slotHint = int(st.cache.regIC[icIdx].SlotIndex)
		}
		if v, slot, ok := st.globals.GetWithSlotHint(name, slotHint); ok {
			if v.IsLazyImport() {
				vm.execCtx.SP = scratchBase
				resolved, err := vm.resolveLazyImport(nil, name, v)
				st.stack = st.syncStackFromExecCtx(vm, st.stack)
				if err != nil {
					st.raiseExc = val.NewException(val.ImportErrorType, err.Error())
					return ip, rc, ctlRaise
				}
				st.globals.Set(name, resolved)
				v = resolved
			}
			if st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil {
				ic := st.cache.ensureRegIC(rc.numIC)
				if st.cache.regGlobals != st.globals {
					clear(ic)
					st.cache.regGlobals = st.globals
				}
				ic[icIdx] = ICSlot{
					CachedVal:       v,
					ExpectedTypeVer: atomic.LoadUint64(&st.globals.VersionTag),
					SlotIndex:       int32(slot),
					Kind:            ICKindGlobal,
				}
			}
			st.stack[base+int(rd)] = v
			return ip + 1, rc, ctlReload
		}
		if v, ok := vm.builtins.Get(name); ok {
			if st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil {
				ic := st.cache.ensureRegIC(rc.numIC)
				if st.cache.regGlobals != st.globals {
					clear(ic)
					st.cache.regGlobals = st.globals
				}
				ic[icIdx] = ICSlot{
					CachedVal:       v,
					ExpectedTypeVer: atomic.LoadUint64(&st.globals.VersionTag),
					ExpectedAuxVer:  atomic.LoadUint64(&vm.builtins.VersionTag),
					Kind:            ICKindBuiltin,
				}
			}
			st.stack[base+int(rd)] = v
			return ip + 1, rc, ctlReload
		}
		if st.globals.Header.Type != val.DictType && st.globals.Header.Type != nil {
			vm.execCtx.SP = scratchBase
			v := vm.getItemSafe(val.ObjectVal(&st.globals.Header), name)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				if exc.IsInstance() && val.IsSubclass(exc.Type(), val.KeyErrorType) {
					if bv, ok := vm.builtins.Get(name); ok {
						st.stack[base+int(rd)] = bv
						return ip + 1, rc, ctlReload
					}
					st.raiseExc = val.NewNameError("name '" + name.AsString() + "' is not defined")
				} else {
					st.raiseExc = exc
				}
				return ip, rc, ctlRaise
			}
			st.stack[base+int(rd)] = v
			return ip + 1, rc, ctlReload
		}
		st.raiseExc = val.NewNameError("name '" + name.AsString() + "' is not defined")
		return ip, rc, ctlRaise

	case REG_STORE_GLOBAL:
		constIdx := aux >> 16
		icIdx := int(aux & 0xFFFF)
		v := st.stack[base+int(ra)]
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		markViewEscaped(v)
		name := st.consts[constIdx]
		if st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil {
			slotHint := -1
			if st.cache.regGlobals == st.globals && icIdx < len(st.cache.regIC) {
				slotHint = int(st.cache.regIC[icIdx].SlotIndex)
			}
			newSlot := st.globals.SetWithSlotHint(name, v, slotHint)
			if newSlot >= 0 {
				ic := st.cache.ensureRegIC(rc.numIC)
				if st.cache.regGlobals != st.globals {
					clear(ic)
					st.cache.regGlobals = st.globals
				}
				ic[icIdx].SlotIndex = int32(newSlot)
			}
		} else {
			vm.execCtx.SP = scratchBase
			vm.setItemSafe(val.ObjectVal(&st.globals.Header), name, v)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
		}
		return ip + 1, rc, ctlReload

	case REG_LOAD_DEREF:
		if int(aux) < len(st.cells) && st.cells[aux].IsCell() {
			v := st.cells[aux].AsCell().Value
			if !v.IsUnbound() {
				st.stack[base+int(rd)] = v
				return ip + 1, rc, ctlNext
			}
		}
		st.raiseExc = val.NewException(val.UnboundLocalErrorType, "cannot access free variable where it is not associated with a value")
		return ip, rc, ctlRaise

	case REG_ADD, REG_SUB, REG_MUL, REG_MOD, REG_FLOOR_DIV, REG_BIT_AND, REG_BIT_OR, REG_BIT_XOR:
		var t1Op Opcode
		switch op {
		case REG_ADD:
			t1Op = OP_BINARY_ADD
		case REG_SUB:
			t1Op = OP_BINARY_SUB
		case REG_MUL:
			t1Op = OP_BINARY_MUL
		case REG_MOD:
			t1Op = OP_BINARY_MOD
		case REG_FLOOR_DIV:
			t1Op = OP_BINARY_FLOOR_DIV
		case REG_BIT_AND:
			t1Op = OP_BINARY_AND
		case REG_BIT_OR:
			t1Op = OP_BINARY_OR
		default:
			t1Op = OP_BINARY_XOR
		}
		return vm.regBinarySlow(st, rc, t1Op, rd, ra, rb, ip, scratchBase, aux != 0)

	case REG_CMP_LT, REG_CMP_EQ, REG_CMP_OP:
		cmpOp := uint8(aux)
		switch op {
		case REG_CMP_LT:
			cmpOp = CMP_LT
		case REG_CMP_EQ:
			cmpOp = CMP_EQ
		}
		a := st.stack[base+int(ra)]
		b := st.stack[base+int(rb)]
		vm.execCtx.SP = scratchBase
		res := vm.compareOpSafe(a, b, cmpOp)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = res
		return ip + 1, rc, ctlReload

	case REG_JIF, REG_JIT:
		cond := st.stack[base+int(ra)]
		truthy, raised := vm.evalInstanceTruthy(st, cond, ip, scratchBase)
		if raised {
			return ip, rc, ctlRaise
		}
		if (op == REG_JIF && !truthy) || (op == REG_JIT && truthy) {
			return int(aux), rc, ctlReload
		}
		return ip + 1, rc, ctlReload

	case REG_LOAD_ATTR:
		constIdx := aux >> 16
		icIdx := int(aux & 0xFFFF)
		obj := st.stack[base+int(ra)]
		attrName := st.consts[constIdx]
		return vm.regLoadAttrSlow(st, rc, obj, attrName, rd, icIdx, ip, scratchBase)

	case REG_STORE_ATTR:
		constIdx := aux >> 16
		icIdx := int(aux & 0xFFFF)
		obj := st.stack[base+int(ra)]
		v := st.stack[base+int(rb)]
		return vm.regStoreAttrSlow(st, rc, obj, attrNameFor(st.consts, constIdx), v, icIdx, ip, scratchBase)

	case REG_SUBSCR:
		obj := st.stack[base+int(ra)]
		key := st.stack[base+int(rb)]
		vm.execCtx.SP = scratchBase
		res := vm.getItemSafe(obj, key)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = res
		return ip + 1, rc, ctlReload

	case REG_STORE_SUBSCR:
		v := st.stack[base+int(rd)]
		obj := st.stack[base+int(ra)]
		key := st.stack[base+int(rb)]
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		markViewEscaped(v)
		vm.execCtx.SP = scratchBase
		vm.setItemSafe(obj, key, v)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		return ip + 1, rc, ctlReload

	case REG_GET_ITER:
		obj := st.stack[base+int(ra)]
		vm.execCtx.SP = scratchBase
		it := vm.getIterSafe(obj)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = it
		return ip + 1, rc, ctlReload

	case REG_FOR_ITER:
		it := st.stack[base+int(ra)]
		vm.execCtx.SP = scratchBase
		var item val.Value
		var status val.IterStatus
		if it.IsGenerator() {
			item, status = vm.resumeGenerator(&vm.execCtx, it.AsGenerator(), val.NoneVal())
		} else if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
			item, status = vm.iterNextSafe(tpIterNext, it)
		} else {
			st.raiseExc = val.NewTypeError("'" + it.Type().Name + "' object is not an iterator")
			return ip, rc, ctlRaise
		}
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		if status == val.IterExhausted {
			if it.IsGenerator() {
				vm.releaseGenerator(it.AsGenerator())
			} else if it.IsIter() && (it.HeaderPtr().Flags&val.FlagEphemeralIter) != 0 {
				vm.releaseForIter(it.AsIter())
			}
			st.stack[base+int(ra)] = val.Value{}
			return int(aux), rc, ctlReload
		}
		st.stack[base+int(rd)] = item
		return ip + 1, rc, ctlReload

	case REG_FOR_ITER_UNPACK2:
		return vm.regForIterUnpack2Slow(st, rc, rd, ra, rb, int(aux), ip, scratchBase)

	case REG_SETUP_RANGE:
		count := int(rb)
		switch count {
		case 1:
			stopVal := st.stack[base+int(ra)]
			if stopVal.IsInt64() && int64(int32(stopVal.AsInt64())) == stopVal.AsInt64() && stopVal.AsInt64() >= 0 {
				st.stack[base+int(rd)] = val.RangeVal(0, int32(stopVal.AsInt64()))
				return ip + 1, rc, ctlNext
			}
		case 2:
			startVal := st.stack[base+int(ra)]
			stopVal := st.stack[base+int(ra)+1]
			if startVal.IsInt64() && stopVal.IsInt64() &&
				int64(int32(startVal.AsInt64())) == startVal.AsInt64() &&
				int64(int32(stopVal.AsInt64())) == stopVal.AsInt64() {
				st.stack[base+int(rd)] = val.RangeVal(int32(startVal.AsInt64()), int32(stopVal.AsInt64()))
				return ip + 1, rc, ctlNext
			}
		}
		if scratchBase+count+16 > len(st.stack) {
			st.stack = vm.growStack(st, st.stack, scratchBase+count+64)
		}
		copy(st.stack[scratchBase:scratchBase+count], st.stack[base+int(ra):base+int(ra)+count])
		vm.execCtx.SP = scratchBase + count
		res := vm.callFromHookSafe(vm.builtinRange, scratchBase, count, nil)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = res
		return ip + 1, rc, ctlReload

	case REG_FOR_ITER_RANGE:
		it := st.stack[base+int(ra)]
		if it.IsRange() {
			cur := it.RangeStart()
			stop := it.RangeStop()
			if cur < stop {
				st.stack[base+int(ra)] = val.RangeVal(cur+1, stop)
				st.stack[base+int(rd)] = val.Int64Val(int64(cur))
				return ip + 1, rc, ctlNext
			}
			st.stack[base+int(ra)] = val.Value{}
			return int(aux), rc, ctlNext
		}
		vm.execCtx.SP = scratchBase
		var item val.Value
		var status val.IterStatus
		if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
			item, status = vm.iterNextSafe(tpIterNext, it)
		} else {
			st.raiseExc = val.NewTypeError("'" + it.Type().Name + "' object is not an iterator")
			return ip, rc, ctlRaise
		}
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		if status == val.IterExhausted {
			st.stack[base+int(ra)] = val.Value{}
			return int(aux), rc, ctlReload
		}
		st.stack[base+int(rd)] = item
		return ip + 1, rc, ctlReload

	case REG_UNPACK_SEQUENCE:
		count := int(rb)
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opUnpackSequence(st, Encode(OP_UNPACK_SEQUENCE, 0, uint16(aux)), ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		copy(st.stack[base+int(rd):base+int(rd)+count], st.stack[scratchBase:scratchBase+count])
		clear(st.stack[scratchBase : scratchBase+count])
		return ip + 1, rc, ctlReload

	case REG_LIST_APPEND:
		lst := st.stack[base+int(rd)].AsList()
		item := st.stack[base+int(ra)]
		val.ClearEphemeral(item)
		val.DetachStringBuf(item)
		markViewEscaped(item)
		if len(lst.Items) == 0 && rb != regUnused {
			if n, ok := val.ListLenHint(st.stack[base+int(rb)]); ok && n > 0 && n <= 1<<20 && n > cap(lst.Items) {
				lst.EnsureCap(n)
			}
		}
		lst.Append(item)
		return ip + 1, rc, ctlNext

	case REG_MAP_ADD:
		d := st.stack[base+int(rd)].AsDict()
		k := st.stack[base+int(ra)]
		v := st.stack[base+int(rb)]
		val.DetachStringBuf(k)
		val.DetachStringBuf(v)
		markViewEscaped(v)
		compKind := aux >> 8
		iterReg := uint8(aux & 0xFF)
		if compKind != 0 && d.Len() == 0 && iterReg != regUnused {
			if n, ok := val.ListLenHint(st.stack[base+int(iterReg)]); ok && n > 0 && n <= 1<<20 {
				if compKind == 2 {
					n = max(n/2, 1)
				}
				if n > 5 {
					d.Reserve(n)
				}
			}
		}
		vm.execCtx.SP = scratchBase
		vm.dictSetSafe(d, k, v)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		return ip + 1, rc, ctlReload

	case REG_SET_ADD:
		s := st.stack[base+int(rd)].AsSet()
		item := st.stack[base+int(ra)]
		val.ClearEphemeral(item)
		val.DetachStringBuf(item)
		markViewEscaped(item)
		if aux != 0 && s.Len() == 0 && rb != regUnused {
			if n, ok := val.ListLenHint(st.stack[base+int(rb)]); ok && n > 0 && n <= 1<<20 {
				if aux == 2 {
					n = max(n/2, 1)
				}
				if n > 0 {
					s.Reserve(n)
				}
			}
		}
		vm.execCtx.SP = scratchBase
		vm.regSetAddSafe(s, item)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		return ip + 1, rc, ctlReload

	case REG_CALL_POS:
		return vm.regCallPosSlow(st, rc, rd, ra, int(rb), int(aux), ip, scratchBase)

	case REG_CALL_METHOD_PIC:
		return vm.regCallMethodPICSlow(st, rc, rd, ra, int(rb), aux>>16, int(aux&0xFFFF), ip, scratchBase)

	case REG_COLD:
		return vm.dispatchRegColdSub(st, rc, rd, ra, rb, &rc.coldMeta[aux], ip, scratchBase)
	}
	return ip + 1, rc, ctlNext
}

func attrNameFor(consts []val.Value, idx uint32) val.Value {
	return consts[idx]
}

func (vm *VM) regSetAddSafe(s *val.PySet, item val.Value) {
	defer func() {
		if r := recover(); r != nil {
			vm.execCtx.Raise(panicToPyException(r))
		}
	}()
	s.Add(item)
}

//go:noinline
func (vm *VM) regForIterUnpack2Slow(st *loopState, rc *RegCode, rd, ra, rb uint8, targetIP, ip, scratchBase int) (int, *RegCode, loopCtl) {
	base := st.base
	it := st.stack[base+int(ra)]
	vm.execCtx.SP = scratchBase

	if it.IsIter() {
		pyIter := it.AsIter()
		switch pyIter.Kind {
		case val.IterKindEnumerate:
			var item val.Value
			var ok bool
			if pyIter.Obj.IsIter() {
				item, ok = pyIter.Obj.AsIter().NextCtx(&vm.execCtx)
			} else if pyIter.Obj.IsGenerator() {
				var status val.IterStatus
				item, status = vm.resumeGenerator(&vm.execCtx, pyIter.Obj.AsGenerator(), val.NoneVal())
				ok = status == val.IterYielded
			} else if tpNext := pyIter.Obj.Type().Slots.TpIterNext; tpNext != nil {
				var status val.IterStatus
				item, status = vm.iterNextSafe(tpNext, pyIter.Obj)
				ok = status == val.IterYielded
			}
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
			if !ok {
				vm.releaseForIter(pyIter)
				st.stack[base+int(ra)] = val.Value{}
				return targetIP, rc, ctlReload
			}
			idxVal := pyIter.NextEnumerateIndex()
			st.stack[base+int(rd)] = idxVal
			st.stack[base+int(rb)] = item
			return ip + 1, rc, ctlReload

		case val.IterKindZip:
			if len(pyIter.Seq) == 2 {
				if pyIter.Stop != 0 {
					vm.releaseForIter(pyIter)
					st.stack[base+int(ra)] = val.Value{}
					return targetIP, rc, ctlReload
				}
				v0, ok0 := pyIter.Seq[0].AsIter().NextCtx(&vm.execCtx)
				st.stack = st.syncStackFromExecCtx(vm, st.stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, rc, ctlRaise
				}
				if !ok0 {
					pyIter.Stop = 1
					if pyIter.Step != 0 {
						raiseZipStrict(&vm.execCtx, pyIter.Seq, 0)
						st.stack = st.syncStackFromExecCtx(vm, st.stack)
						if vm.execCtx.HasException() {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, rc, ctlRaise
						}
					}
					vm.releaseForIter(pyIter)
					st.stack[base+int(ra)] = val.Value{}
					return targetIP, rc, ctlReload
				}
				v1, ok1 := pyIter.Seq[1].AsIter().NextCtx(&vm.execCtx)
				st.stack = st.syncStackFromExecCtx(vm, st.stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, rc, ctlRaise
				}
				if !ok1 {
					pyIter.Stop = 1
					if pyIter.Step != 0 {
						raiseZipStrict(&vm.execCtx, pyIter.Seq, 1)
						st.stack = st.syncStackFromExecCtx(vm, st.stack)
						if vm.execCtx.HasException() {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, rc, ctlRaise
						}
					}
					vm.releaseForIter(pyIter)
					st.stack[base+int(ra)] = val.Value{}
					return targetIP, rc, ctlReload
				}
				st.stack[base+int(rd)] = v0
				st.stack[base+int(rb)] = v1
				return ip + 1, rc, ctlReload
			}

		case val.IterKindDictItems, val.IterKindDictRevItems:
			k, v, ok := pyIter.NextDictItemPairCtx(&vm.execCtx)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
			if !ok {
				vm.releaseForIter(pyIter)
				st.stack[base+int(ra)] = val.Value{}
				return targetIP, rc, ctlReload
			}
			st.stack[base+int(rd)] = k
			st.stack[base+int(rb)] = v
			return ip + 1, rc, ctlReload
		}
	}

	// Generic fallback: advance any iterator, then unpack 2 items into (rd, rb).
	var item val.Value
	var status val.IterStatus
	if it.IsGenerator() {
		item, status = vm.resumeGenerator(&vm.execCtx, it.AsGenerator(), val.NoneVal())
	} else if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
		item, status = vm.iterNextSafe(tpIterNext, it)
	} else {
		st.raiseExc = val.NewTypeError("'" + it.Type().Name + "' object is not an iterator")
		return ip, rc, ctlRaise
	}
	st.stack = st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, rc, ctlRaise
	}
	if status == val.IterExhausted {
		if it.IsGenerator() {
			vm.releaseGenerator(it.AsGenerator())
		} else if it.IsIter() && (it.HeaderPtr().Flags&val.FlagEphemeralIter) != 0 {
			vm.releaseForIter(it.AsIter())
		}
		st.stack[base+int(ra)] = val.Value{}
		return targetIP, rc, ctlReload
	}
	if item.IsTuple() {
		if items := item.AsTuple().Items; len(items) == 2 {
			st.stack[base+int(rd)] = items[0]
			st.stack[base+int(rb)] = items[1]
			return ip + 1, rc, ctlReload
		}
	} else if item.IsList() {
		if items := item.AsList().Items; len(items) == 2 {
			st.stack[base+int(rd)] = items[0]
			st.stack[base+int(rb)] = items[1]
			return ip + 1, rc, ctlReload
		}
	}
	st.stack[scratchBase] = item
	_, _, ctl := vm.opUnpackSequence(st, Encode(OP_UNPACK_SEQUENCE, 0, 2), ip, scratchBase+1)
	if ctl == ctlRaise {
		return ip, rc, ctlRaise
	}
	// opUnpackSequence places item[1] at scratchBase and item[0] at scratchBase+1.
	st.stack[base+int(rd)] = st.stack[scratchBase+1]
	st.stack[base+int(rb)] = st.stack[scratchBase]
	clear(st.stack[scratchBase : scratchBase+2])
	return ip + 1, rc, ctlReload
}

//go:noinline
func (vm *VM) regBinarySlow(st *loopState, rc *RegCode, t1Op Opcode, rd, ra, rb uint8, ip, scratchBase int, inPlace bool) (int, *RegCode, loopCtl) {
	base := st.base
	st.stack[scratchBase] = st.stack[base+int(ra)]
	st.stack[scratchBase+1] = st.stack[base+int(rb)]
	_, _, ctl := vm.opBinarySlowMode(st, t1Op, ip, scratchBase+2, inPlace)
	if ctl == ctlRaise {
		return ip, rc, ctlRaise
	}
	st.stack[base+int(rd)] = st.stack[scratchBase]
	st.stack[scratchBase] = val.Value{}
	st.stack[scratchBase+1] = val.Value{}
	return ip + 1, rc, ctlReload
}

//go:noinline
func (vm *VM) regLoadAttrSlow(st *loopState, rc *RegCode, obj, attrName val.Value, rd uint8, icIdx, ip, scratchBase int) (int, *RegCode, loopCtl) {
	base := st.base
	if instObj := obj.AsInstanceChecked(); instObj != nil && instObj.Shape != nil && instObj.Header.Type != nil {
		instType := instObj.Header.Type
		if (instType.Flags & val.FlagCustomGetAttribute) == 0 {
			ic := st.cache.ensureRegIC(rc.numIC)
			attrPIC := st.cache.ensureRegAttrPIC(rc.numIC)
			vm.execCtx.SP = scratchBase
			res := vm.loadAttrInstanceSlowSafe(instObj, obj, attrName, icIdx, scratchBase, st.frameIdx, ic, attrPIC)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
			st.stack[base+int(rd)] = res
			return ip + 1, rc, ctlReload
		}
	}
	if obj.IsTuple() {
		tp := obj.HeaderPtr().Type
		if tp != nil && (tp.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
			typeVer := tp.Version()
			ic := st.cache.ensureRegIC(rc.numIC)
			interned := val.InternValue(attrName)
			if descr, _, found := tp.LookupMRO(interned); found {
				if TupleGetterType != nil && descr.HeaderPtr() != nil && descr.HeaderPtr().Type == TupleGetterType {
					tg := (*pyTupleGetter)(unsafe.Pointer(descr.HeaderPtr()))
					t := obj.AsTuple()
					if tg.Index >= 0 && tg.Index < len(t.Items) {
						ic[icIdx] = ICSlot{
							Kind:            ICKindTupleGetter,
							ExpectedTypeVer: typeVer,
							SlotIndex:       int32(tg.Index),
						}
						st.stack[base+int(rd)] = t.Items[tg.Index]
						return ip + 1, rc, ctlReload
					}
				} else if descr.IsProperty() {
					prop := descr.AsProperty()
					if prop.FGet.IsFunction() || prop.FGet.IsBuiltinFunc() {
						cc := vm.cacheForPropertyFGet(prop.FGet)
						if cc != nil && cc.regCode == nil && cc.co != nil {
							cc.regCode = cc.co.regCode.Load()
						}
						ic[icIdx] = ICSlot{
							Kind:            ICKindProperty,
							ExpectedTypeVer: typeVer,
							CachedVal:       prop.FGet,
							CalleeCache:     cc,
						}
					}
				}
			}
		}
	}
	if obj.IsModule() && obj.AsModule().Header.Type == val.ModuleType {
		mod := obj.AsModule()
		interned := val.InternValue(attrName)
		if v, ok := mod.Dict.Get(interned); ok && !v.IsLazyImport() {
			ic := st.cache.ensureRegIC(rc.numIC)
			ic[icIdx] = ICSlot{
				CachedVal:       v,
				ExpectedTypeVer: atomic.LoadUint64(&mod.Dict.VersionTag),
				Kind:            ICKindModuleAttr,
			}
			st.stack[base+int(rd)] = v
			return ip + 1, rc, ctlReload
		}
	}
	vm.execCtx.SP = scratchBase
	res := vm.getAttrSafe(obj, attrName)
	st.stack = st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, rc, ctlRaise
	}
	st.stack[base+int(rd)] = res
	return ip + 1, rc, ctlReload
}

//go:noinline
func (vm *VM) regStoreAttrSlow(st *loopState, rc *RegCode, obj, attrName, v val.Value, icIdx, ip, scratchBase int) (int, *RegCode, loopCtl) {
	val.ClearEphemeral(v)
	val.DetachStringBuf(v)
	markViewEscaped(v)
	if instObj := obj.AsInstanceChecked(); instObj != nil && instObj.Shape != nil && instObj.Header.Type != nil {
		instType := instObj.Header.Type
		if (instType.Flags & val.FlagCustomSetAttr) == 0 {
			typeVer := instType.Version()
			// Fast transitionIC check out-of-line (kept out of runRegLoop).
			if !val.MultiThreadedFlag.Load() && uint(icIdx) < uint(len(st.cache.regTransitionIC)) {
				tc := &st.cache.regTransitionIC[icIdx]
				if tc.OldShape == instObj.Shape && tc.TypeVer == typeVer && tc.NewShape != nil {
					if instObj.ExtendBootstrapSlot(int(tc.SlotIdx), v) {
						instObj.Shape = tc.NewShape
						return ip + 1, rc, ctlReload
					}
				}
			}
			ic := st.cache.ensureRegIC(rc.numIC)
			attrPIC := st.cache.ensureRegAttrPIC(rc.numIC)
			attrName = val.InternValue(attrName)
			if attrName != nameDunderDict && attrName != nameClass {
				if descr, _, foundInClass := instType.LookupMRO(attrName); foundInClass {
					if descr.IsMemberDescr() {
						md := descr.AsMemberDescr()
						if md.SlotIndex >= 0 && md.SlotIndex < len(instObj.Slots) && instObj.SetSlot(md.SlotIndex, v) {
							ic[icIdx] = ICSlot{
								Kind:            ICKindSlotMember,
								ExpectedTypeVer: typeVer,
								SlotIndex:       int32(md.SlotIndex),
							}
							return ip + 1, rc, ctlReload
						}
					}
				} else if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
					instObj.SetSlot(slotIdx, v)
					insertAttrPIC(&attrPIC[icIdx], instObj.Shape, slotIdx, typeVer)
					ic[icIdx] = ICSlot{
						ExpectedShapeID: instObj.Shape.ID,
						Kind:            ICKindInstanceSlot,
						SlotIndex:       int32(slotIdx),
						ExpectedTypeVer: typeVer,
					}
					return ip + 1, rc, ctlReload
				} else if (instType.Flags & val.FlagForbidDynamicAttrs) == 0 {
					oldShape := instObj.Shape
					slotIdx := instObj.SetAttr(attrName, v)
					newShape := instObj.Shape
					insertAttrPIC(&attrPIC[icIdx], newShape, slotIdx, typeVer)
					if oldShape != nil && newShape != nil && newShape != oldShape {
						transIC := st.cache.ensureRegTransitionIC(rc.numIC)
						transIC[icIdx] = TransitionICEntry{
							OldShape: oldShape,
							NewShape: newShape,
							TypeVer:  typeVer,
							SlotIdx:  int32(slotIdx),
						}
					}
					ic[icIdx] = ICSlot{
						ExpectedShapeID: newShape.ID,
						Kind:            ICKindInstanceSlot,
						SlotIndex:       int32(slotIdx),
						ExpectedTypeVer: typeVer,
					}
					return ip + 1, rc, ctlReload
				}
			}
		}
	}
	vm.execCtx.SP = scratchBase
	vm.setAttrSafe(obj, attrName, v)
	st.stack = st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, rc, ctlRaise
	}
	return ip + 1, rc, ctlReload
}

//go:noinline
func (vm *VM) regCallPosSlow(st *loopState, rc *RegCode, rd, funcReg uint8, argc, icIdx, ip, scratchBase int) (int, *RegCode, loopCtl) {
	base := st.base
	target := st.stack[base+int(funcReg)]
	argStart := base + int(funcReg) + 1

	// Fast direct BuiltinFunc invocation (zero arg copy, zero callFromHook overhead).
	if target.IsBuiltinFunc() {
		bf := target.AsBuiltinFunc()
		if bf.Fn != nil {
			vm.execCtx.SP = scratchBase
			res := vm.invokeBuiltinSafe(bf.Fn, target, st.stack[argStart:argStart+argc], nil)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
			st.stack[base+int(rd)] = res
			return ip + 1, rc, ctlReload
		}
	}
	if target.IsBoundMethod() {
		bm := target.AsBoundMethod()
		if bm.Func.IsBuiltinFunc() {
			bf := bm.Func.AsBuiltinFunc()
			if bf.Fn != nil {
				total := argc + 1
				if scratchBase+total+64 > len(st.stack) {
					st.stack = vm.growStack(st, st.stack, scratchBase+total+128)
				}
				st.stack[scratchBase] = bm.Self
				if argc > 0 {
					copy(st.stack[scratchBase+1:scratchBase+total], st.stack[argStart:argStart+argc])
				}
				vm.execCtx.SP = scratchBase + total
				res := vm.invokeBuiltinSafe(bf.Fn, bm.Func, st.stack[scratchBase:scratchBase+total], nil)
				st.stack = st.syncStackFromExecCtx(vm, st.stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, rc, ctlRaise
				}
				st.stack[base+int(rd)] = res
				return ip + 1, rc, ctlReload
			}
		}
	}

	// Fast direct Reg-to-Reg promotion + call for exact-arity Python functions.
	if target.IsFunction() && !vm.hasTrackedViews {
		fn := target.AsFunction()
		callee := AsCodeObject(fn.Code)
		if callee != nil && !callee.IsGenerator && !callee.IsCoroutine && !callee.IsAsyncGenerator &&
			callee.SimplePositionalOnly && argc == callee.PositionalParamCount &&
			len(fn.Defaults) == 0 && fn.KwDefaults == nil && !callee.HasCellVars {
			cc := vm.fastCalleeCache(callee)
			if cc.tier2Done == 0 {
				maybeOptimizeCodeObject(vm, callee, cc)
			}
			if calleeRC := cc.regCode; calleeRC != nil {
				ic := st.cache.ensureRegIC(rc.numIC)
				ic[icIdx] = ICSlot{
					CachedVal:   target,
					CalleeCache: cc,
				}
				if st.frameIdx+2 > len(vm.frames) {
					vm.ensureFrameCapacity(st.frameIdx + 2)
				}
				if vm.limits.MaxStackDepth > 0 && st.frameIdx+1 >= vm.limits.MaxStackDepth {
					vm.checkPeriodicSafepoint(st.frameIdx + 1)
				}
				if st.frameIdx+1 >= 1000 {
					st.raiseExc = val.NewException(val.RecursionErrorType, "maximum recursion depth exceeded")
					return ip, rc, ctlRaise
				}
				newBase := scratchBase
				need := newBase + max(256, int(calleeRC.maxRegs)+64)
				if need > len(st.stack) {
					st.stack = vm.growStack(st, st.stack, need)
				}
				if argc > 0 {
					copy(st.stack[newBase:newBase+argc], st.stack[argStart:argStart+argc])
				}
				nextIP, nextRC := vm.pushRegFrameFast(st, rc, fn, cc, calleeRC, newBase, int(rd), ip)
				return nextIP, nextRC, ctlReload
			}
			// Zero-alloc Tier-1 bridge for exact-arity Tier-1 functions (including closures and super() methods).
			ic := st.cache.ensureRegIC(rc.numIC)
			ic[icIdx] = ICSlot{
				CachedVal:   target,
				CalleeCache: cc,
			}
			numLocals := max(callee.EffectiveLocals, max(callee.NumLocals, argc))
			if scratchBase+max(256, numLocals+64) > len(st.stack) {
				st.stack = vm.growStack(st, st.stack, scratchBase+max(256, numLocals+128))
			}
			if argc > 0 {
				copy(st.stack[scratchBase:scratchBase+argc], st.stack[argStart:argStart+argc])
			}
			res, raised := vm.callTier1SimpleFromRegWithCache(st, rc, fn, cc, scratchBase, argc, ip)
			if raised {
				return ip, rc, ctlRaise
			}
			st.stack[base+int(rd)] = res
			return ip + 1, rc, ctlReload
		}
	}

	// Copy args into scratchBase so callFromHookSafe runs cleanly without clobbering caller registers.
	if scratchBase+argc+64 > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, scratchBase+argc+128)
	}
	if argc > 0 {
		copy(st.stack[scratchBase:scratchBase+argc], st.stack[argStart:argStart+argc])
	}
	vm.execCtx.SP = scratchBase + argc
	res := vm.callFromHookSafe(target, scratchBase, argc, nil)
	st.stack = st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, rc, ctlRaise
	}
	st.stack[base+int(rd)] = res
	return ip + 1, rc, ctlReload
}

//go:noinline
func (vm *VM) regCallMethodPICSlow(st *loopState, rc *RegCode, rd, recvReg uint8, argc int, methodConstIdx uint32, icIdx, ip, scratchBase int) (int, *RegCode, loopCtl) {
	base := st.base
	receiver := st.stack[base+int(recvReg)]
	argStart := base + int(recvReg) + 1
	methodName := st.consts[methodConstIdx]

	// Fast ICKindMethodDescr hit for native/builtin methods (str.*, list.append, dict.get, re.match, etc.).
	if !vm.hasTrackedViews && uint(icIdx) < uint(len(st.cache.regIC)) {
		entry := &st.cache.regIC[icIdx]
		if entry.Kind == ICKindMethodDescr && entry.NativeFn != nil {
			matched := false
			if entry.ExpectedShapeID == 0 {
				if hdr := receiver.HeaderPtr(); hdr != nil &&
					hdr.Kind != val.KindInstance && hdr.Kind != val.KindSuper && hdr.Kind != val.KindModule && hdr.Kind != val.KindType {
					tp := hdr.Type
					if tp == nil && hdr.Kind == val.KindInlineStr {
						tp = val.StrType
					}
					if tp != nil && entry.ExpectedAuxVer == uint64(uintptr(unsafe.Pointer(tp))) && tp.Version() == entry.ExpectedTypeVer {
						matched = true
					}
				}
			} else if instObj := receiver.AsInstanceChecked(); instObj != nil && instObj.Shape != nil &&
				instObj.Shape.ID == entry.ExpectedShapeID && instObj.Header.Type.Version() == entry.ExpectedTypeVer {
				matched = true
			}
			if matched {
				vm.execCtx.SP = scratchBase
				res := vm.invokeBuiltinSafe(entry.NativeFn, entry.CachedVal, st.stack[base+int(recvReg):base+int(recvReg)+argc+1], nil)
				st.stack = st.syncStackFromExecCtx(vm, st.stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, rc, ctlRaise
				}
				st.stack[base+int(rd)] = res
				return ip + 1, rc, ctlReload
			}
		}
	}

	// General module-attribute call quickening (works for any module: math, struct, time, os, user modules).
	if !vm.hasTrackedViews && receiver.IsModule() {
		mod := receiver.AsModule()
		if mod.Header.Type == val.ModuleType && mod.Dict != nil {
			modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
			modPtr := uint64(uintptr(unsafe.Pointer(mod)))
			interned := val.InternValue(methodName)
			if v, ok := mod.Dict.Get(interned); ok && !v.IsLazyImport() {
				if v.IsBuiltinFunc() {
					if bf := v.AsBuiltinFunc(); bf.Fn != nil {
						ic := st.cache.ensureRegIC(rc.numIC)
						ic[icIdx] = ICSlot{
							Kind:            ICKindModuleAttr,
							ExpectedTypeVer: modVer,
							ExpectedAuxVer:  modPtr,
							CachedVal:       v,
							NativeFn:        bf.Fn,
						}
						vm.execCtx.SP = scratchBase
						res := vm.invokeBuiltinSafe(bf.Fn, v, st.stack[argStart:argStart+argc], nil)
						st.stack = st.syncStackFromExecCtx(vm, st.stack)
						if vm.execCtx.HasException() {
							exc := vm.execCtx.ActiveException
							vm.execCtx.ClearException()
							st.raiseExc = exc
							return ip, rc, ctlRaise
						}
						st.stack[base+int(rd)] = res
						return ip + 1, rc, ctlReload
					}
				} else if v.IsFunction() {
					fn := v.AsFunction()
					callee := AsCodeObject(fn.Code)
					if callee != nil && !callee.IsGenerator && !callee.IsCoroutine && !callee.IsAsyncGenerator &&
						callee.SimplePositionalOnly && argc == callee.PositionalParamCount &&
						len(fn.Defaults) == 0 && fn.KwDefaults == nil && !callee.HasCellVars {
						cc := vm.fastCalleeCache(callee)
						if cc.tier2Done == 0 {
							maybeOptimizeCodeObject(vm, callee, cc)
						}
						if calleeRC := cc.regCode; calleeRC != nil {
							if st.frameIdx+2 > len(vm.frames) {
								vm.ensureFrameCapacity(st.frameIdx + 2)
							}
							if st.frameIdx+1 >= 1000 {
								st.raiseExc = val.NewException(val.RecursionErrorType, "maximum recursion depth exceeded")
								return ip, rc, ctlRaise
							}
							newBase := scratchBase
							need := newBase + max(256, int(calleeRC.maxRegs)+64)
							if need > len(st.stack) {
								st.stack = vm.growStack(st, st.stack, need)
							}
							if argc > 0 {
								copy(st.stack[newBase:newBase+argc], st.stack[argStart:argStart+argc])
							}
							nextIP, nextRC := vm.pushRegFrameFast(st, rc, fn, cc, calleeRC, newBase, int(rd), ip)
							return nextIP, nextRC, ctlReload
						}
						numLocals := max(callee.EffectiveLocals, max(callee.NumLocals, argc))
						if scratchBase+max(256, numLocals+64) > len(st.stack) {
							st.stack = vm.growStack(st, st.stack, scratchBase+max(256, numLocals+128))
						}
						if argc > 0 {
							copy(st.stack[scratchBase:scratchBase+argc], st.stack[argStart:argStart+argc])
						}
						res, raised := vm.callTier1SimpleFromRegWithCache(st, rc, fn, cc, scratchBase, argc, ip)
						if raised {
							return ip, rc, ctlRaise
						}
						st.stack[base+int(rd)] = res
						return ip + 1, rc, ctlReload
					}
				}
			}
		}
	}

	// Populate ICKindMethodDescr for non-instance native receivers (str, list, dict, set, bytes, Pattern, etc.).
	if !vm.hasTrackedViews && !receiver.IsInstance() && !receiver.IsSuper() && !receiver.IsModule() && !receiver.IsType() {
		if tp := receiver.Type(); tp != nil && (tp.Flags&val.FlagCustomGetAttribute) == 0 {
			interned := val.InternValue(methodName)
			if descr, _, found := tp.LookupMRO(interned); found && descr.IsBuiltinFunc() {
				bf := descr.AsBuiltinFunc()
				if bf.Fn != nil && !bf.NoBind {
					ic := st.cache.ensureRegIC(rc.numIC)
					ic[icIdx] = ICSlot{
						Kind:            ICKindMethodDescr,
						ExpectedTypeVer: tp.Version(),
						ExpectedAuxVer:  uint64(uintptr(unsafe.Pointer(tp))),
						ExpectedShapeID: 0,
						CachedVal:       descr,
						NativeFn:        bf.Fn,
					}
					vm.execCtx.SP = scratchBase
					res := vm.invokeBuiltinSafe(bf.Fn, descr, st.stack[base+int(recvReg):base+int(recvReg)+argc+1], nil)
					st.stack = st.syncStackFromExecCtx(vm, st.stack)
					if vm.execCtx.HasException() {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						st.raiseExc = exc
						return ip, rc, ctlRaise
					}
					st.stack[base+int(rd)] = res
					return ip + 1, rc, ctlReload
				}
			}
		}
	}

	// Populate regMethodPIC on instance method hits.
	if instObj := receiver.AsInstanceChecked(); instObj != nil && instObj.Shape != nil && instObj.Header.Type != nil && !vm.hasTrackedViews {
		instType := instObj.Header.Type
		if (instType.Flags & val.FlagCustomGetAttribute) == 0 {
			// Check existing regMethodPIC hit first (avoids LookupMRO and bound-method allocation for Tier-1 methods).
			var m val.Value
			var fn *val.PyFunction
			if uint(icIdx) < uint(len(st.cache.regMethodPIC)) {
				pic := &st.cache.regMethodPIC[icIdx]
				instID := instObj.Shape.ID
				instVer := instType.Version()
				for k := range 4 {
					if e := &pic[k]; e.Shape != nil && e.Shape.ID == instID && e.TypeVersion == instVer && e.TargetFn != nil {
						m = e.TargetVal
						fn = e.TargetFn
						break
					}
				}
			}
			if fn == nil {
				interned := val.InternValue(methodName)
				if _, hasInstAttr := instObj.Shape.Lookup(interned); !hasInstAttr {
					if foundVal, _, found := instType.LookupMRO(interned); found && foundVal.IsFunction() {
						m = foundVal
						fn = foundVal.AsFunction()
						pic := st.cache.ensureRegMethodPIC(rc.numIC)
						insertRegMethodPIC(&pic[icIdx], instObj.Shape, instType.Version(), m, fn)
					}
				}
			}
			if fn != nil {
				callee := AsCodeObject(fn.Code)
				totalArgs := argc + 1
				if callee != nil && !callee.IsGenerator && !callee.IsCoroutine && !callee.IsAsyncGenerator &&
					callee.SimplePositionalOnly && totalArgs == callee.PositionalParamCount &&
					len(fn.Defaults) == 0 && fn.KwDefaults == nil && !callee.HasCellVars {
					cc := vm.fastCalleeCache(callee)
					if cc.tier2Done == 0 {
						maybeOptimizeCodeObject(vm, callee, cc)
					}
					if calleeRC := cc.regCode; calleeRC != nil {
						if st.frameIdx+2 > len(vm.frames) {
							vm.ensureFrameCapacity(st.frameIdx + 2)
						}
						if st.frameIdx+1 >= 1000 {
							st.raiseExc = val.NewException(val.RecursionErrorType, "maximum recursion depth exceeded")
							return ip, rc, ctlRaise
						}
						newBase := scratchBase
						need := newBase + max(256, int(calleeRC.maxRegs)+64)
						if need > len(st.stack) {
							st.stack = vm.growStack(st, st.stack, need)
						}
						st.stack[newBase] = receiver
						if argc > 0 {
							copy(st.stack[newBase+1:newBase+1+argc], st.stack[argStart:argStart+argc])
						}
						nextIP, nextRC := vm.pushRegFrameFast(st, rc, fn, cc, calleeRC, newBase, int(rd), ip)
						return nextIP, nextRC, ctlReload
					}
				}
				// Tier-1 Python method: invoke unbound function with receiver directly (0 allocs).
				if scratchBase+max(256, callee.NumLocals+64) > len(st.stack) {
					st.stack = vm.growStack(st, st.stack, scratchBase+max(256, callee.NumLocals+128))
				}
				st.stack[scratchBase] = receiver
				if argc > 0 {
					copy(st.stack[scratchBase+1:scratchBase+1+argc], st.stack[argStart:argStart+argc])
				}
				if callee != nil && !callee.IsGenerator && !callee.IsCoroutine && !callee.IsAsyncGenerator &&
					callee.SimplePositionalOnly && totalArgs == callee.PositionalParamCount &&
					len(fn.Defaults) == 0 && fn.KwDefaults == nil && !callee.HasCellVars &&
					callee.DirectCaptureCount == 0 {
					res, raised := vm.callTier1SimpleFromReg(st, rc, fn, callee, scratchBase, totalArgs, ip)
					if raised {
						return ip, rc, ctlRaise
					}
					st.stack[base+int(rd)] = res
					return ip + 1, rc, ctlReload
				}
				vm.execCtx.SP = scratchBase + totalArgs
				res := vm.callFromHookSafe(m, scratchBase, totalArgs, nil)
				st.stack = st.syncStackFromExecCtx(vm, st.stack)
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, rc, ctlRaise
				}
				st.stack[base+int(rd)] = res
				return ip + 1, rc, ctlReload
			}
		}
	}

	// Generic fallback: getattr(receiver, methodName) then callFromHookSafe.
	vm.execCtx.SP = scratchBase
	methodVal := vm.getAttrSafe(receiver, methodName)
	st.stack = st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, rc, ctlRaise
	}
	if scratchBase+argc+64 > len(st.stack) {
		st.stack = vm.growStack(st, st.stack, scratchBase+argc+128)
	}
	if argc > 0 {
		copy(st.stack[scratchBase:scratchBase+argc], st.stack[argStart:argStart+argc])
	}
	vm.execCtx.SP = scratchBase + argc
	res := vm.callFromHookSafe(methodVal, scratchBase, argc, nil)
	st.stack = st.syncStackFromExecCtx(vm, st.stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, rc, ctlRaise
	}
	st.stack[base+int(rd)] = res
	return ip + 1, rc, ctlReload
}

// callTier1SimpleFromReg invokes an exact-arity Tier-1 Python function (including
// closures with DirectCaptureCount > 0 and super()-using methods with fn.CellPtrs)
// from a RegCode caller with 0 heap allocations.
//
//go:noinline
func (vm *VM) callTier1SimpleFromReg(
	st *loopState,
	callerRC *RegCode,
	fn *val.PyFunction,
	callee *CodeObject,
	newBase, totalArgs, callRegIP int,
) (val.Value, bool) {
	cc := vm.fastCalleeCache(callee)
	if cc.tier2Done == 0 {
		maybeOptimizeCodeObject(vm, callee, cc)
	}
	return vm.callTier1SimpleFromRegWithCache(st, callerRC, fn, cc, newBase, totalArgs, callRegIP)
}

// callTier1SimpleFromRegWithCache invokes an exact-arity Tier-1 Python function
// using a pre-resolved VMCodeCache, binding fn.CellPtrs and copying any zero-cell
// DirectCaptureCount captures into trailing local slots.
//
//go:noinline
func (vm *VM) callTier1SimpleFromRegWithCache(
	st *loopState,
	callerRC *RegCode,
	fn *val.PyFunction,
	cc *VMCodeCache,
	newBase, totalArgs, callRegIP int,
) (val.Value, bool) {
	callee := cc.co
	codePtr, codeLen := vm.loadWindow(callee, cc)

	savedFrameIdx := st.frameIdx
	savedStartFrameIdx := st.startFrameIdx
	savedCO := st.curCO
	savedCache := st.cache
	savedRegCode := st.regCode
	savedCodePtr := st.codePtr
	savedCodeLen := st.codeLen
	savedConsts := st.consts
	savedBase := st.base
	savedGlobals := st.globals
	savedCells := st.cells
	savedCellPtrs := st.cellPtrs
	savedExecSP := vm.execCtx.SP

	if savedFrameIdx+2 > len(vm.frames) {
		vm.ensureFrameCapacity(savedFrameIdx + 2)
	}
	saved := &vm.frames[savedFrameIdx]
	saved.cache = savedCache
	saved.ip = callRegIP + 1
	saved.baseOffset = savedBase
	saved.excStackLen = int32(len(vm.excStack))
	saved.flags = (saved.flags & frameFlagHasShadow) | frameFlagReg | frameFlagStackIsVM

	nextFrame := savedFrameIdx + 1
	numLocals := max(callee.EffectiveLocals, max(callee.NumLocals, totalArgs))
	unboundEnd := numLocals
	if callee.DirectCaptureCount > 0 && callee.DirectCaptureBase < unboundEnd {
		unboundEnd = callee.DirectCaptureBase
	}
	for i := totalArgs; i < unboundEnd; i++ {
		st.stack[newBase+i] = val.UnboundVal()
	}
	if callee.DirectCaptureCount > 0 && len(fn.Closure) >= callee.DirectCaptureCount {
		copy(st.stack[newBase+callee.DirectCaptureBase:], fn.Closure[:callee.DirectCaptureCount])
	}

	st.frameIdx = nextFrame
	st.startFrameIdx = nextFrame
	st.curCO = callee
	st.cache = cc
	st.regCode = nil
	st.codePtr = codePtr
	st.codeLen = codeLen
	st.consts = cc.frameConsts(callee)
	st.base = newBase
	if fn.Globals != nil {
		st.globals = fn.Globals
	}
	if callee.DirectCaptureCount > 0 {
		st.cells = nil
		st.cellPtrs = nil
	} else {
		st.cells = fn.Closure
		st.cellPtrs = fn.CellPtrs
	}
	st.ip = callee.EntryIP
	st.sp = newBase + numLocals

	vm.activeCO = callee
	vm.activeBaseOffset = newBase
	vm.activeCells = st.cells
	vm.activeCellPtrs = st.cellPtrs
	vm.activeFrameIdx = nextFrame
	vm.execCtx.SP = newBase + numLocals
	vm.unhandledException = val.NoneVal()

	res := vm.runLoopWithGlobals(st)

	clear(st.stack[newBase : newBase+numLocals])

	st.frameIdx = savedFrameIdx
	st.startFrameIdx = savedStartFrameIdx
	st.curCO = savedCO
	st.cache = savedCache
	st.regCode = savedRegCode
	st.codePtr = savedCodePtr
	st.codeLen = savedCodeLen
	st.consts = savedConsts
	st.base = savedBase
	st.globals = savedGlobals
	st.cells = savedCells
	st.cellPtrs = savedCellPtrs

	vm.activeCO = savedCO
	vm.activeBaseOffset = savedBase
	vm.activeCells = savedCells
	vm.activeCellPtrs = savedCellPtrs
	vm.activeFrameIdx = savedFrameIdx
	vm.execCtx.SP = savedExecSP

	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		st.raiseExc = exc
		return val.NoneVal(), true
	}
	return res, false
}

func insertRegMethodPIC(pic *[4]val.MethodPICEntry, shape *val.Shape, typeVer uint64, targetVal val.Value, fn *val.PyFunction) {
	for i := range 4 {
		if pic[i].Shape == shape && pic[i].TypeVersion == typeVer {
			pic[i].TargetVal = targetVal
			pic[i].TargetFn = fn
			return
		}
	}
	pic[3] = pic[2]
	pic[2] = pic[1]
	pic[1] = pic[0]
	pic[0] = val.MethodPICEntry{
		Shape:       shape,
		TypeVersion: typeVer,
		TargetVal:   targetVal,
		TargetFn:    fn,
	}
}

// dispatchRegColdSub executes REG_COLD sub-opcodes out-of-line.
//
//go:noinline
func (vm *VM) dispatchRegColdSub(st *loopState, rc *RegCode, rd, ra, rb uint8, meta *regColdMeta, ip, scratchBase int) (int, *RegCode, loopCtl) {
	base := st.base
	switch meta.subOp {
	case REG_COLD_CHECK_LOCAL:
		v := st.stack[base+int(ra)]
		if v.IsUnbound() {
			varName := ""
			if st.curCO != nil && int(meta.argBx) < len(st.curCO.VarNames) {
				varName = st.curCO.VarNames[meta.argBx].AsString()
			}
			st.raiseExc = val.NewException(val.UnboundLocalErrorType, "cannot access local variable '"+varName+"' where it is not associated with a value")
			return ip, rc, ctlRaise
		}
		return ip + 1, rc, ctlNext

	case REG_COLD_STORE_GLOBAL:
		v := st.stack[base+int(ra)]
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		markViewEscaped(v)
		name := st.consts[meta.argBx]
		if st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil {
			st.globals.Set(name, v)
		} else {
			vm.execCtx.SP = scratchBase
			vm.setItemSafe(val.ObjectVal(&st.globals.Header), name, v)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
		}
		return ip + 1, rc, ctlReload

	case REG_COLD_STORE_DEREF:
		v := st.stack[base+int(ra)]
		val.ClearEphemeral(v)
		val.DetachStringBuf(v)
		markViewEscaped(v)
		idx := int(meta.argBx)
		if idx < len(vm.activeCellPtrs) && vm.activeCellPtrs[idx] != nil {
			vm.activeCellPtrs[idx].Value = v
		} else if idx < len(st.cells) && st.cells[idx].IsCell() {
			st.cells[idx].AsCell().Value = v
		}
		return ip + 1, rc, ctlNext

	case REG_COLD_LOAD_CLOSURE:
		idx := int(meta.argBx)
		if idx < len(st.cells) {
			st.stack[base+int(rd)] = st.cells[idx]
		} else if idx < len(vm.activeCellPtrs) && vm.activeCellPtrs[idx] != nil {
			st.stack[base+int(rd)] = val.ObjectVal(&vm.activeCellPtrs[idx].Header)
		}
		return ip + 1, rc, ctlNext

	case REG_COLD_DELETE_FAST:
		if st.stack[base+int(ra)].IsUnbound() {
			varName := ""
			if st.curCO != nil && int(meta.argBx) < len(st.curCO.VarNames) {
				varName = st.curCO.VarNames[meta.argBx].AsString()
			}
			st.raiseExc = val.NewException(val.UnboundLocalErrorType, "cannot access local variable '"+varName+"' where it is not associated with a value")
			return ip, rc, ctlRaise
		}
		st.stack[base+int(ra)] = val.UnboundVal()
		return ip + 1, rc, ctlNext

	case REG_COLD_DELETE_GLOBAL:
		name := st.consts[meta.argBx]
		if !st.globals.Delete(name) {
			st.raiseExc = val.NewNameError("name '" + name.AsString() + "' is not defined")
			return ip, rc, ctlRaise
		}
		return ip + 1, rc, ctlNext

	case REG_COLD_DELETE_ATTR:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opDeleteAttr(st, Encode(OP_DELETE_ATTR, 0, meta.argBx), ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[scratchBase] = val.Value{}
		return ip + 1, rc, ctlReload

	case REG_COLD_DELETE_SUBSCR:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		st.stack[scratchBase+1] = st.stack[base+int(rb)]
		_, _, ctl := vm.opDeleteSubscr(st, ip, scratchBase+2)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		clear(st.stack[scratchBase : scratchBase+2])
		return ip + 1, rc, ctlReload

	case REG_COLD_UNARY_NOT:
		v := st.stack[base+int(ra)]
		if v.IsInstance() {
			truthy, raised := vm.evalInstanceTruthy(st, v, ip, scratchBase)
			if raised {
				return ip, rc, ctlRaise
			}
			st.stack[base+int(rd)] = val.BoolVal(!truthy)
			return ip + 1, rc, ctlReload
		}
		st.stack[base+int(rd)] = val.BoolVal(!v.IsTruthy())
		return ip + 1, rc, ctlNext

	case REG_COLD_UNARY_NEG, REG_COLD_UNARY_POS, REG_COLD_UNARY_INVERT:
		var t1Op Opcode
		switch meta.subOp {
		case REG_COLD_UNARY_NEG:
			t1Op = OP_UNARY_NEGATIVE
		case REG_COLD_UNARY_POS:
			t1Op = OP_UNARY_POSITIVE
		default:
			t1Op = OP_UNARY_INVERT
		}
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opUnarySlow(st, t1Op, ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		st.stack[scratchBase] = val.Value{}
		return ip + 1, rc, ctlReload

	case REG_COLD_TRUE_DIV, REG_COLD_POW, REG_COLD_LSHIFT, REG_COLD_RSHIFT, REG_COLD_MATMUL:
		var t1Op Opcode
		switch meta.subOp {
		case REG_COLD_TRUE_DIV:
			t1Op = OP_BINARY_TRUE_DIV
		case REG_COLD_POW:
			t1Op = OP_BINARY_POW
		case REG_COLD_LSHIFT:
			t1Op = OP_BINARY_LSHIFT
		case REG_COLD_RSHIFT:
			t1Op = OP_BINARY_RSHIFT
		default:
			t1Op = OP_BINARY_MATMUL
		}
		return vm.regBinarySlow(st, rc, t1Op, rd, ra, rb, ip, scratchBase, false)

	case REG_COLD_BINARY_SLICE:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		st.stack[scratchBase+1] = st.stack[base+int(rb)]
		st.stack[scratchBase+2] = st.stack[base+int(meta.extraReg)]
		_, _, ctl := vm.opBinarySlice(st, ip, scratchBase+3)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+3])
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_SLICE:
		count := int(rb)
		copy(st.stack[scratchBase:scratchBase+count], st.stack[base+int(ra):base+int(ra)+count])
		_, _, ctl := vm.opBuildSlice(st, Encode(OP_BUILD_SLICE, 0, meta.argBx), ip, scratchBase+count)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+count])
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_LIST:
		count := int(rb)
		items := make([]val.Value, count)
		for i := range count {
			v := st.stack[base+int(ra)+i]
			val.ClearEphemeral(v)
			val.DetachStringBuf(v)
			markViewEscaped(v)
			items[i] = v
		}
		st.stack[base+int(rd)] = val.NewListFromSlice(items)
		return ip + 1, rc, ctlNext

	case REG_COLD_BUILD_TUPLE:
		count := int(rb)
		if count == 0 {
			st.stack[base+int(rd)] = val.EmptyTupleVal()
			return ip + 1, rc, ctlNext
		}
		items := make([]val.Value, count)
		for i := range count {
			v := st.stack[base+int(ra)+i]
			val.ClearEphemeral(v)
			val.DetachStringBuf(v)
			markViewEscaped(v)
			items[i] = v
		}
		st.stack[base+int(rd)] = val.NewTupleFromSlice(items)
		return ip + 1, rc, ctlNext

	case REG_COLD_BUILD_SET:
		count := int(rb)
		s := val.NewSetVal()
		setObj := s.AsSet()
		if count > 0 {
			setObj.Reserve(count)
		}
		for i := range count {
			v := st.stack[base+int(ra)+i]
			val.ClearEphemeral(v)
			val.DetachStringBuf(v)
			markViewEscaped(v)
			vm.execCtx.SP = scratchBase
			vm.regSetAddSafe(setObj, v)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
		}
		st.stack[base+int(rd)] = s
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_MAP:
		count := int(rb)
		var dictVal val.Value
		switch {
		case count == 0 && meta.argA == 1:
			dictVal = val.DictVal(val.NewSmallDictDirect(0))
		case count == 0:
			dictVal = val.NewDictVal(0)
		case count <= 5:
			dictVal = val.DictVal(val.NewSmallDictDirect(0))
		default:
			dictVal = val.NewDictVal(count)
		}
		d := dictVal.AsDict()
		for i := range count {
			k := st.stack[base+int(ra)+2*i]
			v := st.stack[base+int(ra)+2*i+1]
			val.ClearEphemeral(k)
			val.ClearEphemeral(v)
			val.DetachStringBuf(k)
			val.DetachStringBuf(v)
			markViewEscaped(v)
			vm.execCtx.SP = scratchBase
			vm.dictSetSafe(d, k, v)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
		}
		st.stack[base+int(rd)] = dictVal
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_CONST_KEY_MAP:
		count := int(rb)
		keysVal := st.stack[base+int(ra)+count]
		var keys []val.Value
		if keysVal.IsTuple() {
			keys = keysVal.AsTuple().Items
		}
		var dictVal val.Value
		if count <= 5 {
			dictVal = val.DictVal(val.NewSmallDictDirect(0))
		} else {
			dictVal = val.NewDictVal(count)
		}
		d := dictVal.AsDict()
		for i := range count {
			v := st.stack[base+int(ra)+i]
			val.ClearEphemeral(v)
			val.DetachStringBuf(v)
			markViewEscaped(v)
			vm.execCtx.SP = scratchBase
			vm.dictSetSafe(d, keys[i], v)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
		}
		st.stack[base+int(rd)] = dictVal
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_STRING:
		count := int(rb)
		copy(st.stack[scratchBase:scratchBase+count], st.stack[base+int(ra):base+int(ra)+count])
		_, _, ctl := vm.opBuildString(st, Encode(OP_BUILD_STRING, 0, meta.argBx), ip, scratchBase+count)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+count])
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_MAP_UNPACK:
		count := int(rb)
		copy(st.stack[scratchBase:scratchBase+count], st.stack[base+int(ra):base+int(ra)+count])
		_, _, ctl := vm.opBuildMapUnpack(st, Encode(OP_BUILD_MAP_UNPACK, meta.argA, meta.argBx), ip, scratchBase+count)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+count])
		return ip + 1, rc, ctlReload

	case REG_COLD_LIST_EXTEND:
		st.stack[scratchBase] = st.stack[base+int(rd)]
		st.stack[scratchBase+1] = st.stack[base+int(ra)]
		_, _, ctl := vm.opListExtend(st, Encode(OP_LIST_EXTEND, meta.argA, meta.argBx), ip, scratchBase+2)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		clear(st.stack[scratchBase : scratchBase+2])
		return ip + 1, rc, ctlReload

	case REG_COLD_LIST_TO_TUPLE:
		lst := st.stack[base+int(ra)].AsList()
		st.stack[base+int(rd)] = val.NewTupleFromSlice(append([]val.Value(nil), lst.Items...))
		return ip + 1, rc, ctlNext

	case REG_COLD_LIST_TO_SET:
		lst := st.stack[base+int(ra)].AsList()
		s := val.NewSetVal()
		setObj := s.AsSet()
		if len(lst.Items) > 0 {
			setObj.Reserve(len(lst.Items))
		}
		for _, item := range lst.Items {
			vm.execCtx.SP = scratchBase
			vm.regSetAddSafe(setObj, item)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
		}
		st.stack[base+int(rd)] = s
		return ip + 1, rc, ctlReload

	case REG_COLD_FORMAT_VALUE:
		sp := scratchBase
		st.stack[sp] = st.stack[base+int(ra)]
		sp++
		if rb != regUnused {
			st.stack[sp] = st.stack[base+int(rb)]
			sp++
		}
		_, _, ctl := vm.opFormatValue(st, Encode(OP_FORMAT_VALUE, meta.argA, meta.argBx), ip, sp)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase:sp])
		return ip + 1, rc, ctlReload

	case REG_COLD_UNPACK_SEQUENCE:
		count := int(rb)
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opUnpackSequence(st, Encode(OP_UNPACK_SEQUENCE, 0, meta.argBx), ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		copy(st.stack[base+int(rd):base+int(rd)+count], st.stack[scratchBase:scratchBase+count])
		clear(st.stack[scratchBase : scratchBase+count])
		return ip + 1, rc, ctlReload

	case REG_COLD_UNPACK_EX:
		total := int(rb)
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opUnpackEx(st, Encode(OP_UNPACK_EX, meta.argA, meta.argBx), ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		copy(st.stack[base+int(rd):base+int(rd)+total], st.stack[scratchBase:scratchBase+total])
		clear(st.stack[scratchBase : scratchBase+total])
		return ip + 1, rc, ctlReload

	case REG_COLD_CALL_KW:
		totalArgs := int(rb)
		target := st.stack[base+int(ra)]
		kwnames := st.consts[meta.argBx].AsTuple().Items
		if scratchBase+totalArgs+64 > len(st.stack) {
			st.stack = vm.growStack(st, st.stack, scratchBase+totalArgs+128)
		}
		if totalArgs > 0 {
			copy(st.stack[scratchBase:scratchBase+totalArgs], st.stack[base+int(ra)+1:base+int(ra)+1+totalArgs])
		}
		vm.execCtx.SP = scratchBase + totalArgs
		res := vm.callFromHookSafe(target, scratchBase, totalArgs, kwnames)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = res
		return ip + 1, rc, ctlReload

	case REG_COLD_CALL_EX:
		hasKW := (meta.argA & 0x01) != 0
		target := st.stack[base+int(ra)]
		argsVal := st.stack[base+int(ra)+1]
		var kwDictVal val.Value
		if hasKW {
			kwDictVal = st.stack[base+int(ra)+2]
		}
		var posArgs []val.Value
		switch {
		case argsVal.IsTuple():
			posArgs = argsVal.AsTuple().Items
		case argsVal.IsList():
			posArgs = argsVal.AsList().Items
		default:
			vm.execCtx.SP = scratchBase
			it := vm.getIterSafe(argsVal)
			st.stack = st.syncStackFromExecCtx(vm, st.stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, rc, ctlRaise
			}
			for {
				var item val.Value
				var status val.IterStatus
				if it.IsGenerator() {
					item, status = vm.resumeGenerator(&vm.execCtx, it.AsGenerator(), val.NoneVal())
				} else if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
					item, status = vm.iterNextSafe(tpIterNext, it)
				} else {
					st.raiseExc = val.NewTypeError("'" + it.Type().Name + "' object is not an iterator")
					return ip, rc, ctlRaise
				}
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, rc, ctlRaise
				}
				if status == val.IterExhausted {
					break
				}
				posArgs = append(posArgs, item)
			}
		}
		var kwNames []val.Value
		var kwVals []val.Value
		if hasKW && !kwDictVal.IsNone() {
			if kwDictVal.IsDict() {
				for _, item := range kwDictVal.AsDict().Items() {
					if !item.Key.IsAnyString() {
						st.raiseExc = val.NewTypeError("keywords must be strings")
						return ip, rc, ctlRaise
					}
					kwNames = append(kwNames, item.Key)
					kwVals = append(kwVals, item.Value)
				}
			} else {
				st.raiseExc = val.NewTypeError("argument after ** must be a mapping")
				return ip, rc, ctlRaise
			}
		}
		totalArgs := len(posArgs) + len(kwVals)
		if scratchBase+totalArgs+64 > len(st.stack) {
			st.stack = vm.growStack(st, st.stack, scratchBase+totalArgs+128)
		}
		copy(st.stack[scratchBase:], posArgs)
		copy(st.stack[scratchBase+len(posArgs):], kwVals)
		vm.execCtx.SP = scratchBase + totalArgs
		res := vm.callFromHookSafe(target, scratchBase, totalArgs, kwNames)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = res
		return ip + 1, rc, ctlReload

	case REG_COLD_LOAD_METHOD:
		obj := st.stack[base+int(ra)]
		name := st.consts[meta.argBx]
		vm.execCtx.SP = scratchBase
		m := vm.getAttrSafe(obj, name)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = val.UnboundVal()
		st.stack[base+int(rd)+1] = m
		return ip + 1, rc, ctlReload

	case REG_COLD_CALL_METHOD:
		argc := int(rb)
		selfOrUnbound := st.stack[base+int(ra)]
		method := st.stack[base+int(ra)+1]
		argStart := base + int(ra) + 2
		totalArgs := argc
		dst := scratchBase
		if !selfOrUnbound.IsUnbound() {
			totalArgs++
		}
		if scratchBase+totalArgs+64 > len(st.stack) {
			st.stack = vm.growStack(st, st.stack, scratchBase+totalArgs+128)
		}
		if !selfOrUnbound.IsUnbound() {
			st.stack[dst] = selfOrUnbound
			dst++
		}
		if argc > 0 {
			copy(st.stack[dst:dst+argc], st.stack[argStart:argStart+argc])
		}
		vm.execCtx.SP = scratchBase + totalArgs
		res := vm.callFromHookSafe(method, scratchBase, totalArgs, nil)
		st.stack = st.syncStackFromExecCtx(vm, st.stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = res
		return ip + 1, rc, ctlReload

	case REG_COLD_MAKE_FUNCTION:
		nPop := int(rb)
		copy(st.stack[scratchBase:scratchBase+nPop], st.stack[base+int(ra):base+int(ra)+nPop])
		_, _, ctl := vm.opMakeFunction(st, Encode(OP_MAKE_FUNCTION, meta.argA, meta.argBx), ip, scratchBase+nPop)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+nPop])
		return ip + 1, rc, ctlReload

	case REG_COLD_BUILD_CLASS:
		nPop := int(rb)
		copy(st.stack[scratchBase:scratchBase+nPop], st.stack[base+int(ra):base+int(ra)+nPop])
		_, _, ctl := vm.opBuildClass(st, Encode(OP_BUILD_CLASS, meta.argA, meta.argBx), ip, scratchBase+nPop)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+nPop])
		return ip + 1, rc, ctlReload

	case REG_COLD_IMPORT_NAME:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		st.stack[scratchBase+1] = st.stack[base+int(rb)]
		_, _, ctl := vm.opImportName(st, Encode(OP_IMPORT_NAME, meta.argA, meta.argBx), ip, scratchBase+2)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+2])
		return ip + 1, rc, ctlReload

	case REG_COLD_IMPORT_FROM:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opImportFrom(st, Encode(OP_IMPORT_FROM, meta.argA, meta.argBx), ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase+1]
		clear(st.stack[scratchBase : scratchBase+2])
		return ip + 1, rc, ctlReload

	case REG_COLD_IMPORT_STAR:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opImportStar(st, ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[scratchBase] = val.Value{}
		return ip + 1, rc, ctlReload

	case REG_COLD_RAISE:
		argc := meta.argA
		sp := scratchBase
		if argc >= 1 {
			st.stack[sp] = st.stack[base+int(ra)]
			sp++
		}
		if argc == 2 {
			st.stack[sp] = st.stack[base+int(rb)]
			sp++
		}
		vm.opRaise(st, Encode(OP_RAISE, argc, meta.argBx), ip, sp)
		clear(st.stack[scratchBase:sp])
		return ip, rc, ctlRaise

	case REG_COLD_CHECK_EXC_MATCH:
		exc := st.stack[base+int(ra)]
		target := st.stack[base+int(rb)]
		match := false
		if target.IsType() {
			match = val.IsSubclass(exc.Type(), target.AsType())
		} else if target.IsTuple() {
			for _, item := range target.AsTuple().Items {
				if item.IsType() && val.IsSubclass(exc.Type(), item.AsType()) {
					match = true
					break
				}
			}
		}
		st.stack[base+int(rd)] = val.BoolVal(match)
		return ip + 1, rc, ctlNext

	case REG_COLD_PUSH_EXC_INFO:
		vm.excStack = append(vm.excStack, vm.lastException)
		exc := st.stack[base+int(ra)]
		vm.lastException = exc
		if exc == vm.deferredTBExc {
			vm.deferredTBBound = true
		}
		return ip + 1, rc, ctlNext

	case REG_COLD_POP_EXCEPT:
		vm.opPopExcept(st, ip, scratchBase)
		return ip + 1, rc, ctlNext

	case REG_COLD_SETUP_RANGE:
		count := int(rb)
		switch count {
		case 1:
			stopVal := st.stack[base+int(ra)]
			if stopVal.IsInt64() {
				st.stack[base+int(rd)] = val.RangeVal(0, int32(stopVal.AsInt64()))
			}
		case 2:
			startVal := st.stack[base+int(ra)]
			stopVal := st.stack[base+int(ra)+1]
			if startVal.IsInt64() && stopVal.IsInt64() {
				st.stack[base+int(rd)] = val.RangeVal(int32(startVal.AsInt64()), int32(stopVal.AsInt64()))
			}
		}
		return ip + 1, rc, ctlNext

	case REG_COLD_FOR_ITER_RANGE:
		it := st.stack[base+int(ra)]
		if it.IsRange() {
			cur := it.RangeStart()
			stop := it.RangeStop()
			if cur < stop {
				st.stack[base+int(ra)] = val.RangeVal(cur+1, stop)
				st.stack[base+int(rd)] = val.Int64Val(int64(cur))
				return ip + 1, rc, ctlNext
			}
		}
		return int(meta.argBx), rc, ctlNext

	case REG_COLD_MATCH_SEQUENCE:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opMatchSequence(st, ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase+1]
		clear(st.stack[scratchBase : scratchBase+2])
		return ip + 1, rc, ctlReload

	case REG_COLD_MATCH_MAPPING:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		_, _, ctl := vm.opMatchMapping(st, ip, scratchBase+1)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase+1]
		clear(st.stack[scratchBase : scratchBase+2])
		return ip + 1, rc, ctlReload

	case REG_COLD_MATCH_KEYS:
		st.stack[scratchBase] = st.stack[base+int(ra)]
		st.stack[scratchBase+1] = st.stack[base+int(rb)]
		_, _, ctl := vm.opMatchKeys(st, ip, scratchBase+2)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase+2]
		clear(st.stack[scratchBase : scratchBase+3])
		return ip + 1, rc, ctlReload

	case REG_COLD_MATCH_CLASS:
		copy(st.stack[scratchBase:scratchBase+3], st.stack[base+int(ra):base+int(ra)+3])
		_, _, ctl := vm.opMatchClass(st, Encode(OP_MATCH_CLASS, meta.argA, meta.argBx), ip, scratchBase+3)
		if ctl == ctlRaise {
			return ip, rc, ctlRaise
		}
		st.stack[base+int(rd)] = st.stack[scratchBase]
		clear(st.stack[scratchBase : scratchBase+3])
		return ip + 1, rc, ctlReload
	}
	return ip + 1, rc, ctlNext
}
