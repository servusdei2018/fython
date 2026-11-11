package cli

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/servusdei2018/fython/internal/repl"
	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// Version is the reported Fython compatibility version string.
const Version = "Fython 3.15.0"

// Mode represents the resolved CPython invocation mode.
type Mode int

const (
	ModeREPL Mode = iota
	ModeCommand
	ModeModule
	ModeScript
	ModeStdin
)

// String returns a human-readable name for the invocation mode.
func (m Mode) String() string {
	switch m {
	case ModeREPL:
		return "repl"
	case ModeCommand:
		return "command"
	case ModeModule:
		return "module"
	case ModeScript:
		return "script"
	case ModeStdin:
		return "stdin"
	default:
		return "unknown"
	}
}

// CLI defines the Kong command-line grammar for fython.
type CLI struct {
	Command           *string          `short:"c" name:"cmd" xor:"target" placeholder:"cmd" help:"Program passed in as string (terminates option list)."`
	Module            *string          `short:"m" name:"mod" xor:"target" placeholder:"mod" help:"Run library module as a script (terminates option list)."`
	Interactive       bool             `short:"i" name:"interactive" help:"Inspect interactively after running script."`
	Quiet             bool             `short:"q" name:"quiet" help:"Don't display version and copyright messages on interactive startup."`
	DontWriteBytecode bool             `short:"B" name:"dont-write-bytecode" help:"Don't write .pyc/.fyc files on import."`
	Unbuffered        bool             `short:"u" name:"unbuffered" help:"Force the stdout and stderr streams to be unbuffered."`
	IgnoreEnv         bool             `short:"E" name:"ignore-environment" help:"Ignore PYTHON* environment variables (such as PYTHONPATH)."`
	NoUserSite        bool             `short:"s" name:"no-user-site" help:"Don't add user site directory to sys.path."`
	NoSite            bool             `short:"S" name:"no-site" help:"Don't imply 'import site' on initialization."`
	Isolated          bool             `short:"I" name:"isolated" help:"Isolate Python from the user's environment (implies -E and -s)."`
	Optimize          int              `short:"O" name:"optimize" type:"counter" help:"Remove assert and __debug__-dependent statements; -OO also strips docstrings."`
	WarnOptions       []string         `short:"W" name:"warn" placeholder:"arg" help:"Warning control (arg is action:message:category:module:lineno)."`
	XOptions          []string         `short:"X" name:"xopt" placeholder:"opt" help:"Set implementation-specific option."`
	Version           kong.VersionFlag `short:"V" name:"version" help:"Print the Python version number and exit."`

	Args []string `arg:"" optional:"" name:"arg" help:"Program file and arguments (or arguments to -c/-m)."`
}

// Invocation holds the resolved execution target and sys.argv slice.
type Invocation struct {
	Mode              Mode
	Command           string
	Module            string
	Script            string
	Argv              []string
	Interactive       bool
	Quiet             bool
	DontWriteBytecode bool
	Unbuffered        bool
	IgnoreEnv         bool
	NoUserSite        bool
	NoSite            bool
	Isolated          bool
	Optimize          int
	WarnOptions       []string
	XOptions          map[string]string
}

// Resolve computes the effective CPython invocation mode and sys.argv slice from parsed CLI flags.
func (c *CLI) Resolve() Invocation {
	var xopts map[string]string
	if len(c.XOptions) > 0 {
		xopts = make(map[string]string, len(c.XOptions))
		for _, raw := range c.XOptions {
			if k, v, ok := strings.Cut(raw, "="); ok {
				xopts[k] = v
			} else {
				xopts[raw] = ""
			}
		}
	}

	inv := Invocation{
		Interactive:       c.Interactive,
		Quiet:             c.Quiet,
		DontWriteBytecode: c.DontWriteBytecode,
		Unbuffered:        c.Unbuffered,
		IgnoreEnv:         c.IgnoreEnv || c.Isolated,
		NoUserSite:        c.NoUserSite || c.Isolated,
		NoSite:            c.NoSite,
		Isolated:          c.Isolated,
		Optimize:          c.Optimize,
		WarnOptions:       append([]string(nil), c.WarnOptions...),
		XOptions:          xopts,
	}

	switch {
	case c.Command != nil:
		inv.Mode = ModeCommand
		inv.Command = *c.Command
		inv.Argv = append([]string{"-c"}, c.Args...)
	case c.Module != nil:
		inv.Mode = ModeModule
		inv.Module = *c.Module
		inv.Argv = append([]string{*c.Module}, c.Args...)
	case len(c.Args) > 0 && c.Args[0] == "-":
		inv.Mode = ModeStdin
		inv.Argv = append([]string(nil), c.Args...)
	case len(c.Args) > 0:
		inv.Mode = ModeScript
		inv.Script = c.Args[0]
		inv.Argv = append([]string(nil), c.Args...)
	default:
		inv.Mode = ModeREPL
		inv.Argv = []string{""}
	}

	return inv
}

