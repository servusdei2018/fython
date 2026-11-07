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
	internPool                 *val.InternPool
	rootShape                  *val.Shape
	builtins                   *val.PyDict
	vmPool                     sync.Pool
	codeCache                  sync.Map // maps canonical path or module name -> *CodeObject
	singleflight               CompilerSingleflight
	sharedModules              *val.PyDict
	isSharedModules            bool
	searchPaths                []string
	runtimeConfig              RuntimeConfig
	compileHook                func(src []byte) (*CodeObject, error)
	compileWithFilenameHook    func(src []byte, filename string) (*CodeObject, error)
	dynamicCompileHook         func(src []byte, filename string, mode int) (*CodeObject, error)
	dynamicCompileOptimizeHook func(src []byte, filename string, mode, optimize int) (*CodeObject, error)
	virtualModules             sync.Map
	asyncMode                  AsyncMode
	coroPool                   sync.Pool
	osPathMu                   sync.Mutex
	signals                    sigState
	stdStreams                 [3]atomic.Pointer[val.Value]
}

var (
	defaultCompileHook                func(src []byte) (*CodeObject, error)
	defaultCompileWithFilenameHook    func(src []byte, filename string) (*CodeObject, error)
	defaultDynamicCompileHook         func(src []byte, filename string, mode int) (*CodeObject, error)
	defaultDynamicCompileOptimizeHook func(src []byte, filename string, mode, optimize int) (*CodeObject, error)
)

// SetDefaultCompileHook sets the global default callback for source compilation.
func SetDefaultCompileHook(fn func(src []byte) (*CodeObject, error)) {
	defaultCompileHook = fn
}

// SetDefaultCompileWithFilenameHook sets the global default callback for source compilation with filename.
func SetDefaultCompileWithFilenameHook(fn func(src []byte, filename string) (*CodeObject, error)) {
	defaultCompileWithFilenameHook = fn
}

// SetDynamicCompileHook sets the global default callback for dynamic eval/exec compilation.
func SetDynamicCompileHook(fn func(src []byte, filename string, mode int) (*CodeObject, error)) {
	defaultDynamicCompileHook = fn
}

// SetDynamicCompileOptimizeHook sets the global default callback for dynamic compilation with an optimization level.
func SetDynamicCompileOptimizeHook(fn func(src []byte, filename string, mode, optimize int) (*CodeObject, error)) {
	defaultDynamicCompileOptimizeHook = fn
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
		internPool:              val.NewInternPool(256),
		rootShape:               val.NewRootShape(),
		builtins:                val.NewDictVal(64).AsDict(),
		searchPaths:             searchPaths,
		asyncMode:               AsyncModeSingleThread,
		compileHook:             defaultCompileHook,
		compileWithFilenameHook: defaultCompileWithFilenameHook,
		dynamicCompileHook:      defaultDynamicCompileHook,
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
	modules, _ := e.newSysModulesWithDiscovery()
	return modules
}

