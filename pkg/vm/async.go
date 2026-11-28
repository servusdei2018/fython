package vm

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

// AsyncMode specifies the execution scheduling strategy for coroutines.
type AsyncMode uint8

const (
	// AsyncModeSingleThread executes ready coroutines cooperatively on a single worker VM,
	// maintaining deterministic CPython event-loop compatibility while offloading blocking
	// I/O and timers to Go's runtime netpoll.
	AsyncModeSingleThread AsyncMode = iota

	// AsyncModeMultiCore multiplexes ready coroutines across a pool of worker goroutines,
	// executing independent coroutines and gather() tasks in parallel across CPU cores.
	AsyncModeMultiCore
)

type taskNode struct {
	task *val.PyTask
	next atomic.Pointer[taskNode]
}

// LockFreeTaskQueue implements a lock-free FIFO queue (Michael-Scott queue)
// for enqueueing and dequeueing ready tasks with 0 mutex contention.
type LockFreeTaskQueue struct {
	head atomic.Pointer[taskNode]
	tail atomic.Pointer[taskNode]
	wake chan struct{}
}

// NewLockFreeTaskQueue initializes a new LockFreeTaskQueue.
func NewLockFreeTaskQueue() *LockFreeTaskQueue {
	dummy := &taskNode{}
	q := &LockFreeTaskQueue{
		wake: make(chan struct{}, 1),
	}
	q.head.Store(dummy)
	q.tail.Store(dummy)
	return q
}

// Push enqueues a ready task using atomic CAS operations.
func (q *LockFreeTaskQueue) Push(t *val.PyTask) {
	node := &taskNode{task: t}
	for {
		tail := q.tail.Load()
		next := tail.next.Load()
		if tail == q.tail.Load() {
			if next == nil {
				if tail.next.CompareAndSwap(nil, node) {
					q.tail.CompareAndSwap(tail, node)
					select {
					case q.wake <- struct{}{}:
					default:
					}
					return
				}
			} else {
				q.tail.CompareAndSwap(tail, next)
			}
		}
	}
}

// Pop dequeues the next ready task using atomic CAS operations, or returns nil if empty.
func (q *LockFreeTaskQueue) Pop() *val.PyTask {
	for {
		head := q.head.Load()
		tail := q.tail.Load()
		next := head.next.Load()
		if head == q.head.Load() {
			if head == tail {
				if next == nil {
					return nil
				}
				q.tail.CompareAndSwap(tail, next)
			} else {
				if next == nil {
					return nil
				}
				t := next.task
				if q.head.CompareAndSwap(head, next) {
					return t
				}
			}
		}
	}
}

// EventLoop manages coroutine scheduling and bridges tasks to Go's runtime network poller.
type EventLoop struct {
	engine     *Engine
	vm         *VM
	ownsVM     bool
	queue      *LockFreeTaskQueue
	mode       AsyncMode
	stopChan   chan struct{}
	running    atomic.Bool
	activeTask atomic.Pointer[val.PyTask]
	callSoonMu sync.Mutex
	callSoon   []func()
	tasksMu    sync.Mutex
	tasks      map[*val.PyTask]struct{}
	taskSeq    atomic.Uint64
}

// Time returns the event loop's monotonic clock in seconds (sharing time.monotonic()).
func (l *EventLoop) Time() float64 {
	return time.Since(startTime).Seconds()
}

// IsRunning reports whether the event loop is currently executing RunUntilComplete.
func (l *EventLoop) IsRunning() bool {
	return l != nil && l.running.Load()
}

func (l *EventLoop) trackTask(task *val.PyTask) {
	if l == nil || task == nil {
		return
	}
	if task.GetName() == "" {
		seq := l.taskSeq.Add(1)
		task.SetName(fmt.Sprintf("Task-%d", seq))
	}
	l.tasksMu.Lock()
	if l.tasks == nil {
		l.tasks = make(map[*val.PyTask]struct{})
	}
	l.tasks[task] = struct{}{}
	l.tasksMu.Unlock()
	task.AddDoneCallback(func(val.Value) {
		l.untrackTask(task)
	})
}

func (l *EventLoop) untrackTask(task *val.PyTask) {
	if l == nil || task == nil {
		return
	}
	l.tasksMu.Lock()
	delete(l.tasks, task)
	l.tasksMu.Unlock()
}

// AllTasks returns all currently pending (non-done) tasks registered on this loop.
func (l *EventLoop) AllTasks() []*val.PyTask {
	if l == nil {
		return nil
	}
	l.tasksMu.Lock()
	out := make([]*val.PyTask, 0, len(l.tasks))
	for t := range l.tasks {
		if !t.IsDone() {
			out = append(out, t)
		}
	}
	l.tasksMu.Unlock()
	return out
}

