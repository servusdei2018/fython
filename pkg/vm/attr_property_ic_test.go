package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestInlinePropertyDispatch(t *testing.T) {
	eng, v := runPySource(t, `
from collections import namedtuple

class Box:
    def __init__(self, x):
        self._x = x
    @property
    def x(self):
        return self._x + 1

b = Box(10)
total = 0
for _ in range(600):
    total += b.x

# Exception raised inside getter
class Boom:
    @property
    def val(self):
        raise ValueError("bad prop")

bm = Boom()
caught = []
for i in range(10):
    try:
        _ = bm.val
    except ValueError as e:
        caught.append(str(e))

# Class mutation invalidating property IC
class Mut:
    def __init__(self, v):
        self._v = v
    @property
    def p(self):
        return self._v

m = Mut(5)
before_mut = [m.p for _ in range(5)]
Mut.p = property(lambda self: self._v * 10)
after_mut1 = [m.p for _ in range(5)]
Mut.p = 999
after_mut2 = [m.p for _ in range(5)]

# Closure, default arg, and builtin getter fallbacks
def make_closure_cls(scale):
    class Scaled:
        def __init__(self, n):
            self.n = n
        @property
        def scaled(self):
            return self.n * scale
    return Scaled

ScaledCls = make_closure_cls(7)
sc = ScaledCls(6)
closure_vals = [sc.scaled for _ in range(5)]

class WithDefault:
    def __init__(self, n):
        self.n = n
    def _get(self, bonus=3):
        return self.n + bonus
    val = property(_get)

wd = WithDefault(4)
default_vals = [wd.val for _ in range(5)]

class BuiltinGetter:
    def __len__(self):
        return 42
    size = property(len)

bg = BuiltinGetter()
builtin_vals = [bg.size for _ in range(5)]

# NamedTuple field and @property
class Point(namedtuple("Point", ["x", "y"])):
    @property
    def norm2(self):
        return self.x * self.x + self.y * self.y

pt = Point(3, 4)
nt_sum = 0
for _ in range(300):
    nt_sum += pt.x + pt.y + pt.norm2
`)
	defer eng.ReleaseVM(v)

	checkInt := func(name string, want int64) {
		t.Helper()
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsInt64() || got.AsInt64() != want {
			t.Fatalf("%s = %v (ok=%v), want %d", name, got, ok, want)
		}
	}
	checkInt("total", 600*11)
	checkInt("nt_sum", 300*(3+4+25))

	caught, _ := v.Globals().Get(val.InternString("caught"))
	if caught.AsList().Len() != 10 {
		t.Fatalf("caught len = %d, want 10", caught.AsList().Len())
	}

	checkListFirstLast := func(name string, want int64) {
		t.Helper()
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsList() {
			t.Fatalf("%s missing or not list: %v", name, got)
		}
		items := got.AsList().Items
		for i, it := range items {
			if !it.IsInt64() || it.AsInt64() != want {
				t.Fatalf("%s[%d] = %v, want %d", name, i, it, want)
			}
		}
	}
	checkListFirstLast("before_mut", 5)
	checkListFirstLast("after_mut1", 50)
	checkListFirstLast("after_mut2", 999)
	checkListFirstLast("closure_vals", 42)
	checkListFirstLast("default_vals", 7)
	checkListFirstLast("builtin_vals", 42)
}

