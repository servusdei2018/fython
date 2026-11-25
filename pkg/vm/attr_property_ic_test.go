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

func TestTypeAttrAndStoreAttrFastPaths(t *testing.T) {
	eng, v := runPySource(t, `
import sys

# 1. __slots__ + string attribute stores + >6-slot shape transitions
class SlottedBox:
    __slots__ = ("a", "b", "c")
    def __init__(self, a, b, c):
        self.a = a
        self.b = b
        self.c = c

for i in range(10):
    sb = SlottedBox("s_" + str(i), i, "tail")
    assert sb.a == "s_" + str(i) and sb.b == i and sb.c == "tail"

class WideBox:
    def __init__(self, n):
        self.f0 = "a"
        self.f1 = "b"
        self.f2 = n
        self.f3 = n + 1
        self.f4 = n + 2
        self.f5 = n + 3
        self.f6 = "wide_str"
        self.f7 = n + 4

for i in range(10):
    wb = WideBox(i)
    assert wb.f0 == "a" and wb.f6 == "wide_str" and wb.f7 == i + 4

# 2. type.__doc__, @classmethod via LOAD_ATTR, reflection attrs, and invalidation
class MetaDoc:
    """initial doc"""
    @classmethod
    def make(cls, x):
        return (cls.__name__, x)

class NoDoc:
    pass

for _ in range(10):
    assert MetaDoc.__doc__ == "initial doc"
    assert NoDoc.__doc__ is None
    fn = MetaDoc.make
    assert fn(42) == ("MetaDoc", 42)
    assert MetaDoc.__name__ == "MetaDoc"
    assert MetaDoc in MetaDoc.__mro__

MetaDoc.__doc__ = "updated doc"
assert MetaDoc.__doc__ == "updated doc"

# 3. del inst.attr falling back to class attr under hasattr/getattr PIC
class Shadowed:
    val = "from_class"
    def __init__(self):
        self.val = "from_inst"

sh = Shadowed()
for i in range(5):
    assert hasattr(sh, "val")
    assert getattr(sh, "val") == "from_inst"
del sh.val
assert hasattr(sh, "val")
assert getattr(sh, "val") == "from_class"

# 4. Polymorphic hasattr / getattr + AttributeError binding & sys.exc_info safety
class Markup(str):
    def __html__(self):
        return self

samples = ["plain", Markup("<b>ok</b>"), 123, {"k": 1}]
html_flags = []
for _ in range(5):
    for item in samples:
        html_flags.append(hasattr(item, "__html__"))
assert html_flags == [False, True, False, False] * 5

caught_list = []
exc_info_list = []
d = {"score": 10}
for i in range(6):
    try:
        getattr(d, "missing_" + str(i % 2))
    except AttributeError as e:
        caught_list.append(e)
    try:
        getattr(d, "missing_" + str(i % 2))
    except AttributeError:
        t_exc, v_exc, _ = sys.exc_info()
        exc_info_list.append(v_exc)
assert len(caught_list) == 6
assert all("dict" in str(e) for e in caught_list)
assert all("dict" in str(e) for e in exc_info_list)
`)
	defer eng.ReleaseVM(v)
}

func TestEarlyReturnIterAndZeroCopyTupleSlice(t *testing.T) {
	eng, v := runPySource(t, `
def find_in_set(s, target):
    for x in s:
        if x == target:
            return True
    return False

def find_in_list(xs, target):
    for x in xs:
        if x == target:
            return x
    return -1

fs = {"a", "b", "c", "d"}
for _ in range(50):
    assert find_in_set(fs, "b")
    assert not find_in_set(fs, "z")
    assert find_in_list([10, 20, 30], 20) == 20
    assert find_in_list((1, 2, 3), 2) == 2

# Verify NewTupleView clears FlagEphemeralTuple before sharing backing slice
def check_slice_from_unpacked():
    kept = []
    for _ in range(10):
        a, b = (10, 20)
        t = (a, b, 30, 40)
        sub = t[1:4]
        kept.append(sub)
        sub2 = (a, b)[:]
        kept.append(sub2)
    return kept

res = check_slice_from_unpacked()
assert res[0] == (20, 30, 40)
assert res[1] == (10, 20)
assert res[-2] == (20, 30, 40)
assert res[-1] == (10, 20)
`)
	defer eng.ReleaseVM(v)
}

