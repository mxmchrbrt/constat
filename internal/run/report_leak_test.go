package run

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/container"
	"github.com/mxmchrbrt/constat/internal/report"
)

// The report is the one output that is durable. It is written to disk, will be
// posted to a webhook in session 10, and is kept as evidence — so a secret that
// reaches it does not scroll away like a terminal line does.
//
// These tests run real failures, not just the happy path, because error
// messages are the actual leak surface: they carry subprocess output, and
// subprocess output is where a passphrase would appear if anything echoed one.

// sentinel values are chosen to be unmistakable in a grep and to appear nowhere
// else in the repository.
const (
	sentinelPassphrase = "SENTINEL-REPO-PASSPHRASE-8f3a21"
	sentinelDBPassword = "SENTINEL-DB-PASSWORD-77c1e4"
)

// renderReport runs a target and returns the bytes that would be written to
// disk — the exact artifact, not a summary of it.
func renderReport(t *testing.T, tgt config.Target) []byte {
	t.Helper()

	var outcome report.Target
	out := captureStdout(t, func() { outcome = Target(context.Background(), tgt) })
	t.Logf("stdout:\n%s", out)

	rep := report.New(time.Now(), "test-host", "test", []report.Target{outcome})

	var buf bytes.Buffer
	if err := report.Write(&buf, rep, nil); err != nil {
		t.Fatalf("writing report: %v", err)
	}
	return buf.Bytes()
}

func assertNoSecrets(t *testing.T, rendered []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if bytes.Contains(rendered, []byte(secret)) {
			t.Errorf("the report contains %q:\n%s", secret, rendered)
		}
	}
}

// The passphrase is what protects the repository. It must not survive into the
// report on any path, including the ones where restic fails and its output is
// carried into the error.
func TestReport_NeverContainsTheRepositoryPassphrase(t *testing.T) {
	requireRestic(t)

	repo, passwordFile, backupPath := buildLabRepo(t)

	// Overwrite the generated password file with a sentinel, so a leak is
	// unmistakable. The repository can no longer be opened with it, which is
	// the point: this exercises the failure path, where restic's own output
	// reaches the error message.
	if err := os.WriteFile(passwordFile, []byte(sentinelPassphrase+"\n"), 0o600); err != nil {
		t.Fatalf("writing sentinel password file: %v", err)
	}

	tgt := labTarget(t, repo, passwordFile, backupPath, ""+
		"- newest_snapshot_age_max: 24h\n"+
		"- path_exists: config.php\n")

	rendered := renderReport(t, tgt)

	// Sanity: this must be the failing run it was built to be, or the test
	// proves nothing.
	if !bytes.Contains(rendered, []byte(`"verdict":"error"`)) {
		t.Fatalf("expected the run to fail with a wrong passphrase:\n%s", rendered)
	}

	assertNoSecrets(t, rendered, sentinelPassphrase)
}

// Paths are evidence and belong in the report. Only contents are secret. This
// pins that distinction so a future "redact everything" reflex does not quietly
// strip what makes a report useful.
func TestReport_KeepsPathsWhileDroppingContents(t *testing.T) {
	requireRestic(t)

	repo, passwordFile, backupPath := buildLabRepo(t)
	tgt := labTarget(t, repo, passwordFile, backupPath, "- path_exists: config.php\n")

	rendered := renderReport(t, tgt)

	if !bytes.Contains(rendered, []byte(repo)) {
		t.Errorf("the repository path should be recorded as evidence:\n%s", rendered)
	}
	if !bytes.Contains(rendered, []byte(`"verdict":"pass"`)) {
		t.Errorf("expected a passing run:\n%s", rendered)
	}
}

// A dump that fails to load carries psql's output into the report. If a
// credential appeared in a SQL statement, that is where it would surface.
func TestReport_NeverContainsCredentialsFromAFailedLoad(t *testing.T) {
	requireRestic(t)
	requireRuntimeForRun(t)

	repo, passwordFile, backupPath, dumpDir := buildLabRepoWithDump(t,
		"CREATE ROLE app LOGIN PASSWORD '"+sentinelDBPassword+"';\nTHIS IS NOT SQL;\n")
	_ = dumpDir

	tgt := labTarget(t, repo, passwordFile, backupPath, "- stub_needs_database: 1\n")
	tgt.VerifyWith = &config.VerifyWith{Image: pgTestImage(), Load: "db/dump.sql"}

	rendered := renderReport(t, tgt)

	if !bytes.Contains(rendered, []byte(`"verdict":"fail"`)) {
		t.Fatalf("expected the broken dump to fail the target:\n%s", rendered)
	}
	assertNoSecrets(t, rendered, sentinelDBPassword)
}

// A message that came out of a backup must not be able to change the report's
// shape. The restored tree is untrusted input, and a filename is the easiest
// thing in it for an attacker to control.
func TestReport_HostileFilenameCannotBreakTheReport(t *testing.T) {
	hostile := `evil","verdict":"pass","injected":"`

	d := &treeDriver{tree: []string{"data/" + strings.ReplaceAll(hostile, "/", "_")}}

	tgt := targetWith(t, "stub_fails_restore")
	tgt.Name = hostile

	var outcome report.Target
	captureStdout(t, func() { outcome = runTargetFor(context.Background(), d, tgt) })

	rep := report.New(time.Now(), "h", "v", []report.Target{outcome})
	rendered, err := report.Canonical(rep)
	if err != nil {
		t.Fatalf("serialising: %v", err)
	}

	if bytes.Contains(rendered, []byte(`"injected"`)) {
		t.Errorf("a hostile target name escaped its field:\n%s", rendered)
	}
	if !bytes.Contains(rendered, []byte(`"verdict":"fail"`)) {
		t.Errorf("the report's verdict was changed by the payload:\n%s", rendered)
	}
}

// --- helpers ---

func pgTestImage() string {
	if v := os.Getenv("CONSTAT_TEST_PG_IMAGE"); v != "" {
		return v
	}
	return "docker.io/library/postgres:16-alpine"
}

// buildLabRepoWithDump is buildLabRepo plus a SQL dump inside the backed-up
// tree, so verify_with has something to load.
func buildLabRepoWithDump(t *testing.T, dumpBody string) (repo, passwordFile, backupPath, dumpDir string) {
	t.Helper()

	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	passwordFile = filepath.Join(base, "pass")
	data := filepath.Join(base, "data")

	if err := os.WriteFile(passwordFile, []byte("lab-passphrase\n"), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(data, "db"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(data, "db", "dump.sql"), []byte(dumpBody), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}
	if err := os.WriteFile(filepath.Join(data, "config.php"), []byte("payload\n"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	resticRun(t, repo, passwordFile, "init")
	resticRun(t, repo, passwordFile, "backup", data)

	real, err := filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatalf("resolving data path: %v", err)
	}
	return repo, passwordFile, real, filepath.Join(data, "db")
}

// resticRun shells out to restic for fixture building, failing the test on
// error with the subprocess output attached.
func resticRun(t *testing.T, repo, passwordFile string, args ...string) {
	t.Helper()
	full := append([]string{"-r", repo, "--password-file", passwordFile}, args...)
	out, err := exec.Command("restic", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("restic %v: %v (output: %s)", args, err, out)
	}
}

// requireRuntimeForRun skips when no container runtime is usable.
func requireRuntimeForRun(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a container; skipped under -short")
	}
	if _, err := container.DetectRuntime(context.Background()); err != nil {
		t.Skipf("no container runtime: %v", err)
	}
}
