package val

import (
	"sync"
	"testing"
)

func TestWave7ConcurrentDictSetCopyAndTupleHash(t *testing.T) {
	d := NewDictVal(32).AsDict()
	for i := range 32 {
		d.Set(NewStringVal("k"+string(rune('a'+i%26))+string(rune('0'+i/26))), Int64Val(int64(i)))
	}
	s := NewSetVal().AsSet()
	for i := range 32 {
		s.Add(Int64Val(int64(i)))
	}

	// Verify single-threaded memcpy copy preserves exact entries and isolation.
	dCopy := d.Copy()
	if dCopy.Len() != d.Len() {
		t.Fatalf("dict copy len=%d, want %d", dCopy.Len(), d.Len())
	}
	dCopy.Set(NewStringVal("only_in_copy"), Int64Val(777))
	if _, ok := d.Get(NewStringVal("only_in_copy")); ok {
		t.Fatalf("mutation leaked from dict copy to original")
	}

	sCopy := s.Copy()
	if sCopy.Len() != s.Len() {
		t.Fatalf("set copy len=%d, want %d", sCopy.Len(), s.Len())
	}
	sCopy.Add(Int64Val(999))
	if s.Contains(Int64Val(999)) {
		t.Fatalf("mutation leaked from set copy to original")
	}

	// Verify primitive-tuple hash bit-exactness across int/bool/float/inline-str/heap-str/None.
	tInt := NewTupleVal(Int64Val(1), NewStringVal("alpha_long_string"), NoneVal())
	tBool := NewTupleVal(BoolVal(true), NewStringVal("alpha_long_string"), NoneVal())
	tFloat := NewTupleVal(Float64Val(1.0), NewStringVal("alpha_long_string"), NoneVal())
	hInt := HashValue(tInt)
	hBool := HashValue(tBool)
	hFloat := HashValue(tFloat)
	if hInt != hBool || hInt != hFloat {
		t.Fatalf("tuple hash mismatch: int=%#x bool=%#x float=%#x", hInt, hBool, hFloat)
	}

	// Concurrent -race safety for Copy() and tuple hashing.
	MultiThreadedFlag.Store(true)
	defer MultiThreadedFlag.Store(false)

	sharedTuple := NewTupleVal(Int64Val(42), SmallStringVal("hi"), NewStringVal("concurrent_hash_target"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				dc := d.Copy()
				if dc.Len() != 32 {
					t.Errorf("concurrent dict copy len=%d", dc.Len())
				}
				sc := s.Copy()
				if sc.Len() != 32 {
					t.Errorf("concurrent set copy len=%d", sc.Len())
				}
				if HashValue(sharedTuple) == 0 {
					t.Errorf("zero hash")
				}
			}
		})
	}
	wg.Wait()
}