func TestFusedLoadFastAttrProperty(t *testing.T) {
	eng, v := runPySource(t, `
class Holder:
    def __init__(self, v):
        self._v = v
    @property
    def val(self):
        return self._v * 2

def read_prop(h):
    total = 0
    for _ in range(20):
        x = h.val
        total += x
    return total

h = Holder(5)
for _ in range(10):
    assert read_prop(h) == 200

Holder.val = property(lambda self: self._v * 3)
for _ in range(5):
    assert read_prop(h) == 300

Holder.val = 7
for _ in range(5):
    assert read_prop(h) == 140
`)
	defer eng.ReleaseVM(v)

	fnVal, ok := v.Globals().Get(val.InternString("read_prop"))
	if !ok || !fnVal.IsFunction() {
		t.Fatal("missing read_prop")
	}
	co := vm.AsCodeObject(fnVal.AsFunction().Code)
	hasFused := false
	for _, inst := range co.Active() {
		if vm.DecodeOpcode(inst) == vm.OP_LOAD_FAST_ATTR {
			hasFused = true
			break
		}
	}
	if !hasFused {
		t.Fatal("expected OP_LOAD_FAST_ATTR to remain fused for @property site")
	}
}

func TestEntryIPDocstringSkipPython(t *testing.T) {
	eng, v := runPySource(t, `
import asyncio

def with_doc(x):
    """Docstring prologue."""
    return x + 1

def doc_only():
    """Only a docstring."""

def gen_doc(n):
    """Generator docstring."""
    for i in range(n):
        yield i * 2

async def coro_doc(x):
    """Coroutine docstring."""
    return x * 3

assert with_doc.__doc__ == "Docstring prologue."
assert doc_only.__doc__ == "Only a docstring."
assert [with_doc(i) for i in range(12)] == list(range(1, 13))
assert [doc_only() for _ in range(12)] == [None] * 12
assert list(gen_doc(4)) == [0, 2, 4, 6]
assert asyncio.run(coro_doc(7)) == 21
`)
	defer eng.ReleaseVM(v)

	for _, name := range []string{"with_doc", "doc_only", "gen_doc", "coro_doc"} {
		fnVal, ok := v.Globals().Get(val.InternString(name))
		if !ok || !fnVal.IsFunction() {
			t.Fatalf("missing function %s", name)
		}
		co := vm.AsCodeObject(fnVal.AsFunction().Code)
		if co.EntryIP != 2 {
			t.Fatalf("%s EntryIP = %d, want 2", name, co.EntryIP)
		}
	}
}

func TestUnpackSequenceInLoopAndFusedStoreFast(t *testing.T) {
	eng, v := runPySource(t, `
g_a = 0
g_b = 0

def unpack_pairs_and_triples(pairs, triples):
    total = 0
    for a, b in pairs:
        total += a + b
    for x, y, z in triples:
        total += x * y + z
    p, q = 10, 20
    p, q = q, p
    return total + p - q

pairs = [(i, i + 1) for i in range(15)]
triples = [(i, i + 1, i + 2) for i in range(15)]
list_pairs = [[i, i + 2] for i in range(5)]
list_triples = [[i, i + 1, i + 3] for i in range(5)]

res1 = unpack_pairs_and_triples(pairs, triples)
res2 = unpack_pairs_and_triples(list_pairs, list_triples)
assert res1 == sum(2 * i + 1 for i in range(15)) + sum(i * (i + 1) + i + 2 for i in range(15)) + 10
assert res2 == sum(2 * i + 2 for i in range(5)) + sum(i * (i + 1) + i + 3 for i in range(5)) + 10

for i in range(12):
    g_a, g_b = (i, i + 3)
assert (g_a, g_b) == (11, 14)

err_count = 0
for seq in [(1,), (1, 2, 3)]:
    try:
        u1, u2 = seq
    except ValueError:
        err_count += 1
assert err_count == 2

def unpack_with_view(buf):
    mv = memoryview(buf)
    items = [(mv[0:2], 5) for _ in range(6)]
    s = 0
    for sub, tag in items:
        s += sub[0] + tag
    return s

assert unpack_with_view(bytearray(b"abcd")) == 6 * (ord("a") + 5)
`)
	defer eng.ReleaseVM(v)

	fnVal, ok := v.Globals().Get(val.InternString("unpack_pairs_and_triples"))
	if !ok || !fnVal.IsFunction() {
		t.Fatal("missing unpack_pairs_and_triples")
	}
	co := vm.AsCodeObject(fnVal.AsFunction().Code)
	has2, has3 := false, false
	for _, inst := range co.Active() {
		switch vm.DecodeOpcode(inst) {
		case vm.OP_UNPACK_2_FAST:
			has2 = true
		case vm.OP_UNPACK_3_FAST:
			has3 = true
		}
	}
	if !has2 || has3 {
		t.Fatalf("expected OP_UNPACK_2_FAST in fused stream and cold OP_UNPACK_3_FAST unfused, got has2=%v has3=%v", has2, has3)
	}
}

