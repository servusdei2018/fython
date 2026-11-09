package repl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func newWorker(t *testing.T) (*vm.VM, *Worker) {
	t.Helper()
	v := vm.NewVM()
	w := NewWorker(v, 0)
	t.Cleanup(w.Close)
	return v, w
}

// TestSingleModeDisplayHookAndUnderscore drives the worker the way the
// TUI and fallback loops do: "single" mode expressions echo, None does
// not, and _ tracks the last echoed value until explicitly assigned.
func TestSingleModeDisplayHookAndUnderscore(t *testing.T) {
	_, w := newWorker(t)

	res := w.Execute("21 * 2", "<stdin>")
	if res.CompileError != "" {
		t.Fatalf("compile error: %s", res.CompileError)
	}
	if res.Output != "42\n" {
		t.Fatalf("echo = %q, want %q", res.Output, "42\n")
	}

	res = w.Execute("None", "<stdin>")
	if res.Output != "" {
		t.Fatalf("None echo = %q, want empty", res.Output)
	}

	res = w.Execute("print(_)", "<stdin>")
	if res.Output != "42\n" {
		t.Fatalf("_ after None = %q, want %q", res.Output, "42\n")
	}

	res = w.Execute("_ = 99", "<stdin>")
	if res.Output != "" {
		t.Fatalf("assignment echo = %q, want empty", res.Output)
	}

	res = w.Execute("print(_)", "<stdin>")
	if res.Output != "99\n" {
		t.Fatalf("_ after explicit assign = %q, want %q", res.Output, "99\n")
	}
}

// TestIncompleteBlockDetection checks the incomplete-vs-hard-error
// classification for blocks, brackets, strings, and continuations.
func TestIncompleteBlockDetection(t *testing.T) {
	incomplete := []string{
		"def foo(x):",
		"def foo(x):\n    pass",
		"class C:",
		"if x:",
		"if x:\n    pass\nelse:",
		"x = [1, 2",
		"x = {\"a\":",
		"x = (1 +",
		"s = \"\"\"abc",
		"x = 1 + \\",
		"try:",
		"for i in range(3):",
	}
	for _, src := range incomplete {
		if !compiler.IncompleteInput([]byte(src)) {
			t.Errorf("IncompleteInput(%q) = false, want true", src)
		}
	}

	hard := []string{
		"1 + * 2",
		"def 123:",
		"x = [1 2]",
		"if True pass",
	}
	for _, src := range hard {
		if compiler.IncompleteInput([]byte(src)) {
			t.Errorf("IncompleteInput(%q) = true, want false (hard error)", src)
		}
	}

	complete := []string{
		"def foo(x):\n    pass\n",
		"x = [1, 2]\n",
		"y = 1\n",
		"if x:\n    pass\n\n",
	}
	for _, src := range complete {
		if compiler.IncompleteInput([]byte(src)) {
			t.Errorf("IncompleteInput(%q) = true, want false", src)
		}
	}

	// Hard errors surface through the worker immediately.
	_, w := newWorker(t)
	res := w.Execute("1 + * 2", "<stdin>")
	if res.CompileError == "" {
		t.Fatal("hard syntax error must report CompileError")
	}
	if res.Traceback == "" || !strings.Contains(res.Traceback, "SyntaxError") {
		t.Fatalf("hard error traceback = %q", res.Traceback)
	}
}

