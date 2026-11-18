package vm

import (
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opLoadAttrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	consts := st.consts
	obj := stack[sp-1]
	var attrRes val.Value
	instObj := obj.AsInstanceChecked()
	var instType *val.PyType
	ok := false
	if instObj != nil {
		instType = instObj.Header.Type
		ok = instObj.Shape != nil && instType != nil
	}
	switch {
	case ok:
		if instType.Flags&val.FlagCustomGetAttribute != 0 {
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = sp
			attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])
		} else {
			instShape := instObj.Shape
			pic := &st.cache.attrPIC[ip]
			isMT := val.MultiThreadedFlag.Load()
			if isMT {
				for k := range 4 {
					if pic[k].Shape == instShape {
						if v, okSlot := instObj.GetSlot(pic[k].SlotIndex); okSlot && !v.IsUnbound() {
							stack[sp-1] = v
							ip++
							return ip, sp, ctlNext
						}
					}
				}
			}
			entry := st.cache.ic[ip]
			if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer {
				if entry.Kind == ICKindInstanceSlot && isMT {
					if v, okSlot := instObj.GetSlot(entry.SlotIndex); okSlot && !v.IsUnbound() {
						stack[sp-1] = v
						ip++
						return ip, sp, ctlNext
					}
				}
				if entry.Kind == ICKindDataDescriptor {
					descr := entry.CachedVal
					descrType := descr.Type()
					if descrType != nil && descrType.Name == "struct_field_descriptor" && descrType.Slots.TpDescrGet != nil {
						stack[sp-1] = descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
						ip++
						return ip, sp, ctlNext
					}
					vm.syncActiveFrame(st, ip)
					vm.execCtx.SP = sp
					attrRes = vm.descrGetSafe(entry.CachedVal, obj, instType)
					goto loadAttrDone
				}
				if entry.Kind == ICKindProperty {
					fget := entry.CachedVal
					if fget.IsBuiltinFunc() {
						vm.syncActiveFrame(st, ip)
						vm.execCtx.SP = sp
						attrRes = fget.AsBuiltinFunc().Fn(&vm.execCtx, fget, stack[sp-1:sp], nil)
						goto loadAttrDone
					}
					if fget.IsFunction() {
						fgetFn := fget.AsFunction()
						callee := AsCodeObject(fgetFn.Code)
						if !callee.IsGenerator && !callee.IsCoroutine {
							newBase := sp - 1
							retSP := sp - 1
							minCap := max(newBase+256, newBase+callee.NumLocals+64)
							if minCap > len(stack) {
								stack = vm.growStack(st, stack, minCap)
							}
							var nextSP int
							if callee.NumParams == 1 && len(fgetFn.Defaults) == 0 && fgetFn.KwDefaults == nil && callee.KWOnlyArgCount == 0 && (callee.Flags&(CO_VARARGS|CO_VARKEYWORDS)) == 0 && len(callee.CellVars) == 0 {
								nextSP = newBase + max(callee.NumLocals, 1)
							} else {
								vm.syncActiveFrame(st, ip)
								var okBind bool
								nextSP, okBind = vm.bindFunctionArgsSafe(callee, fgetFn.Defaults, fgetFn.KwDefaults, stack, newBase, 1, nil)
								if !okBind {
									exc := vm.execCtx.ActiveException
									vm.execCtx.ClearException()
									st.raiseExc = exc
									return ip, sp, ctlRaise
								}
							}
							if st.frameIdx+2 > len(vm.frames) {
								vm.ensureFrameCapacity(st.frameIdx + 2)
							}
							saved := &vm.frames[st.frameIdx]
							saved.co = st.curCO
							saved.codePtr = st.codePtr
							saved.codeLen = st.codeLen
							saved.consts = st.consts
							saved.ip = ip + 1
							saved.baseOffset = st.base
							saved.retSP = retSP
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
							st.curCO = callee
							st.cache = vm.cacheFor(callee)
							st.reloadWindow(vm, callee)
							st.consts = callee.Consts
							st.base = newBase
							switch {
							case len(callee.CellVars) == 0 && fgetFn.Closure != nil:
								st.cells = fgetFn.Closure
								st.cellPtrs = fgetFn.CellPtrs
							case len(callee.CellVars) == 0 && len(callee.FreeVars) == 0:
								st.cells = nil
								st.cellPtrs = nil
							default:
								st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(callee, fget, stack, newBase, 1)
							}
							if fgetFn.Globals != nil {
								st.globals = fgetFn.Globals
							}
							vm.activeCO = st.curCO
							vm.activeBaseOffset = st.base
							if vm.activeCellPtrs != nil || st.cellPtrs != nil {
								vm.activeCells = st.cells
								vm.activeCellPtrs = st.cellPtrs
							}
							vm.activeFrameIdx = st.frameIdx
							return 0, nextSP, ctlReload
						}
					}
					vm.syncActiveFrame(st, ip)
					vm.execCtx.SP = sp
					attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])
					goto loadAttrDone
				}
			}
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = sp
			attrRes = vm.loadAttrInstanceSlowSafe(instObj, obj, consts[uint16(inst)], ip, sp, st.frameIdx, st.cache.ic, st.cache.attrPIC)
		}
	case obj.IsModule() && obj.Type() == val.ModuleType:
		mod := obj.AsModule()
		entry := &st.cache.ic[ip]
		modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
		attrName := consts[uint16(inst)]
		if v, slot, okMod := mod.Dict.GetWithSlotHint(attrName, entry.SlotIndex); okMod {
			entry.Kind = ICKindModuleAttr
			entry.ExpectedGlobalsVer = modVer
			entry.CachedVal = v
			entry.SlotIndex = slot
			stack[sp-1] = v
			ip++
			return ip, sp, ctlNext
		}
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, attrName)
	default:
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])
	}
