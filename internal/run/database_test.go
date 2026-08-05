package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/container"
	"gopkg.in/yaml.v3"
)

// These cover the runner's half of the database path — which failures are
// verdicts and which are broken runs, and whether a target that needs no
// database still stays out of the container's way. The container's own
// lifecycle is tested in internal/container against a real runtime.

// dumpTarget builds a target whose only assertion needs a database.
func dumpTarget(t *testing.T, image, load string) config.Target {
	t.Helper()
	tgt := targetWith(t, "stub_needs_database")
	if image != "" || load != "" {
		tgt.VerifyWith = &config.VerifyWith{Image: image, Load: load}
	}
	return tgt
}

// assertNode builds one assert: entry, for tests that need a second assertion
// alongside the one targetWith provides.
func assertNode(t *testing.T, name string) yaml.Node {
	t.Helper()
	var nodes []yaml.Node
	if err := yaml.Unmarshal([]byte("- "+name+": 1\n"), &nodes); err != nil {
		t.Fatalf("building assert entry: %v", err)
	}
	return nodes[0]
}

// A target with no database assertion must never touch a container runtime.
// The seam panics if called, which is the strongest way to say "not here".
func TestTarget_NoDatabaseAssertionNeverDetectsARuntime(t *testing.T) {
	orig := newRuntime
	newRuntime = func(context.Context) (*container.Runtime, error) {
		t.Error("a target with no database assertion detected a container runtime")
		return nil, errors.New("should not be called")
	}
	defer func() { newRuntime = orig }()

	d := &recordingDriver{}
	captureStdout(t, func() { runTargetWith(context.Background(), d, targetWith(t, "stub_needs_restore")) })
}

// An assertion that needs a database, on a target with no verify_with block, is
// a config mistake: ERROR, not a verdict about the backup.
func TestTarget_DatabaseAssertionWithoutVerifyWithIsAnError(t *testing.T) {
	d := &treeDriver{tree: []string{"dump.sql"}}

	tgt := dumpTarget(t, "", "")

	var passed bool
	out := captureStdout(t, func() { passed = runTargetWith(context.Background(), d, tgt) })

	if passed {
		t.Error("expected the target to fail")
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected an ERROR line, got:\n%s", out)
	}
	if !strings.Contains(out, "verify_with") {
		t.Errorf("the error should name the missing block, got:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("a missing config block must not read as a failed verification:\n%s", out)
	}
}

// A dump that is not in the backup at all is failure mode #2 with a database
// attached: a verdict, not a broken run. Reached before any runtime is needed,
// so it does not require docker.
func TestTarget_MissingDumpIsAVerdictNotAnError(t *testing.T) {
	orig := newRuntime
	newRuntime = func(context.Context) (*container.Runtime, error) {
		t.Error("a missing dump must be decided before a runtime is needed")
		return nil, errors.New("should not be called")
	}
	defer func() { newRuntime = orig }()

	d := &treeDriver{tree: []string{"config.php"}}
	tgt := dumpTarget(t, "postgres:16-alpine", "db/dump.sql")

	var passed bool
	out := captureStdout(t, func() { passed = runTargetWith(context.Background(), d, tgt) })

	if passed {
		t.Error("expected the target to fail")
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected a FAIL line, got:\n%s", out)
	}
	if strings.Contains(out, "ERROR") {
		t.Errorf("a dump missing from the backup is a verdict, not a broken run:\n%s", out)
	}
}

// No container runtime is the tool being unable to run, never a verdict. A
// verification tool that reports "your backup is fine" because docker is
// missing would be worse than one that reports nothing.
func TestTarget_NoContainerRuntimeIsAnError(t *testing.T) {
	orig := newRuntime
	newRuntime = func(context.Context) (*container.Runtime, error) { return nil, container.ErrNoRuntime }
	defer func() { newRuntime = orig }()

	d := &treeDriver{tree: []string{"dump.sql"}}
	tgt := dumpTarget(t, "postgres:16-alpine", "dump.sql")

	var passed bool
	out := captureStdout(t, func() { passed = runTargetWith(context.Background(), d, tgt) })

	if passed {
		t.Error("expected the target to fail")
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected an ERROR line, got:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("a missing container runtime must not read as a failed backup:\n%s", out)
	}
}

// When the database cannot be brought up, database assertions are skipped
// rather than reported — the one FAIL or ERROR line is their explanation.
// Assertions that only need files still run, per the standing rule that one
// broken thing must not abort the others.
func TestTarget_FileAssertionsStillRunWhenTheDatabaseIsUnavailable(t *testing.T) {
	orig := newRuntime
	newRuntime = func(context.Context) (*container.Runtime, error) { return nil, container.ErrNoRuntime }
	defer func() { newRuntime = orig }()

	stubNeedsDB.ran = false

	d := &treeDriver{tree: []string{"dump.sql", "config.php"}}

	tgt := targetWith(t, "stub_needs_restore")
	tgt.VerifyWith = &config.VerifyWith{Image: "postgres:16-alpine", Load: "dump.sql"}
	tgt.Assert = append(tgt.Assert, assertNode(t, "stub_needs_database"))

	out := captureStdout(t, func() { runTargetWith(context.Background(), d, tgt) })

	if !strings.Contains(out, "PASS") {
		t.Errorf("the file assertion should still have run and passed:\n%s", out)
	}
	if stubNeedsDB.ran {
		t.Error("a database assertion ran with no database available")
	}
}

// safepath governs the dump path too: a dump reached through a symlink that
// leaves the restore root must not be loaded into the container.
func TestOpenDatabase_DumpEscapingTheRootIsNotLoaded(t *testing.T) {
	orig := newRuntime
	newRuntime = func(context.Context) (*container.Runtime, error) {
		t.Error("an escaping dump path must be rejected before a runtime is needed")
		return nil, errors.New("should not be called")
	}
	defer func() { newRuntime = orig }()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "dump.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "dump.sql"), filepath.Join(root, "dump.sql")); err != nil {
		t.Fatalf("building symlink: %v", err)
	}

	tgt := dumpTarget(t, "postgres:16-alpine", "dump.sql")

	db, loadFailed, err := openDatabase(context.Background(), tgt, root)
	if db != nil {
		db.Close()
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loadFailed == nil {
		t.Fatal("a dump resolving outside the restore root must not be loaded")
	}
	if !strings.Contains(loadFailed.Error(), "not in the restored tree") {
		t.Errorf("unexpected message: %v", loadFailed)
	}
}
