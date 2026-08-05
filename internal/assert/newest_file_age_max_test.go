package assert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/driver"
)

// Table derived from CLAUDE.md failure mode #1 ("silent non-execution") on the
// file side, reviewed before the implementation was written. Two calls were the
// author's, recorded in PLAN.md session 5: a tree where nothing matches is a
// FAIL, not a PASS or an error; and a restore whose mtimes are newer than the
// snapshot they came from is an ERROR, because the assertion would otherwise
// report clean for a backup that captured nothing.

// writeAged writes a file and backdates its mtime by age.
func writeAged(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	mustWriteFile(t, path, content)
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("setting mtime on %s: %v", path, err)
	}
}

// snapshotAt builds a driver whose newest snapshot is age old, which is what
// newest_file_age_max validates restored mtimes against.
func snapshotAt(age time.Duration) *fakeDriver {
	return &fakeDriver{latest: &driver.Snapshot{ID: "snap1", Time: time.Now().Add(-age)}}
}

type verdict int

const (
	wantPass verdict = iota
	wantFail
	wantErr
)

func (v verdict) String() string {
	switch v {
	case wantPass:
		return "PASS"
	case wantFail:
		return "FAIL"
	default:
		return "ERROR"
	}
}

func TestNewestFileAgeMax_Check(t *testing.T) {
	const day = 24 * time.Hour

	tests := []struct {
		name string
		// setup populates a fresh restore root and returns nothing; the root
		// is created per case so mtimes cannot leak between them.
		setup  func(t *testing.T, root string)
		maxAge time.Duration
		path   string
		glob   string
		// snapshotAge is how old the repository's newest snapshot is; negative
		// means the snapshot itself is dated in the future.
		snapshotAge time.Duration
		want        verdict
		// wantErrContains pins which guard fired, since both error paths
		// otherwise look identical to a caller reading only the verdict.
		wantErrContains string
	}{
		{
			name: "newest file well inside the threshold",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", time.Hour)
			},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantPass,
		},
		{
			// The headline case: restic reports a fresh snapshot every night,
			// but the application stopped writing three months ago.
			name: "newest file three months old",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", 90*day)
			},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantFail,
		},
		{
			name: "newest file exactly at the threshold",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", day)
			},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantPass,
		},
		{
			name: "freshest file wins, not the average",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "ancient.txt"), "x", 90*day)
				writeAged(t, filepath.Join(root, "old.txt"), "x", 40*day)
				writeAged(t, filepath.Join(root, "fresh.txt"), "x", time.Hour)
			},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantPass,
		},
		{
			name: "glob isolates a stale dump among fresh logs",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "app.log"), "x", time.Hour)
				writeAged(t, filepath.Join(root, "db.sql"), "x", 90*day)
			},
			maxAge: day, glob: "*.sql", snapshotAge: 30 * time.Minute, want: wantFail,
		},
		{
			name: "nested path is searched recursively",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "data", "deep", "a.txt"), "x", time.Hour)
			},
			maxAge: day, path: "data", snapshotAge: 30 * time.Minute, want: wantPass,
		},
		{
			name: "configured subpath absent from the restore",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", time.Hour)
			},
			maxAge: day, path: "does-not-exist", snapshotAge: 30 * time.Minute, want: wantFail,
		},
		{
			name:   "restore is empty",
			setup:  func(t *testing.T, root string) {},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantFail,
		},
		{
			name: "glob matches nothing",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "app.log"), "x", time.Hour)
			},
			maxAge: day, glob: "*.sql", snapshotAge: 30 * time.Minute, want: wantFail,
		},
		{
			name: "mtime in the future",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", -time.Hour)
			},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantErr,
			wantErrContains: "in the future",
		},
		{
			// A clock that jumped forward during the backup future-dates the
			// snapshot as well as the files, so the snapshot comparison below
			// sees nothing wrong. Left unguarded, the age is negative and the
			// check passes silently.
			name: "clock jumped forward: file and snapshot both future-dated",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", -2*time.Hour)
			},
			maxAge: day, snapshotAge: -3 * time.Hour, want: wantErr,
			wantErrContains: "in the future",
		},
		{
			// The silent-uselessness case: a driver that drops mtimes stamps
			// every restored file at restore time, so the freshest file always
			// looks new and the check always passes.
			name: "restore did not preserve mtimes",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", 0)
				writeAged(t, filepath.Join(root, "b.txt"), "x", 0)
			},
			maxAge: day, snapshotAge: 90 * day, want: wantErr,
			wantErrContains: "did not preserve modification times",
		},
		{
			// A file written while the backup was still running is allowed to
			// be a little newer than the snapshot's own timestamp.
			name: "file marginally newer than the snapshot is tolerated",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "a.txt"), "x", time.Minute)
			},
			maxAge: day, snapshotAge: 3 * time.Minute, want: wantPass,
		},
		{
			name: "directory mtime does not count as fresh content",
			setup: func(t *testing.T, root string) {
				writeAged(t, filepath.Join(root, "data", "a.txt"), "x", 90*day)
				now := time.Now()
				if err := os.Chtimes(filepath.Join(root, "data"), now, now); err != nil {
					t.Fatalf("touching directory: %v", err)
				}
			},
			maxAge: day, snapshotAge: 30 * time.Minute, want: wantFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)

			path := tt.path
			if path == "" {
				path = "."
			}
			a := &newestFileAge{maxAge: tt.maxAge, path: path, glob: tt.glob}

			res, err := a.Check(context.Background(), Env{
				Driver:     snapshotAt(tt.snapshotAge),
				RestoreDir: root,
			})

			got := wantPass
			switch {
			case err != nil:
				got = wantErr
			case !res.Passed:
				got = wantFail
			}
			if got != tt.want {
				t.Fatalf("got %s, want %s (message %q, err %v)", got, tt.want, res.Message, err)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error %q does not mention %q, so the wrong guard fired", err, tt.wantErrContains)
			}
		})
	}
}

