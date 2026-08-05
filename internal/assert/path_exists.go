package assert

import (
	"context"
	"fmt"
	"time"

	"github.com/mxmchrbrt/constat/internal/safepath"
	"gopkg.in/yaml.v3"
)

func init() {
	Register("path_exists", newPathExists)
}

// path_exists catches taxonomy #7: the restore succeeds but the app won't
// boot because a config file or expected path is missing.
type pathExists struct {
	path string // cleaned, relative to the restore root
}

func newPathExists(node *yaml.Node) (Assertion, error) {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return nil, fmt.Errorf("path_exists: %w", err)
	}
	clean, err := safepath.ValidateRelPath(raw)
	if err != nil {
		return nil, fmt.Errorf("path_exists: %w", err)
	}
	return &pathExists{path: clean}, nil
}

func (a *pathExists) Requires() Requirements { return Requirements{Restore: true} }

func (a *pathExists) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	_, found, err := safepath.ResolveInRoot(env.RestoreDir, a.path)
	if err != nil {
		return Result{}, fmt.Errorf("path_exists %q: %w", a.path, err)
	}

	return Result{
		Name:     "path_exists",
		Passed:   found,
		Message:  fmt.Sprintf("path %q exists: %v", a.path, found),
		Duration: time.Since(start),
	}, nil
}
