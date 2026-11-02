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
	ctx, err := cli.ParseWith(parser, os.Args[1:])
	parser.FatalIfErrorf(err)
	err = ctx.Run()
	ctx.FatalIfErrorf(err)
}
