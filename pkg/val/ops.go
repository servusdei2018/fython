package val

import (
	"math"
	"math/big"
	"math/bits"
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
	// Direct TypeSlot dispatch (includes str, list, bigint, and custom __add__ classes).
	if nbAdd := a.Type().Slots.NbAdd; nbAdd != nil {
		return nbAdd(ctx, a, b)
	}
	panic("val.Add: unsupported operand types")
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
		return NewStringVal(a.AsString() + b.AsString())
	}
	panic("val.Add: can only concatenate str to str")
}

func listNbAdd(ctx *ExecContext, a, b Value) Value {
	if a.ptr.Kind == KindList && b.ptr.Kind == KindList {
		la := a.AsList().Items
		lb := b.AsList().Items
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
	if nbSub := a.Type().Slots.NbSub; nbSub != nil {
		return nbSub(ctx, a, b)
	}
	panic("val.Sub: unsupported operand types")
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
	if nbMul := a.Type().Slots.NbMul; nbMul != nil {
		return nbMul(ctx, a, b)
	}
	panic("val.Mul: unsupported operand types")
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
func LessThan(a, b Value) Value {
	if a.ptr == &tagInt64 && b.ptr == &tagInt64 {
		if int64(a.num) < int64(b.num) {
			return Value{num: 1, ptr: &tagBool}
		}
		return Value{num: 0, ptr: &tagBool}
	}

	if a.ptr == &tagFloat64 && b.ptr == &tagFloat64 {
		return BoolVal(math.Float64frombits(a.num) < math.Float64frombits(b.num))
	}
	if a.ptr == &tagInt64 && b.ptr == &tagFloat64 {
		return BoolVal(float64(int64(a.num)) < math.Float64frombits(b.num))
	}
	if a.ptr == &tagFloat64 && b.ptr == &tagInt64 {
		return BoolVal(math.Float64frombits(a.num) < float64(int64(b.num)))
	}

	return lessThanSlow(a, b)
}

//go:noinline
func lessThanSlow(a, b Value) Value {
	if a.ptr.Kind == KindBigInt || b.ptr.Kind == KindBigInt {
		if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 {
			return BoolVal(toFloat64(a) < toFloat64(b))
		}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		return BoolVal(bigA.Cmp(bigB) < 0)
	}
	if (a.ptr == &tagInlineStr || a.ptr.Kind == KindString) &&
		(b.ptr == &tagInlineStr || b.ptr.Kind == KindString) {
		return BoolVal(a.AsString() < b.AsString())
	}
	panic("val.LessThan: unsupported operand types")
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
