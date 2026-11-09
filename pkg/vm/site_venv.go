package vm

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
)

// resolveExecutable returns the invoked interpreter path preserving symlinks.
// When os.Args[0] is a bare command name (PATH lookup), it is resolved via
// exec.LookPath and then absolutized without evaluating symlinks, so a
// `.venv/bin/python` symlink accessed via PATH keeps its venv identity.
func resolveExecutable() string {
	if len(os.Args) == 0 || os.Args[0] == "" {
		if exe, err := os.Executable(); err == nil && exe != "" {
			return filepath.Clean(exe)
		}
		return "fython"
	}
	arg0 := os.Args[0]
	if !strings.ContainsRune(arg0, filepath.Separator) && !strings.ContainsRune(arg0, '/') {
		if resolved, err := exec.LookPath(arg0); err == nil && resolved != "" {
			if abs, err := filepath.Abs(filepath.Clean(resolved)); err == nil {
				return abs
			}
			return filepath.Clean(resolved)
		}
	}
	if abs, err := filepath.Abs(filepath.Clean(arg0)); err == nil {
		return abs
	}
	return filepath.Clean(arg0)
}

// baseExecutableFor returns the resolved target for sys._base_executable.
func baseExecutableFor(invoked string) string {
	if resolved, err := filepath.EvalSymlinks(invoked); err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(exe)
	}
	return invoked
}

// RuntimeConfig holds the CPython-compatible command-line flags and process configuration.
type RuntimeConfig struct {
	Argv              []string
	OrigArgv          []string
	Executable        string
	Optimize          int
	DontWriteBytecode bool
	Unbuffered        bool
	IgnoreEnv         bool
	NoUserSite        bool
	NoSite            bool
	Isolated          bool
	Interactive       bool
	Quiet             bool
	WarnOptions       []string
	XOptions          map[string]string // "" value means boolean True
}

// EffectiveRuntimeConfig merges explicit RuntimeConfig settings with PYTHON* environment variables
// (unless IgnoreEnv or Isolated is set).
func (cfg RuntimeConfig) Effective() RuntimeConfig {
	out := cfg
	if out.Isolated {
		out.IgnoreEnv = true
		out.NoUserSite = true
	}
	if !out.IgnoreEnv {
		if !out.DontWriteBytecode && os.Getenv("PYTHONDONTWRITEBYTECODE") != "" {
			out.DontWriteBytecode = true
		}
		if !out.Unbuffered && os.Getenv("PYTHONUNBUFFERED") != "" {
			out.Unbuffered = true
		}
		if !out.NoUserSite && os.Getenv("PYTHONNOUSERSITE") != "" {
			out.NoUserSite = true
		}
		if !out.Interactive && os.Getenv("PYTHONINSPECT") != "" {
			out.Interactive = true
		}
		if out.Optimize == 0 {
			if optEnv := os.Getenv("PYTHONOPTIMIZE"); optEnv != "" {
				if n, err := strconv.Atoi(optEnv); err == nil && n > 0 {
					if n > 2 {
						n = 2
					}
					out.Optimize = n
				} else {
					out.Optimize = 1
				}
			}
		}
		if warnEnv := os.Getenv("PYTHONWARNINGS"); warnEnv != "" {
			for part := range strings.SplitSeq(warnEnv, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					out.WarnOptions = append(out.WarnOptions, trimmed)
				}
			}
		}
	}
	if out.Executable == "" {
		out.Executable = resolveExecutable()
	}
	return out
}

type venvDiscovery struct {
	prefix     string
	basePrefix string
	paths      []string
	pthImports []string
}