loadAttrDone:
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = attrRes
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opStoreAttrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	consts := st.consts
	v := stack[sp-2]
	obj := stack[sp-1]
	if instObj, instType, ok := vm.prepareStoreAttr(obj, v); ok {
		if instType.Flags&val.FlagCustomSetAttr != 0 {
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = sp
			vm.setAttrSafe(obj, consts[uint16(inst)], v)
		} else {
			instShape := instObj.Shape
			pic := &st.cache.attrPIC[ip]
			isMT := val.MultiThreadedFlag.Load()
			for k := range 4 {
				if pic[k].Shape == instShape {
					slotIdx := pic[k].SlotIndex
					if !isMT {
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							instObj.Slots[slotIdx] = v
							sp -= 2
							ip++
							return ip, sp, ctlNext
						}
					} else if instObj.SetSlot(slotIdx, v) {
						sp -= 2
						ip++
						return ip, sp, ctlNext
					}
				}
			}
			entry := st.cache.ic[ip]
			if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer {
				if entry.Kind == ICKindInstanceSlot {
					slotIdx := entry.SlotIndex
					if !isMT {
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							instObj.Slots[slotIdx] = v
							sp -= 2
							ip++
							return ip, sp, ctlNext
						}
					} else if instObj.SetSlot(slotIdx, v) {
						sp -= 2
						ip++
						return ip, sp, ctlNext
					}
				}
				if entry.Kind == ICKindDataDescriptor {
					descr := entry.CachedVal
					descrType := descr.Type()
					if descrType != nil && descrType.Name == "struct_field_descriptor" && descrType.Slots.TpDescrSet != nil {
						descrType.Slots.TpDescrSet(&vm.execCtx, descr, obj, v)
						sp -= 2
						ip++
						return ip, sp, ctlNext
					}
					vm.syncActiveFrame(st, ip)
					vm.execCtx.SP = sp
					vm.descrSetSafe(entry.CachedVal, obj, v)
					goto storeAttrDone
				}
			}
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = sp
			vm.storeAttrInstanceSlowSafe(instObj, obj, consts[uint16(inst)], v, ip, sp, st.frameIdx, st.cache.ic, st.cache.attrPIC)
		}
	} else {
		vm.syncActiveFrame(st, ip)
		vm.execCtx.SP = sp
		vm.setAttrSafe(obj, consts[uint16(inst)], v)
	}
