package cli

import (
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// Version is the reported Fython compatibility version string.
const Version = "Fython 3.14.0"

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
	Command     *string          `short:"c" name:"cmd" xor:"target" placeholder:"cmd" help:"Program passed in as string (terminates option list)."`
	Module      *string          `short:"m" name:"mod" xor:"target" placeholder:"mod" help:"Run library module as a script (terminates option list)."`
	Interactive bool             `short:"i" name:"interactive" help:"Inspect interactively after running script."`
	Quiet       bool             `short:"q" name:"quiet" help:"Don't display version and copyright messages on interactive startup."`
	Version     kong.VersionFlag `short:"V" name:"version" help:"Print the Python version number and exit."`

	Args []string `arg:"" optional:"" name:"arg" help:"Program file and arguments (or arguments to -c/-m)."`
}

// Invocation holds the resolved execution target and sys.argv slice.
type Invocation struct {
	Mode        Mode
	Command     string
	Module      string
	Script      string
	Argv        []string
	Interactive bool
	Quiet       bool
}

// Resolve computes the effective CPython invocation mode and sys.argv slice from parsed CLI flags.
func (c *CLI) Resolve() Invocation {
	inv := Invocation{
		Interactive: c.Interactive,
		Quiet:       c.Quiet,
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

// Run executes the resolved CLI invocation.
func (c *CLI) Run() error {
	inv := c.Resolve()
	switch inv.Mode {
	case ModeCommand:
		if inv.Command != "" {
			co, err := compiler.CompileString(inv.Command)
			if err != nil {
				return err
			}
			v := vm.NewVM()
			v.SetGlobal("__name__", val.NewStringVal("__main__"))
			v.RunCode(co)
		}
		return nil
	case ModeModule:
		if inv.Module != "" {
			v := vm.NewVM()
			if _, err := v.ImportModule(inv.Module, 0, true, nil); err != nil {
				if !strings.HasPrefix(err.Error(), "ModuleNotFoundError:") {
					return err
				}
			}
		}
		return nil
	case ModeScript:
		if inv.Script != "" {
			if _, err := os.Stat(inv.Script); err == nil {
				src, err := os.ReadFile(inv.Script)
				if err != nil {
					return err
				}
				co, err := compiler.CompileSource(src)
				if err != nil {
					return err
				}
				v := vm.NewVM()
				v.SetGlobal("__name__", val.NewStringVal("__main__"))
				v.SetGlobal("__file__", val.NewStringVal(inv.Script))
				v.RunCode(co)
			}
		}
		return nil
	default:
		return nil
	}
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
		if strings.HasPrefix(arg, "--") {
			continue
		}

		// Short flag cluster (e.g. -c, -m, -ic, -c"print(1)")
		shorts := arg[1:]
		for j := 0; j < len(shorts); j++ {
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
