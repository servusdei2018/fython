package compiler

import (
	"math"

	"github.com/servusdei2018/fython/pkg/val"
)

// ConstPool manages type- and value-sensitive constant deduplication within a single CodeObject.
type ConstPool struct {
	consts []val.Value
}

// NewConstPool constructs an empty ConstPool.
func NewConstPool() *ConstPool {
	return &ConstPool{
		consts: make([]val.Value, 0, 16),
	}
}

// Add deduplicates v with strict type+value sensitivity and returns its 16-bit constant pool index.
// Guaranteed: 1 (int), 1.0 (float), and True (bool) will NEVER share a constant slot.
func (cp *ConstPool) Add(v val.Value) uint16 {
	for i, existing := range cp.consts {
		if exactTypeAndValueEqual(v, existing) {
			return uint16(i)
		}
	}
	idx := uint16(len(cp.consts))
	cp.consts = append(cp.consts, v)
	return idx
}

// InternName interns an identifier string at compile time and stores it in the constant pool.
func (cp *ConstPool) InternName(name string) (val.Value, uint16) {
	interned := val.InternString(name)
	idx := cp.Add(interned)
	return interned, idx
}

// Slice returns the accumulated constant pool slice.
func (cp *ConstPool) Slice() []val.Value {
	return cp.consts
}

// exactTypeAndValueEqual enforces Python compile-time constant deduplication semantics.
func exactTypeAndValueEqual(a, b val.Value) bool {
	// 1. Strict Kind equality check
	if a.Kind() != b.Kind() {
		return false
	}

	// 2. Exact value check based on Kind
	switch a.Kind() {
	case val.KindNone:
		return true

	case val.KindBool:
		return a.AsBool() == b.AsBool()

	case val.KindInt64:
		return a.AsInt64() == b.AsInt64()

	case val.KindFloat64:
		// math.Float64bits ensures exact IEEE-754 bit-pattern equality (differentiating 0.0 vs -0.0)
		return math.Float64bits(a.AsFloat64()) == math.Float64bits(b.AsFloat64())

	case val.KindComplex:
		return math.Float64bits(a.AsComplex().Real) == math.Float64bits(b.AsComplex().Real) &&
			math.Float64bits(a.AsComplex().Imag) == math.Float64bits(b.AsComplex().Imag)

	case val.KindInlineStr, val.KindString:
		return a.AsString() == b.AsString()

	case val.KindTuple:
		ta := a.AsTuple().Items
		tb := b.AsTuple().Items
		if len(ta) != len(tb) {
			return false
		}
		for i := range ta {
			if !exactTypeAndValueEqual(ta[i], tb[i]) {
				return false
			}
		}
		return true

	case val.KindCode:
		// Code objects are distinct by pointer
		return a == b

	default:
		return a == b
	}
}
