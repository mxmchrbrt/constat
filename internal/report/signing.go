package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// AUTHOR-ONLY BOUNDARY.
//
// CLAUDE.md keeps anything touching signing keys in the author's hands —
// generation, loading, storage, and the ed25519 call itself — regardless of the
// v0 working-agreement pivot. Everything on this side of the boundary is
// written: the report model, the canonical bytes, the envelope, and the
// verification helper. What is deliberately absent is any implementation of
// Signer.
//
// To finish it, add a type implementing Signer that holds an ed25519 private
// key, and load that key from a path given in the config. The contract it must
// meet is spelled out below so the implementation has no design left in it.

// Signer turns the canonical bytes of a report into a detached signature.
//
// The contract, in full:
//
//   - Sign receives exactly the bytes Canonical produced. It must not
//     re-serialise, re-order, or otherwise touch the report itself.
//   - KeyID identifies the public key a verifier should use. A fingerprint of
//     the public key is the obvious choice. It must never be derived from, or
//     leak, the private key.
//   - Algorithm names the scheme, e.g. "ed25519". It is recorded in the
//     envelope so a future scheme can be added without the old reports becoming
//     ambiguous.
//   - Nothing in an error returned from Sign may contain key material. Errors
//     from here reach stderr.
type Signer interface {
	KeyID() string
	Algorithm() string
	Sign(canonical []byte) ([]byte, error)
}

// Signature is the detached signature recorded alongside a report.
type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`

	// Value is the raw signature bytes; encoding/json renders []byte as
	// base64, which is what a detached signature wants anyway.
	Value []byte `json:"value"`
}

// Envelope is what gets written to disk: the report, plus the signature over
// its canonical form.
//
// Report is held as json.RawMessage holding exactly the canonical bytes, not as
// a Report struct. That is the whole point — if the file were produced by
// re-serialising the struct, the bytes in the file could differ from the bytes
// that were signed (indentation, field order in a future Go release, anything),
// and the signature would be unverifiable against the very file it sits in.
type Envelope struct {
	Report    json.RawMessage `json:"report"`
	Signature *Signature      `json:"signature,omitempty"`
}

// Write serialises r to w, signing it when signer is non-nil.
//
// An unsigned report is a legitimate output, not a degraded one: signing is
// what makes a report evidence to a third party, and plenty of runs only need
// to tell their operator whether the backup is restorable.
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
			// A signature field that is present and empty is worse than none:
			// it looks signed.
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

// Verifier checks a detached signature. The author implements this alongside
// Signer; it is separate because verification needs only the public key, and a
// verifying tool should never be handed the private one.
type Verifier interface {
	Verify(canonical, signature []byte, keyID, algorithm string) error
}

// ErrUnsigned is returned when a report carries no signature at all.
var ErrUnsigned = fmt.Errorf("report carries no signature")

// VerifyEnvelope checks a written report against its signature.
//
// It verifies against the report bytes exactly as they appear in the file,
// never against a re-serialisation of a decoded report. Re-serialising would
// mean verifying what this code believes the report says rather than what the
// file actually says, which is the difference between a check and a formality.
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
