package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mxmchrbrt/constat/internal/safepath"
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

func TestLoad_StripPrefix(t *testing.T) {
	withPrefix := func(prefix string) string {
		return `
targets:
  - name: lab-files
    source:
      kind: restic
      repo: /repo
      password_file: /pass
    restore:
      strip_prefix: ` + prefix + `
    assert:
      - path_exists: config.php
`
	}

	tests := []struct {
		name    string
		prefix  string
		want    string // expected normalised form, when valid
		wantErr string
	}{
		{name: "absolute backup path", prefix: "/home/app/data", want: "home/app/data"},
		{name: "trailing separator tolerated", prefix: `"/home/app/data/"`, want: "home/app/data"},
		{name: "already relative", prefix: "home/app/data", want: "home/app/data"},
		{name: "filesystem root rejected", prefix: `"/"`, wantErr: "whole filesystem root"},
		{name: "relative traversal rejected", prefix: "../../etc", wantErr: "escapes the restore root"},
		{
			// An absolute prefix cannot escape: Clean resolves ".." against
			// the root before the leading separator is trimmed, so this is
			// /etc and therefore "etc" inside the restored tree.
			name:   "traversal inside an absolute prefix is resolved, not rejected",
			prefix: "/home/../../etc", want: "etc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(t, withPrefix(tt.prefix))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got none", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), "line") {
					t.Errorf("error %q does not cite a line number", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// The raw value is what round-trips; normalisation happens where
			// it is used, against a real restored tree. Pin both.
			raw := cfg.Targets[0].Restore.StripPrefix
			if raw == "" {
				t.Fatal("strip_prefix was dropped during decode")
			}
			rel, err := safepath.RelFromBackupPath(raw)
			if err != nil {
				t.Fatalf("a config that loaded cleanly failed to normalise: %v", err)
			}
			if rel != tt.want {
				t.Errorf("normalised %q to %q, want %q", raw, rel, tt.want)
			}
		})
	}
}

func TestLoad_VerifyWith(t *testing.T) {
	withBlock := func(block string) string {
		return `
targets:
  - name: app-db
    source:
      kind: restic
      repo: /repo
      password_file: /pass
` + block + `    assert:
      - path_exists: config.php
`
	}

	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{
			name:  "no verify_with at all is fine",
			block: "",
		},
		{
			name:  "image and load",
			block: "    verify_with:\n      image: postgres:16-alpine\n      load: db/dump.sql\n",
		},
		{
			name:    "image missing",
			block:   "    verify_with:\n      load: db/dump.sql\n",
			wantErr: "verify_with.image is required",
		},
		{
			name:    "load missing",
			block:   "    verify_with:\n      image: postgres:16-alpine\n",
			wantErr: "verify_with.load is required",
		},
		{
			// The dump path comes out of the restored tree, so it gets the
			// same guard as an assertion path.
			name:    "absolute load path rejected",
			block:   "    verify_with:\n      image: postgres:16-alpine\n      load: /etc/passwd\n",
			wantErr: "must be relative to the restore root",
		},
		{
			name:    "traversal in load path rejected",
			block:   "    verify_with:\n      image: postgres:16-alpine\n      load: ../../etc/passwd\n",
			wantErr: "escapes the restore root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(t, withBlock(tt.block))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got none", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), "line") {
					t.Errorf("error %q does not cite a line number", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.block == "" && cfg.Targets[0].VerifyWith != nil {
				t.Error("a target with no verify_with block must decode to nil, so no container is ever started")
			}
		})
	}
}

func TestLoad_Webhook(t *testing.T) {
	withWebhook := func(block string) string {
		return validYAML + block
	}

	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{name: "no webhook block is fine", block: ""},
		{
			name:  "url only",
			block: "webhook:\n  url: https://ntfy.sh/mytopic\n",
		},
		{
			name:  "full block",
			block: "webhook:\n  url: https://ntfy.sh/mytopic\n  format: ntfy\n  timeout: 5s\n  max_attempts: 5\n",
		},
		{
			name:    "missing url",
			block:   "webhook:\n  format: ntfy\n",
			wantErr: "webhook.url is required",
		},
		{
			name:    "negative timeout",
			block:   "webhook:\n  url: https://ntfy.sh/mytopic\n  timeout: -1s\n",
			wantErr: "webhook.timeout must be positive",
		},
		{
			name:    "negative max_attempts",
			block:   "webhook:\n  url: https://ntfy.sh/mytopic\n  max_attempts: -1\n",
			wantErr: "webhook.max_attempts must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(t, withWebhook(tt.block))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got none", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.block == "" && cfg.Webhook != nil {
				t.Error("no webhook block must decode to nil, so no webhook is ever sent")
			}
		})
	}
}

// An unrecognised format is deliberately not rejected at config load — see the
// comment on Webhook.Format. This pins that it is at least preserved through
// decoding rather than silently dropped, so the caller that does validate it
// (cmd/constat/main.go) has something to check.
func TestLoad_Webhook_UnknownFormatPreservedNotRejected(t *testing.T) {
	cfg, err := load(t, validYAML+"webhook:\n  url: https://example.test/hook\n  format: carrier-pigeon\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Webhook.Format != "carrier-pigeon" {
		t.Errorf("Format = %q, want it preserved verbatim", cfg.Webhook.Format)
	}
}

func TestLoad_Signing(t *testing.T) {
	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{name: "no signing block is fine", block: ""},
		{name: "key_file set", block: "signing:\n  key_file: /etc/constat/signing.key\n"},
		{
			name:    "empty key_file",
			block:   "signing:\n  key_file: \"\"\n",
			wantErr: "signing.key_file is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(t, validYAML+tt.block)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got none", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// No signing block must decode to nil, so a run without one is
			// unsigned rather than failing to find a key.
			if tt.block == "" && cfg.Signing != nil {
				t.Error("absent signing block must decode to nil")
			}
		})
	}
}
