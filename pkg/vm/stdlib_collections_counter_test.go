package vm_test

import (
	"testing"
)

func TestCounterStreamingParity(t *testing.T) {
	runSnippet(t, `
from collections import Counter
# exact-type fast paths
assert Counter("abracadabra")["a"] == 5
assert Counter([1, 2, 2, 3, 3, 3])[3] == 3
assert Counter((1, 1, 2))[1] == 2
assert Counter(b"aab") == Counter({97: 2, 98: 1})
assert Counter({"x": 1, "y": 2}) == Counter({"x": 1, "y": 2})
# non-int existing counts use numeric add, not clobber
c = Counter({"a": 2.5})
c.update(["a"])
assert c["a"] == 3.5, c["a"]
# unhashable element raises TypeError
try:
    Counter([[1]])
    raise AssertionError("expected TypeError")
except TypeError:
    pass
`)
}

func TestCounterSubclassIterRespected(t *testing.T) {
	runSnippet(t, `
from collections import Counter
# list subclass overriding __iter__ must be honored, not bypassed
class MyList(list):
    def __iter__(self):
        return iter(["z", "z", "q"])
assert Counter(MyList([1, 2, 3])) == Counter({"z": 2, "q": 1})
# str subclass overriding __iter__ must be honored
class MyStr(str):
    def __iter__(self):
        return iter(["k", "k"])
assert Counter(MyStr("abc")) == Counter({"k": 2})
# tuple subclass overriding __iter__
class MyTuple(tuple):
    def __iter__(self):
        return iter([9, 9])
assert Counter(MyTuple((1, 2))) == Counter({9: 2})
# dict subclass iterable: Mapping fast path wins over __iter__
# (CPython Counter copies mapping contents, ignoring __iter__ override)
class MyDict(dict):
    def __iter__(self):
        return iter(["m", "m"])
assert Counter(MyDict({"a": 1})) == Counter({"a": 1})
`)
}

func TestCounterMissingAndBigCounts(t *testing.T) {
	runSnippet(t, `
from collections import Counter
# dict subclass with __missing__ still counts from zero without invoking it
class M(dict):
    def __missing__(self, k):
        raise AssertionError("__missing__ must not be called by _count_elements")
m = M()
from collections import Counter as C
# drive _count_elements directly through Counter update on a plain dict mapping
c = C()
c.update(["a", "a", "b"])
assert c == Counter({"a": 2, "b": 1})
# big-int existing counts increment without clobbering
big = 1 << 100
c2 = Counter({big: big})
c2.update([big])
assert c2[big] == big + 1
`)
}
