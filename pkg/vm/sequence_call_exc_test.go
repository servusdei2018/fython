package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

// resetSingleThreaded restores single-threaded mode: earlier thread tests
// set the process-global multi-threaded flag permanently, and call-boundary
// error paths legitimately diverge under it (Go-panic vs raised TypeError).
func resetSingleThreaded() {
	val.SetMultiThreaded(false)
}

// Multi-level *args/**kwargs forwarding, caller/callee isolation, and error
// parity for the call-boundary fast paths.
func TestSequenceCallForwarding(t *testing.T) {
	resetSingleThreaded()
	runPySource(t, `
def _f0(*a, **k):
    return sum(a) + sum(k.values())
def _f1(*a, **k):
    return _f0(*a, **k)
def _f2(*a, **k):
    return _f1(*a, **k)
def _f3(*a, **k):
    return _f2(*a, **k)
assert _f3(1, 2, 3, x=4, y=5) == 15
assert _f3() == 0

# mutating the callee kwargs must never touch the caller dict
def inner(**kw):
    kw["z"] = 99
    return kw["a"]
def outer(**kw):
    r = inner(**kw)
    return r, kw
assert outer(a=1) == (1, {"a": 1})

# repeated forwarding from one frame sees pristine kwargs each time
def m0(*a, **k):
    k["seen"] = k.get("seen", 0) + 1
    return sum(a), k["seen"]
def m1(*a, **k):
    r1 = m0(*a, **k)
    r2 = m0(*a, **k)
    return r1, r2, k
assert m1(1, 2) == ((3, 1), (3, 1), {})

# duplicate keyword and unexpected keyword errors
def dup(a, b):
    return a + b
try:
    dup(1, **{"a": 2})
    assert False
except TypeError:
    pass
try:
    dup(1, 2, c=3)
    assert False
except TypeError:
    pass

# non-tuple *args iterables
def sl(*a):
    return a
assert sl(*[1, 2, 3]) == (1, 2, 3)
assert sl(*range(4)) == (0, 1, 2, 3)
assert sl(*"ab") == ("a", "b")

# fixed params mixed with star forms
def fx(a, b, *r):
    return (a, b, r)
assert fx(*[1, 2, 3, 4]) == (1, 2, (3, 4))
assert fx(1, *[2, 3]) == (1, 2, (3,))
def fy(a, *r, **k):
    return (a, r, k)
assert fy(*[1, 2], **{"x": 3}) == (1, (2,), {"x": 3})

# bound methods and __init__ forwarding
class C:
    def __init__(self, *a, **k):
        self.a = a
        self.k = k
    def m(self, *a, **k):
        return (self.a, a, k)
c = C(1, 2, x=3)
assert c.a == (1, 2) and c.k == {"x": 3}
assert c.m(4, y=5) == ((1, 2), (4,), {"y": 5})

# dataclass instantiation and equality
from dataclasses import dataclass
@dataclass
class Point:
    x: int
    y: int
assert Point(1, 2) == Point(1, 2)
assert not (Point(1, 2) == Point(1, 3))
assert (Point(1, 2) != Point(1, 3))

# kwargs-only, defaults, kw-only
def kk(a, b=10, *, c=20):
    return a + b + c
assert kk(1, **{"b": 2, "c": 3}) == 6
assert kk(**{"a": 1}) == 31
`)
}

// list.sort(key=...) across key shapes, closures, reverse, stability, and
// mid-sort exceptions, plus functools parity.
func TestSequenceSortKeys(t *testing.T) {
	resetSingleThreaded()
	runPySource(t, `
base = [(i % 100, i) for i in range(500)]
extra = [(i % 100, 1000 + i) for i in range(100)]

l = list(base)
l.extend(extra)
l.sort(key=lambda x: x[0])
assert l[0][1] == 0 and l[-1][0] == 99 and len(l) == 600

def kf(x):
    return x[0]
l2 = list(base)
l2.sort(key=kf)
assert [a for a, _ in l2[:3]] == [0, 0, 0]

l3 = list(base)
l3.sort(key=lambda x: x[0], reverse=True)
assert l3[0][0] == 99

# closure capture through CellPtrs and direct capture
off = 10
l4 = [(1,), (2,), (3,)]
l4.sort(key=lambda x: x[0] + off)
assert [a[0] for a in l4] == [1, 2, 3]
l5 = [(3,), (1,), (2,)]
l5.sort(key=lambda x: x[0] + off)
assert [a[0] for a in l5] == [1, 2, 3]

# builtin and bound-method keys
words = ["bb", "a", "ccc", "dd"]
words.sort(key=len)
assert words == ["a", "bb", "dd", "ccc"]
ws = ["Xa", "xb", "Ya"]
ws.sort(key=str.lower)
assert [w.lower() for w in ws] == ["xa", "xb", "ya"]

# stability on equal keys
st = [(1, "b"), (1, "a"), (0, "c")]
st.sort(key=lambda x: x[0])
assert st == [(0, "c"), (1, "b"), (1, "a")]

# exception mid-key leaves the list in a valid state
class Boom(Exception):
    pass
def bad(x):
    if x == 2:
        raise Boom("k")
    return x
l6 = [3, 2, 1]
try:
    l6.sort(key=bad)
    assert False
except Boom:
    pass
assert sorted(l6) == [1, 2, 3]

# custom callable objects and recursive sorts inside key
class K:
    def __call__(self, x):
        return -x
l7 = [1, 3, 2]
l7.sort(key=K())
assert l7 == [3, 2, 1]

calls = []
def rkey(x):
    if x == 99:
        m = [3, 1, 2]
        m.sort()
        calls.append(m)
    return x
lr = [3, 99, 1]
lr.sort(key=rkey)
assert lr == [1, 3, 99] and calls == [[1, 2, 3]]

# functools.reduce parity, including closures and error propagation
from functools import reduce, partial
assert reduce(lambda a, b: a + b, [1, 2, 3, 4]) == 10
assert reduce(lambda a, b: a * b, [1, 2, 3, 4], 10) == 240
assert reduce(lambda a, b: a + b, [], 5) == 5
def add2(a, b):
    return a + b
assert reduce(add2, range(100)) == sum(range(100))
c = 100
assert reduce(lambda a, b: a + b + c, [1, 2], 0) == 203
p = partial(lambda a, b, c: a + b + c, 1, 2)
assert p(3) == 6
def rbad(a, b):
    if b == 3:
        raise Boom("r")
    return a + b
try:
    reduce(rbad, [1, 2, 3, 4])
    assert False
except Boom:
    pass
`)
}

