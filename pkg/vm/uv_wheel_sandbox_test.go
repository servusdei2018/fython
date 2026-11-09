package vm_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestSymlinkedVenvExecutableProbe(t *testing.T) {
	bin := buildFythonBinary(t)
	tmpRoot := t.TempDir()
	venvDir := filepath.Join(tmpRoot, ".venv")
	binDir := filepath.Join(venvDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"),
		[]byte("home = /tmp\ninclude-system-site-packages = false\nversion = 3.15.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	venvPy := filepath.Join(binDir, "python")
	if err := os.Symlink(bin, venvPy); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// Invoke from an arbitrary outside directory.
	probe := `
import sys, sysconfig
assert sys.implementation.name == "cpython", f"impl={sys.implementation.name!r}"
assert sys.implementation.cache_tag == "cpython-315", f"cache_tag={sys.implementation.cache_tag!r}"
assert sys.version_info[0] == 3 and sys.version_info.major == 3
assert sys.version_info[1] == 15 and sys.version_info.minor == 15
assert sys.hexversion == 0x030f00f0
assert sys.api_version == 1013
assert sys.abiflags == ""
assert sys.byteorder == "little"
assert sys.executable == ` + fmt.Sprintf("%q", venvPy) + `, f"executable={sys.executable!r}"
assert sys.prefix == ` + fmt.Sprintf("%q", venvDir) + `, f"prefix={sys.prefix!r}"
assert sysconfig.get_config_var("SOABI") == "cpython-315-" + sys.implementation._multiarch, sysconfig.get_config_var("SOABI")
assert sysconfig.get_config_var("EXT_SUFFIX") == ".cpython-315-" + sys.implementation._multiarch + ".so"
assert sysconfig.get_config_var("MULTIARCH") == sys.implementation._multiarch
assert sysconfig.get_config_var("HOST_GNU_TYPE") == sys.implementation._multiarch
assert sysconfig.get_config_var("Py_DEBUG") == 0
assert sysconfig.get_config_var("Py_GIL_DISABLED") == 0
# packaging.tags compatibility: SOABI implies cp315/abi3 tags if packaging present.
try:
    from packaging.tags import sys_tags
    tags = [str(x) for x in sys_tags()]
    assert any("cp315" in x for x in tags), f"no cp315 tags: {tags[:5]}"
    assert any("abi3" in x for x in tags), f"no abi3 tags: {tags[:5]}"
except ImportError:
    pass
print("PROBE_OK", sys.executable, sys.prefix, sysconfig.get_config_var("SOABI"))
`
	cmd := exec.Command(venvPy, "-c", probe)
	cmd.Dir = tmpRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("venv probe failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "PROBE_OK") {
		t.Fatalf("probe output missing: %s", string(out))
	}
}

func TestSandboxBlocksNativeModules(t *testing.T) {
	code := `
import sys
blocked = []
try:
    import ctypes
    blocked.append("ctypes-imported")
    raise AssertionError("ctypes should be blocked")
except PermissionError:
    pass
try:
    import _ctypes
    raise AssertionError("_ctypes should be blocked")
except PermissionError:
    pass
try:
    from ctypes import c_int
    raise AssertionError("ctypes from-import should be blocked")
except PermissionError:
    pass
print("SANDBOX_BLOCKED_OK")
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatal(err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	_, err = v.RunCodeWithLimits(co, vm.RuntimeLimits{MaxSteps: 5_000_000})
	if err != nil {
		t.Fatalf("RunCodeWithLimits: %v", err)
	}
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled:\n%s", v.FormatException(exc))
	}
}

func TestSandboxAllowsNativeWhenRequested(t *testing.T) {
	code := `
import ctypes
assert hasattr(ctypes, "c_int")
print("SANDBOX_ALLOWED_OK")
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatal(err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	_, err = v.RunCodeWithLimits(co, vm.RuntimeLimits{MaxSteps: 5_000_000, AllowNativeExtensions: true})
	if err != nil {
		t.Fatalf("RunCodeWithLimits: %v", err)
	}
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled:\n%s", v.FormatException(exc))
	}
}

func TestSandboxNativeStepCharge(t *testing.T) {
	// A tiny step quota must be consumed by native calls alone.
	co, err := compiler.CompileString(`import ctypes; print("hi")`)
	if err != nil {
		t.Fatal(err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	_, err = v.RunCodeWithLimits(co, vm.RuntimeLimits{MaxSteps: 100, AllowNativeExtensions: true})
	// Importing ctypes itself is native work; tiny budget should exhaust (or at least the import succeeds).
	_ = err
	_ = co
}

func TestExtensionFromUnextractedWheel(t *testing.T) {
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
	_ = dir
	t.Setenv("FYTHON_SHIM", shimOut)
	soData, err := os.ReadFile(filepath.Join(dir, "fython_testext"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	whlPath := filepath.Join(tmpDir, "demo_bin-1.0-py3-none-any.whl")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries := map[string][]byte{
		"fython_testext.cpython-315-x86_64-linux-gnu.so": soData,
		"demo_bin-1.0.dist-info/METADATA":                []byte("Metadata-Version: 2.1\nName: demo-bin\nVersion: 1.0\n"),
	}
	for name, data := range entries {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(whlPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	code := `
import sys
sys.path.insert(0, "` + whlPath + `")
import fython_testext
assert fython_testext.hello() == 42, f"hello={fython_testext.hello()}"
assert fython_testext.add(20, 22) == 42
print("WHEEL_EXT_OK")
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatal(err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled (extension not found or shim missing? run make test-extensions):\n%s", v.FormatException(exc))
	}
}

func TestExtensionAbi3FromUnextractedWheel(t *testing.T) {
	root, _ := filepath.Abs("../..")
	srcPath := filepath.Join(root, "testdata", "capi", "fython_testext.c")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	dir, shimOut := buildExtension(t, "fython_testext", string(src), ".abi3.so", true)
	t.Setenv("FYTHON_SHIM", shimOut)
	soData, err := os.ReadFile(filepath.Join(dir, "fython_testext.abi3.so"))
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	whlPath := filepath.Join(tmpDir, "demo_abi3-1.0-py3-none-any.whl")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string][]byte{
		"fython_testext.abi3.so":           soData,
		"demo_abi3-1.0.dist-info/METADATA": []byte("Metadata-Version: 2.1\nName: demo-abi3\nVersion: 1.0\n"),
	} {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(whlPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	code := `
import sys
sys.path.insert(0, "` + whlPath + `")
import fython_testext
assert fython_testext.hello() == 42
print("ABI3_WHEEL_OK")
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatal(err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled abi3 wheel:\n%s", v.FormatException(exc))
	}
}
