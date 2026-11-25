package vm_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestWave5EphemeralTupleCompareAndMembership(t *testing.T) {
	runPySource(t, `
from dataclasses import dataclass

# 1. 2-, 3-, 4-, and 6-element tuple comparisons (including @dataclass(order=True))
@dataclass(order=True)
class Rec4:
    a: int
    b: str
    c: float
    d: str

recs = [Rec4(i % 7, f"g{i % 3}", float(i % 11), f"t{i % 5}") for i in range(60)]
recs.sort()
for i in range(len(recs) - 1):
    assert recs[i] <= recs[i + 1]

def cmp_pairs(n):
    lt_cnt = 0
    eq_cnt = 0
    for i in range(n):
        if (i, i + 1) < (i, i + 2):
            lt_cnt += 1
        if (i, i + 1, i + 2) == (i, i + 1, i + 2):
            eq_cnt += 1
        if (i, i + 1, i + 2, i + 3, i + 4, i + 5) < (i, i + 1, i + 2, i + 3, i + 4, i + 6):
            lt_cnt += 1
    return lt_cnt, eq_cnt

assert cmp_pairs(40) == (80, 40)

# 2. Custom __eq__ / __lt__ capturing operands (nested tuples & membership) must NOT alias
captured_lt = []
captured_eq = []

class Captor:
    def __init__(self, tag):
        self.tag = tag
    def __lt__(self, other):
        captured_lt.append(other)
        return False
    def __eq__(self, other):
        captured_eq.append(other)
        return False
    def __hash__(self):
        return hash(self.tag)

c = Captor("c")
for i in range(25):
    # Nested tuple inside tuple compare: inner (i, i+1) must NOT be ephemeral when passed to Captor.__lt__
    _ = (c, i) < ((i, i + 1), i + 2)
    # Membership with custom __eq__ receiving LHS tuple (i, i+1)
    _ = (i, i + 1) in (c, (999, 999))
    _ = (i, i + 1) not in [c]
    _ = (i, i + 1) in {c}

# Verify every captured tuple kept its distinct original values (no aliasing / recycling corruption)
assert len(captured_lt) == 25
for idx in range(25):
    assert captured_lt[idx] == (idx, idx + 1), f"lt mismatch at {idx}: {captured_lt[idx]}"
# captured_eq gets 4 entries per iteration (tuple cmp eq check + tuple-in + list-in + set-in)
assert len(captured_eq) == 100
for idx in range(25):
    for j in range(4):
        assert captured_eq[idx * 4 + j] == (idx, idx + 1), f"eq mismatch at {idx},{j}: {captured_eq[idx * 4 + j]}"

# 3. Identity comparisons (CMP_IS / CMP_IS_NOT) must work cleanly
for i in range(20):
    assert ((i, i + 1) is (i, i + 1)) is False
    assert ((i, i + 1) is not (i, i + 1)) is True

# 4. Mid-expression exception between LHS and RHS tuple builds
class Holder:
    def __init__(self, ok):
        if ok:
            self.x = 10

def cmp_may_fail(a, b):
    return (a.x, 1, 2, 3) < (b.x, 1, 2, 4)

good = Holder(True)
bad = Holder(False)
for i in range(20):
    assert cmp_may_fail(good, good) is True
    try:
        cmp_may_fail(good, bad)
        assert False, "expected AttributeError"
    except AttributeError:
        pass

# 5. Custom __getitem__ capturing tuple keys (OP_BINARY_SUBSCR) must never alias
subscr_keys = []
class KeyBox:
    def __getitem__(self, key):
        subscr_keys.append(key)
        return key[0] + key[1]

kb = KeyBox()
for i in range(25):
    assert kb[(i, i + 10)] == 2 * i + 10
for i in range(25):
    assert subscr_keys[i] == (i, i + 10)
`)
}

