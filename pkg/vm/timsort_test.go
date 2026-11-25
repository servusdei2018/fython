package vm_test

import "testing"

func TestTimsortPrimitivesAndPatterns(t *testing.T) {
	runSnippet(t, `
n = 500
a = list(range(n))
a.sort()
assert a == list(range(n))

b = list(range(n, 0, -1))
b.sort()
assert b == list(range(1, n + 1))

c = [(i * 2654435761) % 97 for i in range(n)]
c_sorted = sorted(c)
c.sort()
assert c == c_sorted
for i in range(1, len(c)):
    assert c[i - 1] <= c[i]

s = ["z", "alpha_long_string", "a", "beta_long_string", "ab", "a", "gamma_0001", "b"] * 40
s1 = sorted(s)
s2 = list(s)
s2.sort()
assert s1 == s2
for i in range(1, len(s2)):
    assert s2[i - 1] <= s2[i]

f = [float((i * 17) % 53) - 20.5 for i in range(300)]
f.sort()
for i in range(1, len(f)):
    assert f[i - 1] <= f[i]
`)
}

func TestTimsortStabilityAndReverse(t *testing.T) {
	runSnippet(t, `
items = [(i % 7, i) for i in range(200)]

a = list(items)
a.sort(key=lambda x: x[0])
for i in range(1, len(a)):
    if a[i - 1][0] == a[i][0]:
        assert a[i - 1][1] < a[i][1]
    else:
        assert a[i - 1][0] < a[i][0]

b = list(items)
b.sort(key=lambda x: x[0], reverse=True)
for i in range(1, len(b)):
    if b[i - 1][0] == b[i][0]:
        assert b[i - 1][1] < b[i][1]
    else:
        assert b[i - 1][0] > b[i][0]

tups = [(i % 5, f"k{i % 3}", i) for i in range(250)]
t1 = sorted(tups, key=lambda x: (x[0], x[1]))
for i in range(1, len(t1)):
    k_prev = (t1[i - 1][0], t1[i - 1][1])
    k_curr = (t1[i][0], t1[i][1])
    if k_prev == k_curr:
        assert t1[i - 1][2] < t1[i][2]
    else:
        assert k_prev < k_curr
`)
}

func TestTimsortNaNAndMixedNumbers(t *testing.T) {
	runSnippet(t, `
nan = float("nan")
xs = [3.0, nan, 1.0, 2.0, nan, 0.5]
xs.sort()
assert len(xs) == 6

big = 10 ** 30
mixed = [big, 3.5, True, 2, False, -big, 1.0, 0]
mixed.sort()
assert mixed[0] == -big
assert mixed[-1] == big

eq_test = [1, True, 1.0, 0, False, 0.0]
eq_test.sort()
assert type(eq_test[0]) is int and eq_test[0] == 0
assert type(eq_test[1]) is bool and eq_test[1] is False
assert type(eq_test[2]) is float and eq_test[2] == 0.0
assert type(eq_test[3]) is int and eq_test[3] == 1
assert type(eq_test[4]) is bool and eq_test[4] is True
assert type(eq_test[5]) is float and eq_test[5] == 1.0
`)
}

func TestTimsortMutationAndExceptionRollback(t *testing.T) {
	runSnippet(t, `
lst = [3, 1, 2]
def mut_key(x):
    lst.append(99)
    return x
try:
    lst.sort(key=mut_key)
    assert False, "expected ValueError"
except ValueError as e:
    assert "modified" in str(e)

lst2 = []
class MutItem:
    def __init__(self, v):
        self.v = v
    def __lt__(self, other):
        lst2.append(99)
        return self.v < other.v
lst2.extend([MutItem(3), MutItem(1), MutItem(2)])
try:
    lst2.sort()
    assert False, "expected ValueError"
except ValueError as e:
    assert "modified" in str(e)

class BoomItem:
    calls = 0
    def __init__(self, v):
        self.v = v
    def __lt__(self, other):
        BoomItem.calls += 1
        if BoomItem.calls >= 4:
            raise RuntimeError("boom")
        return self.v < other.v

for rev in (False, True):
    BoomItem.calls = 0
    orig_vals = [5, 3, 8, 1, 4, 2, 7, 6]
    arr = [BoomItem(v) for v in orig_vals]
    try:
        arr.sort(reverse=rev)
        assert False, "expected RuntimeError"
    except RuntimeError:
        pass
    assert [x.v for x in arr] == orig_vals
`)
}

func TestTimsortCustomTupleDunderAndMonkeyPatch(t *testing.T) {
	runSnippet(t, `
class Weird:
    def __init__(self, a, b):
        self.a = a
        self.b = b
    def __eq__(self, other):
        return self.a == other.a
    def __lt__(self, other):
        return self.b < other.b

items = [(Weird(1, 10), 2), (Weird(1, 5), 1), (Weird(2, 1), 0)]
res = sorted(items)
assert res[0][0].a == 2
assert res[1][1] == 1
assert res[2][1] == 2

class PatchMe:
    calls = 0
    def __init__(self, v):
        self.v = v
    def __eq__(self, other):
        return self.v == other.v
    def __lt__(self, other):
        PatchMe.calls += 1
        if PatchMe.calls == 2:
            PatchMe.__lt__ = lambda s, o: s.v > o.v
        return self.v < other.v

arr = [(PatchMe(i), i) for i in range(10)]
arr.sort()
assert len(arr) == 10
`)
}
