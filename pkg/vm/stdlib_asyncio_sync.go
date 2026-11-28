package vm

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	AsyncEventType            *val.PyType
	AsyncLockType             *val.PyType
	AsyncSemaphoreType        *val.PyType
	AsyncBoundedSemaphoreType *val.PyType
	AsyncConditionType        *val.PyType
	AsyncTimeoutType          *val.PyType
	AsyncTaskGroupType        *val.PyType
	EventLoopType             *val.PyType
	syncAwaitableType         *val.PyType
	asyncioSyncOnce           sync.Once
)

type syncAwaitOp uint8

const (
	syncAwaitResolved syncAwaitOp = iota
	syncAwaitEventWait
	syncAwaitLockAcquire
	syncAwaitLockEnter
	syncAwaitLockExit
	syncAwaitSemAcquire
	syncAwaitSemEnter
	syncAwaitSemExit
	syncAwaitCondWait
	syncAwaitCondWaitFor
	syncAwaitTimeoutEnter
	syncAwaitTimeoutExit
	syncAwaitTaskGroupEnter
	syncAwaitTaskGroupExit
)

type condWaitState uint8

const (
	condStateStart condWaitState = iota
	condStateWaiting
	condStateReacquiring
)

type tgExitState uint8

const (
	tgExitStart tgExitState = iota
	tgExitWaiting
)

type timeoutState uint8

const (
	timeoutCreated timeoutState = iota
	timeoutEntered
	timeoutExpired
	timeoutFinished
)

// pyAsyncEvent implements asyncio.Event.
// inst MUST be at offset 0 so Python subclasses and attribute access work natively.
type pyAsyncEvent struct {
	inst    val.PyInstance
	mu      sync.Mutex
	value   bool
	waiters []*val.PyFuture
}

// pyAsyncLock implements asyncio.Lock.
// inst MUST be at offset 0.
type pyAsyncLock struct {
	inst    val.PyInstance
	mu      sync.Mutex
	locked  bool
	waiters []*val.PyFuture
}

// pyAsyncSemaphore implements asyncio.Semaphore and asyncio.BoundedSemaphore.
// inst MUST be at offset 0.
type pyAsyncSemaphore struct {
	inst       val.PyInstance
	mu         sync.Mutex
	value      int
	boundValue int
	bounded    bool
	waiters    []*val.PyFuture
}

// pyAsyncCondition implements asyncio.Condition.
// inst MUST be at offset 0.
type pyAsyncCondition struct {
	inst       val.PyInstance
	mu         sync.Mutex
	lock       val.Value
	nativeLock *pyAsyncLock
	waiters    []*val.PyFuture
}

// pyAsyncTimeout implements asyncio.Timeout (returned by asyncio.timeout / asyncio.timeout_at).
// inst MUST be at offset 0.
type pyAsyncTimeout struct {
	inst            val.PyInstance
	mu              sync.Mutex
	state           timeoutState
	when            float64
	hasWhen         bool
	loop            *EventLoop
	task            *val.PyTask
	timer           *time.Timer
	cancelPending   bool
	enterCancelling int
	timerGen        uint64
}

// pyAsyncTaskGroup implements asyncio.TaskGroup.
// inst MUST be at offset 0.
type pyAsyncTaskGroup struct {
	inst                  val.PyInstance
	mu                    sync.Mutex
	entered               bool
	exiting               bool
	exited                bool
	aborting              bool
	parentCancelRequested bool
	enterCancelling       int
	loop                  *EventLoop
	parentTask            *val.PyTask
	tasks                 map[*val.PyTask]struct{}
	errors                []val.Value
	onCompletedFut        *val.PyFuture
}

// pyEventLoop wraps *EventLoop for asyncio.get_running_loop() / asyncio.get_event_loop().
// inst MUST be at offset 0.
type pyEventLoop struct {
	inst val.PyInstance
	loop *EventLoop
}

// pySyncAwaitable is the native TpIterNext awaitable backing asyncio sync primitives
// and structured concurrency context managers. Header MUST be at offset 0.
type pySyncAwaitable struct {
	Header          val.ObjectHeader
	op              syncAwaitOp
	done            bool
	event           *pyAsyncEvent
	lock            *pyAsyncLock
	sem             *pyAsyncSemaphore
	cond            *pyAsyncCondition
	timeout         *pyAsyncTimeout
	tg              *pyAsyncTaskGroup
	waiter          *val.PyFuture
	result          val.Value
	pred            val.Value
	excVal          val.Value
	savedExc        val.Value
	cancelExc       val.Value
	subAw           val.Value
	condState       condWaitState
	tgState         tgExitState
	hasSavedExc     bool
	propagateCancel bool
}

