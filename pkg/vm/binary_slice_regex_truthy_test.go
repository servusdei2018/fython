package vm_test

import (
	"testing"
)

func TestBinarySliceAndRegexAndColdTruthy(t *testing.T) {
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
