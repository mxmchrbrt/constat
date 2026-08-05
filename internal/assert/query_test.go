package assert

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/container"
)

// Tables A and B, derived from taxonomy #2 and #6 (query_min) and #1
// (query_newer_than), reviewed before these assertions were written. Two calls
// were the author's and are marked at the cases that carry them: a query naming
// a table that is not there is a FAIL rather than an error, split from other SQL
// failures by SQLSTATE 42P01; and a NULL timestamp — how an empty table answers
// MAX() — is also a FAIL, consistent with session 5's "nothing to date".
//
// These run against a real Postgres rather than a mock. The whole point of
// query_min is what the database says when a table is missing, and a mock would
// only ever repeat what this code already believes.

var testDB *sql.DB

func TestMain(m *testing.M) {
	os.Exit(runWithDatabase(m))
}

// runWithDatabase starts one container for the whole package. Per-test
// containers would be honest and would also add roughly fifteen seconds each.
func runWithDatabase(m *testing.M) int {
	// testing.Short() reads a flag, and flags are only parsed inside m.Run().
	// TestMain has to do it itself before asking.
	flag.Parse()

	if testing.Short() {
		return m.Run()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rt, err := container.DetectRuntime(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no container runtime, query assertion tests will skip: %v\n", err)
		return m.Run()
	}

	dir, err := os.MkdirTemp("", "constat-assert-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating scratch directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	dump := filepath.Join(dir, "seed.sql")
	if err := os.WriteFile(dump, []byte(seedDump), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "writing seed dump: %v", err)
		return 1
	}

	pg, err := container.StartPostgres(ctx, rt, testImage(), dump)
	// Armed before the error check on purpose: a container that started and
	// then failed must still be torn down, or a failing test run leaves a
	// Postgres behind.
	//lint:ignore SA5001 cleanup must be armed before the error is handled
	defer pg.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting postgres: %v\n", err)
		return 1
	}
	testDB = pg.DB

	return m.Run()
}

func testImage() string {
	if v := os.Getenv("CONSTAT_TEST_PG_IMAGE"); v != "" {
		return v
	}
	return "docker.io/library/postgres:16-alpine"
}

// seedDump is the fixture every query test reads. Timestamps are relative to
// load time so "recent" stays recent however long the image pull took.
const seedDump = `
CREATE TABLE users (
    id      integer PRIMARY KEY,
    email   text NOT NULL
);
INSERT INTO users (id, email)
SELECT g, 'user' || g || '@example.test' FROM generate_series(1, 500) g;

-- A table that exists and is empty: retention pruned it, or the dump was taken
-- with the wrong --table flags.
CREATE TABLE audit_log (
    id         integer PRIMARY KEY,
    created_at timestamptz NOT NULL
);

-- Rows written up to an hour ago: healthy.
CREATE TABLE orders (
    id         integer PRIMARY KEY,
    created_at timestamptz NOT NULL
);
INSERT INTO orders (id, created_at)
SELECT g, now() - (g || ' hours')::interval FROM generate_series(1, 24) g;

-- Loads perfectly, and nothing has been written to it since March.
CREATE TABLE sessions (
    id         integer PRIMARY KEY,
    created_at timestamptz NOT NULL
);
INSERT INTO sessions (id, created_at)
SELECT g, now() - '90 days'::interval - (g || ' hours')::interval FROM generate_series(1, 10) g;

-- A row dated in the future: a clock that jumped during capture.
CREATE TABLE skewed (
    id         integer PRIMARY KEY,
    created_at timestamptz NOT NULL
);
INSERT INTO skewed (id, created_at) VALUES (1, now() + '3 hours'::interval);
`

func requireDB(t *testing.T) Env {
	t.Helper()
	if testing.Short() {
		t.Skip("query assertions need a live database; skipped under -short")
	}
	if testDB == nil {
		t.Skip("no container runtime; skipping query assertion test")
	}
	return Env{DB: testDB}
}

