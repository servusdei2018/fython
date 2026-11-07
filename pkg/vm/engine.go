package vm

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

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
	internPool         *val.InternPool
	rootShape          *val.Shape
	builtins           *val.PyDict
	vmPool             sync.Pool
	codeCache          sync.Map // maps canonical path or module name -> *CodeObject
	singleflight       CompilerSingleflight
	sharedModules      *val.PyDict
	isSharedModules    bool
	searchPaths        []string
	compileHook        func(src []byte) (*CodeObject, error)
	dynamicCompileHook func(src []byte, filename string, mode int) (*CodeObject, error)
	virtualModules     sync.Map
	asyncMode          AsyncMode
	coroPool           sync.Pool
	osPathMu           sync.Mutex
	signals            sigState
	stdStreams         [3]atomic.Pointer[val.Value]
}

var (
	defaultDynamicCompileHook func(src []byte, filename string, mode int) (*CodeObject, error)
)

// SetDynamicCompileHook sets the global default callback for dynamic eval/exec compilation.
func SetDynamicCompileHook(fn func(src []byte, filename string, mode int) (*CodeObject, error)) {
	defaultDynamicCompileHook = fn
}

// NewEngine creates and initializes a shared Engine instance.
func NewEngine() *Engine {
	searchPaths := []string{"."}
	if p := os.Getenv("FYTHONPATH"); p != "" {
		for _, dir := range filepath.SplitList(p) {
			if dir != "" {
				searchPaths = append(searchPaths, dir)
			}
		}
	} else if p := os.Getenv("PYTHONPATH"); p != "" {
		for _, dir := range filepath.SplitList(p) {
			if dir != "" {
				searchPaths = append(searchPaths, dir)
			}
		}
	}

	e := &Engine{
		internPool:         val.NewInternPool(256),
		rootShape:          val.NewRootShape(),
		builtins:           val.NewDictVal(64).AsDict(),
		searchPaths:        searchPaths,
		asyncMode:          AsyncModeSingleThread,
		dynamicCompileHook: defaultDynamicCompileHook,
	}
	// Temporary bootstrap VM to populate e.builtins
	bootVM := &VM{
		builtins: e.builtins,
		globals:  val.NewDictVal(16).AsDict(),
	}
	bootVM.initBuiltins()
	builtinsDict := e.builtins
	val.FunctionBuiltinsHook = func() val.Value { return val.DictVal(builtinsDict) }

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
	sysMod.Dict.Set(val.InternString("version"), val.NewStringVal("3.14.0 (main, fython)"))
	sysMod.Dict.Set(val.InternString("version_info"), val.NewTupleVal(val.Int64Val(3), val.Int64Val(14), val.Int64Val(0), val.InternString("final"), val.Int64Val(0)))
	sysMod.Dict.Set(val.InternString("hexversion"), val.Int64Val(0x030e00f0))
	sysMod.Dict.Set(val.InternString("platform"), val.InternString(runtime.GOOS))
	sysMod.Dict.Set(val.InternString("byteorder"), val.InternString("little"))
	sysMod.Dict.Set(val.InternString("maxsize"), val.Int64Val(9223372036854775807))
	sysMod.Dict.Set(val.InternString("float_info"), newFloatInfo())
	sysMod.Dict.Set(val.InternString("hash_info"), newHashInfo())
	sysMod.Dict.Set(val.InternString("warnoptions"), val.NewListVal())
	flags := val.NewInstanceOfType(val.ObjectType)
	flags.AsInstance().SetAttr(val.InternString("context_aware_warnings"), val.Int64Val(0))
	flags.AsInstance().SetAttr(val.InternString("warn_default_encoding"), val.Int64Val(0))
	flags.AsInstance().SetAttr(val.InternString("utf8_mode"), val.Int64Val(1))
	sysMod.Dict.Set(val.InternString("flags"), flags)

	// Builtin module names
	builtinNames := []val.Value{
		val.InternString("sys"), val.InternString("builtins"), val.InternString("_abc"),
		val.InternString("_collections"), val.InternString("itertools"), val.InternString("_functools"),
		val.InternString("_operator"), val.InternString("operator"), val.InternString("_sre"),
		val.InternString("re"), val.InternString("_types"), val.InternString("types"),
		val.InternString("_io"), val.InternString("io"), val.InternString("os"),
		val.InternString("time"), val.InternString("math"), val.InternString("json"),
		val.InternString("gc"), val.InternString("_weakref"), val.InternString("weakref"),
		val.InternString("traceback"), val.InternString("inspect"), val.InternString("asyncio"),
		val.InternString("_thread"), val.InternString("_typing"), val.InternString("_colorize"),
		val.InternString("_contextvars"), val.InternString("unicodedata"), val.InternString("_datetime"),
		val.InternString("binascii"), val.InternString("_hashlib"), val.InternString("_struct"),
		val.InternString("_csv"),
	}
	sysMod.Dict.Set(val.InternString("builtin_module_names"), val.NewTupleVal(builtinNames...))

	// sys.intern(string)
	sysMod.Dict.Set(val.InternString("audit"), val.NewBuiltinFuncVal("audit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	sysMod.Dict.Set(val.InternString("intern"), val.NewBuiltinFuncVal("intern", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: intern() requires 1 argument")
		}
		return val.InternString(args[0].AsString())
	}))

	// sys.exit([code])
	sysMod.Dict.Set(val.InternString("exit"), val.NewBuiltinFuncVal("exit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		code := val.NoneVal()
		var items []val.Value
		if len(args) > 0 {
			code = args[0]
			items = []val.Value{code}
		}
		exc := val.NewException(val.SystemExitType, "")
		inst := exc.AsInstance()
		inst.SetAttr(val.InternString("args"), val.NewTupleVal(items...))
		inst.SetAttr(val.InternString("code"), code)
		panic(exc)
	}))

	// sys.stdout, stderr, stdin
	ioMod := e.createIOModule()
	sysMod.Dict.Set(val.InternString("stdout"), e.stdStream(1))
	sysMod.Dict.Set(val.InternString("stderr"), e.stdStream(2))
	sysMod.Dict.Set(val.InternString("stdin"), e.stdStream(0))

	// sys._getframe([depth=0])
	sysMod.Dict.Set(val.InternString("_getframe"), val.NewBuiltinFuncVal("_getframe", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		depth := 0
		if len(args) > 0 && args[0].IsInt64() {
			depth = int(args[0].AsInt64())
		}
		if depth < 0 {
			panic("ValueError: call stack is not deep enough")
		}
		vmInstance := ctx.VM.(*VM)
		targetIdx := vmInstance.activeFrameIdx - depth
		if targetIdx < 0 {
			panic("ValueError: call stack is not deep enough")
		}
		fVal := vmInstance.materializeFrame(targetIdx)
		if fVal.IsNone() {
			panic("ValueError: call stack is not deep enough")
		}
		return fVal
	}))

	// sys.exc_info() -> (type, value, traceback)
	sysMod.Dict.Set(val.InternString("exc_info"), val.NewBuiltinFuncVal("exc_info", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		lastExc := vmInstance.lastException
		if lastExc.IsNone() || lastExc.HeaderPtr() == nil {
			return val.NewTupleVal(val.NoneVal(), val.NoneVal(), val.NoneVal())
		}
		tb := val.NoneVal()
		if lastExc.IsInstance() {
			tb, _ = lastExc.AsInstance().GetAttr(val.InternString("__traceback__"))
		}
		return val.NewTupleVal(val.TypeVal(lastExc.Type()), lastExc, tb)
	}))

	// sys.exception() -> value
	sysMod.Dict.Set(val.InternString("exception"), val.NewBuiltinFuncVal("exception", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		lastExc := vmInstance.lastException
		if lastExc.IsNone() || lastExc.HeaderPtr() == nil {
			return val.NoneVal()
		}
		return lastExc
	}))

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
	modules.Set(val.InternString("io"), ioMod)
	modules.Set(val.InternString("_io"), ioMod)
	modules.Set(val.InternString("asyncio"), e.createAsyncioModule())
	modules.Set(val.InternString("_weakref"), e.createWeakrefCModule())
	modules.Set(val.InternString("weakref"), e.createWeakrefModule())
	modules.Set(val.InternString("traceback"), e.createTracebackModule())
	modules.Set(val.InternString("inspect"), e.createInspectModule())
	modules.Set(val.InternString("gc"), e.createGCModule())
	modules.Set(val.InternString("types"), e.createTypesModule())
	modules.Set(val.InternString("_types"), e.createTypesModule())
	modules.Set(val.InternString("_abc"), e.createABCModule())
	modules.Set(val.InternString("_collections"), e.createCollectionsModule())
	modules.Set(val.InternString("itertools"), e.createItertoolsModule())
	modules.Set(val.InternString("_functools"), e.createFunctoolsModule())
	modules.Set(val.InternString("operator"), e.createOperatorModule())
	modules.Set(val.InternString("_operator"), e.createOperatorModule())
	modules.Set(val.InternString("_sre"), e.createSREModule())
	modules.Set(val.InternString("unicodedata"), e.createUnicodedataModule())
	modules.Set(val.InternString("re"), e.createReModule())
	modules.Set(val.InternString("_ast"), e.createASTModule())
	modules.Set(val.InternString("_thread"), e.createThreadModule())
	modules.Set(val.InternString("threading"), e.createThreadingModule())
	modules.Set(val.InternString("_typing"), e.createTypingModule())
	modules.Set(val.InternString("_colorize"), e.createColorizeModule())
	modules.Set(val.InternString("_contextvars"), e.createContextVarsModule())
	modules.Set(val.InternString("_signal"), e.createSignalModule())
	posixMod := e.createPosixModule()
	modules.Set(val.InternString("posix"), posixMod)
	modules.Set(val.InternString("nt"), posixMod)
	modules.Set(val.InternString("_posixsubprocess"), e.createPosixSubprocessModule())
	modules.Set(val.InternString("select"), e.createSelectModule())
	modules.Set(val.InternString("_string"), e.createStringModule())
	modules.Set(val.InternString("_codecs"), e.createCodecsModule())
	modules.Set(val.InternString("_socket"), e.createSocketModule())
	modules.Set(val.InternString("_ssl"), e.createSSLModule())
	modules.Set(val.InternString("_sqlite3"), e.createSqlite3Module())
	modules.Set(val.InternString("_random"), e.createRandomModule())
	modules.Set(val.InternString("_datetime"), e.createDateTimeModule())
	modules.Set(val.InternString("binascii"), e.createBinasciiModule())
	modules.Set(val.InternString("_hashlib"), e.createHashlibModule())
	modules.Set(val.InternString("_struct"), e.createStructModule())
	modules.Set(val.InternString("_csv"), e.createCsvModule())
	modules.Set(val.InternString("zlib"), e.createZlibModule())
	modules.Set(val.InternString("errno"), e.createErrnoModule())
	modules.Set(val.InternString("gettext"), e.createGettextModule())
	localeMod := e.createLocaleModule()
	modules.Set(val.InternString("locale"), localeMod)
	modules.Set(val.InternString("_locale"), localeMod)
	importlibMod := e.createImportlibModule()
	modules.Set(val.InternString("importlib"), importlibMod)
	if utilVal, ok := importlibMod.AsModule().Dict.Get(val.InternString("util")); ok {
		modules.Set(val.InternString("importlib.util"), utilVal)
	}

	return modules
}

