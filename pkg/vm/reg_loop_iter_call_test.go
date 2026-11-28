package vm_test

import (
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runRegTestScript(t *testing.T, src string, promoted ...string) {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	v := vm.NewVM()
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled Python exception: %v", exc)
	}
	if len(promoted) > 0 {
		assertRegPromoted(t, v, promoted...)
	}
}

func TestRegLoopEnumerateEdgeCases(t *testing.T) {
	src := `
import sys

def run_default(xs):
    acc = 0
    for i, x in enumerate(xs):
        acc += i * 10 + x
    return acc

def run_start(xs, start):
    out = []
    for i, x in enumerate(xs, start):
        out.append((i, x))
    return out

def run_start_kw(xs, start):
    out = []
    for i, x in enumerate(xs, start=start):
        out.append((i, x))
    return out

def run_no_unpack(xs):
    pairs = []
    for p in enumerate(xs):
        pairs.append(p)
    return pairs

def run_dup_target(xs):
    last = -1
    for x, x in enumerate(xs):
        last = x
    return last

def run_nonlocal_target(xs):
    slot = [0, 0]
    acc = 0
    for slot[0], slot[1] in enumerate(xs):
        acc += slot[0] + slot[1]
    return acc, slot[0], slot[1]

def run_nested(xs, ys):
    acc = 0
    for i, x in enumerate(xs):
        for j, y in enumerate(ys, 1):
            acc += (i + 1) * x + j * y
    return acc

def gen_vals(n):
    for k in range(n):
        yield k * 2

# Push all functions past Tier-2 promotion threshold (16 calls)
for _ in range(25):
    assert run_default([3, 4, 5]) == (0*10+3) + (1*10+4) + (2*10+5)
    assert run_default((3, 4, 5)) == (0*10+3) + (1*10+4) + (2*10+5)
    assert run_default(range(3, 6)) == (0*10+3) + (1*10+4) + (2*10+5)
    assert run_default(gen_vals(3)) == (0*10+0) + (1*10+2) + (2*10+4)

    # Negative and large starts
    assert run_start([10, 20], -5) == [(-5, 10), (-4, 20)]
    assert run_start_kw((10, 20), 100) == [(100, 10), (101, 20)]

    # BigInt boundary: yield MaxInt64 first, then promote to BigInt(MaxInt64 + 1)
    max64 = (1 << 63) - 1
    big_res = run_start([10, 20, 30], max64)
    assert big_res == [(max64, 10), (max64 + 1, 20), (max64 + 2, 30)]
    big_start = (1 << 80) + 7
    assert run_start([1, 2], big_start) == [(big_start, 1), (big_start + 1, 2)]

    # Tuple isolation (no aliasing when not unpacked)
    pairs = run_no_unpack([7, 8, 9])
    assert pairs == [(0, 7), (1, 8), (2, 9)]
    assert pairs[0] != pairs[1] and pairs[1] != pairs[2]

    # Duplicate target leaves item 1 (value), matching CPython
    assert run_dup_target([100, 200, 300]) == 300
    assert run_dup_target((100, 200, 300)) == 300
    assert run_dup_target(range(100, 103)) == 102

    # Non-local unpack targets
    assert run_nonlocal_target([5, 6, 7]) == (0+5 + 1+6 + 2+7, 2, 7)

    # Nested enumerate
    assert run_nested([1, 2], (3, 4)) == (1*1+1*3) + (1*1+2*4) + (2*2+1*3) + (2*2+2*4)
`
	runRegTestScript(t, src, "run_default", "run_start", "run_no_unpack", "run_dup_target", "run_nonlocal_target", "run_nested")
}

func TestRegLoopZipAndDictItemsEdgeCases(t *testing.T) {
	src := `
def run_zip2(a, b):
    acc = 0
    for x, y in zip(a, b):
        acc += x * y
    return acc

def run_zip2_strict(a, b):
    acc = 0
    for x, y in zip(a, b, strict=True):
        acc += x + y
    return acc

def run_zip3(a, b, c):
    acc = 0
    for x, y, z in zip(a, b, c):
        acc += x + y + z
    return acc

def run_zip_no_unpack(a, b):
    out = []
    for p in zip(a, b):
        out.append(p)
    return out

def run_zip_dup(a, b):
    last = -1
    for x, x in zip(a, b):
        last = x
    return last

def run_zip3_unpack2(a, b, c):
    for x, y in zip(a, b, c):
        pass

def run_dict_items(d):
    acc = 0
    for k, v in d.items():
        acc += k * v
    return acc

def run_dict_rev_items(d):
    out = []
    for k, v in reversed(d.items()):
        out.append((k, v))
    return out

def gen_range(start, stop):
    for i in range(start, stop):
        yield i

for _ in range(25):
    # List, tuple, mixed, and generator inputs
    assert run_zip2([1, 2, 3], [4, 5, 6]) == 32
    assert run_zip2((1, 2, 3), (4, 5, 6)) == 32
    assert run_zip2([1, 2, 3], (4, 5, 6)) == 32
    assert run_zip2(gen_range(1, 4), gen_range(4, 7)) == 32

    # Unequal lengths (no extra consumption on exhausted second iter)
    assert run_zip2([1, 2, 3, 4], [10, 20]) == 50
    assert run_zip2([1, 2], [10, 20, 30, 40]) == 50

    # strict=True exact match and ValueError mismatch
    assert run_zip2_strict([1, 2], [3, 4]) == 10
    raised_short = False
    try:
        run_zip2_strict([1, 2, 3], [4, 5])
    except ValueError as e:
        raised_short = "shorter" in str(e)
    assert raised_short

    raised_long = False
    try:
        run_zip2_strict([1, 2], [4, 5, 6])
    except ValueError as e:
        raised_long = "longer" in str(e)
    assert raised_long

    # 3-arg zip
    assert run_zip3([1, 2], [3, 4], [5, 6]) == 21

    # Tuple isolation (no aliasing)
    pairs = run_zip_no_unpack([1, 2], [3, 4])
    assert pairs == [(1, 3), (2, 4)]
    assert pairs[0] != pairs[1]

    # Duplicate target leaves item 1
    assert run_zip_dup([1, 2], [9, 10]) == 10

    # 3-arg zip unpacked into 2 targets raises ValueError
    raised_unpack = False
    try:
        run_zip3_unpack2([1], [2], [3])
    except ValueError:
        raised_unpack = True
    assert raised_unpack

    # dict.items() and reversed(d.items())
    d = {1: 10, 2: 20, 3: 30}
    assert run_dict_items(d) == 140
    assert run_dict_rev_items(d) == [(3, 30), (2, 20), (1, 10)]
`
	runRegTestScript(t, src)
}

