package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

// builtinModuleLoader creates a native module on demand.
//
// The loader receives the VM that requested the module so that it can register
// companion modules (asyncio.coroutines, test.support, ...) and so that modules
// that share a single implementation (io and _io, posix and nt, locale and
// _locale) can hand out the very same module object.
type builtinModuleLoader func(vm *VM) val.Value

// builtinModuleLoaders maps the name of every native module to its
// constructor.
//
// The modules are materialized the first time they are imported instead of at
// startup. Building all of them eagerly dominated the cold start of the
// interpreter and inflated its resident set, even though a typical program
// only ever touches a handful of them.
var builtinModuleLoaders map[string]builtinModuleLoader

func init() {
	builtinModuleLoaders = map[string]builtinModuleLoader{
		"json": func(vm *VM) val.Value { return vm.engine.createJSONModule() },
		"math": func(vm *VM) val.Value { return vm.engine.createMathModule() },
		"time": func(vm *VM) val.Value { return vm.engine.createTimeModule() },
		"os":   func(vm *VM) val.Value { return vm.engine.createOSModule() },
		"io":   func(vm *VM) val.Value { return vm.engine.createIOModule() },
		"_io":  func(vm *VM) val.Value { return vm.engine.createIOModule() },
		"asyncio": func(vm *VM) val.Value {
			asyncioMod := vm.engine.createAsyncioModule()
			if coroMod, ok := asyncioMod.AsModule().Dict.Get(val.InternString("coroutines")); ok {
				vm.modules.Set(val.InternString("asyncio.coroutines"), coroMod)
				finalizeBuiltinModule(coroMod)
			}
			return asyncioMod
		},
		"_weakref":          func(vm *VM) val.Value { return vm.engine.createWeakrefCModule() },
		"weakref":           func(vm *VM) val.Value { return vm.engine.createWeakrefModule() },
		"traceback":         func(vm *VM) val.Value { return vm.engine.createTracebackModule() },
		"inspect":           func(vm *VM) val.Value { return vm.engine.createInspectModule() },
		"gc":                func(vm *VM) val.Value { return vm.engine.createGCModule() },
		"types":             func(vm *VM) val.Value { return vm.engine.createTypesModule() },
		"_types":            func(vm *VM) val.Value { return vm.engine.createTypesModule() },
		"_abc":              func(vm *VM) val.Value { return vm.engine.createABCModule() },
		"_collections":      func(vm *VM) val.Value { return vm.engine.createCollectionsModule() },
		"itertools":         func(vm *VM) val.Value { return vm.engine.createItertoolsModule() },
		"_functools":        func(vm *VM) val.Value { return vm.engine.createFunctoolsModule() },
		"operator":          func(vm *VM) val.Value { return vm.engine.createOperatorModule() },
		"_operator":         func(vm *VM) val.Value { return vm.engine.createOperatorModule() },
		"_sre":              func(vm *VM) val.Value { return vm.engine.createSREModule() },
		"unicodedata":       func(vm *VM) val.Value { return vm.engine.createUnicodedataModule() },
		"re":                func(vm *VM) val.Value { return vm.engine.createReModule() },
		"_ast":              func(vm *VM) val.Value { return vm.engine.createASTModule() },
		"_thread":           func(vm *VM) val.Value { return vm.engine.createThreadModule() },
		"threading":         func(vm *VM) val.Value { return vm.engine.createThreadingModule() },
		"_typing":           func(vm *VM) val.Value { return vm.engine.createTypingModule() },
		"_tokenize":         func(vm *VM) val.Value { return vm.engine.createTokenizeModule() },
		"faulthandler":      func(vm *VM) val.Value { return vm.engine.createFaulthandlerModule() },
		"_colorize":         func(vm *VM) val.Value { return vm.engine.createColorizeModule() },
		"_contextvars":      func(vm *VM) val.Value { return vm.engine.createContextVarsModule() },
		"_signal":           func(vm *VM) val.Value { return vm.engine.createSignalModule() },
		"posix":             func(vm *VM) val.Value { return vm.engine.createPosixModule() },
		"nt":                func(vm *VM) val.Value { return vm.engine.createPosixModule() },
		"_posixsubprocess":  func(vm *VM) val.Value { return vm.engine.createPosixSubprocessModule() },
		"select":            func(vm *VM) val.Value { return vm.engine.createSelectModule() },
		"_string":           func(vm *VM) val.Value { return vm.engine.createStringModule() },
		"_codecs":           func(vm *VM) val.Value { return vm.engine.createCodecsModule() },
		"_socket":           func(vm *VM) val.Value { return vm.engine.createSocketModule() },
		"_ssl":              func(vm *VM) val.Value { return vm.engine.createSSLModule() },
		"_sqlite3":          func(vm *VM) val.Value { return vm.engine.createSqlite3Module() },
		"_random":           func(vm *VM) val.Value { return vm.engine.createRandomModule() },
		"_datetime":         func(vm *VM) val.Value { return vm.engine.createDateTimeModule() },
		"binascii":          func(vm *VM) val.Value { return vm.engine.createBinasciiModule() },
		"_hashlib":          func(vm *VM) val.Value { return vm.engine.createHashlibModule() },
		"_struct":           func(vm *VM) val.Value { return vm.engine.createStructModule() },
		"_csv":              func(vm *VM) val.Value { return vm.engine.createCsvModule() },
		"zlib":              func(vm *VM) val.Value { return vm.engine.createZlibModule() },
		"errno":             func(vm *VM) val.Value { return vm.engine.createErrnoModule() },
		"termios":           func(vm *VM) val.Value { return vm.engine.createTermiosModule() },
		"fcntl":             func(vm *VM) val.Value { return vm.engine.createFcntlModule() },
		"resource":          func(vm *VM) val.Value { return vm.engine.createResourceModule() },
		"syslog":            func(vm *VM) val.Value { return vm.engine.createSyslogModule() },
		"grp":               func(vm *VM) val.Value { return vm.engine.createGrpModule() },
		"pwd":               func(vm *VM) val.Value { return vm.engine.createPwdModule() },
		"array":             func(vm *VM) val.Value { return vm.engine.createArrayModule() },
		"mmap":              func(vm *VM) val.Value { return vm.engine.createMmapModule() },
		"_tracemalloc":      func(vm *VM) val.Value { return vm.engine.createTracemallocModule() },
		"_statistics":       func(vm *VM) val.Value { return vm.engine.createStatisticsExtModule() },
		"_zoneinfo":         func(vm *VM) val.Value { return vm.engine.createZoneInfoModule() },
		"multiprocessing":   func(vm *VM) val.Value { return vm.engine.createMultiprocessingModule() },
		"_multiprocessing":  func(vm *VM) val.Value { return vm.engine.createMultiprocessingCoreModule() },
		"gettext":           func(vm *VM) val.Value { return vm.engine.createGettextModule() },
		"locale":            func(vm *VM) val.Value { return vm.engine.createLocaleModule() },
		"_locale":           func(vm *VM) val.Value { return vm.engine.createLocaleModule() },
		"pdb":               func(vm *VM) val.Value { return vm.engine.createPdbModule() },
		"_heapq":            func(vm *VM) val.Value { return vm.engine.createHeapQModule() },
		"_bisect":           func(vm *VM) val.Value { return vm.engine.createBisectModule() },
		"_queue":            func(vm *VM) val.Value { return vm.engine.createQueueModule() },
		"atexit":            func(vm *VM) val.Value { return vm.engine.createAtexitModule() },
		"_testcapi":         func(vm *VM) val.Value { return vm.engine.createTestCapiModule() },
		"_ctypes":           func(vm *VM) val.Value { return vm.engine.createCtypesLowerModule() },
		"marshal":           func(vm *VM) val.Value { return vm.engine.createMarshalModule() },
		"_imp":              func(vm *VM) val.Value { return vm.engine.createImpModule() },
		"_frozen_importlib": func(vm *VM) val.Value { return vm.engine.createFrozenImportlibModule() },
		"_frozen_importlib_external": func(vm *VM) val.Value {
			return vm.engine.createFrozenImportlibExternalModule()
		},
		"zipimport": func(vm *VM) val.Value { return vm.engine.createZipImportModule() },
		"site":      func(vm *VM) val.Value { return vm.engine.createSiteModule() },
		"test": func(vm *VM) val.Value {
			testMod, testSupportMod := vm.engine.createTestPackage()
			vm.modules.Set(val.InternString("test.support"), testSupportMod)
			for _, sub := range []string{"import_helper", "os_helper", "warnings_helper"} {
				if helper, ok := testSupportMod.AsModule().Dict.Get(val.InternString(sub)); ok {
					vm.modules.Set(val.InternString("test.support."+sub), helper)
				}
			}
			return testMod
		},
	}
}

