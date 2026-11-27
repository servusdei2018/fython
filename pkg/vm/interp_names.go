package vm

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opStoreFastView(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	bx := uint16(inst)
	sp--
	valToStore := stack[sp]
	if vm.hasTrackedViews {
		key := viewSlotKey(st.frameIdx, int(bx))
		if bx < 256 {
			vm.storeFastValue(locals[uint8(bx)], valToStore, key)
		} else {
			vm.storeFastValue(stack[st.base+int(bx)], valToStore, key)
		}
	}
	if bx < 256 {
		locals[uint8(bx)] = valToStore
	} else {
		stack[st.base+int(bx)] = valToStore
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opPrintExpr(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	v := stack[sp-1]
	sp--
	if !v.IsNone() && v.HeaderPtr() != nil {
		st.stack = stack
		st.ip = ip
		vm.execCtx.SP = sp
		if hook, ok := vm.getDisplayhook(); ok {
			_, panicValue := vm.invokeDisplayhook(hook, v)
			st.syncStackFromExecCtx(vm, stack)
			if panicValue != nil {
				exc := panicToPyException(panicValue)
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		} else {
			vm.defaultPrintExpr(v)
		}
	}
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opLoadFromDictOr(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	// PEP 649: class-scope-aware name lookup used by the compiler
	// synthesized __annotate__ functions.
	nameVal := st.consts[uint16(inst)]
	sp--
	mapping := stack[sp]
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	res, found := vm.mappingGetOptional(mapping, nameVal)
	if !found && !vm.execCtx.HasException() {
		if opcodeOf(inst) == OP_LOAD_FROM_DICT_OR_GLOBALS {
			res, found = vm.globalOrBuiltin(st.globals, nameVal)
		} else {
			idx := int(uint8(inst >> 16))
			var cell *val.PyCell
			if idx < len(st.cellPtrs) && st.cellPtrs[idx] != nil {
				cell = st.cellPtrs[idx]
			} else {
				cell = (*val.PyCell)(unsafe.Pointer(st.cells[idx].HeaderPtr()))
			}
			res, found = cell.Value, true
		}
	}
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() || !found {
		var exc val.Value
		if vm.execCtx.HasException() {
			exc = vm.execCtx.ActiveException
			vm.execCtx.ClearException()
		} else {
			exc = val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
		}
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = res
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opStoreGlobal(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := uint16(inst)
	nameVal := st.consts[bx]
	sp--
	valToStore := stack[sp]
	val.ClearEphemeral(valToStore)
	val.DetachStringBuf(valToStore)
	markViewEscaped(valToStore)
	st.globals.Set(nameVal, valToStore)
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opLoadName(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := uint16(inst)
	nameVal := st.consts[bx]
	found := false
	var res val.Value

	if !st.curLocals.IsNone() && st.curLocals.HeaderPtr() != nil {
		if st.curLocals.Type() == val.DictType {
			if v, ok := st.curLocals.AsDict().Get(nameVal); ok {
				res = v
				found = true
			}
		} else {
			st.stack = stack
			st.ip = ip
			vm.execCtx.SP = sp
			v := val.GetItemCtx(&vm.execCtx, st.curLocals, nameVal)
			if vm.execCtx.HasException() {
				if val.IsInstance(vm.execCtx.ActiveException, val.KeyErrorType) {
					vm.execCtx.ClearException()
				} else {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					st.raiseExc = exc
					return ip, sp, ctlRaise
				}
			} else {
				res = v
				found = true
			}
		}
	}

	if !found && st.globals != nil {
		if v, ok := st.globals.Get(nameVal); ok {
			res = v
			found = true
		}
	}

	if !found && vm.builtins != nil {
		if v, ok := vm.builtins.Get(nameVal); ok {
			res = v
			found = true
		}
	}

	if !found {
		exc := val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}

	if res.IsLazyImport() {
		resolved, err := vm.resolveLazyImport(nil, nameVal, res)
		if err != nil {
			panic("ImportError: " + err.Error())
		}
		if !st.curLocals.IsNone() && st.curLocals.Type() == val.DictType {
			st.curLocals.AsDict().Set(nameVal, resolved)
		} else if st.globals != nil {
			st.globals.Set(nameVal, resolved)
		}
		res = resolved
	}

	stack[sp] = res
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opStoreName(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := uint16(inst)
	nameVal := st.consts[bx]
	sp--
	valToStore := stack[sp]
	val.ClearEphemeral(valToStore)
	val.DetachStringBuf(valToStore)
	markViewEscaped(valToStore)

	if !st.curLocals.IsNone() && st.curLocals.HeaderPtr() != nil {
		if st.curLocals.Type() == val.DictType {
			st.curLocals.AsDict().Set(nameVal, valToStore)
		} else {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = st.frameIdx
			vm.activeCO = st.curCO
			vm.activeBaseOffset = st.base
			vm.activeCells = st.cells
			val.SetItemCtx(&vm.execCtx, st.curLocals, nameVal, valToStore)
		}
	} else if st.globals != nil {
		st.globals.Set(nameVal, valToStore)
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opFastStreamWriteLocalConst(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	localIdx := uint8(inst >> 16)
	constIdx := uint16(inst)
	receiver := locals[localIdx]
	arg := st.consts[constIdx]
	if receiver.IsInstance() {
		switch receiver.Type() {
		case StringIOType:
			sio := (*pyStringIO)(val.AsPointer(receiver))
			if !sio.isClosed && sio.asciiOnly && sio.newlineMode == sioNewlineLF && sio.pos == len(sio.buf) && arg.IsAnyString() {
				if arg.Kind() == val.KindString {
					data := arg.AsStringObj().Data
					if isASCIIString(data) {
						if cap(sio.buf)-len(sio.buf) < len(data) {
							newCap := max(cap(sio.buf)*2, 131072)
							newBuf := make([]byte, len(sio.buf), newCap)
							copy(newBuf, sio.buf)
							sio.buf = newBuf
						}
						sio.buf = append(sio.buf, data...)
						sio.pos = len(sio.buf)
						sio.bytePos = len(sio.buf)
						if ip+4 < st.codeLen && DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))) == OP_JUMP {
							ip = int(DecodeArgBx(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))))
							return ip, sp, ctlReload
						}
						ip += 4
						return ip, sp, ctlReload
					}
				} else {
					appendStringIOWrite(sio, arg)
					if ip+4 < st.codeLen && DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))) == OP_JUMP {
						ip = int(DecodeArgBx(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))))
						return ip, sp, ctlReload
					}
					ip += 4
					return ip, sp, ctlReload
				}
			}
		case FileType:
			pf := (*pyFile)(val.AsPointer(receiver))
			if !pf.isClosed && !pf.binary {
				if arg.Kind() == val.KindString {
					data := arg.AsStringObj().Data
					pf.writeBuf = append(pf.writeBuf, data...)
					if len(pf.writeBuf) >= 32768 && pf.f != nil {
						_, _ = pf.f.Write(pf.writeBuf)
						pf.writeBuf = pf.writeBuf[:0]
					}
					if ip+4 < st.codeLen && DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))) == OP_JUMP {
						ip = int(DecodeArgBx(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))))
						return ip, sp, ctlReload
					}
					ip += 4
					return ip, sp, ctlReload
				}
				pf.writeBuf, _ = val.AppendValueBytes(pf.writeBuf, arg)
				if len(pf.writeBuf) >= 32768 && pf.f != nil {
					_, _ = pf.f.Write(pf.writeBuf)
					pf.writeBuf = pf.writeBuf[:0]
				}
				if ip+4 < st.codeLen && DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))) == OP_JUMP {
					ip = int(DecodeArgBx(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+4)*4))))
					return ip, sp, ctlReload
				}
				ip += 4
				return ip, sp, ctlReload
			}
		}
	}
	st.stack = stack
	st.ip = ip
	methVal := vm.getAttrSafe(receiver, nameWrite)
	if !methVal.IsNone() {
		vm.Vectorcall(methVal, []val.Value{arg}, nil)
	}
	st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	ip += 4
	return ip, sp, ctlReload

}

