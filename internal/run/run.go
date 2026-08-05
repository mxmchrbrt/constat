// Package run wires config, the assertion registry, and drivers together to
// execute a target. Kept separate from config so config stays dependency-free
// (schema only) and separate from assert so the assertion model doesn't need
// to know how targets are loaded.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mxmchrbrt/constat/internal/assert"
	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/container"
	"github.com/mxmchrbrt/constat/internal/driver"
	"github.com/mxmchrbrt/constat/internal/report"
	"github.com/mxmchrbrt/constat/internal/safepath"
	"gopkg.in/yaml.v3"
)

func buildDriver(src config.Source) (driver.Driver, error) {
	switch src.Kind {
	case "restic":
		return driver.NewResticDriver(src.Repo, src.PasswordFile), nil
	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}
}

// newDriver is a seam: tests swap it to run the full Target path against a
// fake driver without a live restic binary.
var newDriver = buildDriver

// buildAssertions turns the undecoded assert: entries of a target into
// assertions. Each entry must be a single-key mapping: the key names a
// registered assertion, the value is its parameters, left for the factory
// to decode.
func buildAssertions(nodes []yaml.Node) ([]assert.Assertion, error) {
	assertions := make([]assert.Assertion, 0, len(nodes))

	for i := range nodes {
		n := &nodes[i]

		if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
			return nil, fmt.Errorf("assert entry %d at line %d: expected a single-key mapping", i, n.Line)
		}

		nameNode, paramsNode := n.Content[0], n.Content[1]

		a, err := assert.Build(nameNode.Value, paramsNode)
		if err != nil {
			return nil, fmt.Errorf("assert %q at line %d: %w", nameNode.Value, nameNode.Line, err)
		}

		assertions = append(assertions, a)
	}

	return assertions, nil
}

// restoreInto creates a disposable directory, restores the target's snapshot
// into it, and returns the directory plus a cleanup func. The cleanup func is
// returned even when the restore fails, so the caller can always defer it
// without checking the error first.
func restoreInto(ctx context.Context, d driver.Driver, t config.Target) (dir string, elapsed time.Duration, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "constat-restore-")
	if err != nil {
		return "", 0, func() {}, fmt.Errorf("creating restore directory: %w", err)
	}

	// MkdirTemp already creates 0700, but the restored tree carries customer
	// data and this is cheap to make explicit rather than inherited.
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", 0, func() {}, fmt.Errorf("securing restore directory: %w", err)
	}

	cleanup = func() { os.RemoveAll(dir) }

	start := time.Now()
	if err := d.Restore(ctx, dir, t.Restore.Paths); err != nil {
		return dir, time.Since(start), cleanup, fmt.Errorf("restoring: %w", err)
	}
	return dir, time.Since(start), cleanup, nil
}

// assertionRoot is the directory assertion paths are resolved against: the
// restore directory itself, or the subdirectory named by restore.strip_prefix.
//
// Every failure here is an error rather than a verdict, deliberately. A prefix
// that is not in the restored tree means the config is wrong, and the shape
// that has to be avoided is the one where a mistyped prefix roots every
// assertion at an empty directory and the target reports a long list of
// convincing failures about a backup that is fine.
func assertionRoot(restoreDir, stripPrefix string) (string, error) {
	if stripPrefix == "" {
		return restoreDir, nil
	}

	rel, err := safepath.RelFromBackupPath(stripPrefix)
	if err != nil {
		return "", fmt.Errorf("restore.strip_prefix: %w", err)
	}

	root, found, err := safepath.ResolveInRoot(restoreDir, rel)
	if err != nil {
		return "", fmt.Errorf("resolving restore.strip_prefix %q: %w", stripPrefix, err)
	}
	if !found {
		return "", fmt.Errorf("restore.strip_prefix %q is not in the restored tree; it must be the path the snapshot recorded, which `restic snapshots --json` reports under \"paths\"", stripPrefix)
	}

	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("reading restore.strip_prefix %q: %w", stripPrefix, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("restore.strip_prefix %q is a file, not a directory", stripPrefix)
	}

	return root, nil
}

// newRuntime is a seam: tests swap it to exercise the "no container runtime"
// path without uninstalling docker.
var newRuntime = container.DetectRuntime

// openDatabase brings up the target's verify_with container and loads the dump
// out of the restored tree into it.
//
// The two error returns are deliberately separate rather than one error the
// caller inspects. A dump that will not load is a verdict about the backup; a
// missing container runtime is a broken run. Collapsing them would mean either
// alerting on the operator's typo or staying quiet about a dump that cannot be
// restored, and both are wrong in ways that matter.
//
// The returned *container.Postgres is safe to Close on every path, including
// when it is nil.
func openDatabase(ctx context.Context, t config.Target, restoreDir string) (db *container.Postgres, loadFailed error, err error) {
	if t.VerifyWith == nil {
		return nil, nil, fmt.Errorf("an assertion needs a database but the target has no verify_with block")
	}

	dumpPath, found, err := safepath.ResolveInRoot(restoreDir, filepath.Clean(t.VerifyWith.Load))
	if err != nil {
		return nil, nil, fmt.Errorf("resolving verify_with.load %q: %w", t.VerifyWith.Load, err)
	}
	if !found {
		// A dump that is not in the backup at all is a verdict, not a broken
		// run: that is failure mode #2 with a database attached.
		return nil, fmt.Errorf("dump %q is not in the restored tree", t.VerifyWith.Load), nil
	}

	rt, err := newRuntime(ctx)
	if err != nil {
		return nil, nil, err
	}

	pg, err := container.StartPostgres(ctx, rt, t.VerifyWith.Image, dumpPath)
	var loadErr *container.LoadError
	if errors.As(err, &loadErr) {
		return pg, loadErr, nil
	}
	if err != nil {
		return pg, nil, err
	}
	return pg, nil, nil
}

