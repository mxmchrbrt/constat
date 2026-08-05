package assert

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

func init() {
	Register("file_count_min", newFileCountMin)
}

// file_count_min catches taxonomy #2: a volume or database was never added
// to the include list, so the backup is healthy and incomplete.
type fileCountMin struct {
	min  int
	path string // cleaned, relative to the restore root
	glob string // optional filename pattern; empty means every file counts
}

type fileCountMinParams struct {
	Min  int    `yaml:"min"`
	Path string `yaml:"path"`
	Glob string `yaml:"glob"`
}

func newFileCountMin(node *yaml.Node) (Assertion, error) {
	var p fileCountMinParams
	if err := node.Decode(&p); err != nil {
		return nil, fmt.Errorf("file_count_min: %w", err)
	}

	rel := p.Path
	if rel == "" {
		rel = "."
	}
	clean, err := validateRelPath(rel)
	if err != nil {
		return nil, fmt.Errorf("file_count_min: %w", err)
	}

	if p.Glob != "" {
		if _, err := filepath.Match(p.Glob, "probe"); err != nil {
			return nil, fmt.Errorf("file_count_min: invalid glob %q: %w", p.Glob, err)
		}
	}

	return &fileCountMin{min: p.Min, path: clean, glob: p.Glob}, nil
}

func (a *fileCountMin) Requires() Requirements { return Requirements{Restore: true} }

func (a *fileCountMin) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	resolved, found, err := resolveInRoot(env.RestoreDir, a.path)
	if err != nil {
		return Result{}, fmt.Errorf("file_count_min %q: %w", a.path, err)
	}
	if !found {
		// The subpath being entirely absent is coverage drift showing up as
		// a missing tree, not a distinct error case: count as zero.
		return Result{
			Name:     "file_count_min",
			Passed:   a.min <= 0,
			Message:  fmt.Sprintf("path %q does not exist, count 0 (min %d)", a.path, a.min),
			Duration: time.Since(start),
		}, nil
	}

	count := 0
	walkErr := filepath.WalkDir(resolved, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A restore can be large enough that the walk itself outlives the
		// target timeout. Failure mode #10 is about time.
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// Never followed: excluded from the count regardless of target.
			return nil
		}
		if a.glob != "" {
			matched, err := filepath.Match(a.glob, d.Name())
			if err != nil {
				return err
			}
			if !matched {
				return nil
			}
		}
		count++
		return nil
	})
	if walkErr != nil {
		return Result{}, fmt.Errorf("file_count_min %q: %w", a.path, walkErr)
	}

	return Result{
		Name:     "file_count_min",
		Passed:   count >= a.min,
		Message:  fmt.Sprintf("found %d file(s) at %q (min %d)", count, a.path, a.min),
		Duration: time.Since(start),
	}, nil
}
