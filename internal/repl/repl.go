// Package repl provides the fython interactive interpreter: a strict
// POSIX line-oriented fallback for non-TTY/piped execution and a BubbleTea
// inline TUI for interactive terminal sessions, both sharing the same
// single-statement ("single" mode) execution semantics.
package repl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// ExitError reports that Python code raised SystemExit; Code carries the
// proposed process exit status.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string { return e.Message }

// Config configures a REPL run.
type Config struct {
	// VM is the interpreter whose __main__ namespace the REPL drives.
	// When nil, a fresh VM is created.
	VM *vm.VM
	// Optimize is the interpreter optimization level (-O count).
	Optimize int
	// Quiet suppresses the startup banner (the -q flag).
	Quiet bool
	// IgnoreEnv and Isolated disable PYTHONSTARTUP processing.
	IgnoreEnv bool
	Isolated  bool
	// Stdin is the raw input stream; StdinFile, when set, is used for
	// terminal detection (defaults to os.Stdin).
	Stdin     io.Reader
	StdinFile *os.File
	// Stdout and Stderr receive prompt, echo, and traceback output.
	Stdout io.Writer
	Stderr io.Writer
	// StdoutFile, when set, is used for terminal and color detection
	// (defaults to os.Stdout).
	StdoutFile *os.File
}