// A symlink pointing at a fresh file outside the restore root must not make a
// stale tree look current — the same never-follow-out policy as path_exists
// and file_count_min, here with a verdict that would flip if it were broken.
func TestNewestFileAgeMax_FreshSymlinkOutsideRootIgnored(t *testing.T) {
	root := t.TempDir()
	writeAged(t, filepath.Join(root, "stale.txt"), "x", 90*24*time.Hour)

	outside := t.TempDir()
	writeAged(t, filepath.Join(outside, "fresh.txt"), "x", time.Minute)
	mustSymlink(t, filepath.Join(outside, "fresh.txt"), filepath.Join(root, "looks-fresh.txt"))

	a := &newestFileAge{maxAge: 24 * time.Hour, path: "."}
	res, err := a.Check(context.Background(), Env{Driver: snapshotAt(30 * time.Minute), RestoreDir: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Passed {
		t.Errorf("a symlink to a fresh file outside the root must not count: %q", res.Message)
	}
}

func TestNewestFileAgeMax_SubpathIsEscapingSymlink(t *testing.T) {
	root := t.TempDir()

	outside := t.TempDir()
	writeAged(t, filepath.Join(outside, "fresh.txt"), "x", time.Minute)
	mustSymlink(t, outside, filepath.Join(root, "escape-dir"))

	a := &newestFileAge{maxAge: 24 * time.Hour, path: "escape-dir"}
	res, err := a.Check(context.Background(), Env{Driver: snapshotAt(30 * time.Minute), RestoreDir: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Passed {
		t.Errorf("a subpath escaping the root via a symlink must not be walked: %q", res.Message)
	}
}

// The mtime guard depends on snapshot metadata, so a driver that cannot answer
// must surface as an error rather than as a quiet pass.
func TestNewestFileAgeMax_DriverErrorIsNotAPass(t *testing.T) {
	root := t.TempDir()
	writeAged(t, filepath.Join(root, "a.txt"), "x", time.Hour)

	a := &newestFileAge{maxAge: 24 * time.Hour, path: "."}
	_, err := a.Check(context.Background(), Env{
		Driver:     &fakeDriver{latestErr: errors.New("repository unreachable")},
		RestoreDir: root,
	})
	if err == nil {
		t.Fatal("expected an error when snapshot metadata is unavailable, got none")
	}
}

func TestNewestFileAgeMax_ContextCancellationStopsTheWalk(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		writeAged(t, filepath.Join(root, "f", string(rune('a'+i%26)), "x.txt"), "x", time.Hour)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := &newestFileAge{maxAge: 24 * time.Hour, path: "."}
	if _, err := a.Check(ctx, Env{Driver: snapshotAt(time.Hour), RestoreDir: root}); err == nil {
		t.Error("expected a cancelled context to abort the walk, got no error")
	}
}

func TestNewNewestFileAgeMax_ConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{name: "max_age only", yaml: "max_age: 24h\n"},
		{name: "max_age with path and glob", yaml: "max_age: 24h\npath: data\nglob: \"*.sql\"\n"},
		{name: "missing max_age rejected", yaml: "path: data\n", wantErr: true},
		{name: "zero max_age rejected", yaml: "max_age: 0\n", wantErr: true},
		{name: "negative max_age rejected", yaml: "max_age: -1h\n", wantErr: true},
		{name: "absolute path rejected", yaml: "max_age: 24h\npath: /etc\n", wantErr: true},
		{name: "traversal rejected", yaml: "max_age: 24h\npath: ../outside\n", wantErr: true},
		{name: "invalid glob rejected", yaml: "max_age: 24h\nglob: \"[\"\n", wantErr: true},
		{name: "unparseable duration rejected", yaml: "max_age: soon\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newNewestFileAge(mappingNode(t, tt.yaml))
			if tt.wantErr && err == nil {
				t.Fatal("expected a build-time error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewestFileAgeMax_Requires(t *testing.T) {
	a := &newestFileAge{}
	if !a.Requires().Restore {
		t.Error("newest_file_age_max must require a restore")
	}
}
