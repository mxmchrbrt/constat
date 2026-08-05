package assert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

func init() {
	Register("query_min", newQueryMin)
}

// query_min catches taxonomy #2 (coverage drift) and #6 (retention destroyed
// it) inside the database: the dump loads perfectly and a table that should
// have rows has none, or has far fewer than it should.
//
// The dump loading is not the same claim as the dump being complete. A
// retention policy that pruned the wrong thing, or a dump taken with the wrong
// --table flags, produces a file that restores cleanly and is missing data.
type queryMin struct {
	query   string
	min     int64
	timeout time.Duration
}

type queryMinParams struct {
	Query   string        `yaml:"query"`
	Min     int64         `yaml:"min"`
	Timeout time.Duration `yaml:"timeout"`
}

func newQueryMin(node *yaml.Node) (Assertion, error) {
	var p queryMinParams
	if err := node.Decode(&p); err != nil {
		return nil, fmt.Errorf("query_min: %w", err)
	}
	if p.Query == "" {
		return nil, errors.New("query_min: query is required")
	}
	if p.Min < 0 {
		return nil, fmt.Errorf("query_min: min must not be negative, got %d", p.Min)
	}
	timeout, err := queryTimeout(p.Timeout)
	if err != nil {
		return nil, fmt.Errorf("query_min: %w", err)
	}
	return &queryMin{query: p.Query, min: p.Min, timeout: timeout}, nil
}

func (a *queryMin) Requires() Requirements { return Requirements{Database: true} }

func (a *queryMin) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	var count sql.NullInt64
	noRows, err := queryOneValue(ctx, env.DB, a.query, a.timeout, &count)

	if errors.Is(err, errUndefinedTable) {
		// The author's call, and the reason the SQLSTATE is checked at all: a
		// table that should exist and does not is failure mode #2, so it is a
		// verdict about the backup. Every other SQL error stays an error,
		// because a typo in a column name is the operator's mistake and must
		// not be reported as a missing backup.
		return Result{
			Name:     "query_min",
			Passed:   false,
			Message:  fmt.Sprintf("query names a table that is not in the restored database: %v", err),
			Duration: time.Since(start),
		}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("query_min: %w", err)
	}

	// A count query always returns exactly one non-NULL row. Getting no rows,
	// or a NULL, means the query is not a count — the operator's mistake, not
	// the backup's. Deliberately unlike query_newer_than, where NULL is the
	// normal way an empty table answers.
	if noRows {
		return Result{}, fmt.Errorf("query_min: query returned no rows, so there is no number to compare against min")
	}
	if !count.Valid {
		return Result{}, fmt.Errorf("query_min: query returned NULL, so there is no number to compare against min")
	}

	return Result{
		Name:     "query_min",
		Passed:   count.Int64 >= a.min,
		Message:  fmt.Sprintf("query returned %d (min %d)", count.Int64, a.min),
		Duration: time.Since(start),
	}, nil
}
