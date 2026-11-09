package vm_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func buildExtension(t *testing.T, name, src string, suffix string, limited bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, name+".c")
	if err := os.WriteFile(srcPath, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	// Build shim
	root, _ := filepath.Abs("../..")
	shimSrc := filepath.Join(root, "shim", "fython_abi3_shim.c")
	shimOut := filepath.Join(dir, "libfython_abi3.so")
	if runtime.GOOS == "darwin" {
		shimOut = filepath.Join(dir, "libfython_abi3.dylib")
	}
	// Windows shim name for loader search: python315.dll etc. For test on linux, use .so
	cmdShim := exec.Command("gcc", "-fPIC", "-shared", "-o", shimOut, shimSrc)
	if out, err := cmdShim.CombinedOutput(); err != nil {
		t.Fatalf("build shim failed: %v: %s", err, out)
	}
	// Build extension
	extOut := filepath.Join(dir, name+suffix)
	args := []string{"-fPIC", "-shared"}
	if limited {
		args = append(args, "-DPy_LIMITED_API=0x030A0000")
	}
	// includes
	incOut, err := exec.Command("python3-config", "--includes").Output()
	if err != nil {
		incOut = []byte("-I/usr/include/python3.15")
	}
	inc := strings.Fields(string(incOut))
	args = append(args, inc...)
	args = append(args, "-o", extOut, srcPath)
	cmd := exec.Command("gcc", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build ext %s failed: %v: %s", name, err, out)
	}
	return dir, shimOut
}

func TestExtensionSinglePhase(t *testing.T) {
	root, _ := filepath.Abs("../..")
	srcPath := filepath.Join(root, "testdata", "capi", "fython_testext.c")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	suffix := ".cpython-315-x86_64-linux-gnu.so"
	if runtime.GOOS != "linux" {
		suffix = ".so"
	}
	dir, shimOut := buildExtension(t, "fython_testext", string(src), suffix, false)
	t.Setenv("FYTHON_SHIM", shimOut)

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{dir})
	v := engine.NewVM()
	code := `
import fython_testext
hello = fython_testext.hello()
add = fython_testext.add(10, 32)
greeted = fython_testext.greet("world")
d = fython_testext.make_dict()
lst = fython_testext.make_list()
pair = fython_testext.build_pair(7, "hi")
f = fython_testext.float_test(21.0)
bl = fython_testext.bytes_len(b"hello")
kw = fython_testext.kw_add(5, b=7)
try:
    fython_testext.fail()
except ValueError as e:
    caught = str(e)
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v.RunCode(co)
	check := func(name string, want string) {
		got, ok := v.GetGlobal(name)
		if !ok {
			t.Fatalf("global %s missing", name)
		}
		if got.String() != want && got.AsString() != want {
			// For ints, compare AsInt64
			if want == "42" && got.AsInt64() == 42 {
				return
			}
			t.Errorf("%s = %v, want %s", name, got, want)
		}
	}
	// hello ==42
	if g, ok := v.GetGlobal("hello"); !ok || g.AsInt64() != 42 {
		t.Errorf("hello = %v", g)
	}
	if g, ok := v.GetGlobal("add"); !ok || g.AsInt64() != 42 {
		t.Errorf("add = %v", g)
	}
	if g, ok := v.GetGlobal("greeted"); !ok || g.AsString() != "world" {
		t.Errorf("greeted = %v", g)
	}
	if g, ok := v.GetGlobal("f"); !ok || g.AsFloat64() != 42.0 {
		t.Errorf("float = %v", g)
	}
	if g, ok := v.GetGlobal("bl"); !ok || g.AsInt64() != 5 {
		t.Errorf("bytes_len = %v", g)
	}
	if g, ok := v.GetGlobal("kw"); !ok || g.AsInt64() != 12 {
		t.Errorf("kw = %v", g)
	}
	_ = check
	if g, ok := v.GetGlobal("caught"); !ok || g.AsString() == "" {
		t.Errorf("caught missing or empty: %v", g)
	}
	// _imp.extension_suffixes
	co2, _ := compiler.CompileString("import _imp; suf = _imp.extension_suffixes()")
	v.RunCode(co2)
	if g, ok := v.GetGlobal("suf"); !ok || g.AsList().Len() == 0 {
		t.Errorf("extension_suffixes empty: %v", g)
	}
	// Check a is b identity for dict/list handled in C (interned same object)
	// GC cleanup: create many objects and DECREF them via Python? Ensure no leak/crash.
	// churn exercises _Py_Dealloc free-list reuse (1000 temp objects created/freed in C).
	co3, _ := compiler.CompileString("import fython_testext; c = fython_testext.churn()")
	v.RunCode(co3)
	if g, ok := v.GetGlobal("c"); !ok || g.AsInt64() != 42 {
		t.Errorf("churn = %v, want 42", g)
	}
	// Run churn repeatedly to verify free-list reuse (handle count should not grow unboundedly).
	// Arena is global; we just verify no crash and stable behavior.
	for i := range 5 {
		coN, _ := compiler.CompileString("import fython_testext; x = fython_testext.churn()")
		v.RunCode(coN)
		if g, ok := v.GetGlobal("x"); !ok || g.AsInt64() != 42 {
			t.Fatalf("churn iter %d = %v", i, g)
		}
	}
}

func TestExtensionMultiPhase(t *testing.T) {
	root, _ := filepath.Abs("../..")
	srcPath := filepath.Join(root, "testdata", "capi", "fython_testext_multi.c")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	suffix := ".cpython-315-x86_64-linux-gnu.so"
	if runtime.GOOS != "linux" {
		suffix = ".so"
	}
	dir, shimOut := buildExtension(t, "fython_testext_multi", string(src), suffix, false)
	t.Setenv("FYTHON_SHIM", shimOut)

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{dir})
	v := engine.NewVM()
	co, _ := compiler.CompileString("import fython_testext_multi; hello = fython_testext_multi.hello(); ver = fython_testext_multi.VERSION")
	v.RunCode(co)
	if g, ok := v.GetGlobal("hello"); !ok || g.AsInt64() != 43 {
		t.Errorf("multi hello = %v", g)
	}
	if g, ok := v.GetGlobal("ver"); !ok || g.AsInt64() != 2 {
		t.Errorf("multi VERSION = %v", g)
	}
}

func TestExtensionABI3(t *testing.T) {
	root, _ := filepath.Abs("../..")
	srcPath := filepath.Join(root, "testdata", "capi", "fython_testext.c")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	dir, shimOut := buildExtension(t, "fython_testext", string(src), ".abi3.so", true)
	t.Setenv("FYTHON_SHIM", shimOut)

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{dir})
	v := engine.NewVM()
	co, _ := compiler.CompileString("import fython_testext; hello = fython_testext.hello()")
	v.RunCode(co)
	if g, ok := v.GetGlobal("hello"); !ok || g.AsInt64() != 42 {
		t.Errorf("abi3 hello = %v", g)
	}
}
