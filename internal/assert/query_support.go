package assert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Shared machinery for the assertions that query the restored dump.
//
// The threat here is not SQL injection: the queries come from the operator's
// own config file, and an operator who wants to run arbitrary SQL against their
// own restored backup is welcome to. The real risks are a query that hangs and
// a query that returns something other than what the assertion expects, and
// both are handled here rather than in each assertion.

// defaultQueryTimeout bounds a single query. Long enough for a count over a
// large table, short enough that a hung query does not eat the target's whole
// budget before failure mode #10 can be reported.
const defaultQueryTimeout = 30 * time.Second

// errUndefinedTable marks the one SQL failure that is a verdict rather than a
// broken run: the query named a table that is not there.
var errUndefinedTable = errors.New("relation does not exist")

// undefinedTable reports whether err is Postgres' 42P01. Distinguishing it from
// every other SQL error is what lets a missing table read as failure mode #2
// while a typo in a column name still reads as the operator's mistake — the
// alternative is picking one of those to get wrong.
func undefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}

// queryOneValue runs query and scans its single column of its single row into
// dest.
//
// Everything about the shape is checked rather than assumed. A query returning
// two columns, or two rows, or no rows at all, is a config mistake that would
// otherwise surface as a confident wrong answer — which is the one failure mode
// a verification tool cannot have.
//
// noRows is reported separately from an error because the two assertions
// disagree about what it means, and each says so for itself.
func queryOneValue(ctx context.Context, db *sql.DB, query string, timeout time.Duration, dest any) (noRows bool, err error) {
	if db == nil {
		return false, errors.New("no database connection; the target needs a verify_with block")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Read-only: a verification must not be able to modify the restored data,
	// however the operator's query is written.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, fmt.Errorf("starting read-only transaction: %w", err)
	}
	defer tx.Rollback()

	// The context deadline stops constat waiting; statement_timeout stops the
	// server working. Without the second, a cancelled query keeps burning
	// server time inside a container that is about to be torn down.
	//
	// Not parameterised because SET LOCAL does not take parameters. The value
	// is an integer this code computed, never operator text.
	ms := timeout.Milliseconds()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", ms)); err != nil {
		return false, fmt.Errorf("setting statement timeout: %w", err)
	}

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		if undefinedTable(err) {
			return false, fmt.Errorf("%w: %v", errUndefinedTable, err)
		}
		return false, fmt.Errorf("running query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return false, fmt.Errorf("reading query columns: %w", err)
	}
	if len(cols) != 1 {
		return false, fmt.Errorf("query must return exactly one column, got %d (%v)", len(cols), cols)
	}

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			if undefinedTable(err) {
				return false, fmt.Errorf("%w: %v", errUndefinedTable, err)
			}
			return false, fmt.Errorf("reading query result: %w", err)
		}
		return true, nil
	}

	if err := rows.Scan(dest); err != nil {
		return false, fmt.Errorf("query returned a value this check cannot use: %w", err)
	}

	if rows.Next() {
		return false, errors.New("query must return exactly one row, got more than one")
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("reading query result: %w", err)
	}

	return false, nil
}

// queryTimeout is the optional per-assertion timeout, shared by both query
// assertions so they cannot drift apart.
func queryTimeout(configured time.Duration) (time.Duration, error) {
	if configured == 0 {
		return defaultQueryTimeout, nil
	}
	if configured < 0 {
		return 0, fmt.Errorf("timeout must be positive, got %s", configured)
	}
	return configured, nil
}
