// Package run wires config, the assertion registry, and drivers together to
// execute a target. Kept separate from config so config stays dependency-free
// (schema only) and separate from assert so the assertion model doesn't need
// to know how targets are loaded.
package run

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mxmchrbrt/constat/internal/assert"
	"github.com/mxmchrbrt/constat/internal/config"
	"github.com/mxmchrbrt/constat/internal/driver"
	"gopkg.in/yaml.v3"
)

func buildDriver(src config.Source) (driver.Driver, error) {
	switch src.Kind {
	case "restic":
		return driver.NewResticDriver(src.Repo, src.PasswordFile), nil
	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}
}

// newDriver is a seam: tests swap it to run the full Target path against a
// fake driver without a live restic binary.
var newDriver = buildDriver

// buildAssertions turns the undecoded assert: entries of a target into
// assertions. Each entry must be a single-key mapping: the key names a
// registered assertion, the value is its parameters, left for the factory
// to decode.
func buildAssertions(nodes []yaml.Node) ([]assert.Assertion, error) {
	assertions := make([]assert.Assertion, 0, len(nodes))

	for i := range nodes {
		n := &nodes[i]

		if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
			return nil, fmt.Errorf("assert entry %d at line %d: expected a single-key mapping", i, n.Line)
		}

		nameNode, paramsNode := n.Content[0], n.Content[1]

		a, err := assert.Build(nameNode.Value, paramsNode)
		if err != nil {
			return nil, fmt.Errorf("assert %q at line %d: %w", nameNode.Value, nameNode.Line, err)
		}

		assertions = append(assertions, a)
	}

	return assertions, nil
}

// restoreInto creates a disposable directory, restores the target's snapshot
// into it, and returns the directory plus a cleanup func. The cleanup func is
// returned even when the restore fails, so the caller can always defer it
// without checking the error first.
func restoreInto(ctx context.Context, d driver.Driver, t config.Target) (dir string, elapsed time.Duration, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "constat-restore-")
	if err != nil {
		return "", 0, func() {}, fmt.Errorf("creating restore directory: %w", err)
	}

	// MkdirTemp already creates 0700, but the restored tree carries customer
	// data and this is cheap to make explicit rather than inherited.
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", 0, func() {}, fmt.Errorf("securing restore directory: %w", err)
	}

	cleanup = func() { os.RemoveAll(dir) }

	start := time.Now()
	if err := d.Restore(ctx, dir, t.Restore.Paths); err != nil {
		return dir, time.Since(start), cleanup, fmt.Errorf("restoring: %w", err)
	}
	return dir, time.Since(start), cleanup, nil
}

// Target builds the driver and assertions for t, restores if any assertion
// needs files, runs every assertion, and reports PASS/FAIL/ERROR per assertion
// to stdout. One broken assertion does not stop the others. Returns false if
// the target as a whole failed.
//
// The restore directory is always removed, including on the error and panic
// paths: a verification tool that leaves scratch space behind after an
// interrupt is its own bug report.
func Target(ctx context.Context, t config.Target) bool {
	ctx, cancel := context.WithTimeout(ctx, t.EffectiveTimeout())
	defer cancel()

	d, err := newDriver(t.Source)
	if err != nil {
		fmt.Printf("ERROR %s: building driver: %v\n", t.Name, err)
		return false
	}

	assertions, err := buildAssertions(t.Assert)
	if err != nil {
		fmt.Printf("ERROR %s: building assertions: %v\n", t.Name, err)
		return false
	}

	env := assert.Env{Driver: d}

	if assert.AnyRequiresRestore(assertions) {
		dir, elapsed, cleanup, err := restoreInto(ctx, d, t)
		defer cleanup()
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", t.Name, err)
			return false
		}
		env.RestoreDir = dir
		fmt.Printf("      %s: restored in %s\n", t.Name, elapsed.Round(time.Millisecond))
	}

	passed := true

	for _, a := range assertions {
		res, err := a.Check(ctx, env)
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", t.Name, err)
			passed = false
			continue
		}

		status := "PASS "
		if !res.Passed {
			status = "FAIL "
			passed = false
		}
		fmt.Printf("%s %s: %s\n", status, t.Name, res.Message)
	}

	return passed
}
