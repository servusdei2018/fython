package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestWave6CompositeTupleDictAndSetKeys(t *testing.T) {
	runSnippet(t, `
# Numeric cross-type equality inside tuple keys
d = {(1, "a"): "hit", (0, "b"): "zero", (42, "long_string_key_over_7_bytes"): "long"}
assert d[(True, "a")] == "hit"
assert d[(1.0, "a")] == "hit"
assert d[(False, "b")] == "zero"
assert d[(0.0, "b")] == "zero"
assert d[(42, "long_string_key_over_7_bytes")] == "long"
assert (1, "a") in d
assert (True, "a") in d
assert (2, "a") not in d
assert (1, "c") not in d

# NaN identity inside tuple keys (matches CPython PyObject_RichCompareBool)
nan = float("nan")
d_nan = {(nan, 1): 99}
assert d_nan[(nan, 1)] == 99
assert (nan, 1) in d_nan

# Custom __eq__ and __hash__ element inside tuple key
class Box:
    def __init__(self, v):
        self.v = v
    def __hash__(self):
        return hash(self.v)
    def __eq__(self, other):
        return isinstance(other, Box) and self.v == other.v

d_box = {(1, Box(7)): "boxed"}
assert d_box[(1, Box(7))] == "boxed"
assert d_box[(True, Box(7))] == "boxed"
assert (1, Box(8)) not in d_box

# Set and frozenset composite tuple membership
s = {(i, i + 1, "tag") for i in range(50)}
assert (10, 11, "tag") in s
assert (0, 1, "tag") in s
assert (False, True, "tag") in s
assert (10, 12, "tag") not in s

fs = frozenset(s)
assert (10, 11, "tag") in fs
assert (False, True, "tag") in fs
assert len(fs) == 50
`)
}

