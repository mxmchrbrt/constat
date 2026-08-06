// Package container runs the disposable database container that a dump is
// restored into, and guarantees it goes away again.
//
// Shells out to the docker CLI rather than the Docker SDK, so it works
// unchanged against podman and keeps constat a single static binary.
//
// The container is fed data from a customer's backup, which is itself under
// suspicion (corrupt, or attacker-supplied after a compromise), so it is
// treated as hostile: no host network, loopback-only published port, memory
// and process caps, no new privileges, no mounts.
package container

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runtime is the container CLI in use. docker is preferred when both are
// present; podman is a drop-in for everything used here.
type Runtime struct {
	bin string
}

// ErrNoRuntime means neither docker nor podman is usable.
var ErrNoRuntime = errors.New("no container runtime found: install docker or podman")

// DetectRuntime finds a usable container CLI. Presence on PATH is not
// enough — a docker binary with no reachable daemon is common — so the
// daemon is probed too.
func DetectRuntime(ctx context.Context) (*Runtime, error) {
	var tried []string
	for _, bin := range []string{"docker", "podman"} {
		path, err := exec.LookPath(bin)
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, "info")
		if out, err := cmd.Output(); err != nil {
			tried = append(tried, fmt.Sprintf("%s found but not usable: %v (output: %s)", bin, err, trim(out)))
			continue
		}
		return &Runtime{bin: path}, nil
	}
	if len(tried) > 0 {
		return nil, fmt.Errorf("%w; %s", ErrNoRuntime, strings.Join(tried, "; "))
	}
	return nil, ErrNoRuntime
}

// Name reports the runtime binary, for messages.
func (r *Runtime) Name() string { return filepath.Base(r.bin) }

func (r *Runtime) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, r.bin, args...)
}

// run executes a subcommand and returns its standard output. stdout and
// stderr are kept apart because the return value gets parsed, and podman's
// docker-shim wrapper prints an advisory banner on stderr for every
// invocation.
func (r *Runtime) run(ctx context.Context, args ...string) (string, error) {
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		both := append(stdout.Bytes(), stderr.Bytes()...)
		return stdout.String(), fmt.Errorf("%s %s: %w (output: %s)", r.Name(), args[0], err, trim(both))
	}
	return stdout.String(), nil
}

// Spec describes a container to start. Everything here is either required to
// make the database reachable or required to keep it caged.
type Spec struct {
	Image string

	// Written to a file mode 0600 and passed with --env-file, never -e on the
	// command line, where it would show in `ps` to every user on the host.
	Env map[string]string

	// Published to a kernel-assigned port on 127.0.0.1 only.
	ContainerPort string

	MemoryLimit string
	PidsLimit   string
}

// Container is a started container and the means to reach and remove it.
type Container struct {
	rt   *Runtime
	name string
	dir  string // holds the env file; removed by Stop

	// HostPort is the loopback port ContainerPort was published to.
	HostPort string
}

// startAttempts bounds retries of a container that failed to come up. The
// published port is kernel-assigned, and something else on the host can win
// the race for it before the runtime binds — retried with a fresh port
// rather than failing the whole run over a transient collision.
const startAttempts = 3

// Start runs the container detached and returns a handle. The handle is
// returned even on failure paths so the caller can always defer Stop.
//
// A transient port-binding failure is retried with a completely fresh
// container (new name, scratch directory, and port); the failed attempt is
// torn down first.
func (r *Runtime) Start(ctx context.Context, spec Spec) (*Container, error) {
	var lastErr error

	for attempt := 1; attempt <= startAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		c, err := r.startOnce(ctx, spec)
		if err == nil {
			return c, nil
		}
		lastErr = err

		if !retryablePortFailure(err) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("container did not start after %d attempts: %w", startAttempts, lastErr)
}

