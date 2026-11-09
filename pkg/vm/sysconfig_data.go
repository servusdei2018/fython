package vm

import (
	"os"
	"runtime"

	"github.com/servusdei2018/fython/pkg/val"
)

// implementationName returns the sys.implementation name for packaging.tags
// compatibility: cpython by default, overridable for alternative branding.
func implementationName() string {
	if n := os.Getenv("FYTHON_IMPLEMENTATION_NAME"); n != "" {
		return n
	}
	return "cpython"
}

// cacheTag returns sys.implementation.cache_tag matching the version.
func cacheTag() string {
	return "cpython-315"
}

// hostMultiarch returns the GNU multiarch triple for the host (for sysconfig / SOABI).
func hostMultiarch() string {
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "x86_64-linux-gnu"
		case "arm64":
			return "aarch64-linux-gnu"
		case "arm":
			return "arm-linux-gnueabihf"
		case "386":
			return "i386-linux-gnu"
		default:
			return runtime.GOARCH + "-linux-gnu"
		}
	case "darwin":
		return "darwin"
	case "windows":
		return "win32"
	default:
		return runtime.GOOS + "-" + runtime.GOARCH
	}
}

// hostSOABI returns SOABI e.g. "cpython-315-x86_64-linux-gnu".
func hostSOABI() string {
	return "cpython-315-" + hostMultiarch()
}

// implementationModule builds sys.implementation namespace.
func implementationModule(verInfo val.Value) val.Value {
	ns := val.NewInstanceOfType(val.ObjectType)
	inst := ns.AsInstance()
	inst.SetAttr(val.InternString("name"), val.InternString(implementationName()))
	inst.SetAttr(val.InternString("version"), verInfo)
	inst.SetAttr(val.InternString("hexversion"), val.Int64Val(0x030f00f0))
	inst.SetAttr(val.InternString("cache_tag"), val.InternString(cacheTag()))
	inst.SetAttr(val.InternString("_multiarch"), val.InternString(hostMultiarch()))
	return ns
}

// createSysconfigModule builds the _sysconfig builtin module.
func (vm *VM) createSysconfigModule() val.Value {
	modVal := val.NewModuleVal("_sysconfig", "sysconfig support")
	mod := modVal.AsModule()
	prefix, base := vm.engine.sysPrefix()
	btv := val.DictVal(buildSysconfigData(prefix, base))
	mod.Dict.Set(val.InternString("config_vars"), val.NewBuiltinFuncVal("config_vars", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return btv
	}))
	mod.Dict.Set(val.InternString("get_platform"), val.NewBuiltinFuncVal("get_platform", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		switch runtime.GOOS {
		case "linux":
			if runtime.GOARCH == "amd64" {
				return val.NewStringVal("linux-x86_64")
			}
			if runtime.GOARCH == "arm64" {
				return val.NewStringVal("linux-aarch64")
			}
			return val.NewStringVal("linux-" + runtime.GOARCH)
		case "darwin":
			return val.NewStringVal("macosx-13.0-arm64")
		case "windows":
			if runtime.GOARCH == "amd64" {
				return val.NewStringVal("win-amd64")
			}
			return val.NewStringVal("win32")
		default:
			return val.NewStringVal(runtime.GOOS)
		}
	}))
	return modVal
}

// buildSysconfigData returns the full build_time_vars table for _sysconfigdata_*.
func buildSysconfigData(prefix, base string) *val.PyDict {
	multiarch := hostMultiarch()
	soabi := hostSOABI()
	extSuffix := extensionSuffixes()[0]
	shlibSuffix := ".so"
	exeSuffix := ""
	ldLibrary := "libpython3.15.so"
	switch runtime.GOOS {
	case "windows":
		shlibSuffix = ".dll"
		exeSuffix = ".exe"
		ldLibrary = "python315.dll"
	case "darwin":
		shlibSuffix = ".dylib"
		ldLibrary = "libpython3.15.dylib"
	}
	btv := val.NewDictVal(48).AsDict()
	set := func(k string, v val.Value) { btv.Set(val.NewStringVal(k), v) }
	set("prefix", val.NewStringVal(prefix))
	set("exec_prefix", val.NewStringVal(prefix))
	set("base", val.NewStringVal(base))
	set("exec_base", val.NewStringVal(base))
	set("installed_prefix", val.NewStringVal(base))
	set("installed_exec_prefix", val.NewStringVal(base))
	set("VERSION", val.NewStringVal("3.15"))
	set("VERSION_NODOT", val.NewStringVal("315"))
	set("Py_DEBUG", val.Int64Val(0))
	set("Py_GIL_DISABLED", val.Int64Val(0))
	set("SOABI", val.NewStringVal(soabi))
	set("MULTIARCH", val.NewStringVal(multiarch))
	set("HOST_GNU_TYPE", val.NewStringVal(multiarch))
	set("EXT_SUFFIX", val.NewStringVal(extSuffix))
	set("SHLIB_SUFFIX", val.NewStringVal(shlibSuffix))
	set("EXE", val.NewStringVal(exeSuffix))
	set("LDLIBRARY", val.NewStringVal(ldLibrary))
	set("LIBDIR", val.NewStringVal("lib"))
	set("INCLUDEPY", val.NewStringVal("/usr/include/python3.15"))
	set("EXTENSION_SUFFIXES", func() val.Value {
		sufs := extensionSuffixes()
		items := make([]val.Value, len(sufs))
		for i, s := range sufs {
			items[i] = val.NewStringVal(s)
		}
		return val.NewListVal(items...)
	}())
	set("EXT_SUFFIXES", func() val.Value {
		sufs := extensionSuffixes()
		items := make([]val.Value, len(sufs))
		for i, s := range sufs {
			items[i] = val.NewStringVal(s)
		}
		return val.NewListVal(items...)
	}())
	return btv
}