func TestQueryMin_Check(t *testing.T) {
	env := requireDB(t)

	tests := []struct {
		name  string
		query string
		min   int64
		want  verdict
		// wantErrContains pins which failure was reported, since several
		// distinct problems otherwise look alike from a verdict alone.
		wantErrContains string
	}{
		{name: "count well above min", query: "SELECT count(*) FROM users", min: 1, want: wantPass},
		{
			// Taxonomy #6: the table survived the restore, the rows did not.
			name: "table exists but is empty", query: "SELECT count(*) FROM audit_log", min: 1, want: wantFail,
		},
		{name: "count exactly at min", query: "SELECT count(*) FROM users", min: 500, want: wantPass},
		{name: "count just below min", query: "SELECT count(*) FROM users", min: 501, want: wantFail},
		{
			// The author's call: a table that should be there and is not is
			// taxonomy #2, so it is a verdict about the backup.
			name: "table does not exist", query: "SELECT count(*) FROM no_such_table", min: 1, want: wantFail,
		},
		{
			// And the other half of that call: anything else that will not
			// execute is the operator's mistake, not the backup's.
			name: "column does not exist", query: "SELECT count(no_such_column) FROM users", min: 1,
			want: wantErr, wantErrContains: "running query",
		},
		{
			name: "syntax error", query: "SELECT count(*) FRM users", min: 1,
			want: wantErr, wantErrContains: "running query",
		},
		{
			name: "query returns NULL", query: "SELECT max(id) FROM audit_log", min: 1,
			want: wantErr, wantErrContains: "returned NULL",
		},
		{
			name: "query returns no rows", query: "SELECT id FROM users WHERE false", min: 1,
			want: wantErr, wantErrContains: "no rows",
		},
		{
			name: "query returns two columns", query: "SELECT id, email FROM users LIMIT 1", min: 1,
			want: wantErr, wantErrContains: "exactly one column",
		},
		{
			name: "query returns two rows", query: "SELECT id FROM users LIMIT 2", min: 1,
			want: wantErr, wantErrContains: "exactly one row",
		},
		{
			name: "query returns text", query: "SELECT email FROM users LIMIT 1", min: 1,
			want: wantErr, wantErrContains: "cannot use",
		},
		{name: "min of zero passes trivially", query: "SELECT count(*) FROM audit_log", min: 0, want: wantPass},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &queryMin{query: tt.query, min: tt.min, timeout: defaultQueryTimeout}
			res, err := a.Check(context.Background(), env)

			got := wantPass
			switch {
			case err != nil:
				got = wantErr
			case !res.Passed:
				got = wantFail
			}
			if got != tt.want {
				t.Fatalf("got %s, want %s (message %q, err %v)", got, tt.want, res.Message, err)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error %q does not mention %q", err, tt.wantErrContains)
			}
		})
	}
}

// A query that hangs must be stopped, not waited on. Failure mode #10 is about
// time, and a check that never returns is indistinguishable from one that
// passed.
func TestQueryMin_SlowQueryIsStopped(t *testing.T) {
	env := requireDB(t)

	a := &queryMin{query: "SELECT count(*) FROM pg_sleep(30)", min: 1, timeout: 2 * time.Second}

	start := time.Now()
	_, err := a.Check(context.Background(), env)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a slow query to be stopped")
	}
	if elapsed > 10*time.Second {
		t.Errorf("query ran for %s; the timeout was not enforced", elapsed)
	}
}

// The operator's query runs in a read-only transaction: a verification must not
// be able to modify the data it is verifying, however the query is written.
func TestQueryMin_CannotWrite(t *testing.T) {
	env := requireDB(t)

	a := &queryMin{
		query:   "INSERT INTO audit_log (id, created_at) VALUES (1, now()) RETURNING id",
		min:     1,
		timeout: defaultQueryTimeout,
	}
	if _, err := a.Check(context.Background(), env); err == nil {
		t.Fatal("a write must not be allowed from an assertion")
	}

	var n int
	if err := testDB.QueryRow("SELECT count(*) FROM audit_log").Scan(&n); err != nil {
		t.Fatalf("checking the table: %v", err)
	}
	if n != 0 {
		t.Errorf("the assertion wrote %d row(s) into the restored data", n)
	}
}

func TestQueryNewerThan_Check(t *testing.T) {
	env := requireDB(t)

	const day = 24 * time.Hour

	tests := []struct {
		name            string
		query           string
		newerThan       time.Duration
		want            verdict
		wantErrContains string
	}{
		{
			name: "newest row is recent", query: "SELECT max(created_at) FROM orders",
			newerThan: day, want: wantPass,
		},
		{
			// The headline case: the dump loads perfectly and the data stopped
			// three months ago.
			name: "newest row is three months old", query: "SELECT max(created_at) FROM sessions",
			newerThan: day, want: wantFail,
		},
		{
			// The author's call: an empty table answers MAX() with NULL, and
			// nothing to date is a verdict, not a malfunction.
			name: "empty table returns NULL", query: "SELECT max(created_at) FROM audit_log",
			newerThan: day, want: wantFail,
		},
		{
			name: "query returns no rows", query: "SELECT created_at FROM audit_log LIMIT 1",
			newerThan: day, want: wantFail,
		},
		{
			name: "timestamp in the future", query: "SELECT max(created_at) FROM skewed",
			newerThan: day, want: wantErr, wantErrContains: "in the future",
		},
		{
			name: "table does not exist", query: "SELECT max(created_at) FROM no_such_table",
			newerThan: day, want: wantFail,
		},
		{
			name: "column is not a timestamp", query: "SELECT max(email) FROM users",
			newerThan: day, want: wantErr, wantErrContains: "cannot use",
		},
		{
			name: "query returns two rows", query: "SELECT created_at FROM orders LIMIT 2",
			newerThan: day, want: wantErr, wantErrContains: "exactly one row",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &queryNewerThan{query: tt.query, newerThan: tt.newerThan, timeout: defaultQueryTimeout}
			res, err := a.Check(context.Background(), env)

			got := wantPass
			switch {
			case err != nil:
				got = wantErr
			case !res.Passed:
				got = wantFail
			}
			if got != tt.want {
				t.Fatalf("got %s, want %s (message %q, err %v)", got, tt.want, res.Message, err)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error %q does not mention %q", err, tt.wantErrContains)
			}
		})
	}
}

