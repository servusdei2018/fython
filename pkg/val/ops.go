package val

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"math/bits"
	"strings"
)

// Add performs overflow-safe Python addition (a + b) without an explicit VM context.
func Add(a, b Value) Value {
	return AddCtx(nil, a, b)
}

// AddCtx performs overflow-safe Python addition (a + b) with VM callback context for NbAdd slots.
// Fast path: int64 + int64 with bitwise overflow check (0 allocations when non-overflowing).
// Overflow spill path: promotes to *PyBigInt on signed 64-bit overflow.
// Mixed int/float path: coerces KindInt64 to float64 when paired with KindFloat64.
// Polymorphic fallback: invokes a.Type().Slots.NbAdd directly without string dunder lookup.
func AddCtx(ctx *ExecContext, a, b Value) Value {
	if a.ptr == &tagInt64 && b.ptr == &tagInt64 {
		ai := int64(a.num)
		bi := int64(b.num)
		sum := ai + bi
		if ((ai ^ sum) & (bi ^ sum)) >= 0 {
			return Value{num: uint64(sum), ptr: &tagInt64}
		}
		return addInt64Overflow(ai, bi)
	}

	if a.ptr == &tagFloat64 && b.ptr == &tagFloat64 {
		return Float64Val(math.Float64frombits(a.num) + math.Float64frombits(b.num))
	}
	if a.ptr == &tagInt64 && b.ptr == &tagFloat64 {
		return Float64Val(float64(int64(a.num)) + math.Float64frombits(b.num))
	}
	if a.ptr == &tagFloat64 && b.ptr == &tagInt64 {
		return Float64Val(math.Float64frombits(a.num) + float64(int64(b.num)))
	}

	return addSlow(ctx, a, b)
}

//go:noinline
func addInt64Overflow(ai, bi int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	var bigA, bigB big.Int
	bigA.SetInt64(ai)
	bigB.SetInt64(bi)
	obj.Val.Add(&bigA, &bigB)
	return Value{num: 0, ptr: &obj.Header}
}

//go:noinline
func addSlow(ctx *ExecContext, a, b Value) Value {
	aType := a.Type()
	bType := b.Type()
	isStrictSub := aType != bType && IsSubclass(bType, aType)

	// 1. Subclass reflected precedence
	if isStrictSub && bType.dunderRAdd.ptr != nil {
		res := ctx.Invoke2(bType.dunderRAdd, b, a)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 2. Forward __add__
	if nbAdd := aType.Slots.NbAdd; nbAdd != nil {
		res := nbAdd(ctx, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	} else if aType.dunderAdd.ptr != nil {
		res := ctx.Invoke2(aType.dunderAdd, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 3. Reflected __radd__
	if !isStrictSub && bType.dunderRAdd.ptr != nil {
		res := ctx.Invoke2(bType.dunderRAdd, b, a)
		if !res.IsNotImplemented() {
			return res
		}
	}

	panic("val: TypeError: unsupported operand type(s) for +: '" + aType.Name + "' and '" + bType.Name + "'")
}

func numericNbAdd(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		ai := int64(a.num)
		bi := int64(b.num)
		sum := ai + bi
		if ((ai ^ sum) & (bi ^ sum)) >= 0 {
			return Value{num: uint64(sum), ptr: &tagInt64}
		}
		return addInt64Overflow(ai, bi)
	}
	if (a.ptr == &tagFloat64 || b.ptr == &tagFloat64) &&
		(a.ptr == &tagInt64 || a.ptr == &tagBool || a.ptr == &tagFloat64 || a.ptr.Kind == KindBigInt) &&
		(b.ptr == &tagInt64 || b.ptr == &tagBool || b.ptr == &tagFloat64 || b.ptr.Kind == KindBigInt) {
		return Float64Val(toFloat64(a) + toFloat64(b))
	}
	if (a.ptr.Kind == KindBigInt || a.ptr == &tagInt64 || a.ptr == &tagBool) &&
		(b.ptr.Kind == KindBigInt || b.ptr == &tagInt64 || b.ptr == &tagBool) {
		obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType}}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		obj.Val.Add(bigA, bigB)
		return Value{num: 0, ptr: &obj.Header}
	}
	panic("val.Add: unsupported numeric operand types")
}

func strNbAdd(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInlineStr || a.ptr.Kind == KindString) &&
		(b.ptr == &tagInlineStr || b.ptr.Kind == KindString) {
		return ConcatStrings(a, b)
	}
	panic("val.Add: can only concatenate str to str")
}

