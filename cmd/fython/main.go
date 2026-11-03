package main

import (
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
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
