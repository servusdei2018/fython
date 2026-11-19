package vm

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// loopCtl directs the hot interpreter loop after a cold helper or unwind call.
type loopCtl uint8

const (
	ctlNext loopCtl = iota
	ctlReload
	ctlRaise
	ctlExit
)

// loopState holds cold and frame-transition state for an active
// runLoopWithGlobals invocation so hot loop locals (ip, sp, stack, locals,
// consts, codePtr) stay in hardware registers without taking their addresses.
type loopState struct {
	curCO         *CodeObject
	codePtr       unsafe.Pointer
	codeLen       int
	consts        []val.Value
	frameIdx      int
	startFrameIdx int
	base          int
	cells         []val.Value
	cellPtrs      []*val.PyCell
	globals       *val.PyDict
	curLocals     val.Value
	cache         *VMCodeCache
	stack         []val.Value
	activeGen     *val.PyGenerator
	activeCoro    *val.PyCoroutine
	genFrameIdx   int
	stackIsVM     bool
	periodicTick  uint32

	// Entry/exit handoff and helper channels.
	ip       int
	sp       int
	result   val.Value
	raiseExc val.Value
}

// Compile-time check that val.Value is 16 bytes for indexed stackPtr/constsPtr loads.
var _ [16 - unsafe.Sizeof(val.Value{})]byte
var _ [unsafe.Sizeof(val.Value{}) - 16]byte

func (vm *VM) acquireLoopState() *loopState {
	if n := len(vm.loopStatePool); n > 0 {
		st := vm.loopStatePool[n-1]
		vm.loopStatePool = vm.loopStatePool[:n-1]
		return st
	}
	return &loopState{}
}

func (vm *VM) releaseLoopState(st *loopState) {
	if st == nil {
		return
	}
	*st = loopState{}
	if len(vm.loopStatePool) < 64 {
		vm.loopStatePool = append(vm.loopStatePool, st)
	}
}

// syncStackFromVM refreshes st.stack if a call grew vm.stack (or vm.execCtx.Stack
// when executing on a generator/coroutine/nested stack).
func (st *loopState) syncStackFromVM(vm *VM, curStack []val.Value) []val.Value {
	if st.stackIsVM {
		if len(curStack) > 0 && len(vm.stack) > 0 && &curStack[0] != &vm.stack[0] {
			st.stack = vm.stack
			vm.execCtx.Stack = vm.stack
			return vm.stack
		}
	} else if len(curStack) > 0 && len(vm.execCtx.Stack) > 0 && &curStack[0] != &vm.execCtx.Stack[0] {
		s := vm.execCtx.Stack
		st.stack = s
		switch {
		case st.activeGen != nil:
			st.activeGen.Stack = s
		case st.activeCoro != nil:
			st.activeCoro.Stack = s
		}
		return s
	}
	st.stack = curStack
	return curStack
}

// syncStackFromExecCtx refreshes st.stack and the active owner slice when a
// safe call grew vm.execCtx.Stack (or vm.stack).
func (st *loopState) syncStackFromExecCtx(vm *VM, curStack []val.Value) []val.Value {
	if st.stackIsVM && len(curStack) > 0 && len(vm.stack) > 0 && &curStack[0] != &vm.stack[0] {
		st.stack = vm.stack
		vm.execCtx.Stack = vm.stack
		return vm.stack
	}
	if len(curStack) > 0 && len(vm.execCtx.Stack) > 0 && &curStack[0] != &vm.execCtx.Stack[0] {
		s := vm.execCtx.Stack
		st.stack = s
		switch {
		case st.activeGen != nil:
			st.activeGen.Stack = s
		case st.activeCoro != nil:
			st.activeCoro.Stack = s
		case st.stackIsVM:
			vm.stack = s
		}
		return s
	}
	st.stack = curStack
	return curStack
}

// growStack ensures st.stack holds at least minCap slots and propagates the
// new backing slice to execCtx and the stack owner.
//
//go:noinline
func (vm *VM) growStack(st *loopState, curStack []val.Value, minCap int) []val.Value {
	if st.activeGen != nil && !st.stackIsVM {
		if genCO := AsCodeObject(st.activeGen.Code); genCO != nil {
			for {
				cur := genCO.GenStackHint.Load()
				if int(cur) >= minCap || genCO.GenStackHint.CompareAndSwap(cur, int32(minCap)) {
					break
				}
			}
		}
		s, newCls := vm.acquireGeneratorStack(minCap)
		copy(s, curStack)
		if len(curStack) > 0 {
			oldBase := &curStack[0]
			for i := st.genFrameIdx; i < st.frameIdx && i < len(vm.frames); i++ {
				if len(vm.frames[i].stack) > 0 && &vm.frames[i].stack[0] == oldBase {
					vm.frames[i].stack = s
				}
			}
		}
		st.activeGen.Stack = s
		st.activeGen.PoolClass = newCls
		st.stack = s
		vm.execCtx.Stack = s
		vm.releaseStackSlice(curStack)
		return s
	}
	s := ensureStack(curStack, minCap)
	st.stack = s
	vm.execCtx.Stack = s
	switch {
	case st.activeCoro != nil:
		st.activeCoro.Stack = s
	case st.stackIsVM:
		vm.stack = s
	}
	return s
}

func (st *loopState) reloadWindow(vm *VM, co *CodeObject) unsafe.Pointer {
	ptr, length := vm.loadWindow(co, st.cache)
	st.codePtr = ptr
	st.codeLen = length
	return ptr
}

// flushActiveFrame materializes the currently active loopState into vm.frames
// on demand when frame inspection, nested execution, or error reporting needs it.
func (vm *VM) flushActiveFrame() {
	if st := vm.activeState; st != nil {
		vm.syncActiveFrame(st, st.ip)
	}
}

// syncActiveFrameFromState persists hot registers ip and current frame metadata
// into vm.frames[st.frameIdx] before invoking Go runtime helpers that may inspect
// frames or trigger tracebacks.
func (vm *VM) syncActiveFrame(st *loopState, ip int) {
	frameIdx := st.frameIdx
	if frameIdx > vm.maxFrameIdx {
		vm.maxFrameIdx = frameIdx
	}
	if st.stackIsVM && st.base+256 > vm.maxSP {
		vm.maxSP = st.base + 256
	}
	if frameIdx >= 0 && frameIdx < len(vm.frames) {
		saved := &vm.frames[frameIdx]
		saved.co = st.curCO
		saved.codePtr = st.codePtr
		saved.codeLen = st.codeLen
		saved.consts = st.consts
		saved.ip = ip
		saved.baseOffset = st.base
		saved.cells = st.cells
		saved.cellPtrs = st.cellPtrs
		saved.globals = st.globals
		saved.locals = st.curLocals
		saved.cache = st.cache
		saved.excStackLen = len(vm.excStack)
		saved.stackIsVM = st.stackIsVM
		saved.stack = st.stack
		saved.activeGen = st.activeGen
		saved.genFrameIdx = st.genFrameIdx
		saved.initInstance = val.Value{}
		saved.cmpActive = false
		vm.activeFrameIdx = frameIdx
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
}
