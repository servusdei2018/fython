package val

import (
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

// Compile-time assertions that every core heap struct places Header at byte offset 0.
var (
	_ [0 - unsafe.Offsetof(PyDict{}.Header)]byte
	_ [0 - unsafe.Offsetof(PySet{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyList{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyString{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyIter{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyGenerator{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyCoroutine{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyTuple{}.Header)]byte
)

func TestCoreStructLayouts(t *testing.T) {
	if got := unsafe.Sizeof(PyTuple{}); got != 96 {
		t.Fatalf("unsafe.Sizeof(PyTuple{}) = %d, want 96", got)
	}
	if got := unsafe.Sizeof(PyDict{}); got > 144 {
		t.Fatalf("unsafe.Sizeof(PyDict{}) = %d, want <= 144", got)
	}
	// Pointer-bearing fields must precede scalar tails on reordered structs.
	if unsafe.Offsetof(PyDict{}.owner) >= unsafe.Offsetof(PyDict{}.VersionTag) {
		t.Fatalf("PyDict.owner must precede scalar tail VersionTag")
	}
	if unsafe.Offsetof(PyList{}.Attrs) >= unsafe.Offsetof(PyList{}.mu) {
		t.Fatalf("PyList.Attrs must precede scalar mu")
	}
	if unsafe.Offsetof(PyString{}.RuneCache) >= unsafe.Offsetof(PyString{}.BufClaimed) {
		t.Fatalf("PyString.RuneCache must precede scalar BufClaimed")
	}
	if unsafe.Offsetof(PyIter{}.NextFn) >= unsafe.Offsetof(PyIter{}.Idx) {
		t.Fatalf("PyIter.NextFn must precede scalar Idx")
	}
	if unsafe.Offsetof(PyGenerator{}.CellPtrs) >= unsafe.Offsetof(PyGenerator{}.SP) {
		t.Fatalf("PyGenerator.CellPtrs must precede scalar SP")
	}
	if unsafe.Offsetof(PyCoroutine{}.CellPtrs) >= unsafe.Offsetof(PyCoroutine{}.SP) {
		t.Fatalf("PyCoroutine.CellPtrs must precede scalar SP")
	}
}

func TestLazyStringHashAndConcurrency(t *testing.T) {
	s1 := NewStringVal("lazy_hash_string_alpha")
	s2 := NewStringFromBytes([]byte("lazy_hash_string_alpha"))
	s3 := ConcatStrings(NewStringVal("lazy_hash_"), NewStringVal("string_alpha"))

	for i, sv := range []Value{s1, s2, s3} {
		if h := atomic.LoadUint32(&sv.HeaderPtr().Hash); h != 0 {
			t.Fatalf("string %d unexpectedly pre-hashed: %d", i, h)
		}
	}

	if !StringEquals(s1, s2) || !StringEquals(s1, s3) {
		t.Fatalf("expected equal lazy strings before hashing")
	}

	var wg sync.WaitGroup
	expected := HashString("lazy_hash_string_alpha")
	for range 8 {
		wg.Go(func() {
			for range 200 {
				if got := HashValue(s1); got != expected {
					t.Errorf("HashValue(s1) = %d, want %d", got, expected)
				}
				if got := HashValue(s2); got != expected {
					t.Errorf("HashValue(s2) = %d, want %d", got, expected)
				}
				if !StringEquals(s1, s3) {
					t.Errorf("StringEquals(s1, s3) = false")
				}
			}
		})
	}
	wg.Wait()

	if h := atomic.LoadUint32(&s1.HeaderPtr().Hash); h != expected {
		t.Fatalf("cached hash = %d, want %d", h, expected)
	}
}

func TestForkedConcatImmutabilityAndGrowth(t *testing.T) {
	// Case 1: Initial exact-sized ConcatStrings followed by forked TryConcatInPlace
	// that triggers geometric growth on the first branch and falls back on the second.
	a := NewStringVal("hello_")
	b := NewStringVal("world!")
	s := ConcatStrings(a, b)
	ps := (*PyString)(unsafe.Pointer(s.HeaderPtr()))
	if cap(ps.Buf) != len("hello_world!") {
		t.Fatalf("ConcatStrings cap = %d, want exact %d", cap(ps.Buf), len("hello_world!"))
	}

	x, ok1 := TryConcatInPlace(s, NewStringVal("_branch_one"))
	if !ok1 {
		t.Fatalf("first TryConcatInPlace after ConcatStrings should promote geometrically")
	}
	psX := (*PyString)(unsafe.Pointer(x.HeaderPtr()))
	if cap(psX.Buf) < max(64, len(psX.Data)*2) {
		t.Fatalf("promoted cap = %d, want >= %d", cap(psX.Buf), max(64, len(psX.Data)*2))
	}

	// Second fork from the same s must fail CAS and fall back to ConcatStrings.
	if _, ok2 := TryConcatInPlace(s, NewStringVal("_branch_two")); ok2 {
		t.Fatalf("second TryConcatInPlace on already-claimed s must return false")
	}
	y := ConcatStrings(s, NewStringVal("_branch_two"))

	if s.AsString() != "hello_world!" {
		t.Fatalf("s mutated: got %q", s.AsString())
	}
	if x.AsString() != "hello_world!_branch_one" {
		t.Fatalf("x wrong: got %q", x.AsString())
	}
	if y.AsString() != "hello_world!_branch_two" {
		t.Fatalf("y wrong: got %q", y.AsString())
	}

	// Case 2: Fork when cap(buf) has spare room (after geometric promotion on x).
	x1, ok3 := TryConcatInPlace(x, NewStringVal("_A"))
	if !ok3 {
		t.Fatalf("in-place append into spare capacity should succeed")
	}
	if _, ok4 := TryConcatInPlace(x, NewStringVal("_B")); ok4 {
		t.Fatalf("second fork on x must fail CAS")
	}
	x2 := ConcatStrings(x, NewStringVal("_B"))

	if x.AsString() != "hello_world!_branch_one" {
		t.Fatalf("x mutated after in-place append: got %q", x.AsString())
	}
	if x1.AsString() != "hello_world!_branch_one_A" {
		t.Fatalf("x1 wrong: got %q", x1.AsString())
	}
	if x2.AsString() != "hello_world!_branch_one_B" {
		t.Fatalf("x2 wrong: got %q", x2.AsString())
	}
}

func TestRichCompareIntBoolNoAllocAndSemantics(t *testing.T) {
	if !RichCompare(nil, Int64Val(1), BoolVal(true), CmpEQ).AsBool() {
		t.Fatalf("1 == True must be True")
	}
	if RichCompare(nil, Int64Val(1), BoolVal(true), CmpIS).AsBool() {
		t.Fatalf("1 is True must be False")
	}
	if !RichCompare(nil, Int64Val(1), BoolVal(true), CmpISNOT).AsBool() {
		t.Fatalf("1 is not True must be True")
	}
	if !RichCompare(nil, Int64Val(0), BoolVal(true), CmpLT).AsBool() {
		t.Fatalf("0 < True must be True")
	}
	if !RichCompare(nil, BoolVal(true), Int64Val(0), CmpGT).AsBool() {
		t.Fatalf("True > 0 must be True")
	}

	t1 := NewTupleVal(Int64Val(1), BoolVal(false))
	t2 := NewTupleVal(BoolVal(true), Int64Val(0))
	t3 := NewTupleVal(BoolVal(true), Int64Val(1))
	if !RichCompare(nil, t1, t2, CmpEQ).AsBool() {
		t.Fatalf("(1, False) == (True, 0) must be True")
	}
	if !RichCompare(nil, t1, t3, CmpLT).AsBool() {
		t.Fatalf("(1, False) < (True, 1) must be True")
	}

	allocs := testing.AllocsPerRun(100, func() {
		_ = RichCompare(nil, Int64Val(1), BoolVal(true), CmpEQ)
		_ = RichCompare(nil, Int64Val(0), BoolVal(true), CmpLT)
	})
	if allocs != 0 {
		t.Fatalf("RichCompare(int, bool) allocated %.1f times, want 0", allocs)
	}
}
