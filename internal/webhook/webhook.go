// Package webhook posts a run's outcome to an outbound URL — ntfy,
// Healthchecks.io, Discord, or a generic JSON endpoint.
//
// A failing webhook must never change the run's verdict: whether the
// notification got through is a separate, lesser fact than whether the
// backup is restorable.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mxmchrbrt/constat/internal/report"
)

// Format selects how the report is rendered for the destination. A config
// choice, not sniffed from the URL — "looks like ntfy.sh" breaks the moment
// an operator self-hosts the same service under their own domain.
type Format string

const (
	// Generic posts the full JSON report. The default, and the only format
	// that loses no information.
	Generic Format = "generic"
	Ntfy    Format = "ntfy"
	// Healthchecks pings the URL on pass and the URL with /fail appended on
	// fail or error, per that service's own check-in protocol.
	Healthchecks Format = "healthchecks"
	Discord      Format = "discord"
)

// Config is what a run needs to send one notification.
type Config struct {
	URL    string
	Format Format

	// Bounds a single HTTP attempt.
	Timeout time.Duration

	// Bounds total attempts, including the first. 1 disables retries.
	MaxAttempts int
}

// DefaultTimeout and DefaultMaxAttempts apply when Config leaves them zero.
const (
	DefaultTimeout     = 10 * time.Second
	DefaultMaxAttempts = 3
)

func (c Config) effectiveTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

func (c Config) effectiveMaxAttempts() int {
	if c.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return c.MaxAttempts
}

// Send posts r to cfg.URL, retrying transient failures with a bounded
// backoff. The returned error is informational — report it, don't let it
// change an exit code.
func Send(ctx context.Context, cfg Config, r report.Report) error {
	if cfg.URL == "" {
		return fmt.Errorf("webhook: no URL configured")
	}

	req, err := buildRequest(cfg, r)
	if err != nil {
		return fmt.Errorf("webhook: building request: %w", redactURL(err, cfg.URL))
	}

	client := &http.Client{Timeout: cfg.effectiveTimeout()}

	var lastErr error
	attempts := cfg.effectiveMaxAttempts()
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			wait := backoff(attempt - 1)
			select {
			case <-ctx.Done():
				return fmt.Errorf("webhook: %w", ctx.Err())
			case <-time.After(wait):
			}
		}

		attemptReq := req.Clone(ctx)
		if req.Body != nil {
			// Clone shares the body reader; a retry needs its own copy.
			body, _ := req.GetBody()
			attemptReq.Body = body
		}

		lastErr = attemptOnce(client, attemptReq)
		if lastErr == nil {
			return nil
		}
	}

	return fmt.Errorf("webhook: giving up after %d attempt(s): %w", attempts, redactURL(lastErr, cfg.URL))
}

// attemptOnce sends one request. A non-2xx status is treated as a delivery
// failure worth retrying; the response body is capped since a misconfigured
// endpoint can echo the whole request back.
func attemptOnce(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("received status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

// backoff is the wait before retry attempt n (1-indexed). Fixed steps rather
// than exponential-with-jitter: three attempts makes the difference
// unobservable, and fixed steps are easy to verify by inspection.
func backoff(n int) time.Duration {
	steps := []time.Duration{time.Second, 3 * time.Second, 8 * time.Second}
	if n-1 < len(steps) {
		return steps[n-1]
	}
	return steps[len(steps)-1]
}

func buildRequest(cfg Config, r report.Report) (*http.Request, error) {
	switch cfg.Format {
	case Ntfy:
		return ntfyRequest(cfg.URL, r)
	case Healthchecks:
		return healthchecksRequest(cfg.URL, r)
	case Discord:
		return discordRequest(cfg.URL, r)
	case "", Generic:
		return genericRequest(cfg.URL, r)
	default:
		return nil, fmt.Errorf("unknown webhook format %q", cfg.Format)
	}
}

func newJSONRequest(rawURL string, body []byte) (*http.Request, error) {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return req, nil
}

func genericRequest(rawURL string, r report.Report) (*http.Request, error) {
	canonical, err := report.Canonical(r)
	if err != nil {
		return nil, err
	}
	return newJSONRequest(rawURL, canonical)
}

// discordRequest posts {"content": "..."} per Discord's webhook API, capped
// under its 2000-character message limit.
func discordRequest(rawURL string, r report.Report) (*http.Request, error) {
	body, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Content: summary(r, 1900)})
	if err != nil {
		return nil, err
	}
	return newJSONRequest(rawURL, body)
}

// ntfyRequest posts the summary as plain text — any POST to an ntfy topic URL
// becomes the notification body.
func ntfyRequest(rawURL string, r report.Report) (*http.Request, error) {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	body := []byte(summary(r, 4000))

	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", fmt.Sprintf("constat: %s on %s", strings.ToUpper(string(r.Verdict)), r.Host))
	if r.Verdict != report.Pass {
		req.Header.Set("Priority", "high")
		req.Header.Set("Tags", "warning")
	} else {
		req.Header.Set("Tags", "white_check_mark")
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return req, nil
}

// healthchecksRequest pings rawURL on pass, and rawURL+"/fail" on fail or
// error — Healthchecks.io's manual check-in protocol.
func healthchecksRequest(rawURL string, r report.Report) (*http.Request, error) {
	target := rawURL
	if r.Verdict != report.Pass {
		target = strings.TrimRight(rawURL, "/") + "/fail"
	}
	if _, err := url.ParseRequestURI(target); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	body := []byte(summary(r, 10000)) // Healthchecks accepts up to 10KB as the check's log.
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return req, nil
}

// summary is the human-readable rendering shared by the text-based formats,
// capped to n runes so a large run cannot exceed a destination's body limit.
func summary(r report.Report, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "constat: %s on %s (%s)\n", strings.ToUpper(string(r.Verdict)), r.Host, r.GeneratedAt.Format(time.RFC3339))
	for _, t := range r.Targets {
		fmt.Fprintf(&b, "  %s: %s\n", strings.ToUpper(string(t.Verdict)), t.Name)
		for _, a := range t.Assertions {
			if a.Verdict == report.Pass {
				continue
			}
			fmt.Fprintf(&b, "    - %s: %s\n", a.Name, a.Message)
		}
	}
	out := b.String()
	if len(out) > n {
		out = out[:n] + "… (truncated)"
	}
	return out
}

// redactURL replaces a webhook error with one naming only the host, not the
// full URL — a webhook URL can carry its own secret in the path (a Discord
// token, a private ntfy topic), and net/http's *url.Error embeds the
// complete request URL in its Error() string, so the cause is unwrapped
// rather than trusting a string replace to catch it.
func redactURL(err error, rawURL string) error {
	if err == nil {
		return nil
	}

	host := "unknown host"
	if u, parseErr := url.Parse(rawURL); parseErr == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}

	cause := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		cause = urlErr.Err
	}

	return fmt.Errorf("%v (destination %s)", cause, host)
}
