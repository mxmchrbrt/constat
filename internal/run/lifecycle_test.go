package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/assert"
	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/driver"
	"github.com/mxmchrbrt/constat/internal/report"
	"gopkg.in/yaml.v3"
)

// recordingDriver captures what Restore was asked to do, so lifecycle tests
// can assert on the directory afterwards without a live restic.
type recordingDriver struct {
	dest  string
	paths []string
	calls int

	restoreErr error
	writeFile  string // if set, Restore creates this file under dest

	latest    *driver.Snapshot
	latestErr error
}

func (r *recordingDriver) Latest(ctx context.Context) (*driver.Snapshot, error) {
	if r.latestErr != nil {
		return nil, r.latestErr
	}
	if r.latest != nil {
		return r.latest, nil
	}
	return &driver.Snapshot{ID: "fake", Time: time.Now()}, nil
}

func (r *recordingDriver) Restore(ctx context.Context, dest string, paths []string) error {
	r.calls++
	r.dest = dest
	r.paths = paths

	if err := ctx.Err(); err != nil {
		return err
	}
	if r.restoreErr != nil {
		return r.restoreErr
	}
	if r.writeFile != "" {
		return os.WriteFile(filepath.Join(dest, r.writeFile), []byte("x"), 0o600)
	}
	return nil
}

// stubAssertion is registered under a test-only name so Target can build it
// through the normal registry path.
type stubAssertion struct {
	requires assert.Requirements
	passed   bool
	err      error

	sawRestoreDir string
	sawDB         bool
	ran           bool
}

func (s *stubAssertion) Requires() assert.Requirements { return s.requires }

func (s *stubAssertion) Check(ctx context.Context, env assert.Env) (assert.Result, error) {
	s.sawRestoreDir = env.RestoreDir
	s.sawDB = env.DB != nil
	s.ran = true
	if s.err != nil {
		return assert.Result{}, s.err
	}
	return assert.Result{Name: "stub", Passed: s.passed, Message: "stub"}, nil
}

// Registered stubs, keyed by assertion name used in test YAML. The registry is
// global, so each test name registers exactly once via init.
var (
	stubNeedsRestore = &stubAssertion{requires: assert.Requirements{Restore: true}, passed: true}
	stubMetadataOnly = &stubAssertion{requires: assert.Requirements{}, passed: true}
	stubFailsRestore = &stubAssertion{requires: assert.Requirements{Restore: true}, passed: false}
	stubErrsRestore  = &stubAssertion{requires: assert.Requirements{Restore: true}, err: errors.New("stub check error")}
	stubNeedsDB      = &stubAssertion{requires: assert.Requirements{Database: true}, passed: true}
)

func init() {
	assert.Register("stub_needs_restore", func(*yaml.Node) (assert.Assertion, error) { return stubNeedsRestore, nil })
	assert.Register("stub_metadata_only", func(*yaml.Node) (assert.Assertion, error) { return stubMetadataOnly, nil })
	assert.Register("stub_fails_restore", func(*yaml.Node) (assert.Assertion, error) { return stubFailsRestore, nil })
	assert.Register("stub_errs_restore", func(*yaml.Node) (assert.Assertion, error) { return stubErrsRestore, nil })
	assert.Register("stub_needs_database", func(*yaml.Node) (assert.Assertion, error) { return stubNeedsDB, nil })
}

func targetWith(t *testing.T, assertName string) config.Target {
	t.Helper()
	var nodes []yaml.Node
	if err := yaml.Unmarshal([]byte("- "+assertName+": 1\n"), &nodes); err != nil {
		t.Fatalf("building target fixture: %v", err)
	}
	return config.Target{
		Name:   "test-target",
		Source: config.Source{Kind: "restic", Repo: "/repo", PasswordFile: "/pass"},
		Assert: nodes,
	}
}

// runTargetWith swaps in a fake driver for the duration of one Target call.
func runTargetWith(ctx context.Context, d driver.Driver, t config.Target) bool {
	return runTargetFor(ctx, d, t).Verdict == report.Pass
}

// runTargetFor is the same seam, for the tests that need the structured
// outcome rather than just whether it passed.
func runTargetFor(ctx context.Context, d driver.Driver, t config.Target) report.Target {
	orig := newDriver
	newDriver = func(config.Source) (driver.Driver, error) { return d, nil }
	defer func() { newDriver = orig }()
	return Target(ctx, t)
}

func TestTarget_RestoreDirRemovedOnSuccess(t *testing.T) {
	d := &recordingDriver{writeFile: "restored.txt"}

	if !runTargetWith(context.Background(), d, targetWith(t, "stub_needs_restore")) {
		t.Fatal("expected target to pass")
	}
	if d.calls != 1 {
		t.Fatalf("Restore called %d times, want 1", d.calls)
	}
	if _, err := os.Stat(d.dest); !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after success (stat err: %v)", d.dest, err)
	}
}

func TestTarget_RestoreDirRemovedOnAssertionFailure(t *testing.T) {
	d := &recordingDriver{}

	if runTargetWith(context.Background(), d, targetWith(t, "stub_fails_restore")) {
		t.Fatal("expected target to fail")
	}
	if _, err := os.Stat(d.dest); !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after assertion failure", d.dest)
	}
}

