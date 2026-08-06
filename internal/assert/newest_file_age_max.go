package assert

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/mxmchrbrt/constat/internal/safepath"
	"gopkg.in/yaml.v3"
)

func init() {
	Register("newest_file_age_max", newNewestFileAge)
}

// mtimeSkewTolerance is how far a restored file's mtime may sit ahead of the
// snapshot's own timestamp before the restore is treated as untrustworthy
// rather than fresh — a file written while the backup was still running can
// legitimately be a little newer than the snapshot start.
const mtimeSkewTolerance = 5 * time.Minute

// newest_file_age_max catches taxonomy #1 on the file side: the backup job
// still runs, but stopped capturing new data months ago. The snapshot is
// fresh; the contents are not — invisible to newest_snapshot_age_max, which
// only sees the snapshot.
//
// Only mtime is used. ctime cannot be: the kernel sets it on any inode
// change and no userspace API can restore it, so every restored file
// carries the restore's own ctime.
type newestFileAge struct {
	maxAge time.Duration
	path   string // cleaned, relative to the restore root
	glob   string // optional filename pattern; empty means every file counts
}

type newestFileAgeParams struct {
	MaxAge time.Duration `yaml:"max_age"`
	Path   string        `yaml:"path"`
	Glob   string        `yaml:"glob"`
}

func newNewestFileAge(node *yaml.Node) (Assertion, error) {
	var p newestFileAgeParams
	if err := node.Decode(&p); err != nil {
		return nil, fmt.Errorf("newest_file_age_max: %w", err)
	}

	// A zero max_age (missing, or written as 0) would demand a file newer than
	// now and fail every run. That is a config mistake, not a verdict.
	if p.MaxAge <= 0 {
		return nil, fmt.Errorf("newest_file_age_max: max_age is required and must be positive, got %s", p.MaxAge)
	}

	rel := p.Path
	if rel == "" {
		rel = "."
	}
	clean, err := safepath.ValidateRelPath(rel)
	if err != nil {
		return nil, fmt.Errorf("newest_file_age_max: %w", err)
	}

	if p.Glob != "" {
		if _, err := filepath.Match(p.Glob, "probe"); err != nil {
			return nil, fmt.Errorf("newest_file_age_max: invalid glob %q: %w", p.Glob, err)
		}
	}

	return &newestFileAge{maxAge: p.MaxAge, path: clean, glob: p.Glob}, nil
}

func (a *newestFileAge) Requires() Requirements { return Requirements{Restore: true} }

func (a *newestFileAge) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	resolved, found, err := safepath.ResolveInRoot(env.RestoreDir, a.path)
	if err != nil {
		return Result{}, fmt.Errorf("newest_file_age_max %q: %w", a.path, err)
	}
	if !found {
		// Nothing was captured under this path at all: a verdict about the
		// backup, not a malfunction of the check.
		return Result{
			Name:     "newest_file_age_max",
			Passed:   false,
			Message:  fmt.Sprintf("path %q does not exist in the restored tree, nothing to date", a.path),
			Duration: time.Since(start),
		}, nil
	}

	newest, newestRel, count, err := a.walkNewest(ctx, resolved)
	if err != nil {
		return Result{}, fmt.Errorf("newest_file_age_max %q: %w", a.path, err)
	}
	if count == 0 {
		return Result{
			Name:     "newest_file_age_max",
			Passed:   false,
			Message:  fmt.Sprintf("no file matched %s, nothing to date", a.scope()),
			Duration: time.Since(start),
		}, nil
	}

	display := filepath.Join(a.path, newestRel)
	now := time.Now()

	// Both errors, never a verdict: this assertion fails towards PASS when
	// mtimes are wrong (an untrustworthy backend stamps every file at
	// restore time), which taxonomy #1 cannot afford.
	if newest.After(now) {
		return Result{}, fmt.Errorf("newest_file_age_max: restored file %q has an mtime %s in the future (clock skew?)",
			display, newest.Sub(now).Round(time.Second))
	}

	snap, err := env.Driver.Latest(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("newest_file_age_max: reading snapshot metadata to validate mtimes: %w", err)
	}
	if newest.After(snap.Time.Add(mtimeSkewTolerance)) {
		return Result{}, fmt.Errorf("newest_file_age_max: restored file %q has an mtime %s newer than snapshot %s itself, so the restore did not preserve modification times and this check cannot be trusted",
			display, newest.Sub(snap.Time).Round(time.Second), snap.ID)
	}

	// Rounded to the second so a threshold of 24h still passes a file dated
	// exactly 24h ago.
	age := now.Sub(newest).Round(time.Second)

	return Result{
		Name:   "newest_file_age_max",
		Passed: age <= a.maxAge,
		Message: fmt.Sprintf("newest file %q is %s old (max %s, %d file(s) considered %s)",
			display, age, a.maxAge, count, a.scope()),
		Duration: time.Since(start),
	}, nil
}

// walkNewest returns the newest mtime under root, the path of the file
// carrying it relative to root, and how many files were considered.
// Directories and symlinks are never considered: a directory's mtime moves for
// reasons unrelated to its contents, and a symlink's target may be anywhere,
// including outside the restore root.
func (a *newestFileAge) walkNewest(ctx context.Context, root string) (newest time.Time, newestRel string, count int, err error) {
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A restore can be large enough that the walk itself outlives the
		// target timeout. Failure mode #10 is about time.
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
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

		info, err := d.Info()
		if err != nil {
			return err
		}

		count++
		if mt := info.ModTime(); mt.After(newest) {
			newest = mt
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				rel = d.Name()
			}
			newestRel = rel
		}
		return nil
	})
	if walkErr != nil {
		return time.Time{}, "", 0, walkErr
	}
	return newest, newestRel, count, nil
}

// scope describes what the assertion looked at, for messages.
func (a *newestFileAge) scope() string {
	if a.glob == "" {
		return fmt.Sprintf("under %q", a.path)
	}
	return fmt.Sprintf("under %q matching %q", a.path, a.glob)
}
