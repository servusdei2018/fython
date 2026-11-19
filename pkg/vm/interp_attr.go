package vm

import (
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// dispatchPropertyGet executes a cached @property getter directly in the VM loop
// for simple Python functions, or via a zero-alloc stack-slice call for builtins.
func (vm *VM) dispatchPropertyGet(st *loopState, fget val.Value, cc *VMCodeCache, stack []val.Value, ip, sp int) (int, int, loopCtl) {
	if fget.IsBuiltinFunc() {
		st.ip = ip
		vm.execCtx.SP = sp
		res := fget.AsBuiltinFunc().Fn(&vm.execCtx, fget, stack[sp-1:sp], nil)
		stack = st.syncStackFromExecCtx(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		stack[sp-1] = res
		return ip + 1, sp, ctlReload
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
				st.ip = ip
				var okBind bool
				nextSP, okBind = vm.bindFunctionArgsSafe(callee, fgetFn.Defaults, fgetFn.KwDefaults, stack, newBase, 1, nil)
				if !okBind {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
			}
			return vm.pushCalleeFrame(st, callee, fgetFn, cc, newBase, retSP, nextSP, ip+1)
		}
	}
	st.ip = ip
	vm.execCtx.SP = sp
	res := vm.callFromHookSafe(fget, sp-1, 1, nil)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = res
	return ip + 1, sp, ctlReload
}

func (vm *VM) cacheForPropertyFGet(fget val.Value) *VMCodeCache {
	if !fget.IsFunction() {
		return nil
	}
	callee := AsCodeObject(fget.AsFunction().Code)
	if callee == nil || callee.IsGenerator || callee.IsCoroutine {
		return nil
	}
	cc := vm.cacheFor(callee)
	if len(callee.ExceptionTable) == 0 && atomic.LoadInt32(&callee.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, callee, cc)
	}
	_, _ = vm.loadWindow(callee, cc)
	return cc
}

