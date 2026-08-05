package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	Restore(ctx context.Context, dest string) error
}

// ResticDriver implements Driver against a restic repository.
type ResticDriver struct {
	Repo         string
	PasswordFile string
}

func NewResticDriver(repo, passwordFile string) *ResticDriver {
	return &ResticDriver{Repo: repo, PasswordFile: passwordFile}
}

func (d *ResticDriver) Latest(ctx context.Context) (*Snapshot, error) {
	cmd := exec.CommandContext(ctx, "restic",
		"-r", d.Repo,
		"--password-file", d.PasswordFile,
		"--json",
		"snapshots",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("restic snapshots: %w (output: %s)", err, out)
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

func (d *ResticDriver) Restore(ctx context.Context, dest string) error {
	cmd := exec.CommandContext(ctx, "restic",
		"-r", d.Repo,
		"--password-file", d.PasswordFile,
		"restore", "latest",
		"--target", dest,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restic restore: %w (output: %s)", err, out)
	}
	return nil
}
