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

	// Bounds on a container fed data from a backup under suspicion: generous
	// enough for a real dump, small enough a runaway cannot take the host.
	pgMemoryLimit = "512m"
	pgPidsLimit   = "512"
)

// Postgres is a running Postgres container with a connection to it.
type Postgres struct {
	container *Container

	// DB is what assertions receive. Closed by Close.
	DB *sql.DB
}

// LoadError means the dump itself would not load — a distinct type so the
// runner reports it as a failed verification (taxonomy #9, #4), not a
// broken tool run.
type LoadError struct {
	Path string
	Err  error
}

func (e *LoadError) Error() string { return fmt.Sprintf("loading %s: %v", e.Path, e.Err) }
func (e *LoadError) Unwrap() error { return e.Err }

// StartPostgres brings up a Postgres container, waits for it to accept
// connections, and loads dumpPath into it.
//
// The returned *Postgres is non-nil whenever a container was started,
// including when loading failed, so the caller can always defer Close.
func StartPostgres(ctx context.Context, rt *Runtime, image, dumpPath string) (*Postgres, error) {
	// A throwaway credential, generated per run so two concurrent runs
	// cannot reach each other's database, passed via --env-file so it never
	// appears in `ps`.
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
		return p, err
	}

	return p, nil
}

// waitForPostgres polls until the database accepts a connection. A loop
// rather than a fixed sleep: the image restarts its server once during
// initialisation, and only the final restart accepts connections.
func waitForPostgres(ctx context.Context, hostPort, password string) (*sql.DB, error) {
	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable",
		pgUser, password, hostPort, pgDatabase)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database connection: %w", redactDSN(err, password))
	}

	// One connection is all any assertion needs, and keeps the process
	// count predictable under --pids-limit.
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

// checkDumpVersion refuses a dump newer than the server before psql fails
// confusingly at it, reported as a LoadError (taxonomy #9). Silent when the
// dump has no readable version header — it improves the message where it
// can, and never blocks a load it does not understand.
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
		// Not fatal: a courtesy check; psql reports the mismatch itself if
		// there is one.
		return nil
	}

	if err := checkVersionCompatible(dumpVersion, dumpMajor, serverVersion); err != nil {
		return &LoadError{Path: dumpPath, Err: err}
	}
	return nil
}

// load streams the dump into psql inside the container, piped to stdin so
// the container needs no mounts.
//
// ON_ERROR_STOP=1 is not optional: without it psql reports success after
// printing errors for every failed statement, so a corrupt or
// version-mismatched dump would verify clean.
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

// ServerVersion reports the running server's version.
func (p *Postgres) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := p.DB.QueryRowContext(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", fmt.Errorf("reading server version: %w", err)
	}
	return v, nil
}

// Close closes the connection and removes the container. Safe on a nil
// receiver and safe to call twice.
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

// redactDSN keeps a generated password out of anything that reaches a log,
// a report, or stdout.
func redactDSN(err error, password string) error {
	if err == nil {
		return nil
	}
	if password == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), password, "[redacted]"))
}
