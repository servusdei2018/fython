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

// acquireRegFrame returns a scratch register-frame state. Frames are pooled
// so hot entry stays allocation-free; nested register runs acquire their
// own frame while the caller frame is active.
func (vm *VM) acquireRegFrame() *regFrame {
	if n := len(vm.regFramePool); n > 0 {
		rf := vm.regFramePool[n-1]
		vm.regFramePool[n-1] = nil
		vm.regFramePool = vm.regFramePool[:n-1]
		return rf
	}
	return &regFrame{}
}

func (vm *VM) releaseRegFrame(rf *regFrame) {
	if rf == nil {
		return
	}
	*rf = regFrame{}
	if len(vm.regFramePool) < 64 {
		vm.regFramePool = append(vm.regFramePool, rf)
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
	if (st.activeGen != nil || st.activeCoro != nil) && !st.stackIsVM {
		var targetCO *CodeObject
		if st.activeGen != nil {
			targetCO = AsCodeObject(st.activeGen.Code)
		} else {
			targetCO = AsCodeObject(st.activeCoro.Code)
		}
		if st.activeGen != nil && targetCO != nil {
			for {
				cur := targetCO.GenStackHint.Load()
				if int(cur) >= minCap || targetCO.GenStackHint.CompareAndSwap(cur, int32(minCap)) {
					break
				}
			}
		}
		s, newCls := vm.acquireGeneratorStack(minCap)
		copy(s, curStack)
		if len(curStack) > 0 && len(vm.frameAux) > 0 {
			oldBase := &curStack[0]
			for i := st.genFrameIdx; i < st.frameIdx && i < len(vm.frameAux); i++ {
				if len(vm.frameAux[i].stack) > 0 && &vm.frameAux[i].stack[0] == oldBase {
					vm.frameAux[i].stack = s
				}
			}
		}
		if st.activeGen != nil {
			st.activeGen.Stack = s
			st.activeGen.PoolClass = newCls
		} else {
			st.activeCoro.Stack = s
			st.activeCoro.PoolClass = newCls
		}
		st.stack = s
		vm.execCtx.Stack = s
		vm.releaseStackSlice(curStack)
		return s
	}
	s := ensureStack(curStack, minCap)
	st.stack = s
	vm.execCtx.Stack = s
	if st.stackIsVM {
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
		saved.cache = st.cache
		saved.ip = ip
		saved.baseOffset = st.base
		saved.globals = st.globals
		saved.excStackLen = int32(len(vm.excStack))
		saved.initInstance = val.Value{}
		flags := saved.flags & frameFlagHasShadow
		if st.stackIsVM {
			flags |= frameFlagStackIsVM
		}
		if st.cells != nil || st.cellPtrs != nil || !st.curLocals.IsNone() || st.activeGen != nil || st.activeCoro != nil {
			aux := vm.frameAuxAt(frameIdx)
			aux.cells = st.cells
			aux.cellPtrs = st.cellPtrs
			aux.locals = st.curLocals
			aux.stack = st.stack
			aux.activeGen = st.activeGen
			aux.activeCoro = st.activeCoro
			aux.genFrameIdx = st.genFrameIdx
			flags |= frameFlagHasAux
		}
		saved.flags = flags
		vm.activeFrameIdx = frameIdx
		vm.activeCO = st.curCO
		vm.activeBaseOffset = st.base
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
}
