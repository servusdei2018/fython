package vm_test

import "testing"

func TestDictAndSetCompPresizeSources(t *testing.T) {
	eng, v := runPySource(t, `
src_list = list(range(50))
src_tup = tuple(range(50))
src_rng = range(50)
def gen(n):
    for i in range(n):
        yield i

# Unfiltered dict comprehensions across list, tuple, range, stepped range, generator.
for seq in (src_list, src_tup, src_rng, range(0, 100, 2), gen(50)):
    d = {x: x * 2 for x in seq}
    assert len(d) == 50
    for k, val_ in d.items():
        assert val_ == k * 2

# Filtered dict comprehensions.
for seq in (src_list, src_tup, src_rng, gen(50)):
    df = {x: x + 1 for x in seq if (x & 1) == 0}
    assert len(df) == 25
    assert all((k & 1) == 0 and val_ == k + 1 for k, val_ in df.items())

# Unfiltered and filtered set comprehensions.
for seq in (src_list, src_tup, src_rng, range(0, 100, 2), gen(50)):
    s = {x * 3 for x in seq}
    assert len(s) == 50
for seq in (src_list, src_tup, src_rng, gen(50)):
    sf = {x for x in seq if x % 5 == 0}
    assert len(sf) == 10

# Small (<=5) dict comprehension stays valid on smallDictBundle.
dsmall = {x: x for x in range(4)}
assert dsmall == {0: 0, 1: 1, 2: 2, 3: 3}
dsmall[99] = 99
assert len(dsmall) == 5

# Nested comprehensions.
dnest = {(i, j): i + j for i in range(10) for j in range(5) if (i + j) % 2 == 0}
assert len(dnest) == 25
snest = {i * 10 + j for i in range(8) for j in range(4)}
assert len(snest) == 32

# Duplicate keys in dict/set comprehensions.
ddup = {x % 7: x for x in range(100)}
assert len(ddup) == 7
assert ddup[0] == 98
sdup = {x % 7 for x in range(100)}
assert sdup == {0, 1, 2, 3, 4, 5, 6}
`)
	defer eng.ReleaseVM(v)
}

func TestCompPresizeHugeRangeCapAndNonCompOps(t *testing.T) {
	eng, v := runPySource(t, `
# Huge range with early exit / sparse filter must respect the cap and never OOM.
def take_five(it):
    n = 0
    for x in it:
        yield x
        n += 1
        if n == 5:
            break

d = {x: x for x in take_five(range(10**9))}
assert d == {0: 0, 1: 1, 2: 2, 3: 3, 4: 4}

# Non-comprehension OP_SET_ADD ({*a, *b}) and annotations (__annotate__ OP_MAP_ADD).
a = [1, 2, 3]
b = [3, 4, 5]
su = {*a, *b}
assert su == {1, 2, 3, 4, 5}

def annotated(x: int, y: str) -> bool:
    return True

assert annotated.__annotations__ == {"x": int, "y": str, "return": bool}
`)
	defer eng.ReleaseVM(v)
}

func TestListAppendAndCompPresizeConcurrent(t *testing.T) {
	eng, v := runPySource(t, `
import threading

shared_list = []
errors = []

def worker(tid):
    try:
        for i in range(50):
            shared_list.append((tid, i))
        d = {x: x * 2 for x in range(100)}
        s = {x for x in range(100) if (x & 1) == 0}
        assert len(d) == 100
        assert len(s) == 50
    except Exception as e:
        errors.append(e)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(6)]
for th in threads:
    th.start()
for th in threads:
    th.join()

assert not errors, f"concurrent errors: {errors}"
assert len(shared_list) == 300
`)
	defer eng.ReleaseVM(v)
}
