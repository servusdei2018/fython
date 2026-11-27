package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestDictCtorSplitCacheAndExactSizing(t *testing.T) {
	prevMT := val.IsMultiThreaded()
	val.SetMultiThreaded(false)
	defer val.SetMultiThreaded(prevMT)

	_, v := runPySource(t, `
# 1. Empty dict()
empty_d = dict()
assert empty_d == {} and len(empty_d) == 0

# 2. dict(k=v) split-dict hit & key order preservation
def make_rec(x):
    return dict(alpha=x, beta=x + 1, gamma=x + 2)

r1 = make_rec(10)
r2 = make_rec(20)
assert r1 == {"alpha": 10, "beta": 11, "gamma": 12}
assert r2 == {"alpha": 20, "beta": 21, "gamma": 22}
assert list(r1.keys()) == ["alpha", "beta", "gamma"]
assert list(r2.keys()) == ["alpha", "beta", "gamma"]

# 3. Post-construction mutation triggers DisableSplit self-disable cleanly
def make_mutated(x):
    d = dict(a=x, b=x + 1)
    d["extra"] = x + 2
    return d

m1 = make_mutated(1)
m2 = make_mutated(2)
m3 = make_mutated(3)
assert m1 == {"a": 1, "b": 2, "extra": 3}
assert m2 == {"a": 2, "b": 3, "extra": 4}
assert m3 == {"a": 3, "b": 4, "extra": 5}

# 4. dict subclass preservation
class MyDict(dict):
    pass

sub = MyDict(x=1, y=2)
assert type(sub) is MyDict
assert sub["x"] == 1 and sub["y"] == 2

# 5. dict(mapping, a=1) and iterable constructors
base = {"a": 1, "b": 2}
merged = dict(base, a=99, c=3)
assert merged == {"a": 99, "b": 2, "c": 3}
from_pairs = dict([("k1", 1), ("k2", 2), ("k3", 3)])
assert from_pairs == {"k1": 1, "k2": 2, "k3": 3}
from_gen = dict((i, i * 2) for i in range(6))
assert from_gen == {0: 0, 1: 2, 2: 4, 3: 6, 4: 8, 5: 10}

# 6. Dict comprehensions (empty, small <=5, and >5)
comp_empty = {k: v for k, v in []}
assert comp_empty == {} and len(comp_empty) == 0
comp_small = {i: i * i for i in range(4)}
assert comp_small == {0: 0, 1: 1, 2: 4, 3: 9}
comp_large = {i: i + 10 for i in range(10)}
assert len(comp_large) == 10 and comp_large[9] == 19
`)
	// Verify r1/r2 actually used SplitDictKeys and m3 self-disabled split.
	r1Val, ok := v.Globals().Get(val.InternString("r1"))
	if !ok || !r1Val.IsDict() {
		t.Fatalf("expected global dict r1")
	}
	if !r1Val.AsDict().IsSplit() {
		t.Fatalf("expected dict(alpha=..., beta=..., gamma=...) to use SplitDictKeys")
	}
	r2Val, _ := v.Globals().Get(val.InternString("r2"))
	if r1Val.AsDict().SplitKeys() != r2Val.AsDict().SplitKeys() {
		t.Fatalf("expected r1 and r2 to share the same cached SplitDictKeys")
	}
	m3Val, ok := v.Globals().Get(val.InternString("m3"))
	if !ok || !m3Val.IsDict() || m3Val.AsDict().IsSplit() {
		t.Fatalf("expected mutated dict(a=..., b=...) to be combined")
	}
}
