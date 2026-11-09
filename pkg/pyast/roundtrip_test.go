package pyast

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnparseRoundTrip(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	roots := []string{"../stdlib/lib", "testdata"}
	if extra := os.Getenv("PYAST_CORPUS"); extra != "" {
		roots = append(roots, strings.Split(extra, string(os.PathListSeparator))...)
	}
	tmp := t.TempDir()
	var pairs []string
	n := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() || !strings.HasSuffix(p, ".py") {
				return nil //nolint:nilerr
			}
			src, readErr := os.ReadFile(p)
			if readErr != nil || strings.Contains(string(src), `\N{`) {
				return nil //nolint:nilerr
			}
			tree, perr := Parse(src, ModeExec)
			if perr != nil {
				return nil //nolint:nilerr
			}
			out := Unparse(tree, UnparseOptions{PadLines: true})
			n++
			f := filepath.Join(tmp, "u"+strings.ReplaceAll(strings.ReplaceAll(p, "/", "_"), "..", "")+".py")
			_ = os.WriteFile(f, []byte(out+"\n"), 0o644)
			pairs = append(pairs, p+"::"+f)
			return nil
		})
	}
	cmd := exec.Command(py, append([]string{"roundtrip.py"}, pairs...)...)
	out, _ := cmd.CombinedOutput()
	if len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 12 {
			lines = lines[:12]
		}
		t.Errorf("round-trip mismatches (of %d files):\n%s", n, strings.Join(lines, "\n"))
	}
}
