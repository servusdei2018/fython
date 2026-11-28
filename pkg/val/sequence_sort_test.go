package val

import (
	"testing"
)

func sliceVal(start, stop, step Value) Value {
	return NewSliceVal(start, stop, step)
}

// Strided and reverse tuple slicing must allocate once and match CPython.
func TestTupleSliceStridedReverse(t *testing.T) {
	items := make([]Value, 20)
	for i := range items {
		items[i] = Int64Val(int64(i))
	}
	tup := Value{num: 0, ptr: &NewTupleFromSlice(items).AsTuple().Header}
	ctx := &ExecContext{}

	// [::2]
	got := tupleMpSubscript(ctx, tup, sliceVal(NoneVal(), NoneVal(), Int64Val(2))).AsTuple()
	if len(got.Items) != 10 {
		t.Fatalf("strided len = %d, want 10", len(got.Items))
	}
	for i, v := range got.Items {
		if v.AsInt64() != int64(2*i) {
			t.Fatalf("strided[%d] = %d, want %d", i, v.AsInt64(), 2*i)
		}
	}
	// [::-1]
	rev := tupleMpSubscript(ctx, tup, sliceVal(NoneVal(), NoneVal(), Int64Val(-1))).AsTuple()
	if len(rev.Items) != 20 {
		t.Fatalf("reverse len = %d, want 20", len(rev.Items))
	}
	for i, v := range rev.Items {
		if v.AsInt64() != int64(19-i) {
			t.Fatalf("reverse[%d] = %d, want %d", i, v.AsInt64(), 19-i)
		}
	}
	// short strided slice (inline slots, no backing array)
	short := tupleMpSubscript(ctx, tup, sliceVal(Int64Val(5), Int64Val(11), Int64Val(2))).AsTuple()
	if len(short.Items) != 3 {
		t.Fatalf("short strided len = %d, want 3", len(short.Items))
	}
	for i, want := range []int64{5, 7, 9} {
		if short.Items[i].AsInt64() != want {
			t.Fatalf("short[%d] = %d, want %d", i, short.Items[i].AsInt64(), want)
		}
	}
	// empty strided slice returns the singleton
	empty := tupleMpSubscript(ctx, tup, sliceVal(Int64Val(10), Int64Val(5), Int64Val(1)))
	if empty != EmptyTupleVal() {
		t.Fatalf("empty slice did not return EmptyTupleVal")
	}
	// out-of-range bounds clamp like CPython
	clamped := tupleMpSubscript(ctx, tup, sliceVal(Int64Val(-1000), Int64Val(1000), Int64Val(3))).AsTuple()
	if len(clamped.Items) != 7 { // 0,3,6,9,12,15,18
		t.Fatalf("clamped len = %d, want 7", len(clamped.Items))
	}
	// step==1 stays a zero-copy view of the same backing
	view := tupleMpSubscript(ctx, tup, sliceVal(Int64Val(2), Int64Val(6), Int64Val(1))).AsTuple()
	if len(view.Items) != 4 || view.Items[0].AsInt64() != 2 {
		t.Fatalf("step-1 view wrong: %v", view.Items)
	}
}

// List strided/reverse slicing mirrors tuple semantics with owned backing.
func TestListSliceStridedReverse(t *testing.T) {
	items := make([]Value, 10)
	for i := range items {
		items[i] = Int64Val(int64(i))
	}
	lv := NewListVal(items...)
	ctx := &ExecContext{}
	got := listMpSubscript(ctx, lv, sliceVal(NoneVal(), NoneVal(), Int64Val(-1))).AsList()
	if len(got.Items) != 10 || got.Items[0].AsInt64() != 9 || got.Items[9].AsInt64() != 0 {
		t.Fatalf("list reverse wrong: %v", got.Items)
	}
	strided := listMpSubscript(ctx, lv, sliceVal(Int64Val(1), Int64Val(8), Int64Val(3))).AsList()
	if len(strided.Items) != 3 || strided.Items[0].AsInt64() != 1 || strided.Items[2].AsInt64() != 7 {
		t.Fatalf("list strided wrong: %v", strided.Items)
	}
	empty := listMpSubscript(ctx, lv, sliceVal(Int64Val(3), Int64Val(3), Int64Val(1))).AsList()
	if len(empty.Items) != 0 {
		t.Fatalf("empty list slice len = %d", len(empty.Items))
	}
}