func listNbAdd(ctx *ExecContext, a, b Value) Value {
	if a.ptr.Kind == KindList && b.ptr.Kind == KindList {
		la := a.AsList().CopyItems()
		lb := b.AsList().CopyItems()
		merged := make([]Value, len(la)+len(lb))
		copy(merged, la)
		copy(merged[len(la):], lb)
		return NewListVal(merged...)
	}
	panic("val.Add: can only concatenate list to list")
}

// Sub performs overflow-safe Python subtraction (a - b) without an explicit VM context.
func Sub(a, b Value) Value {
	return SubCtx(nil, a, b)
}

// SubCtx performs overflow-safe Python subtraction (a - b) with VM callback context for NbSub slots.
func SubCtx(ctx *ExecContext, a, b Value) Value {
	if a.ptr == &tagInt64 && b.ptr == &tagInt64 {
		ai := int64(a.num)
		bi := int64(b.num)
		diff := ai - bi
		if ((ai ^ bi) & (ai ^ diff)) >= 0 {
			return Value{num: uint64(diff), ptr: &tagInt64}
		}
		return subInt64Overflow(ai, bi)
	}

	if a.ptr == &tagFloat64 && b.ptr == &tagFloat64 {
		return Float64Val(math.Float64frombits(a.num) - math.Float64frombits(b.num))
	}
	if a.ptr == &tagInt64 && b.ptr == &tagFloat64 {
		return Float64Val(float64(int64(a.num)) - math.Float64frombits(b.num))
	}
	if a.ptr == &tagFloat64 && b.ptr == &tagInt64 {
		return Float64Val(math.Float64frombits(a.num) - float64(int64(b.num)))
	}

	return subSlow(ctx, a, b)
}

//go:noinline
func subInt64Overflow(ai, bi int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	var bigA, bigB big.Int
	bigA.SetInt64(ai)
	bigB.SetInt64(bi)
	obj.Val.Sub(&bigA, &bigB)
	return Value{num: 0, ptr: &obj.Header}
}

