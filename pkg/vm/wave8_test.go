package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestWave8SplitTableConstKeyMapDicts(t *testing.T) {
	runPySource(t, `
import json

def make_rec(a, b, c):
    return {"alpha": a, "beta": b, "gamma": c}

def make_wide(i):
    return {
        "k0": i, "k1": i + 1, "k2": i + 2, "k3": i + 3,
        "k4": i + 4, "k5": i + 5, "k6": i + 6, "k7": i + 7,
    }

# Warm up const-key template
for i in range(10):
    d = make_rec(i, i * 2, i * 3)
    assert len(d) == 3
    assert d["alpha"] == i
    assert d["beta"] == i * 2
    assert d["gamma"] == i * 3
    assert d.get("missing", -1) == -1
    assert list(d.keys()) == ["alpha", "beta", "gamma"]
    assert list(d.values()) == [i, i * 2, i * 3]
    assert list(d.items()) == [("alpha", i), ("beta", i * 2), ("gamma", i * 3)]
    assert list(d) == ["alpha", "beta", "gamma"]
    assert set(d) == {"alpha", "beta", "gamma"}

# Overwrite existing key in-place
d1 = make_rec(1, 2, 3)
d1["beta"] = 20
assert d1["beta"] == 20
assert d1 == {"alpha": 1, "beta": 20, "gamma": 3}

# Copy preserves split semantics and independence
d2 = d1.copy()
d2["alpha"] = 99
assert d1["alpha"] == 1
assert d2["alpha"] == 99

# JSON serialization
s = json.dumps(make_rec(10, "hi", True))
assert s == '{"alpha": 10, "beta": "hi", "gamma": true}', s

# Unpacking (**d) and dict merge
def take_kw(alpha, beta, gamma):
    return alpha + beta + gamma
assert take_kw(**make_rec(2, 3, 5)) == 10
merged = {**make_rec(1, 2, 3), "delta": 4}
assert merged == {"alpha": 1, "beta": 2, "gamma": 3, "delta": 4}

# Promotion on new key insert
d3 = make_rec(1, 2, 3)
d3["delta"] = 4
assert len(d3) == 4 and d3["delta"] == 4 and d3["alpha"] == 1

# Promotion on delete / pop / popitem / update / clear
d4 = make_rec(1, 2, 3)
del d4["beta"]
assert "beta" not in d4 and len(d4) == 2
assert d4.pop("alpha") == 1
assert d4.popitem() == ("gamma", 3)
assert len(d4) == 0

d5 = make_rec(7, 8, 9)
d5.update({"beta": 80, "extra": 100})
assert d5 == {"alpha": 7, "beta": 80, "gamma": 9, "extra": 100}
d5.clear()
assert len(d5) == 0 and list(d5.items()) == []

# Wide 8-key split dict (hash-probed splitLookup)
w = make_wide(5)
assert len(w) == 8 and w["k0"] == 5 and w["k7"] == 12
w["k4"] = 44
assert w["k4"] == 44
w["k8"] = 88
assert len(w) == 9 and w["k8"] == 88 and w["k4"] == 44
`)
}

func TestWave8FusedTupleBundleAllocsAndSemantics(t *testing.T) {
	for n := range 11 {
		elems := make([]val.Value, n)
		for i := range n {
			elems[i] = val.Int64Val(int64(i + 1))
		}
		tv := val.NewTupleFromSlice(elems)
		tp := tv.AsTuple()
		if len(tp.Items) != n {
			t.Fatalf("len=%d: got %d", n, len(tp.Items))
		}
		for i := range n {
			if tp.Items[i].AsInt64() != int64(i+1) {
				t.Fatalf("len=%d idx=%d: got %v", n, i, tp.Items[i])
			}
		}
		if n >= 1 && n <= 8 {
			allocs := testing.AllocsPerRun(100, func() {
				_ = val.NewTupleFromSlice(elems)
			})
			if allocs != 1 {
				t.Fatalf("NewTupleFromSlice(len=%d) allocated %.1f times, want 1", n, allocs)
			}
		}
	}

	runPySource(t, `
t4 = (1, 2, 3, 4)
t6 = (1, 2, 3, 4, 5, 6)
t8 = (1, 2, 3, 4, 5, 6, 7, 8)
t10 = (1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
assert t4 == (1, 2, 3, 4) and hash(t4) == hash((1, 2, 3, 4))
assert t6[1:5] == (2, 3, 4, 5)
assert t8[:4] == t4
assert len(t10) == 10 and t10[-1] == 10
d = {t4: "four", t6: "six", t8: "eight"}
assert d[(1, 2, 3, 4)] == "four"
assert d[(1, 2, 3, 4, 5, 6)] == "six"
assert d[(1, 2, 3, 4, 5, 6, 7, 8)] == "eight"
`)
}

func TestWave8ClosureCellsAndDataclass(t *testing.T) {
	runPySource(t, `
from dataclasses import dataclass, asdict, replace

# super() / __class__ cell
class Base:
    def greet(self):
        return "base"

class Child(Base):
    def greet(self):
        return super().greet() + "+child"

assert Child().greet() == "base+child"

# nonlocal mutation + cell_contents inspection (1, 2, 4 cells)
def make_counter(start):
    x = start
    y = start * 2
    def inc(delta):
        nonlocal x, y
        x += delta
        y += delta * 2
        return (x, y)
    return inc

c = make_counter(10)
assert c(3) == (13, 26)
assert c(2) == (15, 30)
assert len(c.__closure__) == 2
assert {cell.cell_contents for cell in c.__closure__} == {15, 30}

# Late-binding closures in loops
fns = []
for i in range(4):
    fns.append(lambda: i)
assert [f() for f in fns] == [3, 3, 3, 3]

# @dataclass asdict / replace
@dataclass
class Point:
    x: int
    y: int
    label: str = "p"

p = Point(3, 4)
assert asdict(p) == {"x": 3, "y": 4, "label": "p"}
p2 = replace(p, y=10)
assert p2 == Point(3, 10, "p")
`)
}

func TestWave8SplitDictSelfDisableOnMutation(t *testing.T) {
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