//go:noinline
func (vm *VM) opLoadAttrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	recordLoadAttrSlow()
	stack := st.stack
	consts := st.consts
	obj := stack[sp-1]
	entry := &st.cache.ic[ip]
	var attrRes val.Value

	switch {
	case obj.IsInstance():
		instObj := obj.AsInstance()
		instType := instObj.Header.Type
		if instObj.Shape != nil && instType != nil {
			if instType.Flags&val.FlagCustomGetAttribute != 0 {
				st.ip = ip
				vm.execCtx.SP = sp
				attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])
				goto loadAttrDone
			}
			instShape := instObj.Shape
			typeVer := instType.Version()
			isMT := val.MultiThreadedFlag.Load()
			if isMT && uint(ip) < uint(len(st.cache.attrPIC)) {
				pic := &st.cache.attrPIC[ip]
				typeVer32 := uint32(typeVer)
				for k := range 4 {
					if pic[k].Shape == instShape && pic[k].TypeVer == typeVer32 {
						if v, okSlot := instObj.GetSlot(int(pic[k].SlotIndex)); okSlot && !v.IsUnbound() {
							recordAttrPICHit()
							stack[sp-1] = v
							return ip + 1, sp, ctlNext
						}
					}
				}
			}
			if entry.ExpectedTypeVer == typeVer {
				switch entry.Kind {
				case ICKindSlotMember:
					slotIdx := int(entry.SlotIndex)
					if !isMT {
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							if v := instObj.Slots[slotIdx]; !v.IsUnbound() {
								recordICSlowHit(ICKindSlotMember)
								stack[sp-1] = v
								return ip + 1, sp, ctlNext
							}
						}
					} else if v, okSlot := instObj.GetSlot(slotIdx); okSlot && !v.IsUnbound() {
						recordICSlowHit(ICKindSlotMember)
						stack[sp-1] = v
						return ip + 1, sp, ctlNext
					}
				case ICKindProperty:
					recordICSlowHit(ICKindProperty)
					return vm.dispatchPropertyGet(st, entry.CachedVal, entry.CalleeCache, stack, ip, sp)
				case ICKindNativeDescr:
					descr := entry.CachedVal
					descrType := descr.Type()
					if descrType != nil && descrType.Slots.TpDescrGet != nil {
						recordICSlowHit(ICKindNativeDescr)
						attrRes = descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
						if vm.execCtx.HasException() {
							goto loadAttrCheckExc
						}
						stack[sp-1] = attrRes
						return ip + 1, sp, ctlNext
					}
				case ICKindDataDescriptor:
					recordICSlowHit(ICKindDataDescriptor)
					st.ip = ip
					vm.execCtx.SP = sp
					attrRes = vm.descrGetSafe(entry.CachedVal, obj, instType)
					goto loadAttrDone
				case ICKindInstanceSlot:
					if instShape.ID == entry.ExpectedShapeID {
						if v, okSlot := instObj.GetSlot(int(entry.SlotIndex)); okSlot && !v.IsUnbound() {
							recordICSlowHit(ICKindInstanceSlot)
							stack[sp-1] = v
							return ip + 1, sp, ctlNext
						}
					}
				case ICKindClassAttr:
					if instShape.ID == entry.ExpectedShapeID {
						recordICSlowHit(ICKindClassAttr)
						stack[sp-1] = entry.CachedVal
						return ip + 1, sp, ctlNext
					}
				case ICKindClassFunc:
					if instShape.ID == entry.ExpectedShapeID {
						recordICSlowHit(ICKindClassFunc)
						stack[sp-1] = val.NewBoundMethodVal(obj, entry.CachedVal)
						return ip + 1, sp, ctlNext
					}
				}
			}
			st.ip = ip
			vm.execCtx.SP = sp
			attrRes = vm.loadAttrInstanceSlowSafe(instObj, obj, consts[uint16(inst)], ip, sp, st.frameIdx, st.cache.ic, st.cache.ensureAttrPIC())
			goto loadAttrDone
		}
		st.ip = ip
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])

	case obj.IsTuple():
		tp := obj.HeaderPtr().Type
		if tp == nil {
			tp = val.TupleType
		}
		if tp.Flags&val.FlagCustomGetAttribute == 0 {
			typeVer := tp.Version()
			if entry.ExpectedTypeVer == typeVer {
				switch entry.Kind {
				case ICKindTupleGetter:
					t := obj.AsTuple()
					if uint(entry.SlotIndex) < uint(len(t.Items)) {
						recordICSlowHit(ICKindTupleGetter)
						stack[sp-1] = t.Items[entry.SlotIndex]
						return ip + 1, sp, ctlNext
					}
				case ICKindProperty:
					recordICSlowHit(ICKindProperty)
					return vm.dispatchPropertyGet(st, entry.CachedVal, entry.CalleeCache, stack, ip, sp)
				case ICKindClassAttr:
					if obj.AsTuple().Attrs == nil {
						recordICSlowHit(ICKindClassAttr)
						stack[sp-1] = entry.CachedVal
						return ip + 1, sp, ctlNext
					}
				case ICKindClassFunc:
					if obj.AsTuple().Attrs == nil {
						recordICSlowHit(ICKindClassFunc)
						stack[sp-1] = val.NewBoundMethodVal(obj, entry.CachedVal)
						return ip + 1, sp, ctlNext
					}
				}
			}
			attrName := val.InternValue(consts[uint16(inst)])
			if descr, _, found := tp.LookupMRO(attrName); found {
				switch {
				case TupleGetterType != nil && descr.HeaderPtr() != nil && descr.HeaderPtr().Type == TupleGetterType:
					tg := (*pyTupleGetter)(unsafe.Pointer(descr.HeaderPtr()))
					t := obj.AsTuple()
					if tg.Index >= 0 && tg.Index < len(t.Items) {
						*entry = ICSlot{
							Kind:            ICKindTupleGetter,
							ExpectedTypeVer: typeVer,
							SlotIndex:       int32(tg.Index),
						}
						stack[sp-1] = t.Items[tg.Index]
						return ip + 1, sp, ctlNext
					}
				case descr.IsProperty():
					prop := descr.AsProperty()
					if prop.FGet.IsFunction() || prop.FGet.IsBuiltinFunc() {
						*entry = ICSlot{
							Kind:            ICKindProperty,
							ExpectedTypeVer: typeVer,
							CachedVal:       prop.FGet,
							CalleeCache:     vm.cacheForPropertyFGet(prop.FGet),
						}
						return vm.dispatchPropertyGet(st, prop.FGet, entry.CalleeCache, stack, ip, sp)
					}
				case obj.AsTuple().Attrs == nil && !descr.IsDataDescriptor():
					descrType := descr.Type()
					if descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind) {
						*entry = ICSlot{
							Kind:            ICKindClassFunc,
							ExpectedTypeVer: typeVer,
							CachedVal:       descr,
						}
						stack[sp-1] = val.NewBoundMethodVal(obj, descr)
						return ip + 1, sp, ctlNext
					}
					if descr.Kind() == val.KindStaticMethod {
						smVal := descr.AsStaticMethod().Callable
						*entry = ICSlot{
							Kind:            ICKindClassAttr,
							ExpectedTypeVer: typeVer,
							CachedVal:       smVal,
						}
						stack[sp-1] = smVal
						return ip + 1, sp, ctlNext
					}
					if descrType.Slots.TpDescrGet == nil || (descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind) {
						*entry = ICSlot{
							Kind:            ICKindClassAttr,
							ExpectedTypeVer: typeVer,
							CachedVal:       descr,
						}
						stack[sp-1] = descr
						return ip + 1, sp, ctlNext
					}
				}
			}
		}
		st.ip = ip
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])

	case obj.IsType():
		cls := obj.AsType()
		meta := cls.Header.Type
		if meta == nil {
			meta = val.TypeType
		}
		clsVer := cls.Version()
		metaVer := meta.Version()
		if entry.Kind == ICKindTypeAttr && entry.ExpectedTypeVer == clsVer && entry.ExpectedAuxVer == metaVer {
			recordICSlowHit(ICKindTypeAttr)
			stack[sp-1] = entry.CachedVal
			return ip + 1, sp, ctlNext
		}
		attrName := val.InternValue(consts[uint16(inst)])
		if meta.Flags&val.FlagCustomGetAttribute == 0 && attrName != nameDunderDoc {
			metaHasDataDescr := false
			if mDescr, _, mOK := meta.LookupMRO(attrName); mOK {
				mt := mDescr.Type()
				if mDescr.IsDataDescriptor() || (mt.Slots.TpDescrGet != nil && mt.Slots.TpDescrSet != nil) {
					metaHasDataDescr = true
				}
			}
			if !metaHasDataDescr {
				if descr, _, ok := cls.LookupMRO(attrName); ok {
					dt := descr.Type()
					switch {
					case dt.Slots.TpDescrGet == nil || descr.IsFunction() || descr.IsBuiltinFunc():
						*entry = ICSlot{
							Kind:            ICKindTypeAttr,
							ExpectedTypeVer: clsVer,
							ExpectedAuxVer:  metaVer,
							CachedVal:       descr,
						}
						stack[sp-1] = descr
						return ip + 1, sp, ctlNext
					case descr.Kind() == val.KindStaticMethod:
						smVal := descr.AsStaticMethod().Callable
						*entry = ICSlot{
							Kind:            ICKindTypeAttr,
							ExpectedTypeVer: clsVer,
							ExpectedAuxVer:  metaVer,
							CachedVal:       smVal,
						}
						stack[sp-1] = smVal
						return ip + 1, sp, ctlNext
					}
				}
			}
		}
		st.ip = ip
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, attrName)

	case obj.IsSuper():
		sup := obj.AsSuper()
		var selfType *val.PyType
		var isClassSuper bool
		if sup.Self.IsType() && val.IsSubclass(sup.Self.AsType(), sup.ThisClass) {
			selfType = sup.Self.AsType()
			isClassSuper = true
		} else {
			selfType = sup.Self.Type()
		}
		typeVer := selfType.Version()
		thisVer := sup.ThisClass.Version()
		if entry.Kind == ICKindSuperMethod && entry.ExpectedTypeVer == typeVer && entry.ExpectedAuxVer == thisVer {
			recordICSlowHit(ICKindSuperMethod)
			if entry.SlotIndex == 1 {
				stack[sp-1] = val.NewBoundMethodVal(sup.Self, entry.CachedVal)
			} else {
				stack[sp-1] = entry.CachedVal
			}
			return ip + 1, sp, ctlNext
		}
		attrName := val.InternValue(consts[uint16(inst)])
		if descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, attrName); ok {
			switch {
			case descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind):
				bind := int32(1)
				res := descr
				if isClassSuper {
					bind = 0
				} else {
					res = val.NewBoundMethodVal(sup.Self, descr)
				}
				*entry = ICSlot{
					Kind:            ICKindSuperMethod,
					ExpectedTypeVer: typeVer,
					ExpectedAuxVer:  thisVer,
					SlotIndex:       bind,
					CachedVal:       descr,
				}
				stack[sp-1] = res
				return ip + 1, sp, ctlNext
			case descr.Kind() == val.KindStaticMethod:
				smVal := descr.AsStaticMethod().Callable
				*entry = ICSlot{
					Kind:            ICKindSuperMethod,
					ExpectedTypeVer: typeVer,
					ExpectedAuxVer:  thisVer,
					SlotIndex:       0,
					CachedVal:       smVal,
				}
				stack[sp-1] = smVal
				return ip + 1, sp, ctlNext
			case descr.Type().Slots.TpDescrGet == nil || (descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind):
				*entry = ICSlot{
					Kind:            ICKindSuperMethod,
					ExpectedTypeVer: typeVer,
					ExpectedAuxVer:  thisVer,
					SlotIndex:       0,
					CachedVal:       descr,
				}
				stack[sp-1] = descr
				return ip + 1, sp, ctlNext
			}
		}
		st.ip = ip
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, attrName)

	case obj.IsModule() && obj.Type() == val.ModuleType:
		mod := obj.AsModule()
		modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
		attrName := val.InternValue(consts[uint16(inst)])
		if v, slot, okMod := mod.Dict.GetWithSlotHint(attrName, int(entry.SlotIndex)); okMod && !v.IsLazyImport() {
			*entry = ICSlot{
				Kind:            ICKindModuleAttr,
				ExpectedTypeVer: modVer,
				CachedVal:       v,
				SlotIndex:       int32(slot),
			}
			stack[sp-1] = v
			return ip + 1, sp, ctlNext
		}
		st.ip = ip
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, attrName)

	default:
		st.ip = ip
		vm.execCtx.SP = sp
		attrRes = vm.getAttrSafe(obj, consts[uint16(inst)])
	}