// startOnce is one complete attempt. On any failure it removes whatever it
// created, so a retry never inherits state from the attempt before it.
func (r *Runtime) startOnce(ctx context.Context, spec Spec) (*Container, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("generating container name: %w", err)
	}
	name := "constat-verify-" + hex.EncodeToString(suffix)

	dir, err := os.MkdirTemp("", "constat-container-")
	if err != nil {
		return nil, fmt.Errorf("creating container scratch directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("securing container scratch directory: %w", err)
	}

	envFile := filepath.Join(dir, "env")
	var b strings.Builder
	for k, v := range spec.Env {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	if err := os.WriteFile(envFile, []byte(b.String()), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("writing container environment: %w", err)
	}

	c := &Container{rt: r, name: name, dir: dir}

	args := []string{
		"run", "--detach",
		"--name", name,
		"--rm",
		"--env-file", envFile,
		// Never --network host: the container must not see the host's
		// network at all.
		"--publish", "127.0.0.1::" + spec.ContainerPort,
		"--security-opt", "no-new-privileges",
	}
	if spec.MemoryLimit != "" {
		args = append(args, "--memory", spec.MemoryLimit)
	}
	if spec.PidsLimit != "" {
		args = append(args, "--pids-limit", spec.PidsLimit)
	}
	args = append(args, spec.Image)

	if _, err := r.run(ctx, args...); err != nil {
		c.Stop()
		return nil, err
	}

	port, err := c.publishedPort(ctx, spec.ContainerPort)
	if err != nil {
		c.Stop()
		return nil, err
	}
	c.HostPort = port

	return c, nil
}

// retryablePortFailure reports whether err looks like the host losing a race
// for the port the kernel just handed out. Matched on message text — there
// is no error code to read from a CLI — and kept narrow, since matching
// broadly would retry a missing image or a broken daemon three times over.
func retryablePortFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"failed to bind port",       // pasta, rootless podman
		"address already in use",    // generic bind failure
		"port is already allocated", // docker
		"bind: permission denied",   // a privileged port lost to another process
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// publishedPort asks the runtime which loopback port the container port
// landed on, since the kernel assigns it.
func (c *Container) publishedPort(ctx context.Context, containerPort string) (string, error) {
	out, err := c.rt.run(ctx, "port", c.name, containerPort+"/tcp")
	if err != nil {
		return "", err
	}
	// Output is one or more lines of "127.0.0.1:49153".
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.LastIndex(line, ":"); i >= 0 && i+1 < len(line) {
			return line[i+1:], nil
		}
	}
	return "", fmt.Errorf("could not read the published port for %s from %q", c.name, out)
}

// Name reports the container's generated name.
func (c *Container) Name() string { return c.name }

// Stop removes the container and its scratch directory. Safe to call more
// than once and on a container that never started.
//
// Does not take the caller's context: it runs on the cleanup path, reached
// precisely when that context is cancelled or timed out, and a teardown
// bound to a cancelled context would tear nothing down.
func (c *Container) Stop() {
	if c == nil {
		return
	}
	if c.name != "" {
		_ = c.rt.command(context.Background(), "rm", "--force", "--volumes", c.name).Run()
	}
	if c.dir != "" {
		os.RemoveAll(c.dir)
		c.dir = ""
	}
}

// ExecStdin runs a command inside the container with stdin piped to it, and
// returns its combined output.
func (c *Container) ExecStdin(ctx context.Context, stdin *os.File, args ...string) (string, error) {
	full := append([]string{"exec", "--interactive", c.name}, args...)
	cmd := c.rt.command(ctx, full...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s exec: %w (output: %s)", c.rt.Name(), err, trim(out))
	}
	return string(out), nil
}

// trim keeps subprocess output in errors useful without letting a runaway
// container log become the error message.
func trim(out []byte) string {
	const max = 2000
	s := strings.TrimSpace(string(out))
	if len(s) > max {
		return s[:max] + "… (truncated)"
	}
	return s
}
