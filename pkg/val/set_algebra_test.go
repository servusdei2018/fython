package val

import "testing"

func makeIntSet(start, end int) *PySet {
	s := NewSetVal().AsSet()
	s.Reserve(end - start)
	for i := start; i < end; i++ {
		s.Add(Int64Val(int64(i)))
	}
	return s
}

func TestSetAlgebraOperationsAndDeduplication(t *testing.T) {
	a := makeIntSet(0, 100)
	b := makeIntSet(50, 150)

	// Union with overlapping keys must preserve exact Len() (no duplicate counting).
	u := a.Union(b).AsSet()
	if u.Len() != 150 {
		t.Fatalf("Union len = %d, want 150", u.Len())
	}
	for i := range 150 {
		if !u.Contains(Int64Val(int64(i))) {
			t.Fatalf("Union missing %d", i)
		}
	}

	// Intersection (asymmetric sizes: small & large, large & small).
	small := makeIntSet(60, 70)
	inter1 := a.Intersection(small).AsSet()
	inter2 := small.Intersection(a).AsSet()
	if inter1.Len() != 10 || inter2.Len() != 10 || !inter1.IsSubset(inter2) || !inter2.IsSubset(inter1) {
		t.Fatalf("Intersection mismatch: %d vs %d", inter1.Len(), inter2.Len())
	}

	// Difference.
	diff := a.Difference(b).AsSet()
	if diff.Len() != 50 {
		t.Fatalf("Difference len = %d, want 50", diff.Len())
	}
	for i := range 50 {
		if !diff.Contains(Int64Val(int64(i))) {
			t.Fatalf("Difference missing %d", i)
		}
	}

	// SymmetricDifference.
	sym := a.SymmetricDifference(b).AsSet()
	if sym.Len() != 100 {
		t.Fatalf("SymmetricDifference len = %d, want 100", sym.Len())
	}
	if sym.Contains(Int64Val(75)) || !sym.Contains(Int64Val(0)) || !sym.Contains(Int64Val(149)) {
		t.Fatalf("SymmetricDifference contents wrong")
	}

	// IsSubset / IsSuperset / IsDisjoint (including early exit on size).
	if !small.IsSubset(a) || a.IsSubset(small) {
		t.Fatalf("IsSubset wrong")
	}
	if !a.IsSuperset(small) || small.IsSuperset(a) {
		t.Fatalf("IsSuperset wrong")
	}
	disj := makeIntSet(200, 210)
	if !a.IsDisjoint(disj) || !disj.IsDisjoint(a) {
		t.Fatalf("IsDisjoint expected true")
	}
	if a.IsDisjoint(b) || b.IsDisjoint(a) {
		t.Fatalf("IsDisjoint expected false")
	}

	// Pop without Keys() allocation.
	p := makeIntSet(10, 15)
	seen := 0
	for p.Len() > 0 {
		_, ok := p.Pop()
		if !ok {
			t.Fatalf("Pop failed on non-empty set")
		}
		seen++
	}
	if seen != 5 {
		t.Fatalf("Popped %d items, want 5", seen)
	}
	if _, ok := p.Pop(); ok {
		t.Fatalf("Pop on empty set succeeded")
	}

	// In-place updates.
	u2 := a.Copy()
	u2.UpdateFromSet(b)
	if u2.Len() != 150 {
		t.Fatalf("UpdateFromSet len = %d, want 150", u2.Len())
	}
	i2 := a.Copy()
	i2.IntersectionUpdateFromSet(b)
	if i2.Len() != 50 {
		t.Fatalf("IntersectionUpdateFromSet len = %d, want 50", i2.Len())
	}
	d2 := a.Copy()
	d2.DifferenceUpdateFromSet(b)
	if d2.Len() != 50 {
		t.Fatalf("DifferenceUpdateFromSet len = %d, want 50", d2.Len())
	}
	s2 := a.Copy()
	s2.SymmetricDifferenceUpdateFromSet(b)
	if s2.Len() != 100 {
		t.Fatalf("SymmetricDifferenceUpdateFromSet len = %d, want 100", s2.Len())
	}

	// Receiver type preservation across custom set subclass.
	subType := MustNewType("CustomSet", []*PyType{SetType}, nil)
	custom := newEmptySetFor(subType)
	custom.Add(Int64Val(1))
	custom.Add(Int64Val(2))
	if res := custom.Union(small); res.Type() != subType {
		t.Fatalf("Union receiver type = %v, want CustomSet", res.Type())
	}
	if res := custom.Intersection(a); res.Type() != subType {
		t.Fatalf("Intersection receiver type = %v, want CustomSet", res.Type())
	}
	if res := custom.Difference(small); res.Type() != subType {
		t.Fatalf("Difference receiver type = %v, want CustomSet", res.Type())
	}
	if res := custom.SymmetricDifference(small); res.Type() != subType {
		t.Fatalf("SymmetricDifference receiver type = %v, want CustomSet", res.Type())
	}
}

func TestSetSubsetAndDisjointZeroAllocs(t *testing.T) {
	a := makeIntSet(0, 200)
	b := makeIntSet(0, 400)
	c := makeIntSet(500, 700)

	allocsSub := testing.AllocsPerRun(100, func() {
		if !a.IsSubset(b) {
			t.Fatal("expected subset")
		}
	})
	if allocsSub != 0 {
		t.Fatalf("IsSubset allocs = %v, want 0", allocsSub)
	}
	allocsDisj := testing.AllocsPerRun(100, func() {
		if !a.IsDisjoint(c) {
			t.Fatal("expected disjoint")
		}
	})
	if allocsDisj != 0 {
		t.Fatalf("IsDisjoint allocs = %v, want 0", allocsDisj)
	}
}

func TestDictBitOrDeduplicationAndSplitTables(t *testing.T) {
	// Combined dicts with overlapping keys.
	da := NewDictVal(4)
	da.AsDict().Set(InternString("a"), Int64Val(1))
	da.AsDict().Set(InternString("b"), Int64Val(2))

	db := NewDictVal(4)
	db.AsDict().Set(InternString("b"), Int64Val(20))
	db.AsDict().Set(InternString("c"), Int64Val(30))

	merged := BitOr(nil, da, db)
	md := merged.AsDict()
	if md.Len() != 3 {
		t.Fatalf("merged dict len = %d, want 3", md.Len())
	}
	if v, ok := md.Get(InternString("b")); !ok || v.AsInt64() != 20 {
		t.Fatalf("merged['b'] = %v (ok=%v), want 20", v, ok)
	}
	if v, ok := md.Get(InternString("a")); !ok || v.AsInt64() != 1 {
		t.Fatalf("merged['a'] = %v (ok=%v), want 1", v, ok)
	}
	if v, ok := md.Get(InternString("c")); !ok || v.AsInt64() != 30 {
		t.Fatalf("merged['c'] = %v (ok=%v), want 30", v, ok)
	}
}
