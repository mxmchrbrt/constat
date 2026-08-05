package assert

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// scalarNode builds a *yaml.Node the way the config unpacker would hand one
// to a factory for a scalar assert entry (e.g. `path_exists: config.php`).
func scalarNode(t *testing.T, value string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := n.Encode(value); err != nil {
		t.Fatalf("encoding scalar node: %v", err)
	}
	return &n
}

// mappingNode builds a *yaml.Node from a YAML mapping literal, the way the
// config unpacker would hand one to a factory for e.g. file_count_min's params.
func mappingNode(t *testing.T, yamlBody string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(yamlBody), &n); err != nil {
		t.Fatalf("parsing mapping node: %v", err)
	}
	// yaml.Unmarshal into a Node gives a DocumentNode; unwrap to its content.
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return n.Content[0]
	}
	return &n
}

func TestNewPathExists_ConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{name: "relative path ok", yaml: "config.php"},
		{name: "nested relative path ok", yaml: "data/config/app.php"},
		{name: "absolute path rejected", yaml: "/etc/passwd", wantErr: true},
		{name: "traversal rejected", yaml: "../../etc/passwd", wantErr: true},
		{name: "empty path rejected", yaml: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := scalarNode(t, tt.yaml)
			_, err := newPathExists(node)
			if tt.wantErr && err == nil {
				t.Fatal("expected a build-time error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPathExists_Check(t *testing.T) {
	root := t.TempDir()

	mustWriteFile(t, filepath.Join(root, "config.php"), "x")
	mustMkdir(t, filepath.Join(root, "data"))

	// symlink resolving inside root
	mustSymlink(t, filepath.Join(root, "config.php"), filepath.Join(root, "inside-link"))

	// symlink resolving outside root
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret"), "x")
	mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "outside-link"))

	tests := []struct {
		name       string
		path       string
		wantPassed bool
	}{
		{name: "regular file exists", path: "config.php", wantPassed: true},
		{name: "directory exists", path: "data", wantPassed: true},
		{name: "missing path", path: "does-not-exist", wantPassed: false},
		{name: "symlink resolving inside root", path: "inside-link", wantPassed: true},
		{name: "symlink resolving outside root", path: "outside-link", wantPassed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &pathExists{path: tt.path}
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

func TestPathExists_Requires(t *testing.T) {
	a := &pathExists{path: "x"}
	if !a.Requires().Restore {
		t.Error("path_exists must require a restore")
	}
}

// --- shared test helpers ---

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}
