package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opLoadLocals(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	stack[sp] = st.curLocals
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opLoadClosure(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	bx := int(uint16(inst))
	if bx < len(st.cellPtrs) && st.cellPtrs[bx] != nil {
		stack[sp] = val.ObjectVal(&st.cellPtrs[bx].Header)
	} else {
		stack[sp] = st.cells[bx]
	}
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opMakeFunction(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	flags := uint8(inst >> 16)
	sp--
	codeVal := stack[sp]
	var closureSlice []val.Value
	isDirectCapture := (flags & 0x10) != 0
	if (flags & 0x01) != 0 {
		nClos := int(uint16(inst))
		sp -= nClos
		closureSlice = stack[sp : sp+nClos]
	} else if isDirectCapture {
		nDirect := int(uint16(inst))
		sp -= nDirect
		closureSlice = stack[sp : sp+nDirect]
		for i := range nDirect {
			v := closureSlice[i]
			val.ClearEphemeral(v)
			val.DetachStringBuf(v)
			if vm.hasTrackedViews {
				markViewEscaped(v)
			}
		}
	}
	var kwdefaultsDict *val.PyDict
	if (flags & 0x08) != 0 {
		sp--
		kwdefVal := stack[sp]
		if kwdefVal.IsDict() {
			kwdefaultsDict = kwdefVal.AsDict()
		}
	}
	var defaultsSlice []val.Value
	if (flags & 0x02) != 0 {
		sp--
		defVal := stack[sp]
		defaultsSlice = defVal.AsTuple().Items
	}
	var annotateVal val.Value
	if (flags & 0x04) != 0 {
		sp--
		annotateVal = stack[sp]
	}
	callee := AsCodeObject(codeVal)
	var fnVal val.Value
	if isDirectCapture {
		fnVal = val.NewFunctionValWithDirectCaptures(callee.Name, codeVal, defaultsSlice, closureSlice, st.globals)
	} else {
		fnVal = val.NewFunctionValWithCellPtrs(callee.Name, codeVal, defaultsSlice, closureSlice, st.globals)
	}
	fn := fnVal.AsFunction()
	fn.KwDefaults = kwdefaultsDict
	if (flags & 0x04) != 0 {
		if fn.Dict == nil {
			fn.Dict = val.NewDictVal(4).AsDict()
		}
		fn.Dict.Set(nameAnnotate, annotateVal)
	}
	stack[sp] = fnVal
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildClass(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	argA := uint8(inst >> 16)
	var kwdsVal val.Value
	var basesVal val.Value
	var nameVal val.Value
	var classBody val.Value

	if argA == 1 {
		kwdsVal = stack[sp-1]
		basesVal = stack[sp-2]
		nameVal = stack[sp-3]
		classBody = stack[sp-4]
		sp -= 4
	} else {
		kwdsVal = val.NoneVal()
		basesVal = stack[sp-1]
		nameVal = stack[sp-2]
		classBody = stack[sp-3]
		sp -= 3
	}

	var origBases []val.Value
	switch {
	case basesVal.IsTuple():
		origBases = basesVal.AsTuple().Items
	case basesVal.IsList():
		origBases = basesVal.AsList().Items
	case !basesVal.IsNone():
		origBases = []val.Value{basesVal}
	}

	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp

	newClsVal := vm.buildClassSafe(classBody, nameVal, origBases, kwdsVal, st.globals)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = newClsVal
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opMatchSequence(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	subject := stack[sp-1]
	isSeq := (subject.IsList() || subject.IsTuple()) && !subject.IsString() && !subject.IsBytes()
	stack[sp] = val.BoolVal(isSeq)
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opMatchMapping(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	subject := stack[sp-1]
	isMap := subject.IsDict()
	stack[sp] = val.BoolVal(isMap)
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opMatchKeys(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	keysTuple := stack[sp-1]
	subject := stack[sp-2]
	sp--
	if subject.IsDict() && keysTuple.IsTuple() {
		d := subject.AsDict()
		keys := keysTuple.AsTuple().Items
		var vals []val.Value
		allFound := true
		for _, k := range keys {
			if v, ok := d.Get(k); ok {
				vals = append(vals, v)
			} else {
				allFound = false
				break
			}
		}
		if allFound {
			stack[sp-1] = val.NewTupleVal(vals...)
			stack[sp] = val.BoolVal(true)
			sp++
		} else {
			stack[sp-1] = val.NoneVal()
			stack[sp] = val.BoolVal(false)
			sp++
		}
	} else {
		stack[sp-1] = val.NoneVal()
		stack[sp] = val.BoolVal(false)
		sp++
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opMatchClass(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	nargs := int(uint8(inst >> 16))
	kwTuple := stack[sp-1]
	clsType := stack[sp-2]
	subject := stack[sp-3]
	sp -= 2
	isInst := subject.IsInstance() && clsType.IsType() && val.IsSubclass(subject.Type(), clsType.AsType())
	if isInst {
		instObj := subject.AsInstance()
		var vals []val.Value
		ok := true
		if nargs > 0 {
			if matchArgs, hasMA := clsType.AsType().Dict.Get(nameMatchArgs); hasMA && matchArgs.IsTuple() {
				maItems := matchArgs.AsTuple().Items
				for i := range nargs {
					if i < len(maItems) {
						attrName := maItems[i]
						if v, found := instObj.GetAttr(attrName); found {
							vals = append(vals, v)
						} else {
							ok = false
							break
						}
					}
				}
			}
		}
		if ok && kwTuple.IsTuple() {
			for _, kw := range kwTuple.AsTuple().Items {
				if v, found := instObj.GetAttr(kw); found {
					vals = append(vals, v)
				} else {
					ok = false
					break
				}
			}
		}
		if ok {
			stack[sp-1] = val.NewTupleVal(vals...)
			stack[sp] = val.BoolVal(true)
			sp++
		} else {
			stack[sp-1] = val.NoneVal()
			stack[sp] = val.BoolVal(false)
			sp++
		}
	} else {
		stack[sp-1] = val.NoneVal()
		stack[sp] = val.BoolVal(false)
		sp++
	}
	ip++
	return ip, sp, ctlNext
}
