package val

import (
	"sync"
	"testing"
	"unsafe"
)

func TestPyDictViewLayoutAndZeroSnapshotAlloc(t *testing.T) {
	if sz := unsafe.Sizeof(PyDictView{}); sz > 32 {
		t.Fatalf("unsafe.Sizeof(PyDictView{}) = %d, want <= 32", sz)
	}
	d := NewDictVal(64).AsDict()
	for i := range 64 {
		d.Set(Int64Val(int64(i)), Int64Val(int64(i*10)))
	}
	// Creating a view must allocate only the 32 B PyDictView (1 alloc), never a snapshot slice.
	for _, tc := range []struct {
		name string
		fn   func() Value
	}{
		{"keys", func() Value { return NewDictKeysVal(d) }},
		{"values", func() Value { return NewDictValuesVal(d) }},
		{"items", func() Value { return NewDictItemsVal(d) }},
	} {
		allocs := testing.AllocsPerRun(50, func() {
			v := tc.fn()
			if !v.IsDictView() {
				t.Fatalf("%s did not return PyDictView", tc.name)
			}
		})
		if allocs > 1 {
			t.Fatalf("NewDict%sVal allocated %.1f times, want <= 1", tc.name, allocs)
		}
	}
}

func TestPyDictReserveExactGrowthSafety(t *testing.T) {
	d := NewDictVal(0).AsDict()
	d.ReserveExact(50)
	if cap(d.Entries) != 50 {
		t.Fatalf("cap(d.Entries) = %d, want 50", cap(d.Entries))
	}
	for i := range 50 {
		d.Set(Int64Val(int64(i)), Int64Val(int64(i*2)))
	}
	// Growing beyond the exact reserved count must remain valid.
	for i := 50; i < 120; i++ {
		d.Set(Int64Val(int64(i)), Int64Val(int64(i*2)))
	}
	if d.Len() != 120 {
		t.Fatalf("d.Len() = %d, want 120", d.Len())
	}
	for i := range 120 {
		v, ok := d.Get(Int64Val(int64(i)))
		if !ok || v.AsInt64() != int64(i*2) {
			t.Fatalf("d.Get(%d) = (%v, %v), want %d", i, v, ok, i*2)
		}
	}
}

func TestPyDictSingleProbePopAndSetDefault(t *testing.T) {
	// 1. Combined dict
	d := NewDictVal(8).AsDict()
	d.Set(NewStringVal("a"), Int64Val(1))
	d.Set(NewStringVal("b"), Int64Val(2))
	if got := d.SetDefault(NewStringVal("a"), Int64Val(99)); got.AsInt64() != 1 {
		t.Fatalf("SetDefault existing = %v, want 1", got)
	}
	if got := d.SetDefault(NewStringVal("c"), Int64Val(3)); got.AsInt64() != 3 {
		t.Fatalf("SetDefault missing = %v, want 3", got)
	}
	if v, ok := d.Pop(NewStringVal("b")); !ok || v.AsInt64() != 2 {
		t.Fatalf("Pop existing = (%v, %v), want (2, true)", v, ok)
	}
	if _, ok := d.Pop(NewStringVal("b")); ok {
		t.Fatalf("Pop missing returned true")
	}

	// 2. Split-table dict
	sk := &SplitDictKeys{Count: 2, Mask: 7}
	k0, k1 := InternString("x"), InternString("y")
	sk.Keys[0], sk.Keys[1] = k0, k1
	sk.Hashes[0], sk.Hashes[1] = HashValue(k0), HashValue(k1)
	for i := range 2 {
		slot := sk.Hashes[i] & sk.Mask
		for sk.Indices[slot] != 0 {
			slot = (slot + 1) & sk.Mask
		}
		sk.Indices[slot] = byte(i + dictIndexOffset)
	}
	sd := NewSplitDict(sk, []Value{Int64Val(10), Int64Val(20)}, 0)
	if got := sd.SetDefault(k0, Int64Val(99)); got.AsInt64() != 10 {
		t.Fatalf("split SetDefault existing = %v, want 10", got)
	}
	if v, ok := sd.Pop(k1); !ok || v.AsInt64() != 20 {
		t.Fatalf("split Pop existing = (%v, %v), want (20, true)", v, ok)
	}
	if sd.Len() != 1 {
		t.Fatalf("split Len after Pop = %d, want 1", sd.Len())
	}

	// 3. Live instance-backed dict
	inst := NewInstanceOfType(ObjectType).AsInstance()
	ld := NewInstanceDictProxy(inst).AsDict()
	if got := ld.SetDefault(InternString("attr"), Int64Val(42)); got.AsInt64() != 42 {
		t.Fatalf("live SetDefault = %v, want 42", got)
	}
	if got := ld.SetDefault(InternString("attr"), Int64Val(99)); got.AsInt64() != 42 {
		t.Fatalf("live SetDefault existing = %v, want 42", got)
	}
	if v, ok := ld.Pop(InternString("attr")); !ok || v.AsInt64() != 42 {
		t.Fatalf("live Pop = (%v, %v), want (42, true)", v, ok)
	}
	if ld.Len() != 0 {
		t.Fatalf("live Len after Pop = %d, want 0", ld.Len())
	}
}

