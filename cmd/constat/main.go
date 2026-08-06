package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/report"
	"github.com/mxmchrbrt/constat/internal/run"
	"github.com/mxmchrbrt/constat/internal/signing"
	"github.com/mxmchrbrt/constat/internal/webhook"
)

// version is the release this binary was built from, recorded in every
// report. Set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Subcommand dispatch before flag parsing, so the original
	// `constat [flags] <config.yaml>` form keeps working unchanged.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "keygen":
			os.Exit(keygen(os.Args[2:]))
		case "verify":
			os.Exit(verify(os.Args[2:]))
		}
	}

	reportPath := flag.String("report", "", "write a JSON report to this path (- for stdout)")
	htmlPath := flag.String("html", "", "write an HTML report to this path (- for stdout)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: constat [flags] <config.yaml>")
		fmt.Fprintln(os.Stderr, "       constat keygen -out <path>")
		fmt.Fprintln(os.Stderr, "       constat verify -key <public-key> <report.json>")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	cfg, err := config.Load(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// On SIGINT/SIGTERM, cancel the context so the in-flight restore stops
	// and its deferred cleanup runs. os.Exit would skip those defers and
	// leave scratch directories behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Loaded before any verification runs: an hour of restores followed by
	// discovering the key is missing loses the run's evidence with nothing
	// to re-sign it from. A configured-but-unusable key is fatal rather
	// than a silent fallback to an unsigned report.
	var signer report.Signer
	if cfg.Signing != nil {
		s, err := signing.LoadSigner(cfg.Signing.KeyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		signer = s
	}

	// One timestamp for the whole run: every target should carry the same
	// "as of" rather than reading the clock per target.
	startedAt := time.Now()

	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}

	var targets []report.Target
	interrupted := false

	for _, t := range cfg.Targets {
		targets = append(targets, run.Target(ctx, t))
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "interrupted")
			interrupted = true
			break
		}
	}

	rep := report.New(startedAt, host, version, targets)

	if *reportPath != "" {
		if err := writeReport(*reportPath, rep, signer); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	if *htmlPath != "" {
		if err := writeHTML(*htmlPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	// Fires on pass and on fail (taxonomy #1: an alert that only fires on
	// failure can't distinguish "healthy" from "not running"). A failing
	// webhook is reported and never changes the exit code.
	if cfg.Webhook != nil {
		if err := sendWebhook(ctx, *cfg.Webhook, rep); err != nil {
			fmt.Fprintf(os.Stderr, "webhook: %v\n", err)
		}
	}

	if interrupted || rep.Verdict.Failed() {
		os.Exit(1)
	}
}

func sendWebhook(ctx context.Context, cfg config.Webhook, rep report.Report) error {
	format := webhook.Format(cfg.Format)
	switch format {
	case "":
		format = webhook.Generic
	case webhook.Generic, webhook.Ntfy, webhook.Healthchecks, webhook.Discord:
		// known
	default:
		return fmt.Errorf("unknown webhook format %q", cfg.Format)
	}

	return webhook.Send(ctx, webhook.Config{
		URL:         cfg.URL,
		Format:      format,
		Timeout:     cfg.Timeout,
		MaxAttempts: cfg.MaxAttempts,
	}, rep)
}

// writeReport writes to a path, or to stdout when the path is "-".
//
// The file is created 0600. A report names repositories and hosts, and while it
// carries no secret by construction, it is still a map of what this machine
// backs up and where — not something to leave world-readable by default.
func writeReport(path string, rep report.Report, signer report.Signer) error {
	if path == "-" {
		return report.Write(os.Stdout, rep, signer)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating report file: %w", err)
	}
	defer f.Close()

	if err := report.Write(f, rep, signer); err != nil {
		return err
	}
	return f.Close()
}

// writeHTML mirrors writeReport for the HTML rendering, same permissions and
// same stdout convention.
func writeHTML(path string, rep report.Report) error {
	if path == "-" {
		return report.HTML(os.Stdout, rep)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating HTML report file: %w", err)
	}
	defer f.Close()

	if err := report.HTML(f, rep); err != nil {
		return err
	}
	return f.Close()
}
