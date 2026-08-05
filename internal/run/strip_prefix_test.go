package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mxmchrbrt/constat/internal/config"
	"gopkg.in/yaml.v3"
)

// treeDriver restores a fixed set of relative paths, so a test can shape the
// tree that restore.strip_prefix is resolved against. Entries ending in a
// separator become directories.
type treeDriver struct {
	recordingDriver
	tree []string
}

func (d *treeDriver) Restore(ctx context.Context, dest string, paths []string) error {
	if err := d.recordingDriver.Restore(ctx, dest, paths); err != nil {
		return err
	}
	for _, rel := range d.tree {
		p := filepath.Join(dest, rel)
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte("payload\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func TestAssertionRoot_NoPrefixUsesRestoreDirUnchanged(t *testing.T) {
	dir := t.TempDir()

	got, err := assertionRoot(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("assertionRoot = %q, want the restore dir %q", got, dir)
	}
}

func TestAssertionRoot_PrefixMovesTheRoot(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "home", "app", "data")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("building tree: %v", err)
	}

	got, err := assertionRoot(dir, "/home/app/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	realNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("resolving expected root: %v", err)
	}
	if got != realNested {
		t.Errorf("assertionRoot = %q, want %q", got, realNested)
	}
}

func TestAssertionRoot_Rejections(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "home", "app", "data"), 0o755); err != nil {
		t.Fatalf("building tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home", "app", "data", "config.php"), []byte("x"), 0o644); err != nil {
		t.Fatalf("building tree: %v", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("building symlink: %v", err)
	}

	tests := []struct {
		name    string
		prefix  string
		wantErr string
	}{
		{
			name:    "prefix absent from the restored tree",
			prefix:  "/home/app/wrong",
			wantErr: "not in the restored tree",
		},
		{
			name:    "prefix names a file",
			prefix:  "/home/app/data/config.php",
			wantErr: "is a file, not a directory",
		},
		{
			// Reads as absent rather than as an escape, because that is what
			// ResolveInRoot reports; either way the target must not end up
			// rooted outside the restore directory.
			name:    "prefix escaping the restore root via a symlink",
			prefix:  "/escape",
			wantErr: "not in the restored tree",
		},
		{
			name:    "filesystem root",
			prefix:  "/",
			wantErr: "whole filesystem root",
		},
		{
			name:    "relative traversal",
			prefix:  "../../etc",
			wantErr: "escapes the restore root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := assertionRoot(dir, tt.prefix)
			if err == nil {
				t.Fatalf("expected an error, got root %q", got)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// The end an operator actually sees, checked through a real assertion rather
// than a stub: with the prefix set, the config can say `config.php` instead of
// repeating the whole original path. The same config without the prefix must
// fail, or the test would pass for the wrong reason.
func TestTarget_StripPrefixReachesAssertions(t *testing.T) {
	tree := []string{"home/app/data/config.php"}

	tgt := labStyleTarget(t, "- path_exists: config.php\n")
	tgt.Restore.StripPrefix = "/home/app/data"

	out := captureStdout(t, func() {
		if !runTargetWith(context.Background(), &treeDriver{tree: tree}, tgt) {
			t.Error("expected a bare relative path to resolve under strip_prefix")
		}
	})
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected a PASS line, got:\n%s", out)
	}

	tgt.Restore.StripPrefix = ""
	out = captureStdout(t, func() {
		if runTargetWith(context.Background(), &treeDriver{tree: tree}, tgt) {
			t.Error("without strip_prefix the same bare path must not resolve")
		}
	})
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected a FAIL line without strip_prefix, got:\n%s", out)
	}
}

// labStyleTarget builds a target from arbitrary assert YAML, for the cases
// that need real assertions rather than the stubs in lifecycle_test.go.
func labStyleTarget(t *testing.T, assertYAML string) config.Target {
	t.Helper()
	var nodes []yaml.Node
	if err := yaml.Unmarshal([]byte(assertYAML), &nodes); err != nil {
		t.Fatalf("parsing assert fixture: %v", err)
	}
	return config.Target{
		Name:   "prefix-target",
		Source: config.Source{Kind: "restic", Repo: "/repo", PasswordFile: "/pass"},
		Assert: nodes,
	}
}

// A mistyped prefix is the whole reason this is an error and not a verdict:
// otherwise every assertion fails convincingly against an empty directory and
// the operator goes looking at a backup that is fine.
func TestTarget_WrongStripPrefixIsAnErrorNotAFailure(t *testing.T) {
	d := &treeDriver{tree: []string{"home/app/data/config.php"}}

	tgt := targetWith(t, "stub_needs_restore")
	tgt.Restore.StripPrefix = "/home/app/dta" // typo

	out := captureStdout(t, func() {
		if runTargetWith(context.Background(), d, tgt) {
			t.Error("expected the target to fail")
		}
	})

	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected an ERROR line, got:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("a mistyped prefix must not read as a failed verification:\n%s", out)
	}
	if !strings.Contains(out, "restic snapshots --json") {
		t.Errorf("the error should say where to find the right value, got:\n%s", out)
	}
}

// The restore directory must still be cleaned up when the prefix is what
// failed, since the failure happens after the restore has already run.
func TestTarget_RestoreDirRemovedOnStripPrefixError(t *testing.T) {
	d := &treeDriver{tree: []string{"home/app/data/config.php"}}

	tgt := targetWith(t, "stub_needs_restore")
	tgt.Restore.StripPrefix = "/nowhere"

	captureStdout(t, func() { runTargetWith(context.Background(), d, tgt) })

	if d.dest == "" {
		t.Fatal("Restore was never called")
	}
	if _, err := os.Stat(d.dest); !os.IsNotExist(err) {
		t.Errorf("restore dir %s still exists after a strip_prefix error", d.dest)
	}
}
