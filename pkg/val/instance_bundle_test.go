package val

import (
	"fmt"
	"sync"
	"testing"
	"unsafe"
)

func TestInstanceBundleSlotSizes(t *testing.T) {
	cases := []struct {
		slotCount int
		wantCap   int
		inline    bool
	}{
		{0, 0, false},
		{1, 1, true},
		{2, 2, true},
		{3, 3, true},
		{4, 4, true},
		{5, 6, true},
		{6, 6, true},
		{7, 8, true},
		{8, 8, true},
		{9, 12, true},
		{12, 12, true},
		{13, 13, false},
		{20, 20, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("slots_%d", tc.slotCount), func(t *testing.T) {
			shape := NewRootShape()
			for i := range tc.slotCount {
				shape = shape.Transition(InternString(fmt.Sprintf("f%d", i)))
			}
			inst := NewInstanceWithShape(ObjectType, shape)
			if len(inst.Slots) != tc.slotCount {
				t.Fatalf("len(Slots) = %d, want %d", len(inst.Slots), tc.slotCount)
			}
			if cap(inst.Slots) != tc.wantCap {
				t.Fatalf("cap(Slots) = %d, want %d", cap(inst.Slots), tc.wantCap)
			}
			if tc.slotCount > 0 {
				instAddr := uintptr(unsafe.Pointer(inst))
				slotAddr := uintptr(unsafe.Pointer(&inst.Slots[0]))
				isInline := slotAddr == instAddr+unsafe.Sizeof(PyInstance{})
				if isInline != tc.inline {
					t.Fatalf("inline=%v, want %v (inst=%#x slot0=%#x)", isInline, tc.inline, instAddr, slotAddr)
				}
			}
		})
	}
}

func TestInstanceLikelySlotCapConvergenceAndGrowth(t *testing.T) {
	cls := MustNewType("DynamicPoint", []*PyType{ObjectType}, nil)

	// First instance starts with 0 slots and learns up to 4 on first instance.
	v1 := NewInstanceOfType(cls)
	i1 := v1.AsInstance()
	if cap(i1.Slots) != 0 {
		t.Fatalf("initial cap = %d, want 0", cap(i1.Slots))
	}
	i1.SetAttr(InternString("a"), Int64Val(1))
	i1.SetAttr(InternString("b"), Int64Val(2))
	i1.SetAttr(InternString("c"), Int64Val(3))
	i1.SetAttr(InternString("d"), Int64Val(4))

	if got := cls.likelySlotCap.Load(); got != 4 {
		t.Fatalf("likelySlotCap after 4 attrs on first instance = %d, want 4", got)
	}

	// Second instance allocates pyInstanceBundle4 directly in 1 allocation.
	v2 := NewInstanceOfType(cls)
	i2 := v2.AsInstance()
	if len(i2.Slots) != 0 || cap(i2.Slots) != 4 {
		t.Fatalf("second instance len/cap = %d/%d, want 0/4", len(i2.Slots), cap(i2.Slots))
	}

	// Growing past bundle cap works cleanly and bumps likelySlotCap.
	for idx := range 15 {
		i2.SetAttr(InternString(fmt.Sprintf("k%d", idx)), Int64Val(int64(idx)))
	}
	if len(i2.Slots) != 15 {
		t.Fatalf("grown len(Slots) = %d, want 15", len(i2.Slots))
	}
	if got := cls.likelySlotCap.Load(); got < 15 {
		t.Fatalf("likelySlotCap after growth = %d, want >= 15", got)
	}
	for idx := range 15 {
		got, ok := i2.GetAttr(InternString(fmt.Sprintf("k%d", idx)))
		if !ok || got.AsInt64() != int64(idx) {
			t.Fatalf("GetAttr(k%d) = (%v, %v), want (%d, true)", idx, got, ok, idx)
		}
	}
}

