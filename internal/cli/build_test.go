package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/servusdei2018/fython/internal/cli"
)

var (
	builtFythonOnce sync.Once
	builtFythonPath string
	builtFythonErr  error
)

func getFythonBinary(t *testing.T) string {
	t.Helper()
	builtFythonOnce.Do(func() {
		tmpDir := t.TempDir()
		binPath := filepath.Join(tmpDir, "fython")
		cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/fython")
		out, err := cmd.CombinedOutput()
		if err != nil {
			builtFythonErr = err
			t.Logf("build output: %s", string(out))
			return
		}
		builtFythonPath = binPath
	})
	if builtFythonErr != nil {
		t.Fatalf("failed to build fython: %v", builtFythonErr)
	}
	return builtFythonPath
}

func TestStandaloneBinaryBundler(t *testing.T) {
	fythonBin := getFythonBinary(t)

	// Resolve wheel path
	wheelsDir, err := filepath.Abs("../../pkg/vm/testdata/wheels")
	if err != nil {
		t.Fatal(err)
	}
	wheelPath := filepath.Join(wheelsDir, "packaging-26.3-py3-none-any.whl")
	if _, err := os.Stat(wheelPath); err != nil {
		t.Fatalf("packaging wheel not found at %s: %v", wheelPath, err)
	}

	appDir := t.TempDir()
	helperPy := filepath.Join(appDir, "math_helper.py")
	helperCode := `
def calculate(x, y):
    return x * y + 7
`
	if err := os.WriteFile(helperPy, []byte(helperCode), 0644); err != nil {
		t.Fatal(err)
	}

	mainPy := filepath.Join(appDir, "app_entry.py")
	mainCode := `
import sys
import math_helper
import packaging

res = math_helper.calculate(6, 7)
assert packaging.__version__ is not None
print(f"STANDALONE_SUCCESS:{res}:{sys.argv[1]}:{packaging.__name__}")
`
	if err := os.WriteFile(mainPy, []byte(mainCode), 0644); err != nil {
		t.Fatal(err)
	}

	outBin := filepath.Join(t.TempDir(), "bundled_app")

	// Run `fython build` command
	buildCmd := exec.Command(
		fythonBin, "build", mainPy,
		"-o", outBin,
		"--wheel", wheelPath,
	)
	var buildOut bytes.Buffer
	buildCmd.Stdout = &buildOut
	buildCmd.Stderr = &buildOut
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("fython build failed: %v\nOutput: %s", err, buildOut.String())
	}

	fi, err := os.Stat(outBin)
	if err != nil {
		t.Fatalf("output binary not found: %v", err)
	}
	if fi.Mode()&0111 == 0 {
		t.Fatalf("output binary is not executable: mode=%v", fi.Mode())
	}

	// Verify it contains the 16-byte trailer
	f, err := os.Open(outBin)
	if err != nil {
		t.Fatal(err)
	}
	offset, ok, err := cli.DetectBundleTrailer(f, fi.Size())
	_ = f.Close()
	if err != nil {
		t.Fatalf("DetectBundleTrailer error: %v", err)
	}
	if !ok {
		t.Fatal("expected DetectBundleTrailer to report true")
	}
	if offset == 0 || offset >= uint64(fi.Size()-cli.BundleFooterSize) {
		t.Fatalf("unexpected bundle offset: %d (file size %d)", offset, fi.Size())
	}

	// Execute compiled standalone binary in clean isolated environment:
	// PATH="", HOME=temp, working directory outside source tree
	isolatedDir := t.TempDir()
	homeDir := t.TempDir()
	runCmd := exec.Command(outBin, "deployed_payload")
	runCmd.Dir = isolatedDir
	runCmd.Env = []string{
		"HOME=" + homeDir,
		"PATH=",
	}

	var stdout, stderr bytes.Buffer
	runCmd.Stdout = &stdout
	runCmd.Stderr = &stderr

	start := time.Now()
	err = runCmd.Run()
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("standalone binary execution failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	outStr := strings.TrimSpace(stdout.String())
	wantPrefix := "STANDALONE_SUCCESS:49:deployed_payload:packaging"
	if outStr != wantPrefix {
		t.Fatalf("got output %q, want %q", outStr, wantPrefix)
	}

	t.Logf("Standalone binary executed successfully in %v (< 25 ms)", duration)
	if duration > 25*time.Millisecond {
		t.Errorf("expected execution time < 25 ms, got %v", duration)
	}
}
