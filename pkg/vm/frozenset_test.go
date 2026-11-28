package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestFrozenSetSemantics(t *testing.T) {
	_, _ = runPySource(t, `
import collections.abc

# Type separation
assert frozenset is not set
assert type(frozenset()) is frozenset
assert isinstance(frozenset(), frozenset)
assert not isinstance(frozenset(), set)
assert not issubclass(frozenset, set)
assert not issubclass(set, frozenset)
assert isinstance(frozenset(), collections.abc.Set)
assert not isinstance(frozenset(), collections.abc.MutableSet)
assert isinstance(frozenset(), collections.abc.Hashable)
assert not isinstance(set(), collections.abc.Hashable)

# Empty singleton & identity fast paths
e1 = frozenset()
e2 = frozenset()
e3 = frozenset([])
assert e1 is e2
assert e1 is e3
assert e1.copy() is e1

fs = frozenset([1, 2, 3])
assert frozenset(fs) is fs
assert fs.copy() is fs

# set(frozenset) produces mutable set, not frozenset
s_from_fs = set(fs)
assert type(s_from_fs) is set
assert s_from_fs == fs
s_from_fs.add(4)
assert 4 not in fs

# Subclass construction & CPython return-type semantics
class MyFS(frozenset):
    pass

m0 = MyFS()
assert type(m0) is MyFS
assert m0 is not e1
assert isinstance(m0, frozenset)
assert not isinstance(m0, set)

m1 = MyFS({1, 2})
assert type(m1) is MyFS
assert m1 == {1, 2}
assert frozenset(m1) is not m1
assert type(frozenset(m1)) is frozenset
assert type(m1.copy()) is frozenset
assert m1.copy() is not m1
assert type(m1 | {3}) is frozenset
assert type(m1.union({3})) is frozenset
assert repr(m0) == "MyFS()"
assert repr(MyFS({1})) == "MyFS({1})"

# Subclass defining __eq__ without __hash__ becomes unhashable
class UnhashableFS(frozenset):
    def __eq__(self, other):
        return super().__eq__(other)

u = UnhashableFS({1, 2})
for fn in (lambda: hash(u), lambda: {u: 1}, lambda: {u}):
    raised = False
    try:
        fn()
    except TypeError:
        raised = True
    assert raised

# Immutability: no mutating methods on frozenset
for name in ("add", "remove", "discard", "pop", "clear", "update",
             "intersection_update", "difference_update", "symmetric_difference_update"):
    assert not hasattr(fs, name), f"unexpected attribute {name} on frozenset"
    raised = False
    try:
        getattr(fs, name)
    except AttributeError:
        raised = True
    assert raised

# Unbound set.<mutator>(frozenset(), ...) must raise TypeError
for fn in (
    lambda: set.add(frozenset(), 1),
    lambda: set.remove(frozenset({1}), 1),
    lambda: set.discard(frozenset({1}), 1),
    lambda: set.pop(frozenset({1})),
    lambda: set.clear(frozenset({1})),
    lambda: set.update(frozenset(), {1}),
    lambda: set.intersection_update(frozenset({1}), {1}),
    lambda: set.difference_update(frozenset({1}), {1}),
    lambda: set.symmetric_difference_update(frozenset({1}), {2}),
    lambda: set.__ior__(frozenset(), {1}),
    lambda: set.__iand__(frozenset({1}), {1}),
    lambda: set.__isub__(frozenset({1}), {1}),
    lambda: set.__ixor__(frozenset({1}), {2}),
):
    raised = False
    try:
        fn()
    except TypeError:
        raised = True
    assert raised
assert len(frozenset()) == 0

# In-place operators: frozenset returns new frozenset without mutating alias
orig = frozenset({1, 2})
alias = orig
orig |= {3}
assert type(orig) is frozenset and orig == frozenset({1, 2, 3})
assert alias == frozenset({1, 2}) and orig is not alias

orig &= {2, 3, 4}
assert type(orig) is frozenset and orig == frozenset({2, 3})
orig -= {3}
assert type(orig) is frozenset and orig == frozenset({2})
orig ^= {2, 5}
assert type(orig) is frozenset and orig == frozenset({5})

# In-place operators: mutable set mutates in place so aliases see changes
ms = {1, 2}
ms_alias = ms
ms |= frozenset({3})
assert ms is ms_alias and ms == {1, 2, 3}
ms &= frozenset({2, 3, 4})
assert ms is ms_alias and ms == {2, 3}
ms -= frozenset({3})
assert ms is ms_alias and ms == {2}
ms ^= frozenset({2, 9})
assert ms is ms_alias and ms == {9}

# In-place operator on set with non-set raises TypeError (unlike .update)
for op in (
    lambda: (lambda s: s.__ior__([1]))({0}),
):
    assert op() is NotImplemented

bad_ior = False
try:
    s_tmp = {1}
    s_tmp |= [2]
except TypeError:
    bad_ior = True
assert bad_ior

# Cross-type equality, ordering, isdisjoint, and receiver-type preservation
s12 = {1, 2}
fs12 = frozenset({1, 2})
fs123 = frozenset({1, 2, 3})
assert s12 == fs12 and fs12 == s12
assert not (s12 != fs12)
assert s12 < fs123 and s12 <= fs12 and fs12 <= s12
assert fs123 > s12 and fs123 >= s12 and fs12 >= s12
assert fs12.isdisjoint({3, 4}) and not fs12.isdisjoint({2, 3})

assert type(fs12 | {3}) is frozenset and (fs12 | {3}) == {1, 2, 3}
assert type({3} | fs12) is set and ({3} | fs12) == {1, 2, 3}
assert type(fs12 & {2, 3}) is frozenset and (fs12 & {2, 3}) == {2}
assert type({2, 3} & fs12) is set and ({2, 3} & fs12) == {2}
assert type(fs12 - {2}) is frozenset and (fs12 - {2}) == {1}
assert type({2, 3} - fs12) is set and ({2, 3} - fs12) == {3}
assert type(fs12 ^ {2, 3}) is frozenset and (fs12 ^ {2, 3}) == {1, 3}
assert type({2, 3} ^ fs12) is set and ({2, 3} ^ fs12) == {1, 3}

assert type(fs12.union([3, 4])) is frozenset and fs12.union([3, 4]) == {1, 2, 3, 4}
assert type(fs12.intersection([2, 3])) is frozenset and fs12.intersection([2, 3]) == {2}
assert type(fs12.difference([2])) is frozenset and fs12.difference([2]) == {1}
assert type(fs12.symmetric_difference([2, 3])) is frozenset and fs12.symmetric_difference([2, 3]) == {1, 3}

# Order-independent hashing, dict keys, nested sets
assert hash(frozenset([1, 2, 3])) == hash(frozenset([3, 1, 2]))
assert hash(frozenset([1, 2])) == hash(frozenset([True, 2.0]))
d = {frozenset({1, 2}): "ok", frozenset(): "empty"}
assert d[frozenset([2, 1])] == "ok"
assert d[frozenset()] == "empty"
nested = {frozenset({1}), frozenset({2})}
assert frozenset({1}) in nested and frozenset({3}) not in nested

# Unhashable element in frozenset() raises TypeError
bad_elem = False
try:
    frozenset([[1, 2]])
except TypeError:
    bad_elem = True
assert bad_elem

# Repr & generic alias
assert repr(frozenset()) == "frozenset()"
assert repr(frozenset({1})) == "frozenset({1})"
assert repr(frozenset({"a"})) == "frozenset({'a'})"
ga = frozenset[int]
assert ga.__origin__ is frozenset
assert ga.__args__ == (int,)

# Membership constant folding with nested unhashable tuple element does not crash
def check_unhashable_tuple(x):
    return x in ([1], 2)
assert check_unhashable_tuple([1]) is True
assert check_unhashable_tuple(2) is True
assert check_unhashable_tuple(3) is False
`)
}

