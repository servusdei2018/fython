package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

// Run executes the top-level bytecode slice with the given constant pool and local variable count.
// Hot loop state (code, consts, ip, baseOffset, sp, stack) is kept in local variables so the Go
// compiler allocates them to CPU registers across loop iterations.
func (vm *VM) Run(code []uint32, consts []val.Value, numLocals int) val.Value {
	return vm.runLoop(code, consts, 0, numLocals, 0)
}

// RunCode executes a *CodeObject on the VM.
func (vm *VM) RunCode(co *CodeObject) val.Value {
	return vm.runLoop(co.Code, co.Consts, 0, co.NumLocals, 0)
}

// Vectorcall invokes a Python or native callable using a contiguous argument slice and optional keyword names.
func (vm *VM) Vectorcall(callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	base := vm.execCtx.SP
	copy(vm.stack[base:base+len(args)], args)
	return vm.callFromHook(&vm.execCtx, callable, base, len(args), kwnames)
}

// callFromHook executes a callable whose arguments are already placed on vm.stack[argBase : argBase+nargs].
func (vm *VM) callFromHook(ctx *val.ExecContext, callable val.Value, argBase int, nargs int, kwnames []val.Value) val.Value {
	switch callable.Kind() {
	case val.KindBuiltinFunc:
		savedSP := ctx.SP
		ctx.SP = argBase + nargs
		res := callable.AsBuiltinFunc().Fn(ctx, callable, vm.stack[argBase:argBase+nargs], kwnames)
		ctx.SP = savedSP
		return res

	case val.KindFunction:
		fn := callable.AsFunction()
		co := AsCodeObject(fn.Code)
		spAfterBind := vm.bindFunctionArgs(co, fn.Defaults, fn.KwDefaults, argBase, nargs, kwnames)
		savedSP := ctx.SP
		res := vm.runLoop(co.Code, co.Consts, argBase, spAfterBind, vm.activeFrameIdx)
		ctx.SP = savedSP
		return res

	case val.KindCode:
		co := AsCodeObject(callable)
		spAfterBind := vm.bindFunctionArgs(co, nil, nil, argBase, nargs, kwnames)
		savedSP := ctx.SP
		res := vm.runLoop(co.Code, co.Consts, argBase, spAfterBind, vm.activeFrameIdx)
		ctx.SP = savedSP
		return res

	case val.KindBoundMethod:
		bm := callable.AsBoundMethod()
		copy(vm.stack[argBase+1:argBase+1+nargs], vm.stack[argBase:argBase+nargs])
		vm.stack[argBase] = bm.Self
		return vm.callFromHook(ctx, bm.Func, argBase, nargs+1, kwnames)

	default:
		if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
			savedSP := ctx.SP
			ctx.SP = argBase + nargs
			res := tpCall(ctx, callable, vm.stack[argBase:argBase+nargs], kwnames)
			ctx.SP = savedSP
			return res
		}
		panic("vm: TypeError: '" + callable.Type().Name + "' object is not callable")
	}
}

