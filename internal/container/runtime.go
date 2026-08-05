// Package container runs the disposable database container that a dump is
// restored into, and guarantees it goes away again.
//
// Shelling out to the docker CLI rather than taking the Docker SDK, per the
// decision already recorded: shelling out is honest, keeps the single static
// binary, and works unchanged against podman.
//
// The container is fed data that came out of a customer's backup. That backup
// is the thing under suspicion — it may be corrupt, it may be attacker-supplied
// after a compromise, and "restore brings the malware back" is failure mode #11
// in its own right. So the container is treated as hostile: no host network, a
// port published only on loopback, a memory and process cap, no new privileges,
// and no mounts at all.
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
// present, because a machine with both usually means docker is the configured
// one; podman is a drop-in for everything used here.
type Runtime struct {
	bin string
}

// ErrNoRuntime means neither docker nor podman is usable. Callers surface this
// as an error rather than skipping the check: a verification that silently
// declines to verify is worse than no verification.
var ErrNoRuntime = errors.New("no container runtime found: install docker or podman")

// DetectRuntime finds a usable container CLI. Presence on PATH is not enough —
// a docker binary with no reachable daemon is the common broken case — so the
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

// run executes a subcommand and returns its standard output, always carrying
// both streams into the error: `exit status 1` alone throws away the useful
// half.
//
// stdout and stderr are kept apart rather than combined because the return
// value gets parsed. The docker-shim wrapper around podman, for one, prints an
// advisory banner on stderr for every invocation, and combining the two means
// parsing whatever a runtime feels like announcing.
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

// Spec describes a container to start. Deliberately narrow: everything here is
// either required to make the database reachable or required to keep it caged.
type Spec struct {
	Image string

	// Env is written to a file mode 0600 and passed with --env-file. Never as
	// -e on the command line, where it would be visible in ps output to every
	// user on the host.
	Env map[string]string

	// ContainerPort is published to a kernel-assigned port on 127.0.0.1 only.
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

// startAttempts bounds how many times Start will retry a container that
// failed to come up.
//
// The failure this exists for is port allocation. The published port is
// kernel-assigned, and between the kernel choosing it and the runtime binding
// it, something else on the host can take it — rootless podman's pasta
// networking reports this as "Failed to bind port N". It is rare, it is
// transient, and a fresh attempt gets a different port.
//
// Retrying is worth it because the alternative is a spurious ERROR verdict on
// a backup that is fine. constat classifies that correctly — the tool could
// not run, rather than the backup being broken — but an operator woken by it
// still has to work that out, and alert fatigue is how real failures get
// ignored.
//
// Three, not more: a port collision that survives three fresh ports is not a
// collision, it is something that will not fix itself by waiting.
const startAttempts = 3

// Start runs the container detached and returns a handle. The handle is
// returned even on some failure paths precisely so the caller can always defer
// Stop; when the returned handle is nil there is nothing to clean up.
//
// A transient port-binding failure is retried with a completely fresh
// container — new name, new scratch directory, new kernel-assigned port — and
// the failed attempt is torn down before the next one begins.
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
		// startOnce has already cleaned up its own failed attempt.
	}

	return nil, fmt.Errorf("container did not start after %d attempts: %w", startAttempts, lastErr)
}

// startOnce is one complete attempt: its own name, its own scratch directory,
// its own port. On any failure it removes whatever it created before
// returning, so a retry never inherits state from the attempt before it.
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
		// Kernel-assigned host port, bound to loopback. Never --network host:
		// the container must not see the host's network at all.
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
		// The container may or may not exist depending on where run failed;
		// Stop tolerates both.
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
// for the port the kernel just handed out.
//
// Matching on message text, which is unpleasant and is the price of shelling
// out to a CLI rather than linking a library — there is no error code to read.
// Kept deliberately narrow: matching too broadly would retry a missing image
// or a broken daemon three times over, turning a fast, clear failure into a
// slow one.
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

// publishedPort asks the runtime which loopback port the container port landed
// on. Parsed rather than assumed, because the kernel assigns it.
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

// Stop removes the container and its scratch directory. Safe to call more than
// once and on a container that never started, so callers can defer it
// unconditionally.
//
// Deliberately does not take the caller's context: it runs on the cleanup path,
// which is reached precisely when that context has been cancelled or has timed
// out. A teardown that inherits a cancelled context does not tear anything
// down, and leaving a container holding restored customer data alive is a
// worse outcome than a slow exit.
func (c *Container) Stop() {
	if c == nil {
		return
	}
	if c.name != "" {
		// --rm removes it on normal exit; this covers every other path, and
		// force is what makes it certain rather than likely.
		_ = c.rt.command(context.Background(), "rm", "--force", "--volumes", c.name).Run()
	}
	if c.dir != "" {
		os.RemoveAll(c.dir)
		c.dir = ""
	}
}

// ExecStdin runs a command inside the container with r piped to its stdin, and
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
