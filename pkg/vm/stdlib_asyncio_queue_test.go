package vm_test

import (
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runQueueTest(t *testing.T, src string) *vm.VM {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString error: %v\nSource:\n%s", err, src)
	}
	v := vm.NewVM()
	v.RunCode(co)
	if exc := v.LastException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unexpected Python exception: %v", exc)
	}
	return v
}

func TestAsyncioQueueFIFO(t *testing.T) {
	src := `
import asyncio

async def main():
    # Unbounded default & maxsize <= 0
    q0 = asyncio.Queue()
    assert q0.maxsize == 0
    assert q0.empty() is True
    assert q0.full() is False
    assert q0.qsize() == 0

    qneg = asyncio.Queue(maxsize=-5)
    assert qneg.maxsize == -5
    assert qneg.full() is False
    for i in range(25):
        qneg.put_nowait(i)
    assert qneg.full() is False
    assert qneg.qsize() == 25
    drained = [qneg.get_nowait() for _ in range(25)]
    assert drained == list(range(25))
    assert qneg.empty() is True

    # QueueEmpty on get_nowait
    got_empty = False
    try:
        qneg.get_nowait()
    except asyncio.QueueEmpty:
        got_empty = True
    assert got_empty

    # Bounded Queue(maxsize=2) + QueueFull + backpressure
    q = asyncio.Queue(maxsize=2)
    assert q.maxsize == 2
    q.put_nowait("a")
    q.put_nowait("b")
    assert q.full() is True
    got_full = False
    try:
        q.put_nowait("c")
    except asyncio.QueueFull:
        got_full = True
    assert got_full

    order = []
    async def producer():
        for i in range(10):
            await q.put(i)

    async def consumer():
        for _ in range(12):
            v = await q.get()
            order.append(v)

    await asyncio.gather(producer(), consumer())
    assert order == ["a", "b", 0, 1, 2, 3, 4, 5, 6, 7, 8, 9]
    assert q.empty() is True
    return True

ok = asyncio.run(main())
`
	v := runQueueTest(t, src)
	ok, _ := v.GetGlobal("ok")
	if !ok.AsBool() {
		t.Fatalf("expected ok=True, got %v", ok)
	}
}

func TestAsyncioLifoQueue(t *testing.T) {
	src := `
import asyncio

async def main():
    q = asyncio.LifoQueue(maxsize=3)
    assert isinstance(q, asyncio.Queue)
    assert isinstance(q, asyncio.LifoQueue)
    assert issubclass(asyncio.LifoQueue, asyncio.Queue)
    assert q.maxsize == 3

    q.put_nowait(1)
    q.put_nowait(2)
    q.put_nowait(3)
    assert q.full() is True

    got_full = False
    try:
        q.put_nowait(4)
    except asyncio.QueueFull:
        got_full = True
    assert got_full

    out = []
    async def putter():
        await q.put(4)
        await q.put(5)

    p = asyncio.create_task(putter())
    await asyncio.sleep(0)
    out.append(await q.get())  # 3
    out.append(await q.get())  # 4 or 2 depending on schedule; drain all 5
    out.append(await q.get())
    out.append(await q.get())
    out.append(await q.get())
    await p
    assert sorted(out) == [1, 2, 3, 4, 5]
    assert out[0] == 3

    # Strict LIFO without contention
    q2 = asyncio.LifoQueue()
    for x in [10, 20, 30, 40]:
        await q2.put(x)
    seq = [await q2.get() for _ in range(4)]
    assert seq == [40, 30, 20, 10]
    return True

ok = asyncio.run(main())
`
	v := runQueueTest(t, src)
	ok, _ := v.GetGlobal("ok")
	if !ok.AsBool() {
		t.Fatalf("expected ok=True, got %v", ok)
	}
}

