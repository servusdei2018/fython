package vm

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

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
	queue      *LockFreeTaskQueue
	mode       AsyncMode
	stopChan   chan struct{}
	running    atomic.Bool
	activeTask atomic.Pointer[val.PyTask]
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
	if e == nil {
		e = DefaultEngine()
	}
	loop := &EventLoop{
		engine:   e,
		vm:       e.AcquireVM(),
		queue:    NewLockFreeTaskQueue(),
		mode:     mode,
		stopChan: make(chan struct{}),
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

// CreateTaskIn wraps a coroutine and stores a shallow copy of parent.
// A nil parent starts from an empty context. The copy shares the parent's
// snapshot, so spawn stays O(1) until the task calls ContextVar.set.
func (l *EventLoop) CreateTaskIn(coroVal val.Value, parent *val.PyContext) *val.PyTask {
	coro := coroVal.AsCoroutine()
	taskVal := val.NewTaskVal(coro)
	task := taskVal.AsTask()
	task.Loop = l
	if parent == nil {
		task.Context = val.NewPyContext()
	} else {
		task.Context = parent.ShallowCopy()
	}
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
			l.engine.ReleaseVM(l.vm)
			l.vm = nil
		}
	}()

	l.running.Store(true)
	defer l.running.Store(false)

	var rootTask *val.PyTask
	if target.IsCoroutine() {
		rootTask = l.CreateTaskIn(target, parent)
	} else if target.IsTask() {
		rootTask = target.AsTask()
		l.queue.Push(rootTask)
	} else if target.IsFuture() {
		fut := target.AsFuture()
		<-fut.DoneChan()
		res, exc := fut.Result()
		if !exc.IsNone() && exc.HeaderPtr() != nil {
			return val.NoneVal(), fmt.Errorf("%s", exc.String())
		}
		return res, nil
	} else {
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
	numWorkers := runtime.NumCPU()
	if numWorkers < 2 {
		numWorkers = 2
	}

	var wg sync.WaitGroup
	done := make(chan struct{})

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerVM := l.engine.AcquireVM()
			defer l.engine.ReleaseVM(workerVM)

			for {
				select {
				case <-done:
					return
				default:
				}

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
		}()
	}

	// Wait for rootTask completion
	<-rootTask.Future.DoneChan()
	close(done)
	// Wake workers so they can exit
	for w := 0; w < numWorkers*2; w++ {
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

func (l *EventLoop) stepTask(vm *VM, task *val.PyTask) {
	coro := task.Coro
	if coro == nil || coro.State == val.CoroClosed {
		return
	}
	if task.Context != nil {
		prev := vm.context
		vm.context = task.Context
		defer func() { vm.context = prev }()
	}

	sent := task.NextSent
	task.NextSent = val.NoneVal()

	_, status := vm.resumeCoroutine(&vm.execCtx, coro, sent)
	if status == val.IterExhausted {
		task.SetResult(coro.RetVal)
		vm.releaseCoroutine(coro)
	} else if status == val.IterYielded {
		aw := coro.YieldVal
		if aw.IsFuture() || aw.IsTask() {
			fut := aw.AsFuture()
			if aw.IsTask() {
				fut = &aw.AsTask().Future
			}
			fut.AddDoneCallback(func(v val.Value) {
				l.queue.Push(task)
			})
		} else {
			l.queue.Push(task)
		}
	} else if status == val.IterError {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		if exc.IsNone() || exc.HeaderPtr() == nil {
			exc = vm.LastException()
		}
		task.SetException(exc)
		vm.releaseCoroutine(coro)
	}
}
