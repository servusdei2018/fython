package vm_test

import (
	"testing"
)

func TestDictSetCopyAndPrimitiveTupleHash(t *testing.T) {
	_, _ = runPySource(t, `
# Small (<=5) and large (>5) dict copy + mutation isolation
d_small = {"a": 1, "b": 2, "c": 3}
c_small = d_small.copy()
assert c_small == d_small and c_small is not d_small
c_small["a"] = 99
d_small["d"] = 4
assert d_small["a"] == 1 and "d" not in c_small

d_large = {f"k{i}": i for i in range(32)}
c_large = d_large.copy()
assert c_large == d_large and list(c_large.items()) == list(d_large.items())
c_large["k0"] = -1
d_large["k32"] = 32
assert d_large["k0"] == 0 and "k32" not in c_large

# Tombstone compaction path after deletes
for i in range(20):
    del d_large[f"k{i}"]
c_tomb = d_large.copy()
assert c_tomb == d_large and list(c_tomb.keys()) == list(d_large.keys())

# Set copy (small, large, tombstoned) + frozenset semantics
s_large = set(range(32))
sc = s_large.copy()
assert sc == s_large and sc is not s_large and type(sc) is set
sc.add(999)
assert 999 not in s_large
for i in range(20):
    s_large.remove(i)
sc_tomb = s_large.copy()
assert sc_tomb == s_large

fs = frozenset(range(10))
assert frozenset(fs) == fs
assert type(fs.copy()) is frozenset

# Primitive-tuple hash bit-exactness
assert hash((1, "a")) == hash((True, "a")) == hash((1.0, "a"))
assert hash((0, "")) == hash((False, "")) == hash((0.0, ""))
assert hash((1, None, "long_string_value_12345")) == hash((True, None, "long_string_value_12345"))
assert hash(((1, 2), "x")) == hash(((True, 2.0), "x"))

d_tup = {(1, "a"): "hit", (0, ""): "zero"}
assert d_tup[(True, "a")] == "hit"
assert d_tup[(1.0, "a")] == "hit"
assert d_tup[(False, "")] == "zero"

s_tup = {(1, 2, "abc"), (0, 0, "")}
assert (True, 2, "abc") in s_tup
assert (1.0, 2.0, "abc") in s_tup
assert (False, False, "") in s_tup

# Unhashable element raises TypeError
unhashable_err = False
try:
    hash(([1], 2))
except TypeError:
    unhashable_err = True
assert unhashable_err
`)
}
