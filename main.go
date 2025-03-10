package main

import (
	"context"
	"os"

	"github.com/m-mizutani/backstream/pkg/cli"
)

func main() {
	if err := cli.Run(context.Background(), os.Args); err != nil {
		os.Exit(1)
	}
}
