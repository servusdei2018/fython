package vm_test

import (
	"testing"
)

func TestPolymorphicMethodPICAndBuiltinsAndPartial(t *testing.T) {
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
