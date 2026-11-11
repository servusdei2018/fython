package vm

import (
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/stdlib"
	"github.com/servusdei2018/fython/pkg/val"
)

var (
	importlibOnce             sync.Once
	ModuleSpecType            *val.PyType
	BuiltinImporterType       *val.PyType
	FrozenImporterType        *val.PyType
	WindowsRegistryFinderType *val.PyType
	PathFinderType            *val.PyType
	FileFinderType            *val.PyType
	LoaderBasicsType          *val.PyType
	FileLoaderType            *val.PyType
	SourceLoaderType          *val.PyType
	SourceFileLoaderType      *val.PyType
	SourcelessFileLoaderType  *val.PyType
	ExtensionFileLoaderType   *val.PyType
	AppleFrameworkLoaderType  *val.PyType
	NamespaceLoaderType       *val.PyType
	NamespacePathType         *val.PyType
	populateSentinel          val.Value
)

func initImportlibTypes() {
	importlibOnce.Do(func() {
		initZipImportTypes()
		populateSentinel = val.NewInstanceOfType(val.ObjectType)

		// 1. ModuleSpec
		ModuleSpecType = val.MustNewType("ModuleSpec", []*val.PyType{val.ObjectType}, nil)
		ModuleSpecType.Dict.Set(val.InternString("__module__"), val.InternString("_frozen_importlib"))
		ModuleSpecType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			a := bindArgs("ModuleSpec", args, kwnames, "self", "name", "loader", "origin", "loader_state", "is_package")
			if a[0].IsUnbound() || a[1].IsUnbound() || a[2].IsUnbound() {
				panic("TypeError: ModuleSpec() requires name and loader")
			}
			self := a[0].AsInstance()
			nameVal := a[1]
			loaderVal := a[2]
			originVal := val.NoneVal()
			if !a[3].IsUnbound() {
				originVal = a[3]
			}
			loaderStateVal := val.NoneVal()
			if !a[4].IsUnbound() {
				loaderStateVal = a[4]
			}
			isPkg := false
			if !a[5].IsUnbound() && !a[5].IsNone() {
				isPkg = a[5].IsTruthy()
			} else if !loaderVal.IsNone() {
				if isPkgFn, ok := vmGetAttrSafe(ctx, loaderVal, "is_package"); ok && !isPkgFn.IsNone() {
					func() {
						defer func() { _ = recover() }()
						res := ctx.Invoke1(isPkgFn, nameVal)
						isPkg = res.IsTruthy()
					}()
				}
			}

			self.SetAttr(val.InternString("name"), nameVal)
			self.SetAttr(val.InternString("loader"), loaderVal)
			self.SetAttr(val.InternString("origin"), originVal)
			self.SetAttr(val.InternString("loader_state"), loaderStateVal)
			if isPkg {
				self.SetAttr(val.InternString("submodule_search_locations"), val.NewListVal())
			} else {
				self.SetAttr(val.InternString("submodule_search_locations"), val.NoneVal())
			}
			self.SetAttr(val.InternString("_set_fileattr"), val.BoolVal(false))
			self.SetAttr(val.InternString("_cached"), val.NoneVal())
			return val.NoneVal()
		}))

		parentGetter := val.NewBuiltinFuncVal("parent", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			nameVal, _ := self.GetAttr(val.InternString("name"))
			subLocs, _ := self.GetAttr(val.InternString("submodule_search_locations"))
			nameStr := ""
			if nameVal.IsAnyString() {
				nameStr = nameVal.AsString()
			}
			if subLocs.IsNone() || subLocs.HeaderPtr() == nil {
				if idx := strings.LastIndexByte(nameStr, '.'); idx >= 0 {
					return val.NewStringVal(nameStr[:idx])
				}
				return val.NewStringVal("")
			}
			return val.NewStringVal(nameStr)
		})
		ModuleSpecType.Dict.Set(val.InternString("parent"), val.NewPropertyVal(parentGetter, val.NoneVal(), val.NoneVal()))

		hasLocGetter := val.NewBuiltinFuncVal("has_location", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			if v, ok := self.GetAttr(val.InternString("_set_fileattr")); ok {
				return val.BoolVal(v.IsTruthy())
			}
			return val.BoolVal(false)
		})
		hasLocSetter := val.NewBuiltinFuncVal("has_location", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			self.SetAttr(val.InternString("_set_fileattr"), val.BoolVal(args[1].IsTruthy()))
			return val.NoneVal()
		})
		ModuleSpecType.Dict.Set(val.InternString("has_location"), val.NewPropertyVal(hasLocGetter, hasLocSetter, val.NoneVal()))

		cachedGetter := val.NewBuiltinFuncVal("cached", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			if v, ok := self.GetAttr(val.InternString("_cached")); ok {
				return v
			}
			return val.NoneVal()
		})
		cachedSetter := val.NewBuiltinFuncVal("cached", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			self.SetAttr(val.InternString("_cached"), args[1])
			return val.NoneVal()
		})
		ModuleSpecType.Dict.Set(val.InternString("cached"), val.NewPropertyVal(cachedGetter, cachedSetter, val.NoneVal()))

		ModuleSpecType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0].AsInstance()
			nameVal, _ := self.GetAttr(val.InternString("name"))
			loaderVal, _ := self.GetAttr(val.InternString("origin"))
			return val.NewStringVal(fmt.Sprintf("ModuleSpec(name=%s, origin=%s)", val.PyRepr(ctx, nameVal), val.PyRepr(ctx, loaderVal)))
		}))
		ModuleSpecType.InvalidateVersion()

		// 2. _LoaderBasics, FileLoader, SourceLoader, SourceFileLoader, SourcelessFileLoader, ExtensionFileLoader, AppleFrameworkLoader
		LoaderBasicsType = val.MustNewType("_LoaderBasics", []*val.PyType{val.ObjectType}, nil)
		LoaderBasicsType.Dict.Set(val.InternString("is_package"), val.NewBuiltinFuncVal("is_package", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(false)
		}))
		LoaderBasicsType.Dict.Set(val.InternString("create_module"), val.NewBuiltinFuncVal("create_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		LoaderBasicsType.Dict.Set(val.InternString("exec_module"), val.NewBuiltinFuncVal("exec_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsModule() {
				panic("TypeError: exec_module() requires a module")
			}
			self := args[0]
			mod := args[1].AsModule()
			getCodeFn := val.GetAttrCtx(ctx, self, val.InternString("get_code"))
			codeVal := ctx.Invoke1(getCodeFn, val.NewStringVal(mod.Name))
			if codeVal.IsNone() {
				panic(val.NewImportError(fmt.Sprintf("cannot load module %q when get_code() returns None", mod.Name)))
			}
			vmInstance := ctx.VM.(*VM)
			vmInstance.RunCodeWithGlobals(AsCodeObject(codeVal), mod.Dict)
			if vmHasException(vmInstance) {
				panic(vmInstance.unhandledException)
			}
			return val.NoneVal()
		}))
		LoaderBasicsType.Dict.Set(val.InternString("load_module"), val.NewBuiltinFuncVal("load_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				panic("TypeError: load_module() requires fullname")
			}
			vmInstance := ctx.VM.(*VM)
			modVal, err := vmInstance.ImportModule(args[1].AsString(), 0, true, nil)
			if err != nil {
				panic(val.NewImportError(err.Error()))
			}
			return modVal
		}))
		LoaderBasicsType.InvalidateVersion()

		FileLoaderType = val.MustNewType("FileLoader", []*val.PyType{LoaderBasicsType}, nil)
		FileLoaderType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: FileLoader.__init__() requires fullname and path")
			}
			inst := args[0].AsInstance()
			inst.SetAttr(val.InternString("name"), args[1])
			inst.SetAttr(val.InternString("path"), args[2])
			return val.NoneVal()
		}))
		FileLoaderType.Dict.Set(val.InternString("get_filename"), val.NewBuiltinFuncVal("get_filename", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if p, ok := args[0].AsInstance().GetAttr(val.InternString("path")); ok {
				return p
			}
			return val.NewStringVal("")
		}))
		FileLoaderType.Dict.Set(val.InternString("get_data"), val.NewBuiltinFuncVal("get_data", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: get_data() requires path")
			}
			reqPath := pathArg(ctx, args[1])
			data, err := readLoaderFileBytes(reqPath)
			if err != nil {
				panic(val.NewException(val.FileNotFoundErrorType, err.Error()))
			}
			return val.NewBytesVal(data)
		}))
		FileLoaderType.Dict.Set(val.InternString("get_resource_reader"), val.NewBuiltinFuncVal("get_resource_reader", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0]
			vmInstance := ctx.VM.(*VM)
			readersMod, err := vmInstance.ImportModule("importlib.resources.readers", 0, true, nil)
			if err != nil || !readersMod.IsModule() {
				return val.NoneVal()
			}
			fileReaderCls, ok := readersMod.AsModule().Dict.Get(val.InternString("FileReader"))
			if !ok {
				return val.NoneVal()
			}
			return ctx.Invoke1(fileReaderCls, self)
		}))
		FileLoaderType.InvalidateVersion()

		SourceLoaderType = val.MustNewType("SourceLoader", []*val.PyType{LoaderBasicsType}, nil)
		SourceLoaderType.Dict.Set(val.InternString("source_to_code"), val.NewBuiltinFuncVal("source_to_code", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			start := 0
			if len(args) > 0 && args[0].IsInstance() && val.IsSubclass(args[0].Type(), SourceLoaderType) {
				start = 1
			}
			if len(args)-start < 1 {
				panic("TypeError: source_to_code() requires data")
			}
			dataVal := args[start]
			pathStr := "<string>"
			if len(args)-start >= 2 && args[start+1].IsAnyString() {
				pathStr = args[start+1].AsString()
			}
			src, ok := sourceBytes(dataVal)
			if !ok {
				panic("TypeError: source_to_code() data must be str or bytes")
			}
			return compileSource(ctx, src, pathStr, "exec")
		}))
		SourceLoaderType.InvalidateVersion()

		SourceFileLoaderType = val.MustNewType("SourceFileLoader", []*val.PyType{FileLoaderType, SourceLoaderType}, nil)
		SourceFileLoaderType.Dict.Set(val.InternString("path_stats"), val.NewBuiltinFuncVal("path_stats", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: path_stats() requires path")
			}
			reqPath := pathArg(ctx, args[1])
			d := val.NewDictVal(2).AsDict()
			if strings.HasPrefix(reqPath, "stdlib:") {
				data, err := readLoaderFileBytes(reqPath)
				if err != nil {
					panic(val.NewException(val.FileNotFoundErrorType, err.Error()))
				}
				d.Set(val.InternString("mtime"), val.Float64Val(0))
				d.Set(val.InternString("size"), val.Int64Val(int64(len(data))))
				return val.DictVal(d)
			}
			info, err := os.Stat(reqPath)
			if err != nil {
				panic(val.NewException(val.FileNotFoundErrorType, err.Error()))
			}
			d.Set(val.InternString("mtime"), val.Float64Val(float64(info.ModTime().UnixNano())/1e9))
			d.Set(val.InternString("size"), val.Int64Val(info.Size()))
			return val.DictVal(d)
		}))
		SourceFileLoaderType.Dict.Set(val.InternString("set_data"), val.NewBuiltinFuncVal("set_data", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		SourceFileLoaderType.Dict.Set(val.InternString("is_package"), val.NewBuiltinFuncVal("is_package", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p, ok := args[0].AsInstance().GetAttr(val.InternString("path"))
			if !ok || !p.IsAnyString() {
				return val.BoolVal(false)
			}
			base := filepath.Base(p.AsString())
			return val.BoolVal(base == "__init__.py" || base == "__init__.pyc" || base == "__init__.fyc")
		}))
		SourceFileLoaderType.Dict.Set(val.InternString("get_source"), val.NewBuiltinFuncVal("get_source", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p, ok := args[0].AsInstance().GetAttr(val.InternString("path"))
			if !ok || !p.IsAnyString() {
				return val.NoneVal()
			}
			data, err := readLoaderFileBytes(p.AsString())
			if err != nil {
				panic(val.NewImportError(err.Error()))
			}
			return val.NewStringVal(string(data))
		}))
		SourceFileLoaderType.Dict.Set(val.InternString("get_code"), val.NewBuiltinFuncVal("get_code", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			p, ok := inst.GetAttr(val.InternString("path"))
			if !ok || !p.IsAnyString() {
				return val.NoneVal()
			}
			modName := ""
			if len(args) > 1 && args[1].IsAnyString() {
				modName = args[1].AsString()
			} else if n, ok := inst.GetAttr(val.InternString("name")); ok && n.IsAnyString() {
				modName = n.AsString()
			}
			vmInstance := ctx.VM.(*VM)
			co, err := vmInstance.engine.LoadOrCompileModule(p.AsString(), modName)
			if err != nil {
				panic(val.NewImportError(err.Error()))
			}
			return NewCodeVal(co)
		}))
		SourceFileLoaderType.Dict.Set(val.InternString("exec_module"), val.NewBuiltinFuncVal("exec_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsModule() {
				panic("TypeError: exec_module() requires a module")
			}
			self := args[0]
			inst := self.AsInstance()
			mod := args[1].AsModule()
			pVal, ok := inst.GetAttr(val.InternString("path"))
			if !ok || !pVal.IsAnyString() {
				panic(val.NewImportError("loader has no path"))
			}
			filePath := pVal.AsString()
			mod.File = filePath
			mod.Dict.Set(val.InternString("__file__"), val.NewStringVal(filePath))
			mod.Dict.Set(val.InternString("__loader__"), self)
			base := filepath.Base(filePath)
			if base == "__init__.py" || base == "__init__.pyc" || base == "__init__.fyc" {
				pkgDir := filepath.Dir(filePath)
				if vfsDir, ok := strings.CutPrefix(filePath, "stdlib:"); ok {
					pkgDir = "stdlib:" + path.Dir(vfsDir)
				}
				mod.Dict.Set(val.InternString("__package__"), val.NewStringVal(mod.Name))
				if _, hasPath := mod.Dict.Get(val.InternString("__path__")); !hasPath {
					mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal(pkgDir)))
				}
			}
			vmInstance := ctx.VM.(*VM)
			co, err := vmInstance.engine.LoadOrCompileModule(filePath, mod.Name)
			if err != nil {
				panic(val.NewImportError(err.Error()))
			}
			vmInstance.RunCodeWithGlobals(co, mod.Dict)
			if vmHasException(vmInstance) {
				panic(vmInstance.unhandledException)
			}
			return val.NoneVal()
		}))
		SourceFileLoaderType.InvalidateVersion()

		SourcelessFileLoaderType = val.MustNewType("SourcelessFileLoader", []*val.PyType{FileLoaderType, LoaderBasicsType}, nil)
		SourcelessFileLoaderType.InvalidateVersion()

		ExtensionFileLoaderType = val.MustNewType("ExtensionFileLoader", []*val.PyType{FileLoaderType, LoaderBasicsType}, nil)
		ExtensionFileLoaderType.Dict.Set(val.InternString("create_module"), val.NewBuiltinFuncVal("create_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		ExtensionFileLoaderType.Dict.Set(val.InternString("exec_module"), val.NewBuiltinFuncVal("exec_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsModule() {
				panic("TypeError: exec_module() requires a module")
			}
			self := args[0]
			inst := self.AsInstance()
			mod := args[1].AsModule()
			if vmInstance, ok := ctx.VM.(*VM); ok && vmInstance != nil && vmInstance.sandboxBlocksNative() {
				panic("PermissionError: native extensions and ctypes are disabled by sandbox policy")
			}
			pVal, ok := inst.GetAttr(val.InternString("path"))
			if !ok || !pVal.IsAnyString() {
				panic(val.NewImportError("loader has no path"))
			}
			filePath := pVal.AsString()
			mod.File = filePath
			mod.Dict.Set(val.InternString("__file__"), val.NewStringVal(filePath))
			mod.Dict.Set(val.InternString("__loader__"), self)
			if err := execExtensionModule(ctx, args[1], filePath); err != nil {
				// Map to ImportError
				if ctx != nil && ctx.HasException() {
					panic(ctx.ActiveException)
				}
				panic(val.NewImportError(err.Error()))
			}
			return val.NoneVal()
		}))
		ExtensionFileLoaderType.InvalidateVersion()

		AppleFrameworkLoaderType = val.MustNewType("AppleFrameworkLoader", []*val.PyType{ExtensionFileLoaderType}, nil)
		AppleFrameworkLoaderType.InvalidateVersion()

		// 3. NamespacePath & NamespaceLoader
		NamespacePathType = val.MustNewType("NamespacePath", []*val.PyType{val.ListType}, nil)
		NamespacePathType.InvalidateVersion()

		NamespaceLoaderType = val.MustNewType("NamespaceLoader", []*val.PyType{val.ObjectType}, nil)
		NamespaceLoaderType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			switch {
			case len(args) > 2:
				inst.SetAttr(val.InternString("_path"), args[2])
			case len(args) > 1:
				inst.SetAttr(val.InternString("_path"), args[1])
			default:
				inst.SetAttr(val.InternString("_path"), val.NewListVal())
			}
			return val.NoneVal()
		}))
		NamespaceLoaderType.Dict.Set(val.InternString("is_package"), val.NewBuiltinFuncVal("is_package", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(true)
		}))
		NamespaceLoaderType.Dict.Set(val.InternString("get_source"), val.NewBuiltinFuncVal("get_source", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewStringVal("")
		}))
		NamespaceLoaderType.Dict.Set(val.InternString("get_code"), val.NewBuiltinFuncVal("get_code", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return compileSource(ctx, []byte(""), "<string>", "exec")
		}))
		NamespaceLoaderType.Dict.Set(val.InternString("create_module"), val.NewBuiltinFuncVal("create_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		NamespaceLoaderType.Dict.Set(val.InternString("exec_module"), val.NewBuiltinFuncVal("exec_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		NamespaceLoaderType.InvalidateVersion()

		// 4. BuiltinImporter, FrozenImporter, WindowsRegistryFinder
		BuiltinImporterType = val.MustNewType("BuiltinImporter", []*val.PyType{val.ObjectType}, nil)
		BuiltinImporterType.Dict.Set(val.InternString("find_spec"), val.NewModuleFuncVal("find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			fullname := extractFinderFullname(args)
			if fullname == "" {
				return val.NoneVal()
			}
			vmInstance := ctx.VM.(*VM)
			if isBuiltinModuleName(vmInstance, fullname) {
				return newBuiltinModuleSpecVal(fullname)
			}
			return val.NoneVal()
		}))
		BuiltinImporterType.Dict.Set(val.InternString("create_module"), val.NewModuleFuncVal("create_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		BuiltinImporterType.Dict.Set(val.InternString("exec_module"), val.NewModuleFuncVal("exec_module", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		BuiltinImporterType.Dict.Set(val.InternString("get_code"), val.NewModuleFuncVal("get_code", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		BuiltinImporterType.Dict.Set(val.InternString("get_source"), val.NewModuleFuncVal("get_source", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		BuiltinImporterType.Dict.Set(val.InternString("is_package"), val.NewModuleFuncVal("is_package", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(false)
		}))
		BuiltinImporterType.InvalidateVersion()

		FrozenImporterType = val.MustNewType("FrozenImporter", []*val.PyType{val.ObjectType}, nil)
		FrozenImporterType.Dict.Set(val.InternString("find_spec"), val.NewModuleFuncVal("find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		FrozenImporterType.Dict.Set(val.InternString("invalidate_caches"), val.NewModuleFuncVal("invalidate_caches", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		FrozenImporterType.InvalidateVersion()

		WindowsRegistryFinderType = val.MustNewType("WindowsRegistryFinder", []*val.PyType{val.ObjectType}, nil)
		WindowsRegistryFinderType.Dict.Set(val.InternString("find_spec"), val.NewModuleFuncVal("find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		WindowsRegistryFinderType.InvalidateVersion()

		// 5. FileFinder & PathFinder
		FileFinderType = val.MustNewType("FileFinder", []*val.PyType{val.ObjectType}, nil)
		FileFinderType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			p := "."
			if len(args) > 1 && args[1].IsAnyString() && args[1].AsString() != "" {
				p = args[1].AsString()
			}
			inst.SetAttr(val.InternString("path"), val.NewStringVal(p))
			return val.NoneVal()
		}))
		FileFinderType.Dict.Set(val.InternString("invalidate_caches"), val.NewBuiltinFuncVal("invalidate_caches", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		FileFinderType.Dict.Set(val.InternString("find_spec"), val.NewBuiltinFuncVal("find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsAnyString() {
				return val.NoneVal()
			}
			inst := args[0].AsInstance()
			pVal, ok := inst.GetAttr(val.InternString("path"))
			if !ok || !pVal.IsAnyString() {
				return val.NoneVal()
			}
			fullname := args[1].AsString()
			vmInstance := ctx.VM.(*VM)
			return findSpecInSingleEntry(vmInstance, fullname, pVal.AsString())
		}))
		FileFinderType.Dict.Set(val.InternString("path_hook"), val.NewModuleFuncVal("path_hook", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewModuleFuncVal("path_hook_for_FileFinder", func(ctx *val.ExecContext, callable val.Value, hArgs []val.Value, hKw []val.Value) val.Value {
				if len(hArgs) < 1 {
					panic(val.NewImportError("only directories are supported"))
				}
				p := pathArg(ctx, hArgs[0])
				if p == "" {
					p = "."
				}
				if strings.HasPrefix(p, "stdlib:") {
					inst := val.NewInstanceOfType(FileFinderType)
					inst.AsInstance().SetAttr(val.InternString("path"), val.NewStringVal(p))
					return inst
				}
				info, err := os.Stat(p)
				if err != nil || !info.IsDir() {
					panic(val.NewImportError("only directories are supported"))
				}
				inst := val.NewInstanceOfType(FileFinderType)
				inst.AsInstance().SetAttr(val.InternString("path"), val.NewStringVal(p))
				return inst
			})
		}))
		FileFinderType.InvalidateVersion()

		PathFinderType = val.MustNewType("PathFinder", []*val.PyType{val.ObjectType}, nil)
		PathFinderType.Dict.Set(val.InternString("invalidate_caches"), val.NewModuleFuncVal("invalidate_caches", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		PathFinderType.Dict.Set(val.InternString("find_spec"), val.NewModuleFuncVal("find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			fullname, pathArg := extractPathFinderArgs(args)
			if fullname == "" {
				return val.NoneVal()
			}
			vmInstance := ctx.VM.(*VM)
			return findSpecOnPath(vmInstance, fullname, pathArg)
		}))
		PathFinderType.Dict.Set(val.InternString("find_distributions"), val.NewModuleFuncVal("find_distributions", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			vmInstance := ctx.VM.(*VM)
			metaMod, err := vmInstance.ImportModule("importlib.metadata", 0, true, nil)
			if err != nil || !metaMod.IsModule() {
				return val.NewIterVal(val.NewTupleVal())
			}
			mpf, ok := metaMod.AsModule().Dict.Get(val.InternString("MetadataPathFinder"))
			if !ok {
				return val.NewIterVal(val.NewTupleVal())
			}
			findDist := val.GetAttrCtx(ctx, mpf, val.InternString("find_distributions"))
			callArgs := args
			if len(callArgs) > 0 && (callArgs[0] == val.TypeVal(PathFinderType) || (callArgs[0].IsInstance() && callArgs[0].Type() == PathFinderType)) {
				callArgs = callArgs[1:]
			}
			return ctx.Call(findDist, callArgs, kwnames)
		}))
		PathFinderType.InvalidateVersion()
	})
}