func TestWave6DictAndSetBulkCopyUpdate(t *testing.T) {
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

func TestWave6DictSetCopyConcurrentRace(t *testing.T) {
	runSnippet(t, `
import threading

shared_dict = {f"k{i}": i for i in range(16)}
shared_set = set(shared_dict)
errors = []

def worker(tid):
    try:
        for _ in range(30):
            c = shared_dict.copy()
            c.update({"k0": 99 + tid, "new": tid})
            assert c["k0"] == 99 + tid and shared_dict["k0"] == 0
            s2 = shared_set.copy()
            s2.add(f"extra_{tid}")
            assert f"extra_{tid}" not in shared_set
    except Exception as e:
        errors.append(e)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not errors, f"concurrent errors: {errors}"
`)
}

func TestWave6DunderCallFromHookAndTruthy(t *testing.T) {
	runSnippet(t, `
from dataclasses import dataclass

class Flag:
    def __init__(self, v):
        self.v = v
    def __bool__(self):
        return bool(self.v)

class Sized:
    def __init__(self, n):
        self.n = n
    def __len__(self):
        return self.n

class Boom:
    def __bool__(self):
        raise ValueError("bool boom")

class DeepBool:
    def __init__(self, depth):
        self.depth = depth
    def __bool__(self):
        def rec(k):
            if k <= 0:
                return True
            return rec(k - 1)
        return rec(self.depth)

assert bool(Flag(1)) is True
assert bool(Flag(0)) is False
assert (True if Flag(1) else False) is True
assert (True if Flag(0) else False) is False
assert (not Flag(0)) is True
assert (not Flag(1)) is False
assert (Flag(1) and 42) == 42
assert (Flag(0) or 99) == 99

assert bool(Sized(3)) is True
assert bool(Sized(0)) is False
assert (True if Sized(0) else False) is False

# Exception propagation from dunders via bool() / __add__ / __lt__
raised = False
try:
    bool(Boom())
except ValueError as e:
    raised = "bool boom" in str(e)
assert raised

# Stack growth inside __bool__
assert (True if DeepBool(40) else False) is True

# __add__, __hash__, __eq__, __lt__
class Vec:
    def __init__(self, x):
        self.x = x
    def __add__(self, other):
        return Vec(self.x + other.x)
    def __hash__(self):
        return hash(self.x)
    def __eq__(self, other):
        return isinstance(other, Vec) and self.x == other.x
    def __lt__(self, other):
        return self.x < other.x

assert (Vec(2) + Vec(3)).x == 5
assert {Vec(10): "ten"}[Vec(10)] == "ten"
assert Vec(1) < Vec(2)

# @dataclass(order=True) comparisons and sorting
@dataclass(order=True)
class SemVer:
    major: int
    minor: int
    patch: int
    pre: str = ""

items = [SemVer(2, 0, 0), SemVer(1, 10, 0), SemVer(1, 2, 3, "rc1"), SemVer(1, 2, 3, "")]
s = sorted(items)
assert s == [SemVer(1, 2, 3, ""), SemVer(1, 2, 3, "rc1"), SemVer(1, 10, 0), SemVer(2, 0, 0)]
`)
}

func TestWave6LikelySlotCapMultiAttrAndRace(t *testing.T) {
	runSnippet(t, `
class Wide:
    def __init__(self, base):
        self.a0 = base + 0
        self.a1 = base + 1
        self.a2 = base + 2
        self.a3 = base + 3
        self.a4 = base + 4
        self.a5 = base + 5
        self.a6 = base + 6
        self.a7 = base + 7
        self.a8 = base + 8
        self.a9 = base + 9
        self.a10 = base + 10
        self.a11 = base + 11

objs = [Wide(i * 100) for i in range(25)]
for i, o in enumerate(objs):
    assert o.a0 == i * 100
    assert o.a6 == i * 100 + 6
    assert o.a11 == i * 100 + 11

# delattr and vars(obj) on pre-allocated Slots
delattr(objs[5], "a7")
assert not hasattr(objs[5], "a7")
v = vars(objs[5])
assert "a7" not in v
assert v["a0"] == 500 and v["a11"] == 511
objs[5].extra = "dyn"
assert objs[5].extra == "dyn"
`)

	runSnippet(t, `
import threading

class SharedWide:
    def __init__(self, n):
        self.x0 = n; self.x1 = n+1; self.x2 = n+2; self.x3 = n+3
        self.x4 = n+4; self.x5 = n+5; self.x6 = n+6; self.x7 = n+7
        self.x8 = n+8; self.x9 = n+9

errors = []
def worker(tid):
    try:
        for i in range(60):
            o = SharedWide(tid * 100 + i)
            assert o.x9 == tid * 100 + i + 9
    except Exception as e:
        errors.append(e)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not errors, f"concurrent errors: {errors}"
`)
}

func TestWave6FormatValueSplitAndUnicodeRuneCache(t *testing.T) {
	runSnippet(t, `
# FormatValueCtx int64 & bool formatting
assert f"{0}" == "0"
assert f"{7}" == "7"
assert f"{-1}" == "-1"
assert f"{123456}" == "123456"
assert f"{-999999}" == "-999999"
assert f"{9223372036854775807}" == "9223372036854775807"
assert f"{-9223372036854775808}" == "-9223372036854775808"
assert f"{True}" == "True"
assert f"{False}" == "False"
assert f"{42}:{True}:{False}:{-7}" == "42:True:False:-7"

# str.split() ASCII & Unicode whitespace, maxsplit, single/multi-char sep, empty sep error
assert "".split() == []
assert "   \t\n\r  ".split() == []
assert "  a   b\tc\nd  ".split() == ["a", "b", "c", "d"]
assert "  a   b   c  ".split(None, 0) == ["a   b   c  "]
assert "  a   b   c  ".split(None, 1) == ["a", "b   c  "]
assert "a\u00a0b\u2003c\u3000d".split() == ["a", "b", "c", "d"]
assert "a\u00a0b\u2003c".split(None, 1) == ["a", "b\u2003c"]

assert "".split(",") == [""]
assert "a,b,c,d".split(",") == ["a", "b", "c", "d"]
assert "a,b,c,d".split(",", 0) == ["a,b,c,d"]
assert "a,b,c,d".split(",", 2) == ["a", "b", "c,d"]
assert "one--two--three".split("--") == ["one", "two", "three"]
assert "one--two--three".split("--", 1) == ["one", "two--three"]

raised = False
try:
    "abc".split("")
except ValueError as e:
    raised = "empty separator" in str(e)
assert raised

# Non-ASCII str indexing and slicing
u = "café 日本語 αβγ"
assert u[0] == "c"
assert u[3] == "é"
assert u[5] == "日"
assert u[-1] == "γ"
assert u[0:4] == "café"
assert u[5:8] == "日本語"
assert u[-3:] == "αβγ"
assert u[3:8:2] == "é日語"
assert u[::-1][0] == "γ"
a, b, c = "αβγ"
assert (a, b, c) == ("α", "β", "γ")
`)

	// Direct verification that PyString.RuneCache is populated and reused.
	sv := val.NewStringVal("café 日本語 αβγ")
	ps := sv.AsStringObj()
	if ps.RuneCache.Load() != nil {
		t.Fatalf("expected RuneCache to start nil")
	}
	r1 := val.StringValRunes(sv)
	if len(r1) != 12 {
		t.Fatalf("expected 12 runes, got %d", len(r1))
	}
	p1 := ps.RuneCache.Load()
	if p1 == nil {
		t.Fatalf("expected RuneCache to be populated after StringValRunes")
	}
	r2 := val.StringValRunes(sv)
	if len(r2) != 12 || &r1[0] != &r2[0] {
		t.Fatalf("expected StringValRunes to reuse cached backing slice")
	}
}