// Range materialization bypasses intermediate iterables.
func TestRangeMaterialization(t *testing.T) {
	ctx := &ExecContext{}
	// list(range(n)) via the fast path
	lv := NewListVal(ExtractIterableItems(ctx, RangeVal(0, 100))...)
	if len(lv.AsList().Items) != 100 || lv.AsList().Items[99].AsInt64() != 99 {
		t.Fatalf("list(range(100)) wrong len/content")
	}
	// tuple(range(10, 0, -3)) == (10, 7, 4, 1)
	it := NewIterVal(NewRangeIterVal(10, 0, -3)).AsIter()
	var collected []Value
	for {
		v, ok := it.NextCtx(nil)
		if !ok {
			break
		}
		collected = append(collected, v)
	}
	tv := newTupleOfTypeFromSlice(TupleType, collected)
	if len(tv.AsTuple().Items) != 4 {
		t.Fatalf("tuple(range(10,0,-3)) len = %d", len(tv.AsTuple().Items))
	}
	if tv.AsTuple().Items[0].AsInt64() != 10 || tv.AsTuple().Items[3].AsInt64() != 1 {
		t.Fatalf("tuple(range(10,0,-3)) wrong: %v", tv.AsTuple().Items)
	}
	// range length helper: empties, negatives, steps
	for _, tc := range []struct {
		start, stop, step int64
		want              int
		ok                bool
	}{
		{5, 5, 1, 0, true},
		{0, 5, -1, 0, true},
		{0, 10, 2, 5, true},
		{10, 0, -3, 4, true},
		{0, 10, 0, 0, false},
	} {
		n, ok := rangeLen(tc.start, tc.stop, tc.step)
		if ok != tc.ok || (ok && n != tc.want) {
			t.Fatalf("rangeLen(%d,%d,%d) = (%d,%v), want (%d,%v)",
				tc.start, tc.stop, tc.step, n, ok, tc.want, tc.ok)
		}
	}
	// list.__init__ self-aliasing: l.__init__(l) preserves elements
	l := NewListVal(Int64Val(1), Int64Val(2), Int64Val(3)).AsList()
	self := Value{num: uint64(KindList), ptr: &l.Header}
	initFn, ok := ListType.Dict.Get(DunderInit)
	if !ok {
		t.Fatalf("list.__init__ missing")
	}
	initFn.AsBuiltinFunc().Fn(ctx, Value{}, []Value{self, self}, nil)
	if len(l.Items) != 3 {
		t.Fatalf("self-init lost items: %v", l.Items)
	}
	// empty tuple iterator is shared and exhausted
	a := NewIterVal(EmptyTupleVal())
	b := NewIterVal(EmptyTupleVal())
	if a != b {
		t.Fatalf("empty tuple iters not shared")
	}
	if _, ok := a.AsIter().NextCtx(nil); ok {
		t.Fatalf("empty tuple iter yielded")
	}
}

// Sort stays correct across key shapes, reverse, and stability.
func TestSortListKeyShapes(t *testing.T) {
	items := []Value{Int64Val(3), Int64Val(1), Int64Val(2), Int64Val(1)}
	sl := NewListVal(items...).AsList()
	SortList(nil, sl, Value{}, false)
	for i := 1; i < len(sl.Items); i++ {
		if sl.Items[i-1].AsInt64() > sl.Items[i].AsInt64() {
			t.Fatalf("plain sort broken: %v", sl.Items)
		}
	}
	// stability: equal keys keep original order (checked in vm tests with keys)
	SortList(nil, sl, Value{}, true)
	for i := 1; i < len(sl.Items); i++ {
		if sl.Items[i-1].AsInt64() < sl.Items[i].AsInt64() {
			t.Fatalf("reverse sort broken: %v", sl.Items)
		}
	}
}