//go:noinline
func (vm *VM) opDeleteFast(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
	reg := uint16(inst)
	if reg < 256 {
		if vm.hasTrackedViews {
			vm.reclaimViewSlot(locals[uint8(reg)], val.UnboundVal())
		}
		locals[uint8(reg)] = val.UnboundVal()
	} else {
		if vm.hasTrackedViews {
			vm.reclaimViewSlot(stack[st.base+int(reg)], val.UnboundVal())
		}
		stack[st.base+int(reg)] = val.UnboundVal()
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opDeleteGlobal(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	bx := uint16(inst)
	nameVal := st.consts[bx]
	st.globals.Delete(nameVal)
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opDeleteName(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	bx := uint16(inst)
	nameVal := st.consts[bx]
	deleted := false
	if !st.curLocals.IsNone() && st.curLocals.HeaderPtr() != nil {
		if st.curLocals.IsDict() {
			deleted = st.curLocals.AsDict().Delete(nameVal)
		} else {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = st.frameIdx
			vm.activeCO = st.curCO
			vm.activeBaseOffset = st.base
			vm.activeCells = st.cells
			if delItemFn, _, ok := st.curLocals.Type().LookupMRO(nameDelitem); ok {
				vm.Vectorcall(delItemFn, []val.Value{st.curLocals, nameVal}, nil)
				deleted = true
			}
		}
	} else if st.globals != nil {
		deleted = st.globals.Delete(nameVal)
	}
	if !deleted {
		exc := val.NewNameError("name '" + nameVal.AsString() + "' is not defined")
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opDeleteAttr(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := uint16(inst)
	attrName := st.consts[bx]
	sp--
	obj := stack[sp]
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	vm.delAttrSafe(obj, attrName)
	st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	ip++
	return ip, sp, ctlReload
}