storeAttrDone:
	st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	sp -= 2
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opLoadMethodSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	consts := st.consts
	obj := stack[sp-1]
	if obj.IsInstance() {
		instObj := obj.AsInstance()
		instShape := instObj.Shape
		instType := instObj.Header.Type
		entry := st.cache.ic[ip]
		if instShape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer {
			if entry.Kind == ICKindInstanceSlot {
				if v, ok := instObj.GetSlot(entry.SlotIndex); ok {
					stack[sp-1] = val.UnboundVal()
					stack[sp] = v
					sp++
					ip++
					return ip, sp, ctlNext
				}
			}
		}
	} else if obj.IsModule() && obj.Type() == val.ModuleType {
		mod := obj.AsModule()
		entry := &st.cache.ic[ip]
		modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
		attrName := consts[uint16(inst)]
		if v, slot, ok := mod.Dict.GetWithSlotHint(attrName, entry.SlotIndex); ok {
			entry.Kind = ICKindModuleAttr
			entry.ExpectedGlobalsVer = modVer
			entry.CachedVal = v
			entry.SlotIndex = slot
			stack[sp-1] = val.UnboundVal()
			stack[sp] = v
			sp++
			ip++
			return ip, sp, ctlNext
		}
	}
	vm.syncActiveFrame(st, ip)
	vm.execCtx.SP = sp
	rec, meth := vm.loadMethodSlowSafe(obj, consts[uint16(inst)], ip, sp, st.frameIdx, st.cache.ic, st.cache.methodPIC)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = rec
	stack[sp] = meth
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opLoadGlobalSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	nameVal := st.consts[uint16(inst)]
	if st.globals.Header.Type != val.DictType && st.globals.Header.Type != nil {
		if v, ok := st.globals.Get(nameVal); ok {
			stack[sp] = v
			sp++
			ip++
			return ip, sp, ctlNext
		}
		if _, _, has := st.globals.Header.Type.LookupMRO(val.DunderMissing); has {
			vm.syncActiveFrame(st, ip)
			vm.execCtx.SP = sp
			res := vm.getItemSafe(val.ObjectVal(&st.globals.Header), nameVal)
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
		if v, ok := vm.builtins.Get(nameVal); ok {
			stack[sp] = v
			sp++
			ip++
			return ip, sp, ctlNext
		}
		st.raiseExc = val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
		return ip, sp, ctlRaise
	}
	globVer := atomic.LoadUint64(&st.globals.VersionTag)
	entry := &st.cache.ic[ip]
	if v, slot, ok := st.globals.GetWithSlotHint(nameVal, entry.SlotIndex); ok {
		if v.IsLazyImport() {
			resolved, err := vm.resolveLazyImport(nil, nameVal, v)
			if err != nil {
				panic("ImportError: " + err.Error())
			}
			st.globals.Set(nameVal, resolved)
			v = resolved
		}
		entry.Kind = ICKindGlobal
		entry.ExpectedGlobalsVer = globVer
		entry.CachedVal = v
		entry.SlotIndex = slot
		stack[sp] = v
		sp++
		ip++
		return ip, sp, ctlNext
	}
	if v, ok := vm.builtins.Get(nameVal); ok {
		entry.Kind = ICKindBuiltin
		entry.ExpectedGlobalsVer = globVer
		entry.ExpectedBuiltinsVer = atomic.LoadUint64(&vm.builtins.VersionTag)
		entry.CachedVal = v
		stack[sp] = v
		sp++
		ip++
		return ip, sp, ctlNext
	}
	st.raiseExc = val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
	return ip, sp, ctlRaise
}
