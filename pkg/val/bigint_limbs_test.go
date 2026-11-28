package val_test

import (
	"math"
	"math/big"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func bigVal(s string) val.Value {
	bi, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad bigint literal " + s)
	}
	return val.NewBigIntVal(bi)
}

func TestLimbsNormalizeInt64(t *testing.T) {
	ctx := &val.ExecContext{}
	// 0, MaxInt64, and small values normalize back to inline int.
	zero := val.BitAnd(ctx, bigVal("0"), bigVal("12345"))
	if !zero.IsInt64() || zero.AsInt64() != 0 {
		t.Fatalf("0 & 12345 = %v, want int 0", zero)
	}
	maxStr := "9223372036854775807"
	m := val.BitOr(ctx, bigVal(maxStr), bigVal("0"))
	if !m.IsInt64() || m.AsInt64() != math.MaxInt64 {
		t.Fatalf("MaxInt64|0 = %v, want int MaxInt64", m)
	}
	// True+True == 2 via limb path (bool operands).
	two := val.BitOr(ctx, val.BoolVal(true), val.Int64Val(1))
	if !two.IsInt64() || two.AsInt64() != 1 {
		// True|1 == 1; use add for 1+1 below.
		t.Fatalf("True|1 = %v, want 1", two)
	}
	sum := val.AddCtx(ctx, val.BoolVal(true), val.BoolVal(true))
	if !sum.IsInt64() || sum.AsInt64() != 2 {
		t.Fatalf("True+True = %v, want int 2", sum)
	}
}

func TestLimbsCarryOverflowNoTruncate(t *testing.T) {
	ctx := &val.ExecContext{}
	// (1<<255) + (1<<255) == 1<<256 needs 5 limbs worth (257 bits with carry).
	a := bigVal("57896044618658097711785492504343953926634992332820282019728792003956564819968") // 1<<255
	sum := val.AddCtx(ctx, a, a)
	want, _ := new(big.Int).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639936", 10) // 1<<256
	got := val.ToBigInt(sum)
	if got.Cmp(want) != 0 {
		t.Fatalf("1<<255 + 1<<255 = %s, want %s", got.String(), want.String())
	}
	if sum.IsInt64() {
		t.Fatalf("1<<256 must not normalize to int64")
	}
	// (1<<256 - 1) + 1 carries across all 4 limbs.
	all := bigVal("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	one := val.AddCtx(ctx, all, val.Int64Val(1))
	if val.ToBigInt(one).Cmp(want) != 0 {
		t.Fatalf("(2^256-1)+1 = %s, want %s", val.ToBigInt(one).String(), want.String())
	}
	// Sub borrow across limbs: 1<<128 - 1 == 2^128-1.
	top := bigVal("340282366920938463463374607431768211456") // 1<<128
	diff := val.SubCtx(ctx, top, val.Int64Val(1))
	wantSub, _ := new(big.Int).SetString("340282366920938463463374607431768211455", 10)
	if val.ToBigInt(diff).Cmp(wantSub) != 0 {
		t.Fatalf("1<<128-1 = %s, want %s", val.ToBigInt(diff).String(), wantSub.String())
	}
}

func TestLimbsSignedAndSubclassParity(t *testing.T) {
	ctx := &val.ExecContext{}
	// Negative operands fall back to big.Int with correct sign.
	// Note: BigInt results that fit int64 stay boxed as BigInt by Add/Sub
	// (existing Fython representation invariant); check numeric value.
	neg := val.AddCtx(ctx, bigVal("-5"), val.Int64Val(3))
	if val.ToBigInt(neg).Cmp(big.NewInt(-2)) != 0 {
		t.Fatalf("-5+3 = %v, want -2", neg)
	}
	// 3 - 5 == -2 via int64 fast path normalizes to int.
	neg2 := val.SubCtx(ctx, val.Int64Val(3), val.Int64Val(5))
	if !neg2.IsInt64() || neg2.AsInt64() != -2 {
		t.Fatalf("3-5 = %v, want -2", neg2)
	}
	// Bitwise on negatives uses big.Int two's complement parity.
	andNeg := val.BitAnd(ctx, val.Int64Val(-1), bigVal("255"))
	if !andNeg.IsInt64() || andNeg.AsInt64() != 255 {
		t.Fatalf("-1 & 255 = %v, want 255", andNeg)
	}
	// Comparisons across sign boundary.
	if !val.RichCompare(ctx, bigVal("-1"), val.Int64Val(0), val.CmpLT).IsTruthy() {
		t.Fatalf("-1 < 0 must be true")
	}
	if !val.RichCompare(ctx, bigVal("115792089237316195423570985008687907853269984665640564039457584007913129639936"), bigVal("0"), val.CmpGT).IsTruthy() {
		t.Fatalf("1<<256 > 0 must be true")
	}
}
