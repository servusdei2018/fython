package vm_test

import (
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestYieldFromDelegationDepthsAndConsumers(t *testing.T) {
	runPySource(t, `
def leaf(n):
    for i in range(n):
        yield i

def build_chain(depth, n):
    g = leaf(n)
    for _ in range(depth):
        def wrap(sub):
            yield from sub
        g = wrap(g)
    return g

for depth in (1, 2, 4, 8, 16, 64):
    # for-loop consumer
    s = 0
    for x in build_chain(depth, 50):
        s += x
    assert s == 1225, f"for depth={depth}: {s}"

    # next() consumer
    g = build_chain(depth, 10)
    out = []
    while True:
        try:
            out.append(next(g))
        except StopIteration:
            break
    assert out == list(range(10)), f"next depth={depth}: {out}"

    # list() consumer
    assert list(build_chain(depth, 20)) == list(range(20)), f"list depth={depth}"

# str.join() consumer across depths
def str_leaf(n):
    for i in range(n):
        yield f"v{i}"

def build_str_chain(depth, n):
    g = str_leaf(n)
    for _ in range(depth):
        def wrap(sub):
            yield from sub
        g = wrap(g)
    return g

expected_join = ",".join([f"v{i}" for i in range(12)])
for depth in (1, 2, 4, 8, 16, 64):
    got = ",".join(build_str_chain(depth, 12))
    assert got == expected_join, f"join depth={depth}: {got}"

# Partial next() followed by str.join() and list()
g_partial = build_str_chain(8, 6)
assert next(g_partial) == "v0"
assert next(g_partial) == "v1"
assert "-".join(g_partial) == "v2-v3-v4-v5"

g_partial2 = build_chain(8, 6)
assert next(g_partial2) == 0
assert list(g_partial2) == [1, 2, 3, 4, 5]
`)
}

func TestYieldFromDelegationSendReturnAndPrePrimed(t *testing.T) {
	runPySource(t, `
# 1. Bidirectional send() and return propagation across multiple yield-from segments
def accum(tag):
    total = 0
    for _ in range(3):
        v = yield (tag, total)
        total += v
    return (tag, total)

def mid(tag):
    r = yield from accum(tag)
    return ("mid", r)

def outer():
    r1 = yield from mid("a")
    r2 = yield from mid("b")
    return (r1, r2)

g = outer()
# Fresh generator rejects non-None send()
try:
    g.send(123)
    assert False, "expected TypeError"
except TypeError as e:
    assert "just-started" in str(e)

assert next(g) == ("a", 0)
assert g.send(10) == ("a", 10)
assert g.send(20) == ("a", 30)
assert g.send(5) == ("b", 0)
assert g.send(7) == ("b", 7)
assert g.send(8) == ("b", 15)
try:
    g.send(9)
    assert False, "expected StopIteration"
except StopIteration as si:
    assert si.value == (("mid", ("a", 35)), ("mid", ("b", 24))), si.value

# 2. Initial yield from entry into a pre-primed (GenSuspended) sub-generator
# Per PEP 380, entering yield from sub for the first time always sends None,
# never forwarding the outer generator's previous LastSent value.
def pre_primed_leaf():
    x = yield "prime"
    y = yield ("got", x)
    return ("done", y)

def pre_primed_mid(sub):
    r = yield from sub
    return r

def delegator(sub):
    first = yield "before"
    r = yield from sub
    return (first, r)

sub_leaf = pre_primed_leaf()
assert next(sub_leaf) == "prime"
sub_mid = pre_primed_mid(sub_leaf)
# Prime sub_mid so sub_mid is suspended delegating to pre-primed sub_leaf?
# First test direct pre-primed leaf:
d1 = delegator(sub_leaf)
assert next(d1) == "before"
# Send non-None "outer_val" to d1: d1 receives "outer_val" as first, then enters
# yield from sub_leaf for the first time -> must send None to sub_leaf!
assert d1.send("outer_val") == ("got", None)
try:
    d1.send("final_val")
    assert False
except StopIteration as si:
    assert si.value == ("outer_val", ("done", "final_val")), si.value

# Now test pre-primed nested chain (sub_mid already suspended at yield from sub_leaf2):
def pre_primed_leaf2():
    a = yield "p0"
    b = yield ("p1", a)
    c = yield ("p2", b)
    return c

sub_leaf2 = pre_primed_leaf2()
sub_mid2 = pre_primed_mid(sub_leaf2)
assert next(sub_mid2) == "p0"
d2 = delegator(sub_mid2)
assert next(d2) == "before"
assert d2.send("outer_v2") == ("p1", None)
assert d2.send("mid_v2") == ("p2", "mid_v2")
try:
    d2.send("end_v2")
    assert False
except StopIteration as si:
    assert si.value == ("outer_v2", "end_v2"), si.value

# 3. Nested collapsed chains (a generator inside a yield-from chain iterating another yield-from chain)
def inner_leaf(k):
    for i in range(k):
        yield i * 2

def inner_wrap(k):
    yield from inner_leaf(k)

def outer_leaf(k):
    for x in inner_wrap(k):
        yield x + 1

def outer_wrap(k):
    yield from outer_leaf(k)

assert list(outer_wrap(5)) == [1, 3, 5, 7, 9]
`)
}

func TestYieldFromDelegationThrowAndTracebacks(t *testing.T) {
	runPySource(t, `
import traceback

# 1. throw() caught at leaf
def leaf_catch():
    try:
        yield 1
    except ValueError as e:
        yield ("caught_leaf", str(e))
    yield 2

def mid_pass(sub):
    yield from sub

g = mid_pass(mid_pass(leaf_catch()))
assert next(g) == 1
assert g.throw(ValueError("leaf_err")) == ("caught_leaf", "leaf_err")
assert next(g) == 2
assert list(g) == []

# 2. throw() uncaught at leaf, caught at mid-chain, with inner-to-outer finally order
events = []
def l3():
    try:
        yield "ready"
    finally:
        events.append("l3_finally")

def l2():
    try:
        yield from l3()
    finally:
        events.append("l2_finally")

def l1():
    try:
        yield from l2()
    except KeyError as e:
        events.append("l1_caught")
        yield ("recovered", e.args[0])
    finally:
        events.append("l1_finally")

def l0():
    try:
        yield from l1()
    finally:
        events.append("l0_finally")

g2 = l0()
assert next(g2) == "ready"
assert g2.throw(KeyError("k")) == ("recovered", "k")
assert events == ["l3_finally", "l2_finally", "l1_caught"], events
assert list(g2) == []
assert events == ["l3_finally", "l2_finally", "l1_caught", "l1_finally", "l0_finally"], events

# 3. Uncaught exception at depth 4: verify exact inner-to-outer finally order AND
# exact traceback function-name order (outer -> g0 -> g1 -> g2 -> g3)
tb_events = []
def g3():
    try:
        yield "ok"
        raise ValueError("boom_depth4")
    finally:
        tb_events.append("g3")

def g2():
    try:
        yield from g3()
    finally:
        tb_events.append("g2")

def g1():
    try:
        yield from g2()
    finally:
        tb_events.append("g1")

def g0():
    try:
        yield from g1()
    finally:
        tb_events.append("g0")

def outer():
    gen = g0()
    assert next(gen) == "ok"
    next(gen)

try:
    outer()
    assert False, "expected ValueError"
except ValueError:
    tb_text = traceback.format_exc()
    assert tb_events == ["g3", "g2", "g1", "g0"], tb_events
    names = ["in outer", "in g0", "in g1", "in g2", "in g3"]
    positions = [tb_text.find(name) for name in names]
    for name, pos in zip(names, positions):
        assert pos != -1, f"missing {name} in traceback:\n{tb_text}"
    assert positions == sorted(positions), f"wrong traceback order {positions} in:\n{tb_text}"

# 4. Uncaught throw() at depth 4: verify traceback includes every intermediate yield from frame
def t3():
    yield "t_ready"

def t2():
    yield from t3()

def t1():
    yield from t2()

def t0():
    yield from t1()

def outer_throw():
    gen = t0()
    assert next(gen) == "t_ready"
    gen.throw(RuntimeError("thrown_depth4"))

try:
    outer_throw()
    assert False, "expected RuntimeError"
except RuntimeError:
    tb_text = traceback.format_exc()
    names = ["in outer_throw", "in t0", "in t1", "in t2", "in t3"]
    positions = [tb_text.find(name) for name in names]
    for name, pos in zip(names, positions):
        assert pos != -1, f"missing {name} in throw traceback:\n{tb_text}"
    assert positions == sorted(positions), f"wrong throw traceback order {positions} in:\n{tb_text}"
`)
}

func TestYieldFromDelegationCloseSemantics(t *testing.T) {
	runPySource(t, `
# 1. Normal close() runs finally blocks innermost-to-outermost
order = []
def c3():
    try:
        yield 1
    finally:
        order.append("c3")

def c2():
    try:
        yield from c3()
    finally:
        order.append("c2")

def c1():
    try:
        yield from c2()
    finally:
        order.append("c1")

def c0():
    try:
        yield from c1()
    finally:
        order.append("c0")

g = c0()
assert next(g) == 1
g.close()
assert order == ["c3", "c2", "c1", "c0"], order

# 2. Leaf catches GeneratorExit and returns cleanly -> outer still receives GeneratorExit
order2 = []
def ret_leaf():
    try:
        yield 1
    except GeneratorExit:
        order2.append("leaf_ret")
        return "ignored"

def ret_outer():
    try:
        yield from ret_leaf()
    finally:
        order2.append("outer_finally")

g2 = ret_outer()
assert next(g2) == 1
g2.close()
assert order2 == ["leaf_ret", "outer_finally"], order2

# 3. Leaf yields during GeneratorExit -> RuntimeError propagated through outer finally
order3 = []
def bad_leaf():
    try:
        yield 1
    except GeneratorExit:
        order3.append("bad_leaf")
        yield 2

def bad_mid():
    try:
        yield from bad_leaf()
    finally:
        order3.append("bad_mid_finally")

def bad_outer():
    try:
        yield from bad_mid()
    finally:
        order3.append("bad_outer_finally")

g3 = bad_outer()
assert next(g3) == 1
try:
    g3.close()
    assert False, "expected RuntimeError"
except RuntimeError as e:
    assert "ignored GeneratorExit" in str(e), str(e)
assert order3 == ["bad_leaf", "bad_mid_finally", "bad_outer_finally"], order3

# 4. Mid-chain yields during GeneratorExit -> RuntimeError propagated to outer
order4 = []
def ok_leaf():
    try:
        yield 1
    finally:
        order4.append("ok_leaf")

def bad_mid2():
    try:
        yield from ok_leaf()
    except GeneratorExit:
        order4.append("bad_mid2")
        yield 99

def outer4():
    try:
        yield from bad_mid2()
    finally:
        order4.append("outer4_finally")

g4 = outer4()
assert next(g4) == 1
try:
    g4.close()
    assert False, "expected RuntimeError"
except RuntimeError as e:
    assert "ignored GeneratorExit" in str(e), str(e)
assert order4 == ["ok_leaf", "bad_mid2", "outer4_finally"], order4

# 5. Inner finally raises ValueError during close() -> propagates to parent except/finally
order5 = []
def err_leaf():
    try:
        yield 1
    finally:
        order5.append("err_leaf_finally")
        raise ValueError("cleanup_failed")

def err_mid():
    try:
        yield from err_leaf()
    finally:
        order5.append("err_mid_finally")

def err_outer():
    try:
        yield from err_mid()
    finally:
        order5.append("err_outer_finally")

g5 = err_outer()
assert next(g5) == 1
try:
    g5.close()
    assert False, "expected ValueError"
except ValueError as e:
    assert str(e) == "cleanup_failed", str(e)
assert order5 == ["err_leaf_finally", "err_mid_finally", "err_outer_finally"], order5

# 6. Inner finally raises ValueError during close(), mid catches it and returns ->
# outer still receives GeneratorExit and runs its finally
order6 = []
def err_leaf2():
    try:
        yield 1
    finally:
        order6.append("leaf2_raise")
        raise ValueError("inner_boom")

def catch_mid():
    try:
        yield from err_leaf2()
    except ValueError:
        order6.append("mid_caught_ve")
        return

def outer6():
    try:
        yield from catch_mid()
    finally:
        order6.append("outer6_finally")

g6 = outer6()
assert next(g6) == 1
g6.close()
assert order6 == ["leaf2_raise", "mid_caught_ve", "outer6_finally"], order6
`)
}

func TestYieldFromDelegationReentrancyIntrospectionAndExternalMutation(t *testing.T) {
	runPySource(t, `
# 1. Re-entrancy and gi_running / gi_yieldfrom inside collapsed chain
refs = {}
seen_running = []

def leaf():
    seen_running.append((refs["g0"].gi_running, refs["g1"].gi_running, refs["g2"].gi_running))
    # Re-entering root, mid-chain, and leaf must all raise ValueError
    for name in ("g0", "g1", "g2"):
        try:
            next(refs[name])
            assert False, f"re-entering {name} should fail"
        except ValueError as e:
            assert "already executing" in str(e), f"{name}: {e}"
    yield 1
    seen_running.append((refs["g0"].gi_running, refs["g1"].gi_running, refs["g2"].gi_running))
    yield 2

def mid(sub):
    yield from sub

def root(sub):
    yield from sub

g2 = leaf()
g1 = mid(g2)
g0 = root(g1)
refs["g0"] = g0
refs["g1"] = g1
refs["g2"] = g2

assert g0.gi_running is False and g1.gi_running is False and g2.gi_running is False
assert g0.gi_yieldfrom is None and g1.gi_yieldfrom is None and g2.gi_yieldfrom is None

assert next(g0) == 1
assert seen_running == [(True, True, True)], seen_running
assert g0.gi_running is False and g1.gi_running is False and g2.gi_running is False
assert g0.gi_yieldfrom is g1
assert g1.gi_yieldfrom is g2
assert g2.gi_yieldfrom is None

# 2. Direct next(g2) while g0 -> g1 -> g2 is suspended:
# Only g2.gi_running is True; g0.gi_running and g1.gi_running must be False!
direct_seen = []
def leaf_direct():
    yield "first"
    direct_seen.append((refs2["g0"].gi_running, refs2["g1"].gi_running, refs2["g2"].gi_running))
    yield "second"

refs2 = {}
dg2 = leaf_direct()
dg1 = mid(dg2)
dg0 = root(dg1)
refs2["g0"] = dg0
refs2["g1"] = dg1
refs2["g2"] = dg2

assert next(dg0) == "first"
assert next(dg2) == "second"
assert direct_seen == [(False, False, True)], direct_seen

# 3. External close() of leaf while outer is suspended (ABA-safe cache invalidation)
def ext_leaf():
    yield 10
    yield 20
    return 99

def ext_mid(sub):
    r = yield from sub
    yield ("after_mid", r)

def ext_outer(sub):
    yield from sub

el = ext_leaf()
em = ext_mid(el)
eo = ext_outer(em)
assert next(eo) == 10
el.close()
assert next(eo) == ("after_mid", None)
assert list(eo) == []
`)
}

func TestYieldFromDelegationCustomIteratorAndNonGen(t *testing.T) {
	runPySource(t, `
# Custom iterator implementing send/throw/close + StopIteration(value)
log = []

class CustomSubIter:
    def __init__(self):
        self.step = 0
    def __iter__(self):
        return self
    def __next__(self):
        log.append(("next", self.step))
        if self.step == 0:
            self.step = 1
            return "c0"
        raise StopIteration("custom_ret")
    def send(self, v):
        log.append(("send", v))
        if v == "stop":
            raise StopIteration("sent_ret")
        return ("echo", v)
    def throw(self, exc):
        log.append(("throw", type(exc).__name__))
        if isinstance(exc, ValueError):
            return "caught_in_custom"
        raise exc
    def close(self):
        log.append(("close",))

def wrap_custom(it):
    r = yield from it
    return ("wrapped", r)

def outer_custom(it):
    r = yield from wrap_custom(it)
    return ("outer", r)

it = CustomSubIter()
g = outer_custom(it)
assert next(g) == "c0"
assert g.send("hi") == ("echo", "hi")
assert g.throw(ValueError("v")) == "caught_in_custom"
try:
    g.send("stop")
    assert False
except StopIteration as si:
    assert si.value == ("outer", ("wrapped", "sent_ret")), si.value

# Custom close() forwarded through yield-from chain
log2 = []
class CloseableIter:
    def __iter__(self):
        return self
    def __next__(self):
        return "item"
    def close(self):
        log2.append("custom_closed")

def wrap_closeable(it):
    try:
        yield from it
    finally:
        log2.append("wrap_finally")

def outer_closeable(it):
    try:
        yield from wrap_closeable(it)
    finally:
        log2.append("outer_finally")

gc = outer_closeable(CloseableIter())
assert next(gc) == "item"
gc.close()
assert log2 == ["custom_closed", "wrap_finally", "outer_finally"], log2

# Built-in non-generator iterators (range, list) inside multi-level yield from
def range_leaf(n):
    yield from range(n)

def range_outer(n):
    yield from range_leaf(n)

assert sum(range_outer(100)) == 4950
`)
}

func TestYieldFromDelegationYieldTupleAndGCHygiene(t *testing.T) {
	prevMT := val.IsMultiThreaded()
	val.SetMultiThreaded(false)
	defer val.SetMultiThreaded(prevMT)

	_, v := runPySource(t, `
def make_pair_helper(i):
    return (i, i * 10)

def pairs_leaf(n):
    for i in range(n):
        yield (i, i * 10)

def pairs_helper_leaf(n):
    for i in range(n):
        p = make_pair_helper(i)
        yield p

def wrap1(sub):
    yield from sub

def wrap2(sub):
    yield from wrap1(sub)

# 1. Unpack across collapsed yield from
acc = []
for a, b in wrap2(pairs_leaf(50)):
    acc.append((a, b))
assert acc == [(i, i * 10) for i in range(50)]

# 2. Helper function called inside collapsed leaf must not alias consumer YieldTuple
kept = []
for a, b in wrap2(pairs_helper_leaf(20)):
    kept.append((a, b))
assert kept == [(i, i * 10) for i in range(20)]

# 3. Early break + resume with next()
g_break = wrap2(pairs_leaf(10))
for a, b in g_break:
    if a == 3:
        break
assert next(g_break) == (4, 40)
g_break.close()

# 4. Retain finished/closed generators for Go-side GC hygiene verification
g_done_leaf = pairs_leaf(5)
g_done_outer = wrap2(g_done_leaf)
assert sum(a + b for a, b in g_done_outer) == sum(i + i * 10 for i in range(5))

g_err_leaf = pairs_leaf(5)
g_err_outer = wrap2(g_err_leaf)
assert next(g_err_outer) == (0, 0)
try:
    g_err_outer.throw(RuntimeError("boom"))
except RuntimeError:
    pass
`)

	for _, name := range []string{"g_break", "g_done_leaf", "g_done_outer", "g_err_leaf", "g_err_outer"} {
		gv, ok := v.Globals().Get(val.InternString(name))
		if !ok || !gv.IsGenerator() {
			t.Fatalf("expected generator global %s", name)
		}
		g := gv.AsGenerator()
		if g.SubGen != nil {
			t.Fatalf("closed generator %s still retains SubGen", name)
		}
		if g.LeafGen != nil {
			t.Fatalf("closed generator %s still retains LeafGen", name)
		}
		if g.YieldTuple != nil {
			t.Fatalf("closed generator %s still retains YieldTuple", name)
		}
		if g.YieldFrom.HeaderPtr() != nil {
			t.Fatalf("closed generator %s still retains YieldFrom", name)
		}
	}
}

func TestYieldFromDelegationConcurrentRace(t *testing.T) {
	const src = `
def leaf(n):
    for i in range(n):
        yield i

def wrap(sub):
    yield from sub

def run(depth, n):
    g = leaf(n)
    for _ in range(depth):
        g = wrap(g)
    s = 0
    for x in g:
        s += x
    return s

assert run(8, 200) == 19900
`
	eng := vm.NewEngine()
	eng.SetCompileHook(compiler.CompileSource)
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	prevMT := val.IsMultiThreaded()
	val.SetMultiThreaded(true)
	defer val.SetMultiThreaded(prevMT)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			v := eng.AcquireVM()
			defer eng.ReleaseVM(v)
			v.RunCode(co)
			exc := v.UnhandledException()
			if !exc.IsNone() && exc.HeaderPtr() != nil {
				t.Errorf("python exception: %s", v.FormatException(exc))
			}
		})
	}
	wg.Wait()
}
