package stdlib

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestCPythonLicense(t *testing.T) {
	text := string(CPythonLicense)
	if !strings.Contains(text, "PYTHON SOFTWARE FOUNDATION LICENSE VERSION 2") {
		t.Fatal("embedded lib/LICENSE is missing the PSF License Version 2")
	}
	if !strings.Contains(text, "BEOPEN.COM LICENSE AGREEMENT") {
		t.Fatal("embedded lib/LICENSE is missing the historical BeOpen license")
	}
}

func TestEmbeddedStdlib(t *testing.T) {
	modules := []string{
		"dataclasses",
		"enum",
		"typing",
		"abc",
		"_collections_abc",
		"collections",
		"functools",
		"contextlib",
		"unittest",
	}

	for _, mod := range modules {
		path, isPkg, found := FindModule(mod)
		if !found {
			t.Fatalf("module %s not found in embedded stdlib", mod)
		}
		data, err := ReadModule(path)
		if err != nil {
			t.Fatalf("failed to read %s (%s): %v", mod, path, err)
		}
		if len(data) == 0 {
			t.Fatalf("module %s is empty", mod)
		}
		t.Logf("Found %s (pkg=%v, size=%d bytes)", mod, isPkg, len(data))
	}
}

// TestZeroOverlays proves that every file embedded under lib/ is byte-for-byte
// the pinned CPython drop recorded in lib/UPSTREAM.sha256: no overlay, no
// local edit, no stray file.
func TestZeroOverlays(t *testing.T) {
	data, err := StdlibFS.ReadFile("lib/UPSTREAM.sha256")
	if err != nil {
		t.Fatalf("missing manifest: %v", err)
	}
	want := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			t.Fatalf("malformed manifest line %q", line)
		}
		want[name] = sum
	}
	if len(want) == 0 {
		t.Fatal("empty manifest")
	}
	seen := map[string]bool{}
	err = fs.WalkDir(StdlibFS, "lib", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "lib/")
		if rel == "UPSTREAM.sha256" {
			return nil
		}
		sum, ok := want[rel]
		if !ok {
			t.Errorf("%s is embedded but not in the upstream manifest (overlay?)", rel)
			return nil
		}
		seen[rel] = true
		body, err := StdlibFS.ReadFile(p)
		if err != nil {
			return err
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != sum {
			t.Errorf("%s differs from upstream: sha256 %s, manifest %s", rel, got, sum)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range want {
		if !seen[rel] {
			t.Errorf("manifest lists %s but it is not embedded", rel)
		}
	}
	for _, must := range []string{"ast.py", "_ast_unparse.py", "textwrap.py", "annotationlib.py", "code.py", "codeop.py", "rlcompleter.py"} {
		if !seen[must] {
			t.Errorf("%s must come from upstream", must)
		}
	}
}
