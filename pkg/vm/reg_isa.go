package vm

import "strconv"

// RegOp identifies a Tier-2 register-bytecode operation executed by
// VM.runRegLoop. The numbering is local to the register ISA and unrelated
// to Opcode values in opcode.go.
type RegOp uint8

const (
	RegInvalid RegOp = iota

	// Moves, constants, globals.
	RegMov        // R[dst] = R[src1]
	RegLoadConst  // R[dst] = consts[aux]
	RegLoadGlobal // R[dst] = globals[auxName]; aux low16 = const index of name
	RegStoreGlobal

	// Integer/float arithmetic with unboxed fast paths.
	RegAdd
	RegSub
	RegMul
	RegTrueDiv
	RegFloorDiv
	RegMod
	RegPow
	RegMatMul
	RegBitAnd
	RegBitOr
	RegBitXor
	RegLShift
	RegRShift

	// Unary operations.
	RegNeg
	RegPos
	RegInvert
	RegNot

	// Comparisons. aux carries the CMP_* code for RegCmp; the RegCmpJMixed
	// fused forms below carry no aux (target in aux).
	RegEq
	RegNe
	RegLt
	RegLe
	RegGt
	RegGe
	RegIn
	RegIs

	// Jumps. aux is the target register pc.
	RegJmp
	RegJmpIfFalse // if !R[src1].IsTruthy() { pc = aux }
	RegJmpIfTrue
	// Fused compare-and-branch. aux is the target register pc.
	RegLtJmpIfFalse
	RegLtJmpIfTrue
	RegEqJmpIfFalse
	RegEqJmpIfTrue
	RegIsNoneJmpIfFalse
	RegIsNoneJmpIfTrue

	// Attributes, methods, subscripts. aux is the const index of the name.
	RegLoadAttr
	RegStoreAttr
	RegLoadMethod
	// Calls. src1 is the callee register, src2 the start of a contiguous
	// argument window holding exactly argc values. aux low16 is argc.
	RegCall
	// src1 is the receiver, src2 the start of a window holding
	// [callable, arg0 .. argN-1]. aux low16 is argc.
	RegCallMethod
	// src1 is the callee, src2 the start of the positional window.
	// aux low16 is total positional args, aux high16 the const index of
	// the keyword-names tuple.
	RegCallKw
	// src1 is the callee, src2 the args value, aux bit0 is hasKW and aux
	// bits 8..15 hold the kwdict register when hasKW is set.
	RegCallFunctionEx
	// src1 is the return value register.
	RegReturn

	// Containers. src1 is the start of a contiguous value window holding
	// exactly aux values, except where noted.
	RegBuildList
	RegBuildTuple
	RegBuildSet
	RegBuildDict // aux is the pair count; window holds 2*aux values
	RegBuildSlice
	RegBuildString
	// src1 is the formatted value, src2 the spec (or unused when the
	// has-spec flag is clear). aux low8 is the conversion char, aux bit8
	// the has-spec flag.
	RegFormatValue
	// src1 is the sequence, dst the first of aux consecutive targets.
	RegUnpack
	// dst is the container, src1 the item, aux the depth/offset operand.
	RegListAppend
	RegSetAdd
	// dst is the container, src1 the key, src2 the value, aux the depth.
	RegMapAdd
	// dst is the list, src1 the iterable, aux the offset operand.
	RegListExtend
	// dst is the new function, src1 the start of a window holding
	// 1+popcount(aux) values bottom-to-top. aux carries MAKE_FUNCTION flags.
	RegMakeFunction

	// Iteration.
	RegSetupRange   // aux 1|2; src1 is stop (and src2 start); dst holds range
	RegGetIter      // R[dst] = iter(R[src1])
	RegForIter      // R[dst] = next(R[src1]) or pc = aux
	RegForIterRange // R[src1] must be range; writes local dst; or pc = aux

	// Subscripts.
	RegGetItem // R[dst] = R[src1][R[src2]]
	// Helper layout is [value, obj, key].
	RegSetItem // R[dst][R[src1]] = R[src2] with value in R[dst]

	// Stack-compat helpers.
	RegPopTop // releases R[src1]
	RegDupTop // R[dst] = R[src1]

	// Exceptions. aux is the argc operand of OP_RAISE.
	RegRaise // src1 is exc (or exc/cause pair in src1, src2)
)