func TestBoundMethodAndAttrICFastPaths(t *testing.T) {
	eng, v := runPySource(t, `
class Base:
    K = 100
    def greet(self):
        return "hi"
Base.greet.tag = "tagged"

b = Base()
bm = b.greet

# Repeated bound method non-dunder attr, class attr, and type attr loads
tags = []
k_sum = 0
for _ in range(20):
    tags.append(bm.tag)
    k_sum += b.K + Base.K

# Dunder attrs on bound method still work
bm_self_ok = (bm.__self__ is b)
bm_func_ok = (bm.__func__ is Base.greet)

# Mutation and deletion of function attr reflected immediately on bound method
Base.greet.tag = "updated"
tag_after = bm.tag
del Base.greet.tag
has_after_del = hasattr(bm, "tag")
try:
    _ = bm.tag
    missing_raised = False
except AttributeError:
    missing_raised = True
`)
	defer eng.ReleaseVM(v)

	if got, _ := v.Globals().Get(val.InternString("k_sum")); !got.IsInt64() || got.AsInt64() != 20*200 {
		t.Fatalf("k_sum = %v, want 4000", got)
	}
	if got, _ := v.Globals().Get(val.InternString("bm_self_ok")); !got.IsBool() || !got.AsBool() {
		t.Fatalf("bm_self_ok = %v, want True", got)
	}
	if got, _ := v.Globals().Get(val.InternString("bm_func_ok")); !got.IsBool() || !got.AsBool() {
		t.Fatalf("bm_func_ok = %v, want True", got)
	}
	if got, _ := v.Globals().Get(val.InternString("tag_after")); !got.StringEquals("updated") {
		t.Fatalf("tag_after = %v, want updated", got)
	}
	if got, _ := v.Globals().Get(val.InternString("has_after_del")); !got.IsBool() || got.AsBool() {
		t.Fatalf("has_after_del = %v, want False", got)
	}
	if got, _ := v.Globals().Get(val.InternString("missing_raised")); !got.IsBool() || !got.AsBool() {
		t.Fatalf("missing_raised = %v, want True", got)
	}
}

func TestGetAttrAndHasAttrFastPaths(t *testing.T) {
	eng, v := runPySource(t, `
class Markup(str):
    def __html__(self):
        return self

class Slotted:
    __slots__ = ("a", "b")
    def __init__(self):
        self.a = 7

class PropErr:
    @property
    def missing(self):
        raise AttributeError("nope")
    @property
    def broken(self):
        raise ValueError("boom")

class DynGetAttr:
    def __getattr__(self, name):
        if name == "magic":
            return 123
        raise AttributeError(name)

class CustomGetAttribute:
    def __getattribute__(self, name):
        if name == "secret":
            return "shh"
        return object.__getattribute__(self, name)

m = Markup("<b>hi</b>")
s = Slotted()
pe = PropErr()
dg = DynGetAttr()
cg = CustomGetAttribute()
d = {"score": 10}

# hasattr checks
assert not hasattr("plain", "__html__")
assert hasattr(m, "__html__")
assert not hasattr(42, "__html__")
assert not hasattr(d, "score")
assert hasattr(d, "get")
assert hasattr(s, "a")
assert not hasattr(s, "b")
assert not hasattr(pe, "missing")
assert hasattr(dg, "magic")
assert not hasattr(dg, "other")
assert hasattr(cg, "secret")
assert not hasattr(cg, "nope")

# hasattr must NOT swallow non-AttributeError from @property
prop_val_err = False
try:
    hasattr(pe, "broken")
except ValueError:
    prop_val_err = True
assert prop_val_err

# getattr 2-arg, 3-arg default, missing raising AttributeError
assert getattr(s, "a") == 7
assert getattr(s, "b", "def") == "def"
assert getattr(d, "score", -1) == -1
assert getattr(dg, "magic") == 123
assert getattr(cg, "secret") == "shh"

dict_err = False
try:
    getattr(d, "score")
except AttributeError:
    dict_err = True
assert dict_err

# Dynamic mutation invalidation
s.b = 99
assert hasattr(s, "b")
assert getattr(s, "b") == 99
del s.b
assert not hasattr(s, "b")

ok = True
`)
	defer eng.ReleaseVM(v)

	if got, ok := v.Globals().Get(val.InternString("ok")); !ok || !got.IsBool() || !got.AsBool() {
		t.Fatalf("ok = %v (found=%v), want True", got, ok)
	}
}

func TestInlinePropertyStatsCounter(t *testing.T) {
	if !vm.ICStatsEnabled() {
		t.Skip("run with -tags icstats to verify inline property hit counter")
	}
	vm.ResetICStats()
	eng, v := runPySource(t, `
class C:
    def __init__(self, x):
        self._x = x
    @property
    def x(self):
        return self._x

c = C(3)
s = 0
for _ in range(50):
    s += c.x
`)
	defer eng.ReleaseVM(v)
	if vm.LoadAttrInlinePropHitCount() == 0 {
		t.Fatalf("expected non-zero LoadAttrInlinePropHitCount")
	}
}