func (inv Invocation) toRuntimeConfig() vm.RuntimeConfig {
	return vm.RuntimeConfig{
		Argv:              inv.Argv,
		OrigArgv:          append([]string(nil), os.Args...),
		Optimize:          inv.Optimize,
		DontWriteBytecode: inv.DontWriteBytecode,
		Unbuffered:        inv.Unbuffered,
		IgnoreEnv:         inv.IgnoreEnv,
		NoUserSite:        inv.NoUserSite,
		NoSite:            inv.NoSite,
		Isolated:          inv.Isolated,
		Interactive:       inv.Interactive,
		Quiet:             inv.Quiet,
		WarnOptions:       inv.WarnOptions,
		XOptions:          inv.XOptions,
	}
}

func isZipArchiveFile(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 4 {
		return false
	}
	_, err = zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	return err == nil
}

// Run executes the resolved CLI invocation.
func (c *CLI) Run() error {
	inv := c.Resolve()
	rtCfg := inv.toRuntimeConfig()
	eff := rtCfg.Effective()

	prevOpt := compiler.DefaultOptimizeLevel()
	compiler.SetDefaultOptimizeLevel(eff.Optimize)
	defer compiler.SetDefaultOptimizeLevel(prevOpt)

	eng := vm.DefaultEngine()
	prevCfg := eng.RuntimeConfig()
	eng.SetRuntimeConfig(rtCfg)
	defer eng.SetRuntimeConfig(prevCfg)

	var v *vm.VM
	var execErr error

	switch inv.Mode {
	case ModeCommand:
		if inv.Command != "" {
			co, err := compiler.CompileSourceModeWithOptimize([]byte(inv.Command), "<string>", compiler.ModeExec, eff.Optimize)
			if err != nil {
				return err
			}
			v = eng.NewVM()
			v.SetGlobal("__name__", val.NewStringVal("__main__"))
			v.RunCode(co)
			execErr = unhandledError(v)
		}
	case ModeModule:
		if inv.Module != "" {
			v = eng.NewVM()
			if !eff.Isolated {
				if cwd, err := os.Getwd(); err == nil {
					prependSysPath0(v, cwd)
				}
			}
			stmt := fmt.Sprintf("import runpy\nrunpy._run_module_as_main(%q, True)\n", inv.Module)
			co, err := compiler.CompileSourceModeWithOptimize([]byte(stmt), "<runpy>", compiler.ModeExec, eff.Optimize)
			if err != nil {
				return err
			}
			v.RunCode(co)
			execErr = unhandledError(v)
		}
	case ModeStdin:
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		if len(src) > 0 {
			co, err := compiler.CompileSourceModeWithOptimize(src, "<stdin>", compiler.ModeExec, eff.Optimize)
			if err != nil {
				return err
			}
			v = eng.NewVM()
			v.SetGlobal("__name__", val.NewStringVal("__main__"))
			v.SetGlobal("__file__", val.NewStringVal("<stdin>"))
			v.RunCode(co)
			execErr = unhandledError(v)
		}
	case ModeScript:
		if inv.Script != "" {
			info, err := os.Stat(inv.Script)
			if err != nil {
				return fmt.Errorf("fython: can't open file %q: %v", inv.Script, err)
			}
			if info.IsDir() || isZipArchiveFile(inv.Script) {
				v = eng.NewVM()
				prependSysPath0(v, inv.Script)
				stmt := "import runpy\nrunpy._run_module_as_main('__main__', False)\n"
				co, err := compiler.CompileSourceModeWithOptimize([]byte(stmt), "<runpy>", compiler.ModeExec, eff.Optimize)
				if err != nil {
					return err
				}
				v.RunCode(co)
				execErr = unhandledError(v)
			} else {
				src, err := os.ReadFile(inv.Script)
				if err != nil {
					return err
				}
				co, err := compiler.CompileSourceModeWithOptimize(src, inv.Script, compiler.ModeExec, eff.Optimize)
				if err != nil {
					return err
				}
				v = eng.NewVM()
				if !eff.Isolated {
					scriptDir := filepath.Dir(inv.Script)
					if absDir, err := filepath.Abs(scriptDir); err == nil {
						scriptDir = absDir
					}
					prependSysPath0(v, scriptDir)
				}
				v.SetGlobal("__name__", val.NewStringVal("__main__"))
				v.SetGlobal("__file__", val.NewStringVal(inv.Script))
				v.RunCode(co)
				execErr = unhandledError(v)
			}
		}
	case ModeREPL:
		if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
			data, _ := io.ReadAll(os.Stdin)
			if len(strings.TrimSpace(string(data))) > 0 {
				v = eng.NewVM()
				return startREPL(v, bytes.NewReader(data), nil, eff)
			}
		}
		return startREPL(v, os.Stdin, os.Stdin, eff)
	}

	if eff.Interactive && v != nil {
		if uncaught, ok := execErr.(*ErrUncaughtException); ok {
			if !uncaught.Reported {
				_, _ = fmt.Fprintln(os.Stderr, uncaught.Error())
			}
			v.ClearUnhandledException()
			execErr = nil
		}
		if execErr == nil {
			return startREPL(v, os.Stdin, os.Stdin, eff)
		}
	}

	return execErr
}

