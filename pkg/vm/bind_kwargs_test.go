package vm_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestBindKwargsBitmaskAndSplitDict(t *testing.T) {
	// Build a 64-param function and a 66-param function to exercise both
	// uint64 bitmask boundary (64) and >64 fallback (66).
	makeManyParamDef := func(name string, n int) string {
		params := make([]string, n)
		for i := range n {
			params[i] = fmt.Sprintf("p%d=%d", i, i)
		}
		return fmt.Sprintf("def %s(%s):\n    return p0 + p%d + p%d\n", name, strings.Join(params, ", "), n-2, n-1)
	}

	src := makeManyParamDef("f64", 64) + "\n" + makeManyParamDef("f66", 66) + `
# 0, 1, 63, 64, and 66 param bitmask / fallback checks
def f0():
    return 7
def f1(a=1):
    return a
assert f0() == 7
assert f1() == 1 and f1(a=9) == 9
assert f64(10, p63=100) == 10 + 62 + 100
assert f64(p0=5, p62=20, p63=30) == 55
assert f66(10, p65=200) == 10 + 64 + 200
assert f66(p0=1, p64=2, p65=3) == 6

# Duplicate keyword TypeError
dup_err = False
try:
    f1(1, a=2)
except TypeError:
    dup_err = True
assert dup_err

# Missing required positional / kw-only TypeError
def req_fn(a, b, *, c, d=4):
    return a + b + c + d

miss_pos = False
try:
    req_fn(1, c=3)
except TypeError:
    miss_pos = True
assert miss_pos

miss_kw = False
try:
    req_fn(1, 2)
except TypeError:
    miss_kw = True
assert miss_kw
assert req_fn(1, 2, c=3) == 10

# Repeated **kwargs SplitDictKeys hit + post-call mutation triggering DisableSplit
captured = []
def collect(a, **kw):
    captured.append(kw)
    return a + kw.get("x", 0) + kw.get("y", 0)

for i in range(20):
    assert collect(i, x=i + 1, y=i + 2) == 3 * i + 3

# Mutate captured **kwargs dicts (insert new keys, delete keys) to trigger DisableSplit
for d in captured:
    d["z"] = 99
    del d["x"]
    assert d["z"] == 99 and "x" not in d

# Subsequent calls at the same call site must continue working cleanly after DisableSplit
for i in range(10):
    assert collect(i, x=1, y=2) == i + 3

# Polymorphic call site signature mismatch: f(a=1, b=2) vs g(a=1, b=2)
def fa(a, **kw):
    return ("fa", a, sorted(kw.items()))

def fb(b, **kw):
    return ("fb", b, sorted(kw.items()))

funcs = [fa, fb]
out = []
for i in range(6):
    fn = funcs[i & 1]
    out.append(fn(a=i, b=i + 10))
assert out[0] == ("fa", 0, [("b", 10)])
assert out[1] == ("fb", 11, [("a", 1)])
assert out[2] == ("fa", 2, [("b", 12)])
assert out[3] == ("fb", 13, [("a", 3)])
`
	runPySource(t, src)
}

func TestBindKwargsConcurrent(t *testing.T) {
	runPySource(t, `
import threading

def accept(a, b=2, *, c=3, **kw):
    return a + b + c + kw.get("k1", 0) + kw.get("k2", 0)

expected = sum(i + 2 + (i + 1) + 5 for i in range(200))
results = [0] * 4
errors = []

def worker(idx):
    try:
        s = 0
        for i in range(200):
            s += accept(i, c=i + 1, k1=2, k2=3)
        results[idx] = s
    except Exception as exc:
        errors.append(exc)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()

assert not errors, errors
assert results == [expected] * 4, results
`)
}
