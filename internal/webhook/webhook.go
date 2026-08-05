// Package webhook posts a run's outcome to an outbound URL — ntfy,
// Healthchecks.io, Discord, or a generic JSON endpoint.
//
// Alerting is the part of the failure taxonomy this tool cannot skip: taxonomy
// #1, silent non-execution, is only silent because nothing tells anyone the job
// stopped. A verification with no alert path is a verification nobody reads
// until it's too late.
//
// A failing webhook must never change the run's verdict. The backup is either
// restorable or it isn't; whether the notification about that fact happened to
// get through is a separate, lesser fact, and conflating them would mean a
// transient network blip on the alerting side silently repainting a real
// failure as a tool error or vice versa.
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

// Format selects how the report is rendered for the destination. Kept as a
// config-level choice rather than sniffed from the URL: sniffing "looks like
// ntfy.sh" is exactly the kind of guess that's right until the operator
// self-hosts the same service under their own domain.
type Format string

const (
	// Generic posts the full JSON report. The default: it works with any
	// endpoint that can accept a JSON body (a custom receiver, a SIEM, n8n),
	// and it's the only format that loses no information.
	Generic Format = "generic"
	Ntfy    Format = "ntfy"
	// Healthchecks pings the URL on pass and the URL with /fail appended on
	// fail or error, per that service's own protocol — there is nothing to
	// invent here, just to match.
	Healthchecks Format = "healthchecks"
	Discord      Format = "discord"
)

// Config is what a run needs to send one notification.
type Config struct {
	URL    string
	Format Format

	// Timeout bounds a single HTTP attempt. Retries get their own timeout
	// each; a hung endpoint must not be able to hold the whole run open past
	// its own configured target timeouts.
	Timeout time.Duration

	// MaxAttempts bounds total attempts, including the first. 1 disables
	// retries outright.
	MaxAttempts int
}

// DefaultTimeout and DefaultMaxAttempts apply when Config leaves them zero.
// Three attempts with a backing-off wait between them rides out a webhook
// receiver's brief restart without turning a five-minute alert delay into
// something that outruns the operator's patience.
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

// Send posts r to cfg.URL, retrying transient failures with a bounded backoff.
//
// The returned error is informational — send it to stderr, do not let it
// change an exit code. Verdict and delivery are independent facts; see the
// package doc.
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
			// Clone shares the body reader; a retried request needs its own
			// copy since the first attempt may have consumed it.
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

// attemptOnce sends one request and classifies the response. A non-2xx status
// is a delivery failure worth retrying; the body of an error response is not
// read beyond a small cap, since some misconfigured endpoints echo the whole
// request back and that has no reason to end up in constat's own error message.
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

// backoff is the wait before retry attempt n (1-indexed: the wait before the
// second overall attempt). Fixed steps rather than exponential-with-jitter:
// three attempts total makes the difference unobservable, and fixed steps are
// something a reader can verify by inspection instead of trusting a formula.
func backoff(n int) time.Duration {
	steps := []time.Duration{time.Second, 3 * time.Second, 8 * time.Second}
	if n-1 < len(steps) {
		return steps[n-1]
	}
	return steps[len(steps)-1]
}

// buildRequest renders r for cfg.Format and returns the request to send. Every
// format's URL is validated with url.Parse before use: a malformed
// operator-supplied URL must fail loudly and immediately, not turn into a
// request to some other host via whatever net/http does with a bad string.
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

// discordRequest posts {"content": "..."} per Discord's webhook API. Content
// is capped well under Discord's 2000-character limit for a message.
func discordRequest(rawURL string, r report.Report) (*http.Request, error) {
	body, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Content: summary(r, 1900)})
	if err != nil {
		return nil, err
	}
	return newJSONRequest(rawURL, body)
}

// ntfyRequest posts the summary as a plain-text body, per ntfy's simplest
// publish form: any POST to the topic URL becomes the notification body.
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

// healthchecksRequest pings the configured URL on pass, and the URL with
// "/fail" appended on fail or error — Healthchecks.io's own protocol for a
// manual (non-cron) check-in. Nothing invented here, just matched.
func healthchecksRequest(rawURL string, r report.Report) (*http.Request, error) {
	target := rawURL
	if r.Verdict != report.Pass {
		target = strings.TrimRight(rawURL, "/") + "/fail"
	}
	if _, err := url.ParseRequestURI(target); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	body := []byte(summary(r, 10000)) // Healthchecks accepts up to 10KB of body as the check's log.
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

// summary is the human-readable line-per-target rendering shared by the
// text-based formats. Capped to n runes so a run with many targets cannot blow
// past a destination's own body-size limit.
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

// redactURL replaces a webhook error with one that names only the host, not
// the full URL.
//
// This is not decoration — the naive approach of appending a redacted host to
// err.Error() would still leak, because net/http's own errors are *url.Error,
// whose Error() method embeds the complete request URL: `Post
// "https://discord.com/api/webhooks/…/the-actual-token": dial tcp: …`. A
// webhook URL is operator-controlled and can carry a path segment that is
// itself the secret — a Discord webhook token, a private ntfy topic — so the
// underlying error is unwrapped and only its cause is kept; the URL half is
// discarded outright rather than trusted to redact cleanly.
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
