package run

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/config"
	"gopkg.in/yaml.v3"
)

// End-to-end coverage against a real restic repository. Everything else in the
// suite runs against fakes, which cannot answer the question this file exists
// for: does restic actually hand back the metadata the file assertions depend
// on? In particular newest_file_age_max's mtime guard errors out when restored
// mtimes look newer than the snapshot, and only a real round trip proves that
// guard does not fire on a healthy restore.
//
// Builds its own repository in a temp directory rather than using
// ~/constat-lab, so it is self-contained and runs anywhere restic is
// installed. Skips cleanly when restic is absent, so CI stays green without it.

// requireRestic skips the test unless a usable restic binary is on PATH. Each
// case initialises a repository and runs a real restore, so they are also
// skipped under -short.
func requireRestic(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test builds a restic repository; skipped under -short")
	}
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not on PATH; skipping end-to-end test")
	}
}

// restoreLab builds a restic repository from a small tree and returns the
// repository path, the password file, and the path the restored tree will
// appear under inside a restore directory.
//
// That last value is the awkward one: restic stores absolute paths, so
// restoring into an empty directory reproduces the whole original path beneath
// it. A restore of /home/app/data lands at <restoredir>/home/app/data, and
// assertion paths have to be written against that. Recorded as a finding in
// PLAN.md session 5 rather than papered over here.
func buildLabRepo(t *testing.T) (repo, passwordFile, restoredPrefix string) {
	t.Helper()

	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	passwordFile = filepath.Join(base, "pass")
	data := filepath.Join(base, "data")

	if err := os.WriteFile(passwordFile, []byte("lab-passphrase\n"), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}

	files := map[string]time.Duration{
		"config.php":    2 * time.Hour,
		"files/a.txt":   90 * time.Minute,
		"files/b.txt":   time.Hour,
		"files/c.log":   30 * time.Minute,
		"files/old.bin": 45 * 24 * time.Hour, // deliberately ancient, must not win
	}
	for rel, age := range files {
		p := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte("payload\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
		stamp := time.Now().Add(-age)
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatalf("setting mtime on %s: %v", p, err)
		}
	}

	restic := func(args ...string) {
		t.Helper()
		full := append([]string{"-r", repo, "--password-file", passwordFile}, args...)
		out, err := exec.Command("restic", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("restic %v: %v (output: %s)", args, err, out)
		}
	}
	restic("init")
	restic("backup", data)

	// restic records the path it was given after symlink resolution, and the
	// restored tree mirrors it minus the leading separator.
	realData, err := filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatalf("resolving data path: %v", err)
	}
	return repo, passwordFile, strings.TrimPrefix(realData, string(filepath.Separator))
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// Target reports verdicts straight to stdout, so this is the only way to make
// assertions about what an operator would actually see.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		io.Copy(&sb, r)
		done <- sb.String()
	}()

	fn()

	os.Stdout = orig
	w.Close()
	out := <-done
	r.Close()
	return out
}

func labTarget(t *testing.T, repo, passwordFile, assertYAML string) config.Target {
	t.Helper()
	var nodes []yaml.Node
	if err := yaml.Unmarshal([]byte(assertYAML), &nodes); err != nil {
		t.Fatalf("parsing assert fixture: %v", err)
	}
	return config.Target{
		Name:    "lab-files",
		Source:  config.Source{Kind: "restic", Repo: repo, PasswordFile: passwordFile},
		Assert:  nodes,
		Timeout: 2 * time.Minute,
	}
}

