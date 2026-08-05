package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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

	// On SIGINT/SIGTERM, cancel the context so the in-flight restore stops
	// and its deferred cleanup runs. os.Exit would skip those defers and
	// leave scratch directories behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	failed := false

	for _, t := range cfg.Targets {
		if !run.Target(ctx, t) {
			failed = true
		}
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "interrupted")
			failed = true
			break
		}
	}

	if failed {
		os.Exit(1)
	}
}
