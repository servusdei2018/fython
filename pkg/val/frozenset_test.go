package val

import (
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func TestFrozenSetLayoutAndSingleton(t *testing.T) {
	if got := unsafe.Sizeof(PySet{}); got != 64 {
		t.Fatalf("unsafe.Sizeof(PySet{}) = %d, want 64", got)
	}
	if off := unsafe.Offsetof(PySet{}.Header); off != 0 {
		t.Fatalf("unsafe.Offsetof(PySet{}.Header) = %d, want 0", off)
	}
	if got := unsafe.Sizeof(setEntry{}); got != 24 {
		t.Fatalf("unsafe.Sizeof(setEntry{}) = %d, want 24", got)
	}

	e1 := NewFrozenSetVal()
	e2 := NewFrozenSetVal()
	if e1 != e2 {
		t.Fatalf("NewFrozenSetVal() did not return canonical empty singleton")
	}
	if e1.Type() != FrozenSetType {
		t.Fatalf("empty frozenset type = %v, want FrozenSetType", e1.Type())
	}
	if IsSubclass(FrozenSetType, SetType) {
		t.Fatalf("FrozenSetType must not be a subclass of SetType")
	}
	if !e1.IsFrozenSet() {
		t.Fatalf("expected IsFrozenSet() == true for empty frozenset")
	}
	if NewSetVal().IsFrozenSet() {
		t.Fatalf("expected IsFrozenSet() == false for mutable set")
	}
}

func TestFrozenSetOrderIndependentHashAndConcurrency(t *testing.T) {
	a := NewFrozenSetVal(Int64Val(1), Int64Val(2), Int64Val(3), InternString("alpha"))
	b := NewFrozenSetVal(InternString("alpha"), Int64Val(3), Int64Val(1), Int64Val(2))
	c := NewFrozenSetVal(BoolVal(true), Float64Val(2.0), Int64Val(3), NewStringVal("alpha"))

	ha := HashValue(a)
	hb := HashValue(b)
	hc := HashValue(c)
	if ha == 0 {
		t.Fatalf("HashValue(frozenset) returned 0")
	}
	if ha != hb || ha != hc {
		t.Fatalf("order-independent hash mismatch: ha=%d hb=%d hc=%d", ha, hb, hc)
	}
	if cached := atomic.LoadUint32(&a.HeaderPtr().Hash); cached != ha {
		t.Fatalf("cached Header.Hash = %d, want %d", cached, ha)
	}

	// Empty frozenset hash is deterministic and non-zero.
	hEmpty := HashValue(NewFrozenSetVal())
	if hEmpty == 0 || hEmpty != HashValue(NewFrozenSetVal()) {
		t.Fatalf("empty frozenset hash = %d", hEmpty)
	}

	// Unhashable checks: set is unhashable, frozenset is hashable.
	if badName, bad := IsUnhashableValue(NewSetVal(Int64Val(1))); !bad || badName != "set" {
		t.Fatalf("IsUnhashableValue(set) = (%q, %v), want (\"set\", true)", badName, bad)
	}
	if badName, bad := IsUnhashableValue(a); bad {
		t.Fatalf("IsUnhashableValue(frozenset) = (%q, %v), want (\"\", false)", badName, bad)
	}

	// Concurrent hashing of a freshly constructed shared frozenset under -race.
	shared := NewFrozenSetVal(Int64Val(10), Int64Val(20), Int64Val(30), InternString("race"))
	const workers = 16
	hashes := make([]uint32, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			hashes[idx] = HashValue(shared)
		}(i)
	}
	wg.Wait()
	for i := 1; i < workers; i++ {
		if hashes[i] != hashes[0] || hashes[i] == 0 {
			t.Fatalf("concurrent HashValue mismatch at %d: got %d, want %d", i, hashes[i], hashes[0])
		}
	}
}

func TestFrozenSetCrossEqualityAndReceiverType(t *testing.T) {
	s := NewSetVal(Int64Val(1), Int64Val(2))
	fs := NewFrozenSetVal(Int64Val(2), Int64Val(1))
	if !Equals(s, fs) || !Equals(fs, s) {
		t.Fatalf("expected set == frozenset cross-equality")
	}

	other := NewSetVal(Int64Val(2), Int64Val(3))
	if res := BitOr(nil, fs, other); res.Type() != FrozenSetType {
		t.Fatalf("frozenset | set type = %v, want frozenset", res.Type())
	}
	if res := BitOr(nil, s, fs); res.Type() != SetType {
		t.Fatalf("set | frozenset type = %v, want set", res.Type())
	}
	if res := BitAnd(nil, fs, other); res.Type() != FrozenSetType {
		t.Fatalf("frozenset & set type = %v, want frozenset", res.Type())
	}
	if res := SubCtx(nil, fs, other); res.Type() != FrozenSetType {
		t.Fatalf("frozenset - set type = %v, want frozenset", res.Type())
	}
	if res := BitXor(nil, fs, other); res.Type() != FrozenSetType {
		t.Fatalf("frozenset ^ set type = %v, want frozenset", res.Type())
	}

	// Subclass binary ops return base FrozenSetType (matching CPython).
	subFS := MustNewType("MyFS", []*PyType{FrozenSetType}, nil)
	subVal, ok := NewFrozenSetValChecked(nil, subFS, Int64Val(1))
	if !ok || subVal.Type() != subFS {
		t.Fatalf("expected MyFS instance, got %v", subVal.Type())
	}
	if res := BitOr(nil, subVal, other); res.Type() != FrozenSetType {
		t.Fatalf("MyFS | set type = %v, want base frozenset", res.Type())
	}
}