loadAttrDone:
	stack = st.syncStackFromExecCtx(vm, stack)
loadAttrCheckExc:
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = attrRes
	return ip + 1, sp, ctlReload
}

//go:noinline
func (vm *VM) opStoreAttrSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	recordStoreAttrSlow()
	stack := st.stack
	consts := st.consts
	v := stack[sp-2]
	obj := stack[sp-1]
	if instObj, instType, ok := vm.prepareStoreAttr(obj, v); ok {
		if instType.Flags&val.FlagCustomSetAttr != 0 {
			st.ip = ip
			vm.execCtx.SP = sp
			vm.setAttrSafe(obj, consts[uint16(inst)], v)
		} else {
			instShape := instObj.Shape
			typeVer := instType.Version()
			typeVer32 := uint32(typeVer)
			isMT := val.MultiThreadedFlag.Load()
			if uint(ip) < uint(len(st.cache.attrPIC)) {
				pic := &st.cache.attrPIC[ip]
				for k := range 4 {
					if pic[k].Shape == instShape && pic[k].TypeVer == typeVer32 {
						slotIdx := int(pic[k].SlotIndex)
						if !isMT {
							if uint(slotIdx) < uint(len(instObj.Slots)) {
								instObj.Slots[slotIdx] = v
								recordAttrPICHit()
								return ip + 1, sp - 2, ctlNext
							}
						} else if instObj.SetSlot(slotIdx, v) {
							recordAttrPICHit()
							return ip + 1, sp - 2, ctlNext
						}
					}
				}
			}
			entry := &st.cache.ic[ip]
			if entry.ExpectedTypeVer == typeVer {
				switch entry.Kind {
				case ICKindSlotMember:
					slotIdx := int(entry.SlotIndex)
					if !isMT {
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							instObj.Slots[slotIdx] = v
							recordICSlowHit(ICKindSlotMember)
							return ip + 1, sp - 2, ctlNext
						}
					} else if instObj.SetSlot(slotIdx, v) {
						recordICSlowHit(ICKindSlotMember)
						return ip + 1, sp - 2, ctlNext
					}
				case ICKindPropertySet:
					fset := entry.CachedVal
					recordICSlowHit(ICKindPropertySet)
					st.ip = ip
					vm.execCtx.SP = sp
					stack[sp-2] = obj
					stack[sp-1] = v
					if fset.IsBuiltinFunc() {
						fset.AsBuiltinFunc().Fn(&vm.execCtx, fset, stack[sp-2:sp], nil)
					} else {
						vm.callFromHookSafe(fset, sp-2, 2, nil)
					}
					goto storeAttrDone
				case ICKindNativeDescr:
					descr := entry.CachedVal
					descrType := descr.Type()
					if descrType != nil && descrType.Slots.TpDescrSet != nil {
						recordICSlowHit(ICKindNativeDescr)
						descrType.Slots.TpDescrSet(&vm.execCtx, descr, obj, v)
						if vm.execCtx.HasException() {
							goto storeAttrCheckExc
						}
						return ip + 1, sp - 2, ctlNext
					}
				case ICKindDataDescriptor:
					recordICSlowHit(ICKindDataDescriptor)
					st.ip = ip
					vm.execCtx.SP = sp
					vm.descrSetSafe(entry.CachedVal, obj, v)
					goto storeAttrDone
				case ICKindInstanceSlot:
					if instShape.ID == entry.ExpectedShapeID {
						slotIdx := int(entry.SlotIndex)
						if !isMT {
							if uint(slotIdx) < uint(len(instObj.Slots)) {
								instObj.Slots[slotIdx] = v
								recordICSlowHit(ICKindInstanceSlot)
								return ip + 1, sp - 2, ctlNext
							}
						} else if instObj.SetSlot(slotIdx, v) {
							recordICSlowHit(ICKindInstanceSlot)
							return ip + 1, sp - 2, ctlNext
						}
					}
				case ICKindInstanceTransition:
					if instShape.ID == entry.ExpectedShapeID && uint(ip) < uint(len(st.cache.transitionIC)) {
						tc := &st.cache.transitionIC[ip]
						if tc.OldShape == instShape && tc.TypeVer == typeVer && tc.NewShape != nil {
							slotIdx := int(tc.SlotIdx)
							if !isMT && instObj.ExtendBootstrapSlot(slotIdx, v) {
								instObj.Shape = tc.NewShape
							} else {
								instObj.ApplyShapeTransition(tc.NewShape, slotIdx, v)
							}
							recordICSlowHit(ICKindInstanceTransition)
							return ip + 1, sp - 2, ctlNext
						}
					}
				}
			}
			st.ip = ip
			vm.execCtx.SP = sp
			vm.storeAttrInstanceSlowSafe(instObj, obj, consts[uint16(inst)], v, ip, sp, st.frameIdx, st.cache)
		}
	} else {
		st.ip = ip
		vm.execCtx.SP = sp
		vm.setAttrSafe(obj, consts[uint16(inst)], v)
	}
