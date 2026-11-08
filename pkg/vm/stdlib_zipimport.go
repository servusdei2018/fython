package vm

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

type cachedZipArchive struct {
	archivePath string
	modTimeNano int64
	size        int64
	files       map[string][]byte
	dirs        map[string]bool
}

var (
	zipArchiveCacheMap sync.Map // map[string]*cachedZipArchive
	zipImportOnce      sync.Once
	ZipImportErrorType *val.PyType
	ZipImporterType    *val.PyType
)

// RegisterVirtualZipArchive registers an in-memory zip/wheel archive under virtualPath.
func RegisterVirtualZipArchive(virtualPath string, raw []byte) (*cachedZipArchive, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(zr.File))
	dirs := make(map[string]bool)
	for _, f := range zr.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "/")
		if strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
			trimmed := strings.TrimSuffix(name, "/")
			if trimmed != "" {
				dirs[trimmed] = true
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			continue
		}
		files[name] = data
		dir := path.Dir(name)
		for dir != "." && dir != "/" && dir != "" {
			dirs[dir] = true
			dir = path.Dir(dir)
		}
	}
	cleanPath := filepath.Clean(virtualPath)
	cached := &cachedZipArchive{
		archivePath: cleanPath,
		size:        int64(len(raw)),
		files:       files,
		dirs:        dirs,
	}
	zipArchiveCacheMap.Store(cleanPath, cached)
	return cached, nil
}

// loadZipArchive opens and caches a zip/wheel archive in memory without extracting to disk.
func loadZipArchive(archivePath string) (*cachedZipArchive, error) {
	cleanPath := filepath.Clean(archivePath)
	if v, ok := zipArchiveCacheMap.Load(cleanPath); ok {
		cached := v.(*cachedZipArchive)
		if cached.modTimeNano == 0 {
			return cached, nil
		}
		if info, err := os.Stat(cleanPath); err == nil {
			if cached.modTimeNano == info.ModTime().UnixNano() && cached.size == info.Size() {
				return cached, nil
			}
		} else {
			return cached, nil
		}
	}

	info, err := os.Stat(cleanPath)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("not a zip file: %s", archivePath)
	}

	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}

	files := make(map[string][]byte, len(zr.File))
	dirs := make(map[string]bool)
	for _, f := range zr.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "/")
		if strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
			trimmed := strings.TrimSuffix(name, "/")
			if trimmed != "" {
				dirs[trimmed] = true
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			continue
		}
		files[name] = data
		dir := path.Dir(name)
		for dir != "." && dir != "/" && dir != "" {
			dirs[dir] = true
			dir = path.Dir(dir)
		}
	}

	cached := &cachedZipArchive{
		archivePath: cleanPath,
		modTimeNano: info.ModTime().UnixNano(),
		size:        info.Size(),
		files:       files,
		dirs:        dirs,
	}
	zipArchiveCacheMap.Store(cleanPath, cached)
	return cached, nil
}

