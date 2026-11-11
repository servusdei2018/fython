package vm

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/servusdei2018/fython/pkg/stdlib"
	"github.com/servusdei2018/fython/pkg/val"
)

// resolveRelativeImport converts a relative import (level > 0) to a canonical absolute module name
// using __package__ or __name__ from callerGlobals according to PEP 328 / PEP 451.
func resolveRelativeImport(name string, level int, callerGlobals *val.PyDict) (string, error) {
	if level == 0 {
		return name, nil
	}

	var basePkg string
	if callerGlobals != nil {
		if pkgVal, ok := callerGlobals.Get(val.InternString("__package__")); ok && pkgVal.IsAnyString() && pkgVal.AsString() != "" {
			basePkg = pkgVal.AsString()
		} else if nameVal, ok := callerGlobals.Get(val.InternString("__name__")); ok && nameVal.IsAnyString() {
			nameStr := nameVal.AsString()
			if _, hasPath := callerGlobals.Get(val.InternString("__path__")); hasPath {
				basePkg = nameStr
			} else if idx := strings.LastIndex(nameStr, "."); idx >= 0 {
				basePkg = nameStr[:idx]
			}
		}
	}

	if basePkg == "" {
		return "", errors.New("ImportError: attempted relative import with no known parent package")
	}

	parts := strings.Split(basePkg, ".")
	steps := level - 1
	if steps >= len(parts) {
		return "", errors.New("ImportError: attempted relative import beyond top-level package")
	}

	remaining := parts[:len(parts)-steps]
	if name != "" {
		remaining = append(remaining, name)
	}
	if len(remaining) == 0 {
		return "", errors.New("ImportError: attempted relative import beyond top-level package")
	}
	return strings.Join(remaining, "."), nil
}

// GetModule looks up a module by name in the VM's sys.modules dictionary.
func (vm *VM) GetModule(name string) (val.Value, bool) {
	if vm == nil || vm.modules == nil {
		return val.NoneVal(), false
	}
	return vm.modules.Get(val.InternString(name))
}

// SysPath returns the current sys.path entries (see liveSysPath).
func (vm *VM) SysPath() []string { return vm.liveSysPath() }

// liveSysPath returns the current string entries of sys.path on the VM,
// falling back to vm.engine.searchPaths if sys.path is unavailable.
func (vm *VM) liveSysPath() []string {
	if vm != nil && vm.modules != nil {
		if sysVal, ok := vm.modules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
			if pathVal, ok := sysVal.AsModule().Dict.Get(val.InternString("path")); ok && pathVal.IsList() {
				items := pathVal.AsList().Items
				out := make([]string, 0, len(items))
				for _, item := range items {
					if item.IsAnyString() {
						out = append(out, item.AsString())
					}
				}
				return out
			}
		}
	}
	if vm != nil && vm.engine != nil {
		return vm.engine.searchPaths
	}
	return []string{"."}
}

// findModuleFile searches the provided search paths for a module or package file.
func (e *Engine) findModuleFile(moduleName string, searchDirs []string) (string, bool, bool) {
	parts := strings.Split(moduleName, ".")
	relPath := filepath.Join(parts...)
	relSlash := path.Join(parts...)

	for _, dir := range searchDirs {
		if dir == "" {
			dir = "."
		}
		if vfsDir, ok := strings.CutPrefix(dir, "stdlib:"); ok {
			initPath := path.Join(vfsDir, relSlash, "__init__.py")
			if _, err := stdlib.StdlibFS.ReadFile(initPath); err == nil {
				return "stdlib:" + initPath, true, true
			}
			modPath := path.Join(vfsDir, relSlash+".py")
			if _, err := stdlib.StdlibFS.ReadFile(modPath); err == nil {
				return "stdlib:" + modPath, false, true
			}
			continue
		}

		// Check zip or wheel archive entry
		if archPath, prefix, arch, ok := splitZipArchivePath(dir); ok && arch != nil {
			if entryPath, isPkg, found := arch.findModule(prefix, relSlash); found {
				return archPath + "/" + entryPath, isPkg, true
			}
			continue
		}

		// 1. Check directory package: dir/pkg/__init__.py
		initPath := filepath.Join(dir, relPath, "__init__.py")
		if info, err := os.Stat(initPath); err == nil && !info.IsDir() {
			return initPath, true, true
		}

		// 2. Check single file module: dir/mod.py
		modPath := filepath.Join(dir, relPath+".py")
		if info, err := os.Stat(modPath); err == nil && !info.IsDir() {
			return modPath, false, true
		}

		// Extension suffixes (cpython-315-*.so / abi3.so / .so / .pyd)
		for _, suf := range extensionSuffixes() {
			extPath := filepath.Join(dir, relPath+suf)
			if info, err := os.Stat(extPath); err == nil && !info.IsDir() {
				return extPath, false, true
			}
		}
		// Extension package __init__<suffix>
		for _, suf := range extensionSuffixes() {
			extInit := filepath.Join(dir, relPath, "__init__"+suf)
			if info, err := os.Stat(extInit); err == nil && !info.IsDir() {
				return extInit, true, true
			}
		}
	}

	// 3. Check embedded stdlib
	if vfsPath, isPkg, found := stdlib.FindModule(moduleName); found {
		return "stdlib:" + vfsPath, isPkg, true
	}

	return "", false, false
}