// builtinModuleAliases maps the alternative names of a native module to its
// primary name. The alias and the primary resolve to the same module object.
var builtinModuleAliases = map[string]string{
	"_io":     "io",
	"nt":      "posix",
	"_locale": "locale",
}

// materializeBuiltinModule creates the native module with the given name and
// registers it in sys.modules. It reports whether such a module exists.
func (vm *VM) materializeBuiltinModule(name string) (val.Value, bool) {
	loader, ok := builtinModuleLoaders[name]
	if !ok {
		return val.NoneVal(), false
	}
	// The creation is serialised: two VMs that share a module namespace
	// must not create two copies of the same module.
	vm.engine.builtinModuleMu.Lock()
	defer vm.engine.builtinModuleMu.Unlock()
	return vm.materializeBuiltinModuleLocked(name, loader)
}

// materializeBuiltinModuleLocked creates a native module; the caller holds the
// engine's module lock.
func (vm *VM) materializeBuiltinModuleLocked(name string, loader builtinModuleLoader) (val.Value, bool) {
	if existing, found := vm.modules.Get(val.InternString(name)); found {
		return existing, true
	}
	// An alias resolves to the module of its primary name.
	if primary, isAlias := builtinModuleAliases[name]; isAlias {
		if primaryLoader, ok := builtinModuleLoaders[primary]; ok {
			modVal, created := vm.materializeBuiltinModuleLocked(primary, primaryLoader)
			if created {
				vm.modules.Set(val.InternString(name), modVal)
			}
			return modVal, created
		}
	}
	modVal := loader(vm)
	if modVal.IsNone() || modVal.HeaderPtr() == nil {
		return val.NoneVal(), false
	}
	if vm.engine.sharedModules != nil && vm.modules == vm.engine.sharedModules {
		// The module is reachable from every VM of the engine.
		modVal.AsModule().Dict.Shared = true
	}
	vm.modules.Set(val.InternString(name), modVal)
	finalizeBuiltinModule(modVal)
	// The alternative names of the module resolve to it as well.
	for alias, primary := range builtinModuleAliases {
		if primary == name {
			vm.modules.Set(val.InternString(alias), modVal)
			finalizeBuiltinModule(modVal)
		}
	}
	return modVal, true
}

// finalizeBuiltinModule installs the __loader__ and __spec__ attributes of a
// native module.
func finalizeBuiltinModule(modVal val.Value) {
	if !modVal.IsModule() {
		return
	}
	m := modVal.AsModule()
	m.Dict.Set(val.InternString("__loader__"), val.TypeVal(BuiltinImporterType))
	if pathVal, hasPath := m.Dict.Get(val.InternString("__path__")); hasPath && pathVal.IsList() {
		var locs []string
		for _, it := range pathVal.AsList().Items {
			if it.IsAnyString() {
				locs = append(locs, it.AsString())
			}
		}
		m.Dict.Set(val.InternString("__spec__"), newModuleSpecVal(m.Name, val.TypeVal(BuiltinImporterType), "built-in", true, locs))
		return
	}
	m.Dict.Set(val.InternString("__spec__"), newBuiltinModuleSpecVal(m.Name))
}
