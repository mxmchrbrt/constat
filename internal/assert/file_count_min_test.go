package assert

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNewFileCountMin_ConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{name: "relative path ok", yaml: "min: 1\npath: data/files\n"},
		{name: "no path defaults to root", yaml: "min: 1\n"},
		{name: "absolute path rejected", yaml: "min: 1\npath: /etc\n", wantErr: true},
		{name: "traversal rejected", yaml: "min: 1\npath: ../outside\n", wantErr: true},
		{name: "invalid glob rejected", yaml: "min: 1\nglob: \"[\"\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := mappingNode(t, tt.yaml)
			_, err := newFileCountMin(node)
			if tt.wantErr && err == nil {
				t.Fatal("expected a build-time error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFileCountMin_Check(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "files"))
	mustWriteFile(t, filepath.Join(root, "files", "a.txt"), "x")
	mustWriteFile(t, filepath.Join(root, "files", "b.txt"), "x")
	mustWriteFile(t, filepath.Join(root, "files", "c.log"), "x")

	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "d.txt"), "x")
	mustSymlink(t, filepath.Join(outside, "d.txt"), filepath.Join(root, "files", "escape-link.txt"))

	tests := []struct {
		name       string
		min        int
		path       string
		glob       string
		wantPassed bool
	}{
		{name: "count above min", min: 2, path: "files", wantPassed: true},
		{name: "count below min", min: 10, path: "files", wantPassed: false},
		{name: "count exactly at min", min: 3, path: "files", wantPassed: true}, // 3 real files, symlink excluded
		{name: "missing subpath, min>0", min: 1, path: "does-not-exist", wantPassed: false},
		{name: "missing subpath, min=0", min: 0, path: "does-not-exist", wantPassed: true},
		{name: "glob filters to txt only", min: 2, path: "files", glob: "*.txt", wantPassed: true},
		{name: "glob filters, below min", min: 3, path: "files", glob: "*.txt", wantPassed: false}, // 2 txt files, symlink excluded
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &fileCountMin{min: tt.min, path: tt.path, glob: tt.glob}
			res, err := a.Check(context.Background(), Env{RestoreDir: root})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (message: %q)", res.Passed, tt.wantPassed, res.Message)
			}
		})
	}
}

func TestFileCountMin_SubpathIsEscapingSymlink(t *testing.T) {
	root := t.TempDir()

	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret.txt"), "x")
	mustSymlink(t, outside, filepath.Join(root, "escape-dir"))

	b := &fileCountMin{min: 1, path: "escape-dir"}
	res, err := b.Check(context.Background(), Env{RestoreDir: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Passed {
		t.Error("path escaping root via a symlink must not be walked; expected 0 files found")
	}
}

func TestFileCountMin_SymlinkNeverCounted(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "files"))

	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "d.txt"), "x")
	mustSymlink(t, filepath.Join(outside, "d.txt"), filepath.Join(root, "files", "only-a-link.txt"))

	a := &fileCountMin{min: 1, path: "files"}
	res, err := a.Check(context.Background(), Env{RestoreDir: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Passed {
		t.Error("a directory containing only a symlink must count as 0 real files")
	}
}

func TestFileCountMin_ContextCancellationStopsTheWalk(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		mustWriteFile(t, filepath.Join(root, "f", string(rune('a'+i%26)), "x.txt"), "x")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := &fileCountMin{min: 1, path: "."}
	if _, err := a.Check(ctx, Env{RestoreDir: root}); err == nil {
		t.Error("expected a cancelled context to abort the walk, got no error")
	}
}

func TestFileCountMin_Requires(t *testing.T) {
	a := &fileCountMin{}
	if !a.Requires().Restore {
		t.Error("file_count_min must require a restore")
	}
}
