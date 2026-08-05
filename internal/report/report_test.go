package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden report files")

// fixedReport is the report every golden and determinism test uses. Time and
// host are injected rather than read from the machine, which is the only reason
// a golden file is possible at all.
func fixedReport() Report {
	return New(
		time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC),
		"backup-host-01",
		"v0.1.0",
		[]Target{
			{
				Name:              "lab-files",
				Verdict:           Pass,
				Kind:              "restic",
				Repository:        "/srv/backups/lab",
				RestoreDurationMs: 3156,
				Assertions: []Assertion{
					{Name: "newest_snapshot_age_max", Verdict: Pass, Message: "newest snapshot 5f1d22e1 is 4h15m8s old (max 48h0m0s)", DurationMs: 812},
					{Name: "path_exists", Verdict: Pass, Message: `path "config.php" exists: true`, DurationMs: 0},
				},
			},
			{
				Name:              "app-db",
				Verdict:           Fail,
				Kind:              "restic",
				Repository:        "/srv/backups/app",
				RestoreDurationMs: 14022,
				Assertions: []Assertion{
					{Name: "query_min", Verdict: Pass, Message: "query returned 500 (min 1)", DurationMs: 41},
					{Name: "query_newer_than", Verdict: Fail, Message: "newest row is 2160h0m0s old (newer_than 24h0m0s)", DurationMs: 38},
				},
			},
		},
	)
}

func goldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := goldenPath(t, name)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run go test ./internal/report -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match %s\n got: %s\nwant: %s", path, got, want)
	}
}

func TestCanonical_MatchesGolden(t *testing.T) {
	got, err := Canonical(fixedReport())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	compareGolden(t, "report.golden.json", got)
}

// The property the whole signing scheme rests on: the same report serialises to
// the same bytes, every time. If this ever fails, every signature this tool has
// ever produced becomes unverifiable.
func TestCanonical_IsDeterministic(t *testing.T) {
	first, err := Canonical(fixedReport())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 50; i++ {
		again, err := Canonical(fixedReport())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("serialisation %d differs:\nfirst: %s\nagain: %s", i, first, again)
		}
	}
}

// A report that survives a JSON round trip unchanged is what lets a verifier
// re-derive the signed bytes. Sub-second timestamps are the usual way this
// breaks, which is why GeneratedAt is truncated to the second.
func TestCanonical_SurvivesARoundTrip(t *testing.T) {
	original, err := Canonical(fixedReport())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(original, &decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	again, err := Canonical(decoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(original, again) {
		t.Errorf("a report changed shape through a round trip:\nbefore: %s\nafter:  %s", original, again)
	}
}

// A message that came out of a backup can contain anything at all. It must not
// be able to change the shape of the document that carries it.
func TestCanonical_HostileMessagesDoNotEscapeTheirField(t *testing.T) {
	hostile := []string{
		`<script>alert(1)</script>`,
		`", "verdict": "pass", "x": "`,
		"line\nbreak\ttab",
		`back\slash and "quotes"`,
		"unicode: é中 and an emoji",
		"null byte-ish: \\u0000",
	}

	for _, message := range hostile {
		r := New(time.Unix(0, 0).UTC(), "h", "v", []Target{{
			Name:       "t",
			Verdict:    Fail,
			Assertions: []Assertion{{Name: "a", Verdict: Fail, Message: message}},
		}})

		got, err := Canonical(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var decoded Report
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("message %q produced unparseable JSON: %v\n%s", message, err, got)
		}
		if decoded.Targets[0].Assertions[0].Message != message {
			t.Errorf("message %q came back as %q", message, decoded.Targets[0].Assertions[0].Message)
		}
		if decoded.Verdict != Fail {
			t.Errorf("message %q changed the report's verdict to %q", message, decoded.Verdict)
		}
	}
}

// HTML escaping is off deliberately. With it on, a message containing "<" would
// serialise differently depending on the encoder's settings, and a signature is
// only as good as the reproducibility of the bytes under it.
func TestCanonical_DoesNotEscapeHTML(t *testing.T) {
	r := New(time.Unix(0, 0).UTC(), "h", "v", []Target{{
		Name:       "t",
		Assertions: []Assertion{{Name: "a", Message: "a < b && c > d"}},
	}})

	got, err := Canonical(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With escaping on, Go rewrites < > & as < > &. The absence
	// of those sequences is the check. The presence of a literal "<" is not,
	// since the unescaped message contains one by design.
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if bytes.Contains(got, []byte(escaped)) {
			t.Errorf("HTML escaping is on (found %s): %s", escaped, got)
		}
	}
	if !bytes.Contains(got, []byte("a < b && c > d")) {
		t.Errorf("message not present verbatim: %s", got)
	}
}

func TestNew_Verdict(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
		want    Verdict
	}{
		{name: "no targets", targets: nil, want: Pass},
		{name: "all pass", targets: []Target{{Verdict: Pass}, {Verdict: Pass}}, want: Pass},
		{name: "one fails", targets: []Target{{Verdict: Pass}, {Verdict: Fail}}, want: Fail},
		{
			// Error outranks fail: a run that could not answer must not be
			// reported as one that answered "no".
			name:    "error outranks fail",
			targets: []Target{{Verdict: Fail}, {Verdict: Error}},
			want:    Error,
		},
		{name: "error alone", targets: []Target{{Verdict: Error}}, want: Error},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New(time.Now(), "h", "v", tt.targets).Verdict; got != tt.want {
				t.Errorf("verdict = %q, want %q", got, tt.want)
			}
		})
	}
}