// startREPL launches the interactive interpreter (TUI or line fallback)
// over the given streams, preserving the VM namespace.
func startREPL(v *vm.VM, stdin io.Reader, stdinFile *os.File, eff vm.RuntimeConfig) error {
	cfg := repl.Config{
		VM:        v,
		Optimize:  eff.Optimize,
		Quiet:     eff.Quiet,
		IgnoreEnv: eff.IgnoreEnv,
		Isolated:  eff.Isolated,
		Stdin:     stdin,
		StdinFile: stdinFile,
	}
	if err := repl.Run(cfg); err != nil {
		if exit, ok := err.(*repl.ExitError); ok {
			return &ErrExit{Code: exit.Code, Message: exit.Message}
		}
		return err
	}
	return nil
}

func prependSysPath0(v *vm.VM, dir string) {
	sysModVal, ok := v.GetModule("sys")
	if !ok || !sysModVal.IsModule() {
		return
	}
	pathVal, ok := sysModVal.AsModule().Dict.Get(val.InternString("path"))
	if !ok || !pathVal.IsList() {
		return
	}
	list := pathVal.AsList()
	if len(list.Items) > 0 && list.Items[0].IsAnyString() && list.Items[0].AsString() == "." {
		list.Items[0] = val.NewStringVal(dir)
		return
	}
	list.Items = append([]val.Value{val.NewStringVal(dir)}, list.Items...)
}

// ErrUncaughtException signals that the program terminated with an uncaught Python exception.
type ErrUncaughtException struct {
	Traceback string
	// Reported is set when the traceback was already written to stderr by a
	// user-installed sys.excepthook, so it must not be printed a second time.
	Reported bool
}

func (e *ErrUncaughtException) Error() string { return strings.TrimRight(e.Traceback, "\n") }

// ErrExit reports that the program ended with SystemExit.
type ErrExit struct {
	Code    int
	Message string
}

func (e *ErrExit) Error() string { return e.Message }

