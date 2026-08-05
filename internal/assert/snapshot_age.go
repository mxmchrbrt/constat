package assert

import (
	"context"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

func init() {
	Register("newest_snapshot_age_max", newSnapshotAge)
}

type snapshotAge struct {
	maxAge time.Duration
}

func newSnapshotAge(node *yaml.Node) (Assertion, error) {
	var maxAge time.Duration
	if err := node.Decode(&maxAge); err != nil {
		return nil, fmt.Errorf("newest_snapshot_age_max: %w", err)
	}
	return &snapshotAge{maxAge: maxAge}, nil
}

func (a *snapshotAge) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	snap, err := env.Driver.Latest(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("newest_snapshot_age_max: %w", err)
	}

	rawAge := time.Since(snap.Time)
	if rawAge < 0 {
		return Result{}, fmt.Errorf("newest_snapshot_age_max: snapshot %s has a timestamp %s in the future (clock skew?)",
			snap.ID, (-rawAge).Round(time.Second))
	}

	age := rawAge.Round(time.Second)
	passed := age <= a.maxAge

	return Result{
		Name:     "newest_snapshot_age_max",
		Passed:   passed,
		Message:  fmt.Sprintf("newest snapshot %s is %s old (max %s)", snap.ID, age, a.maxAge),
		Duration: time.Since(start),
	}, nil
}
