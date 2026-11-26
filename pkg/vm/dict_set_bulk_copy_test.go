package vm_test

import (
	"testing"
)

func TestDictAndSetBulkCopyUpdate(t *testing.T) {
	runSnippet(t, `
# Small (<= 5) and large (> 5) dict.copy() isolation
for n in (0, 1, 5, 6, 16, 40):
    orig = {f"k{i}": i for i in range(n)}
    cp = orig.copy()
    assert cp == orig
    assert cp is not orig
    cp["extra"] = -1
    assert "extra" not in orig
    if n > 0:
        orig["k0"] = 9999
        assert cp["k0"] == 0

# dict.update() with deletions (tombstones), overwrites, and self-update
d = {f"k{i}": i for i in range(20)}
for i in range(0, 20, 2):
    del d[f"k{i}"]
other = {f"k{i}": i * 10 for i in range(15)}
d.update(other)
for i in range(15):
    assert d[f"k{i}"] == i * 10
for i in range(15, 20):
    if i % 2 == 1:
        assert d[f"k{i}"] == i
    else:
        assert f"k{i}" not in d

snap = d.copy()
d.update(d)
assert d == snap

# set.copy() and set(dict) / frozenset(set_or_dict) isolation
s = {i for i in range(24)}
sc = s.copy()
assert sc == s and sc is not s
sc.add(999)
assert 999 not in s

sd = set({"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7})
assert sd == {"a", "b", "c", "d", "e", "f", "g"}
fs = frozenset({"x": 1, "y": 2})
assert "x" in fs and "y" in fs and len(fs) == 2
`)
}
