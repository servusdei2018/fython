package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestGeneratorYieldUnpackReuseAndSafety(t *testing.T) {
	prevMT := val.IsMultiThreaded()
	val.SetMultiThreaded(false)
	defer val.SetMultiThreaded(prevMT)

	_, v := runPySource(t, `
# 1. OP_UNPACK_2_FAST (for a, b in gen())
def pairs(n):
    for i in range(n):
        yield (i, i * 10)

s2 = 0
for a, b in pairs(100):
    s2 += a + b
assert s2 == sum(i + i * 10 for i in range(100))

# 2. OP_UNPACK_SEQUENCE (3-tuple and 1-tuple)
def triples(n):
    for i in range(n):
        yield (i, i + 1, i + 2)

s3 = 0
for a, b, c in triples(100):
    s3 += a + b + c
assert s3 == sum(3 * i + 3 for i in range(100))

def singles(n):
    for i in range(n):
        yield (i,)

s1 = 0
for (x,) in singles(20):
    s1 += x
assert s1 == sum(range(20))

# 3. Nested generators
def inner(n):
    for j in range(n):
        yield (j, j + 1)

def outer(n):
    for i in range(n):
        for a, b in inner(4):
            yield (i, a, b)

nested_sum = 0
for i, a, b in outer(10):
    nested_sum += i * 100 + a * 10 + b
assert nested_sum == sum(i * 100 + j * 10 + (j + 1) for i in range(10) for j in range(4))

# 4. Unpack length mismatch raises ValueError cleanly
mismatch_caught = False
try:
    for a, b in triples(3):
        pass
except ValueError:
    mismatch_caught = True
assert mismatch_caught

# 5. Non-unpack loop must NOT alias yielded tuples
saved = []
for t in pairs(10):
    saved.append(t)
assert saved == [(i, i * 10) for i in range(10)]
assert saved[0] is not saved[1]

# 6. list(gen()) and tuple(gen()) must NOT alias yielded tuples
listed = list(triples(10))
assert listed == [(i, i + 1, i + 2) for i in range(10)]
assert listed[0] is not listed[1]
tupled = tuple(pairs(8))
assert tupled == tuple((i, i * 10) for i in range(8))

# 7. yield from delegation
def delegator(n):
    yield from pairs(n)

s_yf = 0
for a, b in delegator(20):
    s_yf += a + b
assert s_yf == sum(i + i * 10 for i in range(20))

# 8. Mid-loop break followed by next(gen) must NOT alias
g_break = pairs(5)
got_first = None
for a, b in g_break:
    got_first = (a, b)
    break
nxt = next(g_break)
assert got_first == (0, 0)
assert nxt == (1, 10)

# 9. Exception mid-loop and explicit generator close cleanup
g_close = triples(10)
for a, b, c in g_close:
    if a == 2:
        break
g_close.close()

g_exc = pairs(10)
caught_mid = False
try:
    for a, b in g_exc:
        if a == 3:
            raise RuntimeError("stop")
except RuntimeError:
    caught_mid = True
assert caught_mid
g_exc.close()
`)
	// Verify closed generators cleared YieldTuple to prevent GC retention.
	for _, name := range []string{"g_close", "g_exc"} {
		gv, ok := v.Globals().Get(val.InternString(name))
		if !ok || !gv.IsGenerator() {
			t.Fatalf("expected global generator %s", name)
		}
		if gv.AsGenerator().YieldTuple != nil {
			t.Fatalf("closed generator %s still retains YieldTuple", name)
		}
	}
}