func readLoaderFileBytes(reqPath string) ([]byte, error) {
	if vfsPath, ok := strings.CutPrefix(reqPath, "stdlib:"); ok {
		return stdlib.ReadModule(vfsPath)
	}
	if archPath, innerPath, arch, ok := splitZipArchivePath(reqPath); ok && arch != nil && innerPath != "" {
		if data, found := arch.readEntry(innerPath); found {
			return append([]byte(nil), data...), nil
		}
		return nil, fmt.Errorf("file %q not found in archive %q", innerPath, archPath)
	}
	return os.ReadFile(reqPath)
}

func extractFinderFullname(args []val.Value) string {
	for _, a := range args {
		if a.IsAnyString() {
			return a.AsString()
		}
	}
	return ""
}

func extractPathFinderArgs(args []val.Value) (string, val.Value) {
	idx := 0
	if len(args) > 1 && !args[0].IsAnyString() && args[1].IsAnyString() {
		idx = 1
	} else if len(args) > 0 && (args[0].IsType() || (args[0].IsInstance() && args[0].Type() == PathFinderType)) {
		idx = 1
	}
	if len(args) <= idx || !args[idx].IsAnyString() {
		return "", val.NoneVal()
	}
	fullname := args[idx].AsString()
	pathArg := val.NoneVal()
	if len(args) > idx+1 {
		pathArg = args[idx+1]
	}
	return fullname, pathArg
}