// CallSoonThreadsafe schedules fn to run on the event loop thread.
func (l *EventLoop) CallSoonThreadsafe(fn func()) {
	if l == nil || fn == nil {
		return
	}
	l.callSoonMu.Lock()
	l.callSoon = append(l.callSoon, fn)
	l.callSoonMu.Unlock()
	select {
	case l.queue.wake <- struct{}{}:
	default:
	}
}

func (l *EventLoop) drainCallSoon() {
	l.callSoonMu.Lock()
	if len(l.callSoon) == 0 {
		l.callSoonMu.Unlock()
		return
	}
	cbs := l.callSoon
	l.callSoon = nil
	l.callSoonMu.Unlock()
	for _, fn := range cbs {
		fn()
	}
}

var (
	currentEventLoop atomic.Pointer[EventLoop]
)

// GetCurrentEventLoop returns the thread/process active event loop.
func GetCurrentEventLoop() *EventLoop {
	return currentEventLoop.Load()
}

// GetCurrentEventLoopCtx returns the active event loop for ctx/VM, falling back to process-wide loop.
func GetCurrentEventLoopCtx(ctx *val.ExecContext) *EventLoop {
	if ctx != nil && ctx.VM != nil {
		if v, ok := ctx.VM.(*VM); ok && v.eventLoop != nil {
			return v.eventLoop
		}
	}
	return currentEventLoop.Load()
}

// EventLoop returns the active event loop associated with this VM.
func (vm *VM) EventLoop() *EventLoop {
	return vm.eventLoop
}

// SetEventLoop sets the active event loop associated with this VM.
func (vm *VM) SetEventLoop(loop *EventLoop) {
	vm.eventLoop = loop
}

// SetCurrentEventLoop sets the active event loop.
func SetCurrentEventLoop(loop *EventLoop) {
	currentEventLoop.Store(loop)
}

// NewEventLoop creates an EventLoop attached to the provided Engine.
func NewEventLoop(e *Engine, mode AsyncMode) *EventLoop {
	return NewEventLoopWithVM(e, mode, nil)
}

// NewEventLoopWithVM creates an EventLoop, optionally reusing an existing VM instance.
func NewEventLoopWithVM(e *Engine, mode AsyncMode, currentVM *VM) *EventLoop {
	if e == nil {
		e = DefaultEngine()
	}
	loop := &EventLoop{
		engine:   e,
		vm:       currentVM,
		ownsVM:   currentVM == nil,
		queue:    NewLockFreeTaskQueue(),
		mode:     mode,
		stopChan: make(chan struct{}),
	}
	if loop.vm == nil {
		loop.vm = e.AcquireVM()
		loop.ownsVM = true
	}
	if loop.vm != nil {
		loop.vm.eventLoop = loop
	}
	return loop
}

// CreateTask wraps a coroutine in a PyTask and enqueues it on the EventLoop.
// The task gets an empty context. Prefer CreateTaskIn to propagate the caller.
func (l *EventLoop) CreateTask(coroVal val.Value) *val.PyTask {
	return l.CreateTaskIn(coroVal, nil)
}

func (l *EventLoop) cancelTask(task *val.PyTask, exc val.Value) bool {
	if task.IsDone() {
		return false
	}
	w, waitAw := task.PeekWaiter()
	if !waitAw.IsNone() && waitAw.IsTask() {
		if waitAw.AsTask().Cancel(exc) {
			return true
		}
	} else if w != nil {
		if w.Cancel(exc) {
			return true
		}
	}
	task.SetPendingCancel(exc)
	l.queue.Push(task)
	return true
}

// CreateTaskIn wraps a coroutine and stores a shallow copy of parent.
// A nil parent starts from an empty context. The copy shares the parent's
// snapshot, so spawn stays O(1) until the task calls ContextVar.set.
func (l *EventLoop) CreateTaskIn(coroVal val.Value, parent *val.PyContext) *val.PyTask {
	coro := coroVal.AsCoroutine()
	taskVal := val.NewTaskVal(coro)
	task := taskVal.AsTask()
	task.Loop = l
	task.SetCancelHook(l.cancelTask)
	if parent == nil {
		task.Context = val.NewPyContext()
	} else {
		task.Context = parent.ShallowCopy()
	}
	l.trackTask(task)
	l.queue.Push(task)
	return task
}