// Target builds the driver and assertions for t, restores if any assertion
// needs files, runs every assertion, and reports PASS/FAIL/ERROR per assertion
// to stdout. One broken assertion does not stop the others.
//
// Returns the structured outcome as well as printing it. Both are needed and
// neither replaces the other: the printing is what an operator watching a
// terminal sees, and the returned value is what becomes the signed report.
// Deriving one from the other — parsing stdout, or silencing it — would make
// the evidence and the operator's view able to disagree.
//
// The restore directory is always removed, including on the error and panic
// paths: a verification tool that leaves scratch space behind after an
// interrupt is its own bug report.
func Target(ctx context.Context, t config.Target) report.Target {
	ctx, cancel := context.WithTimeout(ctx, t.EffectiveTimeout())
	defer cancel()

	out := report.Target{
		Name:       t.Name,
		Verdict:    report.Pass,
		Kind:       t.Source.Kind,
		Repository: t.Source.Repo,
		Assertions: []report.Assertion{},
	}

	// fail records a whole-target outcome that stops the run: there is nothing
	// left to check once the driver, the restore, or the config is broken.
	fail := func(verdict report.Verdict, name string, err error) report.Target {
		label := "ERROR"
		if verdict == report.Fail {
			label = "FAIL "
		}
		fmt.Printf("%s %s: %v\n", label, t.Name, err)
		out.Verdict = verdict
		out.Assertions = append(out.Assertions, report.Assertion{
			Name:    name,
			Verdict: verdict,
			Message: err.Error(),
		})
		return out
	}

	d, err := newDriver(t.Source)
	if err != nil {
		return fail(report.Error, "driver", fmt.Errorf("building driver: %w", err))
	}

	assertions, err := buildAssertions(t.Assert)
	if err != nil {
		return fail(report.Error, "config", fmt.Errorf("building assertions: %w", err))
	}

	env := assert.Env{Driver: d}

	if assert.AnyRequiresRestore(assertions) {
		dir, elapsed, cleanup, err := restoreInto(ctx, d, t)
		defer cleanup()
		if err != nil {
			return fail(report.Error, "restore", err)
		}
		root, err := assertionRoot(dir, t.Restore.StripPrefix)
		if err != nil {
			return fail(report.Error, "restore", err)
		}

		env.RestoreDir = root
		out.RestoreDurationMs = elapsed.Milliseconds()
		fmt.Printf("      %s: restored in %s\n", t.Name, elapsed.Round(time.Millisecond))
	}

	skipDatabase := false

	if assert.AnyRequiresDatabase(assertions) {
		db, loadFailed, err := openDatabase(ctx, t, env.RestoreDir)
		defer db.Close()

		switch {
		case loadFailed != nil:
			// The author's call, and the right one: a dump that will not load
			// is the failure being hunted — taxonomy #9 and #4 both surface
			// exactly here — so it is a verdict, not a broken run. The
			// database assertions cannot run afterwards, and this line is
			// their explanation.
			fmt.Printf("FAIL  %s: %v\n", t.Name, loadFailed)
			out.Verdict = report.Fail
			out.Assertions = append(out.Assertions, report.Assertion{
				Name:    "database_load",
				Verdict: report.Fail,
				Message: loadFailed.Error(),
			})
			skipDatabase = true
		case err != nil:
			// Everything else — no container runtime, a missing image, a
			// database that never came up — is the tool failing, not the
			// backup.
			fmt.Printf("ERROR %s: %v\n", t.Name, err)
			out.Verdict = report.Error
			out.Assertions = append(out.Assertions, report.Assertion{
				Name:    "database_load",
				Verdict: report.Error,
				Message: err.Error(),
			})
			skipDatabase = true
		default:
			env.DB = db.DB
		}
	}

	for _, a := range assertions {
		if skipDatabase && a.Requires().Database {
			continue
		}

		res, err := a.Check(ctx, env)
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", t.Name, err)
			out.Assertions = append(out.Assertions, report.Assertion{
				Name:    assertionName(a),
				Verdict: report.Error,
				Message: err.Error(),
			})
			// Error outranks fail: a run that could not answer must not be
			// reported as one that answered "no".
			out.Verdict = report.Error
			continue
		}

		verdict := report.Pass
		status := "PASS "
		if !res.Passed {
			verdict = report.Fail
			status = "FAIL "
			if out.Verdict == report.Pass {
				out.Verdict = report.Fail
			}
		}
		fmt.Printf("%s %s: %s\n", status, t.Name, res.Message)

		out.Assertions = append(out.Assertions, report.Assertion{
			Name:       res.Name,
			Verdict:    verdict,
			Message:    res.Message,
			DurationMs: res.Duration.Milliseconds(),
		})
	}

	return out
}

// assertionName recovers the registered name of an assertion whose Check
// errored, since a failed Check returns no Result to read it from.
func assertionName(a assert.Assertion) string {
	if n, ok := a.(interface{ AssertionName() string }); ok {
		return n.AssertionName()
	}
	return "assertion"
}
