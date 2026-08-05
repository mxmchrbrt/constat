package webhook

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/report"
)

func fixedReport(verdict report.Verdict) report.Report {
	assertVerdict := report.Pass
	msg := "all good"
	if verdict != report.Pass {
		assertVerdict = verdict
		msg = "newest row is 90 days old"
	}
	return report.New(
		time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC),
		"backup-host-01", "v0.1.0",
		[]report.Target{{
			Name: "app-db", Verdict: verdict, Kind: "restic", Repository: "/srv/backups/app",
			Assertions: []report.Assertion{{Name: "query_newer_than", Verdict: assertVerdict, Message: msg}},
		}},
	)
}

func TestSend_Success(t *testing.T) {
	var gotMethod, gotContentType string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := Send(context.Background(), Config{URL: srv.URL, Format: Generic}, fixedReport(report.Pass))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %s, want application/json", gotContentType)
	}
	if !strings.Contains(string(gotBody), `"app-db"`) {
		t.Errorf("body does not contain the report: %s", gotBody)
	}
}

func TestSend_NonTwoXXIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	err := Send(context.Background(), Config{URL: srv.URL, Format: Generic, MaxAttempts: 1}, fixedReport(report.Fail))
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error does not mention the status: %v", err)
	}
}

func TestSend_TimeoutIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	err := Send(context.Background(), Config{URL: srv.URL, Format: Generic, Timeout: 20 * time.Millisecond, MaxAttempts: 1}, fixedReport(report.Pass))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed > time.Second {
		t.Errorf("took %s; the timeout was not enforced", elapsed)
	}
}

// A closed port refuses the connection immediately rather than hanging, which
// is a distinct failure path from a timeout and must be handled the same way:
// reported, never fatal to the run.
func TestSend_ConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens here now

	err = Send(context.Background(), Config{URL: "http://" + addr, Format: Generic, Timeout: time.Second, MaxAttempts: 1}, fixedReport(report.Pass))
	if err == nil {
		t.Fatal("expected a connection-refused error")
	}
}

// The property the whole safety section rests on: whatever Send returns, the
// caller decides the exit code from the report's own verdict, never from
// delivery success. This test does not call Send at all — it is here to make
// that property explicit and grep-able, since nothing else in the package
// enforces it structurally.
func TestSend_ReturnValueMustNeverGateTheExitCode(t *testing.T) {
	t.Log("see cmd/constat/main.go: os.Exit depends on rep.Verdict, never on the webhook error")
}

func TestSend_RetriesOnFailureThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := Send(context.Background(), Config{URL: srv.URL, Format: Generic, MaxAttempts: 3, Timeout: time.Second}, fixedReport(report.Pass))
	if err != nil {
		t.Fatalf("unexpected error after eventual success: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server saw %d calls, want 3", got)
	}
}

func TestSend_RetriesAreBounded(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := Send(context.Background(), Config{URL: srv.URL, Format: Generic, MaxAttempts: 3, Timeout: time.Second}, fixedReport(report.Pass))
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server saw %d calls, want exactly 3 (bounded retries)", got)
	}
}

func TestSend_MaxAttemptsOneDisablesRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	Send(context.Background(), Config{URL: srv.URL, Format: Generic, MaxAttempts: 1, Timeout: time.Second}, fixedReport(report.Pass))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d calls, want 1", got)
	}
}

func TestSend_RespectsContextCancellationDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := Send(ctx, Config{URL: srv.URL, Format: Generic, MaxAttempts: 5, Timeout: time.Second}, fixedReport(report.Pass))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	// The first retry waits 1s; cancellation at 50ms must cut that short, not
	// wait it out.
	if elapsed > 500*time.Millisecond {
		t.Errorf("took %s; context cancellation did not interrupt the backoff wait", elapsed)
	}
}

func TestSend_NoURLIsAnError(t *testing.T) {
	if err := Send(context.Background(), Config{Format: Generic}, fixedReport(report.Pass)); err == nil {
		t.Fatal("expected an error for a missing URL")
	}
}