// CreateTaskFromAwaitableIn wraps a non-coroutine awaitable (such as a native
// asyncio.Queue awaitable) in a PyTask and enqueues it on the EventLoop.
func (l *EventLoop) CreateTaskFromAwaitableIn(aw val.Value, parent *val.PyContext) *val.PyTask {
	taskVal := val.NewTaskVal(nil)
	task := taskVal.AsTask()
	task.Awaitable = aw
	task.Loop = l
	task.SetCancelHook(l.cancelTask)
	if parent == nil {
		task.Context = val.NewPyContext()
	} else {
		task.Context = parent.ShallowCopy()
	}
	l.trackTask(task)
	l.queue.Push(task)
	return task
}

// ScheduleCallback schedules a callable to be run with the given arguments on the event loop.
func (l *EventLoop) ScheduleCallback(callable val.Value, args ...val.Value) {
	go func() {
		workerVM := l.engine.AcquireVM()
		defer l.engine.ReleaseVM(workerVM)
		res := workerVM.Vectorcall(callable, args, nil)
		if res.IsCoroutine() {
			l.CreateTask(res)
		}
	}()
}

// RunUntilComplete runs the EventLoop until the specified awaitable (coroutine, future, or task) completes.
func (l *EventLoop) RunUntilComplete(target val.Value) (val.Value, error) {
	return l.RunUntilCompleteIn(target, nil)
}

func (l *EventLoop) RunUntilCompleteIn(target val.Value, parent *val.PyContext) (val.Value, error) {
	prevLoop := GetCurrentEventLoop()
	SetCurrentEventLoop(l)
	if l.vm != nil {
		l.vm.eventLoop = l
	}
	defer func() {
		SetCurrentEventLoop(prevLoop)
		if l.vm != nil {
			l.vm.eventLoop = nil
			if l.ownsVM {
				l.engine.ReleaseVM(l.vm)
			}
			l.vm = nil
		}
	}()

	l.running.Store(true)
	defer l.running.Store(false)

	var rootTask *val.PyTask
	switch {
	case target.IsCoroutine():
		rootTask = l.CreateTaskIn(target, parent)
	case target.IsTask():
		rootTask = target.AsTask()
		l.trackTask(rootTask)
		l.queue.Push(rootTask)
	case target.IsFuture():
		fut := target.AsFuture()
		<-fut.DoneChan()
		res, exc := fut.Result()
		if !exc.IsNone() && exc.HeaderPtr() != nil {
			return val.NoneVal(), fmt.Errorf("%s", exc.String())
		}
		return res, nil
	default:
		return val.NoneVal(), fmt.Errorf("TypeError: a coroutine was expected, got %s", target.Type().Name)
	}

	l.activeTask.Store(rootTask)

	if l.mode == AsyncModeMultiCore {
		return l.runMultiCore(rootTask)
	}
	return l.runSingleThread(rootTask)
}

func (l *EventLoop) runSingleThread(rootTask *val.PyTask) (val.Value, error) {
	for !rootTask.IsDone() {
		l.drainCallSoon()
		if rootTask.IsDone() {
			break
		}
		task := l.queue.Pop()
		if task == nil {
			if rootTask.IsDone() {
				break
			}
			select {
			case <-l.queue.wake:
				continue
			case <-rootTask.Future.DoneChan():
				break
			case <-l.stopChan:
				break
			}
		}
		if task != nil {
			l.stepTask(l.vm, task)
		}
	}

	res, exc := rootTask.Result()
	if !exc.IsNone() && exc.HeaderPtr() != nil {
		return val.NoneVal(), fmt.Errorf("%s", exc.String())
	}
	return res, nil
}

func (l *EventLoop) runMultiCore(rootTask *val.PyTask) (val.Value, error) {
	numWorkers := max(runtime.NumCPU(), 2)

	var wg sync.WaitGroup
	done := make(chan struct{})

	for range numWorkers {
		wg.Go(func() {
			workerVM := l.engine.AcquireVM()
			defer l.engine.ReleaseVM(workerVM)

			for {
				select {
				case <-done:
					return
				default:
				}

				l.drainCallSoon()
				task := l.queue.Pop()
				if task == nil {
					select {
					case <-l.queue.wake:
						continue
					case <-done:
						return
					}
				}
				l.stepTask(workerVM, task)
				if rootTask.IsDone() {
					return
				}
			}
		})
	}

	// Wait for rootTask completion
	<-rootTask.Future.DoneChan()
	close(done)
	// Wake workers so they can exit
	for range numWorkers * 2 {
		select {
		case l.queue.wake <- struct{}{}:
		default:
		}
	}
	wg.Wait()

	res, exc := rootTask.Result()
	if !exc.IsNone() && exc.HeaderPtr() != nil {
		return val.NoneVal(), fmt.Errorf("%s", exc.String())
	}
	return res, nil
}

