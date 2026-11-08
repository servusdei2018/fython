package main

import (
	"errors"
	"os"
	"runtime/debug"

	"github.com/servusdei2018/fython/internal/cli"
)

func main() {
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(500)
	}
	if cli.IsBundledExecutable() {
		err := cli.RunBundledExecutable()
		if err != nil {
			var exit *cli.ErrExit
			if errors.As(err, &exit) {
				if exit.Message != "" {
					os.Stderr.WriteString(exit.Message + "\n")
				}
				os.Exit(exit.Code)
			}
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "build" {
		err := cli.RunBuild(os.Args[2:])
		if err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "serve" {
		err := cli.RunServe(os.Args[2:])
		if err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
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
		var exit *cli.ErrExit
		if errors.As(err, &exit) {
			if exit.Message != "" {
				os.Stderr.WriteString(exit.Message + "\n")
			}
			os.Exit(exit.Code)
		}
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