// LoadOrCompileModule retrieves or compiles a CodeObject for the target file or virtual module.
// Uses Engine singleflight deduplication so concurrent goroutine VMs compile exactly once.
func (e *Engine) LoadOrCompileModule(filePath, moduleName string) (*CodeObject, error) {
	cacheKey := filePath
	if cacheKey == "" {
		cacheKey = moduleName
	}

	// 1. Fast lock-free cache check
	if cached, ok := e.codeCache.Load(cacheKey); ok {
		return cached.(*CodeObject), nil
	}

	// 2. Deduplicate compilation using singleflight
	return e.singleflight.Do(cacheKey, func() (*CodeObject, error) {
		// Re-check cache inside singleflight
		if cached, ok := e.codeCache.Load(cacheKey); ok {
			return cached.(*CodeObject), nil
		}

		// Check virtual in-memory modules first
		if v, ok := e.virtualModules.Load(moduleName); ok {
			srcStr := v.(string)
			if e.compileHook == nil {
				return nil, fmt.Errorf("Engine: no compileHook set to compile virtual module '%s'", moduleName)
			}
			co, err := e.compileHook([]byte(srcStr))
			if err != nil {
				return nil, err
			}
			e.codeCache.Store(cacheKey, co)
			return co, nil
		}

		if vfsPath, ok := strings.CutPrefix(filePath, "stdlib:"); ok {
			src, err := stdlib.ReadModule(vfsPath)
			if err != nil {
				return nil, fmt.Errorf("ModuleNotFoundError: No module named '%s' (%v)", moduleName, err)
			}
			var co *CodeObject
			switch {
			case e.compileWithFilenameHook != nil:
				co, err = e.compileWithFilenameHook(src, filePath)
			case e.compileHook != nil:
				co, err = e.compileHook(src)
			default:
				return nil, fmt.Errorf("Engine: no compileHook set to compile stdlib file '%s'", filePath)
			}
			if err != nil {
				return nil, err
			}
			e.codeCache.Store(cacheKey, co)
			return co, nil
		}

		if filePath == "" {
			return nil, fmt.Errorf("ModuleNotFoundError: No module named '%s'", moduleName)
		}

		// Check if filePath points inside a .zip or .whl archive
		if _, innerPath, arch, ok := splitZipArchivePath(filePath); ok && arch != nil && innerPath != "" {
			src, found := arch.readEntry(innerPath)
			if !found {
				return nil, fmt.Errorf("ModuleNotFoundError: No module named '%s'", moduleName)
			}
			var co *CodeObject
			var err error
			switch {
			case e.compileWithFilenameHook != nil:
				co, err = e.compileWithFilenameHook(src, filePath)
			case e.compileHook != nil:
				co, err = e.compileHook(src)
			default:
				return nil, fmt.Errorf("Engine: no compileHook set to compile zip module '%s'", filePath)
			}
			if err != nil {
				return nil, err
			}
			e.codeCache.Store(cacheKey, co)
			return co, nil
		}

		// Stat source file
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, fmt.Errorf("ModuleNotFoundError: No module named '%s' (%v)", moduleName, err)
		}

		// Determine .fyc bytecode cache path
		dir := filepath.Dir(filePath)
		base := filepath.Base(filePath)
		fycName := strings.TrimSuffix(base, ".py") + ".fyc"
		fycPath := filepath.Join(dir, "__pycache__", fycName)

		// Check if .fyc exists and is valid
		if fycCO, fycHdr, err := LoadFyc(fycPath); err == nil {
			if ValidateFyc(fycHdr, uint64(info.Size()), info.ModTime().UnixNano(), [32]byte{}) {
				e.codeCache.Store(cacheKey, fycCO)
				return fycCO, nil
			}
		}

		// Read source bytes and compile
		src, err := os.ReadFile(filePath)
		if err != nil {
			return nil, err
		}

		var co *CodeObject
		switch {
		case e.compileWithFilenameHook != nil:
			co, err = e.compileWithFilenameHook(src, filePath)
		case e.compileHook != nil:
			co, err = e.compileHook(src)
		default:
			return nil, fmt.Errorf("Engine: no compileHook set to compile source file '%s'", filePath)
		}
		if err != nil {
			return nil, err
		}

		// Save updated .fyc cache unless sys.dont_write_bytecode / -B / PYTHONDONTWRITEBYTECODE is active
		if !e.runtimeConfig.Effective().DontWriteBytecode {
			srcHash := ComputeSourceHash(src)
			_ = SaveFyc(fycPath, co, uint64(info.Size()), info.ModTime().UnixNano(), srcHash)
		}

		e.codeCache.Store(cacheKey, co)
		return co, nil
	})
}

