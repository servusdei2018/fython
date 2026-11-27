package vm

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	QueueEmptyType     *val.PyType
	QueueFullType      *val.PyType
	QueueShutDownType  *val.PyType
	AsyncQueueType     *val.PyType
	LifoQueueType      *val.PyType
	PriorityQueueType  *val.PyType
	queueAwaitableType *val.PyType
	asyncioQueueOnce   sync.Once
)

type asyncQueueKind uint8

const (
	queueKindFifo asyncQueueKind = iota
	queueKindLifo
	queueKindPriority
)

type queueAwaitOp uint8

const (
	queueAwaitGet queueAwaitOp = iota
	queueAwaitPut
	queueAwaitJoin
)

// pyAsyncQueue implements asyncio.Queue, asyncio.LifoQueue, and asyncio.PriorityQueue.
// inst MUST be at offset 0 so Python subclasses and attribute access work natively.
type pyAsyncQueue struct {
	inst            val.PyInstance
	mu              sync.Mutex
	kind            asyncQueueKind
	maxsize         int
	buf             []val.Value // FIFO: power-of-2 ring; LIFO: stack; Priority: min-heap
	head            int         // FIFO ring head index
	count           int         // FIFO active item count
	mask            int         // FIFO ring mask (len(buf) - 1)
	unfinishedTasks int
	shutDown        bool
	getters         []*val.PyFuture
	putters         []*val.PyFuture
	joinWaiters     []*val.PyFuture
}

// pyQueueAwaitable is the native awaitable returned by Queue.get(), Queue.put(), and Queue.join().
// Header MUST be at offset 0.
type pyQueueAwaitable struct {
	Header val.ObjectHeader
	q      *pyAsyncQueue
	op     queueAwaitOp
	item   val.Value
	waiter *val.PyFuture
	done   bool
}

