package vm

import (
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

// ICSlot caches [ExpectedShapeID, SlotIndex] for O(1) zero-allocation attribute access
// in OP_LOAD_ATTR and OP_STORE_ATTR.
type ICSlot struct {
	ExpectedShapeID uint32
	SlotIndex       int
}

// CallFrame is an unboxed value struct stored in the contiguous VM.frames slab.
// It is never heap-allocated per function call.
type CallFrame struct {
	code       []uint32
	consts     []val.Value
	ip         int
	baseOffset int
	retSP      int // Caller's stack pointer where the return value will be written
}

// CodeObject represents a compiled function or module bytecode unit.
// Header is placed at offset 0 so a *CodeObject can be stored inside a val.Value (KindCode)
// in a constant pool with zero-cost downcasting.
type CodeObject struct {
	Header    val.ObjectHeader
	Code      []uint32
	Consts    []val.Value
	NumLocals int
	NumParams int
}

// Compile-time check that CodeObject.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(CodeObject{}.Header)]byte

// NewCodeVal wraps a *CodeObject as a val.Value with KindCode.
func NewCodeVal(co *CodeObject) val.Value {
	co.Header.Kind = val.KindCode
	return val.ObjectVal(&co.Header)
}

// AsCodeObject performs a zero-cost offset-0 downcast from val.Value to *CodeObject.
func AsCodeObject(v val.Value) *CodeObject {
	return (*CodeObject)(unsafe.Pointer(v.HeaderPtr()))
}

// VM represents a single execution thread with contiguous Value, CallFrame, and ICSlot slabs,
// plus a VM-managed string interning pool.
type VM struct {
	stack      []val.Value
	frames     []CallFrame
	ic         []ICSlot
	internPool *val.InternPool
}

// NewVM allocates a new VM with default contiguous stack (4096 Values), frame (256 CallFrames),
// and inline cache (4096 ICSlots) slabs.
func NewVM() *VM {
	return &VM{
		stack:      make([]val.Value, DefaultStackSize),
		frames:     make([]CallFrame, DefaultFrameSlabSize),
		ic:         make([]ICSlot, DefaultICSlabSize),
		internPool: val.NewInternPool(256),
	}
}

// NewVMWithCapacity allocates a new VM with custom stack and frame slab sizes.
func NewVMWithCapacity(stackCap, frameCap int) *VM {
	if stackCap <= 0 {
		stackCap = DefaultStackSize
	}
	if frameCap <= 0 {
		frameCap = DefaultFrameSlabSize
	}
	return &VM{
		stack:      make([]val.Value, stackCap),
		frames:     make([]CallFrame, frameCap),
		ic:         make([]ICSlot, DefaultICSlabSize),
		internPool: val.NewInternPool(256),
	}
}

// Intern canonicalizes an identifier, attribute name, or string constant in the VM's intern pool.
func (vm *VM) Intern(s string) val.Value {
	return vm.internPool.Intern(s)
}

// InternValue canonicalizes a string Value in the VM's intern pool.
func (vm *VM) InternValue(v val.Value) val.Value {
	return vm.internPool.InternValue(v)
}
