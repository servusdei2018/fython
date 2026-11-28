package vm_test

import "testing"

func TestLazyDictViewsSemanticsAndSetOps(t *testing.T) {
	code := `
import collections.abc as cabc
import types

d = {"a": 1, "b": 2, "c": 3}
ks = d.keys()
vs = d.values()
its = d.items()

# Types and collections.abc registration (zero overlays)
assert type(ks).__name__ == "dict_keys"
assert type(vs).__name__ == "dict_values"
assert type(its).__name__ == "dict_items"
assert isinstance(ks, cabc.KeysView) and isinstance(ks, cabc.MappingView)
assert isinstance(vs, cabc.ValuesView) and isinstance(vs, cabc.MappingView)
assert isinstance(its, cabc.ItemsView) and isinstance(its, cabc.MappingView)
assert not isinstance([], cabc.ValuesView)
assert not isinstance([], cabc.ItemsView)

# Live reflection across insert, update, delete
assert len(ks) == 3 and len(vs) == 3 and len(its) == 3
assert bool(ks) and bool(vs) and bool(its)
assert "a" in ks and "z" not in ks and [] not in ks
assert 2 in vs and 99 not in vs
assert ("b", 2) in its and ("b", 99) not in its and "b" not in its and ([], 1) not in its

d["d"] = 4
d["a"] = 10
del d["b"]
assert len(ks) == 3 and list(ks) == ["a", "c", "d"]
assert list(vs) == [10, 3, 4]
assert list(its) == [("a", 10), ("c", 3), ("d", 4)]
assert list(reversed(ks)) == ["d", "c", "a"]
assert list(reversed(vs)) == [4, 3, 10]
assert list(reversed(its)) == [("d", 4), ("c", 3), ("a", 10)]

# Repr and .mapping read-only property
assert repr(ks) == "dict_keys(['a', 'c', 'd'])"
assert repr(vs) == "dict_values([10, 3, 4])"
assert repr(its) == "dict_items([('a', 10), ('c', 3), ('d', 4)])"
assert isinstance(ks.mapping, types.MappingProxyType)
assert ks.mapping["a"] == 10 and vs.mapping["c"] == 3 and its.mapping["d"] == 4
try:
    ks.mapping = {}
    assert False, "mapping should be read-only"
except AttributeError:
    pass

# Unhashable views
for v in (ks, vs, its):
    try:
        hash(v)
        assert False, "dict view should be unhashable"
    except TypeError:
        pass

# Self-referential repr must not stack overflow
rec = {}
rec["self"] = rec
rv = repr(rec.values())
ri = repr(rec.items())
assert "{...}" in rv and "{...}" in ri

# Identity-first NaN parity
nan = float("nan")
dn = {"a": nan}
assert nan in dn.values()
assert ("a", nan) in dn.items()
assert dn.items() == {"a": nan}.items()

# Set-like ops on dict_keys and dict_items (accepting any iterable)
d1 = {"a": 1, "b": 2}
d2 = {"b": 2, "c": 3}
assert d1.keys() | d2.keys() == {"a", "b", "c"}
assert d1.keys() & d2.keys() == {"b"}
assert d1.keys() - d2.keys() == {"a"}
assert d1.keys() ^ d2.keys() == {"a", "c"}
assert d1.keys() | ["c", "d"] == {"a", "b", "c", "d"}
assert ["b", "c"] - d1.keys() == {"c"}
assert d1.keys() - ["b", "c"] == {"a"}
assert d1.keys().isdisjoint(["x", "y"])
assert not d1.keys().isdisjoint(["b", "x"])
assert d1.items() | d2.items() == {("a", 1), ("b", 2), ("c", 3)}
assert d1.items() & d2.items() == {("b", 2)}
assert d1.items() - d2.items() == {("a", 1)}
assert d1.items() ^ d2.items() == {("a", 1), ("c", 3)}
assert [("b", 2), ("z", 9)] - d1.items() == {("z", 9)}
assert d1.items().isdisjoint([("a", 99)])
assert not d1.items().isdisjoint([("a", 1)])

# Comparisons: set/view vs non-set/non-view
assert d1.keys() == {"a", "b"}
assert d1.keys() == frozenset(["a", "b"])
assert not (d1.keys() == ["a", "b"])
assert d1.keys() != ["a", "b"]
assert {"a"} < d1.keys() and d1.keys() > {"a"}
assert d1.keys() <= {"a", "b"} and d1.keys() >= {"a", "b"}
try:
    _ = d1.keys() < ["a", "b", "c"]
    assert False, "expected TypeError for dict_keys < list"
except TypeError:
    pass

# Unhashable values in dict_items: comparisons succeed, set-returning ops raise TypeError
u1 = {"a": [1, 2]}
u2 = {"a": [1, 2], "b": [3]}
assert u1.items() == {"a": [1, 2]}.items()
assert u1.items() != u2.items()
assert u1.items() < u2.items()
assert u1.items() <= u2.items()
assert u2.items() > u1.items()
assert u2.items() >= u1.items()
try:
    _ = u1.items() | u2.items()
    assert False, "expected TypeError for unhashable dict_items |"
except TypeError:
    pass

# dict_values identity-only equality and no set ops
v1 = d1.values()
v2 = d1.values()
assert v1 == v1
assert not (v1 == v2)
assert v1 != v2
for op in (lambda: v1 | v2, lambda: v1 & v2, lambda: v1 - v2, lambda: v1 < v2):
    try:
        op()
        assert False, "expected TypeError on dict_values op"
    except TypeError:
        pass

# MappingProxyType views are also lazy dict views
mp = types.MappingProxyType(d1)
assert type(mp.keys()).__name__ == "dict_keys"
assert type(mp.values()).__name__ == "dict_values"
assert type(mp.items()).__name__ == "dict_items"
`
	runPySource(t, code)
}

