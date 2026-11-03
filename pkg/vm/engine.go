package vm

import (
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

// CompilerSingleflight ensures concurrent goroutines compiling the same module execute exactly once.
type compileCall struct {
	wg  sync.WaitGroup
	val *CodeObject
	err error
}

type CompilerSingleflight struct {
	mu sync.Mutex
	m  map[string]*compileCall
}

func (cs *CompilerSingleflight) Do(key string, fn func() (*CodeObject, error)) (*CodeObject, error) {
	cs.mu.Lock()
	if cs.m == nil {
		cs.m = make(map[string]*compileCall)
	}
	if c, ok := cs.m[key]; ok {
		cs.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := new(compileCall)
	c.wg.Add(1)
	cs.m[key] = c
	cs.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	cs.mu.Lock()
	delete(cs.m, key)
	cs.mu.Unlock()

	return c.val, c.err
}

// Engine represents the shared runtime state across goroutines.
// It manages the thread-safe string intern table, root shape, builtins,
// code object cache, module singleflight compiler, and pools lightweight *VM execution contexts.
type Engine struct {
	internPool      *val.InternPool
	rootShape       *val.Shape
	builtins        *val.PyDict
	vmPool          sync.Pool
	codeCache       sync.Map // maps canonical path or module name -> *CodeObject
	singleflight    CompilerSingleflight
	sharedModules   *val.PyDict
	isSharedModules bool
	searchPaths     []string
	compileHook     func(src []byte) (*CodeObject, error)
	virtualModules  sync.Map
}

// NewEngine creates and initializes a shared Engine instance.
func NewEngine() *Engine {
	e := &Engine{
		internPool:  val.NewInternPool(256),
		rootShape:   val.NewRootShape(),
		builtins:    val.NewDictVal(64).AsDict(),
		searchPaths: []string{"."},
	}
	// Temporary bootstrap VM to populate e.builtins
	bootVM := &VM{
		builtins: e.builtins,
		globals:  val.NewDictVal(16).AsDict(),
	}
	bootVM.initBuiltins()

	e.sharedModules = e.newSysModules()

	e.vmPool = sync.Pool{
		New: func() any {
			return e.newVMInternal(DefaultStackSize, DefaultFrameSlabSize)
		},
	}
	return e
}

func (e *Engine) newSysModules() *val.PyDict {
	modules := val.NewDictVal(32).AsDict()

	// 1. Built-in sys module
	sysModVal := val.NewModuleVal("sys", "")
	sysMod := sysModVal.AsModule()
	sysMod.Dict.Set(val.InternString("modules"), val.ObjectVal(&modules.Header))

	// Search paths list
	pathItems := make([]val.Value, len(e.searchPaths))
	for i, p := range e.searchPaths {
		pathItems[i] = val.NewStringVal(p)
	}
	sysMod.Dict.Set(val.InternString("path"), val.NewListVal(pathItems...))
	sysMod.Dict.Set(val.InternString("version"), val.NewStringVal("Fython 3.14.0"))
	modules.Set(val.InternString("sys"), sysModVal)

	// 2. Built-in builtins module
	builtinsModVal := val.NewModuleVal("builtins", "")
	builtinsMod := builtinsModVal.AsModule()
	builtinsMod.Dict = e.builtins
	modules.Set(val.InternString("builtins"), builtinsModVal)

	// 3. Native standard library modules
	modules.Set(val.InternString("json"), e.createJSONModule())
	modules.Set(val.InternString("math"), e.createMathModule())
	modules.Set(val.InternString("time"), e.createTimeModule())
	modules.Set(val.InternString("os"), e.createOSModule())
	modules.Set(val.InternString("io"), e.createIOModule())

	return modules
}

// newVMInternal allocates a new isolated VM attached to this Engine.
func (e *Engine) newVMInternal(stackCap, frameCap int) *VM {
	if stackCap <= 0 {
		stackCap = DefaultStackSize
	}
	if frameCap <= 0 {
		frameCap = DefaultFrameSlabSize
	}
	modules := e.sharedModules
	if !e.isSharedModules || modules == nil {
		modules = e.newSysModules()
	}
	v := &VM{
		engine:     e,
		stack:      make([]val.Value, stackCap),
		frames:     make([]CallFrame, frameCap),
		ic:         make([]ICSlot, DefaultICSlabSize),
		internPool: e.internPool,
		globals:    val.NewDictVal(64).AsDict(),
		builtins:   e.builtins,
		modules:    modules,
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

// SetSharedModules configures whether VMs attached to this Engine share a single sys.modules
// dictionary or each maintain an isolated namespace.
func (e *Engine) SetSharedModules(enabled bool) {
	e.isSharedModules = enabled
	if enabled && e.sharedModules == nil {
		e.sharedModules = e.newSysModules()
	}
}

// SetSearchPaths sets the directory search paths for module resolution.
func (e *Engine) SetSearchPaths(paths []string) {
	e.searchPaths = append([]string(nil), paths...)
}

// SearchPaths returns the active directory search paths.
func (e *Engine) SearchPaths() []string {
	return e.searchPaths
}

// SetCompileHook sets the source compilation callback.
func (e *Engine) SetCompileHook(fn func(src []byte) (*CodeObject, error)) {
	e.compileHook = fn
}

// RegisterVirtualModule registers in-memory source code under the given module name.
func (e *Engine) RegisterVirtualModule(name string, src string) {
	e.virtualModules.Store(name, src)
}
