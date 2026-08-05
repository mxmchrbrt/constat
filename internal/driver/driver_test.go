package driver

import (
	"strings"
	"testing"
)

// A restic failure carries one line per affected file. Measured against a real
// truncated repository of only 400 files, `restic restore` emitted ~276 KB —
// and a real repository holds orders of magnitude more files.
//
// That output does not stay local. It becomes an assertion message, which is
// written verbatim into report.json and posted as the webhook body. Unbounded,
// the failure compounds in the worst possible direction: the more broken the
// backup, the larger the payload, until the alert meant to report the breakage
// is itself rejected for being oversized.
func TestTrimOutput_BoundsSubprocessOutput(t *testing.T) {
	// Roughly what a mid-sized repository produces on a corrupt restore.
	huge := []byte(strings.Repeat("ignoring error for /var/lib/app/data/file.bin: no such file\n", 5000))
	if len(huge) < 250_000 {
		t.Fatalf("fixture is only %d bytes; it should model a realistic failure", len(huge))
	}

	got := trimOutput(huge)

	if len(got) > maxErrorOutput+len("… (truncated)") {
		t.Errorf("trimmed output is %d bytes, want at most %d", len(got), maxErrorOutput)
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Error("truncation must be visible, so nobody reads a clipped error as the whole story")
	}
	// The beginning is the useful part — the first error explains the failure.
	if !strings.HasPrefix(got, "ignoring error for") {
		t.Errorf("truncation kept the wrong end: %.60s", got)
	}
}

func TestTrimOutput_ShortOutputIsUntouched(t *testing.T) {
	const short = "Fatal: wrong password"

	got := trimOutput([]byte(short + "\n\n"))

	if got != short {
		t.Errorf("trimOutput(%q) = %q, want it trimmed of whitespace but otherwise intact", short, got)
	}
	if strings.Contains(got, "truncated") {
		t.Error("short output must not be marked as truncated")
	}
}

func TestTrimOutput_Empty(t *testing.T) {
	if got := trimOutput(nil); got != "" {
		t.Errorf("trimOutput(nil) = %q, want empty", got)
	}
}