func TestDictViewMutationGuardsAndUnpackIsolation(t *testing.T) {
	code := `
# Size-change guard on forward and reverse view iterators
for make_it in (
    lambda d: iter(d),
    lambda d: iter(d.keys()),
    lambda d: iter(d.values()),
    lambda d: iter(d.items()),
    lambda d: reversed(d.keys()),
    lambda d: reversed(d.values()),
    lambda d: reversed(d.items()),
):
    d = {"a": 1, "b": 2}
    it = make_it(d)
    _ = next(it)
    d["c"] = 3
    try:
        _ = next(it)
        assert False, "expected RuntimeError on size change"
    except RuntimeError as e:
        assert "dictionary changed size during iteration" in str(e)

    # Post-exhaustion mutation must NOT raise
    d2 = {"a": 1}
    it2 = make_it(d2)
    assert list(it2) is not None
    d2["b"] = 2
    assert list(it2) == []

# for k, v in d.items() unpack path vs retained item tuple isolation
d = {i: i * 10 for i in range(100)}
s = 0
for k, v in d.items():
    s += k + v
assert s == sum(i + i * 10 for i in range(100))

# Retained items must never alias each other
kept = []
for item in d.items():
    kept.append(item)
assert len(kept) == 100
assert kept[0] == (0, 0) and kept[50] == (50, 500) and kept[99] == (99, 990)
assert len(set(id(x) for x in kept)) == 100

# Split-table dict and live instance __dict__ views
def make_split(a, b, c):
    return dict(alpha=a, beta=b, gamma=c)

sd = make_split(1, 2, 3)
sv = sd.values()
si = sd.items()
sd["beta"] = 20
assert list(sv) == [1, 20, 3]
assert list(reversed(si)) == [("gamma", 3), ("beta", 20), ("alpha", 1)]

class Box:
    def __init__(self):
        self.x = 10
        self.y = 20

b = Box()
bv = b.__dict__.values()
bi = b.__dict__.items()
b.z = 30
assert list(bv) == [10, 20, 30]
assert list(reversed(bi)) == [("z", 30), ("y", 20), ("x", 10)]

# dict.pop / dict.setdefault unhashable key must raise TypeError (never return default)
for fn in (lambda: d.pop([], "def"), lambda: d.setdefault([], "def")):
    try:
        fn()
        assert False, "expected TypeError for unhashable key"
    except TypeError:
        pass

# dict.update(self), d |= d, dict | dict, dict.fromkeys
m = {i: i for i in range(50)}
m.update(m)
m |= m
assert len(m) == 50
m2 = {i: i * 2 for i in range(25, 75)}
merged = m | m2
assert len(merged) == 75 and merged[0] == 0 and merged[30] == 60 and merged[74] == 148
fk = dict.fromkeys(range(100), 7)
assert len(fk) == 100 and fk[0] == 7 and fk[99] == 7
`
	runPySource(t, code)
}
