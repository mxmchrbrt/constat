package assert

import (
	"context"

	"github.com/mxmchrbrt/constat/internal/driver"
)

type Env struct {
	Driver driver.Driver

	// RestoreDir is the root of the restored tree. Empty unless some
	// assertion on the target declared Requirements.Restore.
	RestoreDir string
}

// Requirements is what an assertion needs the runner to set up before Check is
// called. Kept as a struct rather than a bool so new requirements (a live
// database container, session 7) widen this type instead of the Assertion
// interface, which every assertion implements.
type Requirements struct {
	// Restore means the assertion reads the restored files, so the runner
	// must restore the snapshot to a directory and set Env.RestoreDir.
	// Metadata-only checks leave this false: failure mode #1 must not
	// require restoring 500 GB.
	Restore bool
}

type Assertion interface {
	// Requires is answered by every assertion, deliberately: an author who
	// forgets to declare a restore would otherwise get an empty RestoreDir
	// and a confusing FAIL rather than a compile error.
	Requires() Requirements

	Check(ctx context.Context, env Env) (Result, error)
}

// AnyRequiresRestore reports whether any assertion in as needs a restored tree.
func AnyRequiresRestore(as []Assertion) bool {
	for _, a := range as {
		if a.Requires().Restore {
			return true
		}
	}
	return false
}
