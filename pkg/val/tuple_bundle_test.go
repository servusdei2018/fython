package val

import (
	"testing"
	"unsafe"
)

// TestTupleBundle12And16 checks the fused single-allocation tuples for 9..16
// elements plus the general path for 17+ elements.
func TestTupleBundle12And16(t *testing.T) {
	for _, n := range []int{9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 24} {
		src := make([]Value, n)
		for i := range src {
			src[i] = Int64Val(int64(i*3 - 7))
		}
		tup := NewTupleFromSlice(src)
		if !tup.IsTuple() {
			t.Fatalf("n=%d: NewTupleFromSlice did not return a tuple", n)
		}
		got := tup.AsTuple().Items
		if len(got) != n {
			t.Fatalf("n=%d: len(Items) = %d", n, len(got))
		}
		// Value equality element-wise and against the variadic constructor.
		want := NewTupleVal(src...)
		for i := range src {
			if got[i] != src[i] {
				t.Fatalf("n=%d: Items[%d] = %v, want %v", n, i, got[i], src[i])
			}
		}
		if !tuplesEqual(tup, want) {
			t.Fatalf("n=%d: tuple != variadic-built equal", n)
		}
		// Hashing agrees for equal tuples and is stable.
		if HashValue(tup) != HashValue(want) {
			t.Fatalf("n=%d: hash mismatch for equal tuples", n)
		}
		if HashValue(tup) != HashValue(NewTupleFromSlice(src)) {
			t.Fatalf("n=%d: hash unstable across builds", n)
		}
		// Indexing: every position reads back.
		for i := range src {
			if got[i].AsInt64() != int64(i*3-7) {
				t.Fatalf("n=%d: index %d = %v", n, i, got[i])
			}
		}
		// Slicing the backing items preserves order.
		if n >= 10 {
			sub := got[2:8]
			for i := range sub {
				if sub[i] != src[2+i] {
					t.Fatalf("n=%d: slice[%d] mismatch", n, i)
				}
			}
		}
		// Copy semantics: later mutation of src must not leak in.
		src[0] = Int64Val(99999)
		if got[0].AsInt64() == 99999 {
			t.Fatalf("n=%d: tuple aliases caller slice", n)
		}
		// No shared backing with the caller slice.
		if n > 0 && unsafe.SliceData(got) == unsafe.SliceData(src) {
			t.Fatalf("n=%d: tuple shares backing array with src", n)
		}
	}
}

// TestTupleBundleHeaderOffset pins the offset-0 Header invariant for the new
// fused bundle types.
func TestTupleBundleHeaderOffset(t *testing.T) {
	if off := unsafe.Offsetof(tupleBundle12{}.t.Header); off != 0 {
		t.Fatalf("tupleBundle12 Header offset = %d, want 0", off)
	}
	if off := unsafe.Offsetof(tupleBundle16{}.t.Header); off != 0 {
		t.Fatalf("tupleBundle16 Header offset = %d, want 0", off)
	}
	if got := unsafe.Sizeof(PyTuple{}); got != 96 {
		t.Fatalf("PyTuple size = %d, want 96", got)
	}
}

// TestTupleBundleOwnedSliceStaysZeroCopy ensures NewTupleFromOwnedSlice still
// wraps caller slices without copying for >8 elements.
func TestTupleBundleOwnedSliceStaysZeroCopy(t *testing.T) {
	for _, n := range []int{9, 12, 13, 16, 17, 33} {
		src := make([]Value, n)
		for i := range src {
			src[i] = Int64Val(int64(i))
		}
		tup := NewTupleFromOwnedSlice(src)
		got := tup.AsTuple().Items
		if len(got) != n {
			t.Fatalf("n=%d: len = %d", n, len(got))
		}
		if n > 8 && unsafe.SliceData(got) != unsafe.SliceData(src) {
			t.Fatalf("n=%d: expected zero-copy wrap for owned slice", n)
		}
		for i := range src {
			if got[i] != src[i] {
				t.Fatalf("n=%d: item %d mismatch", n, i)
			}
		}
	}
}

func tuplesEqual(a, b Value) bool {
	ai, bi := a.AsTuple().Items, b.AsTuple().Items
	if len(ai) != len(bi) {
		return false
	}
	for i := range ai {
		if ai[i] != bi[i] {
			return false
		}
	}
	return true
}