// ImportModule imports the specified module into the VM's module namespace.
func (vm *VM) ImportModule(name string, level int, returnLeaf bool, callerGlobals *val.PyDict) (val.Value, error) {
	canonicalName, err := resolveRelativeImport(name, level, callerGlobals)
	if err != nil {
		return val.NoneVal(), err
	}
	if canonicalName == "" {
		return val.NoneVal(), errors.New("ValueError: Empty module name")
	}

	// Sandbox guardrail: block native-capable modules eagerly at the import
	// statement so lazy PEP 690 resolution cannot defer the check.
	if vm.sandboxBlocksNative() {
		leaf := canonicalName
		if idx := strings.LastIndex(canonicalName, "."); idx >= 0 {
			leaf = canonicalName[idx+1:]
		}
		if canonicalName == "ctypes" || canonicalName == "_ctypes" ||
			strings.HasPrefix(canonicalName, "ctypes.") || strings.HasPrefix(canonicalName, "_ctypes.") ||
			leaf == "_ctypes" || leaf == "ctypes" {
			return val.NoneVal(), errors.New("PermissionError: native extensions and ctypes are disabled by sandbox policy")
		}
	}

	parts := strings.Split(canonicalName, ".")

	if canonicalName == "os" || canonicalName == "os.path" || canonicalName == "posixpath" || canonicalName == "genericpath" {
		vm.ensureOSPath()
		if vmHasException(vm) {
			return val.NoneVal(), nil
		}
	}

	// Check if already fully imported in sys.modules
	internedFull := val.InternString(canonicalName)
	if existing, ok := vm.modules.Get(internedFull); ok {
		if returnLeaf {
			return existing, nil
		}
		topVal, _ := vm.modules.Get(val.InternString(parts[0]))
		return topVal, nil
	}

	// Native modules are created on first import.
	if modVal, ok := vm.materializeBuiltinModule(canonicalName); ok {
		if returnLeaf {
			return modVal, nil
		}
		topVal, _ := vm.modules.Get(val.InternString(parts[0]))
		return topVal, nil
	}

	// Incrementally resolve and load parent packages
	var parentVal val.Value
	for i := range parts {
		currName := strings.Join(parts[:i+1], ".")
		internedCurr := val.InternString(currName)

		if existing, ok := vm.modules.Get(internedCurr); ok {
			parentVal = existing
			continue
		}

		// A native parent package (asyncio, test, ...) has to exist before
		// its submodules can be resolved.
		if modVal, ok := vm.materializeBuiltinModule(currName); ok {
			parentVal = modVal
			continue
		}

		// Check custom finders in sys.meta_path before fallback disk lookup
		if sysVal, ok := vm.modules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
			if mpVal, ok := sysVal.AsModule().Dict.Get(val.InternString("meta_path")); ok && mpVal.IsList() {
				pathArg := val.NoneVal()
				if i > 0 && parentVal.IsModule() {
					if pVal, ok := parentVal.AsModule().Dict.Get(val.InternString("__path__")); ok {
						pathArg = pVal
					}
				}
				customHandled := false
				for _, finder := range mpVal.AsList().Items {
					// Builtin finders (BuiltinImporterType, FrozenImporterType, PathFinderType)
					// are handled in the fast-path below.
					if finder == val.TypeVal(BuiltinImporterType) ||
						finder == val.TypeVal(FrozenImporterType) ||
						finder == val.TypeVal(PathFinderType) {
						continue
					}
					if findSpecFn, ok := vmGetAttrSafe(&vm.execCtx, finder, "find_spec"); ok && !findSpecFn.IsNone() {
						spec := vm.execCtx.Call(findSpecFn, []val.Value{val.NewStringVal(currName), pathArg, val.NoneVal()}, nil)
						if vmHasException(vm) {
							return val.NoneVal(), nil
						}
						if !spec.IsNone() && spec.HeaderPtr() != nil {
							loaderVal := val.NoneVal()
							if l, ok := vmGetAttrSafe(&vm.execCtx, spec, "loader"); ok {
								loaderVal = l
							}
							modVal := val.NoneVal()
							if !loaderVal.IsNone() {
								if createFn, ok := vmGetAttrSafe(&vm.execCtx, loaderVal, "create_module"); ok && !createFn.IsNone() {
									modVal = vm.execCtx.Call(createFn, []val.Value{spec}, nil)
									if vmHasException(vm) {
										return val.NoneVal(), nil
									}
								}
							}
							originStr := ""
							if originVal, ok := vmGetAttrSafe(&vm.execCtx, spec, "origin"); ok && originVal.IsAnyString() {
								originStr = originVal.AsString()
							}
							if modVal.IsNone() || !modVal.IsModule() {
								modVal = val.NewModuleVal(currName, originStr)
							}
							mod := modVal.AsModule()
							if i > 0 {
								mod.Package = strings.Join(parts[:i], ".")
							}
							mod.Dict.Set(val.InternString("__name__"), val.NewStringVal(currName))
							mod.Dict.Set(val.InternString("__loader__"), loaderVal)
							mod.Dict.Set(val.InternString("__spec__"), spec)
							if originStr != "" {
								mod.Dict.Set(val.InternString("__file__"), val.NewStringVal(originStr))
							}
							if pVal, ok := vmGetAttrSafe(&vm.execCtx, spec, "parent"); ok && !pVal.IsNone() {
								mod.Dict.Set(val.InternString("__package__"), pVal)
							} else if mod.Package != "" {
								mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(mod.Package))
							} else {
								mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(""))
							}
							if subLocs, ok := vmGetAttrSafe(&vm.execCtx, spec, "submodule_search_locations"); ok && !subLocs.IsNone() {
								mod.Dict.Set(val.InternString("__path__"), subLocs)
							}

							// Register into sys.modules BEFORE executing bytecode (circular import resolution)
							vm.modules.Set(internedCurr, modVal)

							// Link child module as attribute of parent package
							if i > 0 {
								if parentVal.IsModule() {
									parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
								} else {
									val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
								}
							}

							if !loaderVal.IsNone() {
								if execFn, ok := vmGetAttrSafe(&vm.execCtx, loaderVal, "exec_module"); ok && !execFn.IsNone() {
									vm.execCtx.Call(execFn, []val.Value{modVal}, nil)
									if vmHasException(vm) {
										vm.modules.Delete(internedCurr)
										return val.NoneVal(), nil
									}
								}
							}

							// After module execution, check if module replaced sys.modules[__name__]
							if replaced, ok := vm.modules.Get(internedCurr); ok && !replaced.IsNone() {
								modVal = replaced
							}
							if i > 0 {
								if parentVal.IsModule() {
									parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
								} else {
									val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
								}
							}

							parentVal = modVal
							customHandled = true
							break
						}
					}
				}
				if customHandled {
					continue
				}
			}
		}

		// Determine search directories
		var searchDirs []string
		if i == 0 {
			searchDirs = vm.liveSysPath()
		} else {
			// Search in parent package's __path__
			parentMod := parentVal.AsModule()
			if pathVal, ok := parentMod.Dict.Get(val.InternString("__path__")); ok && pathVal.IsList() {
				for _, item := range pathVal.AsList().Items {
					if item.IsAnyString() {
						searchDirs = append(searchDirs, item.AsString())
					}
				}
			}
			if len(searchDirs) == 0 && parentMod.File != "" {
				searchDirs = []string{filepath.Dir(parentMod.File)}
			}
		}

		// Locate module or check virtual modules
		filePath, isPackage, found := vm.engine.findModuleFile(parts[i], searchDirs)
		if !found {
			// Check if virtual module exists for currName
			if _, ok := vm.engine.virtualModules.Load(currName); ok {
				found = true
			} else if vfsPath, isPkg, ok := stdlib.FindModule(currName); ok {
				filePath = "stdlib:" + vfsPath
				isPackage = isPkg
				found = true
			} else if strings.HasPrefix(currName, "_sysconfigdata_") {
				modVal := val.NewModuleVal(currName, "")
				prefix, base := vm.engine.sysPrefix()
				btv := buildSysconfigData(prefix, base)
				modVal.AsModule().Dict.Set(val.InternString("build_time_vars"), val.DictVal(btv))
				vm.modules.Set(internedCurr, modVal)
				if i > 0 {
					if parentVal.IsModule() {
						parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
					} else {
						val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
					}
				}
				parentVal = modVal
				continue
			} else if currName == "_sysconfig" {
				modVal := vm.createSysconfigModule()
				vm.modules.Set(internedCurr, modVal)
				if i > 0 {
					if parentVal.IsModule() {
						parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
					} else {
						val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
					}
				}
				parentVal = modVal
				continue
			}
		}

		if !found {
			return val.NoneVal(), fmt.Errorf("ModuleNotFoundError: No module named '%s'", currName)
		}

		// Extension fast-path: .so/.pyd modules load via execExtensionModule (no bytecode)
		if isExtensionFile(filePath) {
			if vm.sandboxBlocksNative() {
				return val.NoneVal(), errors.New("PermissionError: native extensions and ctypes are disabled by sandbox policy")
			}
			modVal := val.NewModuleVal(currName, filePath)
			mod := modVal.AsModule()
			if i > 0 {
				mod.Package = strings.Join(parts[:i], ".")
			}
			// Set __package__/__path__ for packages
			if isPackage {
				pkgPath := filepath.Dir(filePath)
				if vfsDir, ok := strings.CutPrefix(filePath, "stdlib:"); ok {
					pkgPath = "stdlib:" + path.Dir(vfsDir)
				}
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(currName))
				mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal(pkgPath)))
			} else {
				if mod.Package != "" {
					mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(mod.Package))
				} else {
					mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(""))
				}
			}
			// Loader/spec
			loaderVal := newExtensionFileLoaderVal(currName, filePath)
			specVal := newModuleSpecVal(currName, loaderVal, filePath, isPackage, nil)
			if isPackage {
				pkgPath := filepath.Dir(filePath)
				// Re-create spec with search locations for packages? Use spec with subLocs
				specVal = newModuleSpecVal(currName, loaderVal, filePath, true, []string{pkgPath})
				mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal(pkgPath)))
			}
			mod.Dict.Set(val.InternString("__loader__"), loaderVal)
			mod.Dict.Set(val.InternString("__spec__"), specVal)
			vm.modules.Set(internedCurr, modVal)
			if i > 0 {
				if parentVal.IsModule() {
					parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
				} else {
					val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
				}
			}
			if err := execExtensionModule(&vm.execCtx, modVal, filePath); err != nil {
				vm.modules.Delete(internedCurr)
				return val.NoneVal(), err
			}
			if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
				vm.modules.Delete(internedCurr)
				return val.NoneVal(), nil
			}
			if replaced, ok := vm.modules.Get(internedCurr); ok && !replaced.IsNone() {
				modVal = replaced
			}
			if i > 0 {
				if parentVal.IsModule() {
					parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
				} else {
					val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
				}
			}
			parentVal = modVal
			continue
		}

		// Load or compile CodeObject
		co, err := vm.engine.LoadOrCompileModule(filePath, currName)
		if err != nil {
			return val.NoneVal(), err
		}

		// Create PyModule
		modVal := val.NewModuleVal(currName, filePath)
		mod := modVal.AsModule()

		if i > 0 {
			mod.Package = strings.Join(parts[:i], ".")
		}
		var subSearchLocs []string
		if isPackage {
			pkgPath := filepath.Dir(filePath)
			if vfsDir, ok := strings.CutPrefix(filePath, "stdlib:"); ok {
				pkgPath = "stdlib:" + path.Dir(vfsDir)
			}
			subSearchLocs = []string{pkgPath}
			mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(currName))
			mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal(pkgPath)))
		} else {
			if mod.Package != "" {
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(mod.Package))
			} else {
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(""))
			}
		}

		// Attach PEP 451 __loader__ and __spec__
		var loaderVal val.Value
		if archPath, innerPath, arch, ok := splitZipArchivePath(filePath); ok && arch != nil && innerPath != "" {
			loaderDir := path.Dir(innerPath)
			if isPackage {
				loaderDir = path.Dir(loaderDir)
			}
			if loaderDir == "." || loaderDir == "/" {
				loaderDir = ""
			}
			loaderVal = newZipImporterInstance(vm, archPath, loaderDir)
		} else {
			loaderVal = newSourceFileLoaderVal(currName, filePath)
		}
		specVal := newModuleSpecVal(currName, loaderVal, filePath, isPackage, subSearchLocs)
		mod.Dict.Set(val.InternString("__loader__"), loaderVal)
		mod.Dict.Set(val.InternString("__spec__"), specVal)

		// CRITICAL: Register into sys.modules BEFORE executing bytecode to resolve circular imports
		vm.modules.Set(internedCurr, modVal)

		// Link child module as attribute of parent package
		if i > 0 {
			if parentVal.IsModule() {
				parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
			} else {
				val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
			}
		}

		// Execute module bytecode
		vm.RunCodeWithGlobals(co, mod.Dict)
		if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
			vm.modules.Delete(internedCurr)
			return val.NoneVal(), nil
		}

		// After module execution, check if module replaced sys.modules[__name__]
		if replaced, ok := vm.modules.Get(internedCurr); ok && !replaced.IsNone() {
			modVal = replaced
		}
		if i > 0 {
			if parentVal.IsModule() {
				parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
			} else {
				val.SetAttrCtx(&vm.execCtx, parentVal, val.InternString(parts[i]), modVal)
			}
		}

		parentVal = modVal
	}

	if returnLeaf {
		leafVal, _ := vm.modules.Get(internedFull)
		return leafVal, nil
	}
	topVal, _ := vm.modules.Get(val.InternString(parts[0]))
	return topVal, nil
}