// String returns the mnemonic name of the RegOp.
func (op RegOp) String() string {
	switch op {
	case RegMov:
		return "REG_MOV"
	case RegLoadConst:
		return "REG_LOAD_CONST"
	case RegLoadGlobal:
		return "REG_LOAD_GLOBAL"
	case RegStoreGlobal:
		return "REG_STORE_GLOBAL"
	case RegAdd:
		return "REG_ADD"
	case RegSub:
		return "REG_SUB"
	case RegMul:
		return "REG_MUL"
	case RegTrueDiv:
		return "REG_TRUE_DIV"
	case RegFloorDiv:
		return "REG_FLOOR_DIV"
	case RegMod:
		return "REG_MOD"
	case RegPow:
		return "REG_POW"
	case RegMatMul:
		return "REG_MATMUL"
	case RegBitAnd:
		return "REG_BIT_AND"
	case RegBitOr:
		return "REG_BIT_OR"
	case RegBitXor:
		return "REG_BIT_XOR"
	case RegLShift:
		return "REG_LSHIFT"
	case RegRShift:
		return "REG_RSHIFT"
	case RegNeg:
		return "REG_NEG"
	case RegPos:
		return "REG_POS"
	case RegInvert:
		return "REG_INVERT"
	case RegNot:
		return "REG_NOT"
	case RegEq:
		return "REG_EQ"
	case RegNe:
		return "REG_NE"
	case RegLt:
		return "REG_LT"
	case RegLe:
		return "REG_LE"
	case RegGt:
		return "REG_GT"
	case RegGe:
		return "REG_GE"
	case RegIn:
		return "REG_IN"
	case RegIs:
		return "REG_IS"
	case RegJmp:
		return "REG_JMP"
	case RegJmpIfFalse:
		return "REG_JMP_IF_FALSE"
	case RegJmpIfTrue:
		return "REG_JMP_IF_TRUE"
	case RegLtJmpIfFalse:
		return "REG_LT_JMP_IF_FALSE"
	case RegLtJmpIfTrue:
		return "REG_LT_JMP_IF_TRUE"
	case RegEqJmpIfFalse:
		return "REG_EQ_JMP_IF_FALSE"
	case RegEqJmpIfTrue:
		return "REG_EQ_JMP_IF_TRUE"
	case RegIsNoneJmpIfFalse:
		return "REG_IS_NONE_JMP_IF_FALSE"
	case RegIsNoneJmpIfTrue:
		return "REG_IS_NONE_JMP_IF_TRUE"
	case RegLoadAttr:
		return "REG_LOAD_ATTR"
	case RegStoreAttr:
		return "REG_STORE_ATTR"
	case RegLoadMethod:
		return "REG_LOAD_METHOD"
	case RegCall:
		return "REG_CALL"
	case RegCallMethod:
		return "REG_CALL_METHOD"
	case RegCallKw:
		return "REG_CALL_KW"
	case RegCallFunctionEx:
		return "REG_CALL_FUNCTION_EX"
	case RegReturn:
		return "REG_RETURN"
	case RegBuildList:
		return "REG_BUILD_LIST"
	case RegBuildTuple:
		return "REG_BUILD_TUPLE"
	case RegBuildSet:
		return "REG_BUILD_SET"
	case RegBuildDict:
		return "REG_BUILD_DICT"
	case RegBuildSlice:
		return "REG_BUILD_SLICE"
	case RegBuildString:
		return "REG_BUILD_STRING"
	case RegFormatValue:
		return "REG_FORMAT_VALUE"
	case RegUnpack:
		return "REG_UNPACK"
	case RegListAppend:
		return "REG_LIST_APPEND"
	case RegSetAdd:
		return "REG_SET_ADD"
	case RegMapAdd:
		return "REG_MAP_ADD"
	case RegListExtend:
		return "REG_LIST_EXTEND"
	case RegMakeFunction:
		return "REG_MAKE_FUNCTION"
	case RegSetupRange:
		return "REG_SETUP_RANGE"
	case RegGetIter:
		return "REG_GET_ITER"
	case RegForIter:
		return "REG_FOR_ITER"
	case RegForIterRange:
		return "REG_FOR_ITER_RANGE"
	case RegGetItem:
		return "REG_GET_ITEM"
	case RegSetItem:
		return "REG_SET_ITEM"
	case RegPopTop:
		return "REG_POP_TOP"
	case RegDupTop:
		return "REG_DUP_TOP"
	case RegRaise:
		return "REG_RAISE"
	default:
		return "REG_UNKNOWN(" + strconv.Itoa(int(op)) + ")"
	}
}

// RegInstr is a fixed-width 64-bit register instruction:
//
//	[op uint8 | dst uint8 | src1 uint8 | src2 uint8 | aux uint32]
//
// Every register instruction decodes with a single 64-bit load and carries
// no multi-word operand fetches.
type RegInstr uint64

// EncodeReg packs a register instruction word.
func EncodeReg(op RegOp, dst, src1, src2 uint8, aux uint32) RegInstr {
	return RegInstr(uint64(op) | uint64(dst)<<8 | uint64(src1)<<16 | uint64(src2)<<24 | uint64(aux)<<32)
}

// DecodeRegOp extracts the operation of a register instruction.
func DecodeRegOp(inst RegInstr) RegOp {
	return RegOp(inst & 0xFF)
}

// DecodeRegDst extracts the dst register of a register instruction.
func DecodeRegDst(inst RegInstr) uint8 {
	return uint8(inst >> 8)
}

// DecodeRegSrc1 extracts the src1 register of a register instruction.
func DecodeRegSrc1(inst RegInstr) uint8 {
	return uint8(inst >> 16)
}

// DecodeRegSrc2 extracts the src2 register of a register instruction.
func DecodeRegSrc2(inst RegInstr) uint8 {
	return uint8(inst >> 24)
}

// DecodeRegAux extracts the aux field of a register instruction.
func DecodeRegAux(inst RegInstr) uint32 {
	return uint32(inst >> 32)
}

// RegCode is the compiled register form of a CodeObject. The stack
// instruction stream is retained untouched; RegCode is consulted only when a
// frame is entered through runRegLoop.
type RegCode struct {
	// Code holds the register instructions. pc values in jump aux fields
	// index this slice.
	Code []RegInstr
	// StackPC maps a register pc to the originating stack ip, so runtime
	// errors reconstruct the exact traceback line via co.LineAt.
	StackPC []int32
	// NumRegs is the total register count: locals in [0, ScratchBase) and
	// scratch in [ScratchBase, NumRegs).
	NumRegs uint16
	// ScratchBase is the first non-local register. It equals the compiled
	// frame's EffectiveLocals.
	ScratchBase uint16
	// RetLive records, per REG_RETURN position, the live scratch registers
	// that may hold ephemeral iterators to release.
	RetLive []regRetLive
}