// splitZipArchivePath splits a path like "/path/to/pkg.whl/sub/dir" or "/path/to/pkg.whl"
// into ("/path/to/pkg.whl", "sub/dir", cachedArchive, true) if a valid zip archive prefix exists.
func splitZipArchivePath(target string) (string, string, *cachedZipArchive, bool) {
	if target == "" || strings.HasPrefix(target, "stdlib:") {
		return "", "", nil, false
	}
	cleanTarget := filepath.Clean(target)
	if v, ok := zipArchiveCacheMap.Load(cleanTarget); ok {
		return cleanTarget, "", v.(*cachedZipArchive), true
	}
	slashTarget := filepath.ToSlash(cleanTarget)

	// Fast check: must contain .zip, .whl, .egg, or .pyz somewhere in the path, or be a file directly.
	lower := strings.ToLower(slashTarget)
	hasZipExt := strings.Contains(lower, ".zip") || strings.Contains(lower, ".whl") ||
		strings.Contains(lower, ".egg") || strings.Contains(lower, ".pyz")

	if !hasZipExt {
		if arch, err := loadZipArchive(cleanTarget); err == nil {
			return cleanTarget, "", arch, true
		}
		curr := cleanTarget
		var suffixParts []string
		for {
			parent := filepath.Dir(curr)
			if parent == curr || parent == "." || parent == string(filepath.Separator) {
				break
			}
			suffixParts = append(suffixParts, filepath.Base(curr))
			curr = parent
			if v, ok := zipArchiveCacheMap.Load(curr); ok {
				prefix := ""
				for i, j := 0, len(suffixParts)-1; i < j; i, j = i+1, j-1 {
					suffixParts[i], suffixParts[j] = suffixParts[j], suffixParts[i]
				}
				prefix = strings.Join(suffixParts, "/")
				return curr, prefix, v.(*cachedZipArchive), true
			}
		}
		return "", "", nil, false
	}

	curr := cleanTarget
	var suffixParts []string
	for {
		if arch, err := loadZipArchive(curr); err == nil {
			prefix := ""
			if len(suffixParts) > 0 {
				for i, j := 0, len(suffixParts)-1; i < j; i, j = i+1, j-1 {
					suffixParts[i], suffixParts[j] = suffixParts[j], suffixParts[i]
				}
				prefix = strings.Join(suffixParts, "/")
			}
			return curr, prefix, arch, true
		}
		parent := filepath.Dir(curr)
		if parent == curr || parent == "." || parent == string(filepath.Separator) {
			break
		}
		suffixParts = append(suffixParts, filepath.Base(curr))
		curr = parent
	}
	return "", "", nil, false
}

// findModule locates a module or package inside a cached zip archive under innerPrefix.
// relModule is a slash-separated module path (e.g., "foo" or "foo/bar").
func (z *cachedZipArchive) findModule(innerPrefix, relModule string) (entryPath string, isPkg bool, found bool) {
	base := relModule
	if innerPrefix != "" {
		base = strings.TrimSuffix(innerPrefix, "/") + "/" + relModule
	}
	initPy := base + "/__init__.py"
	if _, ok := z.files[initPy]; ok {
		return initPy, true, true
	}
	modPy := base + ".py"
	if _, ok := z.files[modPy]; ok {
		return modPy, false, true
	}
	return "", false, false
}

func (z *cachedZipArchive) readEntry(entryPath string) ([]byte, bool) {
	norm := strings.TrimPrefix(filepath.ToSlash(entryPath), "/")
	data, ok := z.files[norm]
	return data, ok
}