// discoverVenvAndPaths resolves sys.prefix, sys.base_prefix, FYTHONPATH/PYTHONPATH,
// virtualenv site-packages directories, and .pth files.
func discoverVenvAndPaths(cfg RuntimeConfig, baseSearchPaths []string) venvDiscovery {
	eff := cfg.Effective()
	basePrefix := "/usr/local"
	if exeDir := filepath.Dir(eff.Executable); exeDir != "" && exeDir != "." {
		basePrefix = filepath.Dir(exeDir)
	}
	prefix := basePrefix

	var paths []string
	seen := make(map[string]bool)
	addPath := func(p string) {
		if p == "" {
			return
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	// 1. Initial script/cwd search path unless Isolated (-I)
	if !eff.Isolated {
		if len(baseSearchPaths) > 0 {
			addPath(baseSearchPaths[0])
		} else {
			addPath(".")
		}
	}

	// 2. FYTHONPATH and PYTHONPATH (unless -E or -I)
	if !eff.IgnoreEnv && !eff.Isolated {
		for _, envName := range []string{"FYTHONPATH", "PYTHONPATH"} {
			if envVal := os.Getenv(envName); envVal != "" {
				for _, entry := range filepath.SplitList(envVal) {
					if entry != "" {
						addPath(entry)
					}
				}
			}
		}
	}

	// 3. Remaining explicit engine search paths
	if !eff.Isolated && len(baseSearchPaths) > 1 {
		for _, p := range baseSearchPaths[1:] {
			addPath(p)
		}
	}

	// 4. Detect virtualenv root
	venvRoot := detectVenvRoot(eff)
	if venvRoot != "" {
		prefix = venvRoot
		if cfgHome := parsePyvenvCfgHome(filepath.Join(venvRoot, "pyvenv.cfg")); cfgHome != "" {
			basePrefix = filepath.Dir(cfgHome)
		}
	}

	// 5. Discover site-packages and process .pth files unless -S (NoSite) is passed
	var pthImports []string
	if !eff.NoSite && venvRoot != "" {
		siteDirs := findSitePackagesDirs(venvRoot)
		for _, sdir := range siteDirs {
			addPath(sdir)
			extraPaths, imports := processPthDirectory(sdir)
			for _, ep := range extraPaths {
				addPath(ep)
			}
			pthImports = append(pthImports, imports...)
		}
	}

	return venvDiscovery{
		prefix:     prefix,
		basePrefix: basePrefix,
		paths:      paths,
		pthImports: pthImports,
	}
}

func detectVenvRoot(eff RuntimeConfig) string {
	// 1. Check VIRTUAL_ENV environment variable (unless -E or -I)
	if !eff.IgnoreEnv && !eff.Isolated {
		if venv := os.Getenv("VIRTUAL_ENV"); venv != "" {
			if info, err := os.Stat(venv); err == nil && info.IsDir() {
				return filepath.Clean(venv)
			}
		}
	}

	// 2. Check pyvenv.cfg adjacent to or one directory above os.Executable()
	if eff.Executable != "" {
		exeDir := filepath.Dir(eff.Executable)
		for _, cand := range []string{exeDir, filepath.Dir(exeDir)} {
			if cand == "" || cand == "." || cand == "/" {
				continue
			}
			if fileExists(filepath.Join(cand, "pyvenv.cfg")) {
				return filepath.Clean(cand)
			}
		}
	}

	// 3. Check pyvenv.cfg or .venv/pyvenv.cfg in current working directory
	if cwd, err := os.Getwd(); err == nil {
		if fileExists(filepath.Join(cwd, "pyvenv.cfg")) {
			return filepath.Clean(cwd)
		}
		dotVenv := filepath.Join(cwd, ".venv")
		if fileExists(filepath.Join(dotVenv, "pyvenv.cfg")) {
			return filepath.Clean(dotVenv)
		}
	}

	return ""
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func parsePyvenvCfgHome(cfgPath string) string {
	f, err := os.Open(cfgPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(strings.ToLower(parts[0]))
		valStr := strings.TrimSpace(parts[1])
		if key == "home" {
			return valStr
		}
	}
	return ""
}

// findSitePackagesDirs locates <venv>/lib/python3.*/site-packages (and lib64 / Lib / direct site-packages).
func findSitePackagesDirs(venvRoot string) []string {
	var dirs []string
	seen := make(map[string]bool)
	addDir := func(d string) {
		clean := filepath.Clean(d)
		if seen[clean] {
			return
		}
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			seen[clean] = true
			dirs = append(dirs, clean)
		}
	}

	// Preferred Python versions in descending order
	preferredPy := []string{"python3.15", "python3.14", "python3.13", "python3.12", "python3.11", "python3"}
	for _, libDir := range []string{"lib", "lib64"} {
		for _, pyVer := range preferredPy {
			addDir(filepath.Join(venvRoot, libDir, pyVer, "site-packages"))
		}
		if matches, err := filepath.Glob(filepath.Join(venvRoot, libDir, "python*", "site-packages")); err == nil {
			sort.Sort(sort.Reverse(sort.StringSlice(matches)))
			for _, m := range matches {
				addDir(m)
			}
		}
	}
	addDir(filepath.Join(venvRoot, "Lib", "site-packages"))
	addDir(filepath.Join(venvRoot, "site-packages"))
	return dirs
}

// processPthDirectory reads all *.pth files in siteDir in sorted order,
// returning existing directory/archive paths to append and any "import ..." lines to execute.
func processPthDirectory(siteDir string) ([]string, []string) {
	entries, err := os.ReadDir(siteDir)
	if err != nil {
		return nil, nil
	}
	var pthFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".pth") {
			pthFiles = append(pthFiles, filepath.Join(siteDir, e.Name()))
		}
	}
	sort.Strings(pthFiles)

	var addedPaths []string
	var importLines []string
	for _, pthPath := range pthFiles {
		pPaths, pImports := parseSinglePthFile(siteDir, pthPath)
		addedPaths = append(addedPaths, pPaths...)
		importLines = append(importLines, pImports...)
	}
	return addedPaths, importLines
}