var (
	_ [0 - unsafe.Offsetof(pyAsyncEvent{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyAsyncLock{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyAsyncSemaphore{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyAsyncCondition{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyAsyncTimeout{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyAsyncTaskGroup{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyEventLoop{}.inst)]byte
	_ [0 - unsafe.Offsetof(pySyncAwaitable{}.Header)]byte
)

func asAsyncEvent(v val.Value) *pyAsyncEvent {
	return (*pyAsyncEvent)(val.AsPointer(v))
}

func asAsyncLock(v val.Value) *pyAsyncLock {
	return (*pyAsyncLock)(val.AsPointer(v))
}

func asAsyncSemaphore(v val.Value) *pyAsyncSemaphore {
	return (*pyAsyncSemaphore)(val.AsPointer(v))
}

func asAsyncCondition(v val.Value) *pyAsyncCondition {
	return (*pyAsyncCondition)(val.AsPointer(v))
}

func asAsyncTimeout(v val.Value) *pyAsyncTimeout {
	return (*pyAsyncTimeout)(val.AsPointer(v))
}

func asAsyncTaskGroup(v val.Value) *pyAsyncTaskGroup {
	return (*pyAsyncTaskGroup)(val.AsPointer(v))
}

func asEventLoopObj(v val.Value) *pyEventLoop {
	return (*pyEventLoop)(val.AsPointer(v))
}

func asSyncAwaitable(v val.Value) *pySyncAwaitable {
	return (*pySyncAwaitable)(unsafe.Pointer(v.HeaderPtr()))
}

func hasLiveWaitersLocked(waiters []*val.PyFuture) bool {
	for _, f := range waiters {
		if f != nil && !f.IsDone() {
			return true
		}
	}
	return false
}

func newPyEventLoopVal(loop *EventLoop) val.Value {
	el := &pyEventLoop{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: EventLoopType},
			Shape:  EventLoopType.RootShape,
		},
		loop: loop,
	}
	return val.ObjectVal(&el.inst.Header)
}

func newSyncAwaitable(aw *pySyncAwaitable) val.Value {
	aw.Header = val.ObjectHeader{Kind: val.KindNative, Type: syncAwaitableType}
	return val.ObjectVal(&aw.Header)
}

func isSyncAwaitableNeedingCancelStep(v val.Value) bool {
	if syncAwaitableType == nil || v.IsNone() || v.HeaderPtr() == nil || v.Type() != syncAwaitableType {
		return false
	}
	op := asSyncAwaitable(v).op
	return op == syncAwaitCondWait || op == syncAwaitCondWaitFor || op == syncAwaitTaskGroupExit
}

func stepCancelSyncAwaitable(ctx *val.ExecContext, v val.Value, cancelExc val.Value) (val.Value, val.IterStatus) {
	return asSyncAwaitable(v).stepCancel(ctx, cancelExc)
}

func cancelSyncAwaitable(v val.Value) {
	if syncAwaitableType == nil || v.IsNone() || v.HeaderPtr() == nil || v.Type() != syncAwaitableType {
		return
	}
	asSyncAwaitable(v).onCancel()
}

// --- Lock / Semaphore helpers ---

func (l *pyAsyncLock) release(ctx *val.ExecContext) bool {
	l.mu.Lock()
	if !l.locked {
		l.mu.Unlock()
		ctx.Raise(val.NewRuntimeError("Lock is not acquired."))
		return false
	}
	l.locked = false
	next := popNextWaiterLocked(&l.waiters)
	l.mu.Unlock()
	if next != nil {
		next.SetResult(val.BoolVal(true))
	}
	return true
}

func (s *pyAsyncSemaphore) release(ctx *val.ExecContext) bool {
	s.mu.Lock()
	if s.bounded && s.value >= s.boundValue {
		s.mu.Unlock()
		ctx.Raise(val.NewValueError("BoundedSemaphore released too many times"))
		return false
	}
	s.value++
	next := popNextWaiterLocked(&s.waiters)
	s.mu.Unlock()
	if next != nil {
		next.SetResult(val.BoolVal(true))
	}
	return true
}

// --- Condition helpers ---

func (c *pyAsyncCondition) isLocked(ctx *val.ExecContext) (bool, bool) {
	if c.nativeLock != nil {
		c.nativeLock.mu.Lock()
		v := c.nativeLock.locked
		c.nativeLock.mu.Unlock()
		return v, true
	}
	fn := val.GetAttrCtx(ctx, c.lock, val.InternString("locked"))
	if ctx != nil && ctx.HasException() {
		return false, false
	}
	res := ctx.Call(fn, nil, nil)
	if ctx != nil && ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

func (c *pyAsyncCondition) releaseLock(ctx *val.ExecContext) bool {
	if c.nativeLock != nil {
		return c.nativeLock.release(ctx)
	}
	fn := val.GetAttrCtx(ctx, c.lock, val.InternString("release"))
	if ctx != nil && ctx.HasException() {
		return false
	}
	ctx.Call(fn, nil, nil)
	return ctx == nil || !ctx.HasException()
}

func (c *pyAsyncCondition) notify(ctx *val.ExecContext, n int) {
	locked, ok := c.isLocked(ctx)
	if !ok {
		return
	}
	if !locked {
		ctx.Raise(val.NewRuntimeError("cannot notify on un-acquired lock"))
		return
	}
	if n <= 0 {
		return
	}
	c.mu.Lock()
	var toWake []*val.PyFuture
	for len(toWake) < n {
		w := popNextWaiterLocked(&c.waiters)
		if w == nil {
			break
		}
		toWake = append(toWake, w)
	}
	c.mu.Unlock()
	for _, w := range toWake {
		w.SetResult(val.BoolVal(true))
	}
}

func (c *pyAsyncCondition) notifyAll(ctx *val.ExecContext) {
	locked, ok := c.isLocked(ctx)
	if !ok {
		return
	}
	if !locked {
		ctx.Raise(val.NewRuntimeError("cannot notify on un-acquired lock"))
		return
	}
	c.mu.Lock()
	toWake := takeAllWaitersLocked(&c.waiters)
	c.mu.Unlock()
	for _, w := range toWake {
		if w != nil && !w.IsDone() {
			w.SetResult(val.BoolVal(true))
		}
	}
}

// --- Timeout helpers ---

func (t *pyAsyncTimeout) scheduleLocked() {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.timerGen++
	if !t.hasWhen || t.state != timeoutEntered || t.loop == nil {
		return
	}
	gen := t.timerGen
	delay := t.when - t.loop.Time()
	if delay <= 0 {
		loop := t.loop
		loop.CallSoonThreadsafe(func() {
			t.onTimeout(gen)
		})
		return
	}
	loop := t.loop
	t.timer = time.AfterFunc(time.Duration(delay*float64(time.Second)), func() {
		loop.CallSoonThreadsafe(func() {
			t.onTimeout(gen)
		})
	})
}

func (t *pyAsyncTimeout) onTimeout(gen uint64) {
	t.mu.Lock()
	if t.timerGen != gen || t.state != timeoutEntered {
		t.mu.Unlock()
		return
	}
	t.state = timeoutExpired
	t.timer = nil
	task := t.task
	if task == nil || task.IsDone() {
		t.mu.Unlock()
		return
	}
	t.cancelPending = true
	t.mu.Unlock()
	task.Cancel(newCancelledError(""))
}

func (t *pyAsyncTimeout) reschedule(ctx *val.ExecContext, whenVal val.Value) {
	t.mu.Lock()
	switch t.state {
	case timeoutCreated:
		t.mu.Unlock()
		ctx.Raise(val.NewRuntimeError("Timeout has not been entered"))
		return
	case timeoutExpired, timeoutFinished:
		t.mu.Unlock()
		ctx.Raise(val.NewRuntimeError("Cannot change state of finished Timeout"))
		return
	}
	if whenVal.IsNone() {
		t.hasWhen = false
		t.when = 0
		if t.timer != nil {
			t.timer.Stop()
			t.timer = nil
		}
		t.timerGen++
		t.mu.Unlock()
		return
	}
	var w float64
	switch {
	case whenVal.IsFloat64():
		w = whenVal.AsFloat64()
	case whenVal.IsInt64():
		w = float64(whenVal.AsInt64())
	default:
		t.mu.Unlock()
		ctx.Raise(val.NewTypeError("when must be float, int, or None"))
		return
	}
	t.hasWhen = true
	t.when = w
	t.scheduleLocked()
	t.mu.Unlock()
}

// --- TaskGroup helpers ---

func (tg *pyAsyncTaskGroup) abort() {
	tg.mu.Lock()
	toCancel, cancelParent := tg.abortLocked()
	parent := tg.parentTask
	tg.mu.Unlock()
	for _, t := range toCancel {
		t.Cancel(newCancelledError(""))
	}
	if cancelParent && parent != nil {
		parent.Cancel(newCancelledError(""))
	}
}

func (tg *pyAsyncTaskGroup) abortLocked() (toCancel []*val.PyTask, cancelParent bool) {
	if tg.aborting {
		return nil, false
	}
	tg.aborting = true
	for t := range tg.tasks {
		if !t.IsDone() {
			toCancel = append(toCancel, t)
		}
	}
	if !tg.exiting && tg.parentTask != nil && !tg.parentTask.IsDone() && !tg.parentCancelRequested {
		tg.parentCancelRequested = true
		cancelParent = true
	}
	return toCancel, cancelParent
}

func (tg *pyAsyncTaskGroup) onTaskDone(task *val.PyTask) {
	_, exc := task.Result()
	isErr := !exc.IsNone() && exc.HeaderPtr() != nil && !isCancelledError(exc)

	tg.mu.Lock()
	delete(tg.tasks, task)
	var toCancel []*val.PyTask
	var cancelParent bool
	var parent *val.PyTask
	if isErr {
		tg.errors = append(tg.errors, exc)
		toCancel, cancelParent = tg.abortLocked()
		parent = tg.parentTask
	}
	var completedFut *val.PyFuture
	if len(tg.tasks) == 0 && tg.onCompletedFut != nil {
		completedFut = tg.onCompletedFut
		tg.onCompletedFut = nil
	}
	tg.mu.Unlock()

	for _, t := range toCancel {
		t.Cancel(newCancelledError(""))
	}
	if cancelParent && parent != nil {
		parent.Cancel(newCancelledError(""))
	}
	if completedFut != nil {
		completedFut.SetResult(val.NoneVal())
	}
}

// --- pySyncAwaitable cancellation & stepping ---

func (aw *pySyncAwaitable) onCancel() {
	if aw.done {
		return
	}
	aw.done = true
	w := aw.waiter
	aw.waiter = nil
	if w == nil {
		return
	}
	wWasCancelled := w.IsCancelled()
	if !w.IsDone() {
		w.Cancel(newCancelledError(""))
	}
	switch aw.op {
	case syncAwaitEventWait:
		e := aw.event
		e.mu.Lock()
		removeWaiterLocked(&e.waiters, w)
		e.mu.Unlock()
	case syncAwaitLockAcquire, syncAwaitLockEnter:
		l := aw.lock
		l.mu.Lock()
		removeWaiterLocked(&l.waiters, w)
		var next *val.PyFuture
		if !wWasCancelled && !l.locked {
			next = popNextWaiterLocked(&l.waiters)
		}
		l.mu.Unlock()
		if next != nil {
			next.SetResult(val.BoolVal(true))
		}
	case syncAwaitSemAcquire, syncAwaitSemEnter:
		s := aw.sem
		s.mu.Lock()
		removeWaiterLocked(&s.waiters, w)
		var next *val.PyFuture
		if !wWasCancelled && s.value > 0 {
			next = popNextWaiterLocked(&s.waiters)
		}
		s.mu.Unlock()
		if next != nil {
			next.SetResult(val.BoolVal(true))
		}
	case syncAwaitCondWait, syncAwaitCondWaitFor:
		c := aw.cond
		c.mu.Lock()
		removeWaiterLocked(&c.waiters, w)
		c.mu.Unlock()
	}
}

func (aw *pySyncAwaitable) stepCancel(ctx *val.ExecContext, cancelExc val.Value) (val.Value, val.IterStatus) {
	if aw.done {
		ctx.Raise(cancelExc)
		return val.NoneVal(), val.IterError
	}
	switch aw.op {
	case syncAwaitCondWait, syncAwaitCondWaitFor:
		if !aw.hasSavedExc {
			aw.savedExc = cancelExc
			aw.hasSavedExc = true
		}
		switch aw.condState {
		case condStateWaiting:
			if w := aw.waiter; w != nil {
				aw.waiter = nil
				if !w.IsDone() {
					w.Cancel(cancelExc)
				}
				aw.cond.mu.Lock()
				removeWaiterLocked(&aw.cond.waiters, w)
				aw.cond.mu.Unlock()
			}
			aw.condState = condStateReacquiring
		case condStateReacquiring:
			if w := aw.waiter; w != nil {
				aw.waiter = nil
				wWasCancelled := w.IsCancelled()
				if !w.IsDone() {
					w.Cancel(cancelExc)
				}
				if aw.cond.nativeLock != nil {
					l := aw.cond.nativeLock
					l.mu.Lock()
					removeWaiterLocked(&l.waiters, w)
					var next *val.PyFuture
					if !wWasCancelled && !l.locked {
						next = popNextWaiterLocked(&l.waiters)
					}
					l.mu.Unlock()
					if next != nil {
						next.SetResult(val.BoolVal(true))
					}
				}
			}
			aw.subAw = val.Value{}
		}
		return aw.stepCondReacquire(ctx)

	case syncAwaitTaskGroupExit:
		aw.propagateCancel = true
		if aw.cancelExc.IsNone() || aw.cancelExc.HeaderPtr() == nil {
			aw.cancelExc = cancelExc
		}
		if w := aw.waiter; w != nil {
			aw.waiter = nil
			aw.tg.mu.Lock()
			if aw.tg.onCompletedFut == w {
				aw.tg.onCompletedFut = nil
			}
			aw.tg.mu.Unlock()
		}
		aw.tg.abort()
		return aw.stepTaskGroupWait(ctx)
	}
	aw.onCancel()
	ctx.Raise(cancelExc)
	return val.NoneVal(), val.IterError
}

func (aw *pySyncAwaitable) stepCondReacquire(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	c := aw.cond
	if c.nativeLock != nil {
		l := c.nativeLock
		if aw.waiter != nil {
			w := aw.waiter
			aw.waiter = nil
			if w.IsCancelled() {
				if !aw.hasSavedExc {
					_, exc := w.Result()
					if exc.IsNone() || exc.HeaderPtr() == nil {
						exc = newCancelledError("")
					}
					aw.savedExc = exc
					aw.hasSavedExc = true
				}
				l.mu.Lock()
				removeWaiterLocked(&l.waiters, w)
				l.mu.Unlock()
			}
		}
		l.mu.Lock()
		if !l.locked && !hasLiveWaitersLocked(l.waiters) {
			l.locked = true
			l.mu.Unlock()
			return aw.finishCondWait(ctx)
		}
		if !l.locked {
			l.locked = true
			l.mu.Unlock()
			return aw.finishCondWait(ctx)
		}
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		l.waiters = append(l.waiters, fut)
		aw.waiter = fut
		l.mu.Unlock()
		return futVal, val.IterYielded
	}

	// Custom Python lock fallback: retry acquire() until lock is held even under CancelledError.
	for {
		if aw.subAw.IsNone() || aw.subAw.HeaderPtr() == nil {
			acqFn := val.GetAttrCtx(ctx, c.lock, val.InternString("acquire"))
			if ctx != nil && ctx.HasException() {
				aw.done = true
				return val.NoneVal(), val.IterError
			}
			sub := ctx.Call(acqFn, nil, nil)
			if ctx != nil && ctx.HasException() {
				aw.done = true
				return val.NoneVal(), val.IterError
			}
			aw.subAw = sub
		}
		sub := aw.subAw
		if sub.IsFuture() || sub.IsTask() {
			fut := sub.AsFuture()
			if sub.IsTask() {
				fut = &sub.AsTask().Future
			}
			if !fut.IsDone() {
				aw.waiter = fut
				return sub, val.IterYielded
			}
			aw.waiter = nil
			aw.subAw = val.Value{}
			if fut.IsCancelled() {
				if !aw.hasSavedExc {
					_, exc := fut.Result()
					if exc.IsNone() || exc.HeaderPtr() == nil {
						exc = newCancelledError("")
					}
					aw.savedExc = exc
					aw.hasSavedExc = true
				}
				continue
			}
			_, exc := fut.Result()
			if !exc.IsNone() && exc.HeaderPtr() != nil {
				if isCancelledError(exc) {
					if !aw.hasSavedExc {
						aw.savedExc = exc
						aw.hasSavedExc = true
					}
					continue
				}
				aw.done = true
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
			return aw.finishCondWait(ctx)
		}
		if sub.IsCoroutine() {
			var vm *VM
			if ctx != nil && ctx.VM != nil {
				vm = ctx.VM.(*VM)
			}
			if vm == nil {
				aw.done = true
				ctx.Raise(val.NewRuntimeError("no active VM"))
				return val.NoneVal(), val.IterError
			}
			subCoro := sub.AsCoroutine()
			if aw.waiter != nil {
				w := aw.waiter
				aw.waiter = nil
				if w.IsCancelled() {
					_, exc := w.Result()
					if exc.IsNone() || exc.HeaderPtr() == nil {
						exc = newCancelledError("")
					}
					ctx.Raise(exc)
				}
			}
			_, status := vm.resumeCoroutine(ctx, subCoro, val.NoneVal())
			switch status {
			case val.IterYielded:
				yv := subCoro.YieldVal
				if yv.IsFuture() {
					aw.waiter = yv.AsFuture()
				} else if yv.IsTask() {
					aw.waiter = &yv.AsTask().Future
				}
				return yv, val.IterYielded
			case val.IterExhausted:
				aw.subAw = val.Value{}
				return aw.finishCondWait(ctx)
			default:
				exc := ctx.ActiveException
				ctx.ClearException()
				aw.subAw = val.Value{}
				if isCancelledError(exc) {
					if !aw.hasSavedExc {
						aw.savedExc = exc
						aw.hasSavedExc = true
					}
					continue
				}
				aw.done = true
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
		}
		if tpNext := sub.Type().Slots.TpIterNext; tpNext != nil {
			nextVal, status := tpNext(ctx, sub)
			switch status {
			case val.IterYielded:
				if nextVal.IsFuture() {
					aw.waiter = nextVal.AsFuture()
				} else if nextVal.IsTask() {
					aw.waiter = &nextVal.AsTask().Future
				}
				return nextVal, val.IterYielded
			case val.IterExhausted:
				aw.subAw = val.Value{}
				return aw.finishCondWait(ctx)
			default:
				exc := ctx.ActiveException
				ctx.ClearException()
				aw.subAw = val.Value{}
				if isCancelledError(exc) {
					if !aw.hasSavedExc {
						aw.savedExc = exc
						aw.hasSavedExc = true
					}
					continue
				}
				aw.done = true
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
		}
		aw.subAw = val.Value{}
		return aw.finishCondWait(ctx)
	}
}

func (aw *pySyncAwaitable) finishCondWait(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	if aw.hasSavedExc {
		aw.done = true
		exc := aw.savedExc
		aw.savedExc = val.Value{}
		ctx.Raise(exc)
		return val.NoneVal(), val.IterError
	}
	if aw.op == syncAwaitCondWaitFor {
		aw.condState = condStateStart
		return aw.stepCondWaitFor(ctx)
	}
	aw.done = true
	return val.BoolVal(true), val.IterExhausted
}

func (aw *pySyncAwaitable) stepCondWait(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	c := aw.cond
	switch aw.condState {
	case condStateStart:
		locked, ok := c.isLocked(ctx)
		if !ok {
			aw.done = true
			return val.NoneVal(), val.IterError
		}
		if !locked {
			aw.done = true
			ctx.Raise(val.NewRuntimeError("cannot wait on un-acquired lock"))
			return val.NoneVal(), val.IterError
		}
		if !c.releaseLock(ctx) {
			aw.done = true
			return val.NoneVal(), val.IterError
		}
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		c.mu.Lock()
		c.waiters = append(c.waiters, fut)
		c.mu.Unlock()
		aw.waiter = fut
		aw.condState = condStateWaiting
		return futVal, val.IterYielded

	case condStateWaiting:
		w := aw.waiter
		aw.waiter = nil
		if w != nil {
			if w.IsCancelled() {
				c.mu.Lock()
				removeWaiterLocked(&c.waiters, w)
				c.mu.Unlock()
				if !aw.hasSavedExc {
					_, exc := w.Result()
					if exc.IsNone() || exc.HeaderPtr() == nil {
						exc = newCancelledError("")
					}
					aw.savedExc = exc
					aw.hasSavedExc = true
				}
			}
		}
		aw.condState = condStateReacquiring
		return aw.stepCondReacquire(ctx)

	case condStateReacquiring:
		return aw.stepCondReacquire(ctx)
	}
	aw.done = true
	return val.BoolVal(true), val.IterExhausted
}

func (aw *pySyncAwaitable) stepCondWaitFor(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	if aw.condState == condStateStart {
		res := ctx.Call(aw.pred, nil, nil)
		if ctx != nil && ctx.HasException() {
			aw.done = true
			return val.NoneVal(), val.IterError
		}
		if res.IsTruthy() {
			aw.done = true
			return res, val.IterExhausted
		}
	}
	return aw.stepCondWait(ctx)
}

func (aw *pySyncAwaitable) stepTaskGroupWait(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	tg := aw.tg
	if aw.waiter != nil {
		w := aw.waiter
		aw.waiter = nil
		if w.IsCancelled() {
			aw.propagateCancel = true
			if aw.cancelExc.IsNone() || aw.cancelExc.HeaderPtr() == nil {
				_, exc := w.Result()
				if exc.IsNone() || exc.HeaderPtr() == nil {
					exc = newCancelledError("")
				}
				aw.cancelExc = exc
			}
			tg.mu.Lock()
			if tg.onCompletedFut == w {
				tg.onCompletedFut = nil
			}
			tg.mu.Unlock()
			tg.abort()
		}
	}

	tg.mu.Lock()
	if len(tg.tasks) > 0 {
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		tg.onCompletedFut = fut
		aw.waiter = fut
		tg.mu.Unlock()
		return futVal, val.IterYielded
	}
	tg.exited = true
	childErrors := make([]val.Value, len(tg.errors))
	copy(childErrors, tg.errors)
	tg.errors = nil
	tg.mu.Unlock()

	aw.done = true
	bodyExc := aw.excVal
	hasBodyNonCancel := !bodyExc.IsNone() && bodyExc.HeaderPtr() != nil && !isCancelledError(bodyExc)

	if len(childErrors) == 0 {
		if hasBodyNonCancel {
			// Body raised an exception and no child failed: let body exception propagate unwrapped.
			return val.BoolVal(false), val.IterExhausted
		}
		if aw.propagateCancel {
			if isCancelledError(bodyExc) {
				return val.BoolVal(false), val.IterExhausted
			}
			ce := aw.cancelExc
			if ce.IsNone() || ce.HeaderPtr() == nil {
				ce = newCancelledError("")
			}
			ctx.Raise(ce)
			return val.NoneVal(), val.IterError
		}
		if isCancelledError(bodyExc) {
			return val.BoolVal(true), val.IterExhausted
		}
		return val.BoolVal(false), val.IterExhausted
	}

	allErrors := make([]val.Value, 0, len(childErrors)+1)
	if hasBodyNonCancel {
		allErrors = append(allErrors, bodyExc)
	}
	allErrors = append(allErrors, childErrors...)

	egCls := val.ExceptionGroupType
	for _, e := range allErrors {
		if e.HeaderPtr() == nil || e.Type() == nil || !val.IsSubclass(e.Type(), val.ExceptionType) {
			egCls = val.BaseExceptionGroupType
			break
		}
	}
	ctx.Raise(val.NewExceptionGroupVal(egCls, "unhandled errors in a TaskGroup", allErrors))
	return val.NoneVal(), val.IterError
}

func (aw *pySyncAwaitable) step(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	if aw.done {
		ctx.Raise(val.NewRuntimeError("cannot reuse already awaited coroutine"))
		return val.NoneVal(), val.IterError
	}

	switch aw.op {
	case syncAwaitResolved:
		aw.done = true
		return aw.result, val.IterExhausted

	case syncAwaitEventWait:
		e := aw.event
		if aw.waiter != nil {
			w := aw.waiter
			aw.waiter = nil
			if w.IsCancelled() {
				e.mu.Lock()
				removeWaiterLocked(&e.waiters, w)
				e.mu.Unlock()
				aw.done = true
				_, exc := w.Result()
				if exc.IsNone() || exc.HeaderPtr() == nil {
					exc = newCancelledError("")
				}
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
			aw.done = true
			return val.BoolVal(true), val.IterExhausted
		}
		e.mu.Lock()
		if e.value {
			e.mu.Unlock()
			aw.done = true
			return val.BoolVal(true), val.IterExhausted
		}
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		e.waiters = append(e.waiters, fut)
		aw.waiter = fut
		e.mu.Unlock()
		return futVal, val.IterYielded

	case syncAwaitLockAcquire, syncAwaitLockEnter:
		l := aw.lock
		isEnter := aw.op == syncAwaitLockEnter
		if aw.waiter != nil {
			w := aw.waiter
			aw.waiter = nil
			if w.IsCancelled() {
				l.mu.Lock()
				removeWaiterLocked(&l.waiters, w)
				l.mu.Unlock()
				aw.done = true
				_, exc := w.Result()
				if exc.IsNone() || exc.HeaderPtr() == nil {
					exc = newCancelledError("")
				}
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
			l.mu.Lock()
			if !l.locked {
				l.locked = true
				l.mu.Unlock()
				aw.done = true
				if isEnter {
					return val.NoneVal(), val.IterExhausted
				}
				return val.BoolVal(true), val.IterExhausted
			}
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()
			l.waiters = append(l.waiters, fut)
			aw.waiter = fut
			l.mu.Unlock()
			return futVal, val.IterYielded
		}
		l.mu.Lock()
		if !l.locked && !hasLiveWaitersLocked(l.waiters) {
			l.locked = true
			l.mu.Unlock()
			aw.done = true
			if isEnter {
				return val.NoneVal(), val.IterExhausted
			}
			return val.BoolVal(true), val.IterExhausted
		}
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		l.waiters = append(l.waiters, fut)
		aw.waiter = fut
		l.mu.Unlock()
		return futVal, val.IterYielded

	case syncAwaitLockExit:
		aw.done = true
		if !aw.lock.release(ctx) {
			return val.NoneVal(), val.IterError
		}
		return val.NoneVal(), val.IterExhausted

	case syncAwaitSemAcquire, syncAwaitSemEnter:
		s := aw.sem
		isEnter := aw.op == syncAwaitSemEnter
		if aw.waiter != nil {
			w := aw.waiter
			aw.waiter = nil
			if w.IsCancelled() {
				s.mu.Lock()
				removeWaiterLocked(&s.waiters, w)
				s.mu.Unlock()
				aw.done = true
				_, exc := w.Result()
				if exc.IsNone() || exc.HeaderPtr() == nil {
					exc = newCancelledError("")
				}
				ctx.Raise(exc)
				return val.NoneVal(), val.IterError
			}
			s.mu.Lock()
			if s.value > 0 {
				s.value--
				s.mu.Unlock()
				aw.done = true
				if isEnter {
					return val.NoneVal(), val.IterExhausted
				}
				return val.BoolVal(true), val.IterExhausted
			}
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()
			s.waiters = append(s.waiters, fut)
			aw.waiter = fut
			s.mu.Unlock()
			return futVal, val.IterYielded
		}
		s.mu.Lock()
		if s.value > 0 && !hasLiveWaitersLocked(s.waiters) {
			s.value--
			s.mu.Unlock()
			aw.done = true
			if isEnter {
				return val.NoneVal(), val.IterExhausted
			}
			return val.BoolVal(true), val.IterExhausted
		}
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		s.waiters = append(s.waiters, fut)
		aw.waiter = fut
		s.mu.Unlock()
		return futVal, val.IterYielded

	case syncAwaitSemExit:
		aw.done = true
		if !aw.sem.release(ctx) {
			return val.NoneVal(), val.IterError
		}
		return val.NoneVal(), val.IterExhausted

	case syncAwaitCondWait:
		return aw.stepCondWait(ctx)

	case syncAwaitCondWaitFor:
		return aw.stepCondWaitFor(ctx)

	case syncAwaitTimeoutEnter:
		aw.done = true
		t := aw.timeout
		var task *val.PyTask
		if ctx != nil && ctx.VM != nil {
			task = ctx.VM.(*VM).activeTask
		}
		if task == nil {
			ctx.Raise(val.NewRuntimeError("Timeout should be used inside a task"))
			return val.NoneVal(), val.IterError
		}
		loop := GetCurrentEventLoopCtx(ctx)
		if loop == nil {
			ctx.Raise(val.NewRuntimeError("no running event loop"))
			return val.NoneVal(), val.IterError
		}
		t.mu.Lock()
		if t.state != timeoutCreated {
			t.mu.Unlock()
			ctx.Raise(val.NewRuntimeError("Timeout has already been entered"))
			return val.NoneVal(), val.IterError
		}
		t.state = timeoutEntered
		t.task = task
		t.loop = loop
		t.enterCancelling = task.Cancelling()
		t.scheduleLocked()
		t.mu.Unlock()
		return val.ObjectVal(&t.inst.Header), val.IterExhausted

	case syncAwaitTimeoutExit:
		aw.done = true
		t := aw.timeout
		t.mu.Lock()
		if t.timer != nil {
			t.timer.Stop()
			t.timer = nil
		}
		t.timerGen++
		wasExpired := t.state == timeoutExpired
		cancelPending := t.cancelPending
		t.cancelPending = false
		if !wasExpired {
			t.state = timeoutFinished
		}
		task := t.task
		enterCancelling := t.enterCancelling
		t.mu.Unlock()

		if wasExpired && cancelPending && task != nil {
			remaining := task.Uncancel()
			if remaining == enterCancelling {
				if remaining == 0 {
					task.ClearPendingCancel()
				}
				if isCancelledError(aw.excVal) {
					ctx.Raise(val.NewException(TimeoutErrorType, ""))
					return val.NoneVal(), val.IterError
				}
				return val.BoolVal(false), val.IterExhausted
			}
			// An external task.cancel() also occurred; preserve CancelledError.
			return val.BoolVal(false), val.IterExhausted
		}
		return val.BoolVal(false), val.IterExhausted

	case syncAwaitTaskGroupEnter:
		aw.done = true
		tg := aw.tg
		var task *val.PyTask
		if ctx != nil && ctx.VM != nil {
			task = ctx.VM.(*VM).activeTask
		}
		if task == nil {
			ctx.Raise(val.NewRuntimeError("TaskGroup should be used inside a task"))
			return val.NoneVal(), val.IterError
		}
		loop := GetCurrentEventLoopCtx(ctx)
		if loop == nil {
			ctx.Raise(val.NewRuntimeError("no running event loop"))
			return val.NoneVal(), val.IterError
		}
		tg.mu.Lock()
		if tg.entered {
			tg.mu.Unlock()
			ctx.Raise(val.NewRuntimeError("TaskGroup has already been entered"))
			return val.NoneVal(), val.IterError
		}
		tg.entered = true
		tg.parentTask = task
		tg.loop = loop
		tg.enterCancelling = task.Cancelling()
		if tg.tasks == nil {
			tg.tasks = make(map[*val.PyTask]struct{})
		}
		tg.mu.Unlock()
		return val.ObjectVal(&tg.inst.Header), val.IterExhausted

	case syncAwaitTaskGroupExit:
		tg := aw.tg
		if aw.tgState == tgExitStart {
			aw.tgState = tgExitWaiting
			tg.mu.Lock()
			tg.exiting = true
			parentCancelReq := tg.parentCancelRequested
			parentTask := tg.parentTask
			enterCancelling := tg.enterCancelling
			tg.mu.Unlock()

			switch {
			case parentCancelReq && parentTask != nil:
				remaining := parentTask.Uncancel()
				if remaining == enterCancelling {
					if remaining == 0 {
						parentTask.ClearPendingCancel()
					}
					aw.propagateCancel = false
				} else {
					aw.propagateCancel = true
					aw.cancelExc = aw.excVal
				}
			case isCancelledError(aw.excVal):
				aw.propagateCancel = true
				aw.cancelExc = aw.excVal
				tg.abort()
			case !aw.excVal.IsNone() && aw.excVal.HeaderPtr() != nil:
				tg.abort()
			}
		}
		return aw.stepTaskGroupWait(ctx)
	}

	aw.done = true
	return val.NoneVal(), val.IterExhausted
}

func initAsyncIOSyncTypes() {
	asyncioSyncOnce.Do(func() {
		// Native awaitable type
		syncAwaitableType = val.MustNewType("_SyncAwaitable", []*val.PyType{val.ObjectType}, nil)
		syncAwaitableType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return o
		}
		syncAwaitableType.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			return asSyncAwaitable(o).step(ctx)
		}
		syncAwaitableType.Dict.Set(val.InternString("__await__"), val.NewBuiltinFuncVal("__await__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__await__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return args[0]
		}))

		// --- _EventLoop ---
		EventLoopType = val.MustNewType("_EventLoop", []*val.PyType{val.ObjectType}, nil)
		EventLoopType.Dict.Set(val.InternString("time"), val.NewBuiltinFuncVal("time", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("time() takes at least 1 argument"))
				return val.NoneVal()
			}
			return val.Float64Val(asEventLoopObj(args[0]).loop.Time())
		}))
		EventLoopType.Dict.Set(val.InternString("is_running"), val.NewBuiltinFuncVal("is_running", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.BoolVal(false)
			}
			return val.BoolVal(asEventLoopObj(args[0]).loop.IsRunning())
		}))
		EventLoopType.Dict.Set(val.InternString("create_future"), val.NewBuiltinFuncVal("create_future", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewFutureVal()
		}))
		EventLoopType.Dict.Set(val.InternString("create_task"), val.NewBuiltinFuncVal("create_task", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("create_task() missing 1 required positional argument: 'coro'"))
				return val.NoneVal()
			}
			loop := asEventLoopObj(args[0]).loop
			return createTaskOnLoop(ctx, loop, args[1:], kwnames)
		}))
		loopRepr := val.NewBuiltinFuncVal("_EventLoop.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("<_UnixSelectorEventLoop running=False>")
			}
			running := asEventLoopObj(args[0]).loop.IsRunning()
			if running {
				return val.NewStringVal("<_UnixSelectorEventLoop running=True>")
			}
			return val.NewStringVal("<_UnixSelectorEventLoop running=False>")
		})
		EventLoopType.Dict.Set(val.InternString("__repr__"), loopRepr)
		EventLoopType.Dict.Set(val.InternString("__str__"), loopRepr)
		EventLoopType.InvalidateVersion()

		// --- Event ---
		AsyncEventType = val.MustNewType("Event", []*val.PyType{val.ObjectType}, nil)
		AsyncEventType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("Event.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncEventType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			ev := &pyAsyncEvent{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
			}
			return val.ObjectVal(&ev.inst.Header)
		}))
		AsyncEventType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("Event.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		AsyncEventType.Dict.Set(val.InternString("is_set"), val.NewBuiltinFuncVal("Event.is_set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("is_set() takes at least 1 argument"))
				return val.NoneVal()
			}
			e := asAsyncEvent(args[0])
			e.mu.Lock()
			v := e.value
			e.mu.Unlock()
			return val.BoolVal(v)
		}))
		AsyncEventType.Dict.Set(val.InternString("set"), val.NewBuiltinFuncVal("Event.set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("set() takes at least 1 argument"))
				return val.NoneVal()
			}
			e := asAsyncEvent(args[0])
			e.mu.Lock()
			e.value = true
			waiters := takeAllWaitersLocked(&e.waiters)
			e.mu.Unlock()
			for _, w := range waiters {
				if w != nil && !w.IsDone() {
					w.SetResult(val.BoolVal(true))
				}
			}
			return val.NoneVal()
		}))
		AsyncEventType.Dict.Set(val.InternString("clear"), val.NewBuiltinFuncVal("Event.clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("clear() takes at least 1 argument"))
				return val.NoneVal()
			}
			e := asAsyncEvent(args[0])
			e.mu.Lock()
			e.value = false
			e.mu.Unlock()
			return val.NoneVal()
		}))
		AsyncEventType.Dict.Set(val.InternString("wait"), val.NewBuiltinFuncVal("Event.wait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("wait() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitEventWait, event: asAsyncEvent(args[0])})
		}))
		evRepr := val.NewBuiltinFuncVal("Event.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("<Event>")
			}
			e := asAsyncEvent(args[0])
			e.mu.Lock()
			set := e.value
			nw := len(e.waiters)
			e.mu.Unlock()
			status := "unset"
			if set {
				status = "set"
			}
			if nw > 0 {
				return val.NewStringVal(fmt.Sprintf("<%s at %p [%s, waiters:%d]>", e.inst.Header.Type.Name, e, status, nw))
			}
			return val.NewStringVal(fmt.Sprintf("<%s at %p [%s]>", e.inst.Header.Type.Name, e, status))
		})
		AsyncEventType.Dict.Set(val.InternString("__repr__"), evRepr)
		AsyncEventType.Dict.Set(val.InternString("__str__"), evRepr)
		AsyncEventType.InvalidateVersion()

		// --- Lock ---
		AsyncLockType = val.MustNewType("Lock", []*val.PyType{val.ObjectType}, nil)
		AsyncLockType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("Lock.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncLockType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			l := &pyAsyncLock{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
			}
			return val.ObjectVal(&l.inst.Header)
		}))
		AsyncLockType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("Lock.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		AsyncLockType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("Lock.locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("locked() takes at least 1 argument"))
				return val.NoneVal()
			}
			l := asAsyncLock(args[0])
			l.mu.Lock()
			v := l.locked
			l.mu.Unlock()
			return val.BoolVal(v)
		}))
		AsyncLockType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("Lock.acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("acquire() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitLockAcquire, lock: asAsyncLock(args[0])})
		}))
		AsyncLockType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("Lock.release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("release() takes at least 1 argument"))
				return val.NoneVal()
			}
			asAsyncLock(args[0]).release(ctx)
			return val.NoneVal()
		}))
		AsyncLockType.Dict.Set(val.InternString("__aenter__"), val.NewBuiltinFuncVal("Lock.__aenter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aenter__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitLockEnter, lock: asAsyncLock(args[0])})
		}))
		AsyncLockType.Dict.Set(val.InternString("__aexit__"), val.NewBuiltinFuncVal("Lock.__aexit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aexit__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitLockExit, lock: asAsyncLock(args[0])})
		}))
		lockRepr := val.NewBuiltinFuncVal("Lock.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("<Lock>")
			}
			l := asAsyncLock(args[0])
			l.mu.Lock()
			locked := l.locked
			nw := len(l.waiters)
			l.mu.Unlock()
			status := "unlocked"
			if locked {
				status = "locked"
			}
			if nw > 0 {
				return val.NewStringVal(fmt.Sprintf("<%s at %p [%s, waiters:%d]>", l.inst.Header.Type.Name, l, status, nw))
			}
			return val.NewStringVal(fmt.Sprintf("<%s at %p [%s]>", l.inst.Header.Type.Name, l, status))
		})
		AsyncLockType.Dict.Set(val.InternString("__repr__"), lockRepr)
		AsyncLockType.Dict.Set(val.InternString("__str__"), lockRepr)
		AsyncLockType.InvalidateVersion()

		// --- Semaphore & BoundedSemaphore ---
		AsyncSemaphoreType = val.MustNewType("Semaphore", []*val.PyType{val.ObjectType}, nil)
		AsyncBoundedSemaphoreType = val.MustNewType("BoundedSemaphore", []*val.PyType{AsyncSemaphoreType}, nil)

		AsyncSemaphoreType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("Semaphore.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncSemaphoreType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			s := &pyAsyncSemaphore{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
				value:      1,
				boundValue: 1,
				bounded:    val.IsSubclass(cls, AsyncBoundedSemaphoreType),
			}
			return val.ObjectVal(&s.inst.Header)
		}))
		AsyncSemaphoreType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("Semaphore.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("Semaphore.__init__() takes at least 1 argument"))
				return val.NoneVal()
			}
			s := asAsyncSemaphore(args[0])
			callArgs := args[1:]
			numKw := len(kwnames)
			posCount := len(callArgs) - numKw
			if posCount > 1 {
				ctx.Raise(val.NewTypeError(fmt.Sprintf("Semaphore.__init__() takes from 1 to 2 positional arguments but %d were given", posCount+1)))
				return val.NoneVal()
			}
			v := 1
			hasVal := false
			if posCount == 1 {
				if !callArgs[0].IsInt64() && !callArgs[0].IsBool() {
					ctx.Raise(val.NewTypeError("value must be an integer"))
					return val.NoneVal()
				}
				if callArgs[0].IsBool() {
					if callArgs[0].AsBool() {
						v = 1
					} else {
						v = 0
					}
				} else {
					v = int(callArgs[0].AsInt64())
				}
				hasVal = true
			}
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				kv := callArgs[posCount+i]
				if kwStr == "value" {
					if hasVal {
						ctx.Raise(val.NewTypeError("Semaphore.__init__() got multiple values for argument 'value'"))
						return val.NoneVal()
					}
					if !kv.IsInt64() && !kv.IsBool() {
						ctx.Raise(val.NewTypeError("value must be an integer"))
						return val.NoneVal()
					}
					if kv.IsBool() {
						if kv.AsBool() {
							v = 1
						} else {
							v = 0
						}
					} else {
						v = int(kv.AsInt64())
					}
					hasVal = true
				} else {
					ctx.Raise(val.NewTypeError(fmt.Sprintf("Semaphore.__init__() got an unexpected keyword argument '%s'", kwStr)))
					return val.NoneVal()
				}
			}
			if v < 0 {
				ctx.Raise(val.NewValueError("Semaphore initial value must be >= 0"))
				return val.NoneVal()
			}
			s.mu.Lock()
			s.value = v
			s.boundValue = v
			s.mu.Unlock()
			return val.NoneVal()
		}))
		AsyncSemaphoreType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("Semaphore.locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("locked() takes at least 1 argument"))
				return val.NoneVal()
			}
			s := asAsyncSemaphore(args[0])
			s.mu.Lock()
			locked := s.value == 0 || hasLiveWaitersLocked(s.waiters)
			s.mu.Unlock()
			return val.BoolVal(locked)
		}))
		AsyncSemaphoreType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("Semaphore.acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("acquire() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitSemAcquire, sem: asAsyncSemaphore(args[0])})
		}))
		AsyncSemaphoreType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("Semaphore.release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("release() takes at least 1 argument"))
				return val.NoneVal()
			}
			asAsyncSemaphore(args[0]).release(ctx)
			return val.NoneVal()
		}))
		AsyncSemaphoreType.Dict.Set(val.InternString("__aenter__"), val.NewBuiltinFuncVal("Semaphore.__aenter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aenter__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitSemEnter, sem: asAsyncSemaphore(args[0])})
		}))
		AsyncSemaphoreType.Dict.Set(val.InternString("__aexit__"), val.NewBuiltinFuncVal("Semaphore.__aexit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aexit__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitSemExit, sem: asAsyncSemaphore(args[0])})
		}))
		semRepr := val.NewBuiltinFuncVal("Semaphore.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("<Semaphore>")
			}
			s := asAsyncSemaphore(args[0])
			s.mu.Lock()
			v := s.value
			nw := len(s.waiters)
			s.mu.Unlock()
			status := fmt.Sprintf("unlocked, value:%d", v)
			if v == 0 {
				status = "locked"
			}
			if nw > 0 {
				return val.NewStringVal(fmt.Sprintf("<%s at %p [%s, waiters:%d]>", s.inst.Header.Type.Name, s, status, nw))
			}
			return val.NewStringVal(fmt.Sprintf("<%s at %p [%s]>", s.inst.Header.Type.Name, s, status))
		})
		AsyncSemaphoreType.Dict.Set(val.InternString("__repr__"), semRepr)
		AsyncSemaphoreType.Dict.Set(val.InternString("__str__"), semRepr)
		AsyncSemaphoreType.InvalidateVersion()
		AsyncBoundedSemaphoreType.InvalidateVersion()

		// --- Condition ---
		AsyncConditionType = val.MustNewType("Condition", []*val.PyType{val.ObjectType}, nil)
		AsyncConditionType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("Condition.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncConditionType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			c := &pyAsyncCondition{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
			}
			return val.ObjectVal(&c.inst.Header)
		}))
		AsyncConditionType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("Condition.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("Condition.__init__() takes at least 1 argument"))
				return val.NoneVal()
			}
			c := asAsyncCondition(args[0])
			callArgs := args[1:]
			numKw := len(kwnames)
			posCount := len(callArgs) - numKw
			if posCount > 1 {
				ctx.Raise(val.NewTypeError(fmt.Sprintf("Condition.__init__() takes from 1 to 2 positional arguments but %d were given", posCount+1)))
				return val.NoneVal()
			}
			lockVal := val.NoneVal()
			hasLock := false
			if posCount == 1 {
				lockVal = callArgs[0]
				hasLock = true
			}
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				if kwStr == "lock" {
					if hasLock {
						ctx.Raise(val.NewTypeError("Condition.__init__() got multiple values for argument 'lock'"))
						return val.NoneVal()
					}
					lockVal = callArgs[posCount+i]
					hasLock = true
				} else {
					ctx.Raise(val.NewTypeError(fmt.Sprintf("Condition.__init__() got an unexpected keyword argument '%s'", kwStr)))
					return val.NoneVal()
				}
			}
			if lockVal.IsNone() {
				l := &pyAsyncLock{
					inst: val.PyInstance{
						Header: val.ObjectHeader{Kind: val.KindInstance, Type: AsyncLockType},
						Shape:  AsyncLockType.RootShape,
					},
				}
				c.nativeLock = l
				c.lock = val.ObjectVal(&l.inst.Header)
			} else {
				c.lock = lockVal
				if val.IsInstance(lockVal, AsyncLockType) {
					c.nativeLock = asAsyncLock(lockVal)
				} else {
					c.nativeLock = nil
				}
			}
			return val.NoneVal()
		}))
		AsyncConditionType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("Condition.locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("locked() takes at least 1 argument"))
				return val.NoneVal()
			}
			v, _ := asAsyncCondition(args[0]).isLocked(ctx)
			return val.BoolVal(v)
		}))
		AsyncConditionType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("Condition.acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("acquire() takes at least 1 argument"))
				return val.NoneVal()
			}
			c := asAsyncCondition(args[0])
			if c.nativeLock != nil {
				return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitLockAcquire, lock: c.nativeLock})
			}
			fn := val.GetAttrCtx(ctx, c.lock, val.InternString("acquire"))
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			return ctx.Call(fn, nil, nil)
		}))
		AsyncConditionType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("Condition.release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("release() takes at least 1 argument"))
				return val.NoneVal()
			}
			asAsyncCondition(args[0]).releaseLock(ctx)
			return val.NoneVal()
		}))
		AsyncConditionType.Dict.Set(val.InternString("__aenter__"), val.NewBuiltinFuncVal("Condition.__aenter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aenter__() takes at least 1 argument"))
				return val.NoneVal()
			}
			c := asAsyncCondition(args[0])
			if c.nativeLock != nil {
				return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitLockEnter, lock: c.nativeLock})
			}
			fn := val.GetAttrCtx(ctx, c.lock, val.InternString("__aenter__"))
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			return ctx.Call(fn, nil, nil)
		}))
		AsyncConditionType.Dict.Set(val.InternString("__aexit__"), val.NewBuiltinFuncVal("Condition.__aexit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aexit__() takes at least 1 argument"))
				return val.NoneVal()
			}
			c := asAsyncCondition(args[0])
			if c.nativeLock != nil {
				return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitLockExit, lock: c.nativeLock})
			}
			fn := val.GetAttrCtx(ctx, c.lock, val.InternString("__aexit__"))
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			return ctx.Call(fn, args[1:], nil)
		}))
		AsyncConditionType.Dict.Set(val.InternString("wait"), val.NewBuiltinFuncVal("Condition.wait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("wait() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitCondWait, cond: asAsyncCondition(args[0])})
		}))
		AsyncConditionType.Dict.Set(val.InternString("wait_for"), val.NewBuiltinFuncVal("Condition.wait_for", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("wait_for() missing 1 required positional argument: 'predicate'"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitCondWaitFor, cond: asAsyncCondition(args[0]), pred: args[1]})
		}))
		AsyncConditionType.Dict.Set(val.InternString("notify"), val.NewBuiltinFuncVal("Condition.notify", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("notify() takes at least 1 argument"))
				return val.NoneVal()
			}
			n := 1
			if len(args) >= 2 {
				if !args[1].IsInt64() && !args[1].IsBool() {
					ctx.Raise(val.NewTypeError("n must be an integer"))
					return val.NoneVal()
				}
				if args[1].IsBool() {
					if args[1].AsBool() {
						n = 1
					} else {
						n = 0
					}
				} else {
					n = int(args[1].AsInt64())
				}
			}
			asAsyncCondition(args[0]).notify(ctx, n)
			return val.NoneVal()
		}))
		AsyncConditionType.Dict.Set(val.InternString("notify_all"), val.NewBuiltinFuncVal("Condition.notify_all", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("notify_all() takes at least 1 argument"))
				return val.NoneVal()
			}
			asAsyncCondition(args[0]).notifyAll(ctx)
			return val.NoneVal()
		}))
		AsyncConditionType.InvalidateVersion()

		// --- Timeout ---
		AsyncTimeoutType = val.MustNewType("Timeout", []*val.PyType{val.ObjectType}, nil)
		AsyncTimeoutType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("Timeout.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncTimeoutType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			t := &pyAsyncTimeout{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
				state: timeoutCreated,
			}
			return val.ObjectVal(&t.inst.Header)
		}))
		AsyncTimeoutType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("Timeout.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("Timeout.__init__() missing 1 required positional argument: 'when'"))
				return val.NoneVal()
			}
			t := asAsyncTimeout(args[0])
			w := args[1]
			t.mu.Lock()
			switch {
			case w.IsNone():
				t.hasWhen = false
				t.when = 0
			case w.IsFloat64():
				t.hasWhen = true
				t.when = w.AsFloat64()
			case w.IsInt64():
				t.hasWhen = true
				t.when = float64(w.AsInt64())
			default:
				t.mu.Unlock()
				ctx.Raise(val.NewTypeError("when must be float, int, or None"))
				return val.NoneVal()
			}
			t.mu.Unlock()
			return val.NoneVal()
		}))
		AsyncTimeoutType.Dict.Set(val.InternString("when"), val.NewBuiltinFuncVal("Timeout.when", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("when() takes at least 1 argument"))
				return val.NoneVal()
			}
			t := asAsyncTimeout(args[0])
			t.mu.Lock()
			has := t.hasWhen
			w := t.when
			t.mu.Unlock()
			if !has {
				return val.NoneVal()
			}
			return val.Float64Val(w)
		}))
		AsyncTimeoutType.Dict.Set(val.InternString("expired"), val.NewBuiltinFuncVal("Timeout.expired", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("expired() takes at least 1 argument"))
				return val.NoneVal()
			}
			t := asAsyncTimeout(args[0])
			t.mu.Lock()
			exp := t.state == timeoutExpired
			t.mu.Unlock()
			return val.BoolVal(exp)
		}))
		AsyncTimeoutType.Dict.Set(val.InternString("reschedule"), val.NewBuiltinFuncVal("Timeout.reschedule", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("reschedule() missing 1 required positional argument: 'when'"))
				return val.NoneVal()
			}
			asAsyncTimeout(args[0]).reschedule(ctx, args[1])
			return val.NoneVal()
		}))
		AsyncTimeoutType.Dict.Set(val.InternString("__aenter__"), val.NewBuiltinFuncVal("Timeout.__aenter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aenter__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitTimeoutEnter, timeout: asAsyncTimeout(args[0])})
		}))
		AsyncTimeoutType.Dict.Set(val.InternString("__aexit__"), val.NewBuiltinFuncVal("Timeout.__aexit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aexit__() takes at least 1 argument"))
				return val.NoneVal()
			}
			excVal := val.NoneVal()
			if len(args) >= 3 {
				excVal = args[2]
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitTimeoutExit, timeout: asAsyncTimeout(args[0]), excVal: excVal})
		}))
		AsyncTimeoutType.InvalidateVersion()

		// --- TaskGroup ---
		AsyncTaskGroupType = val.MustNewType("TaskGroup", []*val.PyType{val.ObjectType}, nil)
		AsyncTaskGroupType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("TaskGroup.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncTaskGroupType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			tg := &pyAsyncTaskGroup{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
			}
			return val.ObjectVal(&tg.inst.Header)
		}))
		AsyncTaskGroupType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("TaskGroup.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		AsyncTaskGroupType.Dict.Set(val.InternString("__aenter__"), val.NewBuiltinFuncVal("TaskGroup.__aenter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aenter__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitTaskGroupEnter, tg: asAsyncTaskGroup(args[0])})
		}))
		AsyncTaskGroupType.Dict.Set(val.InternString("__aexit__"), val.NewBuiltinFuncVal("TaskGroup.__aexit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__aexit__() takes at least 1 argument"))
				return val.NoneVal()
			}
			excVal := val.NoneVal()
			if len(args) >= 3 {
				excVal = args[2]
			}
			return newSyncAwaitable(&pySyncAwaitable{op: syncAwaitTaskGroupExit, tg: asAsyncTaskGroup(args[0]), excVal: excVal})
		}))
		AsyncTaskGroupType.Dict.Set(val.InternString("create_task"), val.NewBuiltinFuncVal("TaskGroup.create_task", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("create_task() missing 1 required positional argument: 'coro'"))
				return val.NoneVal()
			}
			tg := asAsyncTaskGroup(args[0])
			tg.mu.Lock()
			if !tg.entered {
				tg.mu.Unlock()
				ctx.Raise(val.NewRuntimeError("TaskGroup has not been entered"))
				return val.NoneVal()
			}
			if tg.exited {
				tg.mu.Unlock()
				ctx.Raise(val.NewRuntimeError("TaskGroup is finished"))
				return val.NoneVal()
			}
			if tg.aborting {
				tg.mu.Unlock()
				ctx.Raise(val.NewRuntimeError("TaskGroup is closing"))
				return val.NoneVal()
			}
			loop := tg.loop
			tg.mu.Unlock()

			taskVal := createTaskOnLoop(ctx, loop, args[1:], kwnames)
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			task := taskVal.AsTask()
			tg.mu.Lock()
			if tg.exited || tg.aborting {
				tg.mu.Unlock()
				task.Cancel(newCancelledError(""))
				ctx.Raise(val.NewRuntimeError("TaskGroup is closing"))
				return val.NoneVal()
			}
			tg.tasks[task] = struct{}{}
			tg.mu.Unlock()
			task.AddDoneCallback(func(val.Value) {
				tg.onTaskDone(task)
			})
			return taskVal
		}))
		AsyncTaskGroupType.InvalidateVersion()
	})
}

