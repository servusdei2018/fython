package vm_test

import "testing"

func TestCompareFastPathsStringsFloatsTuples(t *testing.T) {
	eng, v := runPySource(t, `
# 1. Inline and heap string ordering and equality
def cmp_all(a, b):
    return (a < b, a <= b, a == b, a != b, a > b, a >= b)

# Inline strings (<= 7 bytes)
assert cmp_all("abc", "abd") == (True, True, False, True, False, False)
assert cmp_all("abc", "abc") == (False, True, True, False, False, True)
assert cmp_all("abd", "abc") == (False, False, False, True, True, True)
assert cmp_all("", "a") == (True, True, False, True, False, False)

# Heap strings (> 7 bytes) and mixed inline/heap
h1 = "aaaaaaaa_heap_1"
h2 = "aaaaaaaa_heap_2"
assert cmp_all(h1, h2) == (True, True, False, True, False, False)
assert cmp_all(h1, h1 + "") == (False, True, True, False, False, True)
assert cmp_all("aaaaaaa", h1) == (True, True, False, True, False, False)
assert cmp_all(h1, "b") == (True, True, False, True, False, False)

# str subclass must still dispatch custom dunders
class RevStr(str):
    def __lt__(self, other):
        return str.__gt__(self, other)
    def __eq__(self, other):
        return str.__ne__(self, other)

r1 = RevStr("abc")
r2 = RevStr("xyz")
for _ in range(10):
    assert (r1 < r2) is False
    assert (r2 < r1) is True
    assert (r1 == RevStr("abc")) is False

# 2. Float comparisons including NaN and signed zero
nan = float("nan")
assert cmp_all(1.5, 2.5) == (True, True, False, True, False, False)
assert cmp_all(-0.0, 0.0) == (False, True, True, False, False, True)
assert cmp_all(nan, nan) == (False, False, False, True, False, False)
assert cmp_all(nan, 1.0) == (False, False, False, True, False, False)
assert cmp_all(1.0, nan) == (False, False, False, True, False, False)

# 3. Nested PyTuple comparisons (packaging version-key style)
v1 = (0, (3, 15, 0), ("a", 1))
v2 = (0, (3, 15, 0), ("a", 2))
v3 = (0, (3, 16, 0), ("a", 1))
v4 = (0, (3, 15, 0), ("b", 0))
for _ in range(10):
    assert v1 < v2
    assert v1 <= v2
    assert v2 < v3
    assert v2 < v4
    assert v1 == (0, (3, 15, 0), ("a", 1))
    assert not (v1 != (0, (3, 15, 0), ("a", 1)))
    assert v3 > v2
    assert v3 >= v2

# Different lengths and mixed element types
assert (1, 2) < (1, 2, 0)
assert (1, 2, 0) > (1, 2)
assert (1, 2) <= (1, 2)
assert (1, 2) >= (1, 2)
assert (1, True, None, "abc") == (1, 1, None, "abc")
assert (1, False) != (1, True)
assert (None, 1) < (None, 2)
assert (1.0, "a") == (1, "a")

# NaN inside tuples (distinct float('nan') elements are not equal)
assert not ((float("nan"),) == (float("nan"),))
assert (float("nan"),) != (float("nan"),)
assert not ((float("nan"),) < (1.0,))
assert not ((float("nan"),) <= (float("nan"),))

# Tuple subclass must still honor overridden comparison dunders
class CustomTuple(tuple):
    def __lt__(self, other):
        return self[0] > other[0]

ct1 = CustomTuple((1, 2))
ct2 = CustomTuple((3, 4))
for _ in range(10):
    assert (ct1 < ct2) is False
    assert (ct2 < ct1) is True

# Exception propagation from tuple element comparison
class BoomEq:
    def __eq__(self, other):
        raise ValueError("eq boom")

class BoomLt:
    def __eq__(self, other):
        return False
    def __lt__(self, other):
        raise ValueError("lt boom")

b1 = BoomEq()
b2 = BoomEq()
caught_eq = False
try:
    _ = (1, b1) == (1, b2)
except ValueError as e:
    caught_eq = str(e) == "eq boom"
assert caught_eq

l1 = BoomLt()
l2 = BoomLt()
caught_lt = False
try:
    _ = (1, l1) < (1, l2)
except ValueError as e:
    caught_lt = str(e) == "lt boom"
assert caught_lt

caught_type = False
try:
    _ = (1, "a") < (1, 2)
except TypeError:
    caught_type = True
assert caught_type
`)
	eng.ReleaseVM(v)
}