var (
	_ [0 - unsafe.Offsetof(pyAsyncQueue{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyQueueAwaitable{}.Header)]byte
)

func asAsyncQueue(v val.Value) *pyAsyncQueue {
	return (*pyAsyncQueue)(val.AsPointer(v))
}

func asQueueAwaitable(v val.Value) *pyQueueAwaitable {
	return (*pyQueueAwaitable)(unsafe.Pointer(v.HeaderPtr()))
}

func queueKindForType(cls *val.PyType) asyncQueueKind {
	if cls == PriorityQueueType || (PriorityQueueType != nil && val.IsSubclass(cls, PriorityQueueType)) {
		return queueKindPriority
	}
	if cls == LifoQueueType || (LifoQueueType != nil && val.IsSubclass(cls, LifoQueueType)) {
		return queueKindLifo
	}
	return queueKindFifo
}

func newCancelledError(msg string) val.Value {
	if CancelledErrorType != nil {
		return val.NewException(CancelledErrorType, msg)
	}
	return val.NewException(val.BaseExceptionType, msg)
}

// cancelQueueAwaitable runs CPython-compatible waiter cleanup and handoff when
// a task suspended on a pyQueueAwaitable is cancelled between wakeup and resumption.
func cancelQueueAwaitable(v val.Value) {
	if queueAwaitableType == nil || v.IsNone() || v.HeaderPtr() == nil {
		return
	}
	if v.Type() != queueAwaitableType {
		return
	}
	asQueueAwaitable(v).onCancel()
}

func (q *pyAsyncQueue) lenLocked() int {
	if q.kind == queueKindFifo {
		return q.count
	}
	return len(q.buf)
}

func (q *pyAsyncQueue) fullLocked() bool {
	return q.maxsize > 0 && q.lenLocked() >= q.maxsize
}

func (q *pyAsyncQueue) fifoGrowLocked() {
	newCap := max(len(q.buf)*2, 8)
	newBuf := make([]val.Value, newCap)
	if q.count > 0 {
		first := len(q.buf) - q.head
		if first >= q.count {
			copy(newBuf, q.buf[q.head:q.head+q.count])
		} else {
			copy(newBuf, q.buf[q.head:])
			copy(newBuf[first:], q.buf[:q.count-first])
		}
	}
	q.buf = newBuf
	q.head = 0
	q.mask = newCap - 1
}

func (q *pyAsyncQueue) fifoPushLocked(item val.Value) {
	if q.count == len(q.buf) {
		q.fifoGrowLocked()
	}
	q.buf[(q.head+q.count)&q.mask] = item
	q.count++
}

func (q *pyAsyncQueue) fifoPopLocked() val.Value {
	item := q.buf[q.head]
	q.buf[q.head] = val.Value{}
	q.head = (q.head + 1) & q.mask
	q.count--
	return item
}

func (q *pyAsyncQueue) lifoPushLocked(item val.Value) {
	q.buf = append(q.buf, item)
}

func (q *pyAsyncQueue) lifoPopLocked() val.Value {
	last := len(q.buf) - 1
	item := q.buf[last]
	q.buf[last] = val.Value{}
	q.buf = q.buf[:last]
	return item
}

func (q *pyAsyncQueue) drainLocked() int {
	if q.kind == queueKindFifo {
		n := q.count
		for i := range n {
			q.buf[(q.head+i)&q.mask] = val.Value{}
		}
		q.head = 0
		q.count = 0
		return n
	}
	n := len(q.buf)
	clear(q.buf)
	q.buf = q.buf[:0]
	return n
}

func (q *pyAsyncQueue) snapshotItemsLocked() []val.Value {
	n := q.lenLocked()
	if n == 0 {
		return nil
	}
	out := make([]val.Value, n)
	if q.kind == queueKindFifo {
		first := len(q.buf) - q.head
		if first >= n {
			copy(out, q.buf[q.head:q.head+n])
		} else {
			copy(out, q.buf[q.head:])
			copy(out[first:], q.buf[:n-first])
		}
		return out
	}
	copy(out, q.buf)
	return out
}

func popNextWaiterLocked(waiters *[]*val.PyFuture) *val.PyFuture {
	s := *waiters
	for len(s) > 0 {
		f := s[0]
		s[0] = nil
		s = s[1:]
		if f != nil && !f.IsDone() {
			if len(s) == 0 {
				*waiters = (*waiters)[:0]
			} else {
				*waiters = s
			}
			return f
		}
	}
	*waiters = (*waiters)[:0]
	return nil
}

func removeWaiterLocked(waiters *[]*val.PyFuture, target *val.PyFuture) {
	s := *waiters
	for i, f := range s {
		if f == target {
			copy(s[i:], s[i+1:])
			last := len(s) - 1
			s[last] = nil
			*waiters = s[:last]
			return
		}
	}
}

func takeAllWaitersLocked(waiters *[]*val.PyFuture) []*val.PyFuture {
	if len(*waiters) == 0 {
		return nil
	}
	out := make([]*val.PyFuture, len(*waiters))
	copy(out, *waiters)
	clear(*waiters)
	*waiters = (*waiters)[:0]
	return out
}

func (q *pyAsyncQueue) putNowait(ctx *val.ExecContext, item val.Value) bool {
	if q.kind == queueKindPriority {
		return q.priorityPutNowait(ctx, item)
	}
	q.mu.Lock()
	if q.shutDown {
		q.mu.Unlock()
		ctx.Raise(val.NewException(QueueShutDownType, ""))
		return false
	}
	if q.fullLocked() {
		q.mu.Unlock()
		ctx.Raise(val.NewException(QueueFullType, ""))
		return false
	}
	if q.kind == queueKindFifo {
		q.fifoPushLocked(item)
	} else {
		q.lifoPushLocked(item)
	}
	q.unfinishedTasks++
	w := popNextWaiterLocked(&q.getters)
	q.mu.Unlock()
	if w != nil {
		w.SetResult(val.NoneVal())
	}
	return true
}

func (q *pyAsyncQueue) priorityPutNowait(ctx *val.ExecContext, item val.Value) bool {
	q.mu.Lock()
	if q.shutDown {
		q.mu.Unlock()
		ctx.Raise(val.NewException(QueueShutDownType, ""))
		return false
	}
	if q.fullLocked() {
		q.mu.Unlock()
		ctx.Raise(val.NewException(QueueFullType, ""))
		return false
	}
	if len(q.buf) == 0 {
		q.buf = append(q.buf, item)
		q.unfinishedTasks++
		w := popNextWaiterLocked(&q.getters)
		q.mu.Unlock()
		if w != nil {
			w.SetResult(val.NoneVal())
		}
		return true
	}

	pos := len(q.buf)
	q.buf = append(q.buf, item)
	type heapShift struct {
		parentIdx int
		parentVal val.Value
	}
	var moved [64]heapShift
	nMoved := 0

	for pos > 0 {
		parentPos := (pos - 1) >> 1
		if parentPos >= len(q.buf) || pos >= len(q.buf) {
			break
		}
		parent := q.buf[parentPos]
		q.mu.Unlock()
		ltRes := val.RichCompare(ctx, item, parent, val.CmpLT)
		q.mu.Lock()
		if ctx != nil && ctx.HasException() {
			for k := nMoved - 1; k >= 0; k-- {
				if moved[k].parentIdx < len(q.buf) {
					q.buf[moved[k].parentIdx] = moved[k].parentVal
				}
			}
			if len(q.buf) > 0 {
				last := len(q.buf) - 1
				q.buf[last] = val.Value{}
				q.buf = q.buf[:last]
			}
			q.mu.Unlock()
			return false
		}
		if !ltRes.IsTruthy() {
			break
		}
		if parentPos >= len(q.buf) || pos >= len(q.buf) {
			break
		}
		q.buf[pos] = parent
		if nMoved < len(moved) {
			moved[nMoved] = heapShift{parentIdx: parentPos, parentVal: parent}
			nMoved++
		}
		pos = parentPos
	}
	if pos < len(q.buf) {
		q.buf[pos] = item
	}
	q.unfinishedTasks++
	w := popNextWaiterLocked(&q.getters)
	q.mu.Unlock()
	if w != nil {
		w.SetResult(val.NoneVal())
	}
	return true
}

func (q *pyAsyncQueue) getNowait(ctx *val.ExecContext) (val.Value, bool) {
	if q.kind == queueKindPriority {
		return q.priorityGetNowait(ctx)
	}
	q.mu.Lock()
	if q.lenLocked() == 0 {
		isShutDown := q.shutDown
		q.mu.Unlock()
		if isShutDown {
			ctx.Raise(val.NewException(QueueShutDownType, ""))
		} else {
			ctx.Raise(val.NewException(QueueEmptyType, ""))
		}
		return val.NoneVal(), false
	}
	var item val.Value
	if q.kind == queueKindFifo {
		item = q.fifoPopLocked()
	} else {
		item = q.lifoPopLocked()
	}
	w := popNextWaiterLocked(&q.putters)
	q.mu.Unlock()
	if w != nil {
		w.SetResult(val.NoneVal())
	}
	return item, true
}

func (q *pyAsyncQueue) priorityGetNowait(ctx *val.ExecContext) (val.Value, bool) {
	q.mu.Lock()
	if len(q.buf) == 0 {
		isShutDown := q.shutDown
		q.mu.Unlock()
		if isShutDown {
			ctx.Raise(val.NewException(QueueShutDownType, ""))
		} else {
			ctx.Raise(val.NewException(QueueEmptyType, ""))
		}
		return val.NoneVal(), false
	}
	root := q.buf[0]
	lastIdx := len(q.buf) - 1
	last := q.buf[lastIdx]
	q.buf[lastIdx] = val.Value{}
	q.buf = q.buf[:lastIdx]
	if len(q.buf) == 0 {
		w := popNextWaiterLocked(&q.putters)
		q.mu.Unlock()
		if w != nil {
			w.SetResult(val.NoneVal())
		}
		return root, true
	}
	q.buf[0] = last
	pos := 0
	for {
		n := len(q.buf)
		left := 2*pos + 1
		if left >= n {
			break
		}
		smallest := left
		right := left + 1
		if right < n {
			lVal := q.buf[left]
			rVal := q.buf[right]
			q.mu.Unlock()
			lt := val.RichCompare(ctx, rVal, lVal, val.CmpLT)
			q.mu.Lock()
			if ctx != nil && ctx.HasException() {
				q.mu.Unlock()
				return val.NoneVal(), false
			}
			if lt.IsTruthy() && right < len(q.buf) {
				smallest = right
			}
		}
		if smallest >= len(q.buf) || pos >= len(q.buf) {
			break
		}
		childVal := q.buf[smallest]
		curVal := q.buf[pos]
		q.mu.Unlock()
		lt := val.RichCompare(ctx, childVal, curVal, val.CmpLT)
		q.mu.Lock()
		if ctx != nil && ctx.HasException() {
			q.mu.Unlock()
			return val.NoneVal(), false
		}
		if !lt.IsTruthy() {
			break
		}
		if smallest >= len(q.buf) || pos >= len(q.buf) {
			break
		}
		q.buf[pos], q.buf[smallest] = q.buf[smallest], q.buf[pos]
		pos = smallest
	}
	w := popNextWaiterLocked(&q.putters)
	q.mu.Unlock()
	if w != nil {
		w.SetResult(val.NoneVal())
	}
	return root, true
}

func (q *pyAsyncQueue) taskDone(ctx *val.ExecContext) {
	q.mu.Lock()
	if q.unfinishedTasks <= 0 {
		q.mu.Unlock()
		ctx.Raise(val.NewValueError("task_done() called too many times"))
		return
	}
	q.unfinishedTasks--
	var waiters []*val.PyFuture
	if q.unfinishedTasks == 0 {
		waiters = takeAllWaitersLocked(&q.joinWaiters)
	}
	q.mu.Unlock()
	for _, w := range waiters {
		if w != nil {
			w.SetResult(val.NoneVal())
		}
	}
}

func (q *pyAsyncQueue) shutdown(immediate bool) {
	q.mu.Lock()
	q.shutDown = true
	var joinToWake []*val.PyFuture
	if immediate {
		drained := q.drainLocked()
		if drained > 0 {
			q.unfinishedTasks -= drained
			if q.unfinishedTasks < 0 {
				q.unfinishedTasks = 0
			}
			if q.unfinishedTasks == 0 {
				joinToWake = takeAllWaitersLocked(&q.joinWaiters)
			}
		}
	}
	puttersToFail := takeAllWaitersLocked(&q.putters)
	var gettersToFail []*val.PyFuture
	if immediate || q.lenLocked() == 0 {
		gettersToFail = takeAllWaitersLocked(&q.getters)
	}
	q.mu.Unlock()

	for _, w := range joinToWake {
		if w != nil {
			w.SetResult(val.NoneVal())
		}
	}
	for _, w := range puttersToFail {
		if w != nil && !w.IsDone() {
			w.SetException(val.NewException(QueueShutDownType, ""))
		}
	}
	for _, w := range gettersToFail {
		if w != nil && !w.IsDone() {
			w.SetException(val.NewException(QueueShutDownType, ""))
		}
	}
}

func newQueueAwaitable(q *pyAsyncQueue, op queueAwaitOp, item val.Value) val.Value {
	aw := &pyQueueAwaitable{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: queueAwaitableType},
		q:      q,
		op:     op,
		item:   item,
	}
	return val.ObjectVal(&aw.Header)
}

