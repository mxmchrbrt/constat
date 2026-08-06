package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// Signer turns the canonical bytes of a report into a detached signature.
// internal/signing implements this over ed25519.
//
// Contract: Sign receives exactly the bytes Canonical produced and must not
// re-serialise or reorder the report. KeyID identifies the public key a
// verifier should use — a fingerprint of the public key, never derived from
// or leaking the private key. Algorithm names the scheme (e.g. "ed25519") so
// a future scheme can be added without old reports becoming ambiguous.
// Errors from Sign must never contain key material.
type Signer interface {
	KeyID() string
	Algorithm() string
	Sign(canonical []byte) ([]byte, error)
}

// Signature is the detached signature recorded alongside a report.
type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`

	// Base64-rendered by encoding/json, which is what a detached signature
	// wants anyway.
	Value []byte `json:"value"`
}

// Envelope is what gets written to disk: the report, plus the signature over
// its canonical form.
//
// Report is json.RawMessage holding exactly the canonical bytes, not a Report
// struct — re-serialising it could produce bytes that differ from what was
// signed, making the signature unverifiable against the file it sits in.
type Envelope struct {
	Report    json.RawMessage `json:"report"`
	Signature *Signature      `json:"signature,omitempty"`
}

// Write serialises r to w, signing it when signer is non-nil. An unsigned
// report is a legitimate output, not a degraded one.
func Write(w io.Writer, r Report, signer Signer) error {
	canonical, err := Canonical(r)
	if err != nil {
		return err
	}

	env := Envelope{Report: json.RawMessage(canonical)}

	if signer != nil {
		value, err := signer.Sign(canonical)
		if err != nil {
			return fmt.Errorf("signing report: %w", err)
		}
		if len(value) == 0 {
			// Present-but-empty is worse than absent: it looks signed.
			return fmt.Errorf("signing report: signer produced an empty signature")
		}
		env.Signature = &Signature{
			Algorithm: signer.Algorithm(),
			KeyID:     signer.KeyID(),
			Value:     value,
		}
	}

	out, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("serialising report envelope: %w", err)
	}

	if _, err := w.Write(append(out, '\n')); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	return nil
}

// Verifier checks a detached signature. Separate from Signer because
// verification needs only the public key.
type Verifier interface {
	Verify(canonical, signature []byte, keyID, algorithm string) error
}

// ErrUnsigned is returned when a report carries no signature at all.
var ErrUnsigned = fmt.Errorf("report carries no signature")

// VerifyEnvelope checks a written report against its signature, using the
// report bytes exactly as they appear in raw rather than a re-serialisation.
func VerifyEnvelope(raw []byte, v Verifier) error {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("parsing report envelope: %w", err)
	}
	if env.Signature == nil {
		return ErrUnsigned
	}
	if v == nil {
		return fmt.Errorf("no verifier provided")
	}
	return v.Verify(env.Report, env.Signature.Value, env.Signature.KeyID, env.Signature.Algorithm)
}
