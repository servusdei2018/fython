package vm_test

import "testing"

func TestLRUCacheInvariants(t *testing.T) {
	script := `
import functools

# 1. Recursive miss + cache_clear() re-entrancy
calls = []
@functools.lru_cache(maxsize=4)
def rec_clear(n, do_clear=False):
    calls.append((n, do_clear))
    if do_clear:
        rec_clear.cache_clear()
        return 999
    if n <= 0:
        return 0
    if n == 2:
        rec_clear(1, True)
    return n + rec_clear(n - 1)

assert rec_clear(2) == 3
assert rec_clear(3) == 6
assert rec_clear(3) == 6

# 2. Same-key recursive self-population before return
seen = set()
@functools.lru_cache(maxsize=2)
def reenter_same(x):
    if x not in seen:
        seen.add(x)
        inner = reenter_same(x)
        return inner + 1
    return 41

assert reenter_same(7) == 42
assert reenter_same(7) == 41
assert reenter_same.cache_info().currsize == 1

# 3. typed=True distinguishes 1 vs 1.0 vs True; eviction at maxsize=1
tcalls = []
@functools.lru_cache(maxsize=1, typed=True)
def typed_id(v):
    tcalls.append((type(v).__name__, v))
    return (type(v).__name__, v)

assert typed_id(1) == ("int", 1)
assert typed_id(1.0) == ("float", 1.0)
assert typed_id(True) == ("bool", True)
assert len(tcalls) == 3
assert typed_id(True) == ("bool", True)
assert len(tcalls) == 3
assert typed_id.cache_info().currsize == 1
`
	runSnippet(t, script)
}

func TestGeneratorShortCircuitAndReducers(t *testing.T) {
	script := `
from itertools import islice, chain, repeat

# 1. Non-ephemeral generator resume position after any/all/islice early stop
def gen_seq():
    yield 0
    yield 10
    yield 20
    yield 30

g1 = gen_seq()
assert any(g1) is True
assert next(g1) == 20
assert next(g1) == 30

g2 = gen_seq()
assert all(g2) is False
assert next(g2) == 10

def inner_gen():
    yield 0
    yield 5
    yield 6
    yield 7

def outer_gen():
    yield from inner_gen()
    yield 8

g3 = outer_gen()
assert any(g3) is True
assert list(g3) == [6, 7, 8]

# 2. Bounded islice on named generator preserves exact resume point without over-consuming
g4 = gen_seq()
assert list(islice(g4, 0, 3, 2)) == [0, 20]
assert list(g4) == [30]
assert list(islice(g4, 2)) == []

g5 = gen_seq()
it = islice(g5, 2)
assert list(it) == [0, 10]
assert list(it) == []
assert next(g5) == 20

# 3. sum, min, max, dict reducers over generators and islice(gen, ...)
assert sum(x * 2 for x in range(5)) == 20
assert sum((x for x in range(4)), 10) == 16
assert sum(islice((x for x in range(10)), 1, 6, 2)) == 1 + 3 + 5
assert min(x for x in [5, 2, 9, 3]) == 2
assert max(x for x in [5, 2, 9, 3]) == 9
assert dict((k, k * k) for k in range(3)) == {0: 0, 1: 1, 2: 4}

for empty_fn in (min, max):
    raised = False
    try:
        empty_fn(x for x in ())
    except ValueError:
        raised = True
    assert raised

# 4. chain, chain.from_iterable, repeat
assert list(chain([1, 2], (x for x in [3, 4]), range(5, 7))) == [1, 2, 3, 4, 5, 6]
assert list(chain.from_iterable([[1], (x for x in [2, 3])])) == [1, 2, 3]
assert list(repeat("x", 3)) == ["x", "x", "x"]
assert list(repeat("x", -2)) == []
assert list(islice(repeat("inf"), 4)) == ["inf", "inf", "inf", "inf"]
`
	runSnippet(t, script)
}

func TestMatchFreelistSubSafety(t *testing.T) {
	script := `
import re

captured = []
def repl_escape(m):
    captured.append(m)
    return m.group(0).upper()

out = re.sub(r"[a-z]+", repl_escape, "ab cd ef")
assert out == "AB CD EF"
assert [m.group(0) for m in captured] == ["ab", "cd", "ef"]

def repl_transient(m):
    return "[" + m.group(1) + "]"

for _ in range(50):
    assert re.sub(r"(\w+)", repl_transient, "one two three") == "[one] [two] [three]"
`
	runSnippet(t, script)
}

