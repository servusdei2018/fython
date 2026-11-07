package vm_test

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

var (
	builtFythonOnce sync.Once
	builtFythonPath string
	builtFythonErr  error
)

func buildFythonBinary(t *testing.T) string {
	t.Helper()
	builtFythonOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "fython-cli-bin-*")
		if err != nil {
			builtFythonErr = err
			return
		}
		binPath := filepath.Join(tmpDir, "fython")
		cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/fython")
		out, err := cmd.CombinedOutput()
		if err != nil {
			builtFythonErr = &execError{err: err, out: string(out)}
			return
		}
		builtFythonPath = binPath
	})
	if builtFythonErr != nil {
		t.Fatalf("failed to build fython binary: %v", builtFythonErr)
	}
	return builtFythonPath
}

type execError struct {
	err error
	out string
}

func (e *execError) Error() string {
	return e.err.Error() + "\n" + e.out
}

func runFythonCLI(t *testing.T, stdin string, env []string, args ...string) (string, string, int) {
	t.Helper()
	bin := buildFythonBinary(t)
	cmd := exec.Command(bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("failed to execute fython %v: %v", args, err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func TestCLIFlagsAndRunpy(t *testing.T) {
	t.Run("inline command -c and sys attributes", func(t *testing.T) {
		code := `
import sys
assert sys.argv == ["-c", "alpha", "--beta"], f"unexpected argv: {sys.argv}"
assert sys.flags.dont_write_bytecode == 1
assert sys.dont_write_bytecode is True
assert sys.flags.unbuffered == 1
assert "ignore" in sys.warnoptions, f"unexpected warnoptions: {sys.warnoptions}"
assert sys._xoptions.get("dev") == "1", f"unexpected _xoptions: {sys._xoptions}"
assert sys._xoptions.get("utf8") is True, f"unexpected _xoptions: {sys._xoptions}"
print("CLI_C_OK")
`
		stdout, stderr, exitCode := runFythonCLI(
			t, "", nil,
			"-B", "-u", "-W", "ignore", "-X", "dev=1", "-X", "utf8",
			"-c", code, "alpha", "--beta",
		)
		if exitCode != 0 {
			t.Fatalf("expected exit 0, got %d\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
		}
		if strings.TrimSpace(stdout) != "CLI_C_OK" {
			t.Fatalf("stdout = %q, want CLI_C_OK", stdout)
		}
	})

	t.Run("optimization flags -O and -OO", func(t *testing.T) {
		code := `
"""Module docstring."""
import sys

def documented():
    """Function docstring."""
    return 42

assert False, "should be stripped under -O"
assert __debug__ is False
if __debug__:
    raise RuntimeError("unreachable when __debug__ is False")
print(f"OPT={sys.flags.optimize} DEBUG={__debug__} DOC={documented.__doc__}")
`
		stdoutO, stderrO, codeO := runFythonCLI(t, "", nil, "-O", "-c", code)
		if codeO != 0 {
			t.Fatalf("-O failed (exit %d): %s", codeO, stderrO)
		}
		if strings.TrimSpace(stdoutO) != "OPT=1 DEBUG=False DOC=Function docstring." {
			t.Fatalf("-O stdout = %q", stdoutO)
		}

		stdoutOO, stderrOO, codeOO := runFythonCLI(t, "", nil, "-OO", "-c", code)
		if codeOO != 0 {
			t.Fatalf("-OO failed (exit %d): %s", codeOO, stderrOO)
		}
		if strings.TrimSpace(stdoutOO) != "OPT=2 DEBUG=False DOC=None" {
			t.Fatalf("-OO stdout = %q", stdoutOO)
		}
	})

	t.Run("runpy -m json.tool pretty printing stdin", func(t *testing.T) {
		inputJSON := `{"b":1,"a":[2,3]}`
		stdout, stderr, exitCode := runFythonCLI(t, inputJSON, nil, "-m", "json.tool")
		if exitCode != 0 {
			t.Fatalf("-m json.tool failed (exit %d):\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
		}
		want := "{\n    \"b\": 1,\n    \"a\": [\n        2,\n        3\n    ]\n}"
		if strings.TrimSpace(stdout) != want {
			t.Fatalf("-m json.tool output:\ngot:\n%s\nwant:\n%s", strings.TrimSpace(stdout), want)
		}
	})

	t.Run("runpy -m unittest execution", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "test_sample.py")
		testSrc := `
import unittest

class SampleTest(unittest.TestCase):
    def test_addition(self):
        self.assertEqual(20 + 22, 42)

if __name__ == "__main__":
    unittest.main()
`
		if err := os.WriteFile(testFile, []byte(testSrc), 0644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exitCode := runFythonCLI(
			t, "",
			[]string{"PYTHONPATH=" + tmpDir},
			"-m", "unittest", "test_sample",
		)
		if exitCode != 0 {
			t.Fatalf("-m unittest failed (exit %d):\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
		}
		combined := stdout + stderr
		if !strings.Contains(combined, "OK") {
			t.Fatalf("expected unittest output to contain OK, got:\n%s", combined)
		}
	})

	t.Run("runpy -m package relative imports and __package__", func(t *testing.T) {
		tmpDir := t.TempDir()
		pkgDir := filepath.Join(tmpDir, "mypkg")
		if err := os.MkdirAll(pkgDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, "__init__.py"), []byte("PKG_VAL = 'from_init'\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, "helper.py"), []byte("def greet(name):\n    return f'hello {name}'\n"), 0644); err != nil {
			t.Fatal(err)
		}
		cliSrc := `
import sys
from . import PKG_VAL
from .helper import greet

assert __name__ == "__main__"
assert __package__ == "mypkg", f"unexpected __package__: {__package__}"
assert __spec__ is not None and __spec__.name == "mypkg.cli"
print(f"{PKG_VAL}:{greet(sys.argv[1])}")
`
		if err := os.WriteFile(filepath.Join(pkgDir, "cli.py"), []byte(cliSrc), 0644); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, exitCode := runFythonCLI(
			t, "",
			[]string{"PYTHONPATH=" + tmpDir},
			"-m", "mypkg.cli", "world",
		)
		if exitCode != 0 {
			t.Fatalf("-m mypkg.cli failed (exit %d):\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
		}
		if strings.TrimSpace(stdout) != "from_init:hello world" {
			t.Fatalf("stdout = %q, want 'from_init:hello world'", stdout)
		}
	})

	t.Run("directory __main__.py execution", func(t *testing.T) {
		appDir := filepath.Join(t.TempDir(), "myapp")
		if err := os.MkdirAll(appDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(appDir, "util.py"), []byte("VALUE = 99\n"), 0644); err != nil {
			t.Fatal(err)
		}
		mainSrc := `
import sys
import util
print(f"DIR_MAIN:{util.VALUE}:{sys.argv[1]}")
`
		if err := os.WriteFile(filepath.Join(appDir, "__main__.py"), []byte(mainSrc), 0644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exitCode := runFythonCLI(t, "", nil, appDir, "arg_one")
		if exitCode != 0 {
			t.Fatalf("directory execution failed (exit %d):\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
		}
		if strings.TrimSpace(stdout) != "DIR_MAIN:99:arg_one" {
			t.Fatalf("stdout = %q", stdout)
		}
	})

	t.Run("stdin - execution and interactive -i flag", func(t *testing.T) {
		stdout, stderr, exitCode := runFythonCLI(t, "import sys\nprint('STDIN:' + sys.argv[1])\n", nil, "-", "passed_arg")
		if exitCode != 0 {
			t.Fatalf("stdin execution failed (exit %d): %s", exitCode, stderr)
		}
		if strings.TrimSpace(stdout) != "STDIN:passed_arg" {
			t.Fatalf("stdout = %q", stdout)
		}

		// Test -i interactive inspection after script execution
		scriptPath := filepath.Join(t.TempDir(), "init_script.py")
		if err := os.WriteFile(scriptPath, []byte("shared_var = 123\n"), 0644); err != nil {
			t.Fatal(err)
		}
		stdoutI, stderrI, exitCodeI := runFythonCLI(t, "print('REPL_VAR:', shared_var + 7)\n", nil, "-i", scriptPath)
		if exitCodeI != 0 {
			t.Fatalf("-i execution failed (exit %d): %s", exitCodeI, stderrI)
		}
		if strings.TrimSpace(stdoutI) != "REPL_VAR: 130" {
			t.Fatalf("-i stdout = %q, want 'REPL_VAR: 130'", stdoutI)
		}
	})
}

func TestSitePackagesAndPthDiscovery(t *testing.T) {
	tmpRoot := t.TempDir()
	venvDir := filepath.Join(tmpRoot, ".venv")
	sitePackages := filepath.Join(venvDir, "lib", "python3.15", "site-packages")
	if err := os.MkdirAll(sitePackages, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Write pyvenv.cfg
	pyvenvCfg := "home = /usr/bin\ninclude-system-site-packages = false\nversion = 3.15.0\n"
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte(pyvenvCfg), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Create site-packages/my_pkg/__init__.py and resource file
	myPkgDir := filepath.Join(sitePackages, "my_pkg")
	if err := os.MkdirAll(myPkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(myPkgDir, "__init__.py"), []byte("GREETING = 'hello from venv'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(myPkgDir, "config.txt"), []byte("venv_resource_payload"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Create site-packages/my_pkg-1.0.0.dist-info/METADATA
	distInfoDir := filepath.Join(sitePackages, "my_pkg-1.0.0.dist-info")
	if err := os.MkdirAll(distInfoDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadataContent := "Metadata-Version: 2.1\nName: my_pkg\nVersion: 1.0.0\nSummary: Mock package in venv\n"
	if err := os.WriteFile(filepath.Join(distInfoDir, "METADATA"), []byte(metadataContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Create editable source directory and editable.pth in site-packages
	editableSrcDir := filepath.Join(tmpRoot, "editable_src")
	editablePkgDir := filepath.Join(editableSrcDir, "editable_pkg")
	if err := os.MkdirAll(editablePkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(editablePkgDir, "__init__.py"), []byte("EDITABLE_OK = True\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pthContent := "# Editable install path\n" + editableSrcDir + "\nimport sys; sys._pth_hook_ran = True\n"
	if err := os.WriteFile(filepath.Join(sitePackages, "editable.pth"), []byte(pthContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("VIRTUAL_ENV", venvDir)

	code := `
import sys
import importlib.metadata
import importlib.resources
import my_pkg
import editable_pkg

assert sys.prefix == "` + venvDir + `", f"unexpected sys.prefix: {sys.prefix}"
assert getattr(sys, "_pth_hook_ran", False) is True, "expected .pth import line to execute"
assert my_pkg.GREETING.strip() == "hello from venv"
assert editable_pkg.EDITABLE_OK is True

ver = importlib.metadata.version("my_pkg")
assert ver == "1.0.0", f"expected version 1.0.0, got {ver}"

meta = importlib.metadata.metadata("my_pkg")
assert meta["Summary"] == "Mock package in venv", f"unexpected summary: {meta['Summary']}"

res_text = importlib.resources.files("my_pkg").joinpath("config.txt").read_text(encoding="utf-8")
assert res_text == "venv_resource_payload", f"unexpected resource text: {res_text}"
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception:\n%s", v.FormatException(exc))
	}
}

func TestDirectWheelImport(t *testing.T) {
	tmpDir := t.TempDir()
	whlPath := filepath.Join(tmpDir, "demo_wheel-2.3.4-py3-none-any.whl")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	wheelFiles := map[string]string{
		"demo_wheel/__init__.py": `
from .submod import compute
PACKAGE_LOADED = True
`,
		"demo_wheel/submod.py": `
def compute(x):
    return x * 3 + 1
`,
		"demo_wheel/assets/ greeting.txt":     "loaded straight from wheel zip!",
		"demo_wheel/assets/data.txt":          "wheel_asset_content_123",
		"demo_wheel-2.3.4.dist-info/METADATA": "Metadata-Version: 2.1\nName: demo-wheel\nVersion: 2.3.4\n",
		"__main__.py": `
import demo_wheel
print("WHEEL_MAIN:", demo_wheel.compute(10))
`,
	}
	for name, content := range wheelFiles {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(whlPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Verify importing directly from .whl on sys.path in-memory without unpacking
	code := `
import sys
sys.path.insert(0, "` + whlPath + `")

import demo_wheel
from demo_wheel import submod
import importlib.metadata
import importlib.resources

assert demo_wheel.PACKAGE_LOADED is True
assert demo_wheel.compute(14) == 43
assert submod.compute(5) == 16
assert demo_wheel.__spec__ is not None
assert demo_wheel.__spec__.loader is not None

raw_bytes = demo_wheel.__loader__.get_data("demo_wheel/assets/data.txt")
assert raw_bytes == b"wheel_asset_content_123", f"unexpected get_data: {raw_bytes}"

res_txt = importlib.resources.files("demo_wheel").joinpath("assets/data.txt").read_text(encoding="utf-8")
assert res_txt == "wheel_asset_content_123", f"unexpected resources.files read_text: {res_txt}"

ver = importlib.metadata.version("demo-wheel")
assert ver == "2.3.4", f"expected wheel version 2.3.4, got {ver}"
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v := vm.NewVM()
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception:\n%s", v.FormatException(exc))
	}

	// 2. Verify direct CLI execution of the .whl archive (`fython demo_wheel.whl`)
	stdout, stderr, exitCode := runFythonCLI(t, "", nil, whlPath)
	if exitCode != 0 {
		t.Fatalf("direct wheel execution failed (exit %d):\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "WHEEL_MAIN: 31" {
		t.Fatalf("wheel CLI stdout = %q, want 'WHEEL_MAIN: 31'", stdout)
	}
}
