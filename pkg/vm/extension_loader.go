package vm

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/capi"
	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

// Extension suffixes per platform (CPython 3.15 + PEP 384 abi3).
func extensionSuffixes() []string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	var triple string
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			triple = "x86_64-linux-gnu"
		case "arm64":
			triple = "aarch64-linux-gnu"
		default:
			triple = goarch + "-linux"
		}
	case "darwin":
		triple = "darwin"
	case "windows":
		triple = "win32"
	default:
		triple = goos + "-" + goarch
	}
	if goos == "windows" {
		return []string{".cpython-315-" + triple + ".pyd", ".pyd", ".abi3.pyd"}
	}
	if goos == "darwin" {
		return []string{".cpython-315-" + triple + ".so", ".cpython-315-darwin.so", ".abi3.so", ".so"}
	}
	return []string{".cpython-315-" + triple + ".so", ".abi3.so", ".so"}
}

// Shim loading — singleton.
var (
	shimOnce   sync.Once
	shimHandle dynload.Handle
	shimLoaded bool
)

func ensureShim() (dynload.Handle, error) {
	shimOnce.Do(func() {
		candidates := shimCandidates()
		for _, p := range candidates {
			h, err := dynload.Open(p)
			if err == nil {
				shimHandle = h
				shimLoaded = true
				_ = capi.BindToShim(h)
				return
			}
		}
		// Shim not found — extension loading disabled. Not an error for pure Python.
		shimLoaded = false
	})
	if shimLoaded {
		return shimHandle, nil
	}
	return 0, errors.New("fython: ABI shim not found (build shim/libfython_abi3.*); extension loading disabled")
}

func shimCandidates() []string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	// Env override
	if v := os.Getenv("FYTHON_SHIM"); v != "" {
		return []string{v}
	}
	exe, _ := os.Executable()
	dir := ""
	if exe != "" {
		if idx := strings.LastIndex(exe, "/"); idx >= 0 {
			dir = exe[:idx]
		}
	}
	// Also allow cwd-relative shim/
	cwd, _ := os.Getwd()
	var names []string
	switch goos {
	case "linux":
		names = []string{"libfython_abi3.so"}
	case "darwin":
		names = []string{"libfython_abi3.dylib"}
	case "windows":
		names = []string{"python315.dll", "fython_abi3.dll", "libfython_abi3.dll"}
	default:
		names = []string{"libfython_abi3.so"}
	}
	var out []string
	for _, n := range names {
		if dir != "" {
			out = append(out, dir+"/"+n)
		}
		out = append(out, n)
		if cwd != "" {
			out = append(out, cwd+"/"+n, cwd+"/shim/"+n)
		}
		out = append(out, "shim/"+n)
	}
	_ = goarch
	return out
}

