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

	// Extended Bytecode Operations
	OP_POP_TOP
	OP_DUP_TOP
	OP_LOAD_GLOBAL
	OP_STORE_GLOBAL
	OP_LOAD_DEREF
	OP_STORE_DEREF
	OP_MAKE_FUNCTION
	OP_GET_ITER
	OP_FOR_ITER
	OP_BUILD_LIST
	OP_LIST_APPEND
	OP_BUILD_MAP
	OP_MAP_ADD
	OP_BUILD_TUPLE
	OP_BUILD_SLICE
	OP_BUILD_CLASS
	OP_UNPACK_SEQUENCE
	OP_COMPARE_OP
	OP_BINARY_TRUE_DIV
	OP_BINARY_FLOOR_DIV
	OP_BINARY_MOD
	OP_BINARY_POW
	OP_UNARY_NEGATIVE
	OP_UNARY_NOT
	OP_JUMP_IF_TRUE
	OP_JUMP_IF_FALSE_OR_POP
	OP_JUMP_IF_TRUE_OR_POP
	OP_LOAD_CLOSURE

	// Module Imports
	OP_IMPORT_NAME
	OP_IMPORT_FROM
	OP_IMPORT_STAR

	// Exceptions, Context Managers & Generators
	OP_RAISE
	OP_YIELD_VALUE
	OP_YIELD_FROM
	OP_CHECK_EXC_MATCH
	OP_CLEANUP_EXC
	OP_GET_AWAITABLE
	OP_AWAIT

	// Quickened (PEP 659 Adaptive) Operations
	OP_BINARY_ADD_INT
	OP_BINARY_ADD_FLOAT
	OP_BINARY_SUB_INT
	OP_COMPARE_LT_INT
)

// Comparison operation codes for OP_COMPARE_OP (passed in ArgA field).
const (
	CMP_LT uint8 = iota
	CMP_LTE
	CMP_EQ
	CMP_NEQ
	CMP_GT
	CMP_GTE
	CMP_IN
	CMP_NOT_IN
	CMP_IS
	CMP_IS_NOT
)

// CallFromStack is the sentinel value for ArgBx indicating OP_CALL callee is at stack[sp-argc-1].
const CallFromStack uint16 = 0xFFFF

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
	case OP_POP_TOP:
		return "OP_POP_TOP"
	case OP_DUP_TOP:
		return "OP_DUP_TOP"
	case OP_LOAD_GLOBAL:
		return "OP_LOAD_GLOBAL"
	case OP_STORE_GLOBAL:
		return "OP_STORE_GLOBAL"
	case OP_LOAD_DEREF:
		return "OP_LOAD_DEREF"
	case OP_STORE_DEREF:
		return "OP_STORE_DEREF"
	case OP_MAKE_FUNCTION:
		return "OP_MAKE_FUNCTION"
	case OP_GET_ITER:
		return "OP_GET_ITER"
	case OP_FOR_ITER:
		return "OP_FOR_ITER"
	case OP_BUILD_LIST:
		return "OP_BUILD_LIST"
	case OP_LIST_APPEND:
		return "OP_LIST_APPEND"
	case OP_BUILD_MAP:
		return "OP_BUILD_MAP"
	case OP_MAP_ADD:
		return "OP_MAP_ADD"
	case OP_BUILD_TUPLE:
		return "OP_BUILD_TUPLE"
	case OP_BUILD_SLICE:
		return "OP_BUILD_SLICE"
	case OP_BUILD_CLASS:
		return "OP_BUILD_CLASS"
	case OP_UNPACK_SEQUENCE:
		return "OP_UNPACK_SEQUENCE"
	case OP_COMPARE_OP:
		return "OP_COMPARE_OP"
	case OP_BINARY_TRUE_DIV:
		return "OP_BINARY_TRUE_DIV"
	case OP_BINARY_FLOOR_DIV:
		return "OP_BINARY_FLOOR_DIV"
	case OP_BINARY_MOD:
		return "OP_BINARY_MOD"
	case OP_BINARY_POW:
		return "OP_BINARY_POW"
	case OP_UNARY_NEGATIVE:
		return "OP_UNARY_NEGATIVE"
	case OP_UNARY_NOT:
		return "OP_UNARY_NOT"
	case OP_JUMP_IF_TRUE:
		return "OP_JUMP_IF_TRUE"
	case OP_JUMP_IF_FALSE_OR_POP:
		return "OP_JUMP_IF_FALSE_OR_POP"
	case OP_JUMP_IF_TRUE_OR_POP:
		return "OP_JUMP_IF_TRUE_OR_POP"
	case OP_LOAD_CLOSURE:
		return "OP_LOAD_CLOSURE"
	case OP_IMPORT_NAME:
		return "OP_IMPORT_NAME"
	case OP_IMPORT_FROM:
		return "OP_IMPORT_FROM"
	case OP_IMPORT_STAR:
		return "OP_IMPORT_STAR"
	case OP_RAISE:
		return "OP_RAISE"
	case OP_YIELD_VALUE:
		return "OP_YIELD_VALUE"
	case OP_YIELD_FROM:
		return "OP_YIELD_FROM"
	case OP_CHECK_EXC_MATCH:
		return "OP_CHECK_EXC_MATCH"
	case OP_CLEANUP_EXC:
		return "OP_CLEANUP_EXC"
	case OP_GET_AWAITABLE:
		return "OP_GET_AWAITABLE"
	case OP_AWAIT:
		return "OP_AWAIT"
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