func (e *Engine) newSysModulesWithDiscovery() (*val.PyDict, []string) {
	initImportlibTypes()
	initZipImportTypes()

	modules := val.NewDictVal(64).AsDict()
	eff := e.runtimeConfig.Effective()
	disc := discoverVenvAndPaths(e.runtimeConfig, e.searchPaths)

	// 1. Built-in sys module
	sysModVal := val.NewModuleVal("sys", "")
	sysMod := sysModVal.AsModule()
	sysMod.Dict.Set(val.InternString("modules"), val.ObjectVal(&modules.Header))

	// Search paths list
	pathItems := make([]val.Value, len(disc.paths))
	for i, p := range disc.paths {
		pathItems[i] = val.NewStringVal(p)
	}
	sysMod.Dict.Set(val.InternString("path"), val.NewListVal(pathItems...))

	// sys.argv and sys.orig_argv
	argv := eff.Argv
	if len(argv) == 0 {
		argv = []string{""}
	}
	argvItems := make([]val.Value, len(argv))
	for i, a := range argv {
		argvItems[i] = val.NewStringVal(a)
	}
	sysMod.Dict.Set(val.InternString("argv"), val.NewListVal(argvItems...))

	origArgv := eff.OrigArgv
	if len(origArgv) == 0 {
		origArgv = os.Args
	}
	origArgvItems := make([]val.Value, len(origArgv))
	for i, a := range origArgv {
		origArgvItems[i] = val.NewStringVal(a)
	}
	sysMod.Dict.Set(val.InternString("orig_argv"), val.NewListVal(origArgvItems...))

	sysMod.Dict.Set(val.InternString("executable"), val.NewStringVal(eff.Executable))
	sysMod.Dict.Set(val.InternString("prefix"), val.NewStringVal(disc.prefix))
	sysMod.Dict.Set(val.InternString("exec_prefix"), val.NewStringVal(disc.prefix))
	sysMod.Dict.Set(val.InternString("base_prefix"), val.NewStringVal(disc.basePrefix))
	sysMod.Dict.Set(val.InternString("base_exec_prefix"), val.NewStringVal(disc.basePrefix))
	sysMod.Dict.Set(val.InternString("platlibdir"), val.NewStringVal("lib"))

	sysMod.Dict.Set(val.InternString("version"), val.NewStringVal("3.15.0 (main, fython) [Go]"))
	verInfo := newVersionInfoTuple(3, 15, 0, "final", 0)
	sysMod.Dict.Set(val.InternString("version_info"), verInfo)
	sysMod.Dict.Set(val.InternString("hexversion"), val.Int64Val(0x030f00f0))
	sysMod.Dict.Set(val.InternString("platform"), val.InternString(runtime.GOOS))
	sysMod.Dict.Set(val.InternString("abiflags"), val.InternString(""))
	sysMod.Dict.Set(val.InternString("winver"), val.NoneVal())
	sysMod.Dict.Set(val.InternString("byteorder"), val.InternString("little"))
	sysMod.Dict.Set(val.InternString("maxsize"), val.Int64Val(9223372036854775807))
	sysMod.Dict.Set(val.InternString("float_info"), newFloatInfo())
	sysMod.Dict.Set(val.InternString("hash_info"), newHashInfo())
	sysMod.Dict.Set(val.InternString("dont_write_bytecode"), val.BoolVal(eff.DontWriteBytecode))

	warnItems := make([]val.Value, len(eff.WarnOptions))
	for i, w := range eff.WarnOptions {
		warnItems[i] = val.NewStringVal(w)
	}
	sysMod.Dict.Set(val.InternString("warnoptions"), val.NewListVal(warnItems...))

	xoptsDict := val.NewDictVal(len(eff.XOptions)).AsDict()
	for k, v := range eff.XOptions {
		if v == "" {
			xoptsDict.Set(val.NewStringVal(k), val.BoolVal(true))
		} else {
			xoptsDict.Set(val.NewStringVal(k), val.NewStringVal(v))
		}
	}
	sysMod.Dict.Set(val.InternString("_xoptions"), val.DictVal(xoptsDict))

	implNs := val.NewInstanceOfType(val.ObjectType)
	implInst := implNs.AsInstance()
	implInst.SetAttr(val.InternString("name"), val.InternString("fython"))
	implInst.SetAttr(val.InternString("version"), verInfo)
	implInst.SetAttr(val.InternString("hexversion"), val.Int64Val(0x030f00f0))
	implInst.SetAttr(val.InternString("cache_tag"), val.InternString("fython-315"))
	implInst.SetAttr(val.InternString("_multiarch"), val.InternString(""))
	sysMod.Dict.Set(val.InternString("implementation"), implNs)

	sysMod.Dict.Set(val.InternString("flags"), newSysFlags(eff))

	// PEP 451 / 302 import hooks
	sysMod.Dict.Set(val.InternString("meta_path"), val.NewListVal(
		val.TypeVal(BuiltinImporterType),
		val.TypeVal(FrozenImporterType),
		val.TypeVal(PathFinderType),
	))
	fileFinderHook, _ := FileFinderType.Dict.Get(val.InternString("path_hook"))
	var ffHookCallable val.Value = val.TypeVal(FileFinderType)
	if fileFinderHook.IsBuiltinFunc() {
		ffHookCallable = fileFinderHook.AsBuiltinFunc().Fn(nil, fileFinderHook, nil, nil)
	}
	sysMod.Dict.Set(val.InternString("path_hooks"), val.NewListVal(
		val.TypeVal(ZipImporterType),
		ffHookCallable,
	))
	sysMod.Dict.Set(val.InternString("path_importer_cache"), val.NewDictVal(8))

	// Builtin module names
	builtinNames := []val.Value{
		val.InternString("sys"), val.InternString("builtins"), val.InternString("_imp"),
		val.InternString("_frozen_importlib"), val.InternString("_frozen_importlib_external"),
		val.InternString("zipimport"), val.InternString("site"), val.InternString("_abc"),
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
		val.InternString("_csv"), val.InternString("_heapq"), val.InternString("_bisect"),
		val.InternString("_queue"), val.InternString("atexit"), val.InternString("errno"),
		val.InternString("zlib"), val.InternString("posix"), val.InternString("nt"),
		val.InternString("_signal"), val.InternString("select"), val.InternString("_string"),
		val.InternString("_codecs"), val.InternString("_socket"), val.InternString("_ssl"),
		val.InternString("_sqlite3"), val.InternString("_random"), val.InternString("_ast"),
		val.InternString("locale"), val.InternString("_locale"), val.InternString("gettext"),
		val.InternString("pdb"), val.InternString("marshal"),
	}
	sysMod.Dict.Set(val.InternString("builtin_module_names"), val.NewTupleVal(builtinNames...))

	// sys.intern(string)
	sysMod.Dict.Set(val.InternString("getdefaultencoding"), val.NewBuiltinFuncVal("getdefaultencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("utf-8")
	}))
	sysMod.Dict.Set(val.InternString("getfilesystemencoding"), val.NewBuiltinFuncVal("getfilesystemencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("utf-8")
	}))
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
	outStream := e.stdStream(1)
	errStream := e.stdStream(2)
	inStream := e.stdStream(0)
	sysMod.Dict.Set(val.InternString("stdout"), outStream)
	sysMod.Dict.Set(val.InternString("stderr"), errStream)
	sysMod.Dict.Set(val.InternString("stdin"), inStream)
	sysMod.Dict.Set(val.InternString("__stdout__"), outStream)
	sysMod.Dict.Set(val.InternString("__stderr__"), errStream)
	sysMod.Dict.Set(val.InternString("__stdin__"), inStream)

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

	sysMod.Dict.Set(val.InternString("getrecursionlimit"), val.NewBuiltinFuncVal("getrecursionlimit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(1000)
	}))
	sysMod.Dict.Set(val.InternString("getfilesystemencoding"), val.NewBuiltinFuncVal("getfilesystemencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("utf-8")
	}))
	sysMod.Dict.Set(val.InternString("getfilesystemencodeerrors"), val.NewBuiltinFuncVal("getfilesystemencodeerrors", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("surrogateescape")
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
	asyncioMod := e.createAsyncioModule()
	modules.Set(val.InternString("asyncio"), asyncioMod)
	if coroMod, ok := asyncioMod.AsModule().Dict.Get(val.InternString("coroutines")); ok {
		modules.Set(val.InternString("asyncio.coroutines"), coroMod)
	}
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
	modules.Set(val.InternString("pdb"), e.createPdbModule())
	modules.Set(val.InternString("_heapq"), e.createHeapQModule())
	modules.Set(val.InternString("_bisect"), e.createBisectModule())
	modules.Set(val.InternString("_queue"), e.createQueueModule())
	modules.Set(val.InternString("atexit"), e.createAtexitModule())
	modules.Set(val.InternString("_testcapi"), e.createTestCapiModule())
	testMod, testSupportMod := e.createTestPackage()
	modules.Set(val.InternString("test"), testMod)
	modules.Set(val.InternString("test.support"), testSupportMod)
	if ih, ok := testSupportMod.AsModule().Dict.Get(val.InternString("import_helper")); ok {
		modules.Set(val.InternString("test.support.import_helper"), ih)
	}
	if oh, ok := testSupportMod.AsModule().Dict.Get(val.InternString("os_helper")); ok {
		modules.Set(val.InternString("test.support.os_helper"), oh)
	}
	if wh, ok := testSupportMod.AsModule().Dict.Get(val.InternString("warnings_helper")); ok {
		modules.Set(val.InternString("test.support.warnings_helper"), wh)
	}
	modules.Set(val.InternString("marshal"), e.createMarshalModule())
	modules.Set(val.InternString("_imp"), e.createImpModule())
	modules.Set(val.InternString("_frozen_importlib"), e.createFrozenImportlibModule())
	modules.Set(val.InternString("_frozen_importlib_external"), e.createFrozenImportlibExternalModule())
	modules.Set(val.InternString("zipimport"), e.createZipImportModule())
	modules.Set(val.InternString("site"), e.createSiteModule())

	// Populate __loader__ and __spec__ on all built-in modules
	for _, entry := range modules.Items() {
		if entry.Value.IsModule() {
			m := entry.Value.AsModule()
			m.Dict.Set(val.InternString("__loader__"), val.TypeVal(BuiltinImporterType))
			if pathVal, hasPath := m.Dict.Get(val.InternString("__path__")); hasPath && pathVal.IsList() {
				var locs []string
				for _, it := range pathVal.AsList().Items {
					if it.IsAnyString() {
						locs = append(locs, it.AsString())
					}
				}
				m.Dict.Set(val.InternString("__spec__"), newModuleSpecVal(m.Name, val.TypeVal(BuiltinImporterType), "built-in", true, locs))
			} else {
				m.Dict.Set(val.InternString("__spec__"), newBuiltinModuleSpecVal(m.Name))
			}
		}
	}

	return modules, disc.pthImports
}

var (
	versionInfoTypeOnce sync.Once
	versionInfoType     *val.PyType
)

func newVersionInfoTuple(major, minor, micro int64, releaselevel string, serial int64) val.Value {
	versionInfoTypeOnce.Do(func() {
		versionInfoType = val.MustNewType("version_info", []*val.PyType{val.TupleType}, nil)
		versionInfoType.InvalidateVersion()
	})
	tupVal := val.NewTupleVal(
		val.Int64Val(major),
		val.Int64Val(minor),
		val.Int64Val(micro),
		val.InternString(releaselevel),
		val.Int64Val(serial),
	)
	tup := tupVal.AsTuple()
	tup.Header.Type = versionInfoType
	tup.Attrs = val.NewDictVal(5).AsDict()
	tup.Attrs.Set(val.InternString("major"), val.Int64Val(major))
	tup.Attrs.Set(val.InternString("minor"), val.Int64Val(minor))
	tup.Attrs.Set(val.InternString("micro"), val.Int64Val(micro))
	tup.Attrs.Set(val.InternString("releaselevel"), val.InternString(releaselevel))
	tup.Attrs.Set(val.InternString("serial"), val.Int64Val(serial))
	return tupVal
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func newSysFlags(eff RuntimeConfig) val.Value {
	flags := val.NewInstanceOfType(val.ObjectType)
	inst := flags.AsInstance()
	set := func(name string, v val.Value) { inst.SetAttr(val.InternString(name), v) }
	set("debug", val.Int64Val(0))
	set("inspect", val.Int64Val(boolToInt64(eff.Interactive)))
	set("interactive", val.Int64Val(boolToInt64(eff.Interactive)))
	set("optimize", val.Int64Val(int64(eff.Optimize)))
	set("dont_write_bytecode", val.Int64Val(boolToInt64(eff.DontWriteBytecode)))
	set("no_user_site", val.Int64Val(boolToInt64(eff.NoUserSite)))
	set("no_site", val.Int64Val(boolToInt64(eff.NoSite)))
	set("ignore_environment", val.Int64Val(boolToInt64(eff.IgnoreEnv)))
	set("verbose", val.Int64Val(0))
	set("bytes_warning", val.Int64Val(0))
	set("quiet", val.Int64Val(boolToInt64(eff.Quiet)))
	set("hash_randomization", val.Int64Val(1))
	set("isolated", val.Int64Val(boolToInt64(eff.Isolated)))
	set("dev_mode", val.BoolVal(false))
	set("utf8_mode", val.Int64Val(1))
	set("warn_default_encoding", val.Int64Val(0))
	set("safe_path", val.BoolVal(eff.Isolated))
	set("context_aware_warnings", val.Int64Val(0))
	set("unbuffered", val.Int64Val(boolToInt64(eff.Unbuffered)))
	return flags
}

func (e *Engine) sysPrefix() (string, string) {
	disc := discoverVenvAndPaths(e.runtimeConfig, e.searchPaths)
	return disc.prefix, disc.basePrefix
}

func (e *Engine) createLocaleModule() val.Value {
	modVal := val.NewModuleVal("locale", "Locale support")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("getencoding"), val.NewBuiltinFuncVal("getencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("UTF-8")
	}))
	mod.Dict.Set(val.InternString("getpreferredencoding"), val.NewBuiltinFuncVal("getpreferredencoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("UTF-8")
	}))
	mod.Dict.Set(val.InternString("getlocale"), val.NewBuiltinFuncVal("getlocale", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(val.NewStringVal("en_US"), val.NewStringVal("UTF-8"))
	}))
	mod.Dict.Set(val.InternString("getdefaultlocale"), val.NewBuiltinFuncVal("getdefaultlocale", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(val.NewStringVal("en_US"), val.NewStringVal("UTF-8"))
	}))
	mod.Dict.Set(val.InternString("setlocale"), val.NewBuiltinFuncVal("setlocale", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("C")
	}))
	mod.Dict.Set(val.InternString("normalize"), val.NewBuiltinFuncVal("normalize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			return args[0]
		}
		return val.NewStringVal("")
	}))
	mod.Dict.Set(val.InternString("strcoll"), val.NewBuiltinFuncVal("strcoll", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) >= 2 {
			s1, s2 := args[0].AsString(), args[1].AsString()
			if s1 < s2 {
				return val.Int64Val(-1)
			} else if s1 > s2 {
				return val.Int64Val(1)
			}
		}
		return val.Int64Val(0)
	}))
	mod.Dict.Set(val.InternString("strxfrm"), val.NewBuiltinFuncVal("strxfrm", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			return args[0]
		}
		return val.NewStringVal("")
	}))
	mod.Dict.Set(val.InternString("localeconv"), val.NewBuiltinFuncVal("localeconv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := val.NewDictVal(16).AsDict()
		d.Set(val.InternString("decimal_point"), val.NewStringVal("."))
		d.Set(val.InternString("thousands_sep"), val.NewStringVal(""))
		return val.DictVal(d)
	}))
	mod.Dict.Set(val.InternString("format_string"), val.NewBuiltinFuncVal("format_string", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			return args[0]
		}
		return val.NewStringVal("")
	}))
	mod.Dict.Set(val.InternString("CHAR_MAX"), val.Int64Val(127))
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