func isBuiltinModuleName(vm *VM, name string) bool {
	if vm != nil && vm.modules != nil {
		if sysVal, ok := vm.modules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
			if bn, ok := sysVal.AsModule().Dict.Get(val.InternString("builtin_module_names")); ok && bn.IsTuple() {
				for _, item := range bn.AsTuple().Items {
					if item.IsAnyString() && item.AsString() == name {
						return true
					}
				}
			}
		}
	}
	return false
}

func newExtensionFileLoaderVal(name, filePath string) val.Value {
	initImportlibTypes()
	instVal := val.NewInstanceOfType(ExtensionFileLoaderType)
	inst := instVal.AsInstance()
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("path"), val.NewStringVal(filePath))
	return instVal
}

func newSourceFileLoaderVal(name, filePath string) val.Value {
	initImportlibTypes()
	instVal := val.NewInstanceOfType(SourceFileLoaderType)
	inst := instVal.AsInstance()
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("path"), val.NewStringVal(filePath))
	return instVal
}

func newModuleSpecVal(name string, loader val.Value, origin string, isPkg bool, searchLocations []string) val.Value {
	initImportlibTypes()
	specVal := val.NewInstanceOfType(ModuleSpecType)
	inst := specVal.AsInstance()
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("loader"), loader)
	if origin != "" {
		inst.SetAttr(val.InternString("origin"), val.NewStringVal(origin))
		inst.SetAttr(val.InternString("_set_fileattr"), val.BoolVal(true))
	} else {
		inst.SetAttr(val.InternString("origin"), val.NoneVal())
		inst.SetAttr(val.InternString("_set_fileattr"), val.BoolVal(false))
	}
	inst.SetAttr(val.InternString("loader_state"), val.NoneVal())
	inst.SetAttr(val.InternString("_cached"), val.NoneVal())
	if isPkg {
		items := make([]val.Value, len(searchLocations))
		for i, loc := range searchLocations {
			items[i] = val.NewStringVal(loc)
		}
		inst.SetAttr(val.InternString("submodule_search_locations"), val.NewListVal(items...))
	} else {
		inst.SetAttr(val.InternString("submodule_search_locations"), val.NoneVal())
	}
	return specVal
}