storeAttrDone:
	st.syncStackFromExecCtx(vm, stack)
storeAttrCheckExc:
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	return ip + 1, sp - 2, ctlReload
}

//go:noinline
func (vm *VM) opLoadMethodSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	recordLoadMethodSlow()
	stack := st.stack
	consts := st.consts
	obj := stack[sp-1]
	entry := &st.cache.ic[ip]

	switch {
	case obj.IsInstance():
		instObj := obj.AsInstance()
		instShape := instObj.Shape
		instType := instObj.Header.Type
		if instShape != nil && instType != nil && entry.Kind == ICKindInstanceSlot &&
			instShape.ID == entry.ExpectedShapeID && instType.Version() == entry.ExpectedTypeVer {
			if v, ok := instObj.GetSlot(int(entry.SlotIndex)); ok && !v.IsUnbound() {
				recordICSlowHit(ICKindInstanceSlot)
				stack[sp-1] = val.UnboundVal()
				stack[sp] = v
				return ip + 1, sp + 1, ctlNext
			}
		}

	case obj.IsSuper():
		sup := obj.AsSuper()
		var selfType *val.PyType
		var isClassSuper bool
		if sup.Self.IsType() && val.IsSubclass(sup.Self.AsType(), sup.ThisClass) {
			selfType = sup.Self.AsType()
			isClassSuper = true
		} else {
			selfType = sup.Self.Type()
		}
		typeVer := selfType.Version()
		thisVer := sup.ThisClass.Version()
		if entry.Kind == ICKindSuperMethod && entry.ExpectedTypeVer == typeVer && entry.ExpectedAuxVer == thisVer {
			recordICSlowHit(ICKindSuperMethod)
			if entry.SlotIndex == 1 {
				stack[sp-1] = sup.Self
			} else {
				stack[sp-1] = val.UnboundVal()
			}
			stack[sp] = entry.CachedVal
			return ip + 1, sp + 1, ctlNext
		}
		attrName := val.InternValue(consts[uint16(inst)])
		if descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, attrName); ok {
			switch {
			case descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind):
				bind := int32(1)
				rec := sup.Self
				if isClassSuper {
					bind = 0
					rec = val.UnboundVal()
				}
				*entry = ICSlot{
					Kind:            ICKindSuperMethod,
					ExpectedTypeVer: typeVer,
					ExpectedAuxVer:  thisVer,
					SlotIndex:       bind,
					CachedVal:       descr,
				}
				stack[sp-1] = rec
				stack[sp] = descr
				return ip + 1, sp + 1, ctlNext
			case descr.Kind() == val.KindStaticMethod:
				smVal := descr.AsStaticMethod().Callable
				*entry = ICSlot{
					Kind:            ICKindSuperMethod,
					ExpectedTypeVer: typeVer,
					ExpectedAuxVer:  thisVer,
					SlotIndex:       0,
					CachedVal:       smVal,
				}
				stack[sp-1] = val.UnboundVal()
				stack[sp] = smVal
				return ip + 1, sp + 1, ctlNext
			}
		}

	case obj.IsType():
		cls := obj.AsType()
		meta := cls.Header.Type
		if meta == nil {
			meta = val.TypeType
		}
		clsVer := cls.Version()
		metaVer := meta.Version()
		if entry.Kind == ICKindTypeAttr && entry.ExpectedTypeVer == clsVer && entry.ExpectedAuxVer == metaVer {
			recordICSlowHit(ICKindTypeAttr)
			if entry.SlotIndex == 1 {
				stack[sp-1] = obj
			} else {
				stack[sp-1] = val.UnboundVal()
			}
			stack[sp] = entry.CachedVal
			return ip + 1, sp + 1, ctlNext
		}
		attrName := val.InternValue(consts[uint16(inst)])
		if meta.Flags&val.FlagCustomGetAttribute == 0 && attrName != nameDunderDoc {
			metaHasDataDescr := false
			if mDescr, _, mOK := meta.LookupMRO(attrName); mOK {
				mt := mDescr.Type()
				if mDescr.IsDataDescriptor() || (mt.Slots.TpDescrGet != nil && mt.Slots.TpDescrSet != nil) {
					metaHasDataDescr = true
				}
			}
			if !metaHasDataDescr {
				if descr, _, ok := cls.LookupMRO(attrName); ok {
					switch {
					case descr.IsFunction() || descr.IsBuiltinFunc():
						*entry = ICSlot{
							Kind:            ICKindTypeAttr,
							ExpectedTypeVer: clsVer,
							ExpectedAuxVer:  metaVer,
							SlotIndex:       0,
							CachedVal:       descr,
						}
						stack[sp-1] = val.UnboundVal()
						stack[sp] = descr
						return ip + 1, sp + 1, ctlNext
					case descr.Kind() == val.KindStaticMethod:
						smVal := descr.AsStaticMethod().Callable
						*entry = ICSlot{
							Kind:            ICKindTypeAttr,
							ExpectedTypeVer: clsVer,
							ExpectedAuxVer:  metaVer,
							SlotIndex:       0,
							CachedVal:       smVal,
						}
						stack[sp-1] = val.UnboundVal()
						stack[sp] = smVal
						return ip + 1, sp + 1, ctlNext
					case descr.Kind() == val.KindClassMethod:
						cmVal := descr.AsClassMethod().Callable
						*entry = ICSlot{
							Kind:            ICKindTypeAttr,
							ExpectedTypeVer: clsVer,
							ExpectedAuxVer:  metaVer,
							SlotIndex:       1,
							CachedVal:       cmVal,
						}
						stack[sp-1] = obj
						stack[sp] = cmVal
						return ip + 1, sp + 1, ctlNext
					}
				}
			}
		}

	case obj.IsModule() && obj.Type() == val.ModuleType:
		mod := obj.AsModule()
		modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
		attrName := val.InternValue(consts[uint16(inst)])
		if v, slot, ok := mod.Dict.GetWithSlotHint(attrName, int(entry.SlotIndex)); ok && !v.IsLazyImport() {
			*entry = ICSlot{
				Kind:            ICKindModuleAttr,
				ExpectedTypeVer: modVer,
				CachedVal:       v,
				SlotIndex:       int32(slot),
			}
			stack[sp-1] = val.UnboundVal()
			stack[sp] = v
			return ip + 1, sp + 1, ctlNext
		}
	}

	st.ip = ip
	vm.execCtx.SP = sp
	var methodPIC [][4]val.MethodPICEntry
	if obj.IsInstance() {
		methodPIC = st.cache.ensureMethodPIC()
	}
	rec, meth := vm.loadMethodSlowSafe(obj, consts[uint16(inst)], ip, sp, st.frameIdx, st.cache.ic, methodPIC)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = rec
	stack[sp] = meth
	return ip + 1, sp + 1, ctlReload
}

