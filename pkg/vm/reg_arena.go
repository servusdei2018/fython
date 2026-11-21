package vm

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// discardRegArena releases everything allocated since saved, zeroing the
// popped range so no stale pointers retain heap objects or stay visible to
// the collector.
func (vm *VM) discardRegArena(saved int) {
	if saved < 0 {
		saved = 0
	}
	if saved > vm.regArenaTop {
		return
	}
	clear(vm.regArena[saved:vm.regArenaTop])
	vm.regArenaTop = saved
}

// isArenaItems reports whether items aliases the live bump slab.
func (vm *VM) isArenaItems(items []val.Value) bool {
	if len(items) == 0 || len(vm.regArena) == 0 || vm.regArenaTop <= 0 {
		return false
	}
	base := uintptr(unsafe.Pointer(&vm.regArena[0]))
	slabEnd := uintptr(unsafe.Pointer(&vm.regArena[len(vm.regArena)-1]))
	liveEnd := uintptr(unsafe.Pointer(&vm.regArena[vm.regArenaTop-1]))
	lo := uintptr(unsafe.Pointer(&items[0]))
	hi := uintptr(unsafe.Pointer(&items[len(items)-1]))
	return lo >= base && hi <= slabEnd && lo <= liveEnd && hi >= base
}

// copyOutOfArena heap-copies an arena-backed tuple's items in place so the
// tuple survives frame exit. Non-tuples and heap tuples pass through.
func (vm *VM) copyOutOfArena(v val.Value) val.Value {
	if !v.IsTuple() {
		return v
	}
	t := v.AsTuple()
	if !vm.isArenaItems(t.Items) {
		return v
	}
	heapItems := make([]val.Value, len(t.Items))
	copy(heapItems, t.Items)
	t.Items = heapItems
	return v
}