func init() {
	val.ResolveLazyImportHook = func(v any, mod *val.PyModule, name val.Value, lazyVal val.Value) (val.Value, error) {
		vm, ok := v.(*VM)
		if !ok || vm == nil {
			return val.NoneVal(), errors.New("no active VM to resolve lazy import")
		}
		return vm.resolveLazyImport(mod, name, lazyVal)
	}
}

func (vm *VM) resolveLazyImport(mod *val.PyModule, name val.Value, lazyVal val.Value) (val.Value, error) {
	li := lazyVal.AsLazyImport()
	targetModName := li.Module
	if li.Level > 0 {
		basePkg := li.Package
		if basePkg == "" && mod != nil {
			basePkg = mod.Name
		}
		parts := strings.Split(basePkg, ".")
		if li.Level <= len(parts) {
			prefix := strings.Join(parts[:len(parts)-li.Level+1], ".")
			if li.Module != "" {
				targetModName = prefix + "." + li.Module
			} else {
				targetModName = prefix
			}
		}
	}

	targetMod, err := vm.ImportModule(targetModName, 0, true, nil)
	if err != nil {
		return val.NoneVal(), err
	}

	var res val.Value
	if li.Symbol == "" {
		res = targetMod
	} else {
		res, err = vm.ImportFrom(targetMod, li.Symbol, nil)
		if err != nil {
			return val.NoneVal(), err
		}
	}

	if mod != nil {
		mod.Dict.Set(name, res)
	}
	return res, nil
}