func TestTarget_RestoreDirRemovedOnAssertionError(t *testing.T) {
	d := &recordingDriver{}

	if runTargetWith(context.Background(), d, targetWith(t, "stub_errs_restore")) {
		t.Fatal("expected target to fail")
	}
	if _, err := os.Stat(d.dest); !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after assertion error", d.dest)
	}
}

func TestTarget_RestoreDirRemovedOnRestoreError(t *testing.T) {
	d := &recordingDriver{restoreErr: errors.New("repository unreachable")}

	if runTargetWith(context.Background(), d, targetWith(t, "stub_needs_restore")) {
		t.Fatal("expected target to fail")
	}
	if d.dest == "" {
		t.Fatal("Restore was never called")
	}
	if _, err := os.Stat(d.dest); !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after restore error", d.dest)
	}
}

func TestTarget_RestoreDirRemovedOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the run starts

	d := &recordingDriver{}
	if runTargetWith(ctx, d, targetWith(t, "stub_needs_restore")) {
		t.Fatal("expected target to fail on a cancelled context")
	}
	if _, err := os.Stat(d.dest); !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after cancellation", d.dest)
	}
}

func TestTarget_NoRestoreForMetadataOnlyAssertion(t *testing.T) {
	d := &recordingDriver{}

	if !runTargetWith(context.Background(), d, targetWith(t, "stub_metadata_only")) {
		t.Fatal("expected target to pass")
	}
	if d.calls != 0 {
		t.Errorf("Restore called %d times for a metadata-only target, want 0", d.calls)
	}
	if stubMetadataOnly.sawRestoreDir != "" {
		t.Errorf("metadata-only assertion saw RestoreDir %q, want empty", stubMetadataOnly.sawRestoreDir)
	}
}

// The restore directory's mode is asserted directly in
// TestRestoreInto_DirIsPrivate, where the directory still exists. Here we only
// check that an assertion declaring Restore actually receives a usable path.
func TestTarget_RestoreDirReachesAssertion(t *testing.T) {
	d := &recordingDriver{writeFile: "restored.txt"}

	if !runTargetWith(context.Background(), d, targetWith(t, "stub_needs_restore")) {
		t.Fatal("expected target to pass")
	}
	if stubNeedsRestore.sawRestoreDir == "" {
		t.Fatal("assertion requiring a restore saw an empty RestoreDir")
	}
	if stubNeedsRestore.sawRestoreDir != d.dest {
		t.Errorf("assertion saw %q, driver restored into %q", stubNeedsRestore.sawRestoreDir, d.dest)
	}
}

func TestTarget_PathsForwardedToDriver(t *testing.T) {
	d := &recordingDriver{}
	tgt := targetWith(t, "stub_needs_restore")
	tgt.Restore.Paths = []string{"/var/lib/nextcloud/data", "/etc"}

	runTargetWith(context.Background(), d, tgt)

	if len(d.paths) != 2 || d.paths[0] != "/var/lib/nextcloud/data" || d.paths[1] != "/etc" {
		t.Errorf("driver got paths %v, want the target's restore.paths", d.paths)
	}
}

func TestTarget_TimeoutIsApplied(t *testing.T) {
	tgt := targetWith(t, "stub_needs_restore")
	tgt.Timeout = 10 * time.Millisecond

	slow := &slowDriver{delay: 500 * time.Millisecond}
	start := time.Now()
	passed := runTargetWith(context.Background(), slow, tgt)
	elapsed := time.Since(start)

	if passed {
		t.Error("expected target to fail when the restore outruns the timeout")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("target took %s, timeout of 10ms was not enforced", elapsed)
	}
	if _, err := os.Stat(slow.dest); slow.dest != "" && !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after timeout", slow.dest)
	}
}

type slowDriver struct {
	delay time.Duration
	dest  string
}

func (s *slowDriver) Latest(ctx context.Context) (*driver.Snapshot, error) {
	return &driver.Snapshot{ID: "slow", Time: time.Now()}, nil
}

func (s *slowDriver) Restore(ctx context.Context, dest string, paths []string) error {
	s.dest = dest
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestEffectiveTimeout(t *testing.T) {
	if got := (config.Target{}).EffectiveTimeout(); got != config.DefaultTimeout {
		t.Errorf("unset timeout = %s, want DefaultTimeout %s", got, config.DefaultTimeout)
	}
	if got := (config.Target{Timeout: 5 * time.Minute}).EffectiveTimeout(); got != 5*time.Minute {
		t.Errorf("explicit timeout = %s, want 5m", got)
	}
}

func TestTarget_UnknownKindDoesNotRestore(t *testing.T) {
	tgt := targetWith(t, "stub_needs_restore")
	tgt.Source.Kind = "borg"

	if Target(context.Background(), tgt).Verdict == report.Pass {
		t.Fatal("expected target to fail on unknown source kind")
	}
}

func TestRestoreInto_CleanupIsSafeToCallTwice(t *testing.T) {
	d := &recordingDriver{}
	dir, _, cleanup, err := restoreInto(context.Background(), d, config.Target{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cleanup()
	cleanup() // must not panic
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir %s still exists", dir)
	}
}

func TestRestoreInto_DirIsPrivate(t *testing.T) {
	d := &recordingDriver{}
	dir, _, cleanup, err := restoreInto(context.Background(), d, config.Target{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("restore dir mode = %o, want 700", got)
	}
	if !strings.Contains(filepath.Base(dir), "constat-restore-") {
		t.Errorf("dir name %q does not identify constat as the owner", dir)
	}
}