func (aw *pyQueueAwaitable) onCancel() {
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
	q := aw.q
	q.mu.Lock()
	var nextToWake *val.PyFuture
	switch aw.op {
	case queueAwaitGet:
		removeWaiterLocked(&q.getters, w)
		if !wWasCancelled && q.lenLocked() > 0 {
			nextToWake = popNextWaiterLocked(&q.getters)
		}
	case queueAwaitPut:
		removeWaiterLocked(&q.putters, w)
		if !wWasCancelled && !q.fullLocked() && !q.shutDown {
			nextToWake = popNextWaiterLocked(&q.putters)
		}
	case queueAwaitJoin:
		removeWaiterLocked(&q.joinWaiters, w)
	}
	q.mu.Unlock()
	if nextToWake != nil {
		nextToWake.SetResult(val.NoneVal())
	}
}

func (aw *pyQueueAwaitable) step(ctx *val.ExecContext) (val.Value, val.IterStatus) {
	if aw.done {
		ctx.Raise(val.NewRuntimeError("cannot reuse already awaited coroutine"))
		return val.NoneVal(), val.IterError
	}
	q := aw.q
	if aw.waiter != nil {
		w := aw.waiter
		aw.waiter = nil
		if w.IsCancelled() {
			q.mu.Lock()
			switch aw.op {
			case queueAwaitGet:
				removeWaiterLocked(&q.getters, w)
			case queueAwaitPut:
				removeWaiterLocked(&q.putters, w)
			case queueAwaitJoin:
				removeWaiterLocked(&q.joinWaiters, w)
			}
			q.mu.Unlock()
			aw.done = true
			_, exc := w.Result()
			if exc.IsNone() || exc.HeaderPtr() == nil {
				exc = newCancelledError("")
			}
			ctx.Raise(exc)
			return val.NoneVal(), val.IterError
		}
		_, exc := w.Result()
		if !exc.IsNone() && exc.HeaderPtr() != nil {
			aw.done = true
			ctx.Raise(exc)
			return val.NoneVal(), val.IterError
		}
	}

	switch aw.op {
	case queueAwaitJoin:
		q.mu.Lock()
		if q.unfinishedTasks == 0 {
			q.mu.Unlock()
			aw.done = true
			return val.NoneVal(), val.IterExhausted
		}
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()
		q.joinWaiters = append(q.joinWaiters, fut)
		aw.waiter = fut
		q.mu.Unlock()
		return futVal, val.IterYielded

	case queueAwaitGet:
		if q.kind == queueKindPriority {
			q.mu.Lock()
			if len(q.buf) == 0 {
				if q.shutDown {
					q.mu.Unlock()
					aw.done = true
					ctx.Raise(val.NewException(QueueShutDownType, ""))
					return val.NoneVal(), val.IterError
				}
				futVal := val.NewFutureVal()
				fut := futVal.AsFuture()
				q.getters = append(q.getters, fut)
				aw.waiter = fut
				q.mu.Unlock()
				return futVal, val.IterYielded
			}
			q.mu.Unlock()
			item, ok := q.priorityGetNowait(ctx)
			if !ok {
				if ctx != nil && ctx.HasException() {
					aw.done = true
					return val.NoneVal(), val.IterError
				}
				return aw.step(ctx)
			}
			aw.done = true
			return item, val.IterExhausted
		}
		q.mu.Lock()
		if q.lenLocked() == 0 {
			if q.shutDown {
				q.mu.Unlock()
				aw.done = true
				ctx.Raise(val.NewException(QueueShutDownType, ""))
				return val.NoneVal(), val.IterError
			}
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()
			q.getters = append(q.getters, fut)
			aw.waiter = fut
			q.mu.Unlock()
			return futVal, val.IterYielded
		}
		var item val.Value
		if q.kind == queueKindFifo {
			item = q.fifoPopLocked()
		} else {
			item = q.lifoPopLocked()
		}
		w := popNextWaiterLocked(&q.putters)
		q.mu.Unlock()
		if w != nil {
			w.SetResult(val.NoneVal())
		}
		aw.done = true
		return item, val.IterExhausted

	case queueAwaitPut:
		if q.kind == queueKindPriority {
			q.mu.Lock()
			if q.shutDown {
				q.mu.Unlock()
				aw.done = true
				ctx.Raise(val.NewException(QueueShutDownType, ""))
				return val.NoneVal(), val.IterError
			}
			if q.fullLocked() {
				futVal := val.NewFutureVal()
				fut := futVal.AsFuture()
				q.putters = append(q.putters, fut)
				aw.waiter = fut
				q.mu.Unlock()
				return futVal, val.IterYielded
			}
			q.mu.Unlock()
			if !q.priorityPutNowait(ctx, aw.item) {
				if ctx != nil && ctx.HasException() {
					aw.done = true
					return val.NoneVal(), val.IterError
				}
				return aw.step(ctx)
			}
			aw.done = true
			return val.NoneVal(), val.IterExhausted
		}
		q.mu.Lock()
		if q.shutDown {
			q.mu.Unlock()
			aw.done = true
			ctx.Raise(val.NewException(QueueShutDownType, ""))
			return val.NoneVal(), val.IterError
		}
		if q.fullLocked() {
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()
			q.putters = append(q.putters, fut)
			aw.waiter = fut
			q.mu.Unlock()
			return futVal, val.IterYielded
		}
		if q.kind == queueKindFifo {
			q.fifoPushLocked(aw.item)
		} else {
			q.lifoPushLocked(aw.item)
		}
		q.unfinishedTasks++
		w := popNextWaiterLocked(&q.getters)
		q.mu.Unlock()
		if w != nil {
			w.SetResult(val.NoneVal())
		}
		aw.done = true
		return val.NoneVal(), val.IterExhausted
	}
	return val.NoneVal(), val.IterExhausted
}

