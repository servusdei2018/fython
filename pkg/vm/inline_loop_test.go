package vm_test

import (
	"testing"
)

// TestInlineFastAttrPropertyAndDeopt verifies that @property getters accessed
// via fused OP_LOAD_FAST_ATTR splice and execute in-loop, and deopt cleanly on
// both instance shape transitions and class property reassignment.
func TestInlineFastAttrPropertyAndDeopt(t *testing.T) {
	src := `
class Box:
    def __init__(self, v):
        self._v = v

    @property
    def val(self):
        return self._v + 1

def read_many(b, n):
    total = 0
    for _ in range(n):
        total += b.val
    return total

b1 = Box(10)
assert read_many(b1, 400) == 4400

# Trigger instance shape change (add new attribute) while class version is unchanged.
b2 = Box(20)
b2.extra = 99
assert read_many(b2, 50) == 1050

# Trigger class version invalidation by reassigning the @property on Box.
Box.val = property(lambda self: self._v * 2)
assert read_many(b1, 50) == 1000
assert read_many(b2, 50) == 2000
`
	runPython(t, src)
}

// TestInlineWidenedCustomLenPreservesLocals verifies that an inlined callee
// calling len(customObj) (which pushes a real Python __len__ frame) preserves
// st.base on return so subsequent reads of the inlined callee's locals and
// OP_INLINE_RETURN succeed cleanly.
func TestInlineWidenedCustomLenPreservesLocals(t *testing.T) {
	src := `
class CustomSeq:
    def __init__(self, n):
        self.n = n
    def __len__(self):
        return self.n

class Helper:
    def __init__(self, bias):
        self.bias = bias
    def score(self, seq, extra):
        tmp = extra + self.bias
        k = len(seq)
        is_c = isinstance(seq, CustomSeq)
        return tmp + k + (1 if is_c else 0)

def driver(h, seq, n):
    acc = 0
    for i in range(n):
        acc += h.score(seq, i)
    return acc

h = Helper(10)
s = CustomSeq(5)
# Warm up so driver becomes Tier-2 and h.score splices into driver.
got = driver(h, s, 400)
want = sum((i + 10) + 5 + 1 for i in range(400))
assert got == want, f"got {got}, want {want}"
`
	runPython(t, src)
}

// TestInlineWidenedExceptionsAndTracebacks verifies accurate tracebacks and
// outer exception handling when an inlined callee raises directly (missing
// attribute) or indirectly via a nested Python __len__ call.
func TestInlineWidenedExceptionsAndTracebacks(t *testing.T) {
	src := `
import traceback

class BoomLen:
    def __len__(self):
        raise ValueError("boom-len")

class Holder:
    def __init__(self, x):
        self.x = x
    def read_attr(self, obj):
        return obj.missing_attr
    def read_len(self, obj):
        return len(obj)

def call_attr_loop(h, good, bad, n):
    local_tag = "outer-attr"
    last = 0
    for i in range(n):
        target = good if i < n - 1 else bad
        last = h.read_attr(target)
    return local_tag, last

class Good:
    def __init__(self):
        self.missing_attr = 7

h = Holder(1)
g = Good()
caught_attr = False
try:
    call_attr_loop(h, g, object(), 350)
except AttributeError as e:
    caught_attr = True
    frames = traceback.extract_tb(e.__traceback__)
    names = [f.name for f in frames]
    assert "call_attr_loop" in names, f"missing caller in {names}"
    assert "read_attr" in names, f"missing inlined callee in {names}"
    assert names.index("call_attr_loop") < names.index("read_attr"), f"wrong order: {names}"
assert caught_attr

def call_len_loop(h, good_seq, bad_seq, n):
    marker = 12345
    total = 0
    for i in range(n):
        seq = good_seq if i < n - 1 else bad_seq
        total += h.read_len(seq)
    return marker + total

caught_len = False
try:
    call_len_loop(h, [1, 2], BoomLen(), 350)
except ValueError as e:
    caught_len = True
    assert str(e) == "boom-len"
    frames = traceback.extract_tb(e.__traceback__)
    names = [f.name for f in frames]
    assert "call_len_loop" in names, f"missing caller in {names}"
    assert "read_len" in names, f"missing inlined callee in {names}"
    assert "__len__" in names, f"missing __len__ in {names}"
    assert names.index("call_len_loop") < names.index("read_len") < names.index("__len__"), f"wrong order: {names}"
assert caught_len
`
	runPython(t, src)
}

// TestInlineDirectFuncRetry verifies that a direct function call f(x) in a hot
// loop works accurately across warm-up, inlining retry, and rebinding deopt.
func TestInlineDirectFuncRetry(t *testing.T) {
	src := `
def add1(x):
    y = x + 1
    return y

def loop_direct(n):
    x = 0
    for _ in range(n):
        x = add1(x)
    return x

assert loop_direct(600) == 600

# Rebind global add1 to verify guard miss deopt works cleanly.
def add1(x):
    return x + 2

assert loop_direct(100) == 200
`
	runPython(t, src)
}
