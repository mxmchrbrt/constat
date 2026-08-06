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

// noCache is a correctness requirement, not a tuning knob: restic caches
// tree and index packs locally, and with a warm cache a repository whose
// tree pack has been truncated still restores cleanly, since the damaged
// pack is never read. constat typically runs on the machine that took the
// backup, where the cache is warmest — verifying the cache instead of the
// repository would make failure mode #4 (corruption at rest) invisible.
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

// maxErrorOutput bounds how much of restic's output reaches an error. A
// restic error carries one line per affected file — a truncated pack in a
// 400-file repository produced ~276 KB — and that error becomes a report
// and webhook body, so an unbounded one grows exactly as the backup gets
// worse, risking rejection by the destination when the alert matters most.
const maxErrorOutput = 2000

func trimOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > maxErrorOutput {
		return s[:maxErrorOutput] + "… (truncated)"
	}
	return s
}
