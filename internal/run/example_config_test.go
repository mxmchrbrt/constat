package run

import (
	"testing"

	"github.com/mxmchrbrt/constat/internal/config"
)

// The shipped example is documentation people copy. It has to parse and every
// assertion in it has to build, or the first thing a new user does is hit an
// error the author never saw.
func TestExampleConfigLoadsAndBuilds(t *testing.T) {
	cfg, err := config.Load("../../constat.yaml.example")
	if err != nil {
		t.Fatalf("constat.yaml.example does not load: %v", err)
	}
	if len(cfg.Targets) == 0 {
		t.Fatal("example config has no targets")
	}

	for _, target := range cfg.Targets {
		if _, err := buildAssertions(target.Assert); err != nil {
			t.Errorf("target %q: %v", target.Name, err)
		}
	}
}