func TestFrozenSetFycRoundTrip(t *testing.T) {
	src := `
def check_const_membership(x):
    return x in {1, 2, "three"}

result = (check_const_membership(1), check_const_membership("three"), check_const_membership(99))
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	hash := vm.ComputeSourceHash([]byte(src))
	raw, err := vm.SerializeCodeObject(co, uint64(len(src)), 987654321, hash)
	if err != nil {
		t.Fatalf("SerializeCodeObject failed: %v", err)
	}
	co2, hdr, err := vm.DeserializeCodeObject(raw)
	if err != nil {
		t.Fatalf("DeserializeCodeObject failed: %v", err)
	}
	if !vm.ValidateFyc(hdr, uint64(len(src)), 987654321, hash) {
		t.Fatal("ValidateFyc failed")
	}

	foundFrozenSet := false
	for _, c := range co2.Consts {
		if c.Kind() != val.KindCode {
			continue
		}
		sub := vm.AsCodeObject(c)
		for _, sc := range sub.Consts {
			if sc.Kind() == val.KindSet && sc.Type() == val.FrozenSetType {
				foundFrozenSet = true
			}
		}
	}
	if !foundFrozenSet {
		t.Fatal("expected FrozenSetType constant to survive .fyc round-trip via TagFrozenSet")
	}

	eng := vm.NewEngine()
	v := eng.NewVM()
	defer eng.ReleaseVM(v)
	v.RunCode(co2)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unexpected exception: %v", exc)
	}
	res, ok := v.GetGlobal("result")
	if !ok || !res.IsTuple() {
		t.Fatalf("missing result tuple: %v", res)
	}
	items := res.AsTuple().Items
	if len(items) != 3 || !items[0].AsBool() || !items[1].AsBool() || items[2].AsBool() {
		t.Fatalf("unexpected result: %v", res)
	}
}