// A report with no targets must serialise an empty list, not null: a consumer
// should not have to handle two spellings of "nothing here".
func TestNew_EmptyTargetsIsAListNotNull(t *testing.T) {
	got, err := Canonical(New(time.Unix(0, 0).UTC(), "h", "v", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(got, []byte(`"targets":[]`)) {
		t.Errorf("expected an empty list: %s", got)
	}
}

func TestNew_TimeIsUTCAndWholeSeconds(t *testing.T) {
	local := time.Date(2026, 8, 5, 16, 30, 0, 123456789, time.FixedZone("CEST", 2*3600))

	r := New(local, "h", "v", nil)

	if r.GeneratedAt.Location() != time.UTC {
		t.Errorf("GeneratedAt is in %v, want UTC", r.GeneratedAt.Location())
	}
	if r.GeneratedAt.Nanosecond() != 0 {
		t.Errorf("GeneratedAt kept sub-second precision: %v", r.GeneratedAt)
	}
	if got, want := r.GeneratedAt.Format(time.RFC3339), "2026-08-05T14:30:00Z"; got != want {
		t.Errorf("GeneratedAt = %s, want %s", got, want)
	}
}

func TestVerdict_Failed(t *testing.T) {
	if Pass.Failed() {
		t.Error("pass must not fail the run")
	}
	if !Fail.Failed() || !Error.Failed() {
		t.Error("fail and error must both fail the run")
	}
}

// --- signing seam ---

// stubSigner is test scaffolding, not an implementation: it holds no key and
// performs no cryptography. Implementing Signer for real is the author's, per
// CLAUDE.md.
type stubSigner struct {
	sig  []byte
	err  error
	sawn []byte
}

func (s *stubSigner) KeyID() string     { return "test-key" }
func (s *stubSigner) Algorithm() string { return "stub" }
func (s *stubSigner) Sign(canonical []byte) ([]byte, error) {
	s.sawn = append([]byte(nil), canonical...)
	if s.err != nil {
		return nil, s.err
	}
	return s.sig, nil
}

func TestWrite_Unsigned(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, fixedReport(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	compareGolden(t, "envelope-unsigned.golden.json", buf.Bytes())

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("parsing envelope: %v", err)
	}
	if env.Signature != nil {
		t.Error("an unsigned report must have no signature field at all")
	}
}

// The signature must cover exactly the bytes that end up in the file. If the
// envelope re-serialised the report, the signature would not verify against the
// document it sits in — which is the classic way a signing scheme is wrong
// while looking right.
func TestWrite_SignsExactlyTheBytesInTheFile(t *testing.T) {
	signer := &stubSigner{sig: []byte("signature-bytes")}

	var buf bytes.Buffer
	if err := Write(&buf, fixedReport(), signer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("parsing envelope: %v", err)
	}

	if !bytes.Equal(env.Report, signer.sawn) {
		t.Errorf("signed bytes differ from the bytes in the file:\nfile:   %s\nsigned: %s", env.Report, signer.sawn)
	}

	canonical, err := Canonical(fixedReport())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(signer.sawn, canonical) {
		t.Errorf("the signer did not receive the canonical form:\nsigned:    %s\ncanonical: %s", signer.sawn, canonical)
	}

	if env.Signature == nil {
		t.Fatal("no signature recorded")
	}
	if env.Signature.Algorithm != "stub" || env.Signature.KeyID != "test-key" {
		t.Errorf("signature metadata not recorded: %+v", env.Signature)
	}
	if !bytes.Equal(env.Signature.Value, []byte("signature-bytes")) {
		t.Errorf("signature value = %q", env.Signature.Value)
	}
}

func TestWrite_SignerFailureIsFatal(t *testing.T) {
	var buf bytes.Buffer
	err := Write(&buf, fixedReport(), &stubSigner{err: errors.New("key unavailable")})
	if err == nil {
		t.Fatal("a failed signature must not produce a report")
	}
	if !strings.Contains(err.Error(), "signing report") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// A present-but-empty signature is worse than none: it looks signed.
func TestWrite_EmptySignatureIsRejected(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, fixedReport(), &stubSigner{sig: []byte{}}); err == nil {
		t.Fatal("an empty signature must be rejected")
	}
}

// stubVerifier records what it was asked to check. Like stubSigner, it performs
// no cryptography.
type stubVerifier struct {
	sawCanonical []byte
	sawSignature []byte
	err          error
}

func (v *stubVerifier) Verify(canonical, signature []byte, keyID, algorithm string) error {
	v.sawCanonical = append([]byte(nil), canonical...)
	v.sawSignature = append([]byte(nil), signature...)
	return v.err
}

// Verification must read the bytes in the file, never a re-serialisation of a
// decoded report — otherwise it checks what this code believes the report says
// rather than what the file says.
func TestVerifyEnvelope_UsesTheBytesFromTheFile(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, fixedReport(), &stubSigner{sig: []byte("sig")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tamper with the report inside the envelope without touching the
	// signature. A verifier that re-serialises would not notice.
	tampered := bytes.Replace(buf.Bytes(), []byte(`"verdict":"fail"`), []byte(`"verdict":"pass"`), 1)
	if bytes.Equal(tampered, buf.Bytes()) {
		t.Fatal("test fixture did not contain the string it meant to tamper with")
	}

	v := &stubVerifier{}
	if err := VerifyEnvelope(tampered, v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(v.sawCanonical, []byte(`"verdict":"pass"`)) {
		t.Error("the verifier was not given the tampered bytes from the file")
	}
	if !bytes.Equal(v.sawSignature, []byte("sig")) {
		t.Errorf("signature passed to verifier = %q", v.sawSignature)
	}
}

func TestVerifyEnvelope_Unsigned(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, fixedReport(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := VerifyEnvelope(buf.Bytes(), &stubVerifier{}); !errors.Is(err, ErrUnsigned) {
		t.Errorf("got %v, want ErrUnsigned", err)
	}
}

func TestVerifyEnvelope_PropagatesFailure(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, fixedReport(), &stubSigner{sig: []byte("sig")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v := &stubVerifier{err: errors.New("bad signature")}
	if err := VerifyEnvelope(buf.Bytes(), v); err == nil {
		t.Fatal("a failed verification must be reported")
	}
}
