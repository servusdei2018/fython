package val

import (
	"sync"
	"testing"
)

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
	if setRaceEnabled {
		t.Skip("skipping zero-alloc check under -race (snapshots entries to prevent lock-order deadlock)")
	}
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

func TestSetTombstoneRecyclingBoundedTable(t *testing.T) {
	s := NewSetVal().AsSet()
	for i := range 20 {
		s.Add(Int64Val(int64(i)))
	}
	for i := range 5000 {
		s.Add(Int64Val(int64(1000 + i)))
		if !s.Remove(Int64Val(int64(1000 + i))) {
			t.Fatalf("Remove failed at iter %d", i)
		}
	}
	if s.Len() != 20 {
		t.Fatalf("Len() = %d, want 20", s.Len())
	}
	if len(s.entries) > 64 {
		t.Fatalf("table grew unboundedly under add/remove cycles: len(entries)=%d", len(s.entries))
	}
	for i := range 20 {
		if !s.Contains(Int64Val(int64(i))) {
			t.Fatalf("missing original key %d after tombstone cycles", i)
		}
	}
}

func TestSetCopyFastPathAndTombstoneCompaction(t *testing.T) {
	// Clean set copy: zero tombstones, memmove fast path.
	clean := makeIntSet(0, 64)
	cp := clean.Copy()
	if cp.Len() != 64 || cp.count != cp.used {
		t.Fatalf("clean Copy: len=%d count=%d used=%d", cp.Len(), cp.count, cp.used)
	}
	cp.Add(Int64Val(999))
	if clean.Contains(Int64Val(999)) {
		t.Fatalf("clean Copy aliased backing entries")
	}

	// Tombstoned set copy: compacts tombstones.
	for i := range 50 {
		clean.Remove(Int64Val(int64(i)))
	}
	if clean.count == clean.used {
		t.Fatalf("expected tombstones before Copy")
	}
	compacted := clean.Copy()
	if compacted.Len() != 14 || compacted.count != compacted.used {
		t.Fatalf("compacted Copy: len=%d count=%d used=%d", compacted.Len(), compacted.count, compacted.used)
	}
	for i := 50; i < 64; i++ {
		if !compacted.Contains(Int64Val(int64(i))) {
			t.Fatalf("compacted Copy missing %d", i)
		}
	}
}

func TestSetNumericEquivalenceAndKeyPreservation(t *testing.T) {
	s := NewSetVal().AsSet()
	s.Add(Int64Val(1))
	s.Add(BoolVal(true))
	s.Add(Float64Val(1.0))
	if s.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", s.Len())
	}
	items := s.Items()
	if len(items) != 1 || items[0].Kind() != KindInt64 || items[0].AsInt64() != 1 {
		t.Fatalf("expected first-inserted int(1) preserved, got %v (%v)", items, items[0].Kind())
	}
	if !s.Contains(BoolVal(true)) || !s.Contains(Float64Val(1.0)) {
		t.Fatalf("expected True and 1.0 to match 1")
	}

	// Exact int64 vs float64 above 2^53 must NOT falsely match.
	bigInt := int64(1<<53) + 1
	bigFloat := float64(int64(1 << 53))
	s2 := NewSetVal().AsSet()
	s2.Add(Int64Val(bigInt))
	if s2.Contains(Float64Val(bigFloat)) {
		t.Fatalf("2^53+1 falsely matched float64(2^53)")
	}
	s2.Add(Float64Val(bigFloat))
	if s2.Len() != 2 {
		t.Fatalf("expected 2 distinct elements for 2^53+1 and float64(2^53), got %d", s2.Len())
	}
}

func TestSetSlotZeroingForGC(t *testing.T) {
	s := NewSetVal().AsSet()
	for i := range 16 {
		s.Add(NewStringVal("heap_string_element_for_gc_" + string(rune('a'+i))))
	}
	for i := range 8 {
		s.Remove(NewStringVal("heap_string_element_for_gc_" + string(rune('a'+i))))
	}
	for i := range s.entries {
		if s.entries[i].State != setEntryOccupied && s.entries[i].Key != (Value{}) {
			t.Fatalf("non-occupied slot %d retained key pointer", i)
		}
	}
	for s.Len() > 0 {
		if _, ok := s.Pop(); !ok {
			t.Fatalf("Pop failed")
		}
	}
	for i := range s.entries {
		if s.entries[i].Key != (Value{}) {
			t.Fatalf("slot %d retained key pointer after Pop", i)
		}
	}
	s.Add(NewStringVal("clear_check_heap_string"))
	s.Clear()
	for i := range s.entries {
		if s.entries[i].Key != (Value{}) {
			t.Fatalf("slot %d retained key pointer after Clear", i)
		}
	}
}

func TestSetIterSizeChangeAndExhaustion(t *testing.T) {
	s := makeIntSet(0, 5)
	it := NewIterVal(SetVal(s)).AsIter()
	ctx := &ExecContext{}

	if _, ok := it.NextCtx(ctx); !ok || ctx.HasException() {
		t.Fatalf("first NextCtx failed")
	}
	s.Add(Int64Val(99))
	if _, ok := it.NextCtx(ctx); ok || !ctx.HasException() {
		t.Fatalf("expected RuntimeError on set size change during iteration")
	}
	if !IsInstance(ctx.ActiveException, RuntimeErrorType) {
		t.Fatalf("expected RuntimeError, got %v", ctx.ActiveException.Type().Name)
	}

	// Exhausted iterator must NOT raise RuntimeError on subsequent next() after mutation.
	s2 := makeIntSet(0, 3)
	it2 := NewIterVal(SetVal(s2)).AsIter()
	ctx2 := &ExecContext{}
	count := 0
	for {
		_, ok := it2.NextCtx(ctx2)
		if ctx2.HasException() {
			t.Fatalf("unexpected exception: %v", ctx2.ActiveException)
		}
		if !ok {
			break
		}
		count++
	}
	if count != 3 {
		t.Fatalf("iterated %d items, want 3", count)
	}
	s2.Add(Int64Val(100))
	if _, ok := it2.NextCtx(ctx2); ok || ctx2.HasException() {
		t.Fatalf("exhausted iterator falsely raised or yielded after post-exhaustion mutation")
	}
}