func isCancelledError(exc val.Value) bool {
	if CancelledErrorType == nil || exc.IsNone() || exc.HeaderPtr() == nil {
		return false
	}
	return val.IsSubclass(exc.Type(), CancelledErrorType)
}

func (l *EventLoop) stepTask(vm *VM, task *val.PyTask) {
	task.StepLock()
	defer task.StepUnlock()
	if task.IsDone() {
		return
	}
	if task.Context != nil {
		prev := vm.context
		vm.context = task.Context
		defer func() { vm.context = prev }()
	}

	_, waitAw := task.ClearWaiter()
	pendingCancel := task.TakePendingCancel()

	prevTask := vm.activeTask
	vm.activeTask = task
	defer func() { vm.activeTask = prevTask }()

	// Direct non-coroutine awaitable task (e.g. create_task(q.get())).
	if task.Coro == nil {
		aw := task.Awaitable
		if aw.IsNone() || aw.HeaderPtr() == nil {
			return
		}
		if !pendingCancel.IsNone() && pendingCancel.HeaderPtr() != nil {
			if isSyncAwaitableNeedingCancelStep(aw) {
				nextVal, status := stepCancelSyncAwaitable(&vm.execCtx, aw, pendingCancel)
				switch status {
				case val.IterExhausted:
					task.Awaitable = val.Value{}
					task.SetResult(nextVal)
				case val.IterYielded:
					if nextVal.IsFuture() || nextVal.IsTask() {
						fut := nextVal.AsFuture()
						if nextVal.IsTask() {
							fut = &nextVal.AsTask().Future
						}
						task.SetWaiter(fut, aw)
						fut.AddDoneCallback(func(v val.Value) {
							l.queue.Push(task)
						})
					} else {
						l.queue.Push(task)
					}
				default:
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if exc.IsNone() || exc.HeaderPtr() == nil {
						exc = pendingCancel
					}
					task.Awaitable = val.Value{}
					if isCancelledError(exc) {
						task.Future.Cancel(exc)
					} else {
						task.SetException(exc)
					}
				}
				return
			}
			cancelPendingAwaitable(aw)
			if val.IsAsyncGenASend(aw) || val.IsAsyncGenAThrow(aw) {
				var nextVal val.Value
				var status val.IterStatus
				if val.IsAsyncGenASend(aw) {
					nextVal, status = val.AsAsyncGenASend(aw).Step(&vm.execCtx, val.NoneVal(), pendingCancel)
				} else {
					nextVal, status = val.AsAsyncGenAThrow(aw).Step(&vm.execCtx, val.NoneVal(), pendingCancel)
				}
				switch status {
				case val.IterExhausted:
					task.Awaitable = val.Value{}
					task.SetResult(nextVal)
				case val.IterYielded:
					if nextVal.IsFuture() || nextVal.IsTask() {
						fut := nextVal.AsFuture()
						if nextVal.IsTask() {
							fut = &nextVal.AsTask().Future
						}
						task.SetWaiter(fut, aw)
						fut.AddDoneCallback(func(v val.Value) {
							l.queue.Push(task)
						})
					} else {
						l.queue.Push(task)
					}
				default:
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if exc.IsNone() || exc.HeaderPtr() == nil {
						exc = pendingCancel
					}
					task.Awaitable = val.Value{}
					if isCancelledError(exc) {
						task.Future.Cancel(exc)
					} else {
						task.SetException(exc)
					}
				}
				return
			}
			task.Awaitable = val.Value{}
			task.Future.Cancel(pendingCancel)
			return
		}
		tpIterNext := aw.Type().Slots.TpIterNext
		if tpIterNext == nil {
			task.Awaitable = val.Value{}
			task.SetResult(aw)
			return
		}
		nextVal, status := tpIterNext(&vm.execCtx, aw)
		switch status {
		case val.IterExhausted:
			task.Awaitable = val.Value{}
			task.SetResult(nextVal)
		case val.IterYielded:
			if nextVal.IsFuture() || nextVal.IsTask() {
				fut := nextVal.AsFuture()
				if nextVal.IsTask() {
					fut = &nextVal.AsTask().Future
				}
				task.SetWaiter(fut, aw)
				fut.AddDoneCallback(func(v val.Value) {
					l.queue.Push(task)
				})
			} else {
				l.queue.Push(task)
			}
		default:
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			if exc.IsNone() || exc.HeaderPtr() == nil {
				exc = vm.LastException()
			}
			task.Awaitable = val.Value{}
			if isCancelledError(exc) {
				task.Future.Cancel(exc)
			} else {
				task.SetException(exc)
			}
		}
		return
	}

	coro := task.Coro
	if coro.State == val.CoroClosed {
		return
	}

	if !pendingCancel.IsNone() && pendingCancel.HeaderPtr() != nil {
		if !waitAw.IsNone() && waitAw.HeaderPtr() != nil {
			if isSyncAwaitableNeedingCancelStep(waitAw) {
				nextVal, status := stepCancelSyncAwaitable(&vm.execCtx, waitAw, pendingCancel)
				switch status {
				case val.IterYielded:
					if nextVal.IsFuture() || nextVal.IsTask() {
						fut := nextVal.AsFuture()
						if nextVal.IsTask() {
							fut = &nextVal.AsTask().Future
						}
						task.SetWaiter(fut, waitAw)
						fut.AddDoneCallback(func(v val.Value) {
							l.queue.Push(task)
						})
					} else {
						l.queue.Push(task)
					}
					return
				case val.IterExhausted:
					if coro.State == val.CoroSuspended && coro.SP > 0 && coro.SP <= len(coro.Stack) {
						coro.Stack[coro.SP-1] = nextVal
						coro.IP++
					}
					pendingCancel = val.Value{}
				default:
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if !exc.IsNone() && exc.HeaderPtr() != nil {
						pendingCancel = exc
					}
				}
			} else {
				cancelPendingAwaitable(waitAw)
			}
			if val.IsAsyncGenASend(waitAw) || val.IsAsyncGenAThrow(waitAw) {
				var nextVal val.Value
				var status val.IterStatus
				if val.IsAsyncGenASend(waitAw) {
					nextVal, status = val.AsAsyncGenASend(waitAw).Step(&vm.execCtx, val.NoneVal(), pendingCancel)
				} else {
					nextVal, status = val.AsAsyncGenAThrow(waitAw).Step(&vm.execCtx, val.NoneVal(), pendingCancel)
				}
				switch status {
				case val.IterYielded:
					if nextVal.IsFuture() || nextVal.IsTask() {
						fut := nextVal.AsFuture()
						if nextVal.IsTask() {
							fut = &nextVal.AsTask().Future
						}
						task.SetWaiter(fut, waitAw)
						fut.AddDoneCallback(func(v val.Value) {
							l.queue.Push(task)
						})
					} else {
						l.queue.Push(task)
					}
					return
				case val.IterExhausted:
					// Async generator suppressed CancelledError and yielded a value.
					// Push the yielded value into the suspended coroutine's OP_AWAIT result slot
					// and advance past OP_AWAIT so the coroutine resumes with nextVal.
					if coro.State == val.CoroSuspended && coro.SP > 0 && coro.SP <= len(coro.Stack) {
						coro.Stack[coro.SP-1] = nextVal
						coro.IP++
					}
					pendingCancel = val.Value{}
				default:
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if !exc.IsNone() && exc.HeaderPtr() != nil {
						pendingCancel = exc
					}
				}
			}
		}
		if !pendingCancel.IsNone() && pendingCancel.HeaderPtr() != nil {
			if coro.State == val.CoroCreated {
				coro.State = val.CoroClosed
				task.Coro = nil
				vm.releaseCoroutine(coro)
				task.Future.Cancel(pendingCancel)
				return
			}
			vm.execCtx.Raise(pendingCancel)
		}
	}

	sent := task.NextSent
	task.NextSent = val.NoneVal()

	_, status := vm.resumeCoroutine(&vm.execCtx, coro, sent)
	switch status {
	case val.IterExhausted:
		task.SetResult(coro.RetVal)
		task.Coro = nil
		vm.releaseCoroutine(coro)
	case val.IterYielded:
		aw := coro.YieldVal
		if aw.IsFuture() || aw.IsTask() {
			fut := aw.AsFuture()
			if aw.IsTask() {
				fut = &aw.AsTask().Future
			}
			if w, _ := task.PeekWaiter(); w != fut {
				task.SetWaiter(fut, aw)
			}
			fut.AddDoneCallback(func(v val.Value) {
				l.queue.Push(task)
			})
		} else {
			l.queue.Push(task)
		}
	case val.IterError:
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		if exc.IsNone() || exc.HeaderPtr() == nil {
			exc = vm.LastException()
		}
		vm.lastException = val.NoneVal()
		if isCancelledError(exc) {
			task.Future.Cancel(exc)
		} else {
			task.SetException(exc)
		}
		task.Coro = nil
		vm.releaseCoroutine(coro)
	}
}
