package val

import (
	"bytes"
	"errors"
	"fmt"
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
	if r, ok := complexBinaryAny('+', a, b); ok {
		return r
	}
	return NotImplementedVal()
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
	if a.IsSet() && b.IsSet() {
		return a.AsSet().Difference(b.AsSet())
	}
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
	if r, ok := complexBinaryAny('-', a, b); ok {
		return r
	}
	return NotImplementedVal()
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
	if r, ok := complexBinaryAny('*', a, b); ok {
		return r
	}
	return NotImplementedVal()
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
		return InCtx(ctx, a, b)
	}
	if op == CmpNOTIN {
		return NotInCtx(ctx, a, b)
	}

	if op == CmpEQ || op == CmpNEQ {
		if a.Kind() == KindMappingProxy {
			a = a.AsMappingProxy().Mapping
		}
		if b.Kind() == KindMappingProxy {
			b = b.AsMappingProxy().Mapping
		}
	}

	if a.ptr.Kind == KindComplex || b.ptr.Kind == KindComplex {
		if r, ok := complexCompare(a, b, op); ok {
			return r
		}
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

	// Python data model: if __ne__ is not defined, delegate != to negated ==
	if op == CmpNEQ && ((aType != nil && aType.dunderEq.ptr != nil) || (bType != nil && bType.dunderEq.ptr != nil)) {
		return BoolVal(!RichCompare(ctx, a, b, CmpEQ).IsTruthy())
	}

	if a.ptr.Kind == KindTuple && b.ptr.Kind == KindTuple {
		return compareSliceRich(ctx, a.AsTuple().Items, b.AsTuple().Items, op)
	}
	if a.ptr.Kind == KindList && b.ptr.Kind == KindList {
		return compareSliceRich(ctx, a.AsList().Items, b.AsList().Items, op)
	}
	if a.ptr.Kind == KindSet && b.ptr.Kind == KindSet {
		sa := a.AsSet()
		sb := b.AsSet()
		switch op {
		case CmpEQ:
			return BoolVal(equalsStructural(a, b))
		case CmpNEQ:
			return BoolVal(!equalsStructural(a, b))
		case CmpLTE:
			if sa.Len() > sb.Len() {
				return BoolVal(false)
			}
			for _, k := range sa.Items() {
				if !sb.Contains(k) {
					return BoolVal(false)
				}
			}
			return BoolVal(true)
		case CmpLT:
			return BoolVal(sa.Len() < sb.Len() && RichCompare(ctx, a, b, CmpLTE).IsTruthy())
		case CmpGTE:
			return RichCompare(ctx, b, a, CmpLTE)
		case CmpGT:
			return BoolVal(sa.Len() > sb.Len() && RichCompare(ctx, b, a, CmpLTE).IsTruthy())
		}
	}

	// 5. Final fallback for EQ / NEQ uses structural/identity equality
	if op == CmpEQ {
		return BoolVal(equalsStructural(a, b))
	}
	if op == CmpNEQ {
		return BoolVal(!equalsStructural(a, b))
	}

	opSymbols := [...]string{"<", "<=", "==", "!=", ">", ">="}
	symbol := "comparison"
	if int(op) < len(opSymbols) {
		symbol = opSymbols[op]
	}
	panic("val: TypeError: '" + symbol + "' not supported between instances of '" + aType.Name + "' and '" + bType.Name + "'")
}