// Lazy-frame traceback fidelity, EAFP unwinding, StopIteration values, and
// contextmanager paths, including deep chains and Tier-2 loop handlers.
func TestSequenceTracebackExc(t *testing.T) {
	resetSingleThreaded()
	runPySource(t, `
import traceback
import sys

def leaf():
    raise ValueError("deep")
def mid2():
    return leaf()
def mid1():
    return mid2()
def top():
    return mid1()

try:
    top()
except ValueError as e:
    tb = e.__traceback__
    names = []
    n = 0
    while tb is not None:
        names.append(tb.tb_frame.f_code.co_name)
        assert isinstance(tb.tb_lineno, int) and tb.tb_lineno > 0
        assert isinstance(tb.tb_lasti, int)
        assert tb.tb_frame is tb.tb_frame
        n += 1
        tb = tb.tb_next
    assert names == ["<module>", "top", "mid1", "mid2", "leaf"], names
    assert n == 5
    tb = e.__traceback__
    assert tb.tb_frame.f_back is None
    assert tb.tb_frame.f_lineno == tb.tb_lineno
    assert tb.tb_frame.f_code.co_name == "<module>"
    assert "f_globals" not in dir(tb.tb_frame) or True
    assert isinstance(tb.tb_frame.f_globals, dict)
    s = traceback.format_exc()
    assert "ValueError: deep" in s and "in top" in s and "in leaf" in s

try:
    leaf()
except ValueError:
    t, v, tb = sys.exc_info()
    assert t is ValueError
    assert tb.tb_frame is tb.tb_frame

def rec(d):
    if d == 0:
        raise RuntimeError("bottom")
    return rec(d - 1) + 1
try:
    rec(25)
except RuntimeError as e:
    depth = 0
    tb = e.__traceback__
    while tb is not None:
        depth += 1
        _ = tb.tb_frame.f_code.co_name
        tb = tb.tb_next
    assert depth >= 26
    assert "RuntimeError: bottom" in traceback.format_exc()

def gen():
    yield 1
    return "done"
g = gen()
assert next(g) == 1
try:
    next(g)
    assert False
except StopIteration as e:
    assert e.value == "done"
    assert e.args == ("done",)

from contextlib import contextmanager
entered = []
@contextmanager
def cm(v):
    entered.append(v)
    try:
        yield v * 2
    finally:
        entered.append("exit")
with cm(21) as v:
    assert v == 42
assert entered == [21, "exit"]
try:
    with cm(1):
        raise KeyError("k")
except KeyError:
    pass
assert entered[-1] == "exit"

# promoted-loop EAFP shape
data = list(range(200))
acc = 0
for v in data:
    try:
        if v % 2:
            raise ValueError(v)
        acc += v
    except ValueError as e:
        acc -= 1
assert acc == sum(x for x in data if x % 2 == 0) - 100
`)
}

// Concurrent tb_frame reads over one shared traceback must stay race-clean
// with lazily cached frames: threads only read (CompareAndSwap cache), never
// unwind, following the per-slot results pattern.
func TestSequenceTracebackConcurrent(t *testing.T) {
	resetSingleThreaded()
	defer resetSingleThreaded()
	runPySource(t, `
import threading

def raiser(n):
    if n == 0:
        raise ValueError("go")
    return raiser(n - 1) + 1

try:
    raiser(6)
except ValueError as e:
    shared_tb = e.__traceback__

depths = [0, 0, 0, 0]
probs = []
def worker(idx, rep):
    try:
        dmax = 0
        for _ in range(rep):
            tb = shared_tb
            depth = 0
            while tb is not None:
                assert tb.tb_frame is tb.tb_frame
                assert tb.tb_frame.f_code.co_name == "raiser" or depth == 0
                assert tb.tb_lineno > 0 and tb.tb_lasti >= 0
                depth += 1
                tb = tb.tb_next
            dmax = depth
        depths[idx] = dmax
    except Exception as exc:
        probs.append(exc)

threads = [threading.Thread(target=worker, args=(i, 50)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not probs, probs
assert depths == [8, 8, 8, 8], depths
`)
}
