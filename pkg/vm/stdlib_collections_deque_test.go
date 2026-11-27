package vm

import (
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestDequeRingBufferSemanticsAndGCZeroing(t *testing.T) {
	initCollectionsTypes()
	deq := &pyDeque{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
		MaxLen: -1,
	}

	// Push/pop wrap-around across multiple power-of-two resizes.
	for i := range 50 {
		deq.appendRight(val.Int64Val(int64(i)))
	}
	for i := range 30 {
		v := deq.popLeft(nil)
		if v.AsInt64() != int64(i) {
			t.Fatalf("popLeft %d = %v, want %d", i, v, i)
		}
	}
	// Verify popped slots are zeroed for GC.
	for i := range len(deq.buf) {
		rel := (i - deq.head) & deq.mask
		if rel >= deq.count && deq.buf[i] != (val.Value{}) {
			t.Fatalf("vacated slot %d not zeroed: %v", i, deq.buf[i])
		}
	}
	for i := range 40 {
		deq.appendLeft(val.Int64Val(int64(-1 - i)))
	}
	if deq.count != 60 {
		t.Fatalf("count = %d, want 60", deq.count)
	}
	// Pop all and verify every slot in backing buffer is zeroed.
	for deq.count > 0 {
		deq.popRight(nil)
	}
	for i, slot := range deq.buf {
		if slot != (val.Value{}) {
			t.Fatalf("slot %d not zeroed after draining: %v", i, slot)
		}
	}

	// Verify maxlen == 0 never allocates backing buffer.
	z := &pyDeque{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
		MaxLen: 0,
	}
	z.appendRight(val.Int64Val(1))
	z.appendLeft(val.Int64Val(2))
	z.extendRightSlice([]val.Value{val.Int64Val(3), val.Int64Val(4)})
	if z.count != 0 || z.buf != nil {
		t.Fatalf("maxlen=0 deque allocated or non-empty: count=%d buf=%v", z.count, z.buf)
	}
}

func TestDequeConcurrentRaceSafety(t *testing.T) {
	initCollectionsTypes()
	deq := &pyDeque{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
		MaxLen: 64,
	}
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 500 {
				v := val.Int64Val(int64(id*1000 + i))
				if i%2 == 0 {
					deq.appendRight(v)
				} else {
					deq.appendLeft(v)
				}
				if i%5 == 0 {
					deq.rotate(3)
				}
				if i%3 == 0 {
					deq.Mu.Lock()
					if deq.count > 0 {
						deq.removeAtLocked(0)
					}
					deq.Mu.Unlock()
				}
			}
		}(g)
	}
	wg.Wait()
	if deq.count > 64 {
		t.Fatalf("count %d exceeded maxlen 64", deq.count)
	}
}
