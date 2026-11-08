package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
)

// BuildCLI defines the command-line grammar for `fython build`.
type BuildCLI struct {
	Entry    string   `arg:"" placeholder:"<entry.py | package_dir>" help:"Entry point Python script or package directory."`
	Output   string   `short:"o" name:"output" placeholder:"<output_binary>" help:"Output standalone binary path."`
	Wheels   []string `name:"wheel" placeholder:"<path.whl>" help:"Wheel archives (.whl) to bundle."`
	Includes []string `name:"include" placeholder:"<dir>" help:"Additional directories to bundle."`
	Optimize int      `short:"O" name:"optimize" type:"counter" help:"Optimization level (removes assert statements)."`
}

// RunBuild parses arguments and runs the build command.
func RunBuild(args []string) error {
	var b BuildCLI
	parser, err := kong.New(
		&b,
		kong.Name("fython build"),
		kong.Description("Compile a Python application and its wheel dependencies into a standalone native binary."),
	)
	if err != nil {
		return err
	}
	_, err = parser.Parse(args)
	if err != nil {
		return err
	}

	opts := BuildOptions{
		Entry:    b.Entry,
		Output:   b.Output,
		Wheels:   b.Wheels,
		Includes: b.Includes,
		Optimize: b.Optimize,
	}

	if err := BuildStandaloneBinary(opts); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	outPath := opts.Output
	if outPath == "" {
		base := filepath.Base(opts.Entry)
		outPath = strings.TrimSuffix(base, ".py")
	}
	fmt.Fprintf(os.Stderr, "Successfully built standalone binary %s\n", outPath)
	return nil
}