func initZipImportTypes() {
	zipImportOnce.Do(func() {
		ZipImportErrorType = val.MustNewType("ZipImportError", []*val.PyType{val.ImportErrorType}, nil)
		ZipImportErrorType.Dict.Set(val.InternString("__module__"), val.InternString("zipimport"))
		ZipImportErrorType.InvalidateVersion()

		ZipImporterType = val.MustNewType("zipimporter", []*val.PyType{val.ObjectType}, nil)
		ZipImporterType.Dict.Set(val.InternString("__module__"), val.InternString("zipimport"))

		ZipImporterType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: zipimporter() missing required argument 'path'")
			}
			self := args[0]
			p := pathArg(ctx, args[1])
			if p == "" {
				panic(val.NewException(ZipImportErrorType, "archive path is empty"))
			}
			archPath, prefix, arch, ok := splitZipArchivePath(p)
			if !ok {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("not a Zip file: %q", p)))
			}
			if prefix != "" && !strings.HasSuffix(prefix, "/") {
				prefix += "/"
			}
			inst := self.AsInstance()
			inst.SetAttr(val.InternString("archive"), val.NewStringVal(archPath))
			inst.SetAttr(val.InternString("prefix"), val.NewStringVal(prefix))

			// Populate zipimport._zip_directory_cache[archPath] if zipimport is loaded
			if vmInstance, ok := ctx.VM.(*VM); ok && vmInstance != nil {
				populateZipDirectoryCache(vmInstance, archPath, arch)
			}
			return val.NoneVal()
		}))

		ZipImporterType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			arch, _ := self.GetAttr(val.InternString("archive"))
			prefix, _ := self.GetAttr(val.InternString("prefix"))
			if prefix.IsAnyString() && prefix.AsString() != "" {
				return val.NewStringVal(fmt.Sprintf("<zipimporter object %q>", arch.AsString()+"/"+prefix.AsString()))
			}
			return val.NewStringVal(fmt.Sprintf("<zipimporter object %q>", arch.AsString()))
		}))

		ZipImporterType.Dict.Set(val.InternString("find_spec"), val.NewBuiltinFuncVal("find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				return val.NoneVal()
			}
			self := args[0]
			fullname := args[1].AsString()
			archPath, prefix, arch := getZipImporterState(self)
			if arch == nil {
				return val.NoneVal()
			}
			leaf := fullname
			if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
				leaf = fullname[idx+1:]
			}
			entryPath, isPkg, found := arch.findModule(strings.TrimSuffix(prefix, "/"), leaf)
			if found {
				origin := archPath + "/" + entryPath
				var subSearch []string
				if isPkg {
					pkgDir := strings.TrimSuffix(prefix, "/")
					if pkgDir != "" {
						pkgDir = pkgDir + "/" + leaf
					} else {
						pkgDir = leaf
					}
					subSearch = []string{archPath + "/" + pkgDir}
				}
				return newModuleSpecVal(fullname, self, origin, isPkg, subSearch)
			}
			// Check namespace package directory inside zip
			nsDir := leaf
			if p := strings.TrimSuffix(prefix, "/"); p != "" {
				nsDir = p + "/" + leaf
			}
			if arch.dirs[nsDir] {
				return newNamespaceModuleSpecVal(fullname, []string{archPath + "/" + nsDir})
			}
			return val.NoneVal()
		}))

		ZipImporterType.Dict.Set(val.InternString("is_package"), val.NewBuiltinFuncVal("is_package", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				panic("TypeError: is_package() requires module name")
			}
			fullname := args[1].AsString()
			_, prefix, arch := getZipImporterState(args[0])
			if arch == nil {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			leaf := fullname
			if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
				leaf = fullname[idx+1:]
			}
			_, isPkg, found := arch.findModule(strings.TrimSuffix(prefix, "/"), leaf)
			if !found {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			return val.BoolVal(isPkg)
		}))

		ZipImporterType.Dict.Set(val.InternString("get_filename"), val.NewBuiltinFuncVal("get_filename", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				panic("TypeError: get_filename() requires module name")
			}
			fullname := args[1].AsString()
			archPath, prefix, arch := getZipImporterState(args[0])
			if arch == nil {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			leaf := fullname
			if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
				leaf = fullname[idx+1:]
			}
			entryPath, _, found := arch.findModule(strings.TrimSuffix(prefix, "/"), leaf)
			if !found {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			return val.NewStringVal(archPath + "/" + entryPath)
		}))

		ZipImporterType.Dict.Set(val.InternString("get_source"), val.NewBuiltinFuncVal("get_source", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				panic("TypeError: get_source() requires module name")
			}
			fullname := args[1].AsString()
			_, prefix, arch := getZipImporterState(args[0])
			if arch == nil {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			leaf := fullname
			if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
				leaf = fullname[idx+1:]
			}
			entryPath, _, found := arch.findModule(strings.TrimSuffix(prefix, "/"), leaf)
			if !found {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			data, ok := arch.readEntry(entryPath)
			if !ok {
				return val.NoneVal()
			}
			return val.NewStringVal(string(data))
		}))

		ZipImporterType.Dict.Set(val.InternString("get_data"), val.NewBuiltinFuncVal("get_data", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: get_data() requires pathname")
			}
			reqPath := pathArg(ctx, args[1])
			archPath, _, arch := getZipImporterState(args[0])
			if arch == nil {
				panic(val.NewException(val.OSErrorType, fmt.Sprintf("can't find file %q", reqPath)))
			}
			rel := filepath.ToSlash(reqPath)
			slashArch := filepath.ToSlash(archPath)
			if strings.HasPrefix(rel, slashArch+"/") {
				rel = strings.TrimPrefix(rel, slashArch+"/")
			} else {
				rel = strings.TrimPrefix(rel, "/")
			}
			data, ok := arch.readEntry(rel)
			if !ok {
				panic(val.NewException(val.FileNotFoundErrorType, fmt.Sprintf("No such file in zip archive: %q", reqPath)))
			}
			return val.NewBytesVal(append([]byte(nil), data...))
		}))

		ZipImporterType.Dict.Set(val.InternString("get_code"), val.NewBuiltinFuncVal("get_code", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				panic("TypeError: get_code() requires module name")
			}
			fullname := args[1].AsString()
			archPath, prefix, arch := getZipImporterState(args[0])
			if arch == nil {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			leaf := fullname
			if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
				leaf = fullname[idx+1:]
			}
			entryPath, _, found := arch.findModule(strings.TrimSuffix(prefix, "/"), leaf)
			if !found {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", fullname)))
			}
			origin := archPath + "/" + entryPath
			vmInstance := ctx.VM.(*VM)
			co, err := vmInstance.engine.LoadOrCompileModule(origin, fullname)
			if err != nil {
				panic("SyntaxError: " + err.Error())
			}
			return NewCodeVal(co)
		}))

		ZipImporterType.Dict.Set(val.InternString("create_module"), val.NewBuiltinFuncVal("create_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))

		ZipImporterType.Dict.Set(val.InternString("exec_module"), val.NewBuiltinFuncVal("exec_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsModule() {
				panic("TypeError: exec_module() requires a module object")
			}
			self := args[0]
			mod := args[1].AsModule()
			archPath, prefix, arch := getZipImporterState(self)
			if arch == nil {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", mod.Name)))
			}
			leaf := mod.Name
			if idx := strings.LastIndexByte(mod.Name, '.'); idx >= 0 {
				leaf = mod.Name[idx+1:]
			}
			entryPath, isPkg, found := arch.findModule(strings.TrimSuffix(prefix, "/"), leaf)
			if !found {
				panic(val.NewException(ZipImportErrorType, fmt.Sprintf("can't find module %q", mod.Name)))
			}
			origin := archPath + "/" + entryPath
			mod.File = origin
			mod.Dict.Set(val.InternString("__file__"), val.NewStringVal(origin))
			mod.Dict.Set(val.InternString("__loader__"), self)
			if isPkg {
				pkgDir := strings.TrimSuffix(prefix, "/")
				if pkgDir != "" {
					pkgDir = pkgDir + "/" + leaf
				} else {
					pkgDir = leaf
				}
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(mod.Name))
				if _, hasPath := mod.Dict.Get(val.InternString("__path__")); !hasPath {
					mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal(archPath+"/"+pkgDir)))
				}
			}
			vmInstance := ctx.VM.(*VM)
			co, err := vmInstance.engine.LoadOrCompileModule(origin, mod.Name)
			if err != nil {
				panic("SyntaxError: " + err.Error())
			}
			vmInstance.RunCodeWithGlobals(co, mod.Dict)
			if vmHasException(vmInstance) {
				panic(vmInstance.unhandledException)
			}
			return val.NoneVal()
		}))

		ZipImporterType.Dict.Set(val.InternString("load_module"), val.NewBuiltinFuncVal("load_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				panic("TypeError: load_module() requires module name")
			}
			fullname := args[1].AsString()
			vmInstance := ctx.VM.(*VM)
			modVal, err := vmInstance.ImportModule(fullname, 0, true, nil)
			if err != nil {
				panic(val.NewException(ZipImportErrorType, err.Error()))
			}
			return modVal
		}))

		ZipImporterType.Dict.Set(val.InternString("get_resource_reader"), val.NewBuiltinFuncVal("get_resource_reader", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				return val.NoneVal()
			}
			self := args[0]
			fullname := args[1]
			vmInstance := ctx.VM.(*VM)
			readersMod, err := vmInstance.ImportModule("importlib.resources.readers", 0, true, nil)
			if err != nil || !readersMod.IsModule() {
				return val.NoneVal()
			}
			zipReaderCls, ok := readersMod.AsModule().Dict.Get(val.InternString("ZipReader"))
			if !ok {
				return val.NoneVal()
			}
			return ctx.Call(zipReaderCls, []val.Value{self, fullname}, nil)
		}))

		ZipImporterType.Dict.Set(val.InternString("invalidate_caches"), val.NewBuiltinFuncVal("invalidate_caches", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 && args[0].IsInstance() {
				if arch, ok := args[0].AsInstance().GetAttr(val.InternString("archive")); ok && arch.IsAnyString() {
					zipArchiveCacheMap.Delete(filepath.Clean(arch.AsString()))
				}
			}
			return val.NoneVal()
		}))

		ZipImporterType.InvalidateVersion()
	})
}

