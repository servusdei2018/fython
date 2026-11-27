package val

import (
	"sync"
	"testing"
)

func lookupVal(t *testing.T, cls *PyType, name string) (Value, bool) {
	t.Helper()
	v, _, ok := cls.LookupMRO(InternString(name))
	return v, ok
}

func TestTypeLookupCacheTracksMutation(t *testing.T) {
	base := MustNewType("LCBase", nil, nil)
	mid := MustNewType("LCMid", []*PyType{base}, nil)
	leaf := MustNewType("LCLeaf", []*PyType{mid}, nil)

	// Negative results are cached and must be invalidated by a later definition.
	if _, ok := lookupVal(t, leaf, "lc_attr"); ok {
		t.Fatalf("lc_attr must not exist yet")
	}
	if _, ok := lookupVal(t, leaf, "lc_attr"); ok {
		t.Fatalf("cached miss must stay a miss")
	}
	base.SetAttrStr("lc_attr", Int64Val(1))
	if v, ok := lookupVal(t, leaf, "lc_attr"); !ok || v.AsInt64() != 1 {
		t.Fatalf("base definition not visible through leaf: %v %v", v, ok)
	}

	// Overriding in a middle class shadows the base value.
	mid.SetAttrStr("lc_attr", Int64Val(2))
	if v, _ := lookupVal(t, leaf, "lc_attr"); v.AsInt64() != 2 {
		t.Fatalf("mid override not visible: %d", v.AsInt64())
	}

	// A direct write to the type dict must invalidate through the owner hook.
	mid.Dict.Set(InternString("lc_attr"), Int64Val(3))
	if v, _ := lookupVal(t, leaf, "lc_attr"); v.AsInt64() != 3 {
		t.Fatalf("direct dict write not visible: %d", v.AsInt64())
	}
	mid.Dict.Delete(InternString("lc_attr"))
	if v, _ := lookupVal(t, leaf, "lc_attr"); v.AsInt64() != 1 {
		t.Fatalf("direct dict delete not visible: %d", v.AsInt64())
	}

	// A subclass created after the cache was filled sees the right state.
	late := MustNewType("LCLate", []*PyType{leaf}, nil)
	if v, _ := lookupVal(t, late, "lc_attr"); v.AsInt64() != 1 {
		t.Fatalf("late subclass lookup wrong: %d", v.AsInt64())
	}
	base.SetAttrStr("lc_attr", Int64Val(9))
	if v, _ := lookupVal(t, late, "lc_attr"); v.AsInt64() != 9 {
		t.Fatalf("late subclass not invalidated by base mutation: %d", v.AsInt64())
	}
}

func TestTypeLookupCacheSkipsNonInternedNames(t *testing.T) {
	cls := MustNewType("LCNonInterned", nil, nil)
	cls.SetAttrStr("a_rather_long_attribute_name", Int64Val(5))
	heapName := NewStringVal("a_rather_long_attribute_name")
	if heapName.IsInterned() {
		t.Skip("string constructor already interns")
	}
	if v, _, ok := cls.LookupMRO(heapName); !ok || v.AsInt64() != 5 {
		t.Fatalf("non-interned lookup must still resolve")
	}
	for i := range typeLookupCache {
		s := &typeLookupCache[i]
		s.mu.Lock()
		cached := s.ver != 0 && s.name == heapName
		s.mu.Unlock()
		if cached {
			t.Fatalf("non-interned heap string was cached")
		}
	}
}

func TestTypeLookupSuperCacheTracksMutation(t *testing.T) {
	base := MustNewType("SCBase", nil, nil)
	mid := MustNewType("SCMid", []*PyType{base}, nil)
	leaf := MustNewType("SCLeaf", []*PyType{mid}, nil)
	name := InternString("sc_method")
	base.SetAttrStr("sc_method", Int64Val(1))
	if v, _, ok := leaf.LookupMROAfter(mid, name); !ok || v.AsInt64() != 1 {
		t.Fatalf("super lookup after mid must find base")
	}
	base.SetAttrStr("sc_method", Int64Val(2))
	if v, _, _ := leaf.LookupMROAfter(mid, name); v.AsInt64() != 2 {
		t.Fatalf("super cache stale after base mutation")
	}
	// The same name looked up after a different start class resolves differently.
	mid.SetAttrStr("sc_method", Int64Val(7))
	if v, _, _ := leaf.LookupMROAfter(leaf, name); v.AsInt64() != 7 {
		t.Fatalf("lookup after leaf must find mid's definition")
	}
	if v, _, _ := leaf.LookupMROAfter(mid, name); v.AsInt64() != 2 {
		t.Fatalf("lookup after mid must still find base's definition")
	}
}

func TestTypeSetAttrOnTypeInvalidatesVersion(t *testing.T) {
	cls := MustNewType("SetAttrOnType", nil, nil)
	before := cls.Version()
	ctx := &ExecContext{}
	if err := objectSetAttrForTest(ctx, TypeVal(cls), "late_attr", Int64Val(4)); err != nil {
		t.Fatal(err)
	}
	if cls.Version() <= before {
		t.Fatalf("version did not advance after setting an attribute on the type object")
	}
}

func objectSetAttrForTest(ctx *ExecContext, obj Value, name string, v Value) error {
	objectSetAttr(ctx, obj, InternString(name), v)
	return nil
}

func TestTypeVersionConcurrentMutateAndRead(t *testing.T) {
	SetMultiThreaded(true)
	defer SetMultiThreaded(false)
	cls := MustNewType("ConcurrentVer", nil, nil)
	sub := MustNewType("ConcurrentVerSub", []*PyType{cls}, nil)
	name := InternString("cv_attr")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 2000 {
			cls.SetAttrStr("cv_attr", Int64Val(int64(i)))
		}
	}()
	go func() {
		defer wg.Done()
		for range 2000 {
			_ = sub.Version()
			sub.LookupMRO(name)
		}
	}()
	wg.Wait()
	if v, _, ok := sub.LookupMRO(name); !ok || v.AsInt64() != 1999 {
		t.Fatalf("final lookup stale: %v %v", v, ok)
	}
}
