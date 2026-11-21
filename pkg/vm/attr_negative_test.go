package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

// TestAttrQueryNegativeMiss verifies confirmed-missing attributes cache with
// the -2 sentinel and hit O(1) on repeat probes.
func TestAttrQueryNegativeMiss(t *testing.T) {
	v := NewVM()
	tp := val.MustNewType("NegMissCls", []*val.PyType{val.ObjectType}, nil)
	instVal := val.NewInstanceOfType(tp)
	inst := instVal.AsInstance()
	name := val.InternString("no_such_attr_xyz")

	co := &CodeObject{Name: "negmiss", Code: []uint32{Encode(OP_NOP, 0, 0)}, NumLocals: 1, MaxStackSize: 4}
	co.Init()
	cc := v.cacheFor(co)
	st := v.acquireLoopState()
	defer v.releaseLoopState(st)
	st.cache = cc
	st.curCO = co

	_, found, handled, interned := v.lookupAttrQueryPIC(st, 0, instVal, name, true)
	if !handled || found {
		t.Fatalf("first probe: handled=%v found=%v, want handled=true found=false", handled, found)
	}
	pic := &cc.ensureAttrQueryPIC()[0]
	hit := false
	for k := range 4 {
		if pic[k].Kind == attrQueryMiss && pic[k].Name == interned {
			if pic[k].SlotIndex != attrQueryMissingSlot {
				t.Fatalf("negative entry SlotIndex=%d, want -2", pic[k].SlotIndex)
			}
			hit = true
		}
	}
	if !hit {
		t.Fatalf("negative entry not cached")
	}
	_ = inst

	_, found2, handled2, _ := v.lookupAttrQueryPIC(st, 0, instVal, name, true)
	if !handled2 || found2 {
		t.Fatalf("second probe: handled=%v found=%v, want handled=true found=false", handled2, found2)
	}
}

// TestAttrQueryNegativeInvalidInstance verifies adding an instance attribute
// changes Shape.ID so the old -2 entry no longer hits.
func TestAttrQueryNegativeInvalidInstance(t *testing.T) {
	v := NewVM()
	tp := val.MustNewType("NegInvInst", []*val.PyType{val.ObjectType}, nil)
	instVal := val.NewInstanceOfType(tp)
	name := val.InternString("later_attr_q")

	co := &CodeObject{Name: "neginv", Code: []uint32{Encode(OP_NOP, 0, 0)}, NumLocals: 1, MaxStackSize: 4}
	co.Init()
	cc := v.cacheFor(co)
	st := v.acquireLoopState()
	defer v.releaseLoopState(st)
	st.cache = cc
	st.curCO = co

	if _, found, handled, _ := v.lookupAttrQueryPIC(st, 0, instVal, name, true); !handled || found {
		t.Fatalf("pre probe: handled=%v found=%v", handled, found)
	}
	instVal.AsInstance().SetAttr(name, val.Int64Val(7))
	res, found, handled, _ := v.lookupAttrQueryPIC(st, 0, instVal, name, true)
	if !handled || !found {
		t.Fatalf("post instance-set: handled=%v found=%v, want true/true", handled, found)
	}
	if !res.IsInt64() || res.AsInt64() != 7 {
		t.Fatalf("post instance-set value=%v, want 7", res)
	}
}

// TestAttrQueryNegativeInvalidClass verifies adding a class attribute bumps
// TypeVer so the old -2 entry no longer hits.
func TestAttrQueryNegativeInvalidClass(t *testing.T) {
	v := NewVM()
	tp := val.MustNewType("NegInvCls", []*val.PyType{val.ObjectType}, nil)
	instVal := val.NewInstanceOfType(tp)
	name := val.InternString("cls_later_attr_q")

	co := &CodeObject{Name: "negcls", Code: []uint32{Encode(OP_NOP, 0, 0)}, NumLocals: 1, MaxStackSize: 4}
	co.Init()
	cc := v.cacheFor(co)
	st := v.acquireLoopState()
	defer v.releaseLoopState(st)
	st.cache = cc
	st.curCO = co

	if _, found, handled, _ := v.lookupAttrQueryPIC(st, 0, instVal, name, true); !handled || found {
		t.Fatalf("pre probe: handled=%v found=%v", handled, found)
	}
	tp.SetAttr(name, val.Int64Val(42))
	res, found, handled, _ := v.lookupAttrQueryPIC(st, 0, instVal, name, true)
	if !handled || !found {
		t.Fatalf("post class-set: handled=%v found=%v, want true/true", handled, found)
	}
	if !res.IsInt64() || res.AsInt64() != 42 {
		t.Fatalf("post class-set value=%v, want 42", res)
	}
}