func TestAsyncioPriorityQueue(t *testing.T) {
	src := `
import asyncio

class Job:
    def __init__(self, prio, name):
        self.prio = prio
        self.name = name

    def __lt__(self, other):
        return self.prio < other.prio

async def main():
    # Tuple priorities
    pq = asyncio.PriorityQueue()
    assert isinstance(pq, asyncio.Queue)
    for item in [(3, "c"), (1, "a"), (2, "b"), (1, "aa")]:
        await pq.put(item)
    res = [await pq.get() for _ in range(4)]
    assert res == [(1, "a"), (1, "aa"), (2, "b"), (3, "c")]

    # Custom __lt__ objects
    pq2 = asyncio.PriorityQueue()
    for p, name in [(5, "e"), (2, "b"), (8, "h"), (1, "a"), (3, "c")]:
        pq2.put_nowait(Job(p, name))
    names = [(await pq2.get()).name for _ in range(5)]
    assert names == ["a", "b", "c", "e", "h"]

    # Incomparable items raise TypeError without corrupting heap or unfinished_tasks
    pq3 = asyncio.PriorityQueue()
    pq3.put_nowait(10)
    pq3.put_nowait(5)
    pq3.put_nowait(20)
    got_type_err = False
    try:
        pq3.put_nowait("not-an-int")
    except TypeError:
        got_type_err = True
    assert got_type_err
    assert pq3.qsize() == 3
    assert [pq3.get_nowait() for _ in range(3)] == [5, 10, 20]
    for _ in range(3):
        pq3.task_done()
    # unfinishedTasks must be 0 now; a 4th task_done must raise ValueError
    got_val_err = False
    try:
        pq3.task_done()
    except ValueError:
        got_val_err = True
    assert got_val_err
    await pq3.join()
    return True

ok = asyncio.run(main())
`
	v := runQueueTest(t, src)
	ok, _ := v.GetGlobal("ok")
	if !ok.AsBool() {
		t.Fatalf("expected ok=True, got %v", ok)
	}
}

func TestAsyncioQueueTaskDoneAndJoin(t *testing.T) {
	src := `
import asyncio

async def main():
    q = asyncio.Queue()
    # Immediate join on empty queue
    await q.join()

    # Excess task_done raises ValueError
    got_val_err = False
    try:
        q.task_done()
    except ValueError:
        got_val_err = True
    assert got_val_err

    # Multiple concurrent join waiters
    for i in range(5):
        await q.put(i)

    joined = []
    async def joiner(tag):
        await q.join()
        joined.append(tag)

    j1 = asyncio.create_task(joiner("j1"))
    j2 = asyncio.create_task(joiner("j2"))
    await asyncio.sleep(0)
    assert joined == []

    total = 0
    for _ in range(5):
        total += await q.get()
        q.task_done()

    await asyncio.gather(j1, j2)
    assert total == 10
    assert sorted(joined) == ["j1", "j2"]
    return True

ok = asyncio.run(main())
`
	v := runQueueTest(t, src)
	ok, _ := v.GetGlobal("ok")
	if !ok.AsBool() {
		t.Fatalf("expected ok=True, got %v", ok)
	}
}

