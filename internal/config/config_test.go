package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
targets:
  - name: lab-files
    source:
      kind: restic
      repo: /repo
      password_file: /pass
    assert:
      - newest_snapshot_age_max: 48h
`

func load(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return Load(path)
}

func TestLoad_Valid(t *testing.T) {
	cfg, err := load(t, validYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "lab-files" {
		t.Errorf("Name = %q, want lab-files", cfg.Targets[0].Name)
	}
}

func TestLoad_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "no targets",
			yaml:    "targets: []\n",
			wantErr: "no targets",
		},
		{
			name: "missing name",
			yaml: `
targets:
  - source:
      kind: restic
      repo: /repo
      password_file: /pass
    assert:
      - newest_snapshot_age_max: 48h
`,
			wantErr: "name is required",
		},
		{
			name: "duplicate names",
			yaml: `
targets:
  - name: dup
    source:
      kind: restic
      repo: /repo
      password_file: /pass
    assert:
      - newest_snapshot_age_max: 48h
  - name: dup
    source:
      kind: restic
      repo: /repo2
      password_file: /pass2
    assert:
      - newest_snapshot_age_max: 48h
`,
			wantErr: "duplicate target name",
		},
		{
			name: "empty assert list",
			yaml: `
targets:
  - name: lab-files
    source:
      kind: restic
      repo: /repo
      password_file: /pass
    assert: []
`,
			wantErr: "assert list is empty",
		},
		{
			name: "missing source.kind",
			yaml: `
targets:
  - name: lab-files
    source:
      repo: /repo
      password_file: /pass
    assert:
      - newest_snapshot_age_max: 48h
`,
			wantErr: "source.kind is required",
		},
		{
			name: "missing source.repo",
			yaml: `
targets:
  - name: lab-files
    source:
      kind: restic
      password_file: /pass
    assert:
      - newest_snapshot_age_max: 48h
`,
			wantErr: "source.repo is required",
		},
		{
			name: "missing password_file",
			yaml: `
targets:
  - name: lab-files
    source:
      kind: restic
      repo: /repo
    assert:
      - newest_snapshot_age_max: 48h
`,
			wantErr: "source.password_file is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	_, err := load(t, "targets: [this is not valid: yaml: at all")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoad_DuplicateNameErrorCitesLine(t *testing.T) {
	_, err := load(t, `
targets:
  - name: dup
    source:
      kind: restic
      repo: /repo
      password_file: /pass
    assert:
      - newest_snapshot_age_max: 48h
  - name: dup
    source:
      kind: restic
      repo: /repo2
      password_file: /pass2
    assert:
      - newest_snapshot_age_max: 48h
`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("error = %q, want it to cite a line number", err.Error())
	}
}
