package vm_test

import (
	"testing"
)

func TestWave3CallExPosOnlyAndForwarding(t *testing.T) {
	eng, v := runPySource(t, `
def fixed(a, b, c):
    return a * 100 + b * 10 + c

def varargs_only(*args):
    return (len(args), args)

def prefix_varargs(first, *rest):
    return (first, rest)

def forward_both(*args, **kwargs):
    # Mutating kwargs must NOT alias caller's dict.
    kwargs["mutated"] = 999
    return (args, kwargs)

def gen_callee(*args):
    for x in args:
        yield x * 2

t3 = (1, 2, 3)
assert fixed(*t3) == 123
assert varargs_only(*t3) == (3, (1, 2, 3))
assert prefix_varargs(*t3) == (1, (2, 3))
assert prefix_varargs(*(7,)) == (7, ())

caller_kw = {"k": 10}
a_out, kw_out = forward_both(*t3, **caller_kw)
assert a_out == (1, 2, 3)
assert kw_out == {"k": 10, "mutated": 999}
assert caller_kw == {"k": 10}, f"caller dict was mutated: {caller_kw}"

# Ephemeral dict literal forwarded into *args, **kwargs
a_out2, kw_out2 = forward_both(*(4, 5), **{"x": 1, "y": 2})
assert a_out2 == (4, 5)
assert kw_out2 == {"x": 1, "y": 2, "mutated": 999}

# Generator callee via CALL_EX must still return a generator
g = gen_callee(*t3)
assert list(g) == [2, 4, 6]
`)
	defer eng.ReleaseVM(v)
}

func TestWave3KindNativePatternMatchDispatch(t *testing.T) {
	eng, v := runPySource(t, `
import re

pat = re.compile(r"([a-z]+)=([0-9]+)")

def test_bool_only(p, items):
    hits = 0
    for s in items:
        if p.match(s):
            hits += 1
    return hits

def test_ephemeral(p, items):
    out = []
    for s in items:
        m = p.match(s)
        if m:
            out.append(m.group(1))
    return out

def test_escaping(p, s):
    return p.match(s)

samples = ["foo=123", "bad", "bar=456", "baz=789"] * 5
assert test_bool_only(pat, samples) == 15
assert test_ephemeral(pat, samples[:4]) == ["foo", "bar", "baz"]
m = test_escaping(pat, "hello=42")
assert m is not None and m.group(1) == "hello" and m.group(2) == "42"
assert test_escaping(pat, "nope") is None
`)
	defer eng.ReleaseVM(v)
}

func TestWave3IntStrEdgeCases(t *testing.T) {
	eng, v := runPySource(t, `
assert int("0") == 0
assert int("12345") == 12345
assert int("  \t+42\n ") == 42
assert int(" -99\r\f\v") == -99
assert int("1_000_000") == 1000000
assert int("-1_2_3") == -123
assert int("9223372036854775807") == (1 << 63) - 1
assert int("-9223372036854775808") == -(1 << 63)
assert int("9223372036854775808") == 1 << 63
assert int("-9223372036854775809") == -(1 << 63) - 1
assert int("999999999999999999999999999999") == 10**30 - 1

for bad in ["", "   ", "+", "-", "_1", "1_", "1__2", "+_1", "-_1", "12a", "1.5"]:
    raised = False
    try:
        int(bad)
    except ValueError:
        raised = True
    assert raised, f"expected ValueError for {bad!r}"

class MyInt(int):
    pass

mi = MyInt("12345")
assert type(mi) is MyInt
assert mi == 12345
`)
	defer eng.ReleaseVM(v)
}

func TestWave3CallFromHookReentryAndExceptions(t *testing.T) {
	eng, v := runPySource(t, `
class Box:
    def __init__(self, v, nested=None):
        self.v = v
        self.nested = nested

    def __lt__(self, other):
        """Compare boxes with docstring (EntryIP == 2)."""
        if self.v == -999 or other.v == -999:
            raise RuntimeError("boom in __lt__")
        if self.nested is not None and other.nested is not None:
            return self.nested < other.nested
        return self.v < other.v

# 1. Re-entrant list.sort with Python key + recursive __lt__
items = [Box(i, Box(100 - i)) for i in range(25)]
items.sort()
assert [b.v for b in items] == list(range(24, -1, -1))

def sort_key(b):
    """Key function with docstring."""
    inner = [b.v, b.v - 1, b.v + 1]
    inner.sort(key=lambda x: -x)
    return inner[0]

items.sort(key=sort_key)
assert [b.v for b in items] == list(range(25))

# 2. Nested exception propagation through callFromHook
bad_items = [Box(1), Box(-999), Box(2)]
caught = None
try:
    bad_items.sort()
except RuntimeError as e:
    caught = str(e)
assert caught == "boom in __lt__", f"unexpected exception: {caught!r}"
`)
	defer eng.ReleaseVM(v)
}
