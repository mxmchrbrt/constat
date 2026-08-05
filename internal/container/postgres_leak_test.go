package container

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The generated container password lives in the DSN, and that DSN is handed to
// pgx. Every error pgx produces from that connection therefore has an
// opportunity to carry the password with it — and those errors do not stay
// local: a failure inside an assertion becomes a report.Assertion message,
// which is written to report.json and posted to the webhook.
//
// redactDSN covers the errors this package wraps itself. It cannot cover the
// ones raised later, from inside internal/assert, where the password is not in
// scope to redact against. Those rely on pgx redacting its own connection
// string, which it does — verified, not assumed.
//
// This test pins that dependency guarantee. If a pgx upgrade ever starts
// including the password in connection errors, the leak would otherwise be
// silent, reach durable output, and be discovered by someone reading a report.
func TestPgxConnectionErrorsDoNotCarryThePassword(t *testing.T) {
	const sentinel = "SENTINEL-PGX-DSN-PASSWORD-4f19a2"

	// Port 1 is never listening: forces a connection failure at first use,
	// which is the path an assertion would hit if the container died
	// mid-run.
	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:1/%s?sslmode=disable", pgUser, sentinel, pgDatabase)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		// sql.Open rarely fails, but if it does the error is ours to redact.
		if strings.Contains(redactDSN(err, sentinel).Error(), sentinel) {
			t.Fatalf("redactDSN failed to remove the password: %v", err)
		}
		return
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Each of these is a path internal/assert can reach with no password in
	// scope to redact against.
	_, txErr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	_, queryErr := db.QueryContext(ctx, "SELECT 1")
	pingErr := db.PingContext(ctx)

	for name, err := range map[string]error{
		"BeginTx":      txErr,
		"QueryContext": queryErr,
		"PingContext":  pingErr,
	} {
		if err == nil {
			t.Fatalf("%s: expected a connection error against a closed port", name)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("%s leaked the DSN password into an error that reaches the report: %v", name, err)
		}
	}
}