func TestWave5PolymorphicMethodPICAndBuiltinsAndPartial(t *testing.T) {
	runPySource(t, `
import functools
import math
import operator
import re

# 1. Polymorphic 4-way instance method PIC + invalidation
class A:
    def f(self, x): return x + 1
class B:
    def f(self, x): return x + 2
class C:
    def f(self, x): return x + 3
class D:
    def f(self, x): return x + 4
class E:
    def f(self, x): return x + 5

objs = [A(), B(), C(), D()]
def call_poly(items, n):
    total = 0
    for i in range(n):
        total += items[i % len(items)].f(i)
    return total

assert call_poly(objs, 40) == sum(i + (i % 4) + 1 for i in range(40))
# Mutate class B method to verify invalidation
B.f = lambda self, x: x + 20
assert call_poly(objs, 8) == (0+1)+(1+20)+(2+3)+(3+4)+(4+1)+(5+20)+(6+3)+(7+4)
# 5th receiver exercises eviction
objs5 = [A(), B(), C(), D(), E()]
assert call_poly(objs5, 10) == sum(objs5[i % 5].f(i) for i in range(10))

# 2. Polymorphic module and type (@classmethod / @staticmethod) method PIC + invalidation
class T1:
    @classmethod
    def make(cls, x): return (cls.__name__, x * 2)
    @staticmethod
    def tag(x): return x + 10

class T2:
    @classmethod
    def make(cls, x): return (cls.__name__, x * 3)
    @staticmethod
    def tag(x): return x + 20

types = [T1, T2]
res = []
for i in range(20):
    t = types[i & 1]
    res.append((t.make(i), t.tag(i)))
assert res[0] == (("T1", 0), 10)
assert res[1] == (("T2", 3), 21)

# Invalidate classmethod on T2
T2.make = classmethod(lambda cls, x: (cls.__name__, x * 5))
assert T2.make(4) == ("T2", 20)

mods = [math, operator]
# Call polymorphic module attribute (not same name, test shared name via custom modules)
import types as pytypes
m1 = pytypes.ModuleType("m1")
m1.op = lambda x: x + 100
m2 = pytypes.ModuleType("m2")
m2.op = lambda x: x + 200
ms = [m1, m2]
tot = 0
for i in range(20):
    tot += ms[i & 1].op(i)
assert tot == sum(i + (100 if (i & 1) == 0 else 200) for i in range(20))
m2.op = lambda x: x + 300
assert ms[1].op(5) == 305

# 3. O(1) FastID builtin dispatch parity
def gen(n):
    for i in range(n):
        yield i

for _ in range(15):
    g = gen(3)
    assert next(g) == 0
    assert next(g) == 1
    assert next(g) == 2
    assert next(g, -1) == -1
    assert ",".join(str(i) for i in range(4)) == "0,1,2,3"
    lst = []
    lst.extend(i * 2 for i in range(4))
    assert lst == [0, 2, 4, 6]
    assert any(i > 2 for i in range(5)) is True
    assert all(i >= 0 for i in range(5)) is True
    assert sum(i for i in range(5)) == 10
    assert sum((i for i in range(5)), 100) == 110
    assert min(i for i in range(3, 8)) == 3
    assert max(i for i in range(3, 8)) == 7
    assert len(lst) == 4
    assert len((1, 2, 3)) == 3
    assert len({"a": 1, "b": 2}) == 2
    assert len("abc") == 3
    assert abs(-42) == 42
    assert abs(42) == 42
    assert abs(-(1 << 63)) == (1 << 63)
    assert ord("A") == 65
    assert chr(65) == "A"
    assert isinstance(id(lst), int) and id(lst) == id(lst)

# 4. opCallBoundNative across multiple bound native receivers
pat = re.compile(r"(\w+)=(\d+)")
match_fn = pat.match
out = []
append_fn = out.append
for i in range(20):
    m = match_fn(f"k{i}={i}")
    assert m is not None
    append_fn(m.group(1))
assert len(out) == 20 and out[19] == "k19"

# 5. functools.partial direct unwrapping (positional, nested, bound method, builtin, class, keywords)
def add3(a, b, c):
    return a * 100 + b * 10 + c

p1 = functools.partial(add3, 1)
p2 = functools.partial(p1, 2)
for i in range(20):
    assert p1(2, i) == 120 + i
    assert p2(i) == 120 + i

class Acc:
    def __init__(self, base, extra):
        self.val = base + extra
    def add(self, x, y):
        return self.val + x + y

a = Acc(10, 5)
p_bm = functools.partial(a.add, 100)
p_cls = functools.partial(Acc, 7)
p_bi = functools.partial(int, "1010")
p_kw = functools.partial(add3, 1, c=9)
for i in range(15):
    assert p_bm(i) == 115 + i
    assert p_cls(i).val == 7 + i
    assert p_bi(2) == 10
    assert p_kw(i) == 100 + i * 10 + 9
`)
}

