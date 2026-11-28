package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runSyncTest(t *testing.T, src string) *vm.VM {
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

func TestAsyncioEvent(t *testing.T) {
	src := `
import asyncio

async def main():
    ev = asyncio.Event()
    assert ev.is_set() is False

    woken = []
    async def waiter(i):
        res = await ev.wait()
        assert res is True
        woken.append(i)

    t1 = asyncio.create_task(waiter(1))
    t2 = asyncio.create_task(waiter(2))
    t3 = asyncio.create_task(waiter(3))
    await asyncio.sleep(0)
    assert woken == []

    # Cancel one waiter before set()
    t2.cancel()
    try:
        await t2
        assert False
    except asyncio.CancelledError:
        pass

    ev.set()
    assert ev.is_set() is True
    # Immediate wait when already set
    assert (await ev.wait()) is True

    await t1
    await t3
    assert sorted(woken) == [1, 3]

    # clear() / set() cycle
    ev.clear()
    assert ev.is_set() is False
    got = []
    async def waiter2():
        await ev.wait()
        got.append("ok")
    t4 = asyncio.create_task(waiter2())
    await asyncio.sleep(0)
    assert got == []
    ev.set()
    await t4
    assert got == ["ok"]

asyncio.run(main())
`
	runSyncTest(t, src)
}

func TestAsyncioLockAndCancellationHandoff(t *testing.T) {
	src := `
import asyncio

async def main():
    lock = asyncio.Lock()
    assert lock.locked() is False

    # Releasing an unlocked lock raises RuntimeError
    try:
        lock.release()
        assert False, "expected RuntimeError"
    except RuntimeError as e:
        assert "not acquired" in str(e)

    # Mutual exclusion + FIFO fairness
    order = []
    in_cs = 0
    max_cs = 0

    async def worker(i):
        nonlocal in_cs, max_cs
        async with lock:
            assert lock.locked() is True
            in_cs += 1
            if in_cs > max_cs:
                max_cs = in_cs
            order.append(i)
            await asyncio.sleep(0)
            in_cs -= 1

    await lock.acquire()
    assert lock.locked() is True
    tasks = [asyncio.create_task(worker(i)) for i in range(5)]
    await asyncio.sleep(0)
    lock.release()
    await asyncio.gather(*tasks)
    assert max_cs == 1
    assert order == [0, 1, 2, 3, 4]
    assert lock.locked() is False

    # Between-wakeup-and-resumption cancellation handoff (zero deadlocks)
    await lock.acquire()
    got_lock = []

    async def waiter(name):
        async with lock:
            got_lock.append(name)

    ta = asyncio.create_task(waiter("a"))
    tb = asyncio.create_task(waiter("b"))
    await asyncio.sleep(0)

    # Release wakes ta (ta's future is now resolved, queued to run),
    # then cancel ta BEFORE ta resumes -> ta's onCancel() must hand off to tb!
    lock.release()
    ta.cancel()

    try:
        await ta
        assert False, "ta should have been cancelled"
    except asyncio.CancelledError:
        pass

    await tb
    assert got_lock == ["b"]
    assert lock.locked() is False

asyncio.run(main())
`
	runSyncTest(t, src)
}

func TestAsyncioSemaphoreAndBoundedSemaphore(t *testing.T) {
	src := `
import asyncio

async def main():
    # Reject negative initial value
    try:
        asyncio.Semaphore(-1)
        assert False
    except ValueError:
        pass
    try:
        asyncio.BoundedSemaphore(-2)
        assert False
    except ValueError:
        pass

    # Bounded concurrency
    sem = asyncio.Semaphore(2)
    assert sem.locked() is False
    active = 0
    peak = 0
    done = []

    async def job(i):
        nonlocal active, peak
        async with sem:
            active += 1
            if active > peak:
                peak = active
            await asyncio.sleep(0)
            done.append(i)
            active -= 1

    await asyncio.gather(*(job(i) for i in range(6)))
    assert peak == 2
    assert len(done) == 6
    assert sem.locked() is False

    # Cancellation handoff on Semaphore
    s1 = asyncio.Semaphore(1)
    await s1.acquire()
    assert s1.locked() is True
    got = []
    async def w(tag):
        async with s1:
            got.append(tag)
    t1 = asyncio.create_task(w("first"))
    t2 = asyncio.create_task(w("second"))
    await asyncio.sleep(0)
    s1.release()
    t1.cancel()
    try:
        await t1
        assert False
    except asyncio.CancelledError:
        pass
    await t2
    assert got == ["second"]

    # BoundedSemaphore over-release raises ValueError
    bs = asyncio.BoundedSemaphore(2)
    await bs.acquire()
    bs.release()
    try:
        bs.release()
        assert False, "expected ValueError on over-release"
    except ValueError:
        pass

asyncio.run(main())
`
	runSyncTest(t, src)
}

func TestAsyncioCondition(t *testing.T) {
	src := `
import asyncio

async def main():
    cond = asyncio.Condition()

    # Unlocked wait/notify raise RuntimeError
    try:
        await cond.wait()
        assert False
    except RuntimeError:
        pass
    try:
        cond.notify()
        assert False
    except RuntimeError:
        pass
    try:
        cond.notify_all()
        assert False
    except RuntimeError:
        pass

    # notify(n) and notify_all()
    woken = []
    async def waiter(i):
        async with cond:
            await cond.wait()
            woken.append(i)

    tasks = [asyncio.create_task(waiter(i)) for i in range(4)]
    await asyncio.sleep(0)
    async with cond:
        cond.notify(2)
    await asyncio.sleep(0)
    await asyncio.sleep(0)
    assert woken == [0, 1]

    async with cond:
        cond.notify_all()
    await asyncio.gather(*tasks)
    assert woken == [0, 1, 2, 3]

    # wait_for(predicate)
    state = [0]
    async def waiter_pred():
        async with cond:
            res = await cond.wait_for(lambda: state[0] >= 3)
            return res

    tp = asyncio.create_task(waiter_pred())
    for v in (1, 2, 3):
        await asyncio.sleep(0)
        async with cond:
            state[0] = v
            cond.notify()
    assert (await tp) is True

    # Lock re-acquisition on CancelledError inside async with cond:
    cancelled_cleanly = False
    async def cancel_waiter():
        nonlocal cancelled_cleanly
        try:
            async with cond:
                await cond.wait()
        except asyncio.CancelledError:
            cancelled_cleanly = True
            raise

    tc = asyncio.create_task(cancel_waiter())
    await asyncio.sleep(0)
    # Hold the lock while cancelling tc so tc must wait to re-acquire lock before exiting async with!
    async with cond:
        tc.cancel()
        await asyncio.sleep(0)
        assert not tc.done()
    # Now lock is released -> tc re-acquires, exits async with (releasing cleanly), propagates CancelledError
    try:
        await tc
        assert False
    except asyncio.CancelledError:
        pass
    assert cancelled_cleanly is True
    assert cond.locked() is False

    # Custom Python async lock delegation
    class CustomLock:
        def __init__(self):
            self._inner = asyncio.Lock()
            self.enters = 0
        def locked(self):
            return self._inner.locked()
        async def acquire(self):
            return await self._inner.acquire()
        def release(self):
            self._inner.release()
        async def __aenter__(self):
            self.enters += 1
            await self._inner.acquire()
        async def __aexit__(self, et, ev, tb):
            self._inner.release()

    cl = CustomLock()
    cc = asyncio.Condition(cl)
    ready = [False]
    async def cwaiter():
        async with cc:
            await cc.wait_for(lambda: ready[0])
    tcustom = asyncio.create_task(cwaiter())
    await asyncio.sleep(0)
    async with cc:
        ready[0] = True
        cc.notify()
    await tcustom
    assert cl.enters == 2
    assert cc.locked() is False

asyncio.run(main())
`
	runSyncTest(t, src)
}

func TestAsyncioTimeout(t *testing.T) {
	src := `
import asyncio

assert asyncio.TimeoutError is TimeoutError

async def main():
    loop = asyncio.get_running_loop()
    assert loop.is_running() is True
    t0 = loop.time()
    assert isinstance(t0, float)

    # Normal completion
    async with asyncio.timeout(5.0) as cm:
        assert cm.when() is not None
        assert cm.expired() is False
        await asyncio.sleep(0)
    assert cm.expired() is False

    # reschedule() before __aenter__ and after __aexit__ raises RuntimeError
    t_unentered = asyncio.timeout(1.0)
    try:
        t_unentered.reschedule(None)
        assert False
    except RuntimeError:
        pass
    try:
        cm.reschedule(None)
        assert False
    except RuntimeError:
        pass

    # Expiry raises TimeoutError (catchable by both TimeoutError and asyncio.TimeoutError)
    cm2 = None
    try:
        async with asyncio.timeout(0.02) as cm2:
            await asyncio.sleep(5.0)
        assert False, "expected TimeoutError"
    except TimeoutError:
        pass
    assert cm2.expired() is True
    assert asyncio.current_task().cancelling() == 0

    # delay=None is a no-op
    async with asyncio.timeout(None) as cm3:
        assert cm3.when() is None
        await asyncio.sleep(0)
    assert cm3.expired() is False

    # delay <= 0 schedules immediate expiration
    try:
        async with asyncio.timeout(0) as cm4:
            await asyncio.sleep(1.0)
        assert False, "expected TimeoutError for delay=0"
    except asyncio.TimeoutError:
        pass
    assert cm4.expired() is True

    # timeout_at() and reschedule() extending deadline
    async with asyncio.timeout_at(loop.time() + 0.02) as cm5:
        cm5.reschedule(loop.time() + 5.0)
        await asyncio.sleep(0.04)
    assert cm5.expired() is False

    # reschedule() shortening deadline to immediate
    try:
        async with asyncio.timeout(5.0) as cm6:
            cm6.reschedule(loop.time())
            await asyncio.sleep(1.0)
        assert False
    except TimeoutError:
        pass
    assert cm6.expired() is True

    # External task.cancel() + timeout expiry -> CancelledError preserved!
    async def both_cancel_and_timeout():
        async with asyncio.timeout(0.02):
            await asyncio.sleep(5.0)

    t_both = asyncio.create_task(both_cancel_and_timeout())
    await asyncio.sleep(0.01)
    t_both.cancel()
    try:
        await t_both
        assert False
    except asyncio.CancelledError:
        pass

    # Multi-cancel counting on Task
    async def multi_cancel_worker():
        me = asyncio.current_task()
        try:
            await asyncio.sleep(5.0)
        except asyncio.CancelledError:
            assert me.cancelling() == 2
            assert me.uncancel() == 1
            assert me.uncancel() == 0
            return "recovered"

    tmc = asyncio.create_task(multi_cancel_worker())
    await asyncio.sleep(0)
    assert tmc.cancel() is True
    assert tmc.cancel() is True
    assert tmc.cancelling() == 2
    assert (await tmc) == "recovered"

asyncio.run(main())

# Outside running event loop: get_running_loop / current_task / all_tasks raise RuntimeError
for fn in (asyncio.get_running_loop, asyncio.current_task, asyncio.all_tasks):
    try:
        fn()
        assert False, "expected RuntimeError outside running loop"
    except RuntimeError:
        pass
`
	runSyncTest(t, src)
}

func TestAsyncioTaskGroup(t *testing.T) {
	src := `
import asyncio

async def main():
    # Reject create_task before __aenter__
    tg0 = asyncio.TaskGroup()
    async def noop():
        return 1
    c = noop()
    try:
        tg0.create_task(c)
        assert False
    except RuntimeError:
        pass

    # Concurrent completion + nested create_task + task names
    results = []
    async def child(x, tg=None):
        await asyncio.sleep(0)
        results.append(x)
        if tg is not None:
            tg.create_task(child(x * 10), name="nested")
        return x

    async with asyncio.TaskGroup() as tg:
        t1 = tg.create_task(child(1, tg), name="first")
        t2 = tg.create_task(child(2), name="second")
        assert t1.get_name() == "first"
        assert t2.get_name() == "second"
    assert sorted(results) == [1, 2, 10]
    assert t1.result() == 1
    assert t2.result() == 2

    # Reject re-entering exited TaskGroup and create_task after exit
    try:
        async with tg:
            pass
        assert False
    except RuntimeError:
        pass
    try:
        tg.create_task(noop())
        assert False
    except RuntimeError:
        pass

    # Child failure cancels sibling and raises ExceptionGroup
    sibling_cancelled = False
    async def failing(msg):
        await asyncio.sleep(0)
        raise ValueError(msg)

    async def slow_sibling():
        nonlocal sibling_cancelled
        try:
            await asyncio.sleep(5.0)
        except asyncio.CancelledError:
            sibling_cancelled = True
            raise

    try:
        async with asyncio.TaskGroup() as tg2:
            tg2.create_task(failing("boom1"))
            tg2.create_task(slow_sibling())
        assert False, "expected ExceptionGroup"
    except ExceptionGroup as eg:
        assert eg.message == "unhandled errors in a TaskGroup"
        assert len(eg.exceptions) == 1
        assert isinstance(eg.exceptions[0], ValueError)
        assert "boom1" in str(eg.exceptions[0])
        sub = eg.subgroup(ValueError)
        assert sub is not None and len(sub.exceptions) == 1
        m, r = eg.split(TypeError)
        assert m is None and r is not None
    assert sibling_cancelled is True
    assert asyncio.current_task().cancelling() == 0

    # Multiple child failures aggregated into ExceptionGroup
    async def fail_type():
        await asyncio.sleep(0)
        raise TypeError("bad type")

    try:
        async with asyncio.TaskGroup() as tg3:
            tg3.create_task(failing("v1"))
            tg3.create_task(fail_type())
        assert False
    except ExceptionGroup as eg:
        assert len(eg.exceptions) == 2
        types = sorted(type(e).__name__ for e in eg.exceptions)
        assert types == ["TypeError", "ValueError"]

    # Body exception with NO child error propagates unwrapped!
    child_cancelled = False
    async def wait_forever():
        nonlocal child_cancelled
        try:
            await asyncio.sleep(5.0)
        except asyncio.CancelledError:
            child_cancelled = True
            raise

    try:
        async with asyncio.TaskGroup() as tg4:
            tg4.create_task(wait_forever())
            await asyncio.sleep(0)
            raise KeyError("body_only")
        assert False
    except KeyError as ke:
        assert "body_only" in str(ke)
    assert child_cancelled is True

    # Body exception WITH child error wraps both in ExceptionGroup
    async def fail_on_cancel():
        try:
            await asyncio.sleep(5.0)
        except asyncio.CancelledError:
            raise ValueError("cleanup_err")

    try:
        async with asyncio.TaskGroup() as tg5:
            tg5.create_task(fail_on_cancel())
            await asyncio.sleep(0)
            raise KeyError("body_and_child")
        assert False
    except ExceptionGroup as eg:
        types = [type(e).__name__ for e in eg.exceptions]
        assert "KeyError" in types and "ValueError" in types

    # BaseExceptionGroup when child raises KeyboardInterrupt
    async def raise_kb():
        await asyncio.sleep(0)
        raise KeyboardInterrupt("stop")

    try:
        async with asyncio.TaskGroup() as tg6:
            tg6.create_task(raise_kb())
        assert False
    except BaseExceptionGroup as beg:
        assert not isinstance(beg, ExceptionGroup)
        assert len(beg.exceptions) == 1
        assert isinstance(beg.exceptions[0], KeyboardInterrupt)

    # External cancellation cancels children and propagates CancelledError
    ext_child_cancelled = False
    async def ext_child():
        nonlocal ext_child_cancelled
        try:
            await asyncio.sleep(5.0)
        except asyncio.CancelledError:
            ext_child_cancelled = True
            raise

    async def ext_parent():
        async with asyncio.TaskGroup() as tg7:
            tg7.create_task(ext_child())

    tp = asyncio.create_task(ext_parent())
    await asyncio.sleep(0)
    tp.cancel()
    try:
        await tp
        assert False
    except asyncio.CancelledError:
        pass
    assert ext_child_cancelled is True

asyncio.run(main())
`
	runSyncTest(t, src)
}

func TestAsyncioCurrentAndAllTasksAndSubclassing(t *testing.T) {
	src := `
import asyncio

class MyEvent(asyncio.Event):
    def __init__(self, tag):
        super().__init__()
        self.tag = tag

class MyLock(asyncio.Lock):
    def __init__(self, tag):
        super().__init__()
        self.tag = tag

class MySem(asyncio.Semaphore):
    def __init__(self, n, tag):
        super().__init__(n)
        self.tag = tag

class MyCond(asyncio.Condition):
    def __init__(self, tag):
        super().__init__()
        self.tag = tag

async def main():
    me = asyncio.current_task()
    assert me is not None
    me.set_name("main-task")
    assert me.get_name() == "main-task"

    ev = MyEvent("e1")
    assert ev.tag == "e1"
    lk = MyLock("l1")
    assert lk.tag == "l1"
    sm = MySem(2, "s1")
    assert sm.tag == "s1"
    cd = MyCond("c1")
    assert cd.tag == "c1"

    async def ok_task():
        await ev.wait()
        return 42

    async def err_task():
        await ev.wait()
        raise RuntimeError("boom")

    async def cancel_task():
        await asyncio.sleep(5.0)

    t_ok = asyncio.create_task(ok_task(), name="ok")
    t_err = asyncio.create_task(err_task(), name="err")
    t_can = asyncio.create_task(cancel_task(), name="can")
    await asyncio.sleep(0)

    tasks = asyncio.all_tasks()
    assert me in tasks and t_ok in tasks and t_err in tasks and t_can in tasks

    # Complete via all three terminal paths (result, exception, cancellation)
    ev.set()
    t_can.cancel()
    assert (await t_ok) == 42
    try:
        await t_err
    except RuntimeError:
        pass
    try:
        await t_can
    except asyncio.CancelledError:
        pass

    # Leak-free: all finished tasks must be removed from all_tasks()
    tasks_after = asyncio.all_tasks()
    assert me in tasks_after
    assert t_ok not in tasks_after
    assert t_err not in tasks_after
    assert t_can not in tasks_after

    async with lk:
        assert lk.locked() is True
    async with sm:
        pass
    async with cd:
        assert cd.locked() is True

asyncio.run(main())
`
	runSyncTest(t, src)
}