// ImportFrom resolves an attribute or submodule from an imported module.
func (vm *VM) ImportFrom(modVal val.Value, symbolName string, callerGlobals *val.PyDict) (val.Value, error) {
	interned := val.InternString(symbolName)

	// 1. Fast path for standard module dict
	if modVal.IsModule() && modVal.Type() == val.ModuleType {
		mod := modVal.AsModule()
		if v, ok := mod.Dict.Get(interned); ok {
			if v.IsLazyImport() {
				resolved, err := vm.resolveLazyImport(mod, interned, v)
				if err != nil {
					return val.NoneVal(), err
				}
				return resolved, nil
			}
			return v, nil
		}
	}

	// 2. Try val.GetAttrCtx (descriptor / custom ModuleType / PEP 562 __getattr__)
	var attrVal val.Value
	var attrErr any
	func() {
		defer func() {
			if r := recover(); r != nil {
				attrErr = r
			}
		}()
		attrVal = val.GetAttrCtx(&vm.execCtx, modVal, interned)
	}()
	if attrErr == nil && !attrVal.IsNone() && !attrVal.IsUnbound() {
		return attrVal, nil
	}

	// 3. Fall back to loading submodule mod.Name + "." + symbolName
	modName := ""
	if modVal.IsModule() {
		modName = modVal.AsModule().Name
	} else {
		func() {
			defer func() { _ = recover() }()
			nameVal := val.GetAttrCtx(&vm.execCtx, modVal, val.InternString("__name__"))
			if nameVal.IsAnyString() {
				modName = nameVal.AsString()
			}
		}()
	}

	if modName != "" {
		subName := modName + "." + symbolName
		subVal, err := vm.ImportModule(subName, 0, true, callerGlobals)
		if err == nil {
			if modVal.IsModule() {
				modVal.AsModule().Dict.Set(interned, subVal)
			} else {
				func() {
					defer func() { _ = recover() }()
					val.SetAttrCtx(&vm.execCtx, modVal, interned, subVal)
				}()
			}
			return subVal, nil
		}
		if symbolName == "rewrite" {
			fmt.Printf("DEBUG ImportModule(%s) failed: %v\n", subName, err)
		}
	}

	return val.NoneVal(), fmt.Errorf("ImportError: cannot import name '%s' from '%s'", symbolName, modName)
}

