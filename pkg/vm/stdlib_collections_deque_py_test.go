package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestDequePythonSemanticsAndMutationInvalidation(t *testing.T) {
	eng, v := runPySource(t, `
from collections import deque

# Basic wrap-around, append/appendleft/pop/popleft, indexing, negative indices
d = deque([1, 2, 3])
d.append(4)
d.appendleft(0)
basic_ok = (
    list(d) == [0, 1, 2, 3, 4]
    and d[0] == 0
    and d[-1] == 4
    and d.pop() == 4
    and d.popleft() == 0
    and list(d) == [1, 2, 3]
)

# maxlen eviction & maxlen == 0
dm = deque([1, 2, 3, 4, 5], maxlen=3)
dm.append(6)
dm_after_append = list(dm)
dm.appendleft(0)
dz = deque([1, 2, 3], maxlen=0)
dz.append(1)
dz.appendleft(2)
maxlen_ok = (
    dm_after_append == [4, 5, 6]
    and list(dm) == [0, 4, 5]
    and dm.maxlen == 3
    and list(dz) == []
    and len(dz) == 0
    and dz.maxlen == 0
)

# rotate (positive, negative, |k| > len, full power-of-two ring)
dr = deque(range(8))
dr.rotate(3)
r_full = list(dr)
dr.rotate(-11)  # -11 % 8 == 5 right == 3 left -> back to original
r_back = list(dr)
dr2 = deque([1, 2, 3, 4, 5])
dr2.rotate(7)   # 7 % 5 == 2
r_part = list(dr2)
rotate_ok = (
    r_full == [5, 6, 7, 0, 1, 2, 3, 4]
    and r_back == [0, 1, 2, 3, 4, 5, 6, 7]
    and r_part == [4, 5, 1, 2, 3]
)

# extend, extendleft, self-extension, copy, reverse, remove, count, insert, index, del, in
de = deque([1, 2])
de.extend(de)
de.extendleft([0, -1])
dc = de.copy()
de.reverse()
de.insert(2, 99)
idx99 = de.index(99)
de.remove(99)
del de[0]
ops_ok = (
    list(dc) == [-1, 0, 1, 2, 1, 2]
    and idx99 == 2
    and de.count(1) == 2
    and (2 in de)
    and (99 not in de)
    and list(reversed(dc)) == [2, 1, 2, 1, 0, -1]
)

# Equality, repr, +, +=, *
eq_ok = (
    deque([1, 2]) == deque([1, 2])
    and deque([1, 2]) != deque([1, 3])
    and repr(deque([1, 2], maxlen=4)) == "deque([1, 2], maxlen=4)"
    and list(deque([1, 2]) + deque([3, 4])) == [1, 2, 3, 4]
    and list(deque([1, 2]) * 3) == [1, 2, 1, 2, 1, 2]
)

# Iterator invalidation -> RuntimeError
mut_err = False
dm2 = deque([1, 2, 3])
try:
    for x in dm2:
        dm2.append(x)
except RuntimeError:
    mut_err = True

# Re-entrant __eq__ callback mutating deque must NOT deadlock
class Mutator:
    def __init__(self, target):
        self.target = target
    def __eq__(self, other):
        self.target.append(999)
        return False

reent_err = False
dre = deque([1, 2, 3])
try:
    dre.remove(Mutator(dre))
except IndexError:
    reent_err = True

# Clear
dre.clear()
clear_ok = (len(dre) == 0 and list(dre) == [])
`)
	defer eng.ReleaseVM(v)

	for _, name := range []string{
		"basic_ok", "maxlen_ok", "rotate_ok", "ops_ok",
		"eq_ok", "mut_err", "reent_err", "clear_ok",
	} {
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("%s = %v (ok=%v), want True", name, got, ok)
		}
	}
}