func TestEndToEnd_HealthyRepositoryPasses(t *testing.T) {
	requireRestic(t)
	repo, passwordFile, prefix := buildLabRepo(t)

	tgt := labTarget(t, repo, passwordFile, ""+
		"- newest_snapshot_age_max: 24h\n"+
		"- path_exists: "+prefix+"/config.php\n"+
		"- file_count_min:\n    min: 3\n    path: "+prefix+"/files\n"+
		"- newest_file_age_max:\n    max_age: 24h\n    path: "+prefix+"\n")

	var passed bool
	out := captureStdout(t, func() { passed = Target(context.Background(), tgt) })
	t.Logf("target output:\n%s", out)

	if !passed {
		t.Errorf("healthy repository reported a failure")
	}
	if strings.Count(out, "PASS ") != 4 {
		t.Errorf("expected 4 PASS lines, got:\n%s", out)
	}
	if strings.Contains(out, "FAIL") || strings.Contains(out, "ERROR") {
		t.Errorf("unexpected FAIL or ERROR in output:\n%s", out)
	}
	if !strings.Contains(out, "restored in") {
		t.Errorf("expected the restore to be reported, got:\n%s", out)
	}
	// The whole reason this test uses a real repository: proves restic
	// preserves mtimes well enough that the guard stays quiet, and that the
	// freshest file wins over the 45-day-old one in the same tree.
	if !strings.Contains(out, "newest file") {
		t.Errorf("expected newest_file_age_max to report a dated file, got:\n%s", out)
	}
	if strings.Contains(out, "did not preserve modification times") {
		t.Errorf("mtime guard fired against a real restic restore:\n%s", out)
	}
}

// Taxonomy #1 end to end: the repository and its snapshot are fresh, so
// newest_snapshot_age_max is happy, but the contents are older than the
// threshold and the target must still fail.
func TestEndToEnd_StaleContentsFailWhileSnapshotPasses(t *testing.T) {
	requireRestic(t)
	repo, passwordFile, prefix := buildLabRepo(t)

	tgt := labTarget(t, repo, passwordFile, ""+
		"- newest_snapshot_age_max: 24h\n"+
		"- newest_file_age_max:\n    max_age: 10m\n    path: "+prefix+"\n")

	var passed bool
	out := captureStdout(t, func() { passed = Target(context.Background(), tgt) })
	t.Logf("target output:\n%s", out)

	if passed {
		t.Errorf("stale contents behind a fresh snapshot were reported as a pass:\n%s", out)
	}
	if !strings.Contains(out, "PASS ") {
		t.Errorf("expected the snapshot age check to still pass:\n%s", out)
	}
	if !strings.Contains(out, "FAIL ") {
		t.Errorf("expected the file age check to fail:\n%s", out)
	}
}

// A path the operator got wrong must read as a failed verification, not as a
// crash or a quiet pass.
func TestEndToEnd_MissingPathFails(t *testing.T) {
	requireRestic(t)
	repo, passwordFile, prefix := buildLabRepo(t)

	tgt := labTarget(t, repo, passwordFile,
		"- path_exists: "+prefix+"/does-not-exist.php\n")

	var passed bool
	out := captureStdout(t, func() { passed = Target(context.Background(), tgt) })

	if passed {
		t.Errorf("missing path reported as a pass:\n%s", out)
	}
	if !strings.Contains(out, "FAIL ") {
		t.Errorf("expected a FAIL line, got:\n%s", out)
	}
}

// The password file's contents must never reach stdout, on any path. Checked
// here rather than only by reading the code, because this is the run that
// actually shells out to restic with a real passphrase.
func TestEndToEnd_PassphraseNeverReachesStdout(t *testing.T) {
	requireRestic(t)
	repo, passwordFile, prefix := buildLabRepo(t)

	tgt := labTarget(t, repo, passwordFile, ""+
		"- path_exists: "+prefix+"/config.php\n"+
		"- path_exists: "+prefix+"/does-not-exist.php\n")

	out := captureStdout(t, func() { Target(context.Background(), tgt) })

	if strings.Contains(out, "lab-passphrase") {
		t.Error("the repository passphrase reached stdout")
	}
}

// A repository that cannot be opened must fail the target cleanly, with the
// subprocess output carried into the error rather than a bare exit status.
func TestEndToEnd_UnreadableRepositoryErrorsWithOutput(t *testing.T) {
	requireRestic(t)
	_, passwordFile, _ := buildLabRepo(t)

	tgt := labTarget(t, filepath.Join(t.TempDir(), "no-such-repo"), passwordFile,
		"- newest_snapshot_age_max: 24h\n")

	var passed bool
	out := captureStdout(t, func() { passed = Target(context.Background(), tgt) })

	if passed {
		t.Errorf("target passed against a nonexistent repository:\n%s", out)
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected an ERROR line, got:\n%s", out)
	}
	if !strings.Contains(out, "output:") {
		t.Errorf("expected restic's own output to be carried into the error, got:\n%s", out)
	}
}