// ImportStar copies exported symbols from modVal into callerGlobals.
func (vm *VM) ImportStar(modVal val.Value, callerGlobals *val.PyDict) error {
	if !modVal.IsModule() {
		return fmt.Errorf("ImportError: '%s' is not a module", modVal.String())
	}
	if callerGlobals == nil {
		return nil
	}
	mod := modVal.AsModule()

	// If __all__ is defined, copy only symbols in __all__
	if allVal, ok := mod.Dict.Get(val.InternString("__all__")); ok {
		if allVal.IsList() {
			for _, item := range allVal.AsList().Items {
				if item.IsAnyString() {
					name := item.AsString()
					if v, ok := mod.Dict.Get(val.InternString(name)); ok {
						callerGlobals.Set(val.InternString(name), v)
					}
				}
			}
			return nil
		}
		if allVal.IsTuple() {
			for _, item := range allVal.AsTuple().Items {
				if item.IsAnyString() {
					name := item.AsString()
					if v, ok := mod.Dict.Get(val.InternString(name)); ok {
						callerGlobals.Set(val.InternString(name), v)
					}
				}
			}
			return nil
		}
	}

	// Default: copy all non-private symbols (not starting with '_')
	for _, entry := range mod.Dict.Entries {
		if !entry.Active {
			continue
		}
		name := entry.Key.AsString()
		if !strings.HasPrefix(name, "_") {
			callerGlobals.Set(entry.Key, entry.Value)
		}
	}
	return nil
}