func TestCompareDunderICMonomorphicPolymorphicAndInvalidation(t *testing.T) {
	eng, v := runPySource(t, `
# 1. All 6 rich comparison dunders on a user class (monomorphic hot loop)
class Box:
    def __init__(self, v):
        self.v = v
    def __eq__(self, other):
        if isinstance(other, Box):
            return self.v == other.v
        return NotImplemented
    def __ne__(self, other):
        if isinstance(other, Box):
            return self.v != other.v
        return NotImplemented
    def __lt__(self, other):
        if isinstance(other, Box):
            return self.v < other.v
        return NotImplemented
    def __le__(self, other):
        if isinstance(other, Box):
            return self.v <= other.v
        return NotImplemented
    def __gt__(self, other):
        if isinstance(other, Box):
            return self.v > other.v
        return NotImplemented
    def __ge__(self, other):
        if isinstance(other, Box):
            return self.v >= other.v
        return NotImplemented

a = Box(10)
b = Box(20)
c = Box(10)
for _ in range(300):
    assert (a < b) is True
    assert (b < a) is False
    assert (a <= c) is True
    assert (a == c) is True
    assert (a != b) is True
    assert (b > a) is True
    assert (b >= a) is True

# 2. NotImplemented fallback to reflected dunder, default equality, and TypeError
class OtherBox:
    def __init__(self, v):
        self.v = v
    def __gt__(self, other):
        return self.v > other.v
    def __eq__(self, other):
        return self.v == other.v

ob = OtherBox(20)
for _ in range(10):
    assert (a < ob) is True
    assert (a == ob) is False
    assert (a == OtherBox(10)) is True
    assert (a == 999) is False
    assert (a != 999) is True

got_type_err = False
try:
    _ = a < 999
except TypeError:
    got_type_err = True
assert got_type_err

# 3. Polymorphic comparison site switching across multiple types
class Alt:
    def __init__(self, v):
        self.v = v
    def __lt__(self, other):
        return self.v < other.v

def poly_lt(x, y):
    return x < y

pairs = [
    (Box(1), Box(2), True),
    (Alt(5), Alt(3), False),
    (1, 2, True),
    ("a", "b", True),
    ((1, 2), (1, 3), True),
    (Box(4), Box(4), False),
    (Alt(1), Alt(9), True),
]
for _ in range(10):
    for x, y, expected in pairs:
        assert poly_lt(x, y) is expected

# 4. Strict subclass reflected priority (Base < Sub must call Sub.__gt__ first)
calls = []
class Base:
    def __init__(self, v):
        self.v = v
    def __lt__(self, other):
        calls.append("Base.__lt__")
        return self.v < other.v

class Sub(Base):
    def __gt__(self, other):
        calls.append("Sub.__gt__")
        return self.v > other.v

base_obj = Base(1)
sub_obj = Sub(2)
for _ in range(10):
    assert (base_obj < sub_obj) is True
assert all(c == "Sub.__gt__" for c in calls)
assert len(calls) == 10

# If Sub.__gt__ returns NotImplemented, falls back to Base.__lt__
calls.clear()
class SubNotImpl(Base):
    def __gt__(self, other):
        calls.append("SubNotImpl.__gt__")
        return NotImplemented

sub_ni = SubNotImpl(2)
assert (base_obj < sub_ni) is True
assert "SubNotImpl.__gt__" in calls and "Base.__lt__" in calls

# 5. Dynamic class mutation invalidates quickened OP_COMPARE_DUNDER via type version bump
class Mutable:
    def __init__(self, v):
        self.v = v
    def __lt__(self, other):
        return self.v < other.v

def cmp_mut(x, y):
    return x < y

m1 = Mutable(1)
m2 = Mutable(2)
for _ in range(10):
    assert cmp_mut(m1, m2) is True

Mutable.__lt__ = lambda self, other: self.v > other.v
for _ in range(10):
    assert cmp_mut(m1, m2) is False

del Mutable.__lt__
got_err = False
try:
    _ = cmp_mut(m1, m2)
except TypeError:
    got_err = True
assert got_err

# 6. __contains__ quickening for both 'in' and 'not in', with polymorphic left operands
class Bag:
    def __init__(self, items):
        self.items = items
    def __contains__(self, item):
        return 1 if item in self.items else 0

bag = Bag({1, "x", (2, 3)})
def check_in(k, c):
    return k in c
def check_not_in(k, c):
    return k not in c

for _ in range(10):
    assert check_in(1, bag) is True
    assert check_in("x", bag) is True
    assert check_in((2, 3), bag) is True
    assert check_in("missing", bag) is False
    assert check_not_in("missing", bag) is True
    assert check_not_in(1, bag) is False

# Dynamic __contains__ mutation invalidation
Bag.__contains__ = lambda self, item: False
for _ in range(5):
    assert check_in(1, bag) is False
    assert check_not_in(1, bag) is True

# 7. Dict/set 'in' / 'not in' fast paths and subclasses
d = {"a": 1, 2: "two"}
s = {"a", 2}
for _ in range(10):
    assert ("a" in d) is True
    assert ("z" in d) is False
    assert ("z" not in d) is True
    assert (2 in d) is True
    assert ("a" in s) is True
    assert ("z" not in s) is True

class CustomDict(dict):
    def __contains__(self, key):
        return True

cd = CustomDict()
for _ in range(5):
    assert ("anything" in cd) is True
    assert ("anything" not in cd) is False
`)
	eng.ReleaseVM(v)
}