// bindFunctionArgs binds positional, keyword, and default arguments in-place on vm.stack[argBase:]
// with 0 heap allocations, returning the initial sp for the callee frame.
func (vm *VM) bindFunctionArgs(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, argBase int, nargs int, kwnames []val.Value) int {
	numParams := co.NumParams
	numLocals := co.NumLocals
	if numLocals < numParams {
		numLocals = numParams
	}

	if len(kwnames) == 0 {
		if nargs < numParams && len(defaults) > 0 {
			defStart := numParams - len(defaults)
			for i := nargs; i < numParams; i++ {
				if i >= defStart {
					vm.stack[argBase+i] = defaults[i-defStart]
				}
			}
		}
		if numLocals < nargs {
			numLocals = nargs
		}
		return argBase + numLocals
	}

	kwCount := len(kwnames)
	posCount := nargs - kwCount
	if posCount < 0 {
		panic("vm: TypeError: invalid keyword argument count")
	}

	// Copy keyword argument values into VM scratch buffer before rearranging in-place.
	copy(vm.kwScratch[:kwCount], vm.stack[argBase+posCount:argBase+nargs])
	for i := posCount; i < numParams; i++ {
		vm.kwSet[i] = false
	}

	paramNames := co.ParamNames
	for i := 0; i < kwCount; i++ {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := 0; p < len(paramNames); p++ {
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		if foundIdx < 0 {
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
		if foundIdx < posCount || vm.kwSet[foundIdx] {
			panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
		}
		vm.stack[argBase+foundIdx] = kwVal
		vm.kwSet[foundIdx] = true
	}

	defStart := numParams - len(defaults)
	for i := posCount; i < numParams; i++ {
		if !vm.kwSet[i] {
			if i >= defStart && len(defaults) > 0 {
				vm.stack[argBase+i] = defaults[i-defStart]
			} else if kwDefaults != nil && i < len(paramNames) {
				if v, ok := kwDefaults.Get(paramNames[i]); ok {
					vm.stack[argBase+i] = v
				} else {
					panic("vm: TypeError: missing required argument")
				}
			} else {
				panic("vm: TypeError: missing required argument")
			}
		}
	}

	return argBase + numLocals
}

func (vm *VM) runLoop(code []uint32, consts []val.Value, startBase int, startSP int, startFrameIdx int) val.Value {
	stack := vm.stack

	frameIdx := startFrameIdx
	baseOffset := startBase
	sp := startSP
	ip := 0

	for {
		inst := code[ip]
		op := Opcode(inst >> 24)

		switch op {
		case OP_LOAD_CONST:
			bx := uint16(inst)
			stack[sp] = consts[bx]
			sp++
			ip++

		case OP_LOAD_FAST:
			bx := int(uint16(inst))
			stack[sp] = stack[baseOffset+bx]
			sp++
			ip++

		case OP_STORE_FAST:
			bx := int(uint16(inst))
			sp--
			stack[baseOffset+bx] = stack[sp]
			ip++

		case OP_BINARY_ADD:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_BINARY_ADD_INT
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD_INT) << 24)
				ai := a.AsInt64()
				bi := b.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					stack[sp-2] = val.Int64Val(sum)
				} else {
					stack[sp-2] = val.Add(a, b)
				}
			} else if a.IsFloat64() && b.IsFloat64() {
				// Quickening: specialize in-place to OP_BINARY_ADD_FLOAT
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD_FLOAT) << 24)
				stack[sp-2] = val.Float64Val(a.AsFloat64() + b.AsFloat64())
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.AddCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_ADD_INT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				ai := a.AsInt64()
				bi := b.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					stack[sp-2] = val.Int64Val(sum)
				} else {
					stack[sp-2] = val.Add(a, b)
				}
			} else {
				// Type stability broken: deoptimize in-place back to OP_BINARY_ADD
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD) << 24)
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.AddCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_ADD_FLOAT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsFloat64() && b.IsFloat64() {
				stack[sp-2] = val.Float64Val(a.AsFloat64() + b.AsFloat64())
			} else {
				// Type stability broken: deoptimize in-place back to OP_BINARY_ADD
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD) << 24)
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.AddCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_SUB:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_BINARY_SUB_INT
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_BINARY_SUB_INT) << 24)
				ai := a.AsInt64()
				bi := b.AsInt64()
				diff := ai - bi
				if ((ai ^ bi) & (ai ^ diff)) >= 0 {
					stack[sp-2] = val.Int64Val(diff)
				} else {
					stack[sp-2] = val.Sub(a, b)
				}
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.SubCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_SUB_INT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				ai := a.AsInt64()
				bi := b.AsInt64()
				diff := ai - bi
				if ((ai ^ bi) & (ai ^ diff)) >= 0 {
					stack[sp-2] = val.Int64Val(diff)
				} else {
					stack[sp-2] = val.Sub(a, b)
				}
			} else {
				// Type stability broken: deoptimize in-place back to OP_BINARY_SUB
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_BINARY_SUB) << 24)
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.SubCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_MUL:
			b := stack[sp-1]
			a := stack[sp-2]
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			stack[sp-2] = val.MulCtx(&vm.execCtx, a, b)
			sp--
			ip++

		case OP_BINARY_SUBSCR:
			key := stack[sp-1]
			obj := stack[sp-2]
			if obj.IsList() && key.IsInt64() {
				items := obj.AsList().Items
				idx := int(key.AsInt64())
				if idx < 0 {
					idx += len(items)
				}
				stack[sp-2] = items[idx]
			} else if obj.IsDict() {
				v, ok := obj.AsDict().Get(key)
				if !ok {
					panic("vm: KeyError")
				}
				stack[sp-2] = v
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.GetItemCtx(&vm.execCtx, obj, key)
			}
			sp--
			ip++

		case OP_STORE_SUBSCR:
			valToStore := stack[sp-1]
			key := stack[sp-2]
			obj := stack[sp-3]
			if obj.IsList() && key.IsInt64() {
				items := obj.AsList().Items
				idx := int(key.AsInt64())
				if idx < 0 {
					idx += len(items)
				}
				items[idx] = valToStore
			} else if obj.IsDict() {
				obj.AsDict().Set(key, valToStore)
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				val.SetItemCtx(&vm.execCtx, obj, key, valToStore)
			}
			sp -= 3
			ip++

		case OP_COMPARE_LT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_COMPARE_LT_INT
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_COMPARE_LT_INT) << 24)
				stack[sp-2] = val.BoolVal(a.AsInt64() < b.AsInt64())
			} else {
				stack[sp-2] = val.LessThan(a, b)
			}
			sp--
			ip++

		case OP_COMPARE_LT_INT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				stack[sp-2] = val.BoolVal(a.AsInt64() < b.AsInt64())
			} else {
				// Type stability broken: deoptimize in-place back to OP_COMPARE_LT
				code[ip] = (inst & 0x00FFFFFF) | (uint32(OP_COMPARE_LT) << 24)
				stack[sp-2] = val.LessThan(a, b)
			}
			sp--
			ip++

		case OP_JUMP_IF_FALSE:
			sp--
			cond := stack[sp]
			if !cond.AsBool() {
				ip = int(uint16(inst))
			} else {
				ip++
			}

		case OP_JUMP:
			ip = int(uint16(inst))

		case OP_CALL:
			argc := int(uint8(inst >> 16))
			bx := int(uint16(inst))

			var target val.Value
			var newBase int
			var retSP int

			if bx < len(consts) && isCallableConst(consts[bx]) {
				target = consts[bx]
				newBase = sp - argc
				retSP = newBase
			} else {
				target = stack[sp-argc-1]
				newBase = sp - argc
				retSP = sp - argc - 1
			}

			switch target.Kind() {
			case val.KindCode:
				callee := AsCodeObject(target)
				vm.frames[frameIdx] = CallFrame{
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
				}
				frameIdx++
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				numLocalsCallee := callee.NumLocals
				if numLocalsCallee < argc {
					numLocalsCallee = argc
				}
				sp = newBase + numLocalsCallee
				ip = 0

			case val.KindFunction:
				fn := target.AsFunction()
				callee := AsCodeObject(fn.Code)
				vm.frames[frameIdx] = CallFrame{
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
				}
				frameIdx++
				sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, newBase, argc, nil)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				ip = 0

			case val.KindBuiltinFunc:
				builtin := target.AsBuiltinFunc()
				vm.execCtx.SP = newBase + argc
				vm.activeFrameIdx = frameIdx
				res := builtin.Fn(&vm.execCtx, target, stack[newBase:newBase+argc], nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++

			default:
				vm.execCtx.SP = newBase + argc
				vm.activeFrameIdx = frameIdx
				res := vm.callFromHook(&vm.execCtx, target, newBase, argc, nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++
			}

		case OP_CALL_KW:
			totalArgs := int(uint8(inst >> 16))
			bx := int(uint16(inst))
			kwnames := consts[bx].AsTuple().Items

			target := stack[sp-totalArgs-1]
			newBase := sp - totalArgs
			retSP := sp - totalArgs - 1

			switch target.Kind() {
			case val.KindFunction:
				fn := target.AsFunction()
				callee := AsCodeObject(fn.Code)
				vm.frames[frameIdx] = CallFrame{
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
				}
				frameIdx++
				sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, newBase, totalArgs, kwnames)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				ip = 0

			case val.KindCode:
				callee := AsCodeObject(target)
				vm.frames[frameIdx] = CallFrame{
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
				}
				frameIdx++
				sp = vm.bindFunctionArgs(callee, nil, nil, newBase, totalArgs, kwnames)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				ip = 0

			case val.KindBuiltinFunc:
				builtin := target.AsBuiltinFunc()
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := builtin.Fn(&vm.execCtx, target, stack[newBase:newBase+totalArgs], kwnames)
				stack[retSP] = res
				sp = retSP + 1
				ip++

			default:
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := vm.callFromHook(&vm.execCtx, target, newBase, totalArgs, kwnames)
				stack[retSP] = res
				sp = retSP + 1
				ip++
			}

		case OP_LOAD_METHOD:
			obj := stack[sp-1]
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				entry := vm.ic[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer && entry.CodePtr == &code[0] {
					if entry.Kind == ICKindClassMethod {
						// stack[sp-1] is already obj (Receiver); place raw Callable at stack[sp] with 0 allocs!
						stack[sp] = entry.CachedVal
						sp++
						ip++
						continue
					}
					if entry.Kind == ICKindInstanceSlot {
						stack[sp-1] = val.UnboundVal()
						stack[sp] = instObj.Slots[entry.SlotIndex]
						sp++
						ip++
						continue
					}
				}
			}
			vm.loadMethodSlow(obj, consts[uint16(inst)], ip, sp, frameIdx, &code[0])
			sp++
			ip++

		case OP_CALL_METHOD:
			argc := int(uint8(inst >> 16))
			retSP := sp - argc - 2
			receiver := stack[retSP]
			callable := stack[retSP+1]

			var newBase int
			var totalArgs int

			if !receiver.IsUnbound() {
				// Bound method call fast path: Receiver is already at stack[retSP].
				// Shift positional arguments down by 1 slot over Callable so [Receiver, arg1, ..., argN] are contiguous.
				newBase = retSP
				totalArgs = argc + 1
				if argc == 1 {
					stack[retSP+1] = stack[retSP+2]
				} else if argc > 1 {
					copy(stack[retSP+1:retSP+1+argc], stack[retSP+2:retSP+2+argc])
				}
			} else {
				newBase = retSP + 2
				totalArgs = argc
			}

			switch callable.Kind() {
			case val.KindFunction:
				fn := callable.AsFunction()
				callee := AsCodeObject(fn.Code)
				vm.frames[frameIdx] = CallFrame{
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
				}
				frameIdx++
				if totalArgs < callee.NumParams && len(fn.Defaults) > 0 {
					sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, newBase, totalArgs, nil)
				} else {
					numLocalsCallee := callee.NumLocals
					if numLocalsCallee < totalArgs {
						numLocalsCallee = totalArgs
					}
					sp = newBase + numLocalsCallee
				}
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				ip = 0

			case val.KindCode:
				callee := AsCodeObject(callable)
				vm.frames[frameIdx] = CallFrame{
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
				}
				frameIdx++
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				numLocalsCallee := callee.NumLocals
				if numLocalsCallee < totalArgs {
					numLocalsCallee = totalArgs
				}
				sp = newBase + numLocalsCallee
				ip = 0

			case val.KindBuiltinFunc:
				builtin := callable.AsBuiltinFunc()
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := builtin.Fn(&vm.execCtx, callable, stack[newBase:newBase+totalArgs], nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++

			default:
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := vm.callFromHook(&vm.execCtx, callable, newBase, totalArgs, nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++
			}

		case OP_RETURN:
			sp--
			retVal := stack[sp]
			if frameIdx == startFrameIdx {
				return retVal
			}
			frameIdx--
			caller := vm.frames[frameIdx]
			code = caller.code
			consts = caller.consts
			ip = caller.ip
			baseOffset = caller.baseOffset
			stack[caller.retSP] = retVal
			sp = caller.retSP + 1

		case OP_LOAD_ATTR:
			obj := stack[sp-1]
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				entry := vm.ic[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer && entry.CodePtr == &code[0] {
					if entry.Kind == ICKindInstanceSlot {
						stack[sp-1] = instObj.Slots[entry.SlotIndex]
						ip++
						continue
					}
					if entry.Kind == ICKindDataDescriptor {
						vm.execCtx.SP = sp
						vm.activeFrameIdx = frameIdx
						stack[sp-1] = entry.CachedVal.Type().Slots.TpDescrGet(&vm.execCtx, entry.CachedVal, obj, instType)
						ip++
						continue
					}
				}
				stack[sp-1] = vm.loadAttrInstanceSlow(instObj, obj, consts[uint16(inst)], ip, sp, frameIdx, &code[0])
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-1] = val.GetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)])
			}
			ip++

		case OP_STORE_ATTR:
			a := stack[sp-2]
			b := stack[sp-1]
			var obj val.Value
			var v val.Value
			if a.IsInstance() || a.IsType() || a.IsSuper() {
				obj = a
				v = b
			} else {
				obj = b
				v = a
			}
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				entry := vm.ic[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer && entry.CodePtr == &code[0] {
					if entry.Kind == ICKindInstanceSlot {
						instObj.Slots[entry.SlotIndex] = v
						sp -= 2
						ip++
						continue
					}
					if entry.Kind == ICKindDataDescriptor {
						vm.execCtx.SP = sp
						vm.activeFrameIdx = frameIdx
						entry.CachedVal.Type().Slots.TpDescrSet(&vm.execCtx, entry.CachedVal, obj, v)
						sp -= 2
						ip++
						continue
					}
				}
				vm.storeAttrInstanceSlow(instObj, obj, consts[uint16(inst)], v, ip, sp, frameIdx, &code[0])
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				val.SetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)], v)
			}
			sp -= 2
			ip++

		default:
			panic("vm: unknown opcode")
		}
	}
}

