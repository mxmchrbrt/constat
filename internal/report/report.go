// Package report produces the artifact the product is named after: a dated
// statement of fact about whether a backup could be restored.
//
// Two properties matter more than anything else here.
//
// The report must be *canonical*: serialising the same report twice must give
// byte-identical output, because a signature over non-deterministic bytes
// proves nothing. Go's encoder gives struct fields in declaration order and map
// keys in sorted order, which is most of the way there; the rest is doing the
// remaining work explicitly rather than relying on it.
//
// The report must carry *no secret*. Repository paths, password-file paths and
// image names are all fine and useful. The contents of a password file, and any
// credential inside a load command, must never reach it. This is the one place
// where a leak is durable: a report is written to disk, posted to a webhook,
// and kept as evidence.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the version of this report's shape. It exists so a consumer
// reading a report written months earlier can tell what it is looking at, and
// it goes first in the serialised form for the same reason.
const SchemaVersion = 1

// Verdict is the outcome of a check, a target, or a whole run.
//
// Three values, not two, and the third is not a severity. Fail means the backup
// is not in the state it should be; Error means constat could not answer the
// question. Collapsing them would either alert on the tool's own breakage or
// stay quiet about a real failure. Severity — a "passed with warnings" state —
// is still deliberately absent, per ARCHITECTURE.md.
type Verdict string

const (
	Pass  Verdict = "pass"
	Fail  Verdict = "fail"
	Error Verdict = "error"
)

// Failed reports whether v should make the run exit non-zero.
func (v Verdict) Failed() bool { return v != Pass }

// Assertion is one check's outcome.
type Assertion struct {
	Name    string  `json:"name"`
	Verdict Verdict `json:"verdict"`
	Message string  `json:"message"`

	// DurationMs is milliseconds rather than a Go duration string: a report is
	// read by other programs, and "1.5s" is a parsing problem for every one of
	// them. Integer milliseconds is precise enough for a check whose useful
	// range is seconds to hours.
	DurationMs int64 `json:"duration_ms"`
}

// Target is one verified target: its assertions, and how it came out.
type Target struct {
	Name    string  `json:"name"`
	Verdict Verdict `json:"verdict"`

	// Repository and Kind identify what was checked. Paths are safe to record
	// and are most of what makes a report evidence rather than an assertion
	// that something, somewhere, was fine.
	Kind       string `json:"source_kind"`
	Repository string `json:"repository"`

	// RestoreDurationMs is zero when nothing was restored, which is itself
	// meaningful: a metadata-only target never touched the data.
	RestoreDurationMs int64 `json:"restore_duration_ms"`

	Assertions []Assertion `json:"assertions"`
}

// Report is one run.
type Report struct {
	SchemaVersion int `json:"schema_version"`

	// GeneratedAt is always UTC. A dated statement of fact whose date depends
	// on the reader's timezone is a poor statement of fact.
	GeneratedAt time.Time `json:"generated_at"`

	Host    string   `json:"host"`
	Version string   `json:"constat_version"`
	Verdict Verdict  `json:"verdict"`
	Targets []Target `json:"targets"`
}

// New assembles a report from the targets of one run and derives the overall
// verdict.
//
// generatedAt and host are parameters rather than read from the environment
// here, so tests can produce a byte-identical report and so the caller decides
// once what "now" means for the whole run.
func New(generatedAt time.Time, host, version string, targets []Target) Report {
	verdict := Pass
	for _, t := range targets {
		// Error beats fail: if any target could not be answered, the run as a
		// whole did not answer its question, and saying "fail" would overstate
		// what is known.
		if t.Verdict == Error {
			verdict = Error
			break
		}
		if t.Verdict == Fail {
			verdict = Fail
		}
	}

	if targets == nil {
		// An explicit empty list rather than JSON null: a consumer should not
		// have to handle two spellings of "no targets".
		targets = []Target{}
	}

	return Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   generatedAt.UTC().Truncate(time.Second),
		Host:          host,
		Version:       version,
		Verdict:       verdict,
		Targets:       targets,
	}
}

// Canonical serialises r deterministically. These bytes, and only these, are
// what a signature covers.
//
// Deterministic because: struct field order is fixed by declaration; there are
// no maps in the model, so no iteration order to worry about; times are UTC and
// truncated to the second at construction, so no timezone or sub-second drift;
// durations are integers; HTML escaping is off, so a message containing "<"
// does not change shape depending on the encoder's mood; and there is no
// indentation to disagree about.
//
// Truncating to the second is a real trade: it costs resolution in
// GeneratedAt, and it buys a timestamp that survives a JSON round trip
// unchanged, which a signature verifier depends on.
func Canonical(r Report) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("serialising report: %w", err)
	}

	// Encode appends a newline. Trim it so the signed bytes are exactly the
	// JSON value and nothing else — a trailing byte that is easy to lose in
	// transit is a bad thing to sign.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