func parseSinglePthFile(siteDir, pthPath string) ([]string, []string) {
	f, err := os.Open(pthPath)
	if err != nil {
		return nil, nil
	}
	defer func() { _ = f.Close() }()

	var addedPaths []string
	var importLines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "import\t") {
			importLines = append(importLines, line)
			continue
		}
		resolved := line
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(siteDir, resolved)
		}
		resolved = filepath.Clean(resolved)
		if _, err := os.Stat(resolved); err == nil {
			addedPaths = append(addedPaths, resolved)
		}
	}
	return addedPaths, importLines
}

func (e *Engine) createSiteModule() val.Value {
	modVal := val.NewModuleVal("site", "Append module search paths for third-party packages to sys.path")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("addsitedir"), val.NewBuiltinFuncVal("addsitedir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: addsitedir() requires sitedir")
		}
		sitedir := pathArg(ctx, args[0])
		vmInstance := ctx.VM.(*VM)
		vmInstance.addSiteDir(sitedir)
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("getsitepackages"), val.NewBuiltinFuncVal("getsitepackages", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		prefix := "/usr/local"
		if sysVal, ok := vmInstance.modules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
			if p, ok := sysVal.AsModule().Dict.Get(val.InternString("prefix")); ok && p.IsAnyString() {
				prefix = p.AsString()
			}
		}
		dirs := findSitePackagesDirs(prefix)
		if len(dirs) == 0 {
			dirs = []string{filepath.Join(prefix, "lib", "python3.15", "site-packages")}
		}
		items := make([]val.Value, len(dirs))
		for i, d := range dirs {
			items[i] = val.NewStringVal(d)
		}
		return val.NewListVal(items...)
	}))

	mod.Dict.Set(val.InternString("main"), val.NewBuiltinFuncVal("main", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	return modVal
}

func (vm *VM) addSiteDir(siteDir string) {
	cleanDir := filepath.Clean(siteDir)
	extraPaths, imports := processPthDirectory(cleanDir)
	allNew := append([]string{cleanDir}, extraPaths...)

	if sysVal, ok := vm.modules.Get(val.InternString("sys")); ok && sysVal.IsModule() {
		if pathVal, ok := sysVal.AsModule().Dict.Get(val.InternString("path")); ok && pathVal.IsList() {
			list := pathVal.AsList()
			existing := make(map[string]bool, len(list.Items))
			for _, it := range list.Items {
				if it.IsAnyString() {
					existing[filepath.Clean(it.AsString())] = true
				}
			}
			for _, p := range allNew {
				if !existing[filepath.Clean(p)] {
					existing[filepath.Clean(p)] = true
					list.Items = append(list.Items, val.NewStringVal(p))
				}
			}
		}
	}

	for _, impLine := range imports {
		co, err := vm.engine.CompileDynamic([]byte(impLine+"\n"), "<pth>", 0)
		if err == nil && co != nil {
			vm.RunCode(co)
			vm.unhandledException = val.NoneVal()
		}
	}
}