func TestSend_UnknownFormatIsAnError(t *testing.T) {
	if err := Send(context.Background(), Config{URL: "http://example.test", Format: "carrier-pigeon"}, fixedReport(report.Pass)); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

// A webhook URL can carry its own secret in the path — a Discord webhook
// token, a private ntfy topic. net/http's own errors embed the full request
// URL, so naively wrapping one would leak that secret into constat's own error
// output the moment delivery failed.
func TestSend_ErrorNeverContainsTheFullURL(t *testing.T) {
	const secretToken = "SENTINEL-WEBHOOK-TOKEN-a91c4"

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	targetURL := "http://" + addr + "/webhooks/123/" + secretToken

	err = Send(context.Background(), Config{URL: targetURL, Format: Generic, Timeout: time.Second, MaxAttempts: 1}, fixedReport(report.Pass))
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("error leaked the URL's secret path segment: %v", err)
	}
	if strings.Contains(err.Error(), "/webhooks/123") {
		t.Errorf("error leaked the request path: %v", err)
	}
	// The host is expected to appear — showing where delivery was attempted is
	// the point. Only the path, which is where the secret lives, must be gone.
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error should still name the host it tried to reach: %v", err)
	}
}

// Format-specific request shapes: each is checked against the actual protocol
// documented for that service, not just "a request went out".

func TestNtfyRequest_Shape(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Send(context.Background(), Config{URL: srv.URL, Format: Ntfy}, fixedReport(report.Fail)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := gotHeaders.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q, want high for a failing run", got)
	}
	if !strings.Contains(gotHeaders.Get("Title"), "FAIL") {
		t.Errorf("Title = %q, want it to mention FAIL", gotHeaders.Get("Title"))
	}
	if !strings.Contains(string(gotBody), "app-db") {
		t.Errorf("body does not summarise the target: %s", gotBody)
	}
}

func TestNtfyRequest_PassIsNotHighPriority(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	Send(context.Background(), Config{URL: srv.URL, Format: Ntfy}, fixedReport(report.Pass))
	if got := gotHeaders.Get("Priority"); got == "high" {
		t.Error("a passing run must not be sent at high priority")
	}
}

func TestHealthchecksRequest_PassPingsBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Send(context.Background(), Config{URL: srv.URL + "/ping/abc123", Format: Healthchecks}, fixedReport(report.Pass)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/ping/abc123" {
		t.Errorf("path = %q, want /ping/abc123 unmodified on pass", gotPath)
	}
}

func TestHealthchecksRequest_FailAppendsFailSuffix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Send(context.Background(), Config{URL: srv.URL + "/ping/abc123", Format: Healthchecks}, fixedReport(report.Fail)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/ping/abc123/fail" {
		t.Errorf("path = %q, want /ping/abc123/fail on failure", gotPath)
	}
}

func TestHealthchecksRequest_ErrorAlsoAppendsFailSuffix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	Send(context.Background(), Config{URL: srv.URL + "/ping/abc123", Format: Healthchecks}, fixedReport(report.Error))
	if gotPath != "/ping/abc123/fail" {
		t.Errorf("path = %q, want /ping/abc123/fail on error too", gotPath)
	}
}

func TestDiscordRequest_Shape(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Send(context.Background(), Config{URL: srv.URL, Format: Discord}, fixedReport(report.Fail)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %s, want application/json", gotContentType)
	}
	if !strings.Contains(string(gotBody), `"content"`) {
		t.Errorf("body is not a Discord content payload: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), "app-db") {
		t.Errorf("content does not summarise the target: %s", gotBody)
	}
}

func TestBackoff_IsBoundedAndIncreasing(t *testing.T) {
	prev := time.Duration(0)
	for n := 1; n <= 6; n++ {
		got := backoff(n)
		if got < prev {
			t.Errorf("backoff(%d) = %s, less than backoff(%d) = %s", n, got, n-1, prev)
		}
		if got > 30*time.Second {
			t.Errorf("backoff(%d) = %s, unreasonably large", n, got)
		}
		prev = got
	}
}

func TestConfig_Defaults(t *testing.T) {
	var c Config
	if c.effectiveTimeout() != DefaultTimeout {
		t.Errorf("effectiveTimeout() = %s, want %s", c.effectiveTimeout(), DefaultTimeout)
	}
	if c.effectiveMaxAttempts() != DefaultMaxAttempts {
		t.Errorf("effectiveMaxAttempts() = %d, want %d", c.effectiveMaxAttempts(), DefaultMaxAttempts)
	}
}