// TestTabCompleterImportAndAttrs covers import and attribute completion
// without importing unloaded modules.
func TestTabCompleterImportAndAttrs(t *testing.T) {
	_, w := newWorker(t)

	src := "import pathl"
	cands := w.Complete(src, len(src))
	if !anyCandidate(cands, "pathlib") {
		t.Fatalf("import pathl candidates = %v, want pathlib", candTexts(cands))
	}

	src = "from collections import deq"
	cands = w.Complete(src, len(src))
	if !anyCandidate(cands, "deque") {
		t.Fatalf("from collections import deq candidates = %v, want deque", candTexts(cands))
	}

	res := w.Execute(`s = "hello"`, "<stdin>")
	if res.CompileError != "" {
		t.Fatalf("setup failed: %s", res.CompileError)
	}
	src = "s.up"
	cands = w.Complete(src, len(src))
	if !anyCandidate(cands, "upper") {
		t.Fatalf("s.up candidates = %v, want upper", candTexts(cands))
	}
	for _, c := range cands {
		if c.Text == "upper" && !c.Callable {
			t.Fatal("upper should be marked callable")
		}
	}
}

func anyCandidate(cands []Candidate, want string) bool {
	for _, c := range cands {
		if c.Text == want {
			return true
		}
	}
	return false
}

func candTexts(cands []Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Text
	}
	return out
}

// TestBareCommandsAndCtrlC covers bare exit/quit/clear, PYTHONSTARTUP
// execution, -i state preservation, and Ctrl+C mid-execution.
func TestBareCommandsAndCtrlC(t *testing.T) {
	// Bare exit stops the fallback loop and ignores later lines.
	var outBuf, errBuf bytes.Buffer
	cfg := basicConfig(t, &outBuf, &errBuf)
	cfg.Stdin = strings.NewReader("x = 1\nprint(x)\nexit\nprint(999)\n")
	if err := RunBasic(cfg); err != nil {
		t.Fatalf("RunBasic: %v", err)
	}
	if outBuf.String() != "1\n" {
		t.Fatalf("fallback output = %q, want %q", outBuf.String(), "1\n")
	}
	if strings.Contains(outBuf.String(), "999") {
		t.Fatal("code after bare exit must not run")
	}

	// PYTHONSTARTUP runs before the first prompt.
	startup := filepath.Join(t.TempDir(), "startup.py")
	if err := os.WriteFile(startup, []byte("STARTUP_MARKER = 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYTHONSTARTUP", startup)
	outBuf.Reset()
	cfg = basicConfig(t, &outBuf, &errBuf)
	cfg.Stdin = strings.NewReader("print(STARTUP_MARKER)\n")
	if err := Run(cfg); err != nil {
		t.Fatalf("Run startup: %v", err)
	}
	if outBuf.String() != "7\n" {
		t.Fatalf("PYTHONSTARTUP effect = %q, want %q", outBuf.String(), "7\n")
	}

	// -i preserves the VM namespace into the REPL.
	v := vm.NewVM()
	worker := NewWorker(v, 0)
	defer worker.Close()
	if res := worker.ExecuteSource("x = 11", "<script>"); res.CompileError != "" {
		t.Fatalf("script setup: %s", res.CompileError)
	}
	outBuf.Reset()
	cfg = basicConfig(t, &outBuf, &errBuf)
	cfg.VM = v
	cfg.Stdin = strings.NewReader("print(x)\n")
	if err := RunBasic(cfg); err != nil {
		t.Fatalf("RunBasic -i: %v", err)
	}
	if outBuf.String() != "11\n" {
		t.Fatalf("-i state = %q, want %q", outBuf.String(), "11\n")
	}

	// Ctrl+C while a statement is running interrupts it with a
	// KeyboardInterrupt traceback (TUI busy path).
	v2, w2 := newWorker(t)
	done := make(chan Result, 1)
	go func() { done <- w2.Execute("while True:\n    pass", "<stdin>") }()
	time.Sleep(100 * time.Millisecond)
	w2.RequestInterrupt()
	select {
	case res := <-done:
		if !strings.Contains(res.Traceback, "KeyboardInterrupt") {
			t.Fatalf("interrupt traceback = %q, want KeyboardInterrupt", res.Traceback)
		}
		if res.Exit != nil {
			t.Fatal("KeyboardInterrupt must not exit the REPL")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("statement did not unwind after interrupt")
	}
	_ = v2

	// At the prompt, Ctrl+C clears the buffer and prints
	// KeyboardInterrupt, and bare clear emits the clear sequence.
	m := newReplModel(cfg, w2, &History{limit: 1000})
	m.buf = "x = 1"
	m.cursor = len(m.buf)
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	mm := model.(*replModel)
	if mm.buf != "" {
		t.Fatalf("buffer after Ctrl+C = %q, want empty", mm.buf)
	}
}

// TestHistoryPersistence checks save/load round-trips, including
// multiline entries staying intact.
func TestHistoryPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hist")
	t.Setenv("PYTHON_HISTORY", path)
	h := LoadHistory()
	h.path = path
	h.Add("x = 1")
	h.Add("def f():\n    return 2")
	h.Add("y = 2")
	h.Save()

	fresh := LoadHistory()
	if len(fresh.Entries) != 3 {
		t.Fatalf("entries = %q, want 3 entries", fresh.Entries)
	}
	if fresh.Entries[0] != "x = 1" {
		t.Fatalf("entry 0 = %q", fresh.Entries[0])
	}
	if fresh.Entries[1] != "def f():"+"\n    return 2" {
		t.Fatalf("multiline entry = %q", fresh.Entries[1])
	}
	if fresh.Entries[2] != "y = 2" {
		t.Fatalf("entry 2 = %q", fresh.Entries[2])
	}
}

// TestPasteMode verifies F3 paste mode withholds execution until toggled
// off, including blank lines inside the pasted text.
func TestPasteMode(t *testing.T) {
	_, w := newWorker(t)
	h := &History{limit: 1000}
	m := newReplModel(Config{}, w, h)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyF3})
	m = model.(*replModel)
	if !m.paste {
		t.Fatal("F3 must enable paste mode")
	}
	// Type a block containing a blank line while pasting.
	for _, line := range []string{"def g():", "", "    return 1", ""} {
		model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = model.(*replModel)
		for _, r := range line {
			model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(*replModel)
		}
	}
	if !strings.Contains(m.buf, "\n\n") {
		t.Fatalf("paste buffer = %q, want preserved blank line", m.buf)
	}
	// Toggle paste mode off executes the buffer.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF3})
	m = model.(*replModel)
	if m.paste {
		t.Fatal("second F3 must disable paste mode")
	}
	if cmd == nil {
		t.Fatal("leaving paste mode must submit the buffer")
	}
}

