package driver

import (
	"context"
	"encoding/json"
	"errors"
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

// RepositoryChecker is implemented by drivers whose backend can verify the
// whole repository, not just the snapshot a restore happens to read.
//
// Optional rather than part of Driver: a driver that cannot do this should
// fail to build the assertion with a clear message, not carry a method that
// returns "unsupported".
type RepositoryChecker interface {
	// CheckRepository verifies repository structure. readDataSubset, when
	// non-empty, is passed to restic's --read-data-subset to additionally
	// read back that fraction of pack contents and compare it against the
	// recorded hashes.
	//
	// A repository that is actually damaged returns *CheckFailure; anything
	// else (no binary, no credentials, a cancelled context) returns an
	// ordinary error, because the difference is the difference between a
	// verdict about the backup and a broken run.
	CheckRepository(ctx context.Context, readDataSubset string) error
}

// CheckFailure is a repository that failed verification — damage found, as
// opposed to a check that could not be performed.
type CheckFailure struct {
	Output string
}

func (e *CheckFailure) Error() string { return e.Output }

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

// CheckRepository runs `restic check`, optionally reading back a subset of
// pack data.
//
// This covers what a restore cannot: a restore reads only the packs its own
// snapshot references, so damage to a pack reachable only from an older
// snapshot survives every successful drill. check walks the whole repository.
//
// Without --read-data-subset it verifies structure — index consistency, that
// every referenced blob is accounted for, that packs are present and the size
// they claim. That catches truncated and missing uploads and broken chains
// cheaply, including on metered backends. It does not read pack contents back,
// so a pack that is present, correctly sized and wrong inside needs the
// subset argument, which is why it exists as a knob rather than a default:
// reading everything is exactly the cost that makes people stop running it.
func (d *ResticDriver) CheckRepository(ctx context.Context, readDataSubset string) error {
	args := []string{
		"-r", d.Repo,
		"--password-file", d.PasswordFile,
		noCache,
		"check",
	}
	if readDataSubset != "" {
		args = append(args, "--read-data-subset="+readDataSubset)
	}

	cmd := exec.CommandContext(ctx, "restic", args...)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	// A non-zero exit from a process that actually ran is restic's answer
	// about the repository. Everything else — no binary, no permission, a
	// deadline — is constat failing to ask the question.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return &CheckFailure{Output: summariseCheck(out)}
	}
	return fmt.Errorf("restic check: %w (output: %s)", err, trimOutput(out))
}

// checkNoise is what `restic check` prints on its way to the answer: progress
// steps, and the advisory paragraph it appends after any failure.
//
// Dropped because this output becomes a report line and a webhook body. The
// operator needs the sentence naming the damaged pack, and shipping it inside
// two kilobytes of boilerplate is how that sentence gets missed. Matching on
// noise rather than on signal is deliberate: an unrecognised line is kept, so
// a future restic phrasing degrades to verbose rather than to silent.
var checkNoise = []string{
	"create exclusive lock for repository",
	"load indexes",
	"check all packs",
	"check snapshots, trees and blobs",
	"The repository contains damaged pack files",
	"Damaged pack files can be caused by",
	"Please read the troubleshooting guide",
	"restic repair packs",
	"restic repair snapshots",
}

func summariseCheck(out []byte) string {
	var kept []string
	var last string

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") { // "[0:00] 100.00% 2 / 2 snapshots"
			continue
		}

		noise := false
		for _, prefix := range checkNoise {
			if strings.HasPrefix(line, prefix) {
				noise = true
				break
			}
		}
		if noise {
			continue
		}

		// restic reports the same unreadable pack once per read attempt.
		if line == last {
			continue
		}
		last = line
		kept = append(kept, line)
	}

	if len(kept) == 0 {
		// Nothing recognised: better verbose than empty, since something
		// made restic exit non-zero.
		return trimOutput(out)
	}
	return trimOutput([]byte(strings.Join(kept, "; ")))
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