//go:noinline
func subSlow(ctx *ExecContext, a, b Value) Value {
	aType := a.Type()
	bType := b.Type()
	isStrictSub := aType != bType && IsSubclass(bType, aType)

	// 1. Subclass reflected precedence
	if isStrictSub && bType.dunderRSub.ptr != nil {
		res := ctx.Invoke2(bType.dunderRSub, b, a)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 2. Forward __sub__
	if nbSub := aType.Slots.NbSub; nbSub != nil {
		res := nbSub(ctx, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	} else if aType.dunderSub.ptr != nil {
		res := ctx.Invoke2(aType.dunderSub, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 3. Reflected __rsub__
	if !isStrictSub && bType.dunderRSub.ptr != nil {
		res := ctx.Invoke2(bType.dunderRSub, b, a)
		if !res.IsNotImplemented() {
			return res
		}
	}

	panic("val: TypeError: unsupported operand type(s) for -: '" + aType.Name + "' and '" + bType.Name + "'")
}

func numericNbSub(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		ai := int64(a.num)
		bi := int64(b.num)
		diff := ai - bi
		if ((ai ^ bi) & (ai ^ diff)) >= 0 {
			return Value{num: uint64(diff), ptr: &tagInt64}
		}
		return subInt64Overflow(ai, bi)
	}
	if (a.ptr == &tagFloat64 || b.ptr == &tagFloat64) &&
		(a.ptr == &tagInt64 || a.ptr == &tagBool || a.ptr == &tagFloat64 || a.ptr.Kind == KindBigInt) &&
		(b.ptr == &tagInt64 || b.ptr == &tagBool || b.ptr == &tagFloat64 || b.ptr.Kind == KindBigInt) {
		return Float64Val(toFloat64(a) - toFloat64(b))
	}
	if (a.ptr.Kind == KindBigInt || a.ptr == &tagInt64 || a.ptr == &tagBool) &&
		(b.ptr.Kind == KindBigInt || b.ptr == &tagInt64 || b.ptr == &tagBool) {
		obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType}}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		obj.Val.Sub(bigA, bigB)
		return Value{num: 0, ptr: &obj.Header}
	}
	panic("val.Sub: unsupported numeric operand types")
}

// Mul performs overflow-safe Python multiplication (a * b) without an explicit VM context.
func Mul(a, b Value) Value {
	return MulCtx(nil, a, b)
}

// MulCtx performs overflow-safe Python multiplication (a * b) with VM callback context for NbMul slots.
func MulCtx(ctx *ExecContext, a, b Value) Value {
	if a.ptr == &tagInt64 && b.ptr == &tagInt64 {
		ai := int64(a.num)
		bi := int64(b.num)
		hi, lo := mul64Signed(ai, bi)
		// Signed 128-bit result fits in int64 iff hi == (int64(lo) >> 63).
		if hi == (int64(lo) >> 63) {
			return Value{num: lo, ptr: &tagInt64}
		}
		return mulInt64Overflow(ai, bi)
	}

	if a.ptr == &tagFloat64 && b.ptr == &tagFloat64 {
		return Float64Val(math.Float64frombits(a.num) * math.Float64frombits(b.num))
	}
	if a.ptr == &tagInt64 && b.ptr == &tagFloat64 {
		return Float64Val(float64(int64(a.num)) * math.Float64frombits(b.num))
	}
	if a.ptr == &tagFloat64 && b.ptr == &tagInt64 {
		return Float64Val(math.Float64frombits(a.num) * float64(int64(b.num)))
	}

	return mulSlow(ctx, a, b)
}

// mul64Signed computes the 128-bit signed product of a and b using math/bits.Mul64.
func mul64Signed(a, b int64) (hi int64, lo uint64) {
	uHi, uLo := bits.Mul64(uint64(a), uint64(b))
	hi = int64(uHi)
	if a < 0 {
		hi -= b
	}
	if b < 0 {
		hi -= a
	}
	return hi, uLo
}

//go:noinline
func mulInt64Overflow(ai, bi int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	var bigA, bigB big.Int
	bigA.SetInt64(ai)
	bigB.SetInt64(bi)
	obj.Val.Mul(&bigA, &bigB)
	return Value{num: 0, ptr: &obj.Header}
}

//go:noinline
func mulSlow(ctx *ExecContext, a, b Value) Value {
	aType := a.Type()
	bType := b.Type()
	isStrictSub := aType != bType && IsSubclass(bType, aType)

	// 1. Subclass reflected precedence
	if isStrictSub && bType.dunderRMul.ptr != nil {
		res := ctx.Invoke2(bType.dunderRMul, b, a)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 2. Forward __mul__
	if nbMul := aType.Slots.NbMul; nbMul != nil {
		res := nbMul(ctx, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	} else if aType.dunderMul.ptr != nil {
		res := ctx.Invoke2(aType.dunderMul, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 3. Reflected __rmul__
	if !isStrictSub && bType.dunderRMul.ptr != nil {
		res := ctx.Invoke2(bType.dunderRMul, b, a)
		if !res.IsNotImplemented() {
			return res
		}
	}

	panic("val: TypeError: unsupported operand type(s) for *: '" + aType.Name + "' and '" + bType.Name + "'")
}

func numericNbMul(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		ai := int64(a.num)
		bi := int64(b.num)
		hi, lo := mul64Signed(ai, bi)
		if hi == (int64(lo) >> 63) {
			return Value{num: lo, ptr: &tagInt64}
		}
		return mulInt64Overflow(ai, bi)
	}
	if (a.ptr == &tagFloat64 || b.ptr == &tagFloat64) &&
		(a.ptr == &tagInt64 || a.ptr == &tagBool || a.ptr == &tagFloat64 || a.ptr.Kind == KindBigInt) &&
		(b.ptr == &tagInt64 || b.ptr == &tagBool || b.ptr == &tagFloat64 || b.ptr.Kind == KindBigInt) {
		return Float64Val(toFloat64(a) * toFloat64(b))
	}
	if (a.ptr.Kind == KindBigInt || a.ptr == &tagInt64 || a.ptr == &tagBool) &&
		(b.ptr.Kind == KindBigInt || b.ptr == &tagInt64 || b.ptr == &tagBool) {
		obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType}}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		obj.Val.Mul(bigA, bigB)
		return Value{num: 0, ptr: &obj.Header}
	}
	panic("val.Mul: unsupported numeric operand types")
}

// LessThan performs Python less-than comparison (a < b) and returns a KindBool Value.
const (
	CmpLT uint8 = iota
	CmpLTE
	CmpEQ
	CmpNEQ
	CmpGT
	CmpGTE
	CmpIN
	CmpNOTIN
	CmpIS
	CmpISNOT
)

// RichCompare performs complete Python 3 rich comparisons (a <op> b)
// with subclass reflected precedence and NotImplemented fallback.
func RichCompare(ctx *ExecContext, a, b Value, op uint8) Value {
	// Fast path for identical integers
	if a.ptr == &tagInt64 && b.ptr == &tagInt64 {
		ai := int64(a.num)
		bi := int64(b.num)
		switch op {
		case CmpLT:
			return BoolVal(ai < bi)
		case CmpLTE:
			return BoolVal(ai <= bi)
		case CmpEQ:
			return BoolVal(ai == bi)
		case CmpNEQ:
			return BoolVal(ai != bi)
		case CmpGT:
			return BoolVal(ai > bi)
		case CmpGTE:
			return BoolVal(ai >= bi)
		case CmpIS:
			return BoolVal(ai == bi)
		case CmpISNOT:
			return BoolVal(ai != bi)
		}
	}

	// Identity comparisons: IS, IS_NOT
	if op == CmpIS {
		return BoolVal(a.ptr == b.ptr && a.num == b.num)
	}
	if op == CmpISNOT {
		return BoolVal(a.ptr != b.ptr || a.num != b.num)
	}

	// Containment tests: IN, NOT_IN
	if op == CmpIN {
		return In(a, b)
	}
	if op == CmpNOTIN {
		return NotIn(a, b)
	}

	aType := a.Type()
	bType := b.Type()

	var reflectedOp uint8
	switch op {
	case CmpLT:
		reflectedOp = CmpGT
	case CmpLTE:
		reflectedOp = CmpGTE
	case CmpEQ:
		reflectedOp = CmpEQ
	case CmpNEQ:
		reflectedOp = CmpNEQ
	case CmpGT:
		reflectedOp = CmpLT
	case CmpGTE:
		reflectedOp = CmpLTE
	}

	getOpDunder := func(tp *PyType, operation uint8) Value {
		if tp == nil {
			return Value{}
		}
		switch operation {
		case CmpLT:
			return tp.dunderLt
		case CmpLTE:
			return tp.dunderLe
		case CmpEQ:
			return tp.dunderEq
		case CmpNEQ:
			return tp.dunderNe
		case CmpGT:
			return tp.dunderGt
		case CmpGTE:
			return tp.dunderGe
		}
		return Value{}
	}

	isStrictSub := aType != bType && IsSubclass(bType, aType)

	// 1. If b is a strict subclass of a and defines the reflected comparison, try b first!
	if isStrictSub {
		if rFn := getOpDunder(bType, reflectedOp); rFn.ptr != nil {
			res := ctx.Invoke2(rFn, b, a)
			if !res.IsNotImplemented() {
				return res
			}
		}
	}

	// 2. Try a's comparison operator
	if fn := getOpDunder(aType, op); fn.ptr != nil {
		res := ctx.Invoke2(fn, a, b)
		if !res.IsNotImplemented() {
			return res
		}
	}

	// 3. Try b's reflected comparison operator (if not tried already)
	if !isStrictSub {
		if rFn := getOpDunder(bType, reflectedOp); rFn.ptr != nil {
			res := ctx.Invoke2(rFn, b, a)
			if !res.IsNotImplemented() {
				return res
			}
		}
	}

	// 4. Primitive / Built-in type fallbacks
	if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 || a.ptr == &tagInt64 || b.ptr == &tagInt64 || a.ptr.Kind == KindBigInt || b.ptr.Kind == KindBigInt {
		if (a.ptr == &tagFloat64 || a.ptr == &tagInt64 || a.ptr == &tagBool || a.ptr.Kind == KindBigInt) &&
			(b.ptr == &tagFloat64 || b.ptr == &tagInt64 || b.ptr == &tagBool || b.ptr.Kind == KindBigInt) {
			af := toFloat64(a)
			bf := toFloat64(b)
			switch op {
			case CmpLT:
				return BoolVal(af < bf)
			case CmpLTE:
				return BoolVal(af <= bf)
			case CmpEQ:
				return BoolVal(af == bf)
			case CmpNEQ:
				return BoolVal(af != bf)
			case CmpGT:
				return BoolVal(af > bf)
			case CmpGTE:
				return BoolVal(af >= bf)
			}
		}
	}

	if (a.ptr == &tagInlineStr || a.ptr.Kind == KindString) &&
		(b.ptr == &tagInlineStr || b.ptr.Kind == KindString) {
		as := a.AsString()
		bs := b.AsString()
		switch op {
		case CmpLT:
			return BoolVal(as < bs)
		case CmpLTE:
			return BoolVal(as <= bs)
		case CmpEQ:
			return BoolVal(as == bs)
		case CmpNEQ:
			return BoolVal(as != bs)
		case CmpGT:
			return BoolVal(as > bs)
		case CmpGTE:
			return BoolVal(as >= bs)
		}
	}

	// 5. Final fallback for EQ / NEQ is identity comparison
	if op == CmpEQ {
		return BoolVal(a.ptr == b.ptr && a.num == b.num)
	}
	if op == CmpNEQ {
		return BoolVal(a.ptr != b.ptr || a.num != b.num)
	}

	opSymbols := [...]string{"<", "<=", "==", "!=", ">", ">="}
	symbol := "comparison"
	if int(op) < len(opSymbols) {
		symbol = opSymbols[op]
	}
	panic("val: TypeError: '" + symbol + "' not supported between instances of '" + aType.Name + "' and '" + bType.Name + "'")
}

func LessThan(a, b Value) Value {
	return RichCompare(nil, a, b, CmpLT)
}

// GreaterThan performs Python greater-than comparison (a > b).
func GreaterThan(a, b Value) Value {
	return RichCompare(nil, a, b, CmpGT)
}

// LessOrEqual performs Python less-or-equal comparison (a <= b).
func LessOrEqual(a, b Value) Value {
	return RichCompare(nil, a, b, CmpLTE)
}

// GreaterOrEqual performs Python greater-or-equal comparison (a >= b).
func GreaterOrEqual(a, b Value) Value {
	return RichCompare(nil, a, b, CmpGTE)
}

// NotEquals performs Python inequality comparison (a != b).
func NotEquals(a, b Value) Value {
	return RichCompare(nil, a, b, CmpNEQ)
}

// GetItemCtx evaluates obj[key] via MpSubscript or SqItem type slots.
func GetItemCtx(ctx *ExecContext, obj, key Value) Value {
	tp := obj.Type()
	if tp.Slots.MpSubscript != nil {
		return tp.Slots.MpSubscript(ctx, obj, key)
	}
	if tp.Slots.SqItem != nil {
		return tp.Slots.SqItem(ctx, obj, key)
	}
	panic("val.GetItem: '" + tp.Name + "' object is not subscriptable")
}

// SetItemCtx evaluates obj[key] = value via MpAssSubscript or SqAssItem type slots.
func SetItemCtx(ctx *ExecContext, obj, key, value Value) {
	tp := obj.Type()
	if tp.Slots.MpAssSubscript != nil {
		tp.Slots.MpAssSubscript(ctx, obj, key, value)
		return
	}
	if tp.Slots.SqAssItem != nil {
		tp.Slots.SqAssItem(ctx, obj, key, value)
		return
	}
	panic("val.SetItem: '" + tp.Name + "' object does not support item assignment")
}

// GetAttrCtx evaluates obj.name via TpGetAttr type slot.
func GetAttrCtx(ctx *ExecContext, obj, name Value) Value {
	tp := obj.Type()
	if tp.Slots.TpGetAttr != nil {
		return tp.Slots.TpGetAttr(ctx, obj, name)
	}
	return objectGetAttr(ctx, obj, name)
}

// SetAttrCtx evaluates obj.name = value via TpSetAttr type slot.
func SetAttrCtx(ctx *ExecContext, obj, name, value Value) {
	tp := obj.Type()
	if tp.Slots.TpSetAttr != nil {
		tp.Slots.TpSetAttr(ctx, obj, name, value)
		return
	}
	objectSetAttr(ctx, obj, name, value)
}

func toBigInt(v Value) *big.Int {
	if v.ptr.Kind == KindBigInt {
		return &v.AsBigInt().Val
	}
	if v.ptr == &tagInt64 {
		var b big.Int
		b.SetInt64(int64(v.num))
		return &b
	}
	if v.ptr == &tagBool {
		var b big.Int
		b.SetInt64(int64(v.num))
		return &b
	}
	panic("val: cannot convert value to big.Int")
}

func toFloat64(v Value) float64 {
	if v.ptr == &tagFloat64 {
		return math.Float64frombits(v.num)
	}
	if v.ptr == &tagInt64 || v.ptr == &tagBool {
		return float64(int64(v.num))
	}
	if v.ptr.Kind == KindBigInt {
		f, _ := new(big.Float).SetInt(&v.AsBigInt().Val).Float64()
		return f
	}
	panic("val: cannot convert value to float64")
}

// FloorDiv performs Python floor division (a // b).
func FloorDiv(a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		ai := int64(a.num)
		bi := int64(b.num)
		if bi == 0 {
			panic("vm: ZeroDivisionError: integer division or modulo by zero")
		}
		res := ai / bi
		rem := ai % bi
		if (rem != 0) && ((ai < 0) != (bi < 0)) {
			res--
		}
		return Int64Val(res)
	}
	if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 {
		af := toFloat64(a)
		bf := toFloat64(b)
		if bf == 0 {
			panic("vm: ZeroDivisionError: float floor division by zero")
		}
		return Float64Val(math.Floor(af / bf))
	}
	panic("val.FloorDiv: unsupported operand types")
}

// TrueDiv performs Python true division (a / b).
func TrueDiv(a, b Value) Value {
	bf := toFloat64(b)
	if bf == 0 {
		panic("vm: ZeroDivisionError: division by zero")
	}
	af := toFloat64(a)
	return Float64Val(af / bf)
}

// Mod performs Python modulo operation (a % b).
func Mod(a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		ai := int64(a.num)
		bi := int64(b.num)
		if bi == 0 {
			panic("vm: ZeroDivisionError: integer modulo by zero")
		}
		rem := ai % bi
		if (rem != 0) && ((rem < 0) != (bi < 0)) {
			rem += bi
		}
		return Int64Val(rem)
	}
	if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 {
		af := toFloat64(a)
		bf := toFloat64(b)
		if bf == 0 {
			panic("vm: ZeroDivisionError: float modulo by zero")
		}
		rem := math.Mod(af, bf)
		if (rem != 0) && ((rem < 0) != (bf < 0)) {
			rem += bf
		}
		return Float64Val(rem)
	}
	panic("val.Mod: unsupported operand types")
}

