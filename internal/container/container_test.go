package container

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testImage is the Postgres image these tests bring up. Overridable so the
// suite can be pointed at a mirror or a pinned digest.
func testImage() string {
	if v := os.Getenv("CONSTAT_TEST_PG_IMAGE"); v != "" {
		return v
	}
	return "docker.io/library/postgres:16-alpine"
}

func requireRuntime(t *testing.T) *Runtime {
	t.Helper()
	if testing.Short() {
		t.Skip("container tests start a real container; skipped under -short")
	}
	rt, err := DetectRuntime(context.Background())
	if err != nil {
		t.Skipf("no container runtime: %v", err)
	}
	return rt
}

// containerExists asks the runtime directly rather than trusting our own
// bookkeeping — the point of these tests is that the container is really gone.
func containerExists(t *testing.T, rt *Runtime, name string) bool {
	t.Helper()
	out, err := exec.Command(rt.bin, "ps", "--all", "--quiet", "--filter", "name="+name).Output()
	if err != nil {
		t.Fatalf("listing containers: %v (output: %s)", err, out)
	}
	return strings.TrimSpace(string(out)) != ""
}

func writeDump(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.sql")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing dump: %v", err)
	}
	return path
}

const goodDump = `
CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL);
INSERT INTO users (id, email) VALUES (1, 'a@example.test'), (2, 'b@example.test');
`

func TestStartPostgres_LoadsAndIsQueryable(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pg, err := StartPostgres(ctx, rt, testImage(), writeDump(t, goodDump))
	if pg == nil {
		t.Fatalf("no handle returned: %v", err)
	}
	name := pg.container.Name()
	defer pg.Close()

	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}

	var n int
	if err := pg.DB.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatalf("querying the loaded dump: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d rows, want 2 — the dump did not load as expected", n)
	}

	version, err := pg.ServerVersion(ctx)
	if err != nil {
		t.Fatalf("reading server version: %v", err)
	}
	if version == "" {
		t.Error("server version is empty")
	}
	t.Logf("server version %s in container %s", version, name)

	pg.Close()
	if containerExists(t, rt, name) {
		t.Errorf("container %s still exists after Close", name)
	}
}

// A dump that will not load is the failure this whole path exists to catch, so
// it must be reported as its own type — and it must still take the container
// with it.
func TestStartPostgres_BadDumpIsALoadErrorAndStillTearsDown(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dump := writeDump(t, "CREATE TABLE users (id integer);\nTHIS IS NOT SQL;\n")

	pg, err := StartPostgres(ctx, rt, testImage(), dump)
	if pg == nil {
		t.Fatalf("no handle returned for a failed load, so nothing can clean up: %v", err)
	}
	name := pg.container.Name()
	defer pg.Close()

	var loadErr *LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("got %v, want a *LoadError", err)
	}

	pg.Close()
	if containerExists(t, rt, name) {
		t.Errorf("container %s still exists after a failed load", name)
	}
}

// ON_ERROR_STOP is what makes a broken dump fail at all: psql otherwise prints
// errors for every bad statement and still exits 0. Without this test, removing
// that flag would leave the suite green and the tool silently broken.
func TestStartPostgres_PartiallyBrokenDumpFails(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Valid first statement, then a reference to a table that does not exist:
	// what a dump restored in the wrong order, or with a missing table, looks
	// like.
	dump := writeDump(t, `
CREATE TABLE users (id integer PRIMARY KEY);
INSERT INTO orders (id, user_id) VALUES (1, 1);
`)

	pg, err := StartPostgres(ctx, rt, testImage(), dump)
	if pg != nil {
		defer pg.Close()
	}

	var loadErr *LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("a dump whose later statements fail must not load cleanly; got %v", err)
	}
}

func TestStartPostgres_MissingDumpIsALoadError(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pg, err := StartPostgres(ctx, rt, testImage(), filepath.Join(t.TempDir(), "absent.sql"))
	if pg != nil {
		defer pg.Close()
	}

	var loadErr *LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("got %v, want a *LoadError", err)
	}
}

// A timeout during bring-up must not leave a container behind. This is the path
// that matters most: it is reached when something is already going wrong.
func TestStartPostgres_TimeoutTearsDown(t *testing.T) {
	rt := requireRuntime(t)

	// Long enough to create the container, far too short for Postgres to
	// become ready.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pg, err := StartPostgres(ctx, rt, testImage(), writeDump(t, goodDump))
	if pg != nil {
		defer pg.Close()
	}
	if err == nil {
		t.Skip("postgres became ready within the timeout; nothing to assert")
	}

	// Whatever was created must be gone. Names are unique per start, so any
	// surviving constat-verify container is a leak from this call.
	leaked := listConstatContainers(t, rt)
	if len(leaked) > 0 {
		t.Errorf("containers left behind after a timeout: %v", leaked)
	}
}

func listConstatContainers(t *testing.T, rt *Runtime) []string {
	t.Helper()
	out, err := exec.Command(rt.bin, "ps", "--all", "--format", "{{.Names}}", "--filter", "name=constat-verify-").Output()
	if err != nil {
		t.Fatalf("listing containers: %v (output: %s)", err, out)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

func TestStartPostgres_UnknownImageIsNotALoadError(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg, err := StartPostgres(ctx, rt, "localhost/constat-no-such-image:v0", writeDump(t, goodDump))
	if pg != nil {
		defer pg.Close()
	}
	if err == nil {
		t.Fatal("expected an error for an image that does not exist")
	}

	var loadErr *LoadError
	if errors.As(err, &loadErr) {
		t.Error("a missing image is the run being broken, not the backup failing verification")
	}
}

func TestClose_IsSafeTwiceAndOnNil(t *testing.T) {
	var p *Postgres
	p.Close() // must not panic

	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pg, err := StartPostgres(ctx, rt, testImage(), writeDump(t, goodDump))
	if err != nil {
		if pg != nil {
			pg.Close()
		}
		t.Fatalf("starting postgres: %v", err)
	}
	pg.Close()
	pg.Close() // must not panic
}

// The generated password must never appear in an error, because errors reach
// stdout and one day a report.
func TestRedactDSN(t *testing.T) {
	err := errors.New(`failed to connect to postgres://constat:s3cr3t@127.0.0.1:5432/constat`)
	got := redactDSN(err, "s3cr3t").Error()

	if strings.Contains(got, "s3cr3t") {
		t.Errorf("password survived redaction: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("redaction marker missing: %q", got)
	}
	if redactDSN(nil, "s3cr3t") != nil {
		t.Error("redacting a nil error must stay nil")
	}
}

// The credential is passed by file, never on the command line, where ps would
// show it to every user on the host.
func TestStart_PassphraseIsNotOnTheCommandLine(t *testing.T) {
	rt := requireRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const secret = "sentinel-passphrase-value"

	c, err := rt.Start(ctx, Spec{
		Image:         testImage(),
		ContainerPort: "5432",
		Env:           map[string]string{"POSTGRES_PASSWORD": secret},
	})
	if c != nil {
		defer c.Stop()
	}
	if err != nil {
		t.Fatalf("starting container: %v", err)
	}

	out, err := exec.Command(rt.bin, "inspect", "--format", "{{.Config.CreateCommand}}", c.Name()).Output()
	if err != nil {
		t.Skipf("runtime does not report the create command: %v", err)
	}
	if strings.Contains(string(out), secret) {
		t.Errorf("the credential appears in the container's own command line: %s", out)
	}
}
