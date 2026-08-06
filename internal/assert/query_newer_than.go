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
	Register("query_newer_than", newQueryNewerThan)
}

// query_newer_than catches taxonomy #1 inside the database: the backup job
// runs nightly, the dump loads without complaint, and the newest row in it
// is from March. Only reading the data finds this — a fresh snapshot and a
// freshly written dump file both look healthy.
type queryNewerThan struct {
	query     string
	newerThan time.Duration
	timeout   time.Duration
}

type queryNewerThanParams struct {
	Query     string        `yaml:"query"`
	NewerThan time.Duration `yaml:"newer_than"`
	Timeout   time.Duration `yaml:"timeout"`
}

func newQueryNewerThan(node *yaml.Node) (Assertion, error) {
	var p queryNewerThanParams
	if err := node.Decode(&p); err != nil {
		return nil, fmt.Errorf("query_newer_than: %w", err)
	}
	if p.Query == "" {
		return nil, errors.New("query_newer_than: query is required")
	}
	if p.NewerThan <= 0 {
		return nil, fmt.Errorf("query_newer_than: newer_than is required and must be positive, got %s", p.NewerThan)
	}
	timeout, err := queryTimeout(p.Timeout)
	if err != nil {
		return nil, fmt.Errorf("query_newer_than: %w", err)
	}
	return &queryNewerThan{query: p.Query, newerThan: p.NewerThan, timeout: timeout}, nil
}

func (a *queryNewerThan) Requires() Requirements { return Requirements{Database: true} }

func (a *queryNewerThan) Check(ctx context.Context, env Env) (Result, error) {
	start := time.Now()

	var newest sql.NullTime
	noRows, err := queryOneValue(ctx, env.DB, a.query, a.timeout, &newest)

	if errors.Is(err, errUndefinedTable) {
		return Result{
			Name:     "query_newer_than",
			Passed:   false,
			Message:  fmt.Sprintf("query names a table that is not in the restored database: %v", err),
			Duration: time.Since(start),
		}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("query_newer_than: %w", err)
	}

	// NULL (MAX over an empty table) and no rows (a LIMIT 1 with nothing to
	// return) both mean nothing to date — a verdict about the backup, not a
	// malfunction of the check.
	if noRows || !newest.Valid {
		return Result{
			Name:     "query_newer_than",
			Passed:   false,
			Message:  fmt.Sprintf("query found no rows to date (newer_than %s)", a.newerThan),
			Duration: time.Since(start),
		}, nil
	}

	now := time.Now()
	if newest.Time.After(now) {
		return Result{}, fmt.Errorf("query_newer_than: newest row is dated %s in the future (clock skew?)",
			newest.Time.Sub(now).Round(time.Second))
	}

	age := now.Sub(newest.Time).Round(time.Second)

	return Result{
		Name:     "query_newer_than",
		Passed:   age <= a.newerThan,
		Message:  fmt.Sprintf("newest row is %s old (newer_than %s)", age, a.newerThan),
		Duration: time.Since(start),
	}, nil
}