// isExtensionFile reports whether path ends with a known extension suffix.
func isExtensionFile(path string) bool {
	for _, s := range extensionSuffixes() {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// execExtensionModule dlopens path and initializes module via PyInit_<shortname> (single-phase)
// or PyModuleDef_Init (multi-phase exported as PyInit_<name> returning def? Actually multi-phase exports PyInit that returns def*? No: PEP 489 multi-phase exports an init function returning 0 with PyModuleDef_Init? Let's handle both:
//
// Single-phase: PyMODINIT_FUNC PyInit_mod(void) returns PyObject* module (via PyModule_Create2).
// Multi-phase: PyMODINIT_FUNC PyInit_mod(void) calls PyModuleDef_Init(&def) then returns 0/NULL? Actually it returns NULL and def is registered? No, pattern:
//   static struct PyModuleDef def = {...}; PyMODINIT_FUNC PyInit_mod(void) { return PyModuleDef_Init(&def); }
// In that case PyInit returns def pointer (as PyObject*). Loader detects return value that is a PyModuleDef (check via flag? We can't distinguish. Approach: if PyInit returns non-NULL and FromStub gives a Module Value, treat as single-phase; else treat returned pointer as PyModuleDef* and drive multi-phase: create module via PyModule_Create2(def) ??? But PyModule_Create2 already called by extension? Hmm.
//
// Correct multi-phase handling (PEP 489): PyModuleDef_Init(def) is called BY extension's PyInit, which registers def and returns NULL? Actually doc: PyModuleDef_Init returns def pointer, not module. Extension's PyInit returns that, importer then calls PyModule_Create2? The boilerplate is:
//   static struct PyModuleDef moddef = {...};
//   PyMODINIT_FUNC PyInit_mymod(void) { return PyModuleDef_Init(&moddef); }
// Importer: def = PyInit_mymod(); mod = PyModule_FromDefAndSpec(def, spec) — which calls Py_mod_create/exec slots.
// For fython, after PyModuleDef_Init we need to create module from def (via PyModule_Create2) then call slots.
//
// Our loader will:
//  1. GIL acquire + bind ctx
//  2. dlsym PyInit_<shortname>
//  3. call it (via purego) with GIL held, converting result:
//     - If HasError -> fail
//     - If result == nil -> import error
//     - Try FromStub(result): if it is a Module Value -> single-phase success, use that module
//     - Else treat result as PyModuleDef* -> read def to get name, create module via PyModule_Create2 handler (call capi.PyModule_Create2 directly), register exec slots, run exec slots.
//  4. Populate module dict with methods already added via PyModule_Create2/AddObject (done inside handlers).
//  5. Return module Value

func execExtensionModule(ctx *val.ExecContext, modVal val.Value, filePath string) error {
	shimHandle, err := ensureShim()
	if err != nil {
		return err
	}
	_ = shimHandle
	// Short name = leaf without suffix, per CPython: module name is last component of fullname (e.g., pkg.mod -> PyInit_mod)
	mod := modVal.AsModule()
	fullName := mod.Name
	short := fullName
	if idx := strings.LastIndex(fullName, "."); idx >= 0 {
		short = fullName[idx+1:]
	}
	initName := "PyInit_" + short

	release := capi.AcquireGIL(ctx)
	defer release()
	// Ensure ctx saved GIL (AcquireGIL already bound).
	h, err := dynload.Open(filePath)
	if err != nil {
		return fmt.Errorf("ImportError: %v", err)
	}
	// Do not Dlclose — keep loaded for process lifetime (modules stay).
	sym, err := dynload.Sym(h, initName)
	if err != nil {
		return fmt.Errorf("ImportError: %v (no %s entry point)", err, initName)
	}
	var pyInit func() unsafe.Pointer
	dynload.RegisterFunc(&pyInit, sym)
	// Call PyInit
	// Use recover to catch unexpected Go panics from handlers (handlers shouldn't panic, they set errors).
	var result unsafe.Pointer
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		result = pyInit()
	}()
	if panicked != nil {
		if ctx != nil && ctx.HasException() {
			return errors.New("exception during import")
		}
		return fmt.Errorf("ImportError: panic in %s: %v", initName, panicked)
	}
	if ctx != nil && ctx.HasException() {
		return errors.New("exception during import")
	}
	if result == nil {
		return fmt.Errorf("ImportError: %s returned NULL", initName)
	}
	// Check if result is module (single-phase)
	// Use arena reverse lookup: if FromStub gives Module kind, it's single-phase.
	v := val.ArenaFromStub(result)
	if v.IsModule() {
		// Single-phase: extension already created module and added methods via PyModule_AddObject.
		// fython's module dict is v's dict; merge into modVal's dict (loader expects modVal to be used).
		// Instead, copy dict entries from v into modVal? Or replace modVal reference in sys.modules with v?
		// Loader's caller (ImportModule fast-path) already registered modVal before exec. For extension, we need to ensure sys.modules points to the module returned by PyInit (v), not the placeholder modVal.
		// So we signal caller via mod.Dict replacement: copy all entries from v into modVal, and update modVal.Name/File.
		// Simpler: caller will check if exec returned a different module? Our execExtensionModule mutates modVal in place.
		src := v.AsModule()
		for _, it := range src.Dict.Items() {
			mod.Dict.Set(it.Key, it.Value)
		}
		// Preserve file/loader info
		if mod.File == "" {
			mod.File = filePath
		}
		return nil
	}
	// Else treat as PyModuleDef* (multi-phase)
	// Read def
	name, _, size, _, slots, _ := capi.ReadModuleDef(result)
	// If name empty, fall back to short
	_ = name
	// Create module from def via handler
	modStub := capi.PyModule_Create2(result, 0)
	if modStub == nil {
		return errors.New("ImportError: PyModule_Create2 failed")
	}
	createdVal := val.ArenaFromStub(modStub)
	// Merge created module dict into modVal? Or replace?
	// multi-phase createdVal may be a fresh module; copy into modVal and run exec slots.
	if createdVal.IsModule() {
		src := createdVal.AsModule()
		for _, it := range src.Dict.Items() {
			mod.Dict.Set(it.Key, it.Value)
		}
		if slots != nil {
			// Py_mod_exec signature is int (*)(PyObject *module) — one arg.
			execFn := findExecSlot(slots)
			if execFn != nil {
				var exec func(mod unsafe.Pointer) int32
				dynload.RegisterFunc(&exec, uintptr(execFn))
				ret := exec(modStub)
				if ret != 0 {
					return errors.New("ImportError: module exec failed")
				}
				// exec may have added attributes to modStub (createdVal's dict).
				// Copy again after exec (exec uses createdVal's dict via PyModule_Add*).
				// Since modStub is createdVal's stub, its dict now holds exec additions; re-copy.
				for _, it := range createdVal.AsModule().Dict.Items() {
					mod.Dict.Set(it.Key, it.Value)
				}
			}
		}
		_ = size
		return nil
	}
	return errors.New("ImportError: PyInit returned non-module/invalid def")
}

func findExecSlot(slots unsafe.Pointer) unsafe.Pointer {
	for i := range 16 {
		slotAddr := unsafe.Add(slots, i*16)
		slotID := *(*int32)(slotAddr)
		valPtr := *(*unsafe.Pointer)(unsafe.Add(slotAddr, 8))
		if slotID == 0 {
			break
		}
		if slotID == 2 || slotID == 85 { // exec
			return valPtr
		}
	}
	return nil
}