func TestRegLoopModuleCallQuickening(t *testing.T) {
	src := `
import math
import types

def call_math_loop(n):
    acc = 0.0
    for i in range(n):
        x = i * 0.01
        acc += math.sin(x) + math.cos(x) + math.sqrt(x + 1.0)
    return acc

def call_mod_fn(m, n):
    acc = 0
    for i in range(n):
        acc += m.f(i)
    return acc

def call_mod_monkeypatch(m):
    acc = 0
    for i in range(40):
        if i == 20:
            m.f = lambda x: x * 10
        acc += m.f(i)
    return acc

def call_math_exc(vals):
    hits = 0
    ok_sum = 0
    for v in vals:
        try:
            r = math.factorial(v)
            ok_sum += r
            if ok_sum < 0:
                ok_sum = 0
        except ValueError:
            hits += 1
    return hits + (ok_sum * 0)

for _ in range(25):
    r = call_math_loop(20)
    assert r > 20.0

# Custom modules with builtin and Python functions + switching modules at same site
m1 = types.ModuleType("m1")
m1.f = abs
m2 = types.ModuleType("m2")
def add_five(x):
    return x + 5
m2.f = add_five

for _ in range(25):
    assert call_mod_fn(m1, 10) == 45
    assert call_mod_fn(m2, 10) == 95

# Monkey-patching mid-loop invalidates ICKindModuleAttr via VersionTag
for _ in range(25):
    mp = types.ModuleType("mp")
    mp.f = abs
    res = call_mod_monkeypatch(mp)
    expected = sum(range(20)) + sum(i * 10 for i in range(20, 40))
    assert res == expected

# Short-lived ModuleType objects in loop (verifies no ABA aliasing)
for _ in range(25):
    m_tmp = types.ModuleType("tmp")
    m_tmp.f = abs
    assert call_mod_fn(m_tmp, 5) == 10
    m_tmp2 = types.ModuleType("tmp2")
    m_tmp2.f = lambda x: x + 1
    assert call_mod_fn(m_tmp2, 5) == 15

# Exception propagation from quickened module builtin
for _ in range(25):
    assert call_math_exc([4, -1, 5, -2]) == 2
`
	runRegTestScript(t, src, "call_math_loop", "call_mod_fn", "call_mod_monkeypatch", "call_math_exc")
}

func TestRegLoopGlobalsAndClosures(t *testing.T) {
	src := `
COUNTER = 0

def read_counter():
    return COUNTER

def mutate_global_and_call_closures(n):
    global COUNTER
    COUNTER = 0
    offset = 3
    cell_acc = 0

    # Loop-free direct-capture closure (DirectCaptureCount > 0)
    def add_offset(v):
        return v + offset

    # Loop-free cell-backed closure (nonlocal)
    def bump_cell(v):
        nonlocal cell_acc
        cell_acc += v
        return cell_acc

    total = 0
    for i in range(n):
        total += add_offset(i)
        COUNTER += 1
        bump_cell(1)
    return total, COUNTER, read_counter(), cell_acc

for _ in range(25):
    total, c1, c2, ca = mutate_global_and_call_closures(50)
    assert total == sum(i + 3 for i in range(50))
    assert c1 == 50
    assert c2 == 50
    assert ca == 50
`
	runRegTestScript(t, src)
}

func TestRegLoopIterCallConcurrentRace(t *testing.T) {
	src := `
import math

G = 0

def work(n):
    global G
    xs = list(range(n))
    ys = list(range(n, 2 * n))
    acc = 0
    def inc(v):
        return v + 1
    for i, x in enumerate(xs):
        acc += i + inc(x)
        G += 1
    for a, b in zip(xs, ys):
        acc += a + b
    facc = 0.0
    for i in range(10):
        facc += math.sin(i * 0.1)
    return acc

for _ in range(25):
    assert work(30) == sum(i + (i + 1) for i in range(30)) + sum(i + (i + 30) for i in range(30))
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 4)
	for range 4 {
		wg.Go(func() {
			v := vm.NewVM()
			v.RunCode(co)
			if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
				errs <- exc.String()
			}
		})
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("concurrent execution failed: %s", msg)
	}
}