func TestAsyncioQueueShutdown(t *testing.T) {
	src := `
import asyncio

async def main():
    # 1. shutdown(immediate=False): drain buffered items, reject puts, fail waiting putters
    q = asyncio.Queue(maxsize=2)
    await q.put(1)
    await q.put(2)

    putter_err = None
    async def blocked_putter():
        nonlocal putter_err
        try:
            await q.put(3)
        except asyncio.QueueShutDown:
            putter_err = "shutdown"

    pt = asyncio.create_task(blocked_putter())
    await asyncio.sleep(0)

    q.shutdown()
    await pt
    assert putter_err == "shutdown"

    # put_nowait on full shut-down queue raises QueueShutDown (not QueueFull)
    got_sd_put = False
    try:
        q.put_nowait(99)
    except asyncio.QueueShutDown:
        got_sd_put = True
    assert got_sd_put

    # Remaining items drain cleanly
    assert await q.get() == 1
    assert q.get_nowait() == 2

    # Empty shut-down queue raises QueueShutDown (not QueueEmpty)
    got_sd_get = False
    try:
        q.get_nowait()
    except asyncio.QueueShutDown:
        got_sd_get = True
    assert got_sd_get

    got_sd_aget = False
    try:
        await q.get()
    except asyncio.QueueShutDown:
        got_sd_aget = True
    assert got_sd_aget

    # 2. shutdown(immediate=True): drains items, unblocks join(), fails getters & putters
    q2 = asyncio.Queue()
    await q2.put("x")
    await q2.put("y")
    join_task = asyncio.create_task(q2.join())
    await asyncio.sleep(0)
    assert not join_task.done()
    q2.shutdown(immediate=True)
    await join_task
    assert q2.empty() is True

    # Waiting getter fails on immediate shutdown
    q3 = asyncio.Queue()
    getter_sd = False
    async def blocked_getter():
        nonlocal getter_sd
        try:
            await q3.get()
        except asyncio.QueueShutDown:
            getter_sd = True

    gt = asyncio.create_task(blocked_getter())
    await asyncio.sleep(0)
    q3.shutdown(immediate=True)
    await gt
    assert getter_sd

    # 3. Idempotent escalation: shutdown(immediate=False) -> shutdown(immediate=True)
    q4 = asyncio.Queue()
    await q4.put(10)
    await q4.put(20)
    q4.shutdown(immediate=False)
    assert q4.qsize() == 2
    j4 = asyncio.create_task(q4.join())
    await asyncio.sleep(0)
    assert not j4.done()
    q4.shutdown(immediate=True)
    await j4
    assert q4.empty() is True
    return True

ok = asyncio.run(main())
`
	v := runQueueTest(t, src)
	ok, _ := v.GetGlobal("ok")
	if !ok.AsBool() {
		t.Fatalf("expected ok=True, got %v", ok)
	}
}

func TestAsyncioQueueCancellationHandoff(t *testing.T) {
	src := `
import asyncio
asyncio.set_mode("single")

async def main():
    # 1. Cancel first waiting getter while pending -> second getter receives item
    q = asyncio.Queue()
    async def getter():
        return await q.get()

    t1 = asyncio.create_task(getter())
    t2 = asyncio.create_task(getter())
    await asyncio.sleep(0)
    assert t1.cancel() is True
    await asyncio.sleep(0)
    q.put_nowait(42)
    assert await t2 == 42
    assert t1.cancelled() is True

    # 2. Cancel woken getter BETWEEN put_nowait wakeup and task resumption -> handoff to t4!
    q2 = asyncio.Queue()
    t3 = asyncio.create_task(getter_from(q2))
    t4 = asyncio.create_task(getter_from(q2))
    await asyncio.sleep(0)
    # put_nowait wakes t3 (sets result on t3's waiter future), then we immediately cancel t3
    # before t3 gets a chance to step. t3's onCancel() must hand off wakeup to t4!
    q2.put_nowait(99)
    assert t3.cancel() is True
    assert await t4 == 99
    assert t3.cancelled() is True

    # 3. Direct create_task(q.get()) cancellation handoff
    q3 = asyncio.Queue()
    dt1 = asyncio.create_task(q3.get())
    dt2 = asyncio.create_task(q3.get())
    await asyncio.sleep(0)
    q3.put_nowait(123)
    assert dt1.cancel() is True
    assert await dt2 == 123
    assert dt1.cancelled() is True

    # 4. Bounded putter cancellation handoff between get_nowait and resumption
    qb = asyncio.Queue(maxsize=1)
    qb.put_nowait("first")
    p1 = asyncio.create_task(qb.put("p1"))
    p2 = asyncio.create_task(qb.put("p2"))
    await asyncio.sleep(0)
    assert qb.get_nowait() == "first"
    assert p1.cancel() is True
    await p2
    assert qb.get_nowait() == "p2"
    assert p1.cancelled() is True

    # 5. wait_for timeout cancellation + handoff to next live waiter
    q4 = asyncio.Queue()
    timed_out = False
    try:
        await asyncio.wait_for(q4.get(), timeout=0.01)
    except asyncio.TimeoutError:
        timed_out = True
    assert timed_out

    live = asyncio.create_task(q4.get())
    await asyncio.sleep(0)
    q4.put_nowait("delivered")
    assert await live == "delivered"

    # 6. wait_for edge cases: timeout=None, timeout=0, outer cancellation -> CancelledError
    q5 = asyncio.Queue()
    q5.put_nowait("instant")
    assert await asyncio.wait_for(q5.get(), timeout=None) == "instant"

    done_fut = asyncio.Future()
    done_fut.set_result("already_done")
    assert await asyncio.wait_for(done_fut, timeout=0) == "already_done"

    t0_err = False
    try:
        await asyncio.wait_for(q5.get(), timeout=0)
    except asyncio.TimeoutError:
        t0_err = True
    assert t0_err

    outer_exc = None
    async def outer_waiter():
        nonlocal outer_exc
        try:
            await asyncio.wait_for(q5.get(), timeout=5.0)
        except asyncio.CancelledError:
            outer_exc = "cancelled"
        except asyncio.TimeoutError:
            outer_exc = "timeout"

    ow = asyncio.create_task(outer_waiter())
    await asyncio.sleep(0)
    ow.cancel()
    await ow
    assert outer_exc == "cancelled"

    # 7. Double-awaiting a queue awaitable raises RuntimeError without double-mutating
    q6 = asyncio.Queue()
    q6.put_nowait(1)
    q6.put_nowait(2)
    aw = q6.get()
    assert await aw == 1
    got_re = False
    try:
        await aw
    except RuntimeError:
        got_re = True
    assert got_re
    assert q6.qsize() == 1
    return True

async def getter_from(q):
    return await q.get()

ok = asyncio.run(main())
`
	v := runQueueTest(t, src)
	ok, _ := v.GetGlobal("ok")
	if !ok.AsBool() {
		t.Fatalf("expected ok=True, got %v", ok)
	}
}

