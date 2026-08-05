package run

import (
	"strings"
	"testing"

	"github.com/mxmchrbrt/constat/internal/config"
	"gopkg.in/yaml.v3"
)

func sourceOf(kind string) config.Source {
	return config.Source{Kind: kind, Repo: "/repo", PasswordFile: "/pass"}
}

func parseNodes(t *testing.T, yamlList string) []yaml.Node {
	t.Helper()
	var nodes []yaml.Node
	if err := yaml.Unmarshal([]byte(yamlList), &nodes); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return nodes
}

func TestBuildAssertions(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring, empty means no error expected
	}{
		{
			name: "single valid entry",
			yaml: "- newest_snapshot_age_max: 48h\n",
		},
		{
			name:    "unregistered assertion name",
			yaml:    "- does_not_exist: 48h\n",
			wantErr: "not registered",
		},
		{
			name:    "two keys in one entry",
			yaml:    "- newest_snapshot_age_max: 48h\n  other_key: 1\n",
			wantErr: "expected a single-key mapping",
		},
		{
			name:    "empty mapping",
			yaml:    "- {}\n",
			wantErr: "expected a single-key mapping",
		},
		{
			name:    "entry is a scalar, not a mapping",
			yaml:    "- just_a_string\n",
			wantErr: "expected a single-key mapping",
		},
		{
			name:    "entry is a sequence, not a mapping",
			yaml:    "- [a, b]\n",
			wantErr: "expected a single-key mapping",
		},
		{
			name:    "null value for the assertion params",
			yaml:    "- newest_snapshot_age_max:\n",
			wantErr: "", // null decodes to zero Duration; not a shape error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := parseNodes(t, tt.yaml)
			_, err := buildAssertions(nodes)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestBuildAssertions_Empty(t *testing.T) {
	as, err := buildAssertions(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(as) != 0 {
		t.Errorf("got %d assertions, want 0", len(as))
	}
}

func TestBuildDriver_UnknownKind(t *testing.T) {
	_, err := buildDriver(sourceOf("borg"))
	if err == nil {
		t.Fatal("expected error for unknown source kind, got nil")
	}
	if !strings.Contains(err.Error(), "unknown source kind") {
		t.Errorf("error = %q, want it to name the unknown kind", err.Error())
	}
}

func TestBuildDriver_Restic(t *testing.T) {
	d, err := buildDriver(sourceOf("restic"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil {
		t.Fatal("expected a non-nil driver")
	}
}
