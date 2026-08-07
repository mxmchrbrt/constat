package assert

import (
	"context"

	"github.com/mxmchrbrt/constat/internal/driver"
)

// fakeDriver lets assertion tests run without a live restic binary,
// network, or disk access.
type fakeDriver struct {
	latest    *driver.Snapshot
	latestErr error

	restoreErr error
}

func (f *fakeDriver) Latest(ctx context.Context) (*driver.Snapshot, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	return f.latest, nil
}

func (f *fakeDriver) Restore(ctx context.Context, dest string, paths []string) error {
	return f.restoreErr
}

// fakeChecker is a driver that can also verify a whole repository. Kept
// separate from fakeDriver so the "this source kind cannot" path stays
// testable against a driver that genuinely does not implement it.
type fakeChecker struct {
	fakeDriver

	checkErr error

	// What CheckRepository was asked to read back, recorded so a test can
	// prove the configured subset reaches restic rather than being dropped.
	gotSubset string
}

func (f *fakeChecker) CheckRepository(ctx context.Context, readDataSubset string) error {
	f.gotSubset = readDataSubset
	return f.checkErr
}