func (e *Engine) createLocaleModule() val.Value {
	modVal := val.NewModuleVal("locale", "Locale support")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("getencoding"), val.NewBuiltinFuncVal("getencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("utf-8")
	}))
	mod.Dict.Set(val.InternString("getpreferredencoding"), val.NewBuiltinFuncVal("getpreferredencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("utf-8")
	}))
	mod.Dict.Set(val.InternString("setlocale"), val.NewBuiltinFuncVal("setlocale", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("C")
	}))
	mod.Dict.Set(val.InternString("LC_ALL"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("LC_CTYPE"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("LC_NUMERIC"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("LC_TIME"), val.Int64Val(3))
	mod.Dict.Set(val.InternString("LC_COLLATE"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("LC_MONETARY"), val.Int64Val(5))
	mod.Dict.Set(val.InternString("LC_MESSAGES"), val.Int64Val(6))
	mod.Dict.Set(val.InternString("Error"), val.TypeVal(val.ValueErrorType))
	return modVal
}

func (e *Engine) createImportlibModule() val.Value {
	modVal := val.NewModuleVal("importlib", "Core Python import machinery")
	mod := modVal.AsModule()
	utilModVal := val.NewModuleVal("importlib.util", "Utilities for importlib")
	mod.Dict.Set(val.InternString("util"), utilModVal)

	mod.Dict.Set(val.InternString("import_module"), val.NewBuiltinFuncVal("import_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(val.NewException(val.TypeErrorType, "import_module() requires name"))
		}
		name := args[0].AsString()
		if e.sharedModules != nil {
			if v, ok := e.sharedModules.Get(val.InternString(name)); ok {
				return v
			}
		}
		vm := e.AcquireVM()
		defer e.ReleaseVM(vm)
		modVal, err := vm.ImportModule(name, 0, true, nil)
		if err != nil {
			panic(val.NewException(val.ModuleNotFoundErrorType, err.Error()))
		}
		return modVal
	}))

	mod.Dict.Set(val.InternString("invalidate_caches"), val.NewBuiltinFuncVal("invalidate_caches", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("reload"), val.NewBuiltinFuncVal("reload", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(val.NewException(val.TypeErrorType, "reload() requires module"))
		}
		return args[0]
	}))

	return modVal
}

