package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Snapshot — kept minimal per ARCHITECTURE.md: a time, an identifier.
// Richer drivers expose extras separately, not here.
type Snapshot struct {
	ID   string
	Time time.Time
}

type Driver interface {
	Latest(ctx context.Context) (*Snapshot, error)

	// Restore writes the latest snapshot into dest. paths, when non-empty,
	// restricts what is restored (restic --include semantics: a prefix match
	// against paths inside the snapshot). An empty paths restores everything.
	Restore(ctx context.Context, dest string, paths []string) error
}

// ResticDriver implements Driver against a restic repository.
type ResticDriver struct {
	Repo         string
	PasswordFile string
}

func NewResticDriver(repo, passwordFile string) *ResticDriver {
	return &ResticDriver{Repo: repo, PasswordFile: passwordFile}
}

// noCache is passed to every restic invocation, and it is a correctness
// requirement rather than a tuning knob.
//
// restic caches tree and index packs locally. With a warm cache, a repository
// whose tree pack has been truncated still restores cleanly and reports
// success, because the trees are read from ~/.cache/restic and the damaged pack
// is never touched. Measured, not assumed: it is what the truncated-repo
// fixture found, and the tool said PASS. Data packs are not cached, so
// corruption there is caught either way — but half the corruption being
// invisible is not a usable guarantee.
//
// constat is documented as running on the customer's own machine, which is
// usually the machine that took the backup, so the cache is warm exactly where
// it does the most damage. Verifying the cache instead of the repository would
// make failure mode #4 invisible, and #4 is one of the modes this tool exists
// for.
//
// The cost is re-reading the index on every invocation, which is real for a
// large remote repository. That is the right trade: a verification that might
// be reading a cache is not a verification.
const noCache = "--no-cache"

func (d *ResticDriver) Latest(ctx context.Context) (*Snapshot, error) {
	cmd := exec.CommandContext(ctx, "restic",
		"-r", d.Repo,
		"--password-file", d.PasswordFile,
		noCache,
		"--json",
		"snapshots",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("restic snapshots: %w (output: %s)", err, trimOutput(out))
	}

	var raw []struct {
		ID      string    `json:"id"`
		ShortID string    `json:"short_id"`
		Time    time.Time `json:"time"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing restic output: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("no snapshots in repository")
	}

	newest := raw[0]
	for _, s := range raw {
		if s.Time.After(newest.Time) {
			newest = s
		}
	}
	return &Snapshot{ID: newest.ShortID, Time: newest.Time}, nil
}

func (d *ResticDriver) Restore(ctx context.Context, dest string, paths []string) error {
	args := []string{
		"-r", d.Repo,
		"--password-file", d.PasswordFile,
		noCache,
		"restore", "latest",
		"--target", dest,
	}
	for _, p := range paths {
		args = append(args, "--include", p)
	}

	cmd := exec.CommandContext(ctx, "restic", args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restic restore: %w (output: %s)", err, trimOutput(out))
	}
	return nil
}

// maxErrorOutput bounds how much of restic's output is carried into an error.
//
// This is a correctness bound, not tidiness. A restic error carries one line
// per affected file: a truncated pack in a repository of 400 files produces
// roughly 276 KB of output, and a real repository has orders of magnitude more
// files than that. Those bytes do not stay local — the error becomes an
// assertion message, which is written verbatim into report.json and posted as
// the webhook body.
//
// Left unbounded, the failure compounds in the worst direction: the more
// broken the backup, the larger the payload, until the alert that was supposed
// to report the breakage is itself rejected for being oversized. An alert that
// fails precisely when it matters is worse than no alert.
//
// internal/container.trim does the same job for docker/podman output. The two
// are deliberately separate small copies rather than a shared package: each is
// a handful of lines with no logic to get wrong, and neither package otherwise
// depends on the other. A third caller should trigger extraction.
const maxErrorOutput = 2000

func trimOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > maxErrorOutput {
		return s[:maxErrorOutput] + "… (truncated)"
	}
	return s
}