// Pow performs Python exponentiation (a ** b).
func Pow(a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		ai := int64(a.num)
		bi := int64(b.num)
		if bi >= 0 && bi <= 62 {
			res := int64(1)
			base := ai
			exp := bi
			overflow := false
			for exp > 0 {
				if exp&1 != 0 {
					hi, lo := mul64Signed(res, base)
					if hi != (int64(lo) >> 63) {
						overflow = true
						break
					}
					res = int64(lo)
				}
				exp >>= 1
				if exp > 0 {
					hi, lo := mul64Signed(base, base)
					if hi != (int64(lo) >> 63) {
						overflow = true
						break
					}
					base = int64(lo)
				}
			}
			if !overflow {
				return Int64Val(res)
			}
		}
	}
	af := toFloat64(a)
	bf := toFloat64(b)
	return Float64Val(math.Pow(af, bf))
}

// Is performs Python identity comparison (a is b).
func Is(a, b Value) Value {
	return BoolVal(a.ptr == b.ptr && a.num == b.num)
}

// IsNot performs Python inverted identity comparison (a is not b).
func IsNot(a, b Value) Value {
	return BoolVal(a.ptr != b.ptr || a.num != b.num)
}

// In performs Python containment test (item in container).
func In(item, container Value) Value {
	if container.Type() != nil && container.Type().dunderContains.ptr != nil {
		res := (&ExecContext{}).Invoke2(container.Type().dunderContains, container, item)
		return BoolVal(res.AsBool())
	}
	if container.IsList() {
		return BoolVal(container.AsList().Contains(item))
	}
	if container.IsTuple() {
		items := container.AsTuple().Items
		for i := 0; i < len(items); i++ {
			if Equals(items[i], item) {
				return BoolVal(true)
			}
		}
		return BoolVal(false)
	}
	if container.IsDict() {
		_, ok := container.AsDict().Get(item)
		return BoolVal(ok)
	}
	if container.IsAnyString() && item.IsAnyString() {
		return BoolVal(strings.Contains(container.AsString(), item.AsString()))
	}
	if container.IsBytes() || container.IsByteArray() || container.IsMemoryView() {
		var buf []byte
		if container.IsBytes() {
			buf = container.AsBytes().Data
		} else if container.IsByteArray() {
			buf = container.AsByteArray().Data
		} else {
			buf = container.AsMemoryView().Buf
		}
		if item.IsInt64() {
			targetByte := byte(item.AsInt64())
			return BoolVal(bytes.IndexByte(buf, targetByte) >= 0)
		}
		if item.IsBytes() {
			return BoolVal(bytes.Contains(buf, item.AsBytes().Data))
		}
		if item.IsByteArray() {
			return BoolVal(bytes.Contains(buf, item.AsByteArray().Data))
		}
	}
	panic("val.In: type is not iterable or container")
}