func vmHasException(vm *VM) bool {
	return !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil
}

var (
	pathAttr    = val.InternString("path")
	osModName   = val.InternString("os")
	osPathName  = val.InternString("os.path")
	posixpathNm = "posixpath"
)

// ensureOSPath binds os.path to the upstream posixpath module. posixpath
// imports os itself, so it cannot load while the engine builds its native
// modules; the first import of os (or os.path) triggers it instead. A
// failed load leaves os.path unset and the exception pending on the VM.
func (vm *VM) ensureOSPath() {
	osVal, ok := vm.modules.Get(osModName)
	if !ok || !osVal.IsModule() {
		// The os module is created on demand: materialize it now so that
		// os.path can be bound to it.
		if modVal, materialized := vm.materializeBuiltinModule("os"); materialized {
			osVal = modVal
		} else {
			return
		}
	}
	osMod := osVal.AsModule()
	if _, bound := osMod.Dict.Get(pathAttr); bound || vm.bindingOSPath {
		return
	}
	vm.engine.osPathMu.Lock()
	defer vm.engine.osPathMu.Unlock()
	if _, bound := osMod.Dict.Get(pathAttr); bound {
		return
	}
	vm.bindingOSPath = true
	defer func() { vm.bindingOSPath = false }()

	pv, err := vm.ImportModule(posixpathNm, 0, true, nil)
	if err != nil {
		vm.unhandledException = panicToPyException(err)
		return
	}
	if vmHasException(vm) || !pv.IsModule() {
		return
	}
	osMod.Dict.Set(pathAttr, pv)
	vm.modules.Set(osPathName, pv)
}
