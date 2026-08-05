package report

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The entire reason html/template is used instead of text/template: an
// assertion message is free text built from whatever was in the customer's
// backup — a filename, a query result — and must never be able to inject markup
// into the page that reports on it.
func TestHTML_EscapesHostileMessages(t *testing.T) {
	r := New(time.Unix(0, 0).UTC(), "h", "v", []Target{{
		Name:    `evil<script>alert(1)</script>`,
		Verdict: Fail,
		Kind:    "restic",
		Assertions: []Assertion{
			{Name: "a", Verdict: Fail, Message: `<img src=x onerror=alert(document.cookie)>`},
			{Name: "b", Verdict: Fail, Message: `"><svg onload=alert(1)>`},
		},
	}})

	var buf bytes.Buffer
	if err := HTML(&buf, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	for _, raw := range []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=",
		"<svg onload=alert(1)>",
	} {
		if strings.Contains(out, raw) {
			t.Errorf("unescaped payload made it into the page: %q\n%s", raw, out)
		}
	}

	// The content must still be present, just neutralised — an assertion
	// message that vanishes is as unhelpful as one that executes.
	if !strings.Contains(out, "alert(1)") {
		t.Error("the message's text content did not survive escaping at all")
	}
}

func TestHTML_RendersVerdictsAndValues(t *testing.T) {
	r := New(
		time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC),
		"backup-host-01", "v0.1.0",
		[]Target{
			{
				Name: "lab-files", Verdict: Pass, Kind: "restic", Repository: "/srv/backups/lab",
				RestoreDurationMs: 3156,
				Assertions: []Assertion{
					{Name: "path_exists", Verdict: Pass, Message: "ok", DurationMs: 12},
				},
			},
			{
				Name: "app-db", Verdict: Fail, Kind: "restic", Repository: "/srv/backups/app",
				Assertions: []Assertion{
					{Name: "query_newer_than", Verdict: Fail, Message: "stale", DurationMs: 41},
				},
			},
		},
	)

	var buf bytes.Buffer
	if err := HTML(&buf, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"backup-host-01", "v0.1.0", "2026-08-05 14:30:00 UTC",
		"lab-files", "/srv/backups/lab", "3.2s",
		"app-db", "/srv/backups/app",
		"path_exists", "query_newer_than",
		"PASS", "FAIL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestHTML_NoTargets(t *testing.T) {
	r := New(time.Unix(0, 0).UTC(), "h", "v", nil)

	var buf bytes.Buffer
	if err := HTML(&buf, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "no targets") {
		t.Error("expected an explicit empty state")
	}
}

func TestHTML_NoAssertions(t *testing.T) {
	r := New(time.Unix(0, 0).UTC(), "h", "v", []Target{{Name: "t", Verdict: Error}})

	var buf bytes.Buffer
	if err := HTML(&buf, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "no assertions ran") {
		t.Error("expected an explicit empty state for a target with no assertion results")
	}
}

// The page is self-contained: no external stylesheet, no script tag, nothing
// that stops working the day a report is opened offline six months later.
func TestHTML_IsSelfContained(t *testing.T) {
	var buf bytes.Buffer
	if err := HTML(&buf, fixedReport()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "<script") {
		t.Error("report page contains a script tag")
	}
	if strings.Contains(out, `rel="stylesheet"`) {
		t.Error("report page links an external stylesheet")
	}
	if strings.Contains(out, `src="http`) {
		t.Error("report page loads an external script or image")
	}
}
