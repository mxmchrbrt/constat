package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/run"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: constat <config.yaml>")
		os.Exit(1)
	}

	cfg, err := config.Load(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	failed := false

	for _, t := range cfg.Targets {
		if !run.Target(ctx, t) {
			failed = true
		}
	}

	if failed {
		os.Exit(1)
	}
}
