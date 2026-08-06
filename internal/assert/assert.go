package assert

import (
	"context"
	"database/sql"

	"github.com/mxmchrbrt/constat/internal/driver"
)

type Env struct {
	Driver driver.Driver

	// The root of the restored tree. Empty unless some assertion declared
	// Requirements.Restore.
	RestoreDir string

	// A connection to the disposable database the target's dump was loaded
	// into. Nil unless some assertion declared Requirements.Database. A live
	// connection rather than a container handle, so query assertions get a
	// typed, checked scan and a statement timeout instead of parsing text.
	DB *sql.DB
}

// Requirements is what an assertion needs the runner to set up before Check
// is called. A struct rather than a bool so a new requirement widens this
// type instead of the Assertion interface, which every assertion implements.
type Requirements struct {
	// The assertion reads the restored files, so the runner must restore
	// the snapshot and set Env.RestoreDir. Metadata-only checks leave this
	// false — failure mode #1 must not require restoring 500 GB.
	Restore bool

	// The assertion queries the restored dump, so the runner must bring up
	// the target's verify_with container and set Env.DB. Implies Restore.
	Database bool
}

type Assertion interface {
	// Answered by every assertion, deliberately: an author who forgets to
	// declare a restore would otherwise get an empty RestoreDir and a
	// confusing FAIL rather than a compile error.
	Requires() Requirements

	Check(ctx context.Context, env Env) (Result, error)
}

// AnyRequiresRestore reports whether any assertion in as needs a restored
// tree. A database requirement counts: the dump is restored before loading.
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
// database.
func AnyRequiresDatabase(as []Assertion) bool {
	for _, a := range as {
		if a.Requires().Database {
			return true
		}
	}
	return false
}