func TestAsyncioQueueSubclassGenericRepr(t *testing.T) {
	src := `
import asyncio

class TaggedQueue(asyncio.Queue):
    def __init__(self, tag, maxsize=0):
        super().__init__(maxsize=maxsize)
        self.tag = tag

class TaggedLifo(asyncio.LifoQueue):
    pass

class TaggedPQ(asyncio.PriorityQueue):
    pass

async def main():
    tq = TaggedQueue("jobs", maxsize=4)
    assert isinstance(tq, asyncio.Queue)
    assert isinstance(tq, TaggedQueue)
    assert tq.tag == "jobs"
    assert tq.maxsize == 4
    await tq.put("a")
    await tq.put("b")
    assert [await tq.get(), await tq.get()] == ["a", "b"]

    tl = TaggedLifo()
    await tl.put(1)
    await tl.put(2)
    assert [await tl.get(), await tl.get()] == [2, 1]

    tp = TaggedPQ()
    await tp.put(5)
    await tp.put(1)
    assert [await tp.get(), await tp.get()] == [1, 5]

    # Generic typing subscripts
    g1 = asyncio.Queue[int]
    g2 = asyncio.LifoQueue[str]
    g3 = asyncio.PriorityQueue[tuple[int, str]]
    assert g1.__origin__ is asyncio.Queue
    assert g1.__args__ == (int,)
    assert g2.__origin__ is asyncio.LifoQueue
    assert g2.__args__ == (str,)
    assert g3.__origin__ is asyncio.PriorityQueue

    # __repr__ / __str__
    q = asyncio.Queue(maxsize=3)
    q.put_nowait(7)
    r = repr(q)
    s = str(q)
    return r, s

r, s = asyncio.run(main())
`
	v := runQueueTest(t, src)
	rVal, _ := v.GetGlobal("r")
	sVal, _ := v.GetGlobal("s")
	if rVal.AsString() != sVal.AsString() {
		t.Fatalf("expected repr == str, got %q vs %q", rVal.AsString(), sVal.AsString())
	}
	if !strings.Contains(rVal.AsString(), "<Queue at ") || !strings.Contains(rVal.AsString(), "maxsize=3") || !strings.Contains(rVal.AsString(), "tasks=1") {
		t.Fatalf("unexpected Queue repr: %q", rVal.AsString())
	}
}