func isCallableConst(v val.Value) bool {
	k := v.Kind()
	return k == val.KindCode || k == val.KindFunction || k == val.KindBuiltinFunc
}

//go:noinline
func (vm *VM) loadMethodSlow(obj, attrName val.Value, ip, sp, frameIdx int, codePtr *uint32) {
	attrName = val.InternValue(attrName)

	if obj.IsSuper() {
		sup := obj.AsSuper()
		selfType := sup.Self.Type()
		descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, attrName)
		if !ok {
			panic("vm: AttributeError: 'super' object has no attribute '" + attrName.AsString() + "'")
		}
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			vm.stack[sp-1] = sup.Self
			vm.stack[sp] = descr
			return
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, sup.Self, selfType)
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = res
			return
		}
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = descr
		return
	}

	if obj.IsInstance() {
		instObj := obj.AsInstance()
		instType := instObj.Header.Type

		descr, _, foundInClass := instType.LookupMRO(attrName)
		if foundInClass {
			descrType := descr.Type()
			if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
				vm.stack[sp-1] = val.UnboundVal()
				vm.stack[sp] = res
				return
			}
		}

		if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
			vm.ic[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindInstanceSlot,
				SlotIndex:       slotIdx,
				ExpectedTypeVer: instType.VersionTag,
				CodePtr:         codePtr,
			}
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = instObj.Slots[slotIdx]
			return
		}

		if foundInClass {
			if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
				vm.ic[ip] = ICSlot{
					ExpectedShapeID: instObj.Shape.ID,
					Kind:            ICKindClassMethod,
					ExpectedTypeVer: instType.VersionTag,
					CodePtr:         codePtr,
					CachedVal:       descr,
				}
				vm.stack[sp-1] = obj
				vm.stack[sp] = descr
				return
			}
			if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrGet(&vm.execCtx, descr, obj, instType)
				vm.stack[sp-1] = val.UnboundVal()
				vm.stack[sp] = res
				return
			}
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = descr
			return
		}

		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = res
		return
	}

	if obj.IsType() {
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = res
		return
	}

	// Primitive or built-in heap type method lookup.
	tp := obj.Type()
	if descr, _, ok := tp.LookupMRO(attrName); ok {
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			vm.stack[sp-1] = obj
			vm.stack[sp] = descr
			return
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, obj, tp)
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = res
			return
		}
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = descr
		return
	}

	panic("vm: AttributeError: '" + tp.Name + "' object has no attribute '" + attrName.AsString() + "'")
}

