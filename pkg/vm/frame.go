package vm

import (
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	// DefaultStackSize is the default pre-allocated Value slab capacity per VM instance.
	DefaultStackSize = 4096
	// DefaultFrameSlabSize is the default pre-allocated CallFrame slab capacity per VM instance.
	DefaultFrameSlabSize = 256
	// DefaultICSlabSize is the default pre-allocated Inline Cache side-table capacity per VM instance.
	DefaultICSlabSize = 4096
)

// ICKind identifies the cached resolution category in an ICSlot.
type ICKind uint8

const (
	ICKindNone           ICKind = iota
	ICKindInstanceSlot          // Hit in PyInstance.Slots[SlotIndex]
	ICKindClassMethod           // Hit on PyType MRO method (CachedVal holds raw callable)
	ICKindDataDescriptor        // Hit on PyType MRO data descriptor (CachedVal holds descriptor)
	ICKindClassAttr             // Hit on PyType MRO non-descriptor class attribute
	ICKindGlobal                // Hit on module globals dict
	ICKindBuiltin               // Hit on builtins dict fallback
)

// ICSlot caches [ExpectedShapeID, ExpectedTypeVer, SlotIndex, CachedVal] for O(1)
// zero-allocation attribute and method lookup in OP_LOAD_ATTR, OP_STORE_ATTR, and OP_LOAD_METHOD.
// Also caches [ExpectedGlobalsVer, ExpectedBuiltinsVer, CachedVal] for OP_LOAD_GLOBAL.
type ICSlot struct {
	ExpectedShapeID     uint32
	Kind                ICKind
	SlotIndex           int
	ExpectedTypeVer     uint64
	CodePtr             *uint32
	CachedVal           val.Value
	ExpectedGlobalsVer  uint64
	ExpectedBuiltinsVer uint64
}

// ExceptionEntry records a protected instruction range and handler target in a static exception table.
type ExceptionEntry struct {
	StartIP          int
	EndIP            int
	TargetIP         int
	TargetStackDepth int
	PushLasti        bool
}

// CallFrame is an unboxed value struct stored in the contiguous VM.frames slab.
// It is never heap-allocated per function call.
type CallFrame struct {
	co         *CodeObject
	code       []uint32
	consts     []val.Value
	ip         int
	baseOffset int
	retSP      int // Caller's stack pointer where the return value will be written
	cells      []val.Value
	globals    *val.PyDict
	iters      [16]val.PyIter
}

// CodeObject represents a compiled function or module bytecode unit.
// Header is placed at offset 0 so a *CodeObject can be stored inside a val.Value (KindCode)
// in a constant pool with zero-cost downcasting.
type CodeObject struct {
	Header         val.ObjectHeader
	Name           string
	Code           []uint32
	Consts         []val.Value
	ParamNames     []val.Value
	CellVars       []val.Value
	FreeVars       []val.Value
	NumLocals      int
	NumParams      int
	MaxStackSize   int
	IsGenerator    bool
	ExceptionTable []ExceptionEntry
}

// Compile-time check that CodeObject.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(CodeObject{}.Header)]byte

// NewCodeVal wraps a *CodeObject as a val.Value with KindCode.
func NewCodeVal(co *CodeObject) val.Value {
	co.Header.Kind = val.KindCode
	co.Header.Type = val.CodeType
	return val.ObjectVal(&co.Header)
}

// AsCodeObject performs a zero-cost offset-0 downcast from val.Value to *CodeObject.
func AsCodeObject(v val.Value) *CodeObject {
	return (*CodeObject)(unsafe.Pointer(v.HeaderPtr()))
}

// VM represents a single execution thread with contiguous Value, CallFrame, and ICSlot slabs,
// plus a VM-managed string interning pool and zero-allocation Vectorcall execution context.
type VM struct {
	engine         *Engine
	stack          []val.Value
	frames         []CallFrame
	ic             []ICSlot
	internPool     *val.InternPool
	execCtx        val.ExecContext
	activeFrameIdx int
	kwScratch      [64]val.Value
	kwSet          [64]bool
	globals        *val.PyDict
	builtins       *val.PyDict
	lastException  val.Value
	rootCO         CodeObject
}

// Engine returns the parent Engine managing this VM.
func (vm *VM) Engine() *Engine {
	return vm.engine
}

// Reset clears the active stack and execution frame state for reuse in Engine's sync.Pool.
func (vm *VM) Reset() {
	sp := vm.execCtx.SP
	if sp > len(vm.stack) {
		sp = len(vm.stack)
	}
	for i := 0; i < sp; i++ {
		vm.stack[i] = val.NoneVal()
	}
	vm.execCtx.SP = 0
	vm.activeFrameIdx = 0
	vm.lastException = val.NoneVal()
}

// LastException returns the most recent unhandled exception Value, or NoneVal.
func (vm *VM) LastException() val.Value {
	if vm.lastException.HeaderPtr() == nil {
		return val.NoneVal()
	}
	return vm.lastException
}

// SetLastException sets the most recent exception.
func (vm *VM) SetLastException(exc val.Value) {
	vm.lastException = exc
}

var (
	defaultEngine     *Engine
	defaultEngineOnce sync.Once
)

// DefaultEngine returns the shared default Engine singleton.
func DefaultEngine() *Engine {
	defaultEngineOnce.Do(func() {
		defaultEngine = NewEngine()
	})
	return defaultEngine
}

// NewVM allocates a new VM with default contiguous stack (4096 Values), frame (256 CallFrames),
// and inline cache (4096 ICSlots) slabs attached to the default Engine.
func NewVM() *VM {
	return DefaultEngine().NewVM()
}

// NewVMWithCapacity allocates a new VM with custom stack and frame slab sizes attached to the default Engine.
func NewVMWithCapacity(stackCap, frameCap int) *VM {
	return DefaultEngine().NewVMWithCapacity(stackCap, frameCap)
}

// Globals returns the VM's active module globals dictionary.
func (vm *VM) Globals() *val.PyDict {
	return vm.globals
}

// Builtins returns the VM's builtins dictionary.
func (vm *VM) Builtins() *val.PyDict {
	return vm.builtins
}

// SetGlobal assigns a variable in the VM's module globals dictionary.
func (vm *VM) SetGlobal(name string, v val.Value) {
	vm.globals.Set(val.InternString(name), v)
}

// GetGlobal resolves a variable from the VM's module globals dictionary.
func (vm *VM) GetGlobal(name string) (val.Value, bool) {
	return vm.globals.Get(val.InternString(name))
}

// Intern canonicalizes an identifier, attribute name, or string constant in the VM's intern pool.
// Delegates to the process-wide val.InternString pool so VM-interned and type-interned identifiers
// share identical canonical pointers.
func (vm *VM) Intern(s string) val.Value {
	return val.InternString(s)
}

// InternValue canonicalizes a string Value in the canonical intern pool.
func (vm *VM) InternValue(v val.Value) val.Value {
	return val.InternValue(v)
}
