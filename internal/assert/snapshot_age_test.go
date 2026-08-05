package assert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/driver"
)

func TestSnapshotAge_Check(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		maxAge     time.Duration
		snapTime   time.Time
		driverErr  error
		wantErr    bool
		wantPassed bool
	}{
		{
			name:       "well within threshold",
			maxAge:     48 * time.Hour,
			snapTime:   time.Now().Add(-1 * time.Hour),
			wantPassed: true,
		},
		{
			name:       "older than threshold",
			maxAge:     48 * time.Hour,
			snapTime:   time.Now().Add(-72 * time.Hour),
			wantPassed: false,
		},
		{
			name:       "exactly at threshold passes",
			maxAge:     48 * time.Hour,
			snapTime:   time.Now().Add(-48 * time.Hour),
			wantPassed: true,
		},
		{
			name:      "driver error surfaces as error, not a failed result",
			maxAge:    48 * time.Hour,
			driverErr: errors.New("repository unreachable"),
			wantErr:   true,
		},
		{
			name:     "future snapshot timestamp is an error, not a pass",
			maxAge:   48 * time.Hour,
			snapTime: time.Now().Add(1 * time.Hour),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDriver{
				latest: &driver.Snapshot{ID: "abc123", Time: tt.snapTime},
			}
			if tt.driverErr != nil {
				d.latestErr = tt.driverErr
				d.latest = nil
			}

			a := &snapshotAge{maxAge: tt.maxAge}
			res, err := a.Check(ctx, Env{Driver: d})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got Result{Passed: %v}", res.Passed)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (message: %q)", res.Passed, tt.wantPassed, res.Message)
			}
		})
	}
}

// zero snapshots in the repository is exercised via the same error path as
// a driver error: ResticDriver.Latest already returns an error in that case,
// so no separate branch exists in snapshotAge itself.
func TestSnapshotAge_NoSnapshots(t *testing.T) {
	d := &fakeDriver{latestErr: errors.New("no snapshots in repository")}
	a := &snapshotAge{maxAge: 48 * time.Hour}

	_, err := a.Check(context.Background(), Env{Driver: d})
	if err == nil {
		t.Fatal("expected error for empty repository, got nil")
	}
}