func createTaskOnLoop(ctx *val.ExecContext, loop *EventLoop, args []val.Value, kwnames []val.Value) val.Value {
	numKw := len(kwnames)
	posCount := len(args) - numKw
	if posCount < 1 {
		ctx.Raise(val.NewTypeError("create_task() takes at least 1 argument"))
		return val.NoneVal()
	}
	target := args[0]
	var taskName string
	hasName := false
	var customCtx *val.PyContext
	for i, kw := range kwnames {
		kwStr := kw.AsString()
		v := args[posCount+i]
		switch kwStr {
		case "name":
			if !v.IsNone() {
				taskName = v.String()
				hasName = true
			}
		case "context":
			if !v.IsNone() && v.HeaderPtr() != nil && contextType != nil && v.Type() == contextType {
				customCtx = (*pyContext)(unsafe.Pointer(v.HeaderPtr())).Ctx
			}
		default:
			ctx.Raise(val.NewTypeError(fmt.Sprintf("create_task() got an unexpected keyword argument '%s'", kwStr)))
			return val.NoneVal()
		}
	}
	parent := customCtx
	if parent == nil && ctx != nil && ctx.VM != nil {
		parent = ctx.VM.(*VM).ActiveContext()
	}
	var task *val.PyTask
	switch {
	case target.IsCoroutine():
		task = loop.CreateTaskIn(target, parent)
	case target.Type().Slots.TpIterNext != nil:
		task = loop.CreateTaskFromAwaitableIn(target, parent)
	default:
		ctx.Raise(val.NewTypeError("a coroutine was expected"))
		return val.NoneVal()
	}
	if hasName {
		task.SetName(taskName)
	}
	return val.ObjectVal(&task.Header)
}

func newTimeoutVal(hasWhen bool, when float64) val.Value {
	t := &pyAsyncTimeout{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: AsyncTimeoutType},
			Shape:  AsyncTimeoutType.RootShape,
		},
		state:   timeoutCreated,
		hasWhen: hasWhen,
		when:    when,
	}
	return val.ObjectVal(&t.inst.Header)
}
