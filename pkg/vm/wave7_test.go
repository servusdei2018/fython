package vm_test

import (
	"testing"
)

func TestWave7SuperNewAndPrimitiveSubclasses(t *testing.T) {
	_, _ = runPySource(t, `
class BaseStr(str):
    def __new__(cls, v):
        return super().__new__(cls, v)

class SubStrA(BaseStr):
    def __new__(cls, v, tag="a"):
        inst = super().__new__(cls, v)
        inst.tag = tag
        return inst

class SubStrB(BaseStr):
    def __new__(cls, v):
        inst = super().__new__(cls, v)
        inst.extra = len(v)
        return inst

class SlottedStr(str):
    __slots__ = ("meta",)
    def __new__(cls, v, meta):
        inst = super().__new__(cls, v)
        inst.meta = meta
        return inst

class MyInt(int):
    def __new__(cls, v):
        return super().__new__(cls, v)

class MyTuple(tuple):
    def __new__(cls, iterable):
        return super().__new__(cls, iterable)

class SubTuple(MyTuple):
    def __new__(cls, iterable, name="t"):
        inst = super().__new__(cls, iterable)
        inst.name = name
        return inst

class MyBytes(bytes):
    def __new__(cls, v):
        return super().__new__(cls, v)

class MyObj:
    def __new__(cls, x):
        inst = super().__new__(cls)
        inst.x = x
        return inst

# Polymorphic super().__new__ calls across multiple subclasses
for i in range(50):
    a = SubStrA("hello", "t1")
    b = SubStrB(a)
    c = BaseStr(123)  # non-string coercion fallback
    assert isinstance(a, SubStrA) and a == "hello" and a.tag == "t1"
    assert isinstance(b, SubStrB) and b == "hello" and b.extra == 5
    assert isinstance(c, BaseStr) and c == "123"

ss = SlottedStr("abc", 42)
assert ss == "abc" and ss.meta == 42

ni = MyInt(7)
nb = MyInt(True)
ns = MyInt("99")
assert isinstance(ni, MyInt) and ni == 7
assert isinstance(nb, MyInt) and nb == 1
assert isinstance(ns, MyInt) and ns == 99

t1 = MyTuple((1, 2, 3))
t2 = MyTuple((1, 2, 3, 4, 5))
t3 = SubTuple((10, 20), "pair")
assert isinstance(t1, MyTuple) and t1 == (1, 2, 3)
assert isinstance(t2, MyTuple) and t2 == (1, 2, 3, 4, 5)
assert isinstance(t3, SubTuple) and t3 == (10, 20) and t3.name == "pair"

mb = MyBytes(b"xyz")
assert isinstance(mb, MyBytes) and mb == b"xyz"

mo = MyObj(9)
assert isinstance(mo, MyObj) and mo.x == 9

# Unrelated class must still raise TypeError
class Unrelated:
    pass

for fn, args in [
    (str.__new__, (Unrelated, "x")),
    (int.__new__, (Unrelated, 1)),
    (tuple.__new__, (Unrelated, (1, 2))),
    (bytes.__new__, (Unrelated, b"a")),
]:
    raised = False
    try:
        fn(*args)
    except TypeError:
        raised = True
    assert raised, f"expected TypeError for {fn}"
`)
}

