package vm

import (
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

// Engine represents the shared runtime state across goroutines.
// It manages the thread-safe string intern table, root shape, builtins,
// and pools lightweight *VM execution contexts.
type Engine struct {
	internPool *val.InternPool
	rootShape  *val.Shape
	builtins   *val.PyDict
	vmPool     sync.Pool
}

// NewEngine creates and initializes a shared Engine instance.
func NewEngine() *Engine {
	e := &Engine{
		internPool: val.NewInternPool(256),
		rootShape:  val.NewRootShape(),
		builtins:   val.NewDictVal(64).AsDict(),
	}
	// Temporary bootstrap VM to populate e.builtins
	bootVM := &VM{
		builtins: e.builtins,
		globals:  val.NewDictVal(16).AsDict(),
	}
	bootVM.initBuiltins()

	e.vmPool = sync.Pool{
		New: func() any {
			return e.newVMInternal(DefaultStackSize, DefaultFrameSlabSize)
		},
	}
	return e
}

// newVMInternal allocates a new isolated VM attached to this Engine.
func (e *Engine) newVMInternal(stackCap, frameCap int) *VM {
	if stackCap <= 0 {
		stackCap = DefaultStackSize
	}
	if frameCap <= 0 {
		frameCap = DefaultFrameSlabSize
	}
	v := &VM{
		engine:     e,
		stack:      make([]val.Value, stackCap),
		frames:     make([]CallFrame, frameCap),
		ic:         make([]ICSlot, DefaultICSlabSize),
		internPool: e.internPool,
		globals:    val.NewDictVal(64).AsDict(),
		builtins:   e.builtins,
	}
	v.execCtx.Stack = v.stack
	v.execCtx.CallHook = v.callFromHook
	v.execCtx.VM = v
	return v
}

// AcquireVM retrieves a lightweight *VM from the Engine's sync.Pool.
func (e *Engine) AcquireVM() *VM {
	return e.vmPool.Get().(*VM)
}

// ReleaseVM resets a *VM and returns it to the Engine's sync.Pool.
func (e *Engine) ReleaseVM(v *VM) {
	v.Reset()
	e.vmPool.Put(v)
}

// NewVM creates a new unpooled *VM attached to this Engine.
func (e *Engine) NewVM() *VM {
	return e.newVMInternal(DefaultStackSize, DefaultFrameSlabSize)
}

// NewVMWithCapacity creates a new unpooled *VM with specified stack and frame capacities.
func (e *Engine) NewVMWithCapacity(stackCap, frameCap int) *VM {
	return e.newVMInternal(stackCap, frameCap)
}

// RunCode executes a *CodeObject on a pooled VM from this Engine.
func (e *Engine) RunCode(co *CodeObject) val.Value {
	v := e.AcquireVM()
	defer e.ReleaseVM(v)
	return v.RunCode(co)
}

// RegisterGlobal registers a named global variable in the Engine's shared builtins.
func (e *Engine) RegisterGlobal(name string, v val.Value) {
	e.builtins.Set(val.InternString(name), v)
}

// RegisterFunc registers a named callable into the Engine's shared builtins.
func (e *Engine) RegisterFunc(name string, fn val.Value) {
	e.builtins.Set(val.InternString(name), fn)
}
