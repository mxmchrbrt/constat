package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/config"
	"gopkg.in/yaml.v3"
)

// The fixture corpus: deliberately broken backups, each built from scratch by a
// script, each carrying the verdicts it is expected to produce. This is what
// makes the assertions provably correct rather than plausible — a unit test
// says the code does what it does, a fixture says the tool reaches the right
// conclusion about a named, real failure.
//
// Fixtures are built, not committed: nothing binary lands in the repository and
// any fixture can be rebuilt by hand for inspection.
//
//	testdata/fixtures/stale-snapshot/build.sh /tmp/look-at-this

const fixturesDir = "../../testdata/fixtures"

// expectations mirrors a fixture's expect.yaml.
type expectations struct {
	FailureMode int    `yaml:"failure_mode"`
	Title       string `yaml:"title"`
	Notes       string `yaml:"notes"`

	// TargetPasses is the overall verdict for the target.
	TargetPasses bool `yaml:"target_passes"`

	// Asserts is the target's assert: block, in constat's own config format.
	Asserts []yaml.Node `yaml:"asserts"`

	// Verdicts is the sequence of pass/fail/error lines the run should print.
	// It can be shorter than Asserts: a failed restore stops the target before
	// any assertion reports.
	Verdicts []string `yaml:"verdicts"`
}

func TestFixtureCorpus(t *testing.T) {
	requireRestic(t)

	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatalf("reading fixture directory: %v", err)
	}

	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		found++

		t.Run(e.Name(), func(t *testing.T) {
			runFixture(t, filepath.Join(fixturesDir, e.Name()))
		})
	}

	if found == 0 {
		t.Fatal("no fixtures found; the corpus is the point of this test")
	}
}

func runFixture(t *testing.T, dir string) {
	t.Helper()

	var want expectations
	raw, err := os.ReadFile(filepath.Join(dir, "expect.yaml"))
	if err != nil {
		t.Fatalf("reading expectations: %v", err)
	}
	if err := yaml.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parsing expectations: %v", err)
	}
	if len(want.Asserts) == 0 {
		t.Fatal("fixture declares no assertions")
	}
	t.Logf("failure mode #%d: %s", want.FailureMode, want.Title)

	built := t.TempDir()
	build := exec.Command("sh", filepath.Join(dir, "build.sh"), built)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fixture: %v (output: %s)", err, out)
	}

	backupPath, err := os.ReadFile(filepath.Join(built, "backup-path"))
	if err != nil {
		t.Fatalf("reading the fixture's backup path: %v", err)
	}

	tgt := config.Target{
		Name:    filepath.Base(dir),
		Source:  config.Source{Kind: "restic", Repo: filepath.Join(built, "repo"), PasswordFile: filepath.Join(built, "pass")},
		Restore: config.Restore{StripPrefix: strings.TrimSpace(string(backupPath))},
		Assert:  want.Asserts,
		Timeout: 2 * time.Minute,
	}

	var passed bool
	out := captureStdout(t, func() { passed = Target(context.Background(), tgt) })
	t.Logf("output:\n%s", out)

	if passed != want.TargetPasses {
		t.Errorf("target passed = %v, want %v", passed, want.TargetPasses)
	}

	got := verdictsIn(out)
	if len(got) != len(want.Verdicts) {
		t.Fatalf("got %d verdict lines %v, want %d %v", len(got), got, len(want.Verdicts), want.Verdicts)
	}
	for i := range got {
		if got[i] != want.Verdicts[i] {
			t.Errorf("verdict %d = %s, want %s", i, got[i], want.Verdicts[i])
		}
	}
}

// verdictsIn extracts the pass/fail/error sequence from a target's output.
func verdictsIn(out string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "PASS"):
			got = append(got, "pass")
		case strings.HasPrefix(line, "FAIL"):
			got = append(got, "fail")
		case strings.HasPrefix(line, "ERROR"):
			got = append(got, "error")
		}
	}
	return got
}
