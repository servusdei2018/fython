package vm_test

import (
	"testing"
)

func TestFastGeneratorDrainingAndDeque(t *testing.T) {
	_, _ = runPySource(t, `
from collections import deque

def gen_range(n):
    for i in range(n):
        yield i

def gen_yield_from(n):
    yield from gen_range(n // 2)
    yield from range(n // 2, n)

# deque(gen), deque(gen, maxlen=3), deque.extend(gen), self-extend
d1 = deque(gen_range(20))
assert list(d1) == list(range(20))

d2 = deque(gen_range(20), maxlen=3)
assert list(d2) == [17, 18, 19]

d3 = deque([1, 2], maxlen=4)
d3.extend(gen_yield_from(6))
assert list(d3) == [2, 3, 4, 5]

d4 = deque([1, 2, 3])
d4.extend(d4)
assert list(d4) == [1, 2, 3, 1, 2, 3]

# list/tuple/set draining
assert list(gen_yield_from(10)) == list(range(10))
assert tuple(gen_yield_from(10)) == tuple(range(10))
assert set(gen_range(10)) == set(range(10))

# Mid-drain exception and try/finally execution
cleaned = []
def failing_gen():
    try:
        yield 1
        yield 2
        raise RuntimeError("boom")
    finally:
        cleaned.append("done")

d_err = deque()
caught = False
try:
    d_err.extend(failing_gen())
except RuntimeError as e:
    caught = (str(e) == "boom")
assert caught
assert list(d_err) == [1, 2]
assert cleaned == ["done"]

# Re-entrant generator must raise ValueError
reentrant_err = False
def self_drain():
    global reentrant_err
    try:
        list(g_self)
    except ValueError:
        reentrant_err = True
    yield 42

g_self = self_drain()
assert list(g_self) == [42]
assert reentrant_err
`)
}