// defaultConfig fills zero fields with the process streams.
func (c Config) withDefaults() Config {
	if c.VM == nil {
		c.VM = vm.NewVM()
	}
	if c.Stdin == nil {
		c.Stdin = os.Stdin
	}
	if c.StdinFile == nil {
		c.StdinFile = os.Stdin
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	if c.StdoutFile == nil {
		c.StdoutFile = os.Stdout
	}
	return c
}

// IsBasic reports whether the line-oriented fallback must be used.
func IsBasic(cfg Config) bool {
	cfg = cfg.withDefaults()
	stdinTTY := term.IsTerminal(int(cfg.StdinFile.Fd()))
	stdoutTTY := term.IsTerminal(int(cfg.StdoutFile.Fd()))
	return !stdinTTY || !stdoutTTY || os.Getenv("TERM") == "dumb" || os.Getenv("PYTHON_BASIC_REPL") != ""
}

// Banner returns the startup banner text (without trailing newline).
func Banner() string {
	platform := runtime.GOOS
	if m, ok := ModuleSysPlatform(); ok && m != "" {
		platform = m
	}
	return "Fython 3.15.0 (pure-Go, CGO_ENABLED=0) on " + platform + "\n" +
		`Type "help", "copyright", "credits" or "license" for more information.`
}

// ModuleSysPlatform returns the sys.platform of a host VM; used by the
// banner to mirror CPython. A small indirection so tests can stub it.
var ModuleSysPlatform = func() (string, bool) { return "", false }

// Run starts the interactive interpreter, choosing the TUI or the
// line-oriented fallback based on the environment and file descriptors.
func Run(cfg Config) error {
	cfg = cfg.withDefaults()
	if !cfg.Quiet && term.IsTerminal(int(cfg.StdinFile.Fd())) {
		_, _ = fmt.Fprintln(cfg.Stdout, Banner())
	}
	if !cfg.IgnoreEnv && !cfg.Isolated {
		if startup := os.Getenv("PYTHONSTARTUP"); startup != "" {
			runStartup(cfg, startup)
		}
	}
	if IsBasic(cfg) {
		return RunBasic(cfg)
	}
	return runRich(cfg)
}

// runStartup executes the PYTHONSTARTUP file in the __main__ namespace.
func runStartup(cfg Config, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_, _ = evalSource(cfg, string(src), path)
}

// RunBasic runs the deterministic line-oriented REPL loop. Input is read
// on demand so input() calls inside executed code share the same reader,
// and Ctrl+C interrupts the prompt rather than killing the process.
func RunBasic(cfg Config) error {
	cfg = cfg.withDefaults()
	worker := NewWorker(cfg.VM, cfg.Optimize)
	defer worker.Close()

	lr := bufio.NewReader(cfg.Stdin)
	type lineReq struct {
		reply chan string
		eofch chan bool
	}
	requests := make(chan lineReq)
	go func() {
		for req := range requests {
			line, err := lr.ReadString('\n')
			req.reply <- strings.TrimRight(line, "\r\n")
			req.eofch <- err != nil && line == ""
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	readLine := func() (string, bool, bool) {
		req := lineReq{reply: make(chan string, 1), eofch: make(chan bool, 1)}
		requests <- req
		select {
		case <-sigCh:
			return "", false, true
		case line := <-req.reply:
			return line, <-req.eofch, false
		}
	}

	worker.SetInputHook(func(prompt string) (string, error) {
		if prompt != "" {
			_, _ = fmt.Fprint(cfg.Stdout, prompt)
		}
		line, eof, _ := readLine()
		if eof {
			return "", errors.New("EOF")
		}
		return line, nil
	})

	stdinTTY := term.IsTerminal(int(cfg.StdinFile.Fd()))
	var buf strings.Builder
	more := false
	prompt := func() {
		if !stdinTTY {
			return
		}
		if more {
			_, _ = fmt.Fprint(cfg.Stdout, "... ")
		} else {
			_, _ = fmt.Fprint(cfg.Stdout, ">>> ")
		}
	}
	prompt()

	for {
		line, eof, interrupted := readLine()
		if interrupted {
			buf.Reset()
			more = false
			_, _ = fmt.Fprintln(cfg.Stdout, "\nKeyboardInterrupt")
			prompt()
			continue
		}
		if eof {
			if buf.Len() > 0 {
				if err := execStatement(cfg, worker, buf.String()); err != nil {
					return err
				}
			}
			return nil
		}
		if !more {
			if bare := bareCommand(line); bare != "" {
				act, err := execBareCommand(cfg, bare)
				if err != nil {
					return err
				}
				if act {
					return nil
				}
				prompt()
				continue
			}
		}
		if buf.Len() == 0 {
			buf.WriteString(line)
		} else {
			buf.WriteString("\n")
			buf.WriteString(line)
		}
		src := buf.String()
		if compiler.IncompleteInput([]byte(src)) {
			more = true
			prompt()
			continue
		}
		buf.Reset()
		more = false
		if err := execStatement(cfg, worker, src); err != nil {
			return err
		}
		prompt()
	}
}

// bareCommand returns the bare command name for a line that is exactly
// "exit", "quit", or "clear" (plus surrounding whitespace), else "".
func bareCommand(line string) string {
	switch strings.TrimSpace(line) {
	case "exit", "quit", "clear":
		return strings.TrimSpace(line)
	default:
		return ""
	}
}

// execBareCommand handles a bare exit/quit/clear. It reports whether the
// REPL should terminate.
func execBareCommand(cfg Config, cmd string) (bool, error) {
	switch cmd {
	case "exit", "quit":
		return true, nil
	case "clear":
		_, _ = fmt.Fprint(cfg.Stdout, "\x1b[H\x1b[2J")
	}
	return false, nil
}

// execStatement compiles and runs one complete statement in "single" mode,
// printing tracebacks to Stderr and threading SystemExit as ExitError.
func execStatement(cfg Config, worker *Worker, src string) error {
	res := worker.Execute(src, "<stdin>")
	_, _ = fmt.Fprint(cfg.Stdout, res.Output)
	_, _ = fmt.Fprint(cfg.Stderr, colorizeTraceback(cfg, res.Traceback))
	if res.Exit != nil {
		return res.Exit
	}
	return nil
}

// evalSource runs a complete source text (used for PYTHONSTARTUP) in
// "exec" mode and reports the first error it encounters.
func evalSource(cfg Config, src, filename string) (string, error) {
	worker := NewWorker(cfg.VM, cfg.Optimize)
	return worker.ExecuteSource(src, filename).Output, nil
}

// exitFromException converts an unhandled VM exception into an ExitError
// when it is SystemExit, and nil otherwise.
func exitFromException(v *vm.VM) *ExitError {
	exc := v.UnhandledException()
	if exc.IsNone() || exc.HeaderPtr() == nil {
		return nil
	}
	if exc.IsInstance() && val.IsSubclass(exc.Type(), val.SystemExitType) {
		code, _ := exc.AsInstance().GetAttr(val.InternString("code"))
		switch {
		case code.IsNone():
			return &ExitError{}
		case code.IsInt64():
			return &ExitError{Code: int(code.AsInt64())}
		default:
			return &ExitError{Code: 1, Message: val.PyStr(nil, code)}
		}
	}
	return nil
}

// ScannedLines returns the prompt string for the given continuation state.
func psString(more bool) string {
	if more {
		return "... "
	}
	return ">>> "
}
