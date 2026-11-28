package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestAsyncioSyncGCSlotZeroing(t *testing.T) {
	initAsyncIOTypes()

	// Event waiter slice slot zeroing on set()
	ev := &pyAsyncEvent{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: AsyncEventType},
			Shape:  AsyncEventType.RootShape,
		},
	}
	for range 6 {
		ev.waiters = append(ev.waiters, val.NewFutureVal().AsFuture())
	}
	evWaiters := takeAllWaitersLocked(&ev.waiters)
	if len(evWaiters) != 6 {
		t.Fatalf("expected 6 event waiters, got %d", len(evWaiters))
	}
	for i, f := range ev.waiters[:cap(ev.waiters)] {
		if f != nil {
			t.Fatalf("event waiter backing slot %d not zeroed", i)
		}
	}

	// Lock waiter slice slot zeroing on release() / remove
	lk := &pyAsyncLock{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: AsyncLockType},
			Shape:  AsyncLockType.RootShape,
		},
		locked: true,
	}
	f1 := val.NewFutureVal().AsFuture()
	f2 := val.NewFutureVal().AsFuture()
	f3 := val.NewFutureVal().AsFuture()
	lk.waiters = append(lk.waiters, f1, f2, f3)
	removeWaiterLocked(&lk.waiters, f2)
	var ctx val.ExecContext
	lk.release(&ctx)
	lk.locked = true
	lk.release(&ctx)
	for i, f := range lk.waiters[:cap(lk.waiters)] {
		if f != nil {
			t.Fatalf("lock waiter backing slot %d not zeroed", i)
		}
	}
}