func TestStage5CoroutineClosureQuickeningAndDefaultsGuard(t *testing.T) {
	eng, v := runPySource(t, `
import asyncio

async def yield_once():
    return 1

def make_coro(delta):
    async def inner(x):
        y = await yield_once()
        return x + delta + y
    return inner

def make_coro_with_default(delta):
    async def inner(x=10):
        return x + delta
    return inner

async def call_site(fn, arg):
    return await fn(arg)

async def main():
    total = 0
    for i in range(12):
        total += await call_site(make_coro(i), 2)
    # Polymorphic call site: switch to a function with defaults and mutate __defaults__.
    f = make_coro_with_default(5)
    assert (await f()) == 15
    f.__defaults__ = (20,)
    assert (await f()) == 25
    # Verify non-direct-await coroutine closure site too.
    c = make_coro(3)(4)
    assert (await c) == 8
    return total

res = asyncio.run(main())
assert res == sum(2 + i + 1 for i in range(12)), res
`)
	defer eng.ReleaseVM(v)
}

func TestStage5MethodPICStaticClassMethodAndShadowing(t *testing.T) {
	eng, v := runPySource(t, `
class Base:
    @staticmethod
    def sm(x):
        return x + 1

    @classmethod
    def cm(cls, x):
        return (cls.__name__, x)

class Sub(Base):
    pass

def call_sm(obj, x):
    return obj.sm(x)

def call_cm(obj, x):
    return obj.cm(x)

b = Base()
s = Sub()
for _ in range(10):
    assert call_sm(b, 5) == 6
    assert call_cm(b, 5) == ("Base", 5)
    assert call_cm(s, 5) == ("Sub", 5)

# Instance shadowing must invalidate cached staticmethod/classmethod.
b.sm = lambda x: x + 100
b.cm = lambda x: ("shadow", x)
assert call_sm(b, 5) == 105
assert call_cm(b, 5) == ("shadow", 5)
# Unshadowed instance still sees class methods.
b2 = Base()
assert call_sm(b2, 5) == 6
assert call_cm(b2, 5) == ("Base", 5)

# Polymorphic instance-slot callables across distinct shapes.
class V1:
    def __init__(self, f):
        self.a = 1
        self.run = f
class V2:
    def __init__(self, f):
        self.b = 2
        self.c = 3
        self.run = f

def invoke(obj, x):
    return obj.run(x)

v1 = V1(lambda x: x * 2)
v2 = V2(lambda x: x * 3)
for _ in range(10):
    assert invoke(v1, 4) == 8
    assert invoke(v2, 4) == 12
`)
	defer eng.ReleaseVM(v)
}

func TestStage5StoreAttrTransitionInvalidation(t *testing.T) {
	eng, v := runPySource(t, `
class Box:
    def __init__(self, a, b):
        self.a = a
        self.b = b

for i in range(20):
    obj = Box(i, i + 1)
    assert obj.a == i and obj.b == i + 1

# Adding a data descriptor on the class must invalidate the in-loop transition IC.
hits = []
def set_a(self, val):
    hits.append(val)
    self._a = val
Box.a = property(lambda self: self._a, set_a)

obj2 = Box(99, 100)
assert hits == [99]
assert obj2.a == 99 and obj2.b == 100
`)
	defer eng.ReleaseVM(v)
}