func newNamespaceModuleSpecVal(name string, searchLocations []string) val.Value {
	initImportlibTypes()
	items := make([]val.Value, len(searchLocations))
	for i, loc := range searchLocations {
		items[i] = val.NewStringVal(loc)
	}
	nsPath := val.NewListVal(items...)
	nsLoader := val.NewInstanceOfType(NamespaceLoaderType)
	nsLoader.AsInstance().SetAttr(val.InternString("_path"), nsPath)

	specVal := val.NewInstanceOfType(ModuleSpecType)
	inst := specVal.AsInstance()
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("loader"), nsLoader)
	inst.SetAttr(val.InternString("origin"), val.NoneVal())
	inst.SetAttr(val.InternString("loader_state"), val.NoneVal())
	inst.SetAttr(val.InternString("_set_fileattr"), val.BoolVal(false))
	inst.SetAttr(val.InternString("_cached"), val.NoneVal())
	inst.SetAttr(val.InternString("submodule_search_locations"), nsPath)
	return specVal
}

func newBuiltinModuleSpecVal(name string) val.Value {
	initImportlibTypes()
	specVal := val.NewInstanceOfType(ModuleSpecType)
	inst := specVal.AsInstance()
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("loader"), val.TypeVal(BuiltinImporterType))
	inst.SetAttr(val.InternString("origin"), val.InternString("built-in"))
	inst.SetAttr(val.InternString("loader_state"), val.NoneVal())
	inst.SetAttr(val.InternString("_set_fileattr"), val.BoolVal(false))
	inst.SetAttr(val.InternString("_cached"), val.NoneVal())
	inst.SetAttr(val.InternString("submodule_search_locations"), val.NoneVal())
	return specVal
}