// SetAsyncMode sets the coroutine scheduling strategy for this Engine.
func (e *Engine) SetAsyncMode(mode AsyncMode) {
	e.asyncMode = mode
}

// AsyncMode returns the current coroutine scheduling strategy.
func (e *Engine) AsyncMode() AsyncMode {
	return e.asyncMode
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
		methodPIC:  make([][4]val.MethodPICEntry, DefaultICSlabSize),
		attrPIC:    make([][4]AttrPICEntry, DefaultICSlabSize),
		internPool: e.internPool,
		globals:    val.NewDictVal(64).AsDict(),
		builtins:   e.builtins,
		modules:    modules,
		context:    val.NewPyContext(),
	}
	v.rootCache = VMCodeCache{
		ic:        v.ic,
		methodPIC: v.methodPIC,
		attrPIC:   v.attrPIC,
	}
	v.execCtx.Stack = v.stack
	v.execCtx.CallHook = v.callFromHook
	v.execCtx.VM = v
	if !e.isSharedModules {
		mainModVal := val.NewModuleVal("__main__", "")
		mainModVal.AsModule().Dict = v.globals
		modules.Set(val.InternString("__main__"), mainModVal)
	}
	return v
}

// AcquireVM retrieves a lightweight *VM from the Engine's sync.Pool.
func (e *Engine) AcquireVM() *VM {
	return e.vmPool.Get().(*VM)
}