// Boundary, matching every other threshold in the tool: exactly at the limit
// passes.
func TestQueryNewerThan_ExactlyAtTheThreshold(t *testing.T) {
	env := requireDB(t)

	// Written here rather than in the seed dump, and this matters: the seed's
	// timestamps are fixed when the dump loads, which is fifteen-odd seconds
	// before any test runs, so a row seeded "exactly 24h old" is 24h0m2s old by
	// the time it is read. Anchoring the row to now() at test time is what
	// makes the boundary a boundary rather than a race.
	if _, err := testDB.Exec(`
		CREATE TABLE IF NOT EXISTS boundary (created_at timestamptz NOT NULL);
		TRUNCATE boundary;
		INSERT INTO boundary (created_at) VALUES (now() - '24 hours'::interval);
	`); err != nil {
		t.Fatalf("seeding the boundary row: %v", err)
	}

	a := &queryNewerThan{
		query:     "SELECT max(created_at) FROM boundary",
		newerThan: 24 * time.Hour,
		timeout:   defaultQueryTimeout,
	}
	res, err := a.Check(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Passed {
		t.Errorf("a row exactly at the threshold must pass: %q", res.Message)
	}
}

func TestQueryAssertions_RequireADatabase(t *testing.T) {
	if !(&queryMin{}).Requires().Database {
		t.Error("query_min must require a database")
	}
	if !(&queryNewerThan{}).Requires().Database {
		t.Error("query_newer_than must require a database")
	}
	// Requires().Restore stays false: the runner treats a database requirement
	// as implying a restore, so each assertion declares only what it needs.
	if (&queryMin{}).Requires().Restore {
		t.Error("query_min should not declare Restore itself")
	}
}

// With no database, an assertion that needs one must say so rather than panic
// on a nil connection.
func TestQueryAssertions_NoDatabaseIsAnError(t *testing.T) {
	if _, err := (&queryMin{query: "SELECT 1", min: 1, timeout: time.Second}).
		Check(context.Background(), Env{}); err == nil {
		t.Error("query_min with no database must error")
	}
	if _, err := (&queryNewerThan{query: "SELECT now()", newerThan: time.Hour, timeout: time.Second}).
		Check(context.Background(), Env{}); err == nil {
		t.Error("query_newer_than with no database must error")
	}
}

func TestNewQueryAssertions_ConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{name: "query_min valid", yaml: "query: SELECT count(*) FROM users\nmin: 1\n"},
		{name: "query_min with timeout", yaml: "query: SELECT 1\nmin: 1\ntimeout: 5s\n"},
		{name: "query_min missing query", yaml: "min: 1\n", wantErr: true},
		{name: "query_min negative min", yaml: "query: SELECT 1\nmin: -1\n", wantErr: true},
		{name: "query_min negative timeout", yaml: "query: SELECT 1\nmin: 1\ntimeout: -5s\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newQueryMin(mappingNode(t, tt.yaml))
			if tt.wantErr && err == nil {
				t.Fatal("expected a build-time error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	newerThanTests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{name: "valid", yaml: "query: SELECT max(created_at) FROM orders\nnewer_than: 24h\n"},
		{name: "missing query", yaml: "newer_than: 24h\n", wantErr: true},
		{name: "missing newer_than", yaml: "query: SELECT 1\n", wantErr: true},
		{name: "zero newer_than", yaml: "query: SELECT 1\nnewer_than: 0\n", wantErr: true},
		{name: "negative newer_than", yaml: "query: SELECT 1\nnewer_than: -1h\n", wantErr: true},
		{name: "unparseable duration", yaml: "query: SELECT 1\nnewer_than: soon\n", wantErr: true},
	}

	for _, tt := range newerThanTests {
		t.Run("query_newer_than "+tt.name, func(t *testing.T) {
			_, err := newQueryNewerThan(mappingNode(t, tt.yaml))
			if tt.wantErr && err == nil {
				t.Fatal("expected a build-time error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Timeout defaulting is shared, so it cannot drift between the two assertions.
func TestQueryTimeout(t *testing.T) {
	if got, err := queryTimeout(0); err != nil || got != defaultQueryTimeout {
		t.Errorf("queryTimeout(0) = %s, %v; want the default", got, err)
	}
	if got, err := queryTimeout(5 * time.Second); err != nil || got != 5*time.Second {
		t.Errorf("queryTimeout(5s) = %s, %v; want 5s", got, err)
	}
	if _, err := queryTimeout(-time.Second); err == nil {
		t.Error("a negative timeout must be rejected")
	}
}
