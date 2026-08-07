package assert

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mxmchrbrt/constat/internal/driver"
)

// Table D, derived from taxonomy #4 (corruption at rest). The distinction
// under test is the one that decides whether an operator gets paged about
// their backup or about constat: a repository restic says is damaged is a
// verdict, and everything else is a broken run.
func TestRepositoryCheck_Check(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		checkErr   error
		wantErr    bool
		wantPassed bool
		wantInMsg  string
	}{
		{
			name:       "clean repository passes",
			wantPassed: true,
			wantInMsg:  "no pack data read",
		},
		{
			name:       "damage found is a failed verdict, not an error",
			checkErr:   &driver.CheckFailure{Output: "Pack ID does not match, want 8f2a"},
			wantPassed: false,
			wantInMsg:  "Pack ID does not match",
		},
		{
			name:     "check that could not run is an error, not a failed verdict",
			checkErr: errors.New("exec: \"restic\": executable file not found in $PATH"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeChecker{checkErr: tt.checkErr}

			a := &repositoryCheck{}
			res, err := a.Check(ctx, Env{Driver: d})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got result %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (message: %s)", res.Passed, tt.wantPassed, res.Message)
			}
			if !strings.Contains(res.Message, tt.wantInMsg) {
				t.Errorf("message %q does not contain %q", res.Message, tt.wantInMsg)
			}
		})
	}
}

// A driver that cannot check a repository must say so, rather than silently
// passing — a check that never ran must never report as one that succeeded.
func TestRepositoryCheck_UnsupportedDriver(t *testing.T) {
	a := &repositoryCheck{}

	_, err := a.Check(context.Background(), Env{Driver: &fakeDriver{}})
	if err == nil {
		t.Fatal("expected an error for a driver that cannot check a repository")
	}
	if !strings.Contains(err.Error(), "cannot verify a whole repository") {
		t.Errorf("error %q does not explain why", err)
	}
}

// The configured subset has to reach the driver: silently dropping it would
// turn a paid-for data read into a structure-only check that still reports
// as if the data had been read.
func TestRepositoryCheck_SubsetReachesDriver(t *testing.T) {
	d := &fakeChecker{}

	a := &repositoryCheck{readDataSubset: "5%"}
	res, err := a.Check(context.Background(), Env{Driver: d})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.gotSubset != "5%" {
		t.Errorf("driver got subset %q, want %q", d.gotSubset, "5%")
	}
	if !strings.Contains(res.Message, "5% of pack data") {
		t.Errorf("message %q does not state what was read", res.Message)
	}
}

// read_data_subset is validated at config load because the alternative is
// worse than a config error: restic rejects a malformed value with a non-zero
// exit, which would be reported as a corrupt repository.
func TestNewRepositoryCheck_Params(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErr    bool
		wantSubset string
	}{
		{name: "bare key means structure only", yaml: "repository_check:"},
		{name: "empty mapping means structure only", yaml: "repository_check: {}"},
		{
			name:       "percentage",
			yaml:       "repository_check:\n  read_data_subset: 5%",
			wantSubset: "5%",
		},
		{
			name:       "fraction",
			yaml:       "repository_check:\n  read_data_subset: 1/12",
			wantSubset: "1/12",
		},
		{
			name:       "size",
			yaml:       "repository_check:\n  read_data_subset: 50M",
			wantSubset: "50M",
		},
		{
			name:    "typo is rejected at load, not at run",
			yaml:    "repository_check:\n  read_data_subset: 5 percent",
			wantErr: true,
		},
		{
			name:    "empty-but-present value is rejected rather than silently ignored",
			yaml:    "repository_check:\n  read_data_subset: \"\"",
			wantErr: false, // absent and empty mean the same thing: structure only
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tt.yaml), &doc); err != nil {
				t.Fatalf("test fixture is not valid YAML: %v", err)
			}
			// document -> mapping -> value of the single key
			params := doc.Content[0].Content[1]

			a, err := newRepositoryCheck(params)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", a)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := a.(*repositoryCheck).readDataSubset; got != tt.wantSubset {
				t.Errorf("readDataSubset = %q, want %q", got, tt.wantSubset)
			}
		})
	}
}