// ReleaseVM resets a *VM and returns it to the Engine's sync.Pool.
func (e *Engine) ReleaseVM(v *VM) {
	e.signals.owner.CompareAndSwap(v, nil)
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

// SetDynamicCompileHook sets the source compilation callback for eval/exec.
func (e *Engine) SetDynamicCompileHook(fn func(src []byte, filename string, mode int) (*CodeObject, error)) {
	e.dynamicCompileHook = fn
}

// CompileDynamic compiles Python source bytes dynamically using the configured hook.
func (e *Engine) CompileDynamic(src []byte, filename string, mode int) (*CodeObject, error) {
	if e.dynamicCompileHook != nil {
		return e.dynamicCompileHook(src, filename, mode)
	}
	if defaultDynamicCompileHook != nil {
		return defaultDynamicCompileHook(src, filename, mode)
	}
	return nil, fmt.Errorf("Engine: dynamic compilation hook not set")
}

// RegisterVirtualModule registers in-memory source code under the given module name.
func (e *Engine) RegisterVirtualModule(name string, src string) {
	e.virtualModules.Store(name, src)
}

// stdStream returns the shared sys.stdin/stdout/stderr file object for fd.
func (e *Engine) stdStream(fd int) val.Value {
	if p := e.stdStreams[fd].Load(); p != nil {
		return *p
	}
	files := [3]*os.File{os.Stdin, os.Stdout, os.Stderr}
	names := [3]string{"<stdin>", "<stdout>", "<stderr>"}
	v := newStdStream(files[fd], names[fd], fd != 0)
	if e.stdStreams[fd].CompareAndSwap(nil, &v) {
		return v
	}
	return *e.stdStreams[fd].Load()
}

// newFloatInfo builds sys.float_info (IEEE 754 double).
func newFloatInfo() val.Value {
	info := val.NewInstanceOfType(val.ObjectType)
	inst := info.AsInstance()
	set := func(name string, v val.Value) { inst.SetAttr(val.InternString(name), v) }
	set("max", val.Float64Val(math.MaxFloat64))
	set("max_exp", val.Int64Val(1024))
	set("max_10_exp", val.Int64Val(308))
	set("min", val.Float64Val(2.2250738585072014e-308))
	set("min_exp", val.Int64Val(-1021))
	set("min_10_exp", val.Int64Val(-307))
	set("dig", val.Int64Val(15))
	set("mant_dig", val.Int64Val(53))
	set("epsilon", val.Float64Val(2.220446049250313e-16))
	set("radix", val.Int64Val(2))
	set("rounds", val.Int64Val(1))
	return info
}

func newHashInfo() val.Value {
	info := val.NewInstanceOfType(val.ObjectType)
	inst := info.AsInstance()
	set := func(name string, v val.Value) { inst.SetAttr(val.InternString(name), v) }
	set("width", val.Int64Val(64))
	set("modulus", val.Int64Val(2305843009213693951))
	set("inf", val.Int64Val(314159))
	set("nan", val.Int64Val(0))
	set("imag", val.Int64Val(1000003))
	set("algorithm", val.InternString("siphash13"))
	set("hash_bits", val.Int64Val(64))
	set("seed_bits", val.Int64Val(128))
	set("cutoff", val.Int64Val(0))
	return info
}