func getZipImporterState(self val.Value) (string, string, *cachedZipArchive) {
	if !self.IsInstance() {
		return "", "", nil
	}
	inst := self.AsInstance()
	archVal, ok1 := inst.GetAttr(val.InternString("archive"))
	prefVal, ok2 := inst.GetAttr(val.InternString("prefix"))
	if !ok1 || !archVal.IsAnyString() {
		return "", "", nil
	}
	archPath := archVal.AsString()
	prefix := ""
	if ok2 && prefVal.IsAnyString() {
		prefix = prefVal.AsString()
	}
	arch, err := loadZipArchive(archPath)
	if err != nil {
		return archPath, prefix, nil
	}
	return archPath, prefix, arch
}

func newZipImporterInstance(vm *VM, archivePath, prefix string) val.Value {
	initZipImportTypes()
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	instVal := val.NewInstanceOfType(ZipImporterType)
	inst := instVal.AsInstance()
	inst.SetAttr(val.InternString("archive"), val.NewStringVal(archivePath))
	inst.SetAttr(val.InternString("prefix"), val.NewStringVal(prefix))
	if arch, err := loadZipArchive(archivePath); err == nil && vm != nil {
		populateZipDirectoryCache(vm, archivePath, arch)
	}
	return instVal
}

func populateZipDirectoryCache(vm *VM, archivePath string, arch *cachedZipArchive) {
	if vm == nil || vm.modules == nil || arch == nil {
		return
	}
	zipModVal, ok := vm.modules.Get(val.InternString("zipimport"))
	if !ok || !zipModVal.IsModule() {
		return
	}
	cacheVal, ok := zipModVal.AsModule().Dict.Get(val.InternString("_zip_directory_cache"))
	if !ok || !cacheVal.IsDict() {
		return
	}
	cacheDict := cacheVal.AsDict()
	archKey := val.NewStringVal(archivePath)
	if _, exists := cacheDict.Get(archKey); exists {
		return
	}
	filesDict := val.NewDictVal(len(arch.files)).AsDict()
	for name, data := range arch.files {
		toc := val.NewTupleVal(
			val.NewStringVal(archivePath+"/"+name),
			val.Int64Val(0),
			val.Int64Val(int64(len(data))),
			val.Int64Val(int64(len(data))),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
		)
		filesDict.Set(val.NewStringVal(name), toc)
	}
	for dir := range arch.dirs {
		dirKey := dir + "/"
		toc := val.NewTupleVal(
			val.NewStringVal(archivePath+"/"+dirKey),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
			val.Int64Val(0),
		)
		filesDict.Set(val.NewStringVal(dirKey), toc)
	}
	cacheDict.Set(archKey, val.DictVal(filesDict))
}

func (e *Engine) createZipImportModule() val.Value {
	initZipImportTypes()
	modVal := val.NewModuleVal("zipimport", "Import Python modules from Zip archives")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("ZipImportError"), val.TypeVal(ZipImportErrorType))
	mod.Dict.Set(val.InternString("zipimporter"), val.TypeVal(ZipImporterType))
	mod.Dict.Set(val.InternString("_zip_directory_cache"), val.NewDictVal(8))
	return modVal
}
