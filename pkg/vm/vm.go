package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

// Run executes the top-level bytecode slice with the given constant pool and local variable count.
// Hot loop state (code, consts, ip, baseOffset, sp, stack) is kept in local variables so the Go
// compiler allocates them to CPU registers across loop iterations.
func (vm *VM) Run(code []uint32, consts []val.Value, numLocals int) val.Value {
	stack := vm.stack
	frames := vm.frames
	ic := vm.ic

	frameIdx := 0
	baseOffset := 0
	sp := numLocals
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
				stack[sp-2] = val.Add(a, b)
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
				stack[sp-2] = val.Add(a, b)
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
				stack[sp-2] = val.Add(a, b)
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
				stack[sp-2] = val.Sub(a, b)
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
				stack[sp-2] = val.Sub(a, b)
			}
			sp--
			ip++

		case OP_BINARY_MUL:
			b := stack[sp-1]
			a := stack[sp-2]
			stack[sp-2] = val.Mul(a, b)
			sp--
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

			var callee *CodeObject
			var newBase int
			var retSP int

			if bx < len(consts) && consts[bx].Kind() == val.KindCode {
				callee = AsCodeObject(consts[bx])
				newBase = sp - argc
				retSP = newBase
			} else {
				callee = AsCodeObject(stack[sp-argc-1])
				newBase = sp - argc
				retSP = sp - argc - 1
			}

			// Save caller state into contiguous CallFrame slab
			frames[frameIdx] = CallFrame{
				code:       code,
				consts:     consts,
				ip:         ip + 1,
				baseOffset: baseOffset,
				retSP:      retSP,
			}
			frameIdx++

			// Transition registers to callee frame without heap allocations
			code = callee.Code
			consts = callee.Consts
			baseOffset = newBase
			numLocalsCallee := callee.NumLocals
			if numLocalsCallee < argc {
				numLocalsCallee = argc
			}
			sp = newBase + numLocalsCallee
			ip = 0

		case OP_RETURN:
			sp--
			retVal := stack[sp]
			if frameIdx == 0 {
				return retVal
			}
			frameIdx--
			caller := frames[frameIdx]
			code = caller.code
			consts = caller.consts
			ip = caller.ip
			baseOffset = caller.baseOffset
			stack[caller.retSP] = retVal
			sp = caller.retSP + 1

		case OP_LOAD_ATTR:
			instObj := stack[sp-1].AsInstance()
			entry := ic[ip]
			if instObj.Shape.ID == entry.ExpectedShapeID {
				stack[sp-1] = instObj.Slots[entry.SlotIndex]
			} else {
				bx := uint16(inst)
				slotIdx, ok := instObj.Shape.Lookup(consts[bx])
				if !ok {
					panic("vm: AttributeError: object has no attribute")
				}
				ic[ip] = ICSlot{
					ExpectedShapeID: instObj.Shape.ID,
					SlotIndex:       slotIdx,
				}
				stack[sp-1] = instObj.Slots[slotIdx]
			}
			ip++

		case OP_STORE_ATTR:
			a := stack[sp-2]
			b := stack[sp-1]
			var instObj *val.PyInstance
			var v val.Value
			if a.IsInstance() {
				instObj = a.AsInstance()
				v = b
			} else {
				instObj = b.AsInstance()
				v = a
			}
			entry := ic[ip]
			if instObj.Shape.ID == entry.ExpectedShapeID {
				instObj.Slots[entry.SlotIndex] = v
			} else {
				bx := uint16(inst)
				attrName := consts[bx]
				if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
					instObj.Slots[slotIdx] = v
					ic[ip] = ICSlot{
						ExpectedShapeID: instObj.Shape.ID,
						SlotIndex:       slotIdx,
					}
				} else {
					slotIdx := instObj.SetAttr(attrName, v)
					ic[ip] = ICSlot{
						ExpectedShapeID: instObj.Shape.ID,
						SlotIndex:       slotIdx,
					}
				}
			}
			sp -= 2
			ip++

		default:
			panic("vm: unknown opcode")
		}
	}
}

// RunCode executes a *CodeObject on the VM.
func (vm *VM) RunCode(co *CodeObject) val.Value {
	return vm.Run(co.Code, co.Consts, co.NumLocals)
}