func findSpecInSingleEntry(vm *VM, fullname, dir string) val.Value {
	leaf := fullname
	if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
		leaf = fullname[idx+1:]
	}
	if dir == "" {
		dir = "."
	}

	// 1. Check embedded stdlib VFS path
	if vfsDir, ok := strings.CutPrefix(dir, "stdlib:"); ok {
		initPath := path.Join(vfsDir, leaf, "__init__.py")
		if _, err := stdlib.StdlibFS.ReadFile(initPath); err == nil {
			origin := "stdlib:" + initPath
			pkgDir := "stdlib:" + path.Join(vfsDir, leaf)
			loader := newSourceFileLoaderVal(fullname, origin)
			return newModuleSpecVal(fullname, loader, origin, true, []string{pkgDir})
		}
		modPath := path.Join(vfsDir, leaf+".py")
		if _, err := stdlib.StdlibFS.ReadFile(modPath); err == nil {
			origin := "stdlib:" + modPath
			loader := newSourceFileLoaderVal(fullname, origin)
			return newModuleSpecVal(fullname, loader, origin, false, nil)
		}
		return val.NoneVal()
	}

	// 2. Check zip / wheel archive path
	if archPath, prefix, arch, ok := splitZipArchivePath(dir); ok && arch != nil {
		entryPath, isPkg, found := arch.findModule(prefix, leaf)
		if found {
			origin := archPath + "/" + entryPath
			if isExtensionFile(entryPath) {
				loader := newExtensionFileLoaderVal(fullname, origin)
				var subLocs []string
				if isPkg {
					pkgDir := leaf
					if prefix != "" {
						pkgDir = strings.TrimSuffix(prefix, "/") + "/" + leaf
					}
					subLocs = []string{archPath + "/" + pkgDir}
				}
				return newModuleSpecVal(fullname, loader, origin, isPkg, subLocs)
			}
			zLoader := newZipImporterInstance(vm, archPath, prefix)
			var subLocs []string
			if isPkg {
				pkgDir := leaf
				if prefix != "" {
					pkgDir = strings.TrimSuffix(prefix, "/") + "/" + leaf
				}
				subLocs = []string{archPath + "/" + pkgDir}
			}
			return newModuleSpecVal(fullname, zLoader, origin, isPkg, subLocs)
		}
		return val.NoneVal()
	}

	// 3. Check OS filesystem directory
	initPath := filepath.Join(dir, leaf, "__init__.py")
	if info, err := os.Stat(initPath); err == nil && !info.IsDir() {
		loader := newSourceFileLoaderVal(fullname, initPath)
		return newModuleSpecVal(fullname, loader, initPath, true, []string{filepath.Join(dir, leaf)})
	}
	modPath := filepath.Join(dir, leaf+".py")
	if info, err := os.Stat(modPath); err == nil && !info.IsDir() {
		loader := newSourceFileLoaderVal(fullname, modPath)
		return newModuleSpecVal(fullname, loader, modPath, false, nil)
	}
	// Extension modules (cpython-315-*.so / .abi3.so / .so / .pyd)
	for _, suf := range extensionSuffixes() {
		extPath := filepath.Join(dir, leaf+suf)
		if info, err := os.Stat(extPath); err == nil && !info.IsDir() {
			loader := newExtensionFileLoaderVal(fullname, extPath)
			return newModuleSpecVal(fullname, loader, extPath, false, nil)
		}
	}
	// Extension package: leaf/__init__<suffix>
	for _, suf := range extensionSuffixes() {
		extInit := filepath.Join(dir, leaf, "__init__"+suf)
		if info, err := os.Stat(extInit); err == nil && !info.IsDir() {
			loader := newExtensionFileLoaderVal(fullname, extInit)
			return newModuleSpecVal(fullname, loader, extInit, true, []string{filepath.Join(dir, leaf)})
		}
	}
	return val.NoneVal()
}