// NotIn performs Python inverted containment test (item not in container).
func NotIn(item, container Value) Value {
	return BoolVal(!In(item, container).AsBool())
}

// NormalizeSliceIndices normalizes slice start, stop, step for a sequence of the given length according to PEP / Python slice semantics.
func NormalizeSliceIndices(startVal, stopVal, stepVal Value, length int) (start, stop, step, sliceLen int, err error) {
	step = 1
	if !stepVal.IsNone() && stepVal.ptr != nil {
		if !stepVal.IsInt64() {
			return 0, 0, 0, 0, errors.New("TypeError: slice step must be an integer")
		}
		step = int(stepVal.AsInt64())
		if step == 0 {
			return 0, 0, 0, 0, errors.New("ValueError: slice step cannot be zero")
		}
	}

	if step > 0 {
		if startVal.IsNone() || startVal.ptr == nil {
			start = 0
		} else {
			if !startVal.IsInt64() {
				return 0, 0, 0, 0, errors.New("TypeError: slice indices must be integers")
			}
			start = int(startVal.AsInt64())
			if start < 0 {
				start += length
				if start < 0 {
					start = 0
				}
			} else if start > length {
				start = length
			}
		}

		if stopVal.IsNone() || stopVal.ptr == nil {
			stop = length
		} else {
			if !stopVal.IsInt64() {
				return 0, 0, 0, 0, errors.New("TypeError: slice indices must be integers")
			}
			stop = int(stopVal.AsInt64())
			if stop < 0 {
				stop += length
				if stop < 0 {
					stop = 0
				}
			} else if stop > length {
				stop = length
			}
		}

		if start >= stop {
			sliceLen = 0
		} else {
			sliceLen = (stop - start + step - 1) / step
		}
	} else {
		// Negative step
		if startVal.IsNone() || startVal.ptr == nil {
			start = length - 1
		} else {
			if !startVal.IsInt64() {
				return 0, 0, 0, 0, errors.New("TypeError: slice indices must be integers")
			}
			start = int(startVal.AsInt64())
			if start < 0 {
				start += length
				if start < -1 {
					start = -1
				}
			} else if start >= length {
				start = length - 1
			}
		}

		if stopVal.IsNone() || stopVal.ptr == nil {
			stop = -1
		} else {
			if !stopVal.IsInt64() {
				return 0, 0, 0, 0, errors.New("TypeError: slice indices must be integers")
			}
			stop = int(stopVal.AsInt64())
			if stop < 0 {
				stop += length
				if stop < -1 {
					stop = -1
				}
			} else if stop >= length {
				stop = length - 1
			}
		}

		if start <= stop {
			sliceLen = 0
		} else {
			sliceLen = (stop - start + step + 1) / step
		}
	}

	return start, stop, step, sliceLen, nil
}