//go:noinline
func (vm *VM) loadAttrInstanceSlow(instObj *val.PyInstance, obj, attrName val.Value, ip, sp, frameIdx int, codePtr *uint32) val.Value {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type

	descr, _, foundInClass := instType.LookupMRO(attrName)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			vm.ic[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindDataDescriptor,
				ExpectedTypeVer: instType.VersionTag,
				CodePtr:         codePtr,
				CachedVal:       descr,
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		vm.ic[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       slotIdx,
			ExpectedTypeVer: instType.VersionTag,
			CodePtr:         codePtr,
		}
		return instObj.Slots[slotIdx]
	}

	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
		}
		return descr
	}

	vm.execCtx.SP = sp
	vm.activeFrameIdx = frameIdx
	return val.GetAttrCtx(&vm.execCtx, obj, attrName)
}

//go:noinline
func (vm *VM) storeAttrInstanceSlow(instObj *val.PyInstance, obj, attrName, v val.Value, ip, sp, frameIdx int, codePtr *uint32) {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type

	if descr, _, foundInClass := instType.LookupMRO(attrName); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			vm.ic[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindDataDescriptor,
				ExpectedTypeVer: instType.VersionTag,
				CodePtr:         codePtr,
				CachedVal:       descr,
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			descrType.Slots.TpDescrSet(&vm.execCtx, descr, obj, v)
			return
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		instObj.Slots[slotIdx] = v
		vm.ic[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       slotIdx,
			ExpectedTypeVer: instType.VersionTag,
			CodePtr:         codePtr,
		}
		return
	}

	slotIdx := instObj.SetAttr(attrName, v)
	vm.ic[ip] = ICSlot{
		ExpectedShapeID: instObj.Shape.ID,
		Kind:            ICKindInstanceSlot,
		SlotIndex:       slotIdx,
		ExpectedTypeVer: instType.VersionTag,
		CodePtr:         codePtr,
	}
}
