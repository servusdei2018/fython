package vm

import (
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// dispatchColdOpcode executes non-hot opcodes out of line so runLoopWithGlobals
// stays compact in instruction cache and register pressure.
//
//go:noinline
func (vm *VM) dispatchColdOpcode(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	stackPtr := unsafe.Pointer(unsafe.SliceData(st.stack))
	locals := (*[256]val.Value)(unsafe.Add(stackPtr, uintptr(st.base)*16))
	constsPtr := unsafe.Pointer(unsafe.SliceData(st.consts))
	codePtr := st.codePtr

	switch op {
	case OP_BINARY_ADD_INT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			ai, bi := a.AsInt64(), b.AsInt64()
			sum := ai + bi
			if ((ai ^ sum) & (bi ^ sum)) >= 0 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(sum)
				sp--
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opBinarySlow(st, OP_BINARY_ADD_INT, ip, sp)
	case OP_BINARY_ADD_FLOAT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsFloat64() && b.IsFloat64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Float64Val(a.AsFloat64() + b.AsFloat64())
			sp--
			ip++
			return ip, sp, ctlNext
		}
		return vm.opBinarySlow(st, OP_BINARY_ADD_FLOAT, ip, sp)
	case OP_BINARY_SUB_INT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			ai, bi := a.AsInt64(), b.AsInt64()
			diff := ai - bi
			if ((ai ^ bi) & (ai ^ diff)) >= 0 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(diff)
				sp--
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opBinarySlow(st, OP_BINARY_SUB_INT, ip, sp)
	case OP_BINARY_MUL:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			ai, bi := a.AsInt64(), b.AsInt64()
			if int64(int32(ai)) == ai && int64(int32(bi)) == bi {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(ai * bi)
				sp--
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opBinarySlow(st, OP_BINARY_MUL, ip, sp)
	case OP_STORE_SUBSCR:
		return vm.opStoreSubscr(st, ip, sp)
	case OP_COMPARE_LT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.BoolVal(a.AsInt64() < b.AsInt64())
			sp--
			ip++
			return ip, sp, ctlNext
		}
		if a.IsTuple() && b.IsTuple() && a.HeaderPtr().Type == val.TupleType && b.HeaderPtr().Type == val.TupleType {
			st.ip = ip
			vm.execCtx.SP = sp
			res := val.CompareTuplesFast(&vm.execCtx, a, b, CMP_LT)
			if !vm.execCtx.HasException() && unsafe.Pointer(unsafe.SliceData(vm.execCtx.Stack)) == stackPtr &&
				(!st.stackIsVM || unsafe.Pointer(unsafe.SliceData(vm.stack)) == stackPtr) {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = res
				sp--
				ip++
				return ip, sp, ctlNext
			}
			return vm.finishCompareTupleSlow(st, ip, sp, res)
		}
		return vm.opCompareLtSlow(st, ip, sp)
	case OP_COMPARE_LT_INT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.BoolVal(a.AsInt64() < b.AsInt64())
			sp--
			ip++
			return ip, sp, ctlNext
		}
		return vm.opCompareLtSlow(st, ip, sp)
	case OP_LOAD_METHOD:
		obj := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		if obj.IsInstance() {
			instObj := obj.AsInstance()
			instShape := instObj.Shape
			instType := instObj.Header.Type
			if uint(ip) < uint(len(st.cache.methodPIC)) {
				pic := &st.cache.methodPIC[ip]
				for k := range 4 {
					if pic[k].Shape == instShape && instType.Version() == pic[k].TypeVersion {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = pic[k].TargetVal
						sp++
						ip++
						return ip, sp, ctlNext
					}
				}
			}
			entry := st.cache.ic[ip]
			if instShape.ID == entry.ExpectedShapeID && instType.Version() == entry.ExpectedTypeVer && entry.Kind == ICKindClassMethod {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = entry.CachedVal
				sp++
				ip++
				return ip, sp, ctlNext
			}
		} else if obj.IsModule() && obj.AsModule().Header.Type == val.ModuleType {
			mod := obj.AsModule()
			entry := &st.cache.ic[ip]
			if entry.Kind == ICKindModuleAttr && entry.ExpectedTypeVer == atomic.LoadUint64(&mod.Dict.VersionTag) {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.UnboundVal()
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = entry.CachedVal
				sp++
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opLoadMethodSlow(st, inst, ip, sp)
	case OP_CALL_METHOD:
		return vm.opCallMethodSlow(st, inst, ip, sp)
	case OP_LOAD_FAST_ATTR_MUL_SELF:
		reg := uint8(inst >> 16)
		obj := locals[reg]
		if instObj := obj.AsInstanceChecked(); instObj != nil {
			instShape := instObj.Shape
			instType := instObj.Header.Type
			if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() && uint(ip+1) < uint(len(st.cache.attrPIC)) {
				pic := &st.cache.attrPIC[ip+1]
				if pic[0].Shape == instShape && pic[0].TypeVer == uint32(instType.Version()) {
					slotIdx := pic[0].SlotIndex
					if uint(slotIdx) < uint(len(instObj.Slots)) {
						v := instObj.Slots[slotIdx]
						if v.IsInt64() {
							vi := v.AsInt64()
							if int64(int32(vi)) == vi {
								*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(vi * vi)
								sp++
								ip += 5
								return ip, sp, ctlNext
							}
						}
					}
				}
			}
		}
		return vm.opFastAttrSlow(st, inst, ip, sp)

	case OP_STORE_FAST_ATTR:
		reg := uint8(inst >> 16)
		obj := locals[reg]
		v := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		if !v.IsString() && v.Kind() != val.KindMemoryView && !val.MultiThreadedFlag.Load() {
			if instObj := obj.AsInstanceChecked(); instObj != nil {
				instShape := instObj.Shape
				instType := instObj.Header.Type
				if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomSetAttr) == 0 && uint(ip+1) < uint(len(st.cache.attrPIC)) {
					pic := &st.cache.attrPIC[ip+1]
					if pic[0].Shape == instShape && pic[0].TypeVer == uint32(instType.Version()) {
						slotIdx := pic[0].SlotIndex
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							instObj.Slots[slotIdx] = v
							sp--
							ip += 2
							return ip, sp, ctlNext
						}
					}
				}
			}
		}
		return vm.opFastAttrSlow(st, inst, ip, sp)

	case OP_PRINT_EXPR:
		return vm.opPrintExpr(st, ip, sp)
	case OP_LOAD_LOCALS:
		return vm.opLoadLocals(st, ip, sp)
	case OP_LOAD_FROM_DICT_OR_GLOBALS, OP_LOAD_FROM_DICT_OR_DEREF:
		return vm.opLoadFromDictOr(st, inst, ip, sp)
	case OP_STORE_GLOBAL:
		return vm.opStoreGlobal(st, inst, ip, sp)
	case OP_LOAD_NAME:
		return vm.opLoadName(st, inst, ip, sp)
	case OP_STORE_NAME:
		return vm.opStoreName(st, inst, ip, sp)
	case OP_STORE_DEREF:
		bx := int(uint16(inst))
		sp--
		valToStore := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16))
		if valToStore.IsString() {
			vm.opDetachStoreDeref(valToStore)
		}
		activePtrs := vm.activeCellPtrs
		if uint(bx) < uint(len(activePtrs)) && activePtrs[bx] != nil {
			activePtrs[bx].Value = valToStore
		} else {
			cell := (*val.PyCell)(unsafe.Pointer(st.cells[bx].HeaderPtr()))
			cell.Value = valToStore
		}
		ip++
		return ip, sp, ctlNext

	case OP_INC_DEREF_INT:
		bx := int(uint16(inst))
		var cell *val.PyCell
		activePtrs := vm.activeCellPtrs
		if uint(bx) < uint(len(activePtrs)) && activePtrs[bx] != nil {
			cell = activePtrs[bx]
		} else {
			cell = (*val.PyCell)(unsafe.Pointer(st.cells[bx].HeaderPtr()))
		}
		if cell.Value.IsInt64() {
			cell.Value = val.Int64Val(cell.Value.AsInt64() + 1)
			ip += 4
			return ip, sp, ctlNext
		}
		return vm.opIncDerefSlow(st, cell, ip, sp)

	case OP_LOAD_CLOSURE:
		return vm.opLoadClosure(st, inst, ip, sp)
	case OP_IMPORT_NAME:
		return vm.opImportName(st, inst, ip, sp)
	case OP_IMPORT_FROM:
		return vm.opImportFrom(st, inst, ip, sp)
	case OP_IMPORT_STAR:
		return vm.opImportStar(st, ip, sp)
	case OP_MAKE_FUNCTION:
		return vm.opMakeFunction(st, inst, ip, sp)
	case OP_FOR_ITER_GEN:
		return vm.opForIterGen(st, inst, ip, sp)
	case OP_LIST_APPEND:
		return vm.opListAppend(st, inst, ip, sp)
	case OP_MAP_ADD:
		return vm.opMapAdd(st, inst, ip, sp)
	case OP_BUILD_SLICE:
		return vm.opBuildSlice(st, inst, ip, sp)
	case OP_BUILD_CLASS:
		return vm.opBuildClass(st, inst, ip, sp)
	case OP_BINARY_TRUE_DIV:
		return vm.opBinarySlow(st, OP_BINARY_TRUE_DIV, ip, sp)
	case OP_BINARY_FLOOR_DIV:
		return vm.opBinarySlow(st, OP_BINARY_FLOOR_DIV, ip, sp)
	case OP_BINARY_MOD:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			ai, bi := a.AsInt64(), b.AsInt64()
			if bi != 0 {
				rem := ai % bi
				if rem != 0 && (rem < 0) != (bi < 0) {
					rem += bi
				}
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(rem)
				sp--
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opBinarySlow(st, OP_BINARY_MOD, ip, sp)
	case OP_BINARY_POW:
		return vm.opBinarySlow(st, OP_BINARY_POW, ip, sp)
	case OP_UNARY_NEGATIVE:
		v := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		if v.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.Int64Val(-v.AsInt64())
			ip++
			return ip, sp, ctlNext
		}
		if v.IsFloat64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.Float64Val(-v.AsFloat64())
			ip++
			return ip, sp, ctlNext
		}
		return vm.opUnarySlow(st, OP_UNARY_NEGATIVE, ip, sp)
	case OP_UNARY_POSITIVE:
		v := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		if v.IsInt64() || v.IsFloat64() || v.IsComplex() {
			ip++
			return ip, sp, ctlNext
		}
		return vm.opUnarySlow(st, OP_UNARY_POSITIVE, ip, sp)
	case OP_UNARY_NOT:
		*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.BoolVal(!(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)).IsTruthy())
		ip++
		return ip, sp, ctlNext

	case OP_RAISE:
		return vm.opRaise(st, inst, ip, sp)
	case OP_YIELD_FROM:
		return vm.opYieldFrom(st, ip, sp)
	case OP_GET_AWAITABLE:
		return vm.opGetAwaitable(st, ip, sp)
	case OP_AWAIT:
		return vm.opAwait(st, ip, sp)
	case OP_CHECK_EXC_MATCH:
		return vm.opCheckExcMatch(st, ip, sp)
	case OP_CLEANUP_EXC:
		return vm.opCleanupExc(st, ip, sp)
	case OP_PUSH_EXC_INFO:
		return vm.opPushExcInfo(st, ip, sp)
	case OP_POP_EXCEPT:
		return vm.opPopExcept(st, ip, sp)
	case OP_FAST_INC_INT_LOCAL_CONST:
		v := locals[uint8(inst>>16)]
		c := *(*val.Value)(unsafe.Add(constsPtr, uintptr(uint16(inst))*16))
		if v.IsInt64() && c.IsInt64() {
			vi, ci := v.AsInt64(), c.AsInt64()
			sum := vi + ci
			if ((vi ^ sum) & (ci ^ sum)) >= 0 {
				locals[uint8(inst>>16)] = val.Int64Val(sum)
				ip += 4
				return ip, sp, ctlNext
			}
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload
	case OP_FAST_LOOP_CMP_LT_CONST_JUMP:
		reg := uint8(inst >> 16)
		exitTarget := uint16(inst)
		constIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
		v := locals[reg]
		c := *(*val.Value)(unsafe.Add(constsPtr, uintptr(constIdx)*16))
		if !v.IsInt64() || !c.IsInt64() {
			vm.deoptInst(st, ip, inst)
			return ip, sp, ctlReload
		}
		limit := c.AsInt64()
		cur := v.AsInt64()

		// Hot-Loop Register Optimizer:
		// If followed by OP_FAST_INC_INT_LOCAL_CONST (same reg) and back-edge OP_JUMP back to this instruction
		if ip+8 < st.codeLen {
			inst4 := *(*uint32)(unsafe.Add(codePtr, uintptr(ip+4)*4))
			inst8 := *(*uint32)(unsafe.Add(codePtr, uintptr(ip+8)*4))
			if DecodeOpcode(inst4) == OP_FAST_INC_INT_LOCAL_CONST &&
				DecodeArgA(inst4) == reg &&
				DecodeOpcode(inst8) == OP_JUMP &&
				int(DecodeArgBx(inst8)) == ip {

				stepConstIdx := DecodeArgBx(inst4)
				stepVal := *(*val.Value)(unsafe.Add(constsPtr, uintptr(stepConstIdx)*16))
				if stepVal.IsInt64() {
					step := stepVal.AsInt64()
					if step > 0 && cur < limit {
						for cur < limit {
							next := cur + step
							if next < cur {
								break
							}
							cur = next
						}
						locals[reg] = val.Int64Val(cur)
						if cur >= limit {
							ip = int(exitTarget)
							return ip, sp, ctlNext
						}
					}
				}
			}
		}

		if cur < limit {
			ip += 4
		} else {
			ip = int(exitTarget)
		}
		return ip, sp, ctlNext

	case OP_FAST_SUB_INT_LOCAL_TO_LOCAL:
		va := locals[uint8(inst>>8)]
		vb := locals[uint8(inst)]
		if va.IsInt64() && vb.IsInt64() {
			ai, bi := va.AsInt64(), vb.AsInt64()
			diff := ai - bi
			if ((ai ^ bi) & (ai ^ diff)) >= 0 {
				locals[uint8(inst>>16)] = val.Int64Val(diff)
				ip += 4
				return ip, sp, ctlNext
			}
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload
	case OP_FAST_ADD_INT_LOCALS:
		va := locals[uint8(inst>>16)]
		vb := locals[uint8(inst)]
		if va.IsInt64() && vb.IsInt64() {
			ai, bi := va.AsInt64(), vb.AsInt64()
			sum := ai + bi
			if ((ai ^ sum) & (bi ^ sum)) >= 0 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(sum)
				sp++
				ip += 3
				return ip, sp, ctlNext
			}
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload
	case OP_FAST_SUB_INT_LOCALS:
		va := locals[uint8(inst>>16)]
		vb := locals[uint8(inst)]
		if va.IsInt64() && vb.IsInt64() {
			ai, bi := va.AsInt64(), vb.AsInt64()
			diff := ai - bi
			if ((ai ^ bi) & (ai ^ diff)) >= 0 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(diff)
				sp++
				ip += 3
				return ip, sp, ctlNext
			}
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload
	case OP_FAST_CMP_EQ_JUMP_IF_FALSE:
		srcA := uint8(inst >> 16)
		exitTarget := uint16(inst)
		srcB := uint8(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)) >> 16)
		va := locals[srcA]
		vb := locals[srcB]
		if va.IsInt64() && vb.IsInt64() {
			if va.AsInt64() != vb.AsInt64() {
				ip = int(exitTarget)
				return ip, sp, ctlNext
			}
			ip += 4
			return ip, sp, ctlNext
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload

	case OP_FAST_SUBSCR_DEREF_LIST_LOCAL:
		dstReg := uint8(inst >> 16)
		cellIdx := uint8(inst >> 8)
		idxReg := uint8(inst)
		if int(cellIdx) < len(st.cells) {
			cell := (*val.PyCell)(unsafe.Pointer(st.cells[cellIdx].HeaderPtr()))
			cVal := cell.Value
			if cVal.IsList() {
				iv := locals[idxReg]
				if iv.IsInt64() {
					idx := iv.AsInt64()
					items := cVal.AsList().Items
					if idx >= 0 && idx < int64(len(items)) {
						locals[dstReg] = items[idx]
						ip += 4
						return ip, sp, ctlNext
					}
				}
			}
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload

	case OP_FAST_NEG_INT_LOCAL:
		reg := uint8(inst >> 16)
		v := locals[reg]
		if v.IsInt64() {
			locals[reg] = val.Int64Val(-v.AsInt64())
			ip += 3
			return ip, sp, ctlNext
		}
		vm.deoptInst(st, ip, inst)
		return ip, sp, ctlReload

	case OP_FORMAT_VALUE:
		return vm.opFormatValue(st, inst, ip, sp)
	case OP_BUILD_STRING:
		return vm.opBuildString(st, inst, ip, sp)
	case OP_BUILD_TEMPLATE:
		return vm.opBuildTemplate(st, inst, ip, sp)
	case OP_MATCH_SEQUENCE:
		return vm.opMatchSequence(st, ip, sp)
	case OP_MATCH_MAPPING:
		return vm.opMatchMapping(st, ip, sp)
	case OP_MATCH_KEYS:
		return vm.opMatchKeys(st, ip, sp)
	case OP_MATCH_CLASS:
		return vm.opMatchClass(st, inst, ip, sp)
	case OP_UNPACK_EX:
		return vm.opUnpackEx(st, inst, ip, sp)
	case OP_BINARY_AND:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(a.AsInt64() & b.AsInt64())
			sp--
			ip++
			return ip, sp, ctlNext
		}
		return vm.opBinarySlow(st, OP_BINARY_AND, ip, sp)
	case OP_BINARY_OR:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(a.AsInt64() | b.AsInt64())
			sp--
			ip++
			return ip, sp, ctlNext
		}
		return vm.opBinarySlow(st, OP_BINARY_OR, ip, sp)
	case OP_BINARY_XOR:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(a.AsInt64() ^ b.AsInt64())
			sp--
			ip++
			return ip, sp, ctlNext
		}
		return vm.opBinarySlow(st, OP_BINARY_XOR, ip, sp)
	case OP_BINARY_LSHIFT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			sh := b.AsInt64()
			if sh >= 0 && sh < 63 {
				va := a.AsInt64()
				res := va << uint(sh)
				if (res >> uint(sh)) == va {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(res)
					sp--
					ip++
					return ip, sp, ctlNext
				}
			}
		}
		return vm.opBinarySlow(st, OP_BINARY_LSHIFT, ip, sp)
	case OP_BINARY_RSHIFT:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			sh := b.AsInt64()
			if sh >= 0 {
				if sh >= 64 {
					if a.AsInt64() >= 0 {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(0)
					} else {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(-1)
					}
				} else {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(a.AsInt64() >> uint(sh))
				}
				sp--
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opBinarySlow(st, OP_BINARY_RSHIFT, ip, sp)
	case OP_BINARY_MATMUL:
		return vm.opBinarySlow(st, OP_BINARY_MATMUL, ip, sp)
	case OP_UNARY_INVERT:
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		if a.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.Int64Val(^a.AsInt64())
			ip++
			return ip, sp, ctlNext
		}
		return vm.opUnarySlow(st, OP_UNARY_INVERT, ip, sp)
	case OP_FAST_AND_INT_LOCAL_TO_LOCAL:
		vA := locals[uint8(inst>>8)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			locals[uint8(inst>>16)] = val.Int64Val(vA.AsInt64() & vB.AsInt64())
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_FAST_AND_INT_LOCAL_TO_LOCAL, inst, ip, sp)
	case OP_FAST_OR_INT_LOCAL_TO_LOCAL:
		vA := locals[uint8(inst>>8)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			locals[uint8(inst>>16)] = val.Int64Val(vA.AsInt64() | vB.AsInt64())
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_FAST_OR_INT_LOCAL_TO_LOCAL, inst, ip, sp)
	case OP_FAST_XOR_INT_LOCAL_TO_LOCAL:
		vA := locals[uint8(inst>>8)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			locals[uint8(inst>>16)] = val.Int64Val(vA.AsInt64() ^ vB.AsInt64())
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_FAST_XOR_INT_LOCAL_TO_LOCAL, inst, ip, sp)
	case OP_FAST_LSHIFT_INT_LOCAL_TO_LOCAL:
		vA := locals[uint8(inst>>8)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			sh := vB.AsInt64()
			if sh >= 0 && sh < 63 {
				locals[uint8(inst>>16)] = val.Int64Val(vA.AsInt64() << uint(sh))
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opFastBitSlow(st, OP_FAST_LSHIFT_INT_LOCAL_TO_LOCAL, inst, ip, sp)
	case OP_FAST_RSHIFT_INT_LOCAL_TO_LOCAL:
		vA := locals[uint8(inst>>8)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			sh := vB.AsInt64()
			if sh >= 0 && sh < 64 {
				locals[uint8(inst>>16)] = val.Int64Val(vA.AsInt64() >> uint(sh))
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opFastBitSlow(st, OP_FAST_RSHIFT_INT_LOCAL_TO_LOCAL, inst, ip, sp)
	case OP_FAST_AND_INT_LOCALS:
		vA := locals[uint8(inst>>16)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(vA.AsInt64() & vB.AsInt64())
			sp++
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_FAST_AND_INT_LOCALS, inst, ip, sp)
	case OP_FAST_OR_INT_LOCALS:
		vA := locals[uint8(inst>>16)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(vA.AsInt64() | vB.AsInt64())
			sp++
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_FAST_OR_INT_LOCALS, inst, ip, sp)
	case OP_FAST_XOR_INT_LOCALS:
		vA := locals[uint8(inst>>16)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(vA.AsInt64() ^ vB.AsInt64())
			sp++
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_FAST_XOR_INT_LOCALS, inst, ip, sp)
	case OP_FAST_LSHIFT_INT_LOCALS:
		vA := locals[uint8(inst>>16)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			sh := vB.AsInt64()
			if sh >= 0 && sh < 63 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(vA.AsInt64() << uint(sh))
				sp++
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opFastBitSlow(st, OP_FAST_LSHIFT_INT_LOCALS, inst, ip, sp)
	case OP_FAST_RSHIFT_INT_LOCALS:
		vA := locals[uint8(inst>>16)]
		vB := locals[uint8(inst)]
		if vA.IsInt64() && vB.IsInt64() {
			sh := vB.AsInt64()
			if sh >= 0 && sh < 64 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(vA.AsInt64() >> uint(sh))
				sp++
				ip++
				return ip, sp, ctlNext
			}
		}
		return vm.opFastBitSlow(st, OP_FAST_RSHIFT_INT_LOCALS, inst, ip, sp)
	case OP_FAST_SUBSCR_DEREF_LIST_LOCAL_PUSH:
		cellIdx := uint8(inst >> 16)
		localIdx := uint16(inst)
		if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
			cVal := st.cellPtrs[cellIdx].Value
			if cVal.IsList() && localIdx < 256 {
				kVal := locals[uint8(localIdx)]
				if kVal.IsInt64() {
					k := kVal.AsInt64()
					items := cVal.AsList().Items
					if uint64(k) < uint64(len(items)) {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = items[k]
						sp++
						ip++
						return ip, sp, ctlNext
					}
				}
			}
		}
		return vm.opFastSubscrPushSlow(st, inst, ip, sp)
	case OP_JUMP_IF_BITAND_ZERO:
		b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
		a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
		if a.IsInt64() && b.IsInt64() {
			sp -= 2
			if (a.AsInt64() & b.AsInt64()) == 0 {
				ip = int(uint16(inst))
				return ip, sp, ctlNext
			}
			ip++
			return ip, sp, ctlNext
		}
		return vm.opFastBitSlow(st, OP_JUMP_IF_BITAND_ZERO, inst, ip, sp)
	case OP_FAST_STREAM_WRITE_LOCAL_CONST:
		return vm.opFastStreamWriteLocalConst(st, inst, ip, sp)
	case OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE:
		localIdx := uint8(inst >> 16)
		exitTarget := int(uint16(inst))
		cellIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
		if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
			cVal := st.cellPtrs[cellIdx].Value
			lVal := locals[localIdx]
			if lVal.IsInt64() && cVal.IsInt64() {
				if lVal.AsInt64() != cVal.AsInt64() {
					ip = exitTarget
					return ip, sp, ctlNext
				}
				ip += 4
				return ip, sp, ctlNext
			}
		}
		return vm.opFastCmpEqDerefSlow(st, inst, ip, sp)
	case OP_FAST_SELF_RECURSE_INC_LOCAL:
		localReg := uint8(inst >> 16)
		cellIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
		constIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+2)*4)))
		if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil && st.frameIdx+2 <= len(vm.frames) && sp+256 <= len(st.stack) {
			fnVal := st.cellPtrs[cellIdx].Value
			if fnVal.IsFunction() {
				callee := AsCodeObject(fnVal.AsFunction().Code)
				argVal := locals[localReg]
				cVal := *(*val.Value)(unsafe.Add(constsPtr, uintptr(constIdx)*16))
				if callee == st.curCO && argVal.IsInt64() && cVal.IsInt64() {
					ai, ci := argVal.AsInt64(), cVal.AsInt64()
					sum := ai + ci
					if ((ai ^ sum) & (ci ^ sum)) >= 0 {
						saved := &vm.frames[st.frameIdx]
						saved.co = st.curCO
						saved.codePtr = codePtr
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
						saved.initInstance = val.Value{}
						saved.cmpActive = false
						st.curLocals = val.NoneVal()
						st.frameIdx++
						st.base = sp
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(sum)
						sp += st.curCO.NumLocals
						vm.activeBaseOffset = st.base
						vm.activeFrameIdx = st.frameIdx
						ip = 0
						return ip, sp, ctlReload
					}
				}
			}
		}
		return vm.opFastSelfRecurseSlow(st, inst, ip, sp)
	case OP_BUILD_SET:
		return vm.opBuildSet(st, inst, ip, sp)
	case OP_SET_ADD:
		return vm.opSetAdd(st, inst, ip, sp)
	case OP_DELETE_FAST:
		return vm.opDeleteFast(st, inst, ip, sp)
	case OP_DELETE_GLOBAL:
		return vm.opDeleteGlobal(st, inst, ip, sp)
	case OP_DELETE_NAME:
		return vm.opDeleteName(st, inst, ip, sp)
	case OP_DELETE_ATTR:
		return vm.opDeleteAttr(st, inst, ip, sp)
	case OP_DELETE_SUBSCR:
		return vm.opDeleteSubscr(st, ip, sp)
	case OP_CHECK_EXC_MATCH_STAR:
		return vm.opCheckExcMatchStar(st, ip, sp)
	case OP_BUILD_MAP_UNPACK:
		return vm.opBuildMapUnpack(st, inst, ip, sp)
	case OP_LIST_EXTEND:
		return vm.opListExtend(st, inst, ip, sp)
	case OP_LIST_TO_TUPLE:
		return vm.opListToTuple(st, ip, sp)
	case OP_LIST_TO_SET:
		return vm.opListToSet(st, ip, sp)
	default:
		panic("vm: unknown opcode")
	}
}