func findSpecOnPath(vm *VM, fullname string, pathArg val.Value) val.Value {
	var searchDirs []string
	isTopLevel := false
	switch {
	case pathArg.IsNone() || pathArg.IsUnbound() || pathArg.HeaderPtr() == nil:
		isTopLevel = true
		searchDirs = vm.liveSysPath()
	case pathArg.IsList():
		for _, item := range pathArg.AsList().Items {
			if item.IsAnyString() {
				searchDirs = append(searchDirs, item.AsString())
			}
		}
	case pathArg.IsTuple():
		for _, item := range pathArg.AsTuple().Items {
			if item.IsAnyString() {
				searchDirs = append(searchDirs, item.AsString())
			}
		}
	}

	leaf := fullname
	if idx := strings.LastIndexByte(fullname, '.'); idx >= 0 {
		leaf = fullname[idx+1:]
	}

	var namespacePortions []string
	for _, entry := range searchDirs {
		spec := findSpecInSingleEntry(vm, fullname, entry)
		if !spec.IsNone() {
			return spec
		}
		// Check namespace package candidate directory
		if !strings.HasPrefix(entry, "stdlib:") {
			if archPath, prefix, arch, ok := splitZipArchivePath(entry); ok && arch != nil {
				nsDir := leaf
				if prefix != "" {
					nsDir = strings.TrimSuffix(prefix, "/") + "/" + leaf
				}
				if arch.dirs[nsDir] {
					namespacePortions = append(namespacePortions, archPath+"/"+nsDir)
				}
			} else {
				candDir := filepath.Join(entry, leaf)
				if info, err := os.Stat(candDir); err == nil && info.IsDir() {
					namespacePortions = append(namespacePortions, candDir)
				}
			}
		}
	}

	if isTopLevel {
		// Check virtual modules and embedded stdlib
		if _, ok := vm.engine.virtualModules.Load(fullname); ok {
			loader := newSourceFileLoaderVal(fullname, fullname+".py")
			return newModuleSpecVal(fullname, loader, fullname+".py", false, nil)
		}
		if vfsPath, isPkg, ok := stdlib.FindModule(fullname); ok {
			origin := "stdlib:" + vfsPath
			loader := newSourceFileLoaderVal(fullname, origin)
			var subLocs []string
			if isPkg {
				subLocs = []string{"stdlib:" + path.Dir(vfsPath)}
			}
			return newModuleSpecVal(fullname, loader, origin, isPkg, subLocs)
		}
	}

	if len(namespacePortions) > 0 {
		return newNamespaceModuleSpecVal(fullname, namespacePortions)
	}
	return val.NoneVal()
}

