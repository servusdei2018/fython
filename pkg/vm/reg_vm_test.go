package vm_test

import (
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func assertRegPromoted(t *testing.T, v *vm.VM, names ...string) {
	t.Helper()
	for _, name := range names {
		fnVal, ok := v.Globals().Get(val.InternString(name))
		if !ok || !fnVal.IsFunction() {
			t.Fatalf("expected global function %q, got ok=%v val=%v", name, ok, fnVal)
		}
		co := vm.AsCodeObject(fnVal.AsFunction().Code)
		if !co.HasRegCode() {
			t.Fatalf("expected function %q to be promoted to RegCode", name)
		}
	}
}

func assertNotRegPromoted(t *testing.T, v *vm.VM, names ...string) {
	t.Helper()
	for _, name := range names {
		fnVal, ok := v.Globals().Get(val.InternString(name))
		if !ok || !fnVal.IsFunction() {
			t.Fatalf("expected global function %q, got ok=%v val=%v", name, ok, fnVal)
		}
		co := vm.AsCodeObject(fnVal.AsFunction().Code)
		if co.HasRegCode() {
			t.Fatalf("expected function %q to remain in Tier-1 (not promoted to RegCode)", name)
		}
	}
}

func TestRegVMLocalClobberAndUnboundLocal(t *testing.T) {
	eng, v := runPySource(t, `
def walrus_clobber(x):
    acc = 0
    for _ in range(1):
        y = x + (x := 2)
        acc = y
    return (x, acc)

def swap_locals(a, b):
    for _ in range(1):
        a, b = b, a
    return (a, b)

def arg_eval_clobber(x):
    def pair(u, v):
        return (u, v)
    res = None
    for _ in range(1):
        res = pair(x, (x := x + 10))
    return res

def conditional_unbound(flag):
    for _ in range(1):
        if flag:
            z = 42
    return z

def deleted_local(x):
    acc = 0
    for i in range(2):
        acc = acc + i + 1
    if acc > 0:
        del x
    return x

# Warm past promotion threshold (16 calls) and verify Tier-1 vs Tier-2 equivalence.
# Dispatch conditional_unbound/deleted_local through a list so Tier-1 inlining does not bypass RegCode entry.
fns = [conditional_unbound, deleted_local]
for i in range(40):
    assert walrus_clobber(1) == (2, 3), f"walrus_clobber failed at iter {i}"
    assert walrus_clobber(10) == (2, 12), f"walrus_clobber(10) failed at iter {i}"
    assert swap_locals(i, i + 1) == (i + 1, i), f"swap_locals failed at iter {i}"
    assert arg_eval_clobber(5) == (5, 15), f"arg_eval_clobber failed at iter {i}"
    assert fns[0](True) == 42

    try:
        fns[1](99)
    except UnboundLocalError:
        pass

    if i >= 20:
        caught_cond = False
        try:
            fns[0](False)
        except UnboundLocalError:
            caught_cond = True
        assert caught_cond, f"expected UnboundLocalError for conditional_unbound at iter {i}"

        caught_del = False
        try:
            fns[1](99)
        except UnboundLocalError:
            caught_del = True
        assert caught_del, f"expected UnboundLocalError for deleted_local at iter {i}"
`)
	defer eng.ReleaseVM(v)
	assertRegPromoted(t, v, "walrus_clobber", "swap_locals", "arg_eval_clobber", "conditional_unbound", "deleted_local")
}

func TestRegVMExceptionsAndMixedTracebackLines(t *testing.T) {
	eng, v := runPySource(t, `
import sys

def reg_nested_try(mode):
    log = []
    try:
        try:
            if mode == 1:
                raise ValueError("v1")
            elif mode == 2:
                raise TypeError("t2")
            elif mode == 3:
                raise KeyError("k3")
        except ValueError as e:
            log.append(("val", str(e)))
        else:
            log.append(("else", mode))
        finally:
            log.append(("inner_fin", mode))
    except (TypeError, KeyError) as e:
        log.append(("outer", type(e).__name__, str(e)))
    finally:
        log.append(("outer_fin", mode))
    return log

for i in range(30):
    r0 = reg_nested_try(0)
    assert r0 == [("else", 0), ("inner_fin", 0), ("outer_fin", 0)], f"mode 0 iter {i}: {r0}"
    r1 = reg_nested_try(1)
    assert r1 == [("val", "v1"), ("inner_fin", 1), ("outer_fin", 1)], f"mode 1 iter {i}: {r1}"
    r2 = reg_nested_try(2)
    assert r2 == [("inner_fin", 2), ("outer", "TypeError", "t2"), ("outer_fin", 2)], f"mode 2 iter {i}: {r2}"
    r3 = reg_nested_try(3)
    assert r3 == [("inner_fin", 3), ("outer", "KeyError", "k3"), ("outer_fin", 3)], f"mode 3 iter {i}: {r3}"

# Bare reraise and raise ... from ...
def reg_reraise(x):
    try:
        if x < 0:
            raise ValueError("neg")
    except ValueError:
        raise

def reg_raise_from(x):
    try:
        raise ValueError("root")
    except ValueError as e:
        raise RuntimeError("wrap") from e

for i in range(25):
    try:
        reg_reraise(-1)
        assert False
    except ValueError as e:
        assert str(e) == "neg"
    try:
        reg_raise_from(i)
        assert False
    except RuntimeError as e:
        assert str(e) == "wrap"
        assert isinstance(e.__cause__, ValueError)

# Mixed Tier-1 -> Reg -> Tier-1 -> Reg exact tb_lineno verification.
# Keep tier1_outer / tier1_mid as generators (always Tier-1) and reg_inner / reg_leaf promoted (>16 calls).
def reg_leaf(boom):
    for _ in range(1):
        if boom:
            raise ValueError("boom")  # LINE_LEAF
    return 1

def tier1_mid(boom):
    yield reg_leaf(boom)  # LINE_MID

def reg_caller(boom):
    res = 0
    for _ in range(1):
        res = next(tier1_mid(boom))  # LINE_REG_CALLER
    return res

def tier1_outer(boom):
    yield reg_caller(boom)  # LINE_OUTER

# Warm reg_leaf and reg_caller past RegCode promotion threshold.
for _ in range(25):
    assert reg_leaf(False) == 1
    assert reg_caller(False) == 1

# Capture exact line numbers from __code__.co_firstlineno offsets.
line_outer = tier1_outer.__code__.co_firstlineno + 1
line_reg_caller = reg_caller.__code__.co_firstlineno + 3
line_mid = tier1_mid.__code__.co_firstlineno + 1
line_leaf = reg_leaf.__code__.co_firstlineno + 3

try:
    next(tier1_outer(True))
    assert False, "expected ValueError"
except ValueError as exc:
    tb = exc.__traceback__
    frames = []
    while tb is not None:
        frames.append((tb.tb_frame.f_code.co_name, tb.tb_lineno))
        tb = tb.tb_next
    # Extract the 4-frame mixed chain (excluding module frame).
    chain = [(name, ln) for (name, ln) in frames if name in ("tier1_outer", "reg_caller", "tier1_mid", "reg_leaf")]
    expected = [
        ("tier1_outer", line_outer),
        ("reg_caller", line_reg_caller),
        ("tier1_mid", line_mid),
        ("reg_leaf", line_leaf),
    ]
    assert chain == expected, f"traceback chain mismatch: got {chain}, want {expected}"
`)
	defer eng.ReleaseVM(v)
	assertRegPromoted(t, v, "reg_leaf", "reg_caller")
}

func TestRegVMAttributeICsSlotsAndProperties(t *testing.T) {
	eng, v := runPySource(t, `
class Slotted:
    __slots__ = ("x", "y")
    def __init__(self, x, y):
        self.x = x
        self.y = y
    def bump(self, dx, dy):
        self.x = self.x + dx
        self.y = self.y + dy
        return self.x + self.y

class Dynamic:
    def __init__(self, a):
        self.a = a
    def add_b(self, b):
        self.b = b
        return self.a + self.b

class WithProp:
    def __init__(self, val):
        self._val = val
    @property
    def val(self):
        return self._val * 2
    @val.setter
    def val(self, v):
        self._val = v + 1

def exercise_slotted(obj, n):
    total = 0
    for i in range(n):
        obj.x = i
        obj.y = i * 2
        total += obj.x + obj.y
    return total

def exercise_dynamic(obj, n):
    total = 0
    for i in range(n):
        obj.a = i
        obj.b = i + 1
        total += obj.a + obj.b
    return total

def exercise_prop(obj, n):
    total = 0
    for i in range(n):
        obj.val = i
        total += obj.val
    return total

s = Slotted(0, 0)
d = Dynamic(0)
p = WithProp(0)

for _ in range(30):
    assert exercise_slotted(s, 10) == sum(3 * i for i in range(10))
    assert s.bump(1, 2) == s.x + s.y
    d2 = Dynamic(1)
    assert d2.add_b(5) == 6
    assert exercise_dynamic(d, 10) == sum(2 * i + 1 for i in range(10))
    assert exercise_prop(p, 10) == sum(2 * (i + 1) for i in range(10))
`)
	defer eng.ReleaseVM(v)
	assertRegPromoted(t, v, "exercise_slotted", "exercise_dynamic", "exercise_prop")
}

func TestRegVMComprehensionsAndMixedGenUnpack(t *testing.T) {
	eng, v := runPySource(t, `
def pair_gen(n):
    for i in range(n):
        yield (i, i * 2)

def run_comps(n):
    lc = [i * i for i in range(n)]
    lcf = [i for i in range(n) if i % 2 == 0]
    dc = {i: i + 1 for i in range(n)}
    dcf = {i: i * 3 for i in range(n) if i % 3 == 0}
    sc = {i % 7 for i in range(n)}
    scf = {i for i in range(n) if i % 2 == 1}
    # Reg caller iterating Tier-1 generator with 2-tuple unpack (exercises frameFlagReg guard).
    gsum = 0
    for a, b in pair_gen(n):
        gsum += a + b
    return (len(lc), len(lcf), len(dc), len(dcf), len(sc), len(scf), gsum)

for _ in range(30):
    assert run_comps(20) == (20, 10, 20, 7, 7, 10, sum(3 * i for i in range(20)))
`)
	defer eng.ReleaseVM(v)
	assertRegPromoted(t, v, "run_comps")
}

func TestRegVMClosuresRecursionAndFrameInspection(t *testing.T) {
	eng, v := runPySource(t, `
import sys

def make_counter(start):
    n = start
    def inc(step):
        nonlocal n
        for _ in range(1):
            n = n + step
        return n
    return inc

def make_direct(k):
    def add_k(x):
        res = 0
        for _ in range(1):
            res = x + k
        return res
    return add_k

def fact(n):
    for _ in range(1):
        if n <= 1:
            return 1
    return n * fact(n - 1)

def infinite(n):
    for _ in range(1):
        pass
    return 1 + infinite(n + 1)

def inspect_locals_and_frame(a, b):
    c = a + b
    for _ in range(1):
        f = sys._getframe(0)
        assert f.f_code.co_name == "inspect_locals_and_frame"
        assert f.f_lineno == inspect_locals_and_frame.__code__.co_firstlineno + 3
        loc = locals()
        assert loc["a"] == a and loc["b"] == b and loc["c"] == c
    return c

ctr = make_counter(10)
add5 = make_direct(5)

for i in range(30):
    assert ctr(2) == 12 + 2 * i
    assert add5(i) == i + 5
    assert fact(6) == 720
    assert inspect_locals_and_frame(i, i + 1) == 2 * i + 1

caught_rec = False
try:
    infinite(0)
except RecursionError:
    caught_rec = True
assert caught_rec, "expected RecursionError"
`)
	defer eng.ReleaseVM(v)
	assertRegPromoted(t, v, "fact", "infinite", "inspect_locals_and_frame")
}

func TestRegVMConcurrentRace(t *testing.T) {
	src := `
class Box:
    __slots__ = ("v",)
    def __init__(self, v):
        self.v = v

def work(n):
    b = Box(0)
    total = 0
    for i in range(n):
        try:
            b.v = i
            if i == 7:
                raise ValueError("seven")
            total += b.v
        except ValueError:
            total += 100
    d = {k: k * 2 for k in range(10) if k % 2 == 0}
    return total + len(d)

res = 0
for _ in range(30):
    res = work(15)
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	co.Init()
	val.SetMultiThreaded(true)
	defer val.SetMultiThreaded(false)

	const expected = int64(105 - 7 + 100 + 5)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			v := vm.NewVM()
			v.RunCode(co)
			if !v.UnhandledException().IsNone() && v.UnhandledException().HeaderPtr() != nil {
				t.Errorf("unhandled exception: %v", v.UnhandledException())
				return
			}
			got, ok := v.Globals().Get(val.InternString("res"))
			if !ok || !got.IsInt64() || got.AsInt64() != expected {
				t.Errorf("res = %v (ok=%v), want %d", got, ok, expected)
			}
		})
	}
	wg.Wait()
}

func TestRegVMStaticPromotionCostModel(t *testing.T) {
	eng, v := runPySource(t, `
def short_add(a, b):
    return a + b

def cold_heavy_builder(a, b, c):
    t1 = (a, b)
    t2 = [b, c]
    t3 = {a: b, b: c}
    return (t1, t2, t3)

def call_ex_forwarder(fn, args, kw):
    return fn(*args, **kw)

def map_unpack_glue(d1, d2):
    return {**d1, **d2}

def looped_sum(n):
    s = 0
    for i in range(n):
        s += i
    return s

def unpack_and_range(pairs, n):
    total = 0
    for i in range(-2, n):
        total += i
    for a, b in pairs:
        a, b = b, a
        total += a - b
    return total

def helper_target(x, y=1):
    return x + y

fns = [short_add, cold_heavy_builder, call_ex_forwarder, map_unpack_glue]
for i in range(30):
    assert fns[0](i, 1) == i + 1
    assert len(fns[1](i, i + 1, i + 2)) == 3
    assert fns[2](helper_target, (i,), {"y": 2}) == i + 2
    assert fns[3]({"a": i}, {"b": 2}) == {"a": i, "b": 2}
    assert looped_sum(10) == 45
    assert unpack_and_range([(1, 4), (2, 7)], 5) == sum(range(-2, 5)) + (4 - 1) + (7 - 2)
`)
	defer eng.ReleaseVM(v)
	assertNotRegPromoted(t, v, "short_add", "cold_heavy_builder", "call_ex_forwarder", "map_unpack_glue")
	assertRegPromoted(t, v, "looped_sum", "unpack_and_range")
}
