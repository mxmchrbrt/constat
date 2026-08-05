package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelPath(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "simple relative path", raw: "config.php", want: "config.php"},
		{name: "nested relative path", raw: "data/config/app.php", want: "data/config/app.php"},
		{name: "current directory", raw: ".", want: "."},
		{name: "redundant components cleaned", raw: "./data//files/", want: "data/files"},
		{name: "interior traversal that stays inside", raw: "data/../data/files", want: "data/files"},
		{name: "empty rejected", raw: "", wantErr: true},
		{name: "absolute rejected", raw: "/etc/passwd", wantErr: true},
		{name: "traversal rejected", raw: "../outside", wantErr: true},
		{name: "bare traversal rejected", raw: "..", wantErr: true},
		{name: "traversal via interior components rejected", raw: "data/../../outside", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateRelPath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateRelPath(%q) = %q, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateRelPath(%q): unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ValidateRelPath(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestResolveInRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	mustWrite(t, filepath.Join(root, "config.php"))
	mustWrite(t, filepath.Join(root, "data", "app.php"))
	mustWrite(t, filepath.Join(outside, "secret"))

	mustSymlink(t, filepath.Join(root, "config.php"), filepath.Join(root, "inside-link"))
	mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "escaping-link"))
	mustSymlink(t, outside, filepath.Join(root, "escaping-dir"))
	mustSymlink(t, filepath.Join(root, "nowhere"), filepath.Join(root, "broken-link"))

	tests := []struct {
		name      string
		rel       string
		wantFound bool
	}{
		{name: "regular file", rel: "config.php", wantFound: true},
		{name: "nested file", rel: "data/app.php", wantFound: true},
		{name: "the root itself", rel: ".", wantFound: true},
		{name: "missing path", rel: "does-not-exist", wantFound: false},
		{name: "symlink resolving inside root", rel: "inside-link", wantFound: true},
		{name: "symlink resolving outside root", rel: "escaping-link", wantFound: false},
		{name: "symlinked directory outside root", rel: "escaping-dir", wantFound: false},
		{name: "file reached through an escaping directory symlink", rel: "escaping-dir/secret", wantFound: false},
		{name: "dangling symlink", rel: "broken-link", wantFound: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, found, err := ResolveInRoot(root, tt.rel)
			if err != nil {
				t.Fatalf("ResolveInRoot(%q): unexpected error: %v", tt.rel, err)
			}
			if found != tt.wantFound {
				t.Fatalf("ResolveInRoot(%q) found = %v, want %v (resolved %q)", tt.rel, found, tt.wantFound, resolved)
			}
			if !found {
				return
			}
			// Anything reported as found must be usable directly, and must
			// still be inside the root after symlink resolution.
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("resolved path %q is not usable: %v", resolved, err)
			}
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatalf("resolving root: %v", err)
			}
			if rel, err := filepath.Rel(realRoot, resolved); err != nil || rel == ".." || filepath.IsAbs(rel) {
				t.Errorf("resolved path %q is not inside root %q", resolved, realRoot)
			}
		})
	}
}

// A root that is itself reached through a symlink must not make every path
// under it look like an escape — /tmp is a symlink to /private/tmp on macOS,
// and the same shape shows up wherever a restore directory sits under one.
func TestResolveInRoot_SymlinkedRootIsNotAnEscape(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")

	mustWrite(t, filepath.Join(real, "config.php"))
	mustSymlink(t, real, link)

	resolved, found, err := ResolveInRoot(link, "config.php")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("path under a symlinked root reported as not found (resolved %q)", resolved)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}