func (e *Engine) createImpModule() val.Value {
	modVal := val.NewModuleVal("_imp", "Built-in import internals")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("pyc_magic_number_token"), val.Int64Val(3531))
	mod.Dict.Set(val.InternString("check_hash_based_pycs"), val.InternString("default"))
	mod.Dict.Set(val.InternString("source_hash"), val.NewBuiltinFuncVal("source_hash", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: source_hash() takes 2 positional arguments")
		}
		src, ok := sourceBytes(args[1])
		if !ok {
			panic("TypeError: source_hash() arg 2 must be bytes-like")
		}
		h := ComputeSourceHash(src)
		return val.NewBytesVal(h[:8])
	}))
	mod.Dict.Set(val.InternString("extension_suffixes"), val.NewBuiltinFuncVal("extension_suffixes", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		suf := extensionSuffixes()
		items := make([]val.Value, len(suf))
		for i, s := range suf {
			items[i] = val.NewStringVal(s)
		}
		return val.NewListVal(items...)
	}))
	mod.Dict.Set(val.InternString("is_builtin"), val.NewBuiltinFuncVal("is_builtin", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsAnyString() {
			return val.Int64Val(0)
		}
		vmInstance := ctx.VM.(*VM)
		if isBuiltinModuleName(vmInstance, args[0].AsString()) {
			return val.Int64Val(1)
		}
		return val.Int64Val(0)
	}))
	mod.Dict.Set(val.InternString("is_frozen"), val.NewBuiltinFuncVal("is_frozen", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	mod.Dict.Set(val.InternString("is_frozen_package"), val.NewBuiltinFuncVal("is_frozen_package", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	mod.Dict.Set(val.InternString("create_builtin"), val.NewBuiltinFuncVal("create_builtin", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsInstance() {
			return val.NoneVal()
		}
		nameVal, _ := args[0].AsInstance().GetAttr(val.InternString("name"))
		vmInstance := ctx.VM.(*VM)
		if m, ok := vmInstance.modules.Get(val.InternValue(nameVal)); ok {
			return m
		}
		// Native modules are created on first import: materialize it here so
		// that the caller gets the real module instead of an empty shell.
		if m, ok := vmInstance.materializeBuiltinModule(nameVal.AsString()); ok {
			return m
		}
		return val.NewModuleVal(nameVal.AsString(), "")
	}))
	mod.Dict.Set(val.InternString("exec_builtin"), val.NewBuiltinFuncVal("exec_builtin", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(0)
	}))
	mod.Dict.Set(val.InternString("create_dynamic"), val.NewBuiltinFuncVal("create_dynamic", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("exec_dynamic"), val.NewBuiltinFuncVal("exec_dynamic", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || !args[1].IsModule() {
			return val.Int64Val(-1)
		}
		// args[0] is spec (instance with path?), args[1] is module. Try to resolve file path from spec.
		// CPython signature: exec_dynamic(spec, module). spec.origin holds .so path.
		pathStr := ""
		if len(args) >= 1 && args[0].IsInstance() {
			if v, ok := args[0].AsInstance().GetAttr(val.InternString("origin")); ok && v.IsAnyString() {
				pathStr = v.AsString()
			}
			if pathStr == "" {
				panic("ImportError: exec_dynamic missing origin")
			}
		} else {
			panic("TypeError: exec_dynamic requires spec and module")
		}
		if err := execExtensionModule(ctx, args[1], pathStr); err != nil {
			panic(val.NewImportError(err.Error()))
		}
		return val.Int64Val(0)
	}))
	mod.Dict.Set(val.InternString("acquire_lock"), val.NewBuiltinFuncVal("acquire_lock", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("release_lock"), val.NewBuiltinFuncVal("release_lock", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("lock_held"), val.NewBuiltinFuncVal("lock_held", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	mod.Dict.Set(val.InternString("_fix_co_filename"), val.NewBuiltinFuncVal("_fix_co_filename", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	return modVal
}

func (e *Engine) createFrozenImportlibModule() val.Value {
	initImportlibTypes()
	modVal := val.NewModuleVal("_frozen_importlib", "Core import machinery")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("ModuleSpec"), val.TypeVal(ModuleSpecType))
	mod.Dict.Set(val.InternString("BuiltinImporter"), val.TypeVal(BuiltinImporterType))
	mod.Dict.Set(val.InternString("FrozenImporter"), val.TypeVal(FrozenImporterType))

	mod.Dict.Set(val.InternString("spec_from_loader"), val.NewBuiltinFuncVal("spec_from_loader", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := bindArgs("spec_from_loader", args, kwnames, "name", "loader", "origin", "is_package")
		if a[0].IsUnbound() || a[1].IsUnbound() {
			panic("TypeError: spec_from_loader() requires name and loader")
		}
		nameStr := a[0].AsString()
		loaderVal := a[1]
		originStr := ""
		if !a[2].IsUnbound() && a[2].IsAnyString() {
			originStr = a[2].AsString()
		} else if !loaderVal.IsNone() {
			if getFn, ok := vmGetAttrSafe(ctx, loaderVal, "get_filename"); ok && !getFn.IsNone() {
				func() {
					defer func() { _ = recover() }()
					r := ctx.Invoke1(getFn, a[0])
					if r.IsAnyString() {
						originStr = r.AsString()
					}
				}()
			}
		}
		isPkg := false
		if !a[3].IsUnbound() && !a[3].IsNone() {
			isPkg = a[3].IsTruthy()
		} else if !loaderVal.IsNone() {
			if isPkgFn, ok := vmGetAttrSafe(ctx, loaderVal, "is_package"); ok && !isPkgFn.IsNone() {
				func() {
					defer func() { _ = recover() }()
					r := ctx.Invoke1(isPkgFn, a[0])
					isPkg = r.IsTruthy()
				}()
			}
		}
		var subLocs []string
		if isPkg && originStr != "" {
			subLocs = []string{filepath.Dir(originStr)}
		}
		return newModuleSpecVal(nameStr, loaderVal, originStr, isPkg, subLocs)
	}))

	mod.Dict.Set(val.InternString("module_from_spec"), val.NewBuiltinFuncVal("module_from_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsInstance() {
			panic("TypeError: module_from_spec() requires a ModuleSpec")
		}
		spec := args[0]
		inst := spec.AsInstance()
		nameVal, _ := inst.GetAttr(val.InternString("name"))
		loaderVal, _ := inst.GetAttr(val.InternString("loader"))
		originVal, _ := inst.GetAttr(val.InternString("origin"))
		subLocs, _ := inst.GetAttr(val.InternString("submodule_search_locations"))

		if !loaderVal.IsNone() {
			if createFn, ok := vmGetAttrSafe(ctx, loaderVal, "create_module"); ok && !createFn.IsNone() {
				m := ctx.Invoke1(createFn, spec)
				if !m.IsNone() {
					initModuleAttrsFromSpec(ctx, m, spec, nameVal, loaderVal, originVal, subLocs)
					return m
				}
			}
		}
		originStr := ""
		if originVal.IsAnyString() {
			originStr = originVal.AsString()
		}
		m := val.NewModuleVal(nameVal.AsString(), originStr)
		initModuleAttrsFromSpec(ctx, m, spec, nameVal, loaderVal, originVal, subLocs)
		return m
	}))

	mod.Dict.Set(val.InternString("_resolve_name"), val.NewBuiltinFuncVal("_resolve_name", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: _resolve_name() takes 3 arguments")
		}
		name := args[0].AsString()
		pkg := args[1].AsString()
		level := int(args[2].AsInt64())
		parts := strings.Split(pkg, ".")
		steps := level - 1
		if steps > len(parts) || steps < 0 {
			panic(val.NewImportError("attempted relative import beyond top-level package"))
		}
		base := strings.Join(parts[:len(parts)-steps], ".")
		if name != "" {
			return val.NewStringVal(base + "." + name)
		}
		return val.NewStringVal(base)
	}))

	mod.Dict.Set(val.InternString("_find_spec"), val.NewBuiltinFuncVal("_find_spec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsAnyString() {
			return val.NoneVal()
		}
		name := args[0].AsString()
		pathArg := val.NoneVal()
		if len(args) > 1 {
			pathArg = args[1]
		}
		targetArg := val.NoneVal()
		if len(args) > 2 {
			targetArg = args[2]
		}
		vmInstance := ctx.VM.(*VM)
		if sysVal, ok := vmInstance.modules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
			if mpVal, ok := sysVal.AsModule().Dict.Get(val.InternString("meta_path")); ok && mpVal.IsList() {
				for _, finder := range mpVal.AsList().Items {
					if findSpecFn, ok := vmGetAttrSafe(ctx, finder, "find_spec"); ok && !findSpecFn.IsNone() {
						spec := ctx.Call(findSpecFn, []val.Value{val.NewStringVal(name), pathArg, targetArg}, nil)
						if !spec.IsNone() {
							return spec
						}
					}
				}
			}
		}
		if pathArg.IsNone() {
			if isBuiltinModuleName(vmInstance, name) {
				return newBuiltinModuleSpecVal(name)
			}
		}
		return findSpecOnPath(vmInstance, name, pathArg)
	}))

	mod.Dict.Set(val.InternString("_exec"), val.NewBuiltinFuncVal("_exec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: _exec() requires spec and module")
		}
		spec := args[0]
		module := args[1]
		loader := val.GetAttrCtx(ctx, spec, val.InternString("loader"))
		if !loader.IsNone() {
			if execFn, ok := vmGetAttrSafe(ctx, loader, "exec_module"); ok && !execFn.IsNone() {
				ctx.Invoke1(execFn, module)
			}
		}
		return module
	}))

	mod.Dict.Set(val.InternString("_gcd_import"), val.NewBuiltinFuncVal("_gcd_import", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsAnyString() {
			panic("TypeError: _gcd_import() requires name")
		}
		name := args[0].AsString()
		pkg := ""
		if len(args) > 1 && args[1].IsAnyString() {
			pkg = args[1].AsString()
		}
		level := 0
		if len(args) > 2 && args[2].IsInt64() {
			level = int(args[2].AsInt64())
		}
		var callerGlobals *val.PyDict
		if level > 0 && pkg != "" {
			callerGlobals = val.NewDictVal(2).AsDict()
			callerGlobals.Set(val.InternString("__package__"), val.NewStringVal(pkg))
		}
		vmInstance := ctx.VM.(*VM)
		res, err := vmInstance.ImportModule(name, level, true, callerGlobals)
		if err != nil {
			panic(panicToPyException(err))
		}
		if vmHasException(vmInstance) {
			exc := vmInstance.unhandledException
			vmInstance.unhandledException = val.NoneVal()
			panic(exc)
		}
		return res
	}))

	if impFn, ok := e.builtins.Get(val.InternString("__import__")); ok {
		mod.Dict.Set(val.InternString("__import__"), impFn)
	}

	return modVal
}

