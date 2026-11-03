package val

import (
	"math"
	"math/big"
	"math/bits"
)

// Add performs overflow-safe Python addition (a + b).
// Fast path: int64 + int64 with bitwise overflow check (0 allocations when non-overflowing).
// Overflow spill path: promotes to *PyBigInt on signed 64-bit overflow.
// Mixed int/float path: coerces KindInt64 to float64 when paired with KindFloat64.
func Add(a, b Value) Value {
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

	return addSlow(a, b)
}

//go:noinline
func addInt64Overflow(ai, bi int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt},
	}
	var bigA, bigB big.Int
	bigA.SetInt64(ai)
	bigB.SetInt64(bi)
	obj.Val.Add(&bigA, &bigB)
	return Value{num: 0, ptr: &obj.Header}
}

//go:noinline
func addSlow(a, b Value) Value {
	// String concatenation (inline or heap strings).
	if (a.ptr == &tagInlineStr || a.ptr.Kind == KindString) &&
		(b.ptr == &tagInlineStr || b.ptr.Kind == KindString) {
		return NewStringVal(a.AsString() + b.AsString())
	}

	// BigInt arithmetic.
	if a.ptr.Kind == KindBigInt || b.ptr.Kind == KindBigInt {
		if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 {
			return Float64Val(toFloat64(a) + toFloat64(b))
		}
		obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt}}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		obj.Val.Add(bigA, bigB)
		return Value{num: 0, ptr: &obj.Header}
	}

	// List concatenation.
	if a.ptr.Kind == KindList && b.ptr.Kind == KindList {
		la := a.AsList().Items
		lb := b.AsList().Items
		merged := make([]Value, len(la)+len(lb))
		copy(merged, la)
		copy(merged[len(la):], lb)
		return NewListVal(merged...)
	}

	panic("val.Add: unsupported operand types")
}

// Sub performs overflow-safe Python subtraction (a - b).
// Fast path: int64 - int64 with bitwise overflow check (0 allocations when non-overflowing).
func Sub(a, b Value) Value {
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

	return subSlow(a, b)
}

//go:noinline
func subInt64Overflow(ai, bi int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt},
	}
	var bigA, bigB big.Int
	bigA.SetInt64(ai)
	bigB.SetInt64(bi)
	obj.Val.Sub(&bigA, &bigB)
	return Value{num: 0, ptr: &obj.Header}
}

//go:noinline
func subSlow(a, b Value) Value {
	if a.ptr.Kind == KindBigInt || b.ptr.Kind == KindBigInt {
		if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 {
			return Float64Val(toFloat64(a) - toFloat64(b))
		}
		obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt}}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		obj.Val.Sub(bigA, bigB)
		return Value{num: 0, ptr: &obj.Header}
	}
	panic("val.Sub: unsupported operand types")
}

// Mul performs overflow-safe Python multiplication (a * b).
// Fast path: int64 * int64 using math/bits.Mul64 to compute exact 128-bit product (0 allocations).
func Mul(a, b Value) Value {
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

	return mulSlow(a, b)
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
		Header: ObjectHeader{Kind: KindBigInt},
	}
	var bigA, bigB big.Int
	bigA.SetInt64(ai)
	bigB.SetInt64(bi)
	obj.Val.Mul(&bigA, &bigB)
	return Value{num: 0, ptr: &obj.Header}
}

//go:noinline
func mulSlow(a, b Value) Value {
	if a.ptr.Kind == KindBigInt || b.ptr.Kind == KindBigInt {
		if a.ptr == &tagFloat64 || b.ptr == &tagFloat64 {
			return Float64Val(toFloat64(a) * toFloat64(b))
		}
		obj := &PyBigInt{Header: ObjectHeader{Kind: KindBigInt}}
		bigA := toBigInt(a)
		bigB := toBigInt(b)
		obj.Val.Mul(bigA, bigB)
		return Value{num: 0, ptr: &obj.Header}
	}
	panic("val.Mul: unsupported operand types")
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
	if v.ptr == &tagInt64 {
		return float64(int64(v.num))
	}
	if v.ptr.Kind == KindBigInt {
		f, _ := new(big.Float).SetInt(&v.AsBigInt().Val).Float64()
		return f
	}
	panic("val: cannot convert value to float64")
}