func TestPySetBinaryOpsExactSizingAndFrozenHash(t *testing.T) {
	// Test >256 spill, disjoint, subset, overlapping, and frozenset cached Header.Hash reset.
	var aItems, bItems []Value
	for i := range 400 {
		aItems = append(aItems, Int64Val(int64(i)))
	}
	for i := 100; i < 450; i++ {
		bItems = append(bItems, Int64Val(int64(i)))
	}
	faVal := NewFrozenSetVal(aItems...)
	fbVal := NewFrozenSetVal(bItems...)
	fa, fb := faVal.AsSet(), fbVal.AsSet()

	// Force cached Header.Hash on fa before Union (triggers memcpy fast path when union fits).
	hashA := HashValue(faVal)
	if hashA == 0 {
		t.Fatalf("expected non-zero frozenset hash")
	}

	// Union that fits in fa's existing 1024-slot table (400 + 50 = 450 <= 682).
	uniVal := fa.Union(fb)
	uni := uniVal.AsSet()
	if uni.Len() != 450 {
		t.Fatalf("Union len = %d, want 450", uni.Len())
	}
	if uni.Header.Hash != 0 {
		t.Fatalf("Union inherited cached Header.Hash = %d, want 0", uni.Header.Hash)
	}
	expectedUni := NewFrozenSetVal(append(append([]Value{}, aItems...), bItems...)...)
	if HashValue(uniVal) != HashValue(expectedUni) {
		t.Fatalf("Union hash mismatch: got %d, want %d", HashValue(uniVal), HashValue(expectedUni))
	}

	// Intersection (>256 hits: 300 elements).
	interVal := fa.Intersection(fb)
	if interVal.AsSet().Len() != 300 {
		t.Fatalf("Intersection len = %d, want 300", interVal.AsSet().Len())
	}

	// Disjoint intersection must return emptyFrozenSetVal with 0 allocs.
	fc := NewFrozenSetVal(Int64Val(9000), Int64Val(9001)).AsSet()
	disj := fa.Intersection(fc)
	if disj.AsSet().Len() != 0 || disj.ptr != emptyFrozenSetVal().ptr {
		t.Fatalf("disjoint frozenset intersection did not return empty singleton")
	}

	// Difference (>256 spill and empty).
	diffVal := fa.Difference(fb)
	if diffVal.AsSet().Len() != 100 {
		t.Fatalf("Difference len = %d, want 100", diffVal.AsSet().Len())
	}
	if fa.Difference(fa).AsSet().Len() != 0 {
		t.Fatalf("self Difference must be empty")
	}

	// Concurrent -race safety on Union / Intersection / Difference.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_ = fa.Union(fb)
				_ = fa.Intersection(fb)
				_ = fa.Difference(fb)
			}
		})
	}
	wg.Wait()
}