// BitAnd performs Python bitwise and (a & b).
func BitAnd(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		return Int64Val(int64(a.num) & int64(b.num))
	}
	if a.IsSet() && b.IsSet() {
		return a.AsSet().Intersection(b.AsSet())
	}
	if m, _, ok := a.Type().LookupMRO(InternString("__and__")); ok {
		if ctx != nil {
			return ctx.Invoke2(m, a, b)
		}
	}
	panic("TypeError: unsupported operand type(s) for &: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}

// BitOr performs Python bitwise or (a | b).
func BitOr(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		return Int64Val(int64(a.num) | int64(b.num))
	}
	if a.IsSet() && b.IsSet() {
		return a.AsSet().Union(b.AsSet())
	}
	if a.IsType() || b.IsType() {
		nameA := a.Type().Name
		if a.IsType() {
			nameA = a.AsType().Name
		}
		nameB := b.Type().Name
		if b.IsType() {
			nameB = b.AsType().Name
		}
		u := MustNewType(nameA+" | "+nameB, []*PyType{ObjectType}, nil)
		return TypeVal(u)
	}
	if m, _, ok := a.Type().LookupMRO(InternString("__or__")); ok {
		if ctx != nil {
			return ctx.Invoke2(m, a, b)
		}
	}
	panic("TypeError: unsupported operand type(s) for |: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}

// BitXor performs Python bitwise xor (a ^ b).
func BitXor(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		return Int64Val(int64(a.num) ^ int64(b.num))
	}
	if a.IsSet() && b.IsSet() {
		return a.AsSet().SymmetricDifference(b.AsSet())
	}
	if m, _, ok := a.Type().LookupMRO(InternString("__xor__")); ok {
		if ctx != nil {
			return ctx.Invoke2(m, a, b)
		}
	}
	panic("TypeError: unsupported operand type(s) for ^: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}

// LShift performs Python left shift (a << b).
func LShift(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		return Int64Val(int64(a.num) << uint64(b.num))
	}
	if m, _, ok := a.Type().LookupMRO(InternString("__lshift__")); ok {
		if ctx != nil {
			return ctx.Invoke2(m, a, b)
		}
	}
	panic("TypeError: unsupported operand type(s) for <<: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}

// RShift performs Python right shift (a >> b).
func RShift(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		return Int64Val(int64(a.num) >> uint64(b.num))
	}
	if m, _, ok := a.Type().LookupMRO(InternString("__rshift__")); ok {
		if ctx != nil {
			return ctx.Invoke2(m, a, b)
		}
	}
	panic("TypeError: unsupported operand type(s) for >>: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}

// Invert performs Python bitwise invert (~a).
func Invert(ctx *ExecContext, a Value) Value {
	if a.ptr == &tagInt64 || a.ptr == &tagBool {
		return Int64Val(^int64(a.num))
	}
	if m, _, ok := a.Type().LookupMRO(InternString("__invert__")); ok {
		if ctx != nil {
			return ctx.Invoke1(m, a)
		}
	}
	panic("TypeError: bad operand type for unary ~: '" + a.Type().Name + "'")
}

// MatMul performs Python matrix multiplication (a @ b).
func MatMul(ctx *ExecContext, a, b Value) Value {
	if m, _, ok := a.Type().LookupMRO(InternString("__matmul__")); ok {
		if ctx != nil {
			return ctx.Invoke2(m, a, b)
		}
	}
	panic("TypeError: unsupported operand type(s) for @: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}