func initModuleAttrsFromSpec(ctx *val.ExecContext, modVal, spec, nameVal, loaderVal, originVal, subLocs val.Value) {
	val.SetAttrCtx(ctx, modVal, val.InternString("__name__"), nameVal)
	val.SetAttrCtx(ctx, modVal, val.InternString("__loader__"), loaderVal)
	val.SetAttrCtx(ctx, modVal, val.InternString("__spec__"), spec)
	parentVal := val.GetAttrCtx(ctx, spec, val.InternString("parent"))
	val.SetAttrCtx(ctx, modVal, val.InternString("__package__"), parentVal)
	if !originVal.IsNone() {
		val.SetAttrCtx(ctx, modVal, val.InternString("__file__"), originVal)
	}
	if !subLocs.IsNone() {
		val.SetAttrCtx(ctx, modVal, val.InternString("__path__"), subLocs)
	}
}

func (e *Engine) createFrozenImportlibExternalModule() val.Value {
	initImportlibTypes()
	modVal := val.NewModuleVal("_frozen_importlib_external", "External import machinery")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("SOURCE_SUFFIXES"), val.NewListVal(val.NewStringVal(".py")))
	mod.Dict.Set(val.InternString("BYTECODE_SUFFIXES"), val.NewListVal(val.NewStringVal(".pyc"), val.NewStringVal(".fyc")))
	mod.Dict.Set(val.InternString("DEBUG_BYTECODE_SUFFIXES"), val.NewListVal(val.NewStringVal(".pyc"), val.NewStringVal(".fyc")))
	mod.Dict.Set(val.InternString("OPTIMIZED_BYTECODE_SUFFIXES"), val.NewListVal(val.NewStringVal(".pyc"), val.NewStringVal(".fyc")))
	mod.Dict.Set(val.InternString("EXTENSION_SUFFIXES"), func() val.Value {
		suf := extensionSuffixes()
		items := make([]val.Value, len(suf))
		for i, s := range suf {
			items[i] = val.NewStringVal(s)
		}
		return val.NewListVal(items...)
	}())
	mod.Dict.Set(val.InternString("MAGIC_NUMBER"), val.NewBytesVal([]byte{0xcb, 0x0d, 0x0d, 0x0a}))
	mod.Dict.Set(val.InternString("_POPULATE"), populateSentinel)

	mod.Dict.Set(val.InternString("WindowsRegistryFinder"), val.TypeVal(WindowsRegistryFinderType))
	mod.Dict.Set(val.InternString("PathFinder"), val.TypeVal(PathFinderType))
	mod.Dict.Set(val.InternString("FileFinder"), val.TypeVal(FileFinderType))
	mod.Dict.Set(val.InternString("_LoaderBasics"), val.TypeVal(LoaderBasicsType))
	mod.Dict.Set(val.InternString("FileLoader"), val.TypeVal(FileLoaderType))
	mod.Dict.Set(val.InternString("SourceLoader"), val.TypeVal(SourceLoaderType))
	mod.Dict.Set(val.InternString("SourceFileLoader"), val.TypeVal(SourceFileLoaderType))
	mod.Dict.Set(val.InternString("SourcelessFileLoader"), val.TypeVal(SourcelessFileLoaderType))
	mod.Dict.Set(val.InternString("ExtensionFileLoader"), val.TypeVal(ExtensionFileLoaderType))
	mod.Dict.Set(val.InternString("AppleFrameworkLoader"), val.TypeVal(AppleFrameworkLoaderType))
	mod.Dict.Set(val.InternString("NamespaceLoader"), val.TypeVal(NamespaceLoaderType))
	mod.Dict.Set(val.InternString("NamespacePath"), val.TypeVal(NamespacePathType))

	mod.Dict.Set(val.InternString("_pack_uint32"), val.NewBuiltinFuncVal("_pack_uint32", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: _pack_uint32() requires 1 argument")
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(intArg(ctx, args[0], 0)))
		return val.NewBytesVal(b[:])
	}))
	mod.Dict.Set(val.InternString("_unpack_uint32"), val.NewBuiltinFuncVal("_unpack_uint32", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: _unpack_uint32() requires 1 argument")
		}
		data, ok := sourceBytes(args[0])
		if !ok || len(data) < 4 {
			panic("ValueError: _unpack_uint32() requires 4 bytes")
		}
		return val.Int64Val(int64(binary.LittleEndian.Uint32(data[:4])))
	}))

	mod.Dict.Set(val.InternString("cache_from_source"), val.NewBuiltinFuncVal("cache_from_source", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: cache_from_source() requires path")
		}
		p := pathArg(ctx, args[0])
		dir := filepath.Dir(p)
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		return val.NewStringVal(filepath.Join(dir, "__pycache__", base+".fyc"))
	}))

	mod.Dict.Set(val.InternString("source_from_cache"), val.NewBuiltinFuncVal("source_from_cache", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: source_from_cache() requires path")
		}
		p := pathArg(ctx, args[0])
		dir := filepath.Dir(filepath.Dir(p))
		base := filepath.Base(p)
		if idx := strings.IndexByte(base, '.'); idx > 0 {
			base = base[:idx]
		}
		return val.NewStringVal(filepath.Join(dir, base+".py"))
	}))

	mod.Dict.Set(val.InternString("decode_source"), val.NewBuiltinFuncVal("decode_source", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: decode_source() requires source_bytes")
		}
		b, ok := sourceBytes(args[0])
		if !ok {
			panic("TypeError: decode_source() requires bytes-like object")
		}
		return val.NewStringVal(string(b))
	}))

	mod.Dict.Set(val.InternString("spec_from_file_location"), val.NewBuiltinFuncVal("spec_from_file_location", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		a := bindArgs("spec_from_file_location", args, kwnames, "name", "location", "loader", "submodule_search_locations")
		if a[0].IsUnbound() {
			panic("TypeError: spec_from_file_location() missing required argument 'name'")
		}
		nameStr := a[0].AsString()
		locStr := "<unknown>"
		if !a[1].IsUnbound() && !a[1].IsNone() {
			locStr = pathArg(ctx, a[1])
		}
		loaderVal := a[2]
		if loaderVal.IsUnbound() || loaderVal.IsNone() {
			loaderVal = newSourceFileLoaderVal(nameStr, locStr)
		}
		isPkg := false
		var subLocs []string
		if !a[3].IsUnbound() && a[3] != populateSentinel {
			if !a[3].IsNone() {
				isPkg = true
				if a[3].IsList() {
					for _, it := range a[3].AsList().Items {
						if it.IsAnyString() {
							subLocs = append(subLocs, it.AsString())
						}
					}
				}
			}
		} else {
			base := filepath.Base(locStr)
			if base == "__init__.py" || base == "__init__.pyc" || base == "__init__.fyc" {
				isPkg = true
				subLocs = []string{filepath.Dir(locStr)}
			}
		}
		return newModuleSpecVal(nameStr, loaderVal, locStr, isPkg, subLocs)
	}))

	return modVal
}
