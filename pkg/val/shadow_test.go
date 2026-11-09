package val_test

import (
	"testing"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// Roundtrip: ToStub + FromStub preserves Value (pointer identity for heap objects).
func TestShadowArenaRoundtrip(t *testing.T) {
	a := val.NewShadowArena()
	v := val.NewListVal(val.Int64Val(1), val.Int64Val(2))
	s1 := a.ToStub(v)
	s2 := a.BorrowStub(v)
	if s1 != s2 {
		t.Fatalf("expected same stub for same heap object, got %p vs %p", s1, s2)
	}
	got := a.FromStub(s1)
	if !got.IsList() || len(got.AsList().Items) != 2 {
		t.Fatalf("roundtrip failed: %v", got)
	}
	// Different heap object gets different stub.
	v2 := val.NewListVal(val.Int64Val(1), val.Int64Val(2))
	s3 := a.ToStub(v2)
	if s3 == s1 {
		t.Fatalf("different objects shared stub")
	}
}

func TestShadowArenaDealloc(t *testing.T) {
	a := val.NewShadowArena()
	v := val.NewDictVal(8)
	s := a.ToStub(v)
	// Simulate C DECREF to 0 then _Py_Dealloc
	*(*int64)(s) = 0
	a.DeallocStub(s)
	// After dealloc, handle is freed; a new value should reuse slot without panic.
	v2 := val.NewListVal()
	a.ToStub(v2)
	// Immortal survives dealloc.
	noneStub := val.ArenaBorrowStub(val.NoneVal())
	*(*int64)(noneStub) = val.ImmortalRefcnt
	a.DeallocStub(noneStub)
	if a.FromStub(noneStub).Kind() != val.KindNone {
		t.Fatalf("immortal None was freed")
	}
	_ = unsafe.Pointer(nil)
}

func TestShadowArenaStringCache(t *testing.T) {
	a := val.NewShadowArena()
	v := val.NewStringVal("hello world test")
	s := a.ToStub(v)
	p1 := a.StubUTF8(s)
	p2 := a.StubUTF8(s)
	if p1 != p2 || p1 == nil {
		t.Fatalf("string cache not stable: %p vs %p", p1, p2)
	}
}

func TestShadowIdentityPrimitives(t *testing.T) {
	// Two equal small ints share stub (intern-like); distinct heap objects do not.
	a := val.NewShadowArena()
	s1 := a.ToStub(val.Int64Val(42))
	s2 := a.ToStub(val.Int64Val(42))
	if s1 != s2 {
		t.Fatalf("same int value should share stub")
	}
}