// SetRuntimeConfig configures CLI flags and runtime options on this Engine.
func (e *Engine) SetRuntimeConfig(cfg RuntimeConfig) {
	e.runtimeConfig = cfg
}

// RuntimeConfig returns the configured CLI flags and runtime options on this Engine.
func (e *Engine) RuntimeConfig() RuntimeConfig {
	return e.runtimeConfig
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
	var pthImports []string
	if !e.isSharedModules || modules == nil {
		modules, pthImports = e.newSysModulesWithDiscovery()
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
		v.globals.Set(val.InternString("__name__"), val.InternString("__main__"))
		v.globals.Set(val.InternString("__doc__"), val.NoneVal())
		v.globals.Set(val.InternString("__package__"), val.NoneVal())
		v.globals.Set(val.InternString("__loader__"), val.NoneVal())
		v.globals.Set(val.InternString("__spec__"), val.NoneVal())
		modules.Set(val.InternString("__main__"), mainModVal)
	}
	for _, impLine := range pthImports {
		co, err := e.CompileDynamic([]byte(impLine+"\n"), "<pth>", 0)
		if err == nil && co != nil {
			v.RunCode(co)
			v.unhandledException = val.NoneVal()
		}
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
	if e.sharedModules != nil {
		if sysVal, ok := e.sharedModules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
			disc := discoverVenvAndPaths(e.runtimeConfig, e.searchPaths)
			pathItems := make([]val.Value, len(disc.paths))
			for i, p := range disc.paths {
				pathItems[i] = val.NewStringVal(p)
			}
			sysVal.AsModule().Dict.Set(val.InternString("path"), val.NewListVal(pathItems...))
		}
	}
}