//go:noinline
func (vm *VM) opLoadGlobalSlow(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	nameVal := st.consts[uint16(inst)]
	if st.globals.Header.Type != val.DictType && st.globals.Header.Type != nil {
		if v, ok := st.globals.Get(nameVal); ok {
			stack[sp] = v
			return ip + 1, sp + 1, ctlNext
		}
		if _, _, has := st.globals.Header.Type.LookupMRO(val.DunderMissing); has {
			st.ip = ip
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
			return ip + 1, sp + 1, ctlReload
		}
		if v, ok := vm.builtins.Get(nameVal); ok {
			stack[sp] = v
			return ip + 1, sp + 1, ctlNext
		}
		st.raiseExc = val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
		return ip, sp, ctlRaise
	}
	globVer := atomic.LoadUint64(&st.globals.VersionTag)
	entry := &st.cache.ic[ip]
	if v, slot, ok := st.globals.GetWithSlotHint(nameVal, int(entry.SlotIndex)); ok {
		if v.IsLazyImport() {
			resolved, err := vm.resolveLazyImport(nil, nameVal, v)
			if err != nil {
				panic("ImportError: " + err.Error())
			}
			st.globals.Set(nameVal, resolved)
			v = resolved
		}
		*entry = ICSlot{
			Kind:            ICKindGlobal,
			ExpectedTypeVer: globVer,
			CachedVal:       v,
			SlotIndex:       int32(slot),
		}
		stack[sp] = v
		return ip + 1, sp + 1, ctlNext
	}
	if v, ok := vm.builtins.Get(nameVal); ok {
		*entry = ICSlot{
			Kind:            ICKindBuiltin,
			ExpectedTypeVer: globVer,
			ExpectedAuxVer:  atomic.LoadUint64(&vm.builtins.VersionTag),
			CachedVal:       v,
		}
		stack[sp] = v
		return ip + 1, sp + 1, ctlNext
	}
	st.raiseExc = val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
	return ip, sp, ctlRaise
}
