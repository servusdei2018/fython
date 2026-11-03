package vm

// Opcode represents an 8-bit VM instruction opcode.
type Opcode uint8

const (
	// Generic Bytecode Operations
	OP_LOAD_CONST Opcode = iota
	OP_LOAD_FAST
	OP_STORE_FAST
	OP_BINARY_ADD
	OP_BINARY_SUB
	OP_BINARY_MUL
	OP_COMPARE_LT
	OP_JUMP_IF_FALSE
	OP_JUMP
	OP_CALL
	OP_RETURN
	OP_LOAD_ATTR
	OP_STORE_ATTR
	OP_LOAD_METHOD
	OP_CALL_METHOD
	OP_CALL_KW
	OP_BINARY_SUBSCR
	OP_STORE_SUBSCR

	// Quickened (PEP 659 Adaptive) Operations
	OP_BINARY_ADD_INT
	OP_BINARY_ADD_FLOAT
	OP_BINARY_SUB_INT
	OP_COMPARE_LT_INT
)

// String returns the mnemonic name of the Opcode.
func (op Opcode) String() string {
	switch op {
	case OP_LOAD_CONST:
		return "OP_LOAD_CONST"
	case OP_LOAD_FAST:
		return "OP_LOAD_FAST"
	case OP_STORE_FAST:
		return "OP_STORE_FAST"
	case OP_BINARY_ADD:
		return "OP_BINARY_ADD"
	case OP_BINARY_SUB:
		return "OP_BINARY_SUB"
	case OP_BINARY_MUL:
		return "OP_BINARY_MUL"
	case OP_COMPARE_LT:
		return "OP_COMPARE_LT"
	case OP_JUMP_IF_FALSE:
		return "OP_JUMP_IF_FALSE"
	case OP_JUMP:
		return "OP_JUMP"
	case OP_CALL:
		return "OP_CALL"
	case OP_RETURN:
		return "OP_RETURN"
	case OP_LOAD_ATTR:
		return "OP_LOAD_ATTR"
	case OP_STORE_ATTR:
		return "OP_STORE_ATTR"
	case OP_LOAD_METHOD:
		return "OP_LOAD_METHOD"
	case OP_CALL_METHOD:
		return "OP_CALL_METHOD"
	case OP_CALL_KW:
		return "OP_CALL_KW"
	case OP_BINARY_SUBSCR:
		return "OP_BINARY_SUBSCR"
	case OP_STORE_SUBSCR:
		return "OP_STORE_SUBSCR"
	case OP_BINARY_ADD_INT:
		return "OP_BINARY_ADD_INT"
	case OP_BINARY_ADD_FLOAT:
		return "OP_BINARY_ADD_FLOAT"
	case OP_BINARY_SUB_INT:
		return "OP_BINARY_SUB_INT"
	case OP_COMPARE_LT_INT:
		return "OP_COMPARE_LT_INT"
	default:
		return "OP_UNKNOWN"
	}
}

// Encode packs an 8-bit Opcode, 8-bit ArgA, and 16-bit ArgBx into a 32-bit instruction word:
// [8-bit Opcode (bits 24..31)][8-bit ArgA (bits 16..23)][16-bit ArgBx (bits 0..15)]
func Encode(op Opcode, argA uint8, argBx uint16) uint32 {
	return (uint32(op) << 24) | (uint32(argA) << 16) | uint32(argBx)
}

// DecodeOpcode extracts the 8-bit Opcode from a 32-bit instruction word.
func DecodeOpcode(inst uint32) Opcode {
	return Opcode(inst >> 24)
}

// DecodeArgA extracts the 8-bit ArgA field from a 32-bit instruction word.
func DecodeArgA(inst uint32) uint8 {
	return uint8(inst >> 16)
}

// DecodeArgBx extracts the 16-bit ArgBx field from a 32-bit instruction word.
func DecodeArgBx(inst uint32) uint16 {
	return uint16(inst)
}

// ReplaceOpcode returns a new 32-bit instruction word with its Opcode replaced by newOp,
// preserving ArgA and ArgBx for in-place PEP 659 quickening and deoptimization.
func ReplaceOpcode(inst uint32, newOp Opcode) uint32 {
	return (inst & 0x00FFFFFF) | (uint32(newOp) << 24)
}
