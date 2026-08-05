package container

import (
	"errors"
	"testing"
)

// The retry exists for one specific transient failure and must not quietly
// become a retry-everything. Matching too broadly would turn a missing image
// or a dead daemon — fast, clear failures — into slow ones.
func TestRetryablePortFailure(t *testing.T) {
	retryable := []struct {
		name string
		msg  string
	}{
		{
			// The one actually observed, from rootless podman's pasta
			// networking during session 12's audit.
			name: "pasta bind failure",
			msg:  "podman run: exit status 126 (output: Error: pasta failed with exit code 1:\nFailed to bind port 42523 ((null)) for option '-t 127.0.0.1/42523-42523:5432-5432')",
		},
		{name: "docker port allocated", msg: "docker run: Bind for 127.0.0.1:49153 failed: port is already allocated"},
		{name: "generic bind", msg: "listen tcp 127.0.0.1:49153: bind: address already in use"},
		{name: "case insensitive", msg: "FAILED TO BIND PORT 5432"},
	}

	for _, tt := range retryable {
		t.Run(tt.name, func(t *testing.T) {
			if !retryablePortFailure(errors.New(tt.msg)) {
				t.Errorf("should be retried: %q", tt.msg)
			}
		})
	}

	notRetryable := []struct {
		name string
		msg  string
	}{
		{name: "unknown image", msg: `docker run: Error: initializing source docker://nope:v0: reading manifest v0: manifest unknown`},
		{name: "daemon down", msg: "docker run: Cannot connect to the Docker daemon at unix:///var/run/docker.sock"},
		{name: "out of memory", msg: "docker run: Error: OCI runtime error: container_linux.go: cannot allocate memory"},
		{name: "disk full", msg: "docker run: write /var/lib/containers: no space left on device"},
		{name: "empty", msg: ""},
	}

	for _, tt := range notRetryable {
		t.Run(tt.name, func(t *testing.T) {
			if retryablePortFailure(errors.New(tt.msg)) {
				t.Errorf("should fail fast, not be retried: %q", tt.msg)
			}
		})
	}

	if retryablePortFailure(nil) {
		t.Error("a nil error must not be retryable")
	}
}