func TestSetSelfAliasingOps(t *testing.T) {
	s := makeIntSet(0, 10)
	if !s.IsSubset(s) || !s.IsSuperset(s) {
		t.Fatalf("s must be subset and superset of itself")
	}
	if s.IsDisjoint(s) {
		t.Fatalf("non-empty s must not be disjoint with itself")
	}
	empty := NewSetVal().AsSet()
	if !empty.IsDisjoint(empty) {
		t.Fatalf("empty set must be disjoint with itself")
	}
	if u := s.Union(s).AsSet(); u.Len() != 10 || u == s {
		t.Fatalf("s.Union(s) mismatch")
	}
	if inter := s.Intersection(s).AsSet(); inter.Len() != 10 || inter == s {
		t.Fatalf("s.Intersection(s) mismatch")
	}
	if diff := s.Difference(s).AsSet(); diff.Len() != 0 {
		t.Fatalf("s.Difference(s) len = %d, want 0", diff.Len())
	}
	if sym := s.SymmetricDifference(s).AsSet(); sym.Len() != 0 {
		t.Fatalf("s.SymmetricDifference(s) len = %d, want 0", sym.Len())
	}

	s1 := s.Copy()
	s1.UpdateFromSet(s1)
	if s1.Len() != 10 {
		t.Fatalf("s |= s changed len to %d", s1.Len())
	}
	s1.IntersectionUpdateFromSet(s1)
	if s1.Len() != 10 {
		t.Fatalf("s &= s changed len to %d", s1.Len())
	}
	s2 := s.Copy()
	s2.DifferenceUpdateFromSet(s2)
	if s2.Len() != 0 {
		t.Fatalf("s -= s len = %d, want 0", s2.Len())
	}
	s3 := s.Copy()
	s3.SymmetricDifferenceUpdateFromSet(s3)
	if s3.Len() != 0 {
		t.Fatalf("s ^= s len = %d, want 0", s3.Len())
	}
}

func TestSetReentrantEqMutationNoDeadlock(t *testing.T) {
	prevMT := MultiThreadedFlag.Swap(true)
	defer MultiThreadedFlag.Store(prevMT)
	prevHook := AcquireExecContextHook
	AcquireExecContextHook = func() (*ExecContext, func()) {
		ec := &ExecContext{Stack: make([]Value, 16)}
		ec.CallHook = func(c *ExecContext, fn Value, argBase int, nargs int, kwnames []Value) Value {
			return fn.AsBuiltinFunc().Fn(c, fn, c.Stack[argBase:argBase+nargs], kwnames)
		}
		return ec, func() {}
	}
	defer func() { AcquireExecContextHook = prevHook }()

	target := NewSetVal().AsSet()
	mutType := MustNewType("MutKey", []*PyType{ObjectType}, nil)
	hashFn := NewBuiltinFuncVal("__hash__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return Int64Val(42)
	})
	mutated := false
	eqFn := NewBuiltinFuncVal("__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if !mutated {
			mutated = true
			// Force a rehash inside __eq__ while Add/Contains is probing target!
			for i := range 32 {
				target.Add(Int64Val(int64(100 + i)))
			}
		}
		return BoolVal(args[0] == args[1])
	})
	mutType.Dict.Set(InternString("__hash__"), hashFn)
	mutType.dunderHash = hashFn
	mutType.Dict.Set(DunderEq, eqFn)
	mutType.dunderEq = eqFn

	k1 := NewInstanceVal(mutType.RootShape)
	k1.HeaderPtr().Type = mutType
	k2 := NewInstanceVal(mutType.RootShape)
	k2.HeaderPtr().Type = mutType
	target.Add(k1)
	target.Add(k2)
	if !mutated {
		t.Fatalf("expected re-entrant __eq__ mutation to run")
	}
	if !target.Contains(k1) || !target.Contains(k2) || target.Len() != 34 {
		t.Fatalf("unexpected state after re-entrant mutation: len=%d", target.Len())
	}
}

func TestSetConcurrentAccessRace(t *testing.T) {
	for _, mt := range []bool{false, true} {
		prevMT := MultiThreadedFlag.Swap(mt)
		a := makeIntSet(0, 64)
		b := makeIntSet(32, 96)

		var wg sync.WaitGroup
		for g := range 4 {
			id := g
			wg.Go(func() {
				for i := range 200 {
					k := Int64Val(int64((id*200 + i) % 128))
					a.Add(k)
					_ = a.Contains(k)
					_ = a.Len()
					if i%5 == 0 {
						a.Discard(k)
					}
					if i%10 == 0 {
						_ = a.Copy()
						_ = a.Union(b)
						_ = b.Union(a)
						_ = a.Intersection(b)
						_ = a.Difference(b)
						_ = a.IsSubset(b)
						_ = a.IsDisjoint(b)
					}
					if i%15 == 0 {
						a.ForEach(func(v Value) bool { return true })
					}
				}
			})
		}
		wg.Wait()
		MultiThreadedFlag.Store(prevMT)
	}
}
