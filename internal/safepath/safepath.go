// Package safepath holds the guards that keep a path taken from a config file,
// or read out of a restored tree, from reaching outside that tree.
//
// It is its own package because two callers need it and they must not disagree:
// the assertions, which resolve operator-supplied paths inside the restore, and
// the runner, which resolves a target's restore.strip_prefix the same way. A
// restored tree is untrusted input — it came out of a backup that may itself be
// what is broken — so a symlink in it is never allowed to resolve outside the
// restore root.
package safepath

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// ValidateRelPath rejects config paths that could escape the restore root:
// absolute paths and any ".." component. Called at build time, so a hostile or
// mistaken config path fails before any restore runs.
func ValidateRelPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("path must not be empty")
	}
	if filepath.IsAbs(raw) {
		return "", fmt.Errorf("path %q must be relative to the restore root, not absolute", raw)
	}
	clean := filepath.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes the restore root", raw)
	}
	return clean, nil
}

// RelFromBackupPath converts a path as it was recorded in the backup — almost
// always absolute, since that is what backup tools store — into a path relative
// to a restore root. Restoring reproduces the original tree beneath the restore
// directory, so a snapshot of /home/app/data lands at <restoredir>/home/app/data
// and "/home/app/data" has to become "home/app/data" before anything can look
// for it.
func RelFromBackupPath(raw string) (string, error) {
	trimmed := strings.TrimLeft(filepath.Clean(raw), string(filepath.Separator))
	if trimmed == "" || trimmed == "." {
		return "", fmt.Errorf("path %q names the whole filesystem root, not a directory inside the backup", raw)
	}
	return ValidateRelPath(trimmed)
}

// ResolveInRoot resolves rel (already validated by ValidateRelPath) against
// root, following symlinks, and reports whether the final target exists and
// stays inside root. A missing path and a path that escapes root via a
// symlink both report found=false, err=nil: from the caller's side both mean
// "nothing usable here". The restored tree is untrusted input, so a symlink
// is never allowed to resolve outside root.
func ResolveInRoot(root, rel string) (resolved string, found bool, err error) {
	full := filepath.Join(root, rel)

	target, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}

	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false, err
	}

	if target != rootResolved && !strings.HasPrefix(target, rootResolved+string(filepath.Separator)) {
		return "", false, nil
	}

	return target, true, nil
}