func TestBoundNativeSortTuplePoolAndGenTierInvariants(t *testing.T) {
	script := `
import io
import operator
import re

# 1. list.sort Schwartzian single-call key evaluation and mutation-during-sort guard
calls = 0
def count_key(x):
    global calls
    calls += 1
    return -x

xs = [3, 1, 4, 1, 5, 9]
xs.sort(key=count_key)
assert xs == [9, 5, 4, 3, 1, 1]
assert calls == 6

rows = [(2, "b", 10), (1, "a", 20), (1, "a", 5), (2, "a", 1)]
rows.sort(key=operator.itemgetter(0, 2, 1))
assert rows == [(1, "a", 5), (1, "a", 20), (2, "a", 1), (2, "b", 10)]

bad = [1, 2, 3]
def mut_key(x):
    bad.append(x)
    return x

mut_err = False
try:
    bad.sort(key=mut_key)
except ValueError as exc:
    mut_err = "modified during sort" in str(exc)
assert mut_err

# 2. Quickened OP_CALL_BOUND_NATIVE fast path, polymorphism fallback, and error unwinding
pat = re.compile(r"([a-z]+)")
rexmatch = pat.match
seen = []
for s in ("abc", "123", "xyz"):
    m = rexmatch(s)
    seen.append(m.group(1) if m is not None else None)
assert seen == ["abc", None, "xyz"]

def py_callable(s, pos=0):
    return s[pos:]

def call_bound(fn, s):
    return fn(s, 1)

assert call_bound(rexmatch, "zabc") is not None
assert call_bound(py_callable, "zabc") == "abc"
assert call_bound(rexmatch, "zxyz") is not None

idx_fn = [10, 20, 30].index
def call_bound1(fn, arg):
    return fn(arg)

assert call_bound1(idx_fn, 20) == 1
assert call_bound1(idx_fn, 30) == 2

raised = False
try:
    call_bound1(idx_fn, 999)
except ValueError:
    raised = True
assert raised

# 3. 3- and 4-element ephemeral tuple pooling + ClearInline + unpack preservation
kept = []
for i in range(40):
    a, b, c = (i, i + 1, i + 2)
    w, x, y, z = (a, b, c, c + 1)
    assert (w, x, y, z) == (i, i + 1, i + 2, i + 3)
    if i % 10 == 0:
        t3 = (i, "tag", str(i))
        kept.append(t3)
assert len(kept) == 4 and kept[2] == (20, "tag", "20")

# 4. Tiny generator stack 64-slot tier + deep call growth beyond 64 slots
def deep_leaf(n):
    a1 = n + 1; a2 = n + 2; a3 = n + 3; a4 = n + 4
    a5 = n + 5; a6 = n + 6; a7 = n + 7; a8 = n + 8
    return a1 + a2 + a3 + a4 + a5 + a6 + a7 + a8

def gen_tiny():
    for i in range(5):
        yield deep_leaf(i)

assert sum(gen_tiny()) == sum(deep_leaf(i) for i in range(5))

# 5. StringIO pool reuse across with/close cycles and str subclass slot storage
class SubStr(str):
    pass

s1 = SubStr("hello")
assert str(s1) == "hello" and s1.upper() == "HELLO"
for _ in range(5):
    with io.StringIO() as sio:
        sio.write(s1)
        sio.write("!")
        sio.seek(1)
        sio.write("E")
        assert sio.getvalue() == "hEllo!"
`
	runSnippet(t, script)
}

func TestPyDictLinearScanSubscrContainsStringIOAndCleanup(t *testing.T) {
	script := `
import io

# 1. Small inline PyDict linear scan parity (updates, deletes, 1 == True, var proxy d.Live)
d = {"a": 1, "b": 2, 1: "one"}
assert d["a"] == 1 and d[True] == "one"
d["b"] = 20
del d["a"]
assert "a" not in d and d["b"] == 20
d.update({"c": 3, "d": 4, "e": 5, "f": 6})
assert d["f"] == 6 and len(d) == 6

class Box:
    def __init__(self, x):
        self.x = x

bx = Box(42)
assert bx.__dict__["x"] == 42
bx.__dict__["y"] = 99
assert bx.y == 99

# 2. In-loop tuple[int], str[int], and 'in' / 'not in' (str, small tuple/list, 1 == True/1.0 fallback)
t = (10, 20, 30)
assert t[0] == 10 and t[2] == 30
s = "abc"
assert s[1] == "b"
big = "abcdefghijk"
assert "def" in big and "zzz" not in big
assert "b" in ("a", "b", "c")
assert 2 in [1, 2, 3]
assert 1 in [True]
assert 1 in [1.0]
assert 9 not in (1, 2, 3)

# 3. Builtin str subclass (Markup-style) + str mixed method PIC calls
class MarkupStr(str):
    pass

def call_replace(val):
    return val.replace("a", "b")

results = []
for v in ("cat", MarkupStr("hat"), "mat", MarkupStr("bat")):
    results.append(call_replace(v))
assert results == ["cbt", "hbt", "mbt", "bbt"]

# 4. StringIO double-close & GC AddCleanup safety (unclosed large StringIOs)
sio = io.StringIO()
sio.write("hello")
sio.close()
sio.close()

for _ in range(3):
    tmp = io.StringIO()
    chunk = "x" * 65536
    for _ in range(4):
        tmp.write(chunk)
    assert len(tmp.getvalue()) == 262144
`
	runSnippet(t, script)
}