func TestSlotsExactSizingAndPrimitiveSubclasses(t *testing.T) {
	// Empty __slots__ -> 0-slot PyInstance (56 B -> 64 B class).
	emptyDict := NewDictVal(2).AsDict()
	emptyDict.Set(DunderSlots, EmptyTupleVal())
	emptyCls := MustNewType("EmptySlotted", []*PyType{ObjectType}, emptyDict)
	emptyInst := NewInstanceOfType(emptyCls).AsInstance()
	if len(emptyInst.Slots) != 0 || cap(emptyInst.Slots) != 0 {
		t.Fatalf("EmptySlotted len/cap = %d/%d, want 0/0", len(emptyInst.Slots), cap(emptyInst.Slots))
	}

	// 3-slot __slots__ -> exact pyInstanceBundle3.
	triDict := NewDictVal(2).AsDict()
	triDict.Set(DunderSlots, NewTupleVal(InternString("x"), InternString("y"), InternString("z")))
	triCls := MustNewType("TriSlotted", []*PyType{ObjectType}, triDict)
	triInst := NewInstanceOfType(triCls).AsInstance()
	if len(triInst.Slots) != 3 || cap(triInst.Slots) != 3 {
		t.Fatalf("TriSlotted len/cap = %d/%d, want 3/3", len(triInst.Slots), cap(triInst.Slots))
	}

	// Primitive subclass with no extra slots -> pyInstanceBundle1.
	strSubNoSlots := MustNewType("TextNoSlots", []*PyType{StrType}, nil)
	vPrim := newPrimSubInstance(strSubNoSlots, NewStringVal("abc"))
	instPrim := vPrim.AsInstance()
	if len(instPrim.Slots) != 1 || cap(instPrim.Slots) != 1 || instPrim.Slots[0].AsString() != "abc" {
		t.Fatalf("TextNoSlots len/cap/val = %d/%d/%v, want 1/1/abc", len(instPrim.Slots), cap(instPrim.Slots), instPrim.Slots[0])
	}

	// Primitive subclass with __slots__ = ('a', 'b') -> Slots[0]=prim, Slots[1..2]=UnboundVal.
	subDict := NewDictVal(2).AsDict()
	subDict.Set(DunderSlots, NewTupleVal(InternString("a"), InternString("b")))
	strSubSlotted := MustNewType("TextSlotted", []*PyType{StrType}, subDict)
	vSlotted := newPrimSubInstance(strSubSlotted, NewStringVal("xyz"))
	instSlotted := vSlotted.AsInstance()
	n := len(strSubSlotted.RootShape.Attrs)
	if len(instSlotted.Slots) != n || cap(instSlotted.Slots) != n {
		t.Fatalf("TextSlotted len/cap = %d/%d, want %d/%d", len(instSlotted.Slots), cap(instSlotted.Slots), n, n)
	}
	if instSlotted.Slots[0].AsString() != "xyz" {
		t.Fatalf("TextSlotted slot 0 = %v, want xyz", instSlotted.Slots[0])
	}
	for i := 1; i < n; i++ {
		if !instSlotted.Slots[i].IsUnbound() {
			t.Fatalf("TextSlotted slot %d = %v, want UnboundVal", i, instSlotted.Slots[i])
		}
	}
}

func TestExceptionBundlesAndLazyAttrErrPoolGuard(t *testing.T) {
	ke := NewKeyError(InternString("missing"))
	keInst := ke.AsInstance()
	if len(keInst.Slots) != 3 || cap(keInst.Slots) != 3 {
		t.Fatalf("KeyError len/cap = %d/%d, want 3/3", len(keInst.Slots), cap(keInst.Slots))
	}
	keInst.SetAttr(InternString("extra"), Int64Val(42))
	if got, ok := keInst.GetAttr(InternString("extra")); !ok || got.AsInt64() != 42 {
		t.Fatalf("KeyError extra attr = (%v, %v), want (42, true)", got, ok)
	}

	si := NewStopIteration(Int64Val(7))
	siInst := si.AsInstance()
	if len(siInst.Slots) != 3 || cap(siInst.Slots) != 3 {
		t.Fatalf("StopIteration len/cap = %d/%d, want 3/3", len(siInst.Slots), cap(siInst.Slots))
	}

	ctx := &ExecContext{}
	lae := NewLazyAttributeErrorCtx(ctx, ObjectType, InternString("nope"))
	laeInst := lae.AsInstance()
	if len(laeInst.Slots) != 4 || cap(laeInst.Slots) != 4 {
		t.Fatalf("LazyAttrErr len/cap = %d/%d, want 4/4", len(laeInst.Slots), cap(laeInst.Slots))
	}
	ReleaseLazyAttributeErrorCtx(ctx, lae)
	if len(ctx.LazyAttrErrFreeList) != 1 {
		t.Fatalf("expected recycled LazyAttrErr in freelist, got %d", len(ctx.LazyAttrErrFreeList))
	}

	// Reacquire from freelist with 0 allocs and verify cap stays 4.
	lae2 := NewLazyAttributeErrorCtx(ctx, ObjectType, InternString("again"))
	if lae2.AsInstance() != laeInst || len(lae2.AsInstance().Slots) != 4 || cap(lae2.AsInstance().Slots) != 4 {
		t.Fatalf("reacquired LazyAttrErr mismatch")
	}

	// If an exception grows past cap 4, ReleaseLazyAttributeErrorCtx must NOT pool it.
	lae2.AsInstance().Slots = append(lae2.AsInstance().Slots, Int64Val(1), Int64Val(2))
	ReleaseLazyAttributeErrorCtx(ctx, lae2)
	if len(ctx.LazyAttrErrFreeList) != 0 {
		t.Fatalf("grown LazyAttrErr must not be pooled, got freelist len %d", len(ctx.LazyAttrErrFreeList))
	}
}

func TestInstanceBundleConcurrentInstantiation(t *testing.T) {
	cls := MustNewType("ConcurrentNode", []*PyType{ObjectType}, nil)
	SetMultiThreaded(true)
	defer SetMultiThreaded(false)

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 200 {
				inst := NewInstanceOfType(cls).AsInstance()
				inst.SetAttr(InternString("id"), Int64Val(int64(id)))
				inst.SetAttr(InternString("seq"), Int64Val(int64(i)))
				inst.SetAttr(InternString("tag"), InternString("ok"))
				v, ok := inst.GetAttr(InternString("seq"))
				if !ok || v.AsInt64() != int64(i) {
					t.Errorf("concurrent GetAttr(seq) = (%v, %v), want (%d, true)", v, ok, i)
				}
			}
		}(g)
	}
	wg.Wait()
	if got := cls.likelySlotCap.Load(); got < 3 {
		t.Fatalf("likelySlotCap after concurrent runs = %d, want >= 3", got)
	}
}
