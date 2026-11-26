package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestSplitDictSelfDisableOnMutation(t *testing.T) {
	// Verify split-dict self-disable threshold (2 promotions), Clear() on split
	// dicts, tombstoned combined dicts, and Live obj.__dict__ proxies.
	keys := &val.SplitDictKeys{
		Keys:   [16]val.Value{val.InternString("a"), val.InternString("b")},
		Hashes: [16]uint32{val.HashValue(val.InternString("a")), val.HashValue(val.InternString("b"))},
		Count:  2,
		Mask:   7,
	}
	for i := range keys.Count {
		slot := keys.Hashes[i] & keys.Mask
		for keys.Indices[slot] != 0 {
			slot = (slot + 1) & keys.Mask
		}
		keys.Indices[slot] = byte(i + 2)
	}

	// 1st promotion: Promotions == 1, DisableSplit still false.
	d1 := val.NewSplitDict(keys, []val.Value{val.Int64Val(1), val.Int64Val(2)}, 0)
	if !d1.IsSplit() {
		t.Fatal("d1 should start split")
	}
	d1.Set(val.InternString("c"), val.Int64Val(3))
	if d1.IsSplit() {
		t.Fatal("d1 should promote on new key")
	}
	if keys.Promotions.Load() != 1 || keys.DisableSplit.Load() {
		t.Fatalf("after 1st promotion: promotions=%d disabled=%v, want 1 false", keys.Promotions.Load(), keys.DisableSplit.Load())
	}

	// Clear() on a split dict must clear d.split and return false on Get, then accept Set.
	dClear := val.NewSplitDict(keys, []val.Value{val.Int64Val(10), val.Int64Val(20)}, 0)
	dClear.Clear()
	if dClear.IsSplit() {
		t.Fatal("cleared split dict must not remain split")
	}
	if _, ok := dClear.Get(val.InternString("a")); ok {
		t.Fatal("cleared split dict returned stale split key")
	}
	dClear.Set(val.InternString("a"), val.Int64Val(99))
	if got, ok := dClear.Get(val.InternString("a")); !ok || got.AsInt64() != 99 {
		t.Fatalf("post-clear Get(a) = (%v, %v), want (99, true)", got, ok)
	}

	// 2nd promotion: Promotions == 2, DisableSplit becomes true.
	d2 := val.NewSplitDict(keys, []val.Value{val.Int64Val(4), val.Int64Val(5)}, 0)
	d2.Set(val.InternString("d"), val.Int64Val(6))
	if keys.Promotions.Load() != 2 || !keys.DisableSplit.Load() {
		t.Fatalf("after 2nd promotion: promotions=%d disabled=%v, want 2 true", keys.Promotions.Load(), keys.DisableSplit.Load())
	}

	// Copy() of an existing split dict whose keys are disabled produces a combined dict.
	d3 := val.NewSplitDict(keys, []val.Value{val.Int64Val(7), val.Int64Val(8)}, 0)
	d3Copy := d3.Copy()
	if d3Copy.IsSplit() {
		t.Fatal("Copy() of disabled split dict should materialize combined dict")
	}
	if got, ok := d3Copy.Get(val.InternString("b")); !ok || got.AsInt64() != 8 {
		t.Fatalf("d3Copy.Get(b) = (%v, %v), want (8, true)", got, ok)
	}

	// Combined dict with all entries deleted (used == 0, len(Entries) > 0).
	if !d3Copy.Delete(val.InternString("a")) || !d3Copy.Delete(val.InternString("b")) {
		t.Fatal("expected deletes to succeed")
	}
	if _, ok := d3Copy.Get(val.InternString("a")); ok {
		t.Fatal("tombstoned empty dict should return false on Get")
	}
	if _, _, ok := d3Copy.GetWithSlot(val.InternString("b")); ok {
		t.Fatal("tombstoned empty dict should return false on GetWithSlot")
	}
	if d3Copy.Delete(val.InternString("a")) {
		t.Fatal("tombstoned empty dict should return false on Delete")
	}

	// End-to-end VM test: mutating const-key literal self-disables, non-mutating stays split,
	// and Live obj.__dict__ proxies remain intact for Get/Set/Delete/Copy.
	runPySource(t, `
def make_mut(x):
    d = {"a": x, "b": x + 1}
    d["c"] = x + 2
    return d

def make_ro(x):
    return {"x": x, "y": x * 2}

for i in range(10):
    m = make_mut(i)
    assert m == {"a": i, "b": i + 1, "c": i + 2}
    r = make_ro(i)
    assert r["x"] == i and r["y"] == i * 2

# Clear on split dict
s = make_ro(5)
s.clear()
assert len(s) == 0 and s.get("x") is None
s["z"] = 42
assert s == {"z": 42}

# Live obj.__dict__ proxy (d.Live != nil, len(d.Entries) == 0)
class Box:
    def __init__(self, v):
        self.v = v

b = Box(10)
assert b.__dict__["v"] == 10
assert b.__dict__.get("v") == 10
assert b.__dict__.get("missing") is None
b.__dict__["w"] = 20
assert b.w == 20 and b.__dict__["w"] == 20
cp = b.__dict__.copy()
assert cp == {"v": 10, "w": 20}
del b.__dict__["v"]
assert "v" not in b.__dict__ and not hasattr(b, "v")
`)
}