func formatAsyncQueueRepr(ctx *val.ExecContext, q *pyAsyncQueue) string {
	q.mu.Lock()
	maxsize := q.maxsize
	tasks := q.unfinishedTasks
	nGetters := len(q.getters)
	nPutters := len(q.putters)
	items := q.snapshotItemsLocked()
	q.mu.Unlock()

	var sb strings.Builder
	fmt.Fprintf(&sb, "<%s at %p maxsize=%d", q.inst.Header.Type.Name, q, maxsize)
	if len(items) > 0 {
		sb.WriteString(" _queue=[")
		for i, it := range items {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(val.PyRepr(ctx, it))
		}
		sb.WriteString("]")
	}
	if nGetters > 0 {
		fmt.Fprintf(&sb, " _getters[%d]", nGetters)
	}
	if nPutters > 0 {
		fmt.Fprintf(&sb, " _putters[%d]", nPutters)
	}
	if tasks > 0 {
		fmt.Fprintf(&sb, " tasks=%d", tasks)
	}
	sb.WriteByte('>')
	return sb.String()
}

func initAsyncIOQueueTypes() {
	asyncioQueueOnce.Do(func() {
		QueueEmptyType = val.MustNewType("QueueEmpty", []*val.PyType{val.ExceptionType}, nil)
		QueueFullType = val.MustNewType("QueueFull", []*val.PyType{val.ExceptionType}, nil)
		QueueShutDownType = val.MustNewType("QueueShutDown", []*val.PyType{val.ExceptionType}, nil)

		queueAwaitableType = val.MustNewType("_QueueAwaitable", []*val.PyType{val.ObjectType}, nil)
		queueAwaitableType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return o
		}
		queueAwaitableType.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			return asQueueAwaitable(o).step(ctx)
		}
		queueAwaitableType.Dict.Set(val.InternString("__await__"), val.NewBuiltinFuncVal("__await__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("__await__() takes at least 1 argument"))
				return val.NoneVal()
			}
			return args[0]
		}))

		AsyncQueueType = val.MustNewType("Queue", []*val.PyType{val.ObjectType}, nil)
		LifoQueueType = val.MustNewType("LifoQueue", []*val.PyType{AsyncQueueType}, nil)
		PriorityQueueType = val.MustNewType("PriorityQueue", []*val.PyType{AsyncQueueType}, nil)

		AsyncQueueType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("Queue.__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := AsyncQueueType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			q := &pyAsyncQueue{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
				kind: queueKindForType(cls),
			}
			return val.ObjectVal(&q.inst.Header)
		}))

		AsyncQueueType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("Queue.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("Queue.__init__() takes at least 1 argument"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			callArgs := args[1:]
			numKw := len(kwnames)
			posCount := len(callArgs) - numKw
			if posCount > 1 {
				ctx.Raise(val.NewTypeError(fmt.Sprintf("Queue.__init__() takes from 1 to 2 positional arguments but %d were given", posCount+1)))
				return val.NoneVal()
			}
			maxsize := 0
			hasMaxsize := false
			if posCount == 1 {
				if !callArgs[0].IsInt64() && !callArgs[0].IsBool() {
					ctx.Raise(val.NewTypeError("maxsize must be an integer"))
					return val.NoneVal()
				}
				if callArgs[0].IsBool() {
					if callArgs[0].AsBool() {
						maxsize = 1
					}
				} else {
					maxsize = int(callArgs[0].AsInt64())
				}
				hasMaxsize = true
			}
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				v := callArgs[posCount+i]
				if kwStr == "maxsize" {
					if hasMaxsize {
						ctx.Raise(val.NewTypeError("Queue.__init__() got multiple values for argument 'maxsize'"))
						return val.NoneVal()
					}
					if !v.IsInt64() && !v.IsBool() {
						ctx.Raise(val.NewTypeError("maxsize must be an integer"))
						return val.NoneVal()
					}
					if v.IsBool() {
						if v.AsBool() {
							maxsize = 1
						} else {
							maxsize = 0
						}
					} else {
						maxsize = int(v.AsInt64())
					}
					hasMaxsize = true
				} else {
					ctx.Raise(val.NewTypeError(fmt.Sprintf("Queue.__init__() got an unexpected keyword argument '%s'", kwStr)))
					return val.NoneVal()
				}
			}
			q.mu.Lock()
			q.maxsize = maxsize
			q.mu.Unlock()
			return val.NoneVal()
		}))

		// maxsize read-only property
		AsyncQueueType.Dict.Set(val.InternString("maxsize"), val.NewPropertyVal(
			val.NewBuiltinFuncVal("Queue.maxsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) < 1 {
					return val.Int64Val(0)
				}
				q := asAsyncQueue(args[0])
				q.mu.Lock()
				ms := q.maxsize
				q.mu.Unlock()
				return val.Int64Val(int64(ms))
			}),
			val.NoneVal(),
			val.NoneVal(),
		))

		AsyncQueueType.Dict.Set(val.InternString("qsize"), val.NewBuiltinFuncVal("Queue.qsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("qsize() takes at least 1 argument"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			q.mu.Lock()
			n := q.lenLocked()
			q.mu.Unlock()
			return val.Int64Val(int64(n))
		}))

		AsyncQueueType.Dict.Set(val.InternString("empty"), val.NewBuiltinFuncVal("Queue.empty", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("empty() takes at least 1 argument"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			q.mu.Lock()
			isEmpty := q.lenLocked() == 0
			q.mu.Unlock()
			return val.BoolVal(isEmpty)
		}))

		AsyncQueueType.Dict.Set(val.InternString("full"), val.NewBuiltinFuncVal("Queue.full", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("full() takes at least 1 argument"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			q.mu.Lock()
			isFull := q.fullLocked()
			q.mu.Unlock()
			return val.BoolVal(isFull)
		}))

		AsyncQueueType.Dict.Set(val.InternString("put_nowait"), val.NewBuiltinFuncVal("Queue.put_nowait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("put_nowait() missing 1 required positional argument: 'item'"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			q.putNowait(ctx, args[1])
			return val.NoneVal()
		}))

		AsyncQueueType.Dict.Set(val.InternString("put"), val.NewBuiltinFuncVal("Queue.put", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewTypeError("put() missing 1 required positional argument: 'item'"))
				return val.NoneVal()
			}
			return newQueueAwaitable(asAsyncQueue(args[0]), queueAwaitPut, args[1])
		}))

		AsyncQueueType.Dict.Set(val.InternString("get_nowait"), val.NewBuiltinFuncVal("Queue.get_nowait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("get_nowait() takes at least 1 argument"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			item, _ := q.getNowait(ctx)
			return item
		}))

		AsyncQueueType.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("Queue.get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("get() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newQueueAwaitable(asAsyncQueue(args[0]), queueAwaitGet, val.NoneVal())
		}))

		AsyncQueueType.Dict.Set(val.InternString("task_done"), val.NewBuiltinFuncVal("Queue.task_done", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("task_done() takes at least 1 argument"))
				return val.NoneVal()
			}
			asAsyncQueue(args[0]).taskDone(ctx)
			return val.NoneVal()
		}))

		AsyncQueueType.Dict.Set(val.InternString("join"), val.NewBuiltinFuncVal("Queue.join", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("join() takes at least 1 argument"))
				return val.NoneVal()
			}
			return newQueueAwaitable(asAsyncQueue(args[0]), queueAwaitJoin, val.NoneVal())
		}))

		AsyncQueueType.Dict.Set(val.InternString("shutdown"), val.NewBuiltinFuncVal("Queue.shutdown", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewTypeError("shutdown() takes at least 1 argument"))
				return val.NoneVal()
			}
			q := asAsyncQueue(args[0])
			callArgs := args[1:]
			numKw := len(kwnames)
			posCount := len(callArgs) - numKw
			if posCount > 1 {
				ctx.Raise(val.NewTypeError(fmt.Sprintf("shutdown() takes from 1 to 2 positional arguments but %d were given", posCount+1)))
				return val.NoneVal()
			}
			immediate := false
			hasImmediate := false
			if posCount == 1 {
				immediate = callArgs[0].IsTruthy()
				hasImmediate = true
			}
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				v := callArgs[posCount+i]
				if kwStr == "immediate" {
					if hasImmediate {
						ctx.Raise(val.NewTypeError("shutdown() got multiple values for argument 'immediate'"))
						return val.NoneVal()
					}
					immediate = v.IsTruthy()
					hasImmediate = true
				} else {
					ctx.Raise(val.NewTypeError(fmt.Sprintf("shutdown() got an unexpected keyword argument '%s'", kwStr)))
					return val.NoneVal()
				}
			}
			q.shutdown(immediate)
			return val.NoneVal()
		}))

		reprFn := val.NewBuiltinFuncVal("Queue.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NewStringVal("<Queue>")
			}
			return val.NewStringVal(formatAsyncQueueRepr(ctx, asAsyncQueue(args[0])))
		})
		AsyncQueueType.Dict.Set(val.InternString("__repr__"), reprFn)
		AsyncQueueType.Dict.Set(val.InternString("__str__"), reprFn)

		classGetItem := val.NewClassMethodVal(val.NewBuiltinFuncVal("__class_getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			initTypingTypes()
			return GenericAliasType.Slots.TpCall(ctx, val.TypeVal(GenericAliasType), args, kwnames)
		}))
		AsyncQueueType.Dict.Set(nameClassGetitem, classGetItem)

		AsyncQueueType.InvalidateVersion()
		LifoQueueType.InvalidateVersion()
		PriorityQueueType.InvalidateVersion()
	})
}
