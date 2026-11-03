package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			basePkg = nameVal.AsString()
		}
	}

	if basePkg == "" {
		return "", fmt.Errorf("ImportError: attempted relative import with no known parent package")
	}

	parts := strings.Split(basePkg, ".")
	steps := level - 1
	if steps > len(parts) {
		return "", fmt.Errorf("ImportError: attempted relative import beyond top-level package")
	}

	remaining := parts[:len(parts)-steps]
	if name != "" {
		remaining = append(remaining, name)
	}
	if len(remaining) == 0 {
		return "", fmt.Errorf("ImportError: attempted relative import beyond top-level package")
	}
	return strings.Join(remaining, "."), nil
}

// findModuleFile searches the provided search paths for a module or package file.
func (e *Engine) findModuleFile(moduleName string, searchDirs []string) (string, bool, bool) {
	parts := strings.Split(moduleName, ".")
	relPath := filepath.Join(parts...)

	for _, dir := range searchDirs {
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

		if filePath == "" {
			return nil, fmt.Errorf("ModuleNotFoundError: No module named '%s'", moduleName)
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

		if e.compileHook == nil {
			return nil, fmt.Errorf("Engine: no compileHook set to compile source file '%s'", filePath)
		}

		co, err := e.compileHook(src)
		if err != nil {
			return nil, err
		}

		// Save updated .fyc cache asynchronously/silently
		srcHash := ComputeSourceHash(src)
		_ = SaveFyc(fycPath, co, uint64(info.Size()), info.ModTime().UnixNano(), srcHash)

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
		return val.NoneVal(), fmt.Errorf("ValueError: Empty module name")
	}

	parts := strings.Split(canonicalName, ".")

	// Check if already fully imported in sys.modules
	internedFull := val.InternString(canonicalName)
	if existing, ok := vm.modules.Get(internedFull); ok {
		if returnLeaf {
			return existing, nil
		}
		topVal, _ := vm.modules.Get(val.InternString(parts[0]))
		return topVal, nil
	}

	// Incrementally resolve and load parent packages
	var parentVal val.Value
	for i := 0; i < len(parts); i++ {
		currName := strings.Join(parts[:i+1], ".")
		internedCurr := val.InternString(currName)

		if existing, ok := vm.modules.Get(internedCurr); ok {
			parentVal = existing
			continue
		}

		// Determine search directories
		var searchDirs []string
		if i == 0 {
			searchDirs = vm.engine.searchPaths
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
			}
		}

		if !found {
			return val.NoneVal(), fmt.Errorf("ModuleNotFoundError: No module named '%s'", currName)
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
		if isPackage {
			mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(currName))
			mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal(filepath.Dir(filePath))))
		} else {
			if mod.Package != "" {
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(mod.Package))
			} else {
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(""))
			}
		}

		// CRITICAL: Register into sys.modules BEFORE executing bytecode to resolve circular imports
		vm.modules.Set(internedCurr, modVal)

		// Link child module as attribute of parent package
		if i > 0 && parentVal.IsModule() {
			parentVal.AsModule().Dict.Set(val.InternString(parts[i]), modVal)
		}

		// Execute module bytecode
		vm.RunCodeWithGlobals(co, mod.Dict)

		parentVal = modVal
	}

	if returnLeaf {
		leafVal, _ := vm.modules.Get(internedFull)
		return leafVal, nil
	}
	topVal, _ := vm.modules.Get(val.InternString(parts[0]))
	return topVal, nil
}

// ImportFrom resolves an attribute or submodule from an imported module.
func (vm *VM) ImportFrom(modVal val.Value, symbolName string, callerGlobals *val.PyDict) (val.Value, error) {
	if !modVal.IsModule() {
		return val.NoneVal(), fmt.Errorf("ImportError: '%s' is not a module", modVal.String())
	}
	mod := modVal.AsModule()
	interned := val.InternString(symbolName)

	// 1. Check module dictionary
	if v, ok := mod.Dict.Get(interned); ok {
		return v, nil
	}

	// 2. Fall back to loading submodule mod.Name + "." + symbolName
	subName := mod.Name + "." + symbolName
	if subVal, err := vm.ImportModule(subName, 0, true, callerGlobals); err == nil {
		mod.Dict.Set(interned, subVal)
		return subVal, nil
	}

	return val.NoneVal(), fmt.Errorf("ImportError: cannot import name '%s' from '%s'", symbolName, mod.Name)
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
