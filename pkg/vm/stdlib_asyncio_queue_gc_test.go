package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestAsyncioQueueGCSlotZeroing(t *testing.T) {
	initAsyncIOTypes()

	// 1. FIFO ring buffer slot zeroing on pop and drain
	q := &pyAsyncQueue{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: AsyncQueueType},
			Shape:  AsyncQueueType.RootShape,
		},
		kind: queueKindFifo,
	}
	var ctx val.ExecContext
	for i := range 20 {
		if !q.putNowait(&ctx, val.NewStringVal("item")) {
			t.Fatalf("fifo putNowait %d failed", i)
		}
	}
	for i := range 20 {
		if _, ok := q.getNowait(&ctx); !ok {
			t.Fatalf("fifo getNowait %d failed", i)
		}
	}
	for i, v := range q.buf {
		if v != (val.Value{}) {
			t.Fatalf("fifo slot %d not zeroed after pop: %v", i, v)
		}
	}

	// 2. LIFO stack slot zeroing on pop and immediate shutdown drain
	lq := &pyAsyncQueue{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: LifoQueueType},
			Shape:  LifoQueueType.RootShape,
		},
		kind: queueKindLifo,
	}
	for range 8 {
		lq.putNowait(&ctx, val.NewStringVal("lifo"))
	}
	lq.getNowait(&ctx)
	lq.shutdown(true)
	fullBacking := lq.buf[:cap(lq.buf)]
	for i, v := range fullBacking {
		if v != (val.Value{}) {
			t.Fatalf("lifo backing slot %d not zeroed after drain: %v", i, v)
		}
	}

	// 3. PriorityQueue min-heap slot zeroing on pop and drain
	pq := &pyAsyncQueue{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: PriorityQueueType},
			Shape:  PriorityQueueType.RootShape,
		},
		kind: queueKindPriority,
	}
	for _, x := range []int64{5, 1, 9, 3, 7} {
		pq.putNowait(&ctx, val.Int64Val(x))
	}
	for range 5 {
		if _, ok := pq.getNowait(&ctx); !ok {
			t.Fatalf("pq getNowait failed")
		}
	}
	pqBacking := pq.buf[:cap(pq.buf)]
	for i, v := range pqBacking {
		if v != (val.Value{}) {
			t.Fatalf("pq backing slot %d not zeroed after drain: %v", i, v)
		}
	}

	// 4. Waiter slice pointer zeroing after pop/remove/shutdown
	f1 := val.NewFutureVal().AsFuture()
	f2 := val.NewFutureVal().AsFuture()
	q.getters = append(q.getters, f1, f2)
	removeWaiterLocked(&q.getters, f1)
	if len(q.getters) != 1 || q.getters[:cap(q.getters)][1] != nil {
		t.Fatalf("removed getter slot not zeroed")
	}
	w := popNextWaiterLocked(&q.getters)
	if w != f2 {
		t.Fatalf("expected f2 from popNextWaiterLocked")
	}
	for i, p := range q.getters[:cap(q.getters)] {
		if p != nil {
			t.Fatalf("getter backing slot %d not zeroed", i)
		}
	}
}
