package vm_test

import (
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestCallTypeInitInLoopAndInvalidation(t *testing.T) {
	eng, v := runPySource(t, `
# 1. Zero-arg type with default object.__init__ (OP_CALL_TYPE_INIT inline alloc)
class Empty:
    pass

items = [Empty() for _ in range(15)]
assert len(items) == 15
assert all(isinstance(x, Empty) for x in items)

# Mutate Empty to add __init__; PyType.Version() must invalidate the inline cache.
def make_empty():
    return Empty()

for _ in range(10):
    e = make_empty()
    assert not hasattr(e, "tag")

def custom_init(self):
    self.tag = 42

Empty.__init__ = custom_init
e2 = make_empty()
assert e2.tag == 42

# 2. Type with Python __init__ pushed via pushFrameFast
class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y

total = 0
for i in range(20):
    p = Point(i, i + 1)
    total += p.x + p.y
assert total == sum(i + (i + 1) for i in range(20))

# Non-None return from __init__ must raise TypeError
class BadInit:
    def __init__(self, x):
        return x

def call_bad(x):
    return BadInit(x)

got_bad_err = False
for _ in range(5):
    try:
        call_bad(1)
    except TypeError:
        got_bad_err = True
assert got_bad_err

# 3. User-defined Python __new__ without custom __init__ (!tp.HasInit())
class TaggedStr(str):
    def __new__(cls, text, prefix="pre:"):
        return super().__new__(cls, prefix + text)

vals = [TaggedStr("ok") for _ in range(15)]
assert vals[0] == "pre:ok" and isinstance(vals[0], TaggedStr)
vals2 = [TaggedStr("x", "p-") for _ in range(15)]
assert vals2[0] == "p-x"

# Adding __init__ to TaggedStr later must invalidate SlotIndex == 1 cache
seen = []
def ts_init(self, text, prefix="pre:"):
    seen.append(str(self))

def make_ts(s):
    return TaggedStr(s)

for _ in range(5):
    make_ts("a")
TaggedStr.__init__ = ts_init
make_ts("b")
assert seen == ["pre:b"]

# 4. Polymorphic site switching between Python function and class
def fn_target(x):
    return x + 10

class Box:
    def __init__(self, x):
        self.x = x

def poly_call(c, x):
    return c(x)

for _ in range(5):
    assert poly_call(fn_target, 3) == 13
for _ in range(5):
    b = poly_call(Box, 7)
    assert isinstance(b, Box) and b.x == 7
assert poly_call(fn_target, 4) == 14
`)
	defer eng.ReleaseVM(v)
	_ = val.NoneVal()
}

func TestCallMethodDescrFastAndFallback(t *testing.T) {
	eng, v := runPySource(t, `
# Monomorphic native method descriptors: list.append, dict.get, str.join, str.startswith, set.add
acc = []
d = {"k": 10}
s = set()
hit_sum = 0
for i in range(25):
    acc.append(i)
    hit_sum += d.get("k", 0) + d.get("missing", 1)
    s.add(i % 5)
    assert ("item_" + str(i)).startswith("item_")
joined = ",".join([str(x) for x in acc[:4]])
assert joined == "0,1,2,3"
assert len(acc) == 25 and hit_sum == 25 * 11 and len(s) == 5

# Polymorphic fallback at a quickened OP_CALL_METHOD_DESCR_FAST site
class CustomAppender:
    def __init__(self):
        self.buf = []
    def append(self, v):
        self.buf.append(v * 2)

def push_val(container, val):
    container.append(val)

lst = []
for i in range(10):
    push_val(lst, i)
ca = CustomAppender()
for i in range(10):
    push_val(ca, i)
assert lst == list(range(10))
assert ca.buf == [i * 2 for i in range(10)]

# OP_LOAD_ATTR caching on non-instance builtin receivers
append_fn = lst.append
for i in range(10):
    append_fn(100 + i)
assert len(lst) == 20 and lst[-1] == 109

get_fn = d.get
assert sum(get_fn("k", 0) for _ in range(10)) == 100
`)
	defer eng.ReleaseVM(v)
}

func TestCallMethodDescrFastTracebackLine(t *testing.T) {
	eng, v := runPySource(t, `
import sys, traceback

seen_lines = []
def failing_gen():
    seen_lines.append(sys._getframe(1).f_lineno)
    yield "a"
    raise ValueError("boom")

def join_caller():
    return "".join(failing_gen())

# Warm up OP_CALL_METHOD_DESCR_FAST on str.join before raising inside callback
def join_ok():
    return "".join(["x", "y"])

for _ in range(10):
    assert join_ok() == "xy"

tb_text = ""
for _ in range(5):
    try:
        join_caller()
    except ValueError as exc:
        tb_text = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__))
assert seen_lines == [11] * 5, seen_lines
assert "failing_gen" in tb_text and "boom" in tb_text
`)
	defer eng.ReleaseVM(v)
	_ = strings.Contains
}

func TestCallMethodDescrFastExceptionAndReentrancy(t *testing.T) {
	eng, v := runPySource(t, `
def pop_from(xs):
    return xs.pop()

xs = list(range(15))
popped = [pop_from(xs) for _ in range(15)]
assert popped == list(range(14, -1, -1))

caught_empty = False
try:
    pop_from(xs)
except IndexError:
    caught_empty = True
assert caught_empty

def split_inline(s):
    return s.split(":")

for _ in range(20):
    assert split_inline("a:b") == ["a", "b"]

def key_grow(x):
    acc = 0
    for i in range(10):
        acc += i
    return -x

def sort_desc(items):
    items.sort(key=key_grow)
    return items

for _ in range(15):
    assert sort_desc([1, 3, 2, 4]) == [4, 3, 2, 1]
`)
	defer eng.ReleaseVM(v)
}
