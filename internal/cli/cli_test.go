package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

func TestParseAndResolve(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    Invocation
		wantErr bool
	}{
		{
			name: "default REPL mode",
			args: nil,
			want: Invocation{
				Mode: ModeREPL,
				Argv: []string{""},
			},
		},
		{
			name: "interactive and quiet flags",
			args: []string{"-i", "-q"},
			want: Invocation{
				Mode:        ModeREPL,
				Argv:        []string{""},
				Interactive: true,
				Quiet:       true,
			},
		},
		{
			name: "combined short flags -iq",
			args: []string{"-iq"},
			want: Invocation{
				Mode:        ModeREPL,
				Argv:        []string{""},
				Interactive: true,
				Quiet:       true,
			},
		},
		{
			name: "command mode with trailing flags and args",
			args: []string{"-c", "print(1)", "--foo", "-b"},
			want: Invocation{
				Mode:    ModeCommand,
				Command: "print(1)",
				Argv:    []string{"-c", "--foo", "-b"},
			},
		},
		{
			name: "interactive combined with command mode -ic",
			args: []string{"-ic", "x = 1", "arg1"},
			want: Invocation{
				Mode:        ModeCommand,
				Command:     "x = 1",
				Argv:        []string{"-c", "arg1"},
				Interactive: true,
			},
		},
		{
			name: "module mode with trailing flags and args",
			args: []string{"-m", "http.server", "8000", "--bind", "127.0.0.1"},
			want: Invocation{
				Mode:   ModeModule,
				Module: "http.server",
				Argv:   []string{"http.server", "8000", "--bind", "127.0.0.1"},
			},
		},
		{
			name: "script mode with passthrough flags",
			args: []string{"script.py", "--flag", "arg1"},
			want: Invocation{
				Mode:   ModeScript,
				Script: "script.py",
				Argv:   []string{"script.py", "--flag", "arg1"},
			},
		},
		{
			name: "stdin mode with args",
			args: []string{"-", "arg1", "arg2"},
			want: Invocation{
				Mode: ModeStdin,
				Argv: []string{"-", "arg1", "arg2"},
			},
		},
		{
			name: "runtime flags -OO -B -u -E -s -S -W -X with command",
			args: []string{"-OO", "-B", "-u", "-E", "-s", "-S", "-W", "ignore", "-X", "dev=1", "-X", "utf8", "-c", "x = 1"},
			want: Invocation{
				Mode:              ModeCommand,
				Command:           "x = 1",
				Argv:              []string{"-c"},
				DontWriteBytecode: true,
				Unbuffered:        true,
				IgnoreEnv:         true,
				NoUserSite:        true,
				NoSite:            true,
				Optimize:          2,
				WarnOptions:       []string{"ignore"},
				XOptions:          map[string]string{"dev": "1", "utf8": ""},
			},
		},
		{
			name: "isolated flag -I implies -E and -s",
			args: []string{"-I", "-c", "x = 2"},
			want: Invocation{
				Mode:       ModeCommand,
				Command:    "x = 2",
				Argv:       []string{"-c"},
				Isolated:   true,
				IgnoreEnv:  true,
				NoUserSite: true,
			},
		},
		{
			name:    "mutual exclusion error combining -c and -m",
			args:    []string{"-c", "print(1)", "-m", "mod"},
			wantErr: true,
		},
		{
			name:    "unknown flag error before target",
			args:    []string{"--unknown-flag"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, ctx, err := Parse(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for args %v, got nil", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for args %v: %v", tt.args, err)
			}

			got := parsed.Resolve()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Resolve() = %+v, want %+v", got, tt.want)
			}

			if got.Mode == ModeCommand && !got.Interactive {
				if err := ctx.Run(); err != nil {
					t.Errorf("ctx.Run() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestVersionAndHelpFlags(t *testing.T) {
	t.Run("version flag -V", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var exited bool
		var exitCode int

		_, _, err := Parse(
			[]string{"-V"},
			kong.Writers(&stdout, &stderr),
			kong.Exit(func(code int) {
				exited = true
				exitCode = code
			}),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exited || exitCode != 0 {
			t.Errorf("expected exit(0), got exited=%v code=%d", exited, exitCode)
		}
		if got := strings.TrimSpace(stdout.String()); got != Version {
			t.Errorf("stdout = %q, want %q", got, Version)
		}
	})

	t.Run("help flag -h", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var exited bool
		var exitCode int

		_, _, _ = Parse(
			[]string{"-h"},
			kong.Writers(&stdout, &stderr),
			kong.Exit(func(code int) {
				exited = true
				exitCode = code
			}),
		)
		if !exited || exitCode != 0 {
			t.Errorf("expected exit(0), got exited=%v code=%d", exited, exitCode)
		}
		if !strings.Contains(stdout.String(), "Usage: fython") {
			t.Errorf("expected help output to contain 'Usage: fython', got:\n%s", stdout.String())
		}
	})
}
