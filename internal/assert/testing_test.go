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

func (f *fakeDriver) Restore(ctx context.Context, dest string) error {
	return f.restoreErr
}
