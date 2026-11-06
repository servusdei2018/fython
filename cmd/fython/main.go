package main

import (
	"errors"
	"os"

	"github.com/servusdei2018/fython/internal/cli"
)

func main() {
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
