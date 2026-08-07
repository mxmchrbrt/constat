package assert

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mxmchrbrt/constat/internal/driver"
)

func init() {
	Register("repository_check", newRepositoryCheck)
}

// repository_check catches taxonomy #4 (corruption at rest) in the region no
// restore reaches: a restore reads only the packs the restored snapshot
// references, so a pack reachable only from an older snapshot can rot through
// every green drill. `restic check` walks the whole repository.
//
// read_data_subset is opt-in because it is the expensive half. Structure
// alone — index consistency, blob accounting, pack presence and size — is
// cheap enough to run on every verification and catches truncated uploads and
// broken chains. Reading pack contents back to catch a silent bit flip costs
// bandwidth on metered storage, so it belongs on a rotation the operator
// chooses, not on by default.
type repositoryCheck struct {
	readDataSubset string
}

type repositoryCheckParams struct {
	// restic --read-data-subset syntax: "5%", "1/12", or a size like "50M".
	ReadDataSubset string `yaml:"read_data_subset"`
}

// subsetPattern mirrors restic's three accepted forms. Validated here, at
// config load, rather than left to restic: a typo would otherwise come back
// as a non-zero exit and be reported as a corrupt repository, which is the
// one false alarm this assertion must never raise.
var subsetPattern = regexp.MustCompile(`^(\d+%|\d+/\d+|\d+[kKmMgGtT]?)$`)

func newRepositoryCheck(node *yaml.Node) (Assertion, error) {
	// `- repository_check: {}` and a bare `- repository_check:` both mean
	// structure-only, which is the sane default and the common case.
	if node.Tag == "!!null" {
		return &repositoryCheck{}, nil
	}

	var p repositoryCheckParams
	if err := node.Decode(&p); err != nil {
		return nil, fmt.Errorf("repository_check: %w", err)
	}
	if p.ReadDataSubset != "" && !subsetPattern.MatchString(p.ReadDataSubset) {
		return nil, fmt.Errorf(
			"repository_check: read_data_subset %q is not a percentage (\"5%%\"), a fraction (\"1/12\"), or a size (\"50M\")",
			p.ReadDataSubset)
	}
	return &repositoryCheck{readDataSubset: p.ReadDataSubset}, nil
}

// Requires no restore: this reads the repository directly, and the whole
// point is to cover what a restore does not.
func (a *repositoryCheck) Requires() Requirements { return Requirements{} }

// AssertionName lets the runner name this check in the report when Check
// returns an error and there is no Result to read the name from.
func (a *repositoryCheck) AssertionName() string { return "repository_check" }

func (a *repositoryCheck) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	checker, ok := env.Driver.(driver.RepositoryChecker)
	if !ok {
		return Result{}, fmt.Errorf("repository_check: this source kind cannot verify a whole repository")
	}

	err := checker.CheckRepository(ctx, a.readDataSubset)

	var failure *driver.CheckFailure
	if errors.As(err, &failure) {
		return Result{
			Name:     "repository_check",
			Passed:   false,
			Message:  fmt.Sprintf("%s: %s", a.scope(), failure.Output),
			Duration: time.Since(start),
		}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("repository_check: %w", err)
	}

	return Result{
		Name:     "repository_check",
		Passed:   true,
		Message:  fmt.Sprintf("%s passed", a.scope()),
		Duration: time.Since(start),
	}, nil
}

// scope states what was actually verified, so a passing structure-only check
// never reads as a promise that every byte was compared.
func (a *repositoryCheck) scope() string {
	if a.readDataSubset == "" {
		return "repository structure (no pack data read)"
	}
	return fmt.Sprintf("repository structure and %s of pack data", a.readDataSubset)
}
