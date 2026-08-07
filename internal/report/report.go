// Package report produces the artifact the product is named after: a dated
// statement of fact about whether a backup could be restored.
//
// It has two constraints: it must serialise canonically (Canonical) so a
// signature over it means something, and it must carry no secret — repository
// paths and image names are fine, credentials never are.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the version of this report's shape.
const SchemaVersion = 1

// Verdict is the outcome of a check, a target, or a whole run.
//
// Three values, not two: Fail means the backup is not in the state it should
// be, Error means constat could not answer the question. Severity ("passed
// with warnings") is deliberately absent — see ARCHITECTURE.md.
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

	// Integer milliseconds, not a Go duration string — the report is read by
	// other languages.
	DurationMs int64 `json:"duration_ms"`
}

// Target is one verified target: its assertions, and how it came out.
type Target struct {
	Name    string  `json:"name"`
	Verdict Verdict `json:"verdict"`

	Kind       string `json:"source_kind"`
	Repository string `json:"repository"`

	// Zero when nothing was restored — a metadata-only target never touched
	// the data.
	RestoreDurationMs int64 `json:"restore_duration_ms"`

	// Nil for a target with no verify_with block, which keeps the canonical
	// bytes of a file-only report unchanged.
	Database *Database `json:"database,omitempty"`

	Assertions []Assertion `json:"assertions"`
}

// Database records which Postgres the dump declared it came from and which
// one it was loaded into.
//
// This is the report earning the name: pinning the source version in
// Infrastructure-as-Code prevents the runbook from drifting, but a signed,
// dated report stating the versions is evidence of what was true at the time,
// which is what someone reconstructing a restore a year later actually needs
// (taxonomy #9). Empty strings where a version could not be determined — a
// custom-format dump carries no readable header.
type Database struct {
	// The version pg_dump wrote into the dump header, e.g. "16.14".
	DumpVersion string `json:"dump_version,omitempty"`

	// The version of the disposable server it was loaded into.
	ServerVersion string `json:"server_version,omitempty"`

	// The configured verify_with.image, recorded verbatim so the drill is
	// reproducible from the report alone.
	Image string `json:"image,omitempty"`
}

// Report is one run.
type Report struct {
	SchemaVersion int `json:"schema_version"`

	// Always UTC.
	GeneratedAt time.Time `json:"generated_at"`

	Host    string   `json:"host"`
	Version string   `json:"constat_version"`
	Verdict Verdict  `json:"verdict"`
	Targets []Target `json:"targets"`
}

// New assembles a report from the targets of one run and derives the overall
// verdict. generatedAt and host are parameters, not read from the
// environment, so callers control what "now" means and tests stay
// reproducible.
func New(generatedAt time.Time, host, version string, targets []Target) Report {
	verdict := Pass
	for _, t := range targets {
		// Error outranks fail: a run that couldn't answer must not be
		// reported as one that answered "no".
		if t.Verdict == Error {
			verdict = Error
			break
		}
		if t.Verdict == Fail {
			verdict = Fail
		}
	}

	if targets == nil {
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
// what a signature covers: fixed struct field order, no maps, UTC timestamps
// truncated to the second, integer durations, HTML escaping off, no
// indentation.
func Canonical(r Report) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("serialising report: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
