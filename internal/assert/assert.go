package assert

import (
	"context"
	"database/sql"

	"github.com/mxmchrbrt/constat/internal/driver"
)

type Env struct {
	Driver driver.Driver

	// RestoreDir is the root of the restored tree. Empty unless some
	// assertion on the target declared Requirements.Restore.
	RestoreDir string

	// DB is a connection to the disposable database the target's dump was
	// loaded into. Nil unless some assertion declared Requirements.Database.
	//
	// A live connection rather than a container handle: query assertions want
	// a typed, checked scan and a statement timeout, and parsing psql's text
	// output would give neither. The container is an implementation detail of
	// getting here and assertions never see it.
	DB *sql.DB
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

	// Database means the assertion queries the restored dump, so the runner
	// must bring up the target's verify_with container, load the dump, and set
	// Env.DB. Implies Restore: the dump comes out of the restored tree.
	//
	// This is the second field the struct was designed for — see
	// ARCHITECTURE.md on why Requirements is a struct and not a bool.
	Database bool
}

type Assertion interface {
	// Requires is answered by every assertion, deliberately: an author who
	// forgets to declare a restore would otherwise get an empty RestoreDir
	// and a confusing FAIL rather than a compile error.
	Requires() Requirements

	Check(ctx context.Context, env Env) (Result, error)
}

// AnyRequiresRestore reports whether any assertion in as needs a restored tree.
// A database requirement counts: the dump is restored before it is loaded.
func AnyRequiresRestore(as []Assertion) bool {
	for _, a := range as {
		r := a.Requires()
		if r.Restore || r.Database {
			return true
		}
	}
	return false
}

// AnyRequiresDatabase reports whether any assertion in as needs a live
// database. A target where none does never starts a container.
func AnyRequiresDatabase(as []Assertion) bool {
	for _, a := range as {
		if a.Requires().Database {
			return true
		}
	}
	return false
}