func compareSliceRich(ctx *ExecContext, ia, ib []Value, op uint8) Value {
	switch op {
	case CmpEQ:
		if len(ia) != len(ib) {
			return BoolVal(false)
		}
		for i := range ia {
			if !RichCompare(ctx, ia[i], ib[i], CmpEQ).IsTruthy() {
				return BoolVal(false)
			}
		}
		return BoolVal(true)
	case CmpNEQ:
		return BoolVal(!compareSliceRich(ctx, ia, ib, CmpEQ).IsTruthy())
	case CmpLT, CmpLTE, CmpGT, CmpGTE:
		minLen := len(ia)
		if len(ib) < minLen {
			minLen = len(ib)
		}
		for i := 0; i < minLen; i++ {
			if !RichCompare(ctx, ia[i], ib[i], CmpEQ).IsTruthy() {
				return RichCompare(ctx, ia[i], ib[i], op)
			}
		}
		switch op {
		case CmpLT:
			return BoolVal(len(ia) < len(ib))
		case CmpLTE:
			return BoolVal(len(ia) <= len(ib))
		case CmpGT:
			return BoolVal(len(ia) > len(ib))
		case CmpGTE:
			return BoolVal(len(ia) >= len(ib))
		}
	}
	return BoolVal(false)
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

// ObjectGetAttr performs default attribute lookup and does not re-enter TpGetAttr.
// Custom TpGetAttr slots call this for names they do not handle themselves.
func ObjectGetAttr(ctx *ExecContext, obj, name Value) Value {
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

// DelAttrCtx deletes obj.name via standard Python attribute deletion semantics.
func DelAttrCtx(ctx *ExecContext, obj, name Value) {
	objectDelAttr(ctx, obj, name)
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
	if isIntKind(a) && isIntKind(b) {
		q, _ := bigFloorDivMod(toBigInt(a), toBigInt(b))
		return normBig(q)
	}
	panic("val: TypeError: unsupported operand type(s) for //: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
}

// TrueDiv performs Python true division (a / b).
func TrueDiv(a, b Value) Value {
	if r, ok := complexBinaryAny('/', a, b); ok {
		return r
	}
	bf := toFloat64(b)
	if bf == 0 {
		panic("vm: ZeroDivisionError: division by zero")
	}
	af := toFloat64(a)
	return Float64Val(af / bf)
}

// Mod performs Python modulo operation (a % b).
func Mod(a, b Value) Value {
	if a.IsAnyString() {
		return formatStringMod(a.AsString(), b)
	}
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
	if isIntKind(a) && isIntKind(b) {
		_, r := bigFloorDivMod(toBigInt(a), toBigInt(b))
		return normBig(r)
	}
	panic("val: TypeError: unsupported operand type(s) for %: '" + a.Type().Name + "' and '" + b.Type().Name + "'")
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
	aInt, bInt := isIntKind(a), isIntKind(b)
	if aInt && bInt {
		bb := toBigInt(b)
		if bb.Sign() >= 0 {
			ba := toBigInt(a)
			if !bb.IsInt64() {
				switch {
				case ba.Sign() == 0 || ba.Cmp(big.NewInt(1)) == 0:
					return normBig(new(big.Int).Set(ba))
				case ba.Cmp(big.NewInt(-1)) == 0:
					if bb.Bit(0) == 0 {
						return Int64Val(1)
					}
					return Int64Val(-1)
				}
				panic("OverflowError: exponent too large")
			}
			if int64(ba.BitLen())*bb.Int64() > 1<<26 {
				panic("MemoryError: result of exponentiation is too large")
			}
			return normBig(new(big.Int).Exp(ba, bb, nil))
		}
		if toBigInt(a).Sign() == 0 {
			panic("vm: ZeroDivisionError: zero to a negative power")
		}
	}
	if a.Kind() == KindComplex || b.Kind() == KindComplex {
		return complexPow(a, b)
	}
	if !isNumberKind(a) || !isNumberKind(b) {
		panic("val: TypeError: unsupported operand type(s) for ** or pow(): '" + a.Type().Name + "' and '" + b.Type().Name + "'")
	}
	af := toFloat64(a)
	bf := toFloat64(b)
	if af == 0 && bf < 0 {
		panic("vm: ZeroDivisionError: zero to a negative power")
	}
	if af < 0 && !math.IsInf(bf, 0) && bf != math.Floor(bf) {
		return complexPow(Float64Val(af), Float64Val(bf))
	}
	return Float64Val(math.Pow(af, bf))
}

func isIntKind(v Value) bool {
	return v.ptr == &tagInt64 || v.ptr == &tagBool || v.ptr.Kind == KindBigInt
}

func isNumberKind(v Value) bool {
	return isIntKind(v) || v.ptr == &tagFloat64
}

// normBig converts a big.Int to an unboxed int64 when it fits.
func normBig(x *big.Int) Value {
	if x.IsInt64() {
		return Int64Val(x.Int64())
	}
	return NewBigIntVal(x)
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
	return InCtx(nil, item, container)
}

// InCtx performs Python containment test (item in container) with an optional ExecContext.
func InCtx(ctx *ExecContext, item, container Value) Value {
	cType := container.Type()
	if container.IsSet() && cType == SetType {
		return BoolVal(container.AsSet().Contains(item))
	}
	if container.IsList() && cType == ListType {
		return BoolVal(container.AsList().Contains(item))
	}
	if container.IsTuple() && cType == TupleType {
		items := container.AsTuple().Items
		for i := 0; i < len(items); i++ {
			if Equals(items[i], item) {
				return BoolVal(true)
			}
		}
		return BoolVal(false)
	}
	if container.IsDict() && cType == DictType {
		_, ok := container.AsDict().Get(item)
		return BoolVal(ok)
	}
	if container.IsAnyString() && cType == StrType {
		if !item.IsAnyString() {
			panic("TypeError: 'in <string>' requires string as left operand, not " + item.Type().Name)
		}
		return BoolVal(strings.Contains(container.AsString(), item.AsString()))
	}
	if cType != nil {
		if cType.dunderContains.ptr != nil {
			if cType.dunderContains.IsNone() {
				panic("TypeError: argument of type '" + cType.Name + "' is not iterable")
			}
			res := ctx.Invoke2(cType.dunderContains, container, item)
			return BoolVal(res.IsTruthy())
		}
		if fn, _, ok := cType.LookupMRO(DunderContains); ok {
			if fn.IsNone() {
				panic("TypeError: argument of type '" + cType.Name + "' is not iterable")
			}
			res := ctx.Invoke2(fn, container, item)
			return BoolVal(res.IsTruthy())
		}
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
			v := item.AsInt64()
			if v < 0 || v > 255 {
				panic("ValueError: byte must be in range(0, 256)")
			}
			return BoolVal(bytes.IndexByte(buf, byte(v)) >= 0)
		}
		if item.IsBytes() {
			return BoolVal(bytes.Contains(buf, item.AsBytes().Data))
		}
		if item.IsByteArray() {
			return BoolVal(bytes.Contains(buf, item.AsByteArray().Data))
		}
		panic("TypeError: a bytes-like object is required, not '" + item.Type().Name + "'")
	}
	if ctx == nil && AcquireExecContextHook != nil {
		var release func()
		ctx, release = AcquireExecContextHook()
		defer release()
	}
	var it Value
	if cType != nil && cType.Slots.TpIter != nil {
		it = cType.Slots.TpIter(ctx, container)
	} else {
		it = NewIterVal(container)
	}
	if ctx != nil && ctx.HasException() {
		return BoolVal(false)
	}
	itNext := it.Type().Slots.TpIterNext
	if itNext == nil {
		typeName := "object"
		if cType != nil {
			typeName = cType.Name
		}
		panic("TypeError: argument of type '" + typeName + "' is not iterable")
	}
	for {
		elem, status := itNext(ctx, it)
		if status == IterError || (ctx != nil && ctx.HasException()) {
			return BoolVal(false)
		}
		if status == IterExhausted {
			return BoolVal(false)
		}
		if elem == item || Equals(elem, item) {
			return BoolVal(true)
		}
	}
}

// NotIn performs Python inverted containment test (item not in container).
func NotIn(item, container Value) Value {
	return NotInCtx(nil, item, container)
}

// NotInCtx performs Python inverted containment test (item not in container) with an optional ExecContext.
func NotInCtx(ctx *ExecContext, item, container Value) Value {
	res := InCtx(ctx, item, container)
	if ctx != nil && ctx.HasException() {
		return BoolVal(false)
	}
	return BoolVal(!res.AsBool())
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

// UnionOrHook builds a PEP 604 union when both operands are unionable.
// The VM installs this once the native Union type exists. A false ok
// means the operands are not union members and BitOr should continue
// with the normal __or__ lookup.
var UnionOrHook func(ctx *ExecContext, a, b Value) (Value, bool)

// BitOr performs Python bitwise or (a | b).
func BitOr(ctx *ExecContext, a, b Value) Value {
	if (a.ptr == &tagInt64 || a.ptr == &tagBool) && (b.ptr == &tagInt64 || b.ptr == &tagBool) {
		return Int64Val(int64(a.num) | int64(b.num))
	}
	if a.IsSet() && b.IsSet() {
		return a.AsSet().Union(b.AsSet())
	}
	if UnionOrHook != nil {
		if v, ok := UnionOrHook(ctx, a, b); ok {
			return v
		}
	} else if a.IsType() || b.IsType() {
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
	if a.IsDict() && b.IsDict() {
		merged := NewDictVal(a.AsDict().Len() + b.AsDict().Len())
		md := merged.AsDict()
		for _, e := range a.AsDict().ActiveEntries() {
			md.Set(e.Key, e.Value)
		}
		for _, e := range b.AsDict().ActiveEntries() {
			md.Set(e.Key, e.Value)
		}
		return merged
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

func formatStringMod(fmtStr string, arg Value) Value {
	var items []Value
	var argDict *PyDict
	if arg.IsTuple() {
		items = arg.AsTuple().Items
	} else if arg.IsDict() && strings.Contains(fmtStr, "%(") {
		argDict = arg.AsDict()
	} else {
		items = []Value{arg}
	}

	var sb strings.Builder
	itemIdx := 0
	i := 0
	n := len(fmtStr)
	hasAnyMapKey := false

	for i < n {
		ch := fmtStr[i]
		if ch != '%' {
			sb.WriteByte(ch)
			i++
			continue
		}
		if i+1 < n && fmtStr[i+1] == '%' {
			sb.WriteByte('%')
			i += 2
			continue
		}
		i++ // skip '%'

		// Check for mapping key: %(name)...
		var mapKey string
		hasMapKey := false
		if i < n && fmtStr[i] == '(' {
			startKey := i + 1
			closeIdx := strings.IndexByte(fmtStr[startKey:], ')')
			if closeIdx >= 0 {
				mapKey = fmtStr[startKey : startKey+closeIdx]
				hasMapKey = true
				hasAnyMapKey = true
				i = startKey + closeIdx + 1
			}
		}

		// Flags / width / precision: e.g. -0+ #, width, .prec
		startSpec := i
		for i < n && (fmtStr[i] == '-' || fmtStr[i] == '+' || fmtStr[i] == '0' || fmtStr[i] == ' ' || fmtStr[i] == '#' || (fmtStr[i] >= '0' && fmtStr[i] <= '9') || fmtStr[i] == '.') {
			i++
		}
		if i >= n {
			panic("ValueError: incomplete format")
		}
		specifier := fmtStr[startSpec:i]
		convType := fmtStr[i]
		i++

		var val Value
		if hasMapKey {
			if argDict == nil {
				panic("TypeError: format requires a mapping")
			}
			var ok bool
			val, ok = argDict.Get(InternString(mapKey))
			if !ok {
				panic(NewException(KeyErrorType, fmt.Sprintf("KeyError: %s", mapKey)))
			}
		} else {
			if itemIdx >= len(items) {
				panic("TypeError: not enough arguments for format string")
			}
			val = items[itemIdx]
			itemIdx++
		}

		// Convert value based on convType
		var rendered string
		switch convType {
		case 's':
			if val.IsAnyString() {
				rendered = val.AsString()
			} else {
				rendered = modConvert(val, PyStr)
			}
		case 'r':
			rendered = modConvert(val, PyRepr)
		case 'a':
			rendered = asciiEscape(modConvert(val, PyRepr))
		case 'd', 'i', 'u':
			rendered = fmt.Sprintf("%"+specifier+"d", val.AsInt64())
		case 'x':
			rendered = fmt.Sprintf("%"+specifier+"x", val.AsInt64())
		case 'X':
			rendered = fmt.Sprintf("%"+specifier+"X", val.AsInt64())
		case 'o':
			rendered = fmt.Sprintf("%"+specifier+"o", val.AsInt64())
		case 'f', 'F':
			fVal := toFloat64(val)
			rendered = fmt.Sprintf("%"+specifier+"f", fVal)
		case 'e', 'E':
			fVal := toFloat64(val)
			rendered = fmt.Sprintf("%"+specifier+"e", fVal)
		case 'g', 'G':
			fVal := toFloat64(val)
			rendered = fmt.Sprintf("%"+specifier+"g", fVal)
		case 'c':
			if val.IsAnyString() {
				rendered = val.AsString()
			} else if val.IsInt64() {
				rendered = string(rune(val.AsInt64()))
			} else {
				panic("TypeError: %c requires int or char")
			}
		default:
			rendered = val.String()
		}

		// Apply specifier padding if not already handled
		if specifier != "" && (convType == 's' || convType == 'r' || convType == 'a') {
			rendered = fmt.Sprintf("%"+specifier+"s", rendered)
		}
		sb.WriteString(rendered)
	}

	if !hasAnyMapKey && itemIdx < len(items) {
		panic("TypeError: not all arguments converted during string formatting")
	}

	return NewStringVal(sb.String())
}

// modConvert renders v with fn, borrowing a VM context when one is
// available so user-defined __str__ and __repr__ run.
func modConvert(v Value, fn func(*ExecContext, Value) string) string {
	if AcquireExecContextHook != nil {
		ctx, release := AcquireExecContextHook()
		defer release()
		return fn(ctx, v)
	}
	return fn(nil, v)
}

// bigFloorDivMod returns Python floor division and modulo for big integers.
func bigFloorDivMod(a, b *big.Int) (*big.Int, *big.Int) {
	if b.Sign() == 0 {
		panic("vm: ZeroDivisionError: integer division or modulo by zero")
	}
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if r.Sign() != 0 && (r.Sign() < 0) != (b.Sign() < 0) {
		q.Sub(q, big.NewInt(1))
		r.Add(r, b)
	}
	return q, r
}