func TestWave5JSONKeyCacheAndASTShapes(t *testing.T) {
	runPySource(t, `
import ast
import json

# 1. JSON key cache: <=7B inline, 8..32B heap, >32B, empty "", escaped, unicode, duplicates, strict control chars
long_key = "k" * 40
payload = (
    '['
    '{"id": 1, "customer_name": "alice", "": 0, "' + long_key + '": 9, "a\\"b": 2, "caf\\u00e9": 3, "日本語": 4},'
    '{"id": 2, "customer_name": "bob",   "": 5, "' + long_key + '": 8, "a\\"b": 6, "café": 7, "日本語": 8}'
    ']'
)
for _ in range(10):
    rows = json.loads(payload)
    assert len(rows) == 2
    assert rows[0]["id"] == 1
    assert rows[0]["customer_name"] == "alice"
    assert rows[0][""] == 0
    assert rows[0][long_key] == 9
    assert rows[0]['a"b'] == 2
    assert rows[0]["café"] == 3
    assert rows[0]["日本語"] == 4
    assert rows[1]["id"] == 2
    assert rows[1]["customer_name"] == "bob"
    assert rows[1][""] == 5

# Duplicate keys: last key wins, len(d) stays 1
dup = json.loads('{"a": 1, "a": 2, "long_repeated_key": 10, "long_repeated_key": 20}')
assert dup == {"a": 2, "long_repeated_key": 20}
assert len(dup) == 2

# Control characters in keys must fail in strict mode and succeed when strict=False
try:
    json.loads('{"a\nb": 1}')
    assert False, "expected JSONDecodeError for control char in key"
except json.JSONDecodeError:
    pass
non_strict = json.loads('{"a\nb": 1}', strict=False)
assert non_strict == {"a\nb": 1}

# 2. _ast pre-seeded shapes: attribute layout, vars(), del, dynamic attribute addition
tree = ast.parse("def f(x):\n    y = x + 1\n    return y\n")
fn = tree.body[0]
assert isinstance(fn, ast.FunctionDef)
assert fn.name == "f"
assert fn.lineno == 1
assert fn.col_offset == 0
v = vars(fn)
assert v["name"] == "f"
assert v["lineno"] == 1
assert "body" in v

# Dynamic attribute addition on pre-seeded shape
fn.custom_meta = "tagged"
assert fn.custom_meta == "tagged"
assert vars(fn)["custom_meta"] == "tagged"

# del attribute on pre-seeded shape
del fn.custom_meta
assert not hasattr(fn, "custom_meta")
del fn.name
assert not hasattr(fn, "name")
assert "name" not in vars(fn)
del fn.returns
assert "returns" not in vars(fn)
assert fn.returns is None  # optional field falls back to class default None
`)
}

func TestWave5ConcurrentJSONAndAST(t *testing.T) {
	eng := vm.NewEngine()
	eng.SetCompileHook(compiler.CompileSource)
	src := `
import ast
import json

raw = '[{"id": 1, "name": "alpha", "a": 1, "a": 2}, {"id": 2, "name": "beta"}]'
for _ in range(20):
    d = json.loads(raw)
    assert d[0]["a"] == 2 and d[1]["name"] == "beta"
    tree = ast.parse("x = 1 + 2\n")
    assert tree.body[0].lineno == 1
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			v := eng.AcquireVM()
			defer eng.ReleaseVM(v)
			v.RunCode(co)
			if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
				errs <- fmt.Errorf("python exception: %s", v.FormatException(exc))
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent exec failed: %v", err)
	}
}
