package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Table C, derived from taxonomy #9 (version mismatch). The header line is
// verbatim from `pg_dump` 16.14 output.

const realDumpHeader = `--
-- PostgreSQL database dump
--

\restrict LKElHpgDpzj5nrmPANZjYh4sQ8N2ffHedkyAjO4tOtmVACmLe1pA2ZmkANd6fJt

-- Dumped from database version 16.14
-- Dumped by pg_dump version 16.14

SET statement_timeout = 0;
`

func TestParseDumpVersion(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantMajor int
		wantRaw   string
		wantOK    bool
	}{
		{
			name: "real pg_dump header", body: realDumpHeader,
			wantMajor: 16, wantRaw: "16.14", wantOK: true,
		},
		{
			name: "single digit major", body: "-- Dumped from database version 9.6.24\n",
			wantMajor: 9, wantRaw: "9.6.24", wantOK: true,
		},
		{
			name: "vendor suffix", body: "-- Dumped from database version 15.4 (Debian 15.4-1)\n",
			wantMajor: 15, wantRaw: "15.4 (Debian 15.4-1)", wantOK: true,
		},
		{
			// Not an error: hand-written SQL and custom-format dumps have no
			// such header, and the gate must stay silent rather than block.
			name: "no header at all", body: "CREATE TABLE users (id integer);\n",
			wantOK: false,
		},
		{
			name: "header present but unparseable", body: "-- Dumped from database version unknown\n",
			wantOK: false,
		},
		{
			name: "empty file", body: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, raw, ok := parseDumpVersion(strings.NewReader(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (major %d, raw %q)", ok, tt.wantOK, major, raw)
			}
			if !ok {
				return
			}
			if major != tt.wantMajor {
				t.Errorf("major = %d, want %d", major, tt.wantMajor)
			}
			if raw != tt.wantRaw {
				t.Errorf("raw = %q, want %q", raw, tt.wantRaw)
			}
		})
	}
}

// The version line must not be hunted through an entire dump. A dump is
// routinely larger than memory.
func TestParseDumpVersion_StopsAfterTheHeader(t *testing.T) {
	body := strings.Repeat("-- filler line to push past the scan limit\n", 4000) +
		"-- Dumped from database version 16.14\n"

	if _, _, ok := parseDumpVersion(strings.NewReader(body)); ok {
		t.Error("a version line beyond the header scan limit must not be found")
	}
}

func TestCheckVersionCompatible(t *testing.T) {
	tests := []struct {
		name       string
		dumpRaw    string
		dumpMajor  int
		server     string
		wantReject bool
	}{
		{name: "same major", dumpRaw: "16.14", dumpMajor: 16, server: "16.14"},
		{name: "minor versions differ", dumpRaw: "16.2", dumpMajor: 16, server: "16.14"},
		{
			// Supported by Postgres, and what an upgrade looks like. Flagging
			// it would fail runs that are working as intended.
			name: "older dump into newer server", dumpRaw: "14.1", dumpMajor: 14, server: "16.14",
		},
		{
			// The case the whole check exists for.
			name: "newer dump into older server", dumpRaw: "16.14", dumpMajor: 16, server: "15.7",
			wantReject: true,
		},
		{name: "much newer dump", dumpRaw: "17.0", dumpMajor: 17, server: "13.2", wantReject: true},
		{
			// Nothing to compare against: stay silent rather than guess.
			name: "unreadable server version", dumpRaw: "16.14", dumpMajor: 16, server: "not a version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkVersionCompatible(tt.dumpRaw, tt.dumpMajor, tt.server)
			if tt.wantReject && err == nil {
				t.Fatal("expected a rejection, got none")
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if !tt.wantReject {
				return
			}
			// The message is the entire point of checking before psql runs.
			for _, want := range []string{tt.dumpRaw, tt.server, "older major version"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q does not mention %q", err, want)
				}
			}
		})
	}
}

// End to end against a real server: a dump declaring a version newer than the
// running image is rejected before psql sees it, with the clear message rather
// than a wall of syntax errors.
//
// The header is synthetic, which is deliberate — it tests the gate against a
// real server without pulling a second Postgres image for the sake of one case.
func TestStartPostgres_NewerDumpIsRejectedBeforeLoading(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Valid SQL, so anything that fails here failed on the version, not the
	// statements.
	dump := filepath.Join(t.TempDir(), "future.sql")
	body := "-- Dumped from database version 99.1\n\nCREATE TABLE users (id integer);\n"
	if err := os.WriteFile(dump, []byte(body), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}

	pg, err := StartPostgres(ctx, rt, testImage(), dump)
	if pg != nil {
		defer pg.Close()
	}

	var loadErr *LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("got %v, want a *LoadError", err)
	}
	if !strings.Contains(err.Error(), "99.1") {
		t.Errorf("message should name the dump's version: %v", err)
	}
	if !strings.Contains(err.Error(), "older major version") {
		t.Errorf("message should explain the mismatch: %v", err)
	}

	// The gate must fire before the load: nothing should have been created.
	if pg != nil && pg.DB != nil {
		var n int
		err := pg.DB.QueryRowContext(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_name = 'users'").Scan(&n)
		if err != nil {
			t.Fatalf("checking whether the dump was loaded: %v", err)
		}
		if n != 0 {
			t.Error("the dump was loaded despite the version mismatch")
		}
	}
}

// An older dump still loads: the gate must not become an obstacle.
func TestStartPostgres_OlderDumpStillLoads(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dump := filepath.Join(t.TempDir(), "old.sql")
	body := "-- Dumped from database version 9.6.24\n\nCREATE TABLE users (id integer);\n"
	if err := os.WriteFile(dump, []byte(body), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}

	pg, err := StartPostgres(ctx, rt, testImage(), dump)
	if pg != nil {
		defer pg.Close()
	}
	if err != nil {
		t.Fatalf("an older dump must still load: %v", err)
	}
}