func unhandledError(v *vm.VM) error {
	exc := v.UnhandledException()
	if exc.IsNone() || exc.HeaderPtr() == nil {
		return nil
	}
	if exc.IsInstance() && val.IsSubclass(exc.Type(), val.SystemExitType) {
		code, _ := exc.AsInstance().GetAttr(val.InternString("code"))
		switch {
		case code.IsNone():
			return &ErrExit{}
		case code.IsInt64():
			return &ErrExit{Code: int(code.AsInt64())}
		default:
			return &ErrExit{Code: 1, Message: val.PyStr(nil, code)}
		}
	}
	// A user-installed sys.excepthook takes over the reporting.
	if v.CallUserExcepthook(exc) {
		return &ErrUncaughtException{Traceback: v.FormatException(exc), Reported: true}
	}
	return &ErrUncaughtException{Traceback: v.FormatException(exc)}
}

// NewParser constructs a configured Kong parser for CLI.
func NewParser(cli *CLI, opts ...kong.Option) (*kong.Kong, error) {
	defaultOpts := []kong.Option{
		kong.Name("fython"),
		kong.Description("A CPython compatible interpreter in Go."),
		kong.Vars{"version": Version},
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
	}
	defaultOpts = append(defaultOpts, opts...)
	return kong.New(cli, defaultOpts...)
}

// ParseWith normalizes args for CPython option-list termination and parses them with parser.
func ParseWith(parser *kong.Kong, args []string) (*kong.Context, error) {
	return parser.Parse(normalizeArgs(args))
}

// Parse creates a Kong parser and parses args into a new CLI struct.
func Parse(args []string, opts ...kong.Option) (*CLI, *kong.Context, error) {
	var cli CLI
	parser, err := NewParser(&cli, opts...)
	if err != nil {
		return nil, nil, err
	}
	ctx, err := ParseWith(parser, args)
	if err != nil {
		return nil, nil, err
	}
	return &cli, ctx, nil
}

// normalizeArgs terminates the interpreter option list at the first execution target
// (-c <cmd>, -m <mod>, script file, or "-" for stdin) by inserting "--" so trailing
// arguments are captured verbatim in CLI.Args, while still allowing Kong to validate
// interpreter flags and xor constraints prior to the target.
func normalizeArgs(args []string) []string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return args
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			// Positional script or stdin argument reached; terminate interpreter options before it.
			return terminateAt(args, i)
		}

		// Long flags --cmd or --mod
		if arg == "--cmd" || arg == "--mod" {
			if i+1 < len(args) {
				return terminateAt(args, i+2)
			}
			return args
		}
		if strings.HasPrefix(arg, "--cmd=") || strings.HasPrefix(arg, "--mod=") {
			return terminateAt(args, i+1)
		}
		if arg == "--warn" || arg == "--xopt" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}

		// Short flag cluster (e.g. -c, -m, -ic, -c"print(1)", -Wignore, -W ignore, -Xdev)
		shorts := arg[1:]
		for j := range len(shorts) {
			ch := shorts[j]
			if ch == 'c' || ch == 'm' {
				if j+1 < len(shorts) {
					// Value is attached to the short flag, e.g. -c"code"
					return terminateAt(args, i+1)
				}
				if i+1 < len(args) {
					return terminateAt(args, i+2)
				}
				return args
			}
			if ch == 'W' || ch == 'X' {
				if j+1 < len(shorts) {
					// Attached value e.g. -Wignore or -Xutf8
					break
				}
				// Separate argument e.g. -W ignore or -X utf8
				i++
				break
			}
		}
	}
	return args
}

func terminateAt(args []string, idx int) []string {
	if idx >= len(args) {
		return args
	}
	// If the immediate next token is the mutually exclusive target flag (-c/-m/--cmd/--mod),
	// let Kong's xor:"target" validation surface the conflict error.
	next := args[idx]
	if next == "-c" || next == "-m" || next == "--cmd" || next == "--mod" ||
		strings.HasPrefix(next, "--cmd=") || strings.HasPrefix(next, "--mod=") {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, args[:idx]...)
	out = append(out, "--")
	out = append(out, args[idx:]...)
	return out
}