func TestWave7FastGeneratorDrainingAndDeque(t *testing.T) {
	_, _ = runPySource(t, `
from collections import deque

def gen_range(n):
    for i in range(n):
        yield i

def gen_yield_from(n):
    yield from gen_range(n // 2)
    yield from range(n // 2, n)

# deque(gen), deque(gen, maxlen=3), deque.extend(gen), self-extend
d1 = deque(gen_range(20))
assert list(d1) == list(range(20))

d2 = deque(gen_range(20), maxlen=3)
assert list(d2) == [17, 18, 19]

d3 = deque([1, 2], maxlen=4)
d3.extend(gen_yield_from(6))
assert list(d3) == [2, 3, 4, 5]

d4 = deque([1, 2, 3])
d4.extend(d4)
assert list(d4) == [1, 2, 3, 1, 2, 3]

# list/tuple/set draining
assert list(gen_yield_from(10)) == list(range(10))
assert tuple(gen_yield_from(10)) == tuple(range(10))
assert set(gen_range(10)) == set(range(10))

# Mid-drain exception and try/finally execution
cleaned = []
def failing_gen():
    try:
        yield 1
        yield 2
        raise RuntimeError("boom")
    finally:
        cleaned.append("done")

d_err = deque()
caught = False
try:
    d_err.extend(failing_gen())
except RuntimeError as e:
    caught = (str(e) == "boom")
assert caught
assert list(d_err) == [1, 2]
assert cleaned == ["done"]

# Re-entrant generator must raise ValueError
reentrant_err = False
def self_drain():
    global reentrant_err
    try:
        list(g_self)
    except ValueError:
        reentrant_err = True
    yield 42

g_self = self_drain()
assert list(g_self) == [42]
assert reentrant_err
`)
}

func TestWave7BinarySliceAndRegexAndColdTruthy(t *testing.T) {
	_, _ = runPySource(t, `
import re

# Exact full-slice identity & subclass non-identity
t = (1, 2, 3, 4)
assert t[:] is t
assert t[0:4] is t
assert t[2:2] == ()
assert t[-2:] == (3, 4)
assert t[::2] == (1, 3)

s = "hello_world"
assert s[:] is s
s_short = "abc"
assert s_short[:] is s_short
s_uni = "héllo_wörld"
assert s_uni[:] is s_uni
assert s_uni[1:5] == "éllo"

b = b"abcdef"
assert b[:] is b
assert b[1:4] == b"bcd"

class MyTuple(tuple):
    pass

mt = MyTuple((1, 2, 3))
mt_sl = mt[:]
assert mt_sl is not mt
assert type(mt_sl) is tuple
assert mt_sl == (1, 2, 3)

# Ephemeral tuple slice escape safety
escaped = []
for i in range(20):
    base = (i, i + 1, i + 2, i + 3)
    if base[1:3] < (i + 2, i + 3):
        escaped.append(base[1:3])
assert len(escaped) == 20
assert escaped[0] == (1, 2) and escaped[-1] == (20, 21)

# Regex match recycling with OP_STORE_LOAD_FAST and is None / is not None
pat = re.compile(r"([a-z0-9]+)=([0-9]+)")
pairs = []
for i in range(50):
    text = f"k{i}={i}" if i % 2 == 0 else "no_match"
    m = pat.match(text)
    if m:
        pairs.append((m.group(1), m.group(2)))
    m2 = pat.search(text)
    if m2 is not None:
        assert m2.group(1) == f"k{i}"
    if pat.match(text) is None:
        assert i % 2 == 1
assert len(pairs) == 25

# Cold IsTruthyCtx with custom __bool__ / __len__ and exception propagation
class BoolObj:
    def __init__(self, flag, fail=False):
        self.flag = flag
        self.fail = fail
    def __bool__(self):
        if self.fail:
            raise ValueError("bool failed")
        return self.flag

class LenObj:
    def __init__(self, n):
        self.n = n
    def __len__(self):
        return self.n

assert bool(BoolObj(True)) is True
assert bool(BoolObj(False)) is False
assert (not BoolObj(False)) is True
assert (not BoolObj(True)) is False
assert (BoolObj(False) or "fallback") == "fallback"
assert (BoolObj(True) and "ok") == "ok"
assert (not LenObj(0)) is True
assert (not LenObj(3)) is False

raised_bool = False
try:
    _ = not BoolObj(True, fail=True)
except ValueError as e:
    raised_bool = (str(e) == "bool failed")
assert raised_bool
`)
}

func TestWave7DictSetCopyAndPrimitiveTupleHash(t *testing.T) {
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
