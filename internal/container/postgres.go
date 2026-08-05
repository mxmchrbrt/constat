package container

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
)

const (
	pgUser     = "constat"
	pgDatabase = "constat"
	pgPort     = "5432"

	// Bounds on a container fed data from a backup that is under suspicion.
	// Generous enough for a real dump, small enough that a runaway cannot take
	// the host with it.
	pgMemoryLimit = "512m"
	pgPidsLimit   = "512"
)

// Postgres is a running Postgres container with a connection to it.
type Postgres struct {
	container *Container

	// DB is what assertions receive. Closed by Close.
	DB *sql.DB
}

// LoadError means the dump itself would not load. It is deliberately a distinct
// type: a dump that will not load is the failure being hunted — taxonomy #9
// (Postgres 16 dump into 15) and #4 (corruption) both surface exactly here — so
// the runner reports it as a FAILED VERIFICATION, not as a broken tool run.
type LoadError struct {
	Path string
	Err  error
}

func (e *LoadError) Error() string { return fmt.Sprintf("loading %s: %v", e.Path, e.Err) }
func (e *LoadError) Unwrap() error { return e.Err }

// StartPostgres brings up a Postgres container, waits for it to accept
// connections, and loads dumpPath into it.
//
// The returned *Postgres is non-nil whenever a container was started, including
// when loading failed, so the caller can always defer Close and so a LoadError
// does not leave a container running.
func StartPostgres(ctx context.Context, rt *Runtime, image, dumpPath string) (*Postgres, error) {
	// A throwaway credential for a container that lives seconds and listens on
	// loopback only. Generated per run rather than fixed so that two concurrent
	// runs cannot reach each other's database, and passed via --env-file rather
	// than -e so it never appears in ps output.
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating database credential: %w", err)
	}
	password := hex.EncodeToString(secret)

	c, err := rt.Start(ctx, Spec{
		Image:         image,
		ContainerPort: pgPort,
		MemoryLimit:   pgMemoryLimit,
		PidsLimit:     pgPidsLimit,
		Env: map[string]string{
			"POSTGRES_USER":     pgUser,
			"POSTGRES_DB":       pgDatabase,
			"POSTGRES_PASSWORD": password,
		},
	})
	if err != nil {
		return nil, err
	}

	p := &Postgres{container: c}

	db, err := waitForPostgres(ctx, c.HostPort, password)
	if err != nil {
		p.Close()
		return nil, err
	}
	p.DB = db

	if err := p.checkDumpVersion(ctx, dumpPath); err != nil {
		return p, err
	}

	if err := p.load(ctx, dumpPath); err != nil {
		// Deliberately not Close()d here: the caller defers Close, and the
		// container must stay reachable long enough for nothing — but the
		// handle must be returned so that deferred Close actually runs.
		return p, err
	}

	return p, nil
}

// waitForPostgres polls until the database accepts a connection. A loop rather
// than a fixed sleep: the image starts a temporary server during
// initialisation and restarts it, so any single sleep is either flaky or slow.
// The host-side port only accepts connections after that final restart, which
// is what makes polling it reliable.
func waitForPostgres(ctx context.Context, hostPort, password string) (*sql.DB, error) {
	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable",
		pgUser, password, hostPort, pgDatabase)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		// Never wrapped with the DSN: it carries the password.
		return nil, fmt.Errorf("opening database connection: %w", redactDSN(err, password))
	}

	// One connection is all any assertion needs, and it keeps the container's
	// process count predictable under --pids-limit.
	db.SetMaxOpenConns(1)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := db.PingContext(pingCtx)
		cancel()
		if err == nil {
			return db, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			db.Close()
			return nil, fmt.Errorf("database never became ready: %w (last attempt: %v)",
				ctx.Err(), redactDSN(lastErr, password))
		case <-ticker.C:
		}
	}
}

// checkDumpVersion refuses a dump that is newer than the server before psql
// gets a chance to fail confusingly at it. Reported as a LoadError, because a
// dump that cannot be loaded into the operator's own image is a verdict about
// the backup — taxonomy #9 — not a broken run.
//
// Silent when the dump carries no readable version header: the check improves
// the message where it can and never blocks a load it does not understand.
func (p *Postgres) checkDumpVersion(ctx context.Context, dumpPath string) error {
	f, err := os.Open(dumpPath)
	if err != nil {
		return &LoadError{Path: dumpPath, Err: err}
	}
	defer f.Close()

	dumpMajor, dumpVersion, ok := parseDumpVersion(f)
	if !ok {
		return nil
	}

	serverVersion, err := p.ServerVersion(ctx)
	if err != nil {
		// Not fatal: this check is a courtesy, and psql will still report the
		// mismatch in its own words if there is one.
		return nil
	}

	if err := checkVersionCompatible(dumpVersion, dumpMajor, serverVersion); err != nil {
		return &LoadError{Path: dumpPath, Err: err}
	}
	return nil
}

// load streams the dump into psql inside the container.
//
// Piped to stdin rather than mounted: the container then needs no mounts at
// all, which is one less way for it to reach the host.
//
// ON_ERROR_STOP=1 is not optional. Without it psql reports success after
// printing errors for every failed statement, which would make a corrupt or
// version-mismatched dump verify clean — the exact shape of failure this tool
// exists to prevent.
func (p *Postgres) load(ctx context.Context, dumpPath string) error {
	f, err := os.Open(dumpPath)
	if err != nil {
		return &LoadError{Path: dumpPath, Err: err}
	}
	defer f.Close()

	out, err := p.container.ExecStdin(ctx, f,
		"psql",
		"--username", pgUser,
		"--dbname", pgDatabase,
		"--no-password",
		"--quiet",
		"--variable", "ON_ERROR_STOP=1",
	)
	if err != nil {
		return &LoadError{Path: dumpPath, Err: fmt.Errorf("%w (output: %s)", err, trim([]byte(out)))}
	}
	return nil
}

// ServerVersion reports the version of the running server, for the version
// mismatch check in session 8 and for messages.
func (p *Postgres) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := p.DB.QueryRowContext(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", fmt.Errorf("reading server version: %w", err)
	}
	return v, nil
}

// Close closes the connection and removes the container. Safe on a nil
// receiver and safe to call twice, so callers defer it unconditionally.
func (p *Postgres) Close() {
	if p == nil {
		return
	}
	if p.DB != nil {
		p.DB.Close()
		p.DB = nil
	}
	p.container.Stop()
}

// redactDSN keeps a generated password out of anything that reaches a log, a
// report, or stdout. The password is random and short-lived, but a tool whose
// promise is evidence must not be the thing that prints credentials.
func redactDSN(err error, password string) error {
	if err == nil {
		return nil
	}
	if password == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), password, "[redacted]"))
}
