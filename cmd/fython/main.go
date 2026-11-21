package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"

	"github.com/servusdei2018/fython/internal/cli"
	"github.com/servusdei2018/fython/pkg/vm"
)

func main() {
	os.Exit(run())
}

func run() int {
	if os.Getenv("FYTHON_ICSTATS") != "" {
		defer vm.DumpICStats(os.Stderr)
	}
	if os.Getenv("FYTHON_OPSTATS") != "" {
		defer vm.DumpOpStats(os.Stderr)
		defer vm.DumpRegStats(os.Stderr)
	}
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(500)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(256 << 20)
	}
	if path := os.Getenv("FYTHON_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fython: cpuprofile: %v\n", err)
			return 1
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "fython: cpuprofile: %v\n", err)
			return 1
		}
		defer func() {
			pprof.StopCPUProfile()
			_ = f.Close()
		}()
	}
	if path := os.Getenv("FYTHON_MEMPROFILE"); path != "" {
		defer func() {
			runtime.GC()
			f, err := os.Create(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "fython: memprofile: %v\n", err)
				return
			}
			_ = pprof.WriteHeapProfile(f)
			_ = f.Close()
		}()
	}
	if cli.IsBundledExecutable() {
		err := cli.RunBundledExecutable()
		if err != nil {
			if exit, ok := errors.AsType[*cli.ErrExit](err); ok {
				if exit.Message != "" {
					fmt.Fprintln(os.Stderr, exit.Message)
				}
				return exit.Code
			}
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	}

	if len(os.Args) > 1 && os.Args[1] == "build" {
		err := cli.RunBuild(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	}

	if len(os.Args) > 1 && os.Args[1] == "serve" {
		err := cli.RunServe(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	}

	var c cli.CLI
	parser, err := cli.NewParser(&c)
	if err != nil {
		panic(err)
	}
	_, err = cli.ParseWith(parser, os.Args[1:])
	parser.FatalIfErrorf(err)
	err = c.Run()
	if err != nil {
		if exit, ok := errors.AsType[*cli.ErrExit](err); ok {
			if exit.Message != "" {
				fmt.Fprintln(os.Stderr, exit.Message)
			}
			return exit.Code
		}
		if uncaught, ok := errors.AsType[*cli.ErrUncaughtException](err); ok {
			// A user-installed sys.excepthook already reported the failure.
			if !uncaught.Reported {
				fmt.Fprintln(os.Stderr, uncaught.Error())
			}
			return 1
		}
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	return 0
}