// SearchPaths returns the active directory search paths.
func (e *Engine) SearchPaths() []string {
	return e.searchPaths
}

func setCodeFilenameRecursive(co *CodeObject, filename string) {
	if co == nil || filename == "" {
		return
	}
	if co.FileName == "" || co.FileName == "<string>" {
		co.FileName = filename
	}
	for _, c := range co.Consts {
		if c.IsCode() {
			if child := AsCodeObject(c); child != nil {
				setCodeFilenameRecursive(child, filename)
			}
		}
	}
}

// SetCompileHook sets the source compilation callback.
func (e *Engine) SetCompileHook(fn func(src []byte) (*CodeObject, error)) {
	e.compileHook = fn
	if fn == nil {
		e.compileWithFilenameHook = nil
	} else {
		e.compileWithFilenameHook = func(src []byte, filename string) (*CodeObject, error) {
			co, err := fn(src)
			if err == nil && co != nil {
				setCodeFilenameRecursive(co, filename)
			}
			return co, err
		}
	}
}

// SetCompileWithFilenameHook sets the source compilation callback with filename.
func (e *Engine) SetCompileWithFilenameHook(fn func(src []byte, filename string) (*CodeObject, error)) {
	e.compileWithFilenameHook = fn
}

// SetDynamicCompileHook sets the source compilation callback for eval/exec.
func (e *Engine) SetDynamicCompileHook(fn func(src []byte, filename string, mode int) (*CodeObject, error)) {
	e.dynamicCompileHook = fn
}

// SetDynamicCompileOptimizeHook sets the source compilation callback for eval/exec/compile with optimize.
func (e *Engine) SetDynamicCompileOptimizeHook(fn func(src []byte, filename string, mode, optimize int) (*CodeObject, error)) {
	e.dynamicCompileOptimizeHook = fn
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

// CompileDynamicWithOptimize compiles Python source bytes dynamically with an explicit optimize level.
func (e *Engine) CompileDynamicWithOptimize(src []byte, filename string, mode, optimize int) (*CodeObject, error) {
	if e.dynamicCompileOptimizeHook != nil {
		return e.dynamicCompileOptimizeHook(src, filename, mode, optimize)
	}
	if defaultDynamicCompileOptimizeHook != nil {
		return defaultDynamicCompileOptimizeHook(src, filename, mode, optimize)
	}
	return e.CompileDynamic(src, filename, mode)
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
