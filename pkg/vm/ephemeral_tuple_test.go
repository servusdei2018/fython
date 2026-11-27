package vm_test

import (
	"testing"
)

func TestEphemeralTupleCompareAndMembership(t *testing.T) {
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
# captured_eq gets 3 entries per iteration (tuple cmp eq check + tuple-in + list-in;
# set-in skips __eq__ on hash mismatch per CPython).
assert len(captured_eq) == 75
for idx in range(25):
    for j in range(3):
        assert captured_eq[idx * 3 + j] == (idx, idx + 1), f"eq mismatch at {idx},{j}: {captured_eq[idx * 3 + j]}"

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
