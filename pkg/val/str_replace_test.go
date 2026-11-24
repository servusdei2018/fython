package val_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestStrReplaceOneByteASCII(t *testing.T) {
	ctx := &val.ExecContext{}
	replaceFn, ok := val.StrType.Dict.Get(val.InternString("replace"))
	if !ok {
		t.Fatal("str.replace not found")
	}
	fn := replaceFn.AsBuiltinFunc().Fn

	// 1. Heap string miss returns exact same Value (0 allocs).
	s := val.NewStringVal("user_12345678")
	old := val.SmallStringVal("<")
	nw := val.SmallStringVal("&lt;")
	res := fn(ctx, replaceFn, []val.Value{s, old, nw}, nil)
	if res != s {
		t.Fatalf("expected identical Value on miss, got %v", res)
	}

	// 2. Inline string miss returns exact same Value.
	sInl := val.SmallStringVal("user_1")
	resInl := fn(ctx, replaceFn, []val.Value{sInl, old, nw}, nil)
	if resInl != sInl {
		t.Fatalf("expected identical inline Value on miss, got %v", resInl)
	}

	// 3. Heap string hit with expansion.
	sHit := val.NewStringVal("a<b>c<d>e")
	resHit := fn(ctx, replaceFn, []val.Value{sHit, old, nw}, nil)
	if resHit.AsString() != "a&lt;b>c&lt;d>e" {
		t.Fatalf("unexpected hit result: %q", resHit.AsString())
	}

	// 4. Heap string hit shrinking to <= 7 bytes returns inline str.
	sShrink := val.NewStringVal("a_b_c_d_e")
	resShrink := fn(ctx, replaceFn, []val.Value{sShrink, val.SmallStringVal("_"), val.SmallStringVal("")}, nil)
	if !resShrink.IsInlineStr() || resShrink.AsString() != "abcde" {
		t.Fatalf("expected inline str 'abcde', got kind=%v val=%q", resShrink.Kind(), resShrink.AsString())
	}

	// 5. Count limit (n=1 and n=0).
	resOne := fn(ctx, replaceFn, []val.Value{sHit, old, nw, val.Int64Val(1)}, nil)
	if resOne.AsString() != "a&lt;b>c<d>e" {
		t.Fatalf("unexpected count=1 result: %q", resOne.AsString())
	}
	resZero := fn(ctx, replaceFn, []val.Value{sHit, old, nw, val.Int64Val(0)}, nil)
	if resZero != sHit {
		t.Fatalf("expected identical Value for count=0, got %v", resZero)
	}

	// 6. Zero allocations on miss for heap and inline strings.
	argsHeap := []val.Value{s, old, nw}
	argsInl := []val.Value{sInl, old, nw}
	allocs := testing.AllocsPerRun(100, func() {
		_ = fn(ctx, replaceFn, argsHeap, nil)
		_ = fn(ctx, replaceFn, argsInl, nil)
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs on 1-byte ASCII miss, got %.1f", allocs)
	}
}