// TestColorGating covers NO_COLOR and PYTHON_COLORS.
func TestColorGating(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("PYTHON_COLORS", "1")
	if !ColorEnabled(nil) {
		t.Fatal("PYTHON_COLORS=1 must force colors")
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		t.Skip("NO_COLOR inherited from environment")
	}
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(nil) {
		t.Fatal("NO_COLOR must disable colors")
	}
	_ = os.Unsetenv("NO_COLOR")
	t.Setenv("PYTHON_COLORS", "0")
	if ColorEnabled(nil) {
		t.Fatal("PYTHON_COLORS=0 must disable colors")
	}
}

// TestBasicModeFallback verifies the fallback decision logic.
func TestBasicModeFallback(t *testing.T) {
	t.Setenv("PYTHON_BASIC_REPL", "1")
	if !IsBasic(Config{}) {
		t.Fatal("PYTHON_BASIC_REPL must force basic mode")
	}
	if Highlight("x = 1", true) == "" {
		t.Fatal("highlight must not erase source")
	}
}

func basicConfig(t *testing.T, out, errBuf *bytes.Buffer) Config {
	t.Helper()
	return Config{
		Stdin:      strings.NewReader(""),
		Stdout:     out,
		Stderr:     errBuf,
		Quiet:      true,
		StdinFile:  notTTYFile(t),
		StdoutFile: notTTYFile(t),
		VM:         vm.NewVM(),
	}
}

// notTTYFile returns a file that is never a terminal (/dev/null).
func notTTYFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
