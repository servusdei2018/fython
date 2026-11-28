package vm_test

import (
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runAsyncGenPython(t *testing.T, src string) *vm.VM {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v := vm.NewVM()
	v.RunCode(co)
	return v
}

func TestAsyncGeneratorBasicAndAsyncFor(t *testing.T) {
	src := `
import asyncio

async def count(n):
    for i in range(n):
        yield i

async def main():
    # Basic async for + else on normal exhaustion
    items = []
    else_ran = False
    async for x in count(4):
        items.append(x)
    else:
        else_ran = True
    assert items == [0, 1, 2, 3], items
    assert else_ran is True

    # break must skip else:
    broken = []
    else_on_break = False
    async for x in count(5):
        if x == 2:
            break
        broken.append(x)
    else:
        else_on_break = True
    assert broken == [0, 1], broken
    assert else_on_break is False

    # continue + try/finally
    odds = []
    fin_count = 0
    async for x in count(6):
        try:
            if x % 2 == 0:
                continue
            odds.append(x)
        finally:
            fin_count += 1
    assert odds == [1, 3, 5], odds
    assert fin_count == 6, fin_count

    # TypeError on non-async-iterable
    try:
        async for _ in 42:
            pass
        assert False, "expected TypeError"
    except TypeError as e:
        assert "__aiter__" in str(e), str(e)

    # TypeError when __aiter__ returns object without __anext__
    class BadAIter:
        def __aiter__(self):
            return 123
    try:
        async for _ in BadAIter():
            pass
        assert False, "expected TypeError"
    except TypeError as e:
        assert "__anext__" in str(e), str(e)

    # TypeError when __anext__ returns non-awaitable
    class BadANext:
        def __aiter__(self):
            return self
        def __anext__(self):
            return 42
    try:
        async for _ in BadANext():
            pass
        assert False, "expected TypeError"
    except TypeError as e:
        assert "__anext__" in str(e), str(e)

    return "ok"

res = asyncio.run(main())
assert res == "ok"
`
	runAsyncGenPython(t, src)
}

func TestAsyncGeneratorBuiltinsAndComprehensions(t *testing.T) {
	src := `
import asyncio

async def gen(n):
    for i in range(n):
        await asyncio.sleep(0)
        yield i

async def dbl(x):
    await asyncio.sleep(0)
    return x * 2

class CustomCoroAIter:
    def __init__(self, items):
        self._it = iter(items)
    def __aiter__(self):
        return self
    async def __anext__(self):
        try:
            return next(self._it)
        except StopIteration:
            raise StopAsyncIteration

class CustomAwaitableObj:
    def __init__(self, val, stop=False):
        self.val = val
        self.stop = stop
    def __await__(self):
        yield from ()
        if self.stop:
            raise StopAsyncIteration
        return self.val

class CustomAwaitAIter:
    def __init__(self, items):
        self._it = iter(items)
    def __aiter__(self):
        return self
    def __anext__(self):
        try:
            return CustomAwaitableObj(next(self._it))
        except StopIteration:
            return CustomAwaitableObj(None, stop=True)

async def main():
    # aiter / anext 1-arg and 2-arg
    ag = aiter(gen(2))
    assert await anext(ag) == 0
    assert await anext(ag) == 1
    try:
        await anext(ag)
        assert False, "expected StopAsyncIteration"
    except StopAsyncIteration:
        pass
    assert await anext(ag, "END") == "END"

    # aiter / anext errors
    try:
        aiter(123)
        assert False
    except TypeError:
        pass
    try:
        anext(123)
        assert False
    except TypeError:
        pass

    # anext(it, default) with coroutine-based and custom __await__ async iterators
    c1 = CustomCoroAIter([10, 20])
    assert await anext(c1, -1) == 10
    assert await anext(c1, -1) == 20
    assert await anext(c1, -1) == -1

    c2 = CustomAwaitAIter([30])
    assert await anext(c2, -2) == 30
    assert await anext(c2, -2) == -2

    # anext(it, default) must propagate non-StopAsyncIteration errors
    async def boom():
        yield 1
        raise ValueError("boom")
    bg = boom()
    assert await anext(bg, "D") == 1
    try:
        await anext(bg, "D")
        assert False, "expected ValueError"
    except ValueError as e:
        assert str(e) == "boom"

    # Async list, set, dict comprehensions
    lst = [x * 10 async for x in gen(4) if x % 2 == 1]
    assert lst == [10, 30], lst

    st = {x + 1 async for x in gen(4)}
    assert st == {1, 2, 3, 4}, st

    dct = {x: x * x async for x in gen(4) if x > 0}
    assert dct == {1: 1, 2: 4, 3: 9}, dct

    # Mixed sync/async comprehensions
    mix1 = [x + y async for x in gen(2) for y in [10, 20]]
    assert mix1 == [10, 20, 11, 21], mix1

    mix2 = [x + y for x in [10, 20] async for y in gen(2)]
    assert mix2 == [10, 11, 20, 21], mix2

    # Async generator expressions
    age1 = (x * 3 async for x in gen(4) if x != 2)
    out1 = [v async for v in age1]
    assert out1 == [0, 3, 9], out1

    age2 = (await dbl(x) for x in [1, 2, 3])
    out2 = [v async for v in age2]
    assert out2 == [2, 4, 6], out2

    return "ok"

res = asyncio.run(main())
assert res == "ok"
`
	v := runAsyncGenPython(t, src)
	if r, ok := v.GetGlobal("res"); !ok || !r.IsAnyString() || r.AsString() != "ok" {
		t.Fatalf("expected res == 'ok', got %v", r)
	}
}

func TestAsyncGeneratorInterleavedAwaitAndDescriptors(t *testing.T) {
	src := `
import asyncio

async def helper(x):
    await asyncio.sleep(0)
    return x + 100

async def main():
    q = asyncio.Queue()
    observed = {}

    async def producer():
        observed["in_body_running"] = g.ag_running
        observed["in_body_await"] = g.ag_await
        v1 = await q.get()
        v2 = await helper(v1)
        yield v2
        await asyncio.sleep(0)
        yield v2 + 1

    g = producer()
    assert g.ag_running is False
    assert g.ag_await is None
    assert g.__name__ == "producer"
    assert g.__qualname__.endswith("producer")
    assert g.ag_code.co_name == "producer"
    assert g.ag_frame is not None

    g.__name__ = "renamed"
    assert g.__name__ == "renamed"

    # Start stepping g in a task so we can inspect ag_running & ag_await while suspended on q.get()
    step_task = asyncio.create_task(g.__anext__())
    await asyncio.sleep(0)
    assert g.ag_running is True
    assert g.ag_await is not None

    await q.put(5)
    first = await step_task
    assert first == 105, first
    assert g.ag_running is False
    assert g.ag_await is None
    assert observed["in_body_running"] is True
    assert observed["in_body_await"] is None

    second = await g.__anext__()
    assert second == 106, second
    assert g.ag_running is False
    assert g.ag_await is None

    try:
        await g.__anext__()
        assert False
    except StopAsyncIteration:
        pass
    assert g.ag_running is False
    assert g.ag_await is None
    assert g.ag_frame is None

    return "ok"

res = asyncio.run(main())
assert res == "ok"
`
	v := runAsyncGenPython(t, src)
	if r, ok := v.GetGlobal("res"); !ok || !r.IsAnyString() || r.AsString() != "ok" {
		t.Fatalf("expected res == 'ok', got %v", r)
	}
}

func TestAsyncGeneratorProtocolLazyASendAThrowAClose(t *testing.T) {
	src := `
import asyncio

async def echo():
    x = yield "ready"
    while x is not None:
        try:
            x = yield f"got:{x}"
        except ValueError as e:
            x = yield f"caught:{e}"

async def main():
    # 1. Lazy validation on Created async gen
    g = echo()
    bad_send = g.asend(42)  # must NOT raise yet!
    try:
        await bad_send
        assert False, "expected TypeError"
    except TypeError as e:
        assert "non-None" in str(e), str(e)

    # Re-awaiting already awaited asend -> RuntimeError
    try:
        await bad_send
        assert False, "expected RuntimeError"
    except RuntimeError as e:
        assert "already awaited" in str(e), str(e)

    # g is still Created and usable after failed first asend(42)!
    assert await g.asend(None) == "ready"
    assert await g.asend("a") == "got:a"
    assert await g.athrow(ValueError("oops")) == "caught:oops"
    assert await g.asend("b") == "got:b"

    # Exhaust via asend(None)
    try:
        await g.asend(None)
        assert False, "expected StopAsyncIteration"
    except StopAsyncIteration:
        pass

    # Closed async gen: asend(42) raises StopAsyncIteration (NOT TypeError)
    try:
        await g.asend(42)
        assert False, "expected StopAsyncIteration"
    except StopAsyncIteration:
        pass

    # Closed async gen: athrow(exc) re-raises exc directly (not wrapped even for StopIteration)
    try:
        await g.athrow(StopIteration("raw"))
        assert False, "expected StopIteration"
    except StopIteration as e:
        assert str(e) == "raw"

    # Created async gen: athrow(exc) transitions Created -> Closed without running body, re-raises exc directly
    entered = False
    async def never_run():
        nonlocal entered
        entered = True
        yield 1
    g_fresh = never_run()
    th = g_fresh.athrow(StopAsyncIteration("direct"))  # lazy: must not raise or close yet
    try:
        await th
        assert False, "expected StopAsyncIteration"
    except StopAsyncIteration as e:
        assert str(e) == "direct"
    assert entered is False
    # Now g_fresh is closed:
    try:
        await g_fresh.__anext__()
        assert False
    except StopAsyncIteration:
        pass

    # aclose() on Created transitions to Closed without entering body
    g_created = never_run()
    cl = g_created.aclose()  # lazy
    assert await cl is None
    assert entered is False
    assert await g_created.aclose() is None  # already Closed -> no-op None
    try:
        await cl
        assert False, "expected RuntimeError on reusing aclose()"
    except RuntimeError as e:
        assert "already awaited" in str(e), str(e)

    # aclose() on Suspended with async finally: await asyncio.sleep(0)
    fin_ran = False
    async def with_async_finally():
        nonlocal fin_ran
        try:
            yield 1
            yield 2
        finally:
            await asyncio.sleep(0)
            fin_ran = True

    g_fin = with_async_finally()
    assert await g_fin.__anext__() == 1
    assert await g_fin.aclose() is None
    assert fin_ran is True
    assert g_fin.ag_running is False
    assert g_fin.ag_frame is None

    # aclose() when generator ignores GeneratorExit by yielding -> RuntimeError
    async def ignores_exit():
        try:
            yield 1
        except GeneratorExit:
            yield 2

    g_ign = ignores_exit()
    assert await g_ign.__anext__() == 1
    try:
        await g_ign.aclose()
        assert False, "expected RuntimeError"
    except RuntimeError as e:
        assert "ignored GeneratorExit" in str(e), str(e)

    return "ok"

res = asyncio.run(main())
`
	v := runAsyncGenPython(t, src)
	if r, ok := v.GetGlobal("res"); !ok || !r.IsAnyString() || r.AsString() != "ok" {
		t.Fatalf("expected res == 'ok', got %v", r)
	}
}

func TestAsyncGeneratorPEP479AndSyntaxErrors(t *testing.T) {
	// 1. Runtime PEP 479/525 conversions
	src := `
import asyncio

async def raises_stop_iter():
    yield 1
    raise StopIteration("stop1")

async def raises_stop_async_iter():
    yield 2
    raise StopAsyncIteration("stop2")

async def bare_return_ok():
    yield 10
    return
    yield 20

async def outer_with_nested_sync_yield_from():
    def inner():
        yield from [1, 2, 3]
    for v in inner():
        yield v

async def main():
    g1 = raises_stop_iter()
    assert await g1.__anext__() == 1
    try:
        await g1.__anext__()
        assert False, "expected RuntimeError"
    except RuntimeError as e:
        assert "StopIteration" in str(e), str(e)

    g2 = raises_stop_async_iter()
    assert await g2.__anext__() == 2
    try:
        await g2.__anext__()
        assert False, "expected RuntimeError"
    except RuntimeError as e:
        assert "StopAsyncIteration" in str(e), str(e)

    assert [x async for x in bare_return_ok()] == [10]
    assert [x async for x in outer_with_nested_sync_yield_from()] == [1, 2, 3]
    return "ok"

assert asyncio.run(main()) == "ok"
`
	runAsyncGenPython(t, src)

	// 2. Compile-time SyntaxErrors
	badCases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "yield_from_in_async_def",
			src:     "async def f():\n    yield from [1, 2]\n",
			wantSub: "yield from",
		},
		{
			name:    "return_value_in_async_gen",
			src:     "async def f():\n    yield 1\n    return 2\n",
			wantSub: "return",
		},
		{
			name:    "return_none_in_async_gen",
			src:     "async def f():\n    yield 1\n    return None\n",
			wantSub: "return",
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compiler.CompileString(tc.src)
			if err == nil {
				t.Fatalf("expected SyntaxError for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "SyntaxError") || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

func TestAsyncGeneratorReentrancyAndCancellation(t *testing.T) {
	src := `
import asyncio

async def main():
    # 1. Re-entrancy guard while ag is suspended on an inner await
    q = asyncio.Queue()
    async def wait_gen():
        v = await q.get()
        yield v

    g = wait_gen()
    t1 = asyncio.create_task(g.__anext__())
    await asyncio.sleep(0)
    assert g.ag_running is True

    # Concurrent asend / athrow / aclose must raise RuntimeError without breaking t1
    for op, aw in [("asend", g.asend(None)), ("athrow", g.athrow(ValueError("x"))), ("aclose", g.aclose())]:
        try:
            await aw
            assert False, f"expected RuntimeError for concurrent {op}"
        except RuntimeError as e:
            assert "already running" in str(e), f"{op}: {e}"

    assert g.ag_running is True
    await q.put(99)
    assert await t1 == 99
    assert g.ag_running is False

    # 2. task.cancel() mid-await asyncio.Queue.get() inside async gen
    q2 = asyncio.Queue()
    cancelled_in_gen = False
    finally_awaited = False

    async def cancel_target():
        nonlocal cancelled_in_gen, finally_awaited
        try:
            v = await q2.get()
            yield v
        except asyncio.CancelledError:
            cancelled_in_gen = True
            raise
        finally:
            await asyncio.sleep(0)
            finally_awaited = True

    g2 = cancel_target()
    async def consumer():
        async for x in g2:
            return x

    ct = asyncio.create_task(consumer())
    await asyncio.sleep(0)
    assert g2.ag_running is True
    ct.cancel()
    try:
        await ct
        assert False, "expected CancelledError"
    except asyncio.CancelledError:
        pass
    assert cancelled_in_gen is True
    assert finally_awaited is True
    assert g2.ag_running is False

    # Verify queue waiter was not leaked: a fresh getter receives the next put
    q2.put_nowait("clean")
    assert await q2.get() == "clean"

    # 3. asyncio.wait_for timeout cancellation mid-await inside async gen
    wf_finally = False
    async def slow_gen():
        nonlocal wf_finally
        try:
            await asyncio.sleep(5.0)
            yield 1
        finally:
            wf_finally = True

    g3 = slow_gen()
    try:
        await asyncio.wait_for(g3.__anext__(), timeout=0.01)
        assert False, "expected TimeoutError"
    except (asyncio.TimeoutError, TimeoutError):
        pass
    for _ in range(5):
        if wf_finally:
            break
        await asyncio.sleep(0)
    assert wf_finally is True
    assert g3.ag_running is False

    # 4. Async gen catching CancelledError and yielding a recovery value
    q4 = asyncio.Queue()
    async def recover_gen():
        try:
            v = await q4.get()
            yield v
        except asyncio.CancelledError:
            yield "recovered"

    g4 = recover_gen()
    t4 = asyncio.create_task(g4.__anext__())
    while not g4.ag_running:
        await asyncio.sleep(0)
    t4.cancel()
    assert await t4 == "recovered"
    assert g4.ag_running is False

    return "ok"

res = asyncio.run(main())
`
	v := runAsyncGenPython(t, src)
	if r, ok := v.GetGlobal("res"); !ok || !r.IsAnyString() || r.AsString() != "ok" {
		t.Fatalf("expected res == 'ok', got %v", r)
	}
}

func TestAsyncGeneratorIntrospectionGCHygieneAndFYCRoundTrip(t *testing.T) {
	src := `
import asyncio
import types
import inspect
import collections.abc

async def sample_ag():
    yield 1

async def sample_coro():
    return 1

def sample_gen():
    yield 1

g = sample_ag()
asend_obj = g.__anext__()
athrow_obj = g.athrow(ValueError("x"))
aclose_obj = g.aclose()

assert isinstance(g, types.AsyncGeneratorType)
assert inspect.isasyncgen(g) is True
assert inspect.isasyncgen(sample_gen()) is False
assert inspect.isasyncgenfunction(sample_ag) is True
assert inspect.isasyncgenfunction(sample_coro) is False
assert inspect.isasyncgenfunction(sample_gen) is False
assert inspect.iscoroutinefunction(sample_ag) is False
assert inspect.isgeneratorfunction(sample_ag) is False
assert asyncio.iscoroutinefunction(sample_ag) is False
assert inspect.isawaitable(g) is False
assert inspect.isawaitable(asend_obj) is True
assert inspect.isawaitable(athrow_obj) is True
assert inspect.isawaitable(aclose_obj) is True

assert isinstance(g, collections.abc.AsyncGenerator)
assert isinstance(g, collections.abc.AsyncIterator)
assert isinstance(g, collections.abc.AsyncIterable)

async def drain():
    assert await asend_obj == 1
    assert await aclose_obj is None

asyncio.run(drain())
`
	v := runAsyncGenPython(t, src)

	// Verify GC stack release on closed async generator
	gv, ok := v.GetGlobal("g")
	if !ok || !gv.IsAsyncGenerator() {
		t.Fatalf("expected global g to be PyAsyncGenerator, got %v", gv)
	}
	ag := gv.AsAsyncGenerator()
	if ag.State != val.AsyncGenClosed {
		t.Fatalf("expected ag.State == AsyncGenClosed, got %v", ag.State)
	}
	if ag.Stack != nil {
		t.Fatalf("expected ag.Stack == nil after close for GC hygiene")
	}
	if !ag.AgAwait.IsNone() {
		t.Fatalf("expected ag.AgAwait == None after close, got %v", ag.AgAwait)
	}
	if ag.Running {
		t.Fatalf("expected ag.Running == false after close")
	}

	// Verify .fyc round-trip preserves CO_ASYNC_GENERATOR and IsAsyncGenerator
	fycSrc := `
import asyncio

async def fyc_ag(n):
    for i in range(n):
        await asyncio.sleep(0)
        yield i * 2

async def run():
    return [x async for x in fyc_ag(4)]

out = asyncio.run(run())
`
	co, err := compiler.CompileString(fycSrc)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	var srcHash [32]byte
	data, err := vm.SerializeCodeObject(co, uint64(len(fycSrc)), 12345, srcHash)
	if err != nil {
		t.Fatalf("SerializeCodeObject failed: %v", err)
	}
	co2, _, err := vm.DeserializeCodeObject(data)
	if err != nil {
		t.Fatalf("DeserializeCodeObject failed: %v", err)
	}

	var foundAG bool
	for _, c := range co2.Consts {
		if c.Kind() == val.KindCode {
			sub := vm.AsCodeObject(c)
			if sub.Name == "fyc_ag" {
				foundAG = true
				if !sub.IsAsyncGenerator || sub.IsGenerator || sub.IsCoroutine {
					t.Fatalf("fyc_ag flags wrong after round-trip: IsAsyncGenerator=%v IsGenerator=%v IsCoroutine=%v",
						sub.IsAsyncGenerator, sub.IsGenerator, sub.IsCoroutine)
				}
				if (sub.Flags & vm.CO_ASYNC_GENERATOR) == 0 {
					t.Fatalf("fyc_ag missing CO_ASYNC_GENERATOR flag: 0x%x", sub.Flags)
				}
				if (sub.Flags & (vm.CO_GENERATOR | vm.CO_COROUTINE)) != 0 {
					t.Fatalf("fyc_ag must not set CO_GENERATOR or CO_COROUTINE: 0x%x", sub.Flags)
				}
			}
		}
	}
	if !foundAG {
		t.Fatalf("fyc_ag code object not found in unmarshaled constants")
	}

	v2 := vm.NewVM()
	v2.RunCode(co2)
	out, ok := v2.GetGlobal("out")
	if !ok || !out.IsList() || out.AsList().Len() != 4 {
		t.Fatalf("unexpected out from .fyc round-trip: %v", out)
	}
	for i, want := range []int64{0, 2, 4, 6} {
		if got := out.AsList().Items[i].AsInt64(); got != want {
			t.Fatalf("out[%d] = %d, want %d", i, got, want)
		}
	}
}
