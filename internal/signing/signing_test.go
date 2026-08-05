package signing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxmchrbrt/constat/internal/report"
)

// newKeyFiles generates a keypair and writes both halves into a temp
// directory, returning their paths.
func newKeyFiles(t *testing.T) (privPath, pubPath string) {
	t.Helper()

	dir := t.TempDir()
	privPath = filepath.Join(dir, "signing.key")
	pubPath = filepath.Join(dir, "signing.pub")

	priv, pub, err := Generate()
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	if err := WritePrivateKey(privPath, priv); err != nil {
		t.Fatalf("writing private key: %v", err)
	}
	if err := WritePublicKey(pubPath, pub); err != nil {
		t.Fatalf("writing public key: %v", err)
	}
	return privPath, pubPath
}

func fixedReport() report.Report {
	return report.New(
		time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC),
		"backup-host-01", "v0.1.0",
		[]report.Target{{
			Name: "app-db", Verdict: report.Pass, Kind: "restic", Repository: "/srv/backups/app",
			Assertions: []report.Assertion{{Name: "query_min", Verdict: report.Pass, Message: "query returned 500 (min 1)"}},
		}},
	)
}

// The whole point, end to end: a report signed by this key verifies against
// its own public half.
func TestSignAndVerify_RoundTrip(t *testing.T) {
	privPath, pubPath := newKeyFiles(t)

	signer, err := LoadSigner(privPath)
	if err != nil {
		t.Fatalf("loading signer: %v", err)
	}
	verifier, err := LoadVerifier(pubPath)
	if err != nil {
		t.Fatalf("loading verifier: %v", err)
	}

	var buf bytes.Buffer
	if err := report.Write(&buf, fixedReport(), signer); err != nil {
		t.Fatalf("writing signed report: %v", err)
	}

	if err := report.VerifyEnvelope(buf.Bytes(), verifier); err != nil {
		t.Fatalf("a freshly signed report failed to verify: %v", err)
	}
}

// The property that makes a signature worth anything: change one byte of the
// report and verification must fail. Without this test the whole scheme could
// be a no-op and every other test would still pass.
func TestVerify_DetectsTampering(t *testing.T) {
	privPath, pubPath := newKeyFiles(t)

	signer, _ := LoadSigner(privPath)
	verifier, _ := LoadVerifier(pubPath)

	var buf bytes.Buffer
	if err := report.Write(&buf, fixedReport(), signer); err != nil {
		t.Fatalf("writing signed report: %v", err)
	}

	// The edit an attacker would actually make: flip a failing verdict to a
	// passing one. Here the report already passes, so flip it the other way —
	// the direction does not matter, only that the bytes changed.
	tampered := bytes.Replace(buf.Bytes(), []byte(`"verdict":"pass"`), []byte(`"verdict":"fail"`), 1)
	if bytes.Equal(tampered, buf.Bytes()) {
		t.Fatal("fixture did not contain the string it meant to tamper with")
	}

	err := report.VerifyEnvelope(tampered, verifier)
	if err == nil {
		t.Fatal("a modified report verified successfully")
	}
	if !strings.Contains(err.Error(), "modified") {
		t.Errorf("error should say the report was modified, got: %v", err)
	}
}

// Tampering with the signature rather than the report must also fail.
func TestVerify_DetectsAForgedSignature(t *testing.T) {
	privPath, pubPath := newKeyFiles(t)

	signer, _ := LoadSigner(privPath)
	verifier, _ := LoadVerifier(pubPath)

	var buf bytes.Buffer
	report.Write(&buf, fixedReport(), signer)

	var env report.Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("parsing envelope: %v", err)
	}
	env.Signature.Value[0] ^= 0xff

	if err := verifier.Verify(env.Report, env.Signature.Value, env.Signature.KeyID, env.Signature.Algorithm); err == nil {
		t.Fatal("a corrupted signature verified successfully")
	}
}

// Verifying against the wrong key must say so specifically. "Signature does
// not match" reads as tampering; the operator grabbing the wrong public key is
// a completely different problem and far more common.
func TestVerify_WrongKeyIsDistinguishedFromTampering(t *testing.T) {
	privPath, _ := newKeyFiles(t)
	_, otherPubPath := newKeyFiles(t)

	signer, _ := LoadSigner(privPath)
	otherVerifier, _ := LoadVerifier(otherPubPath)

	var buf bytes.Buffer
	report.Write(&buf, fixedReport(), signer)

	err := report.VerifyEnvelope(buf.Bytes(), otherVerifier)
	if err == nil {
		t.Fatal("a report verified against an unrelated key")
	}
	if !strings.Contains(err.Error(), "signed with key") {
		t.Errorf("error should identify a key mismatch, got: %v", err)
	}
	if strings.Contains(err.Error(), "modified") {
		t.Errorf("a wrong key must not be reported as tampering: %v", err)
	}
}

func TestVerify_UnknownAlgorithmIsRejected(t *testing.T) {
	_, pubPath := newKeyFiles(t)
	verifier, _ := LoadVerifier(pubPath)

	err := verifier.Verify([]byte("payload"), make([]byte, ed25519.SignatureSize), verifier.KeyID(), "rsa-pkcs1")
	if err == nil {
		t.Fatal("an unknown algorithm was accepted")
	}
	if !strings.Contains(err.Error(), "rsa-pkcs1") {
		t.Errorf("error should name the algorithm it refused: %v", err)
	}
}

// A signing key the rest of the host can read is not a signing key: anything
// it signs proves nothing about who signed it. Refused outright rather than
// warned about, because a warning at 3am into a log nobody reads is not a
// control.
func TestLoadSigner_RefusesAGroupOrWorldReadableKey(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644, 0o604, 0o660, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			privPath, _ := newKeyFiles(t)
			if err := os.Chmod(privPath, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			_, err := LoadSigner(privPath)
			if err == nil {
				t.Fatalf("a key with mode %04o was accepted", mode)
			}
			if !strings.Contains(err.Error(), "chmod 600") {
				t.Errorf("error should tell the operator how to fix it, got: %v", err)
			}
		})
	}
}

func TestLoadSigner_AcceptsOwnerOnlyModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		t.Run(mode.String(), func(t *testing.T) {
			privPath, _ := newKeyFiles(t)
			if err := os.Chmod(privPath, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			if _, err := LoadSigner(privPath); err != nil {
				t.Errorf("mode %04o should be accepted: %v", mode, err)
			}
		})
	}
}

// Losing a private key makes every report ever signed with it unverifiable, so
// writing one must never clobber silently.
func TestWritePrivateKey_RefusesToOverwrite(t *testing.T) {
	privPath, _ := newKeyFiles(t)

	priv, _, err := Generate()
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	if err := WritePrivateKey(privPath, priv); err == nil {
		t.Fatal("writing over an existing signing key was allowed")
	}
}

func TestWritePrivateKey_IsOwnerOnly(t *testing.T) {
	privPath, pubPath := newKeyFiles(t)

	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("private key mode = %04o, want 0600", got)
	}

	pubInfo, err := os.Stat(pubPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := pubInfo.Mode().Perm(); got&0o044 == 0 {
		t.Errorf("public key mode = %04o, want it readable — it is meant to be distributed", got)
	}
}

// The key ID goes into every report constat writes, so it must be derived from
// public material only.
func TestKeyID_DerivesFromThePublicKeyOnly(t *testing.T) {
	priv, pub, err := Generate()
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	fromPub, err := KeyID(pub)
	if err != nil {
		t.Fatalf("KeyID: %v", err)
	}
	fromPriv, err := KeyID(priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatalf("KeyID: %v", err)
	}
	if fromPub != fromPriv {
		t.Errorf("key ID differs depending on how the public key was obtained: %s vs %s", fromPub, fromPriv)
	}

	// The private key's raw bytes must not appear anywhere in the identifier
	// that gets published in every report.
	if strings.Contains(fromPub, string(priv.Seed())) {
		t.Error("the key ID contains private key material")
	}
}

func TestKeyID_DiffersBetweenKeys(t *testing.T) {
	_, a, _ := Generate()
	_, b, _ := Generate()

	idA, _ := KeyID(a)
	idB, _ := KeyID(b)

	if idA == idB {
		t.Error("two different keys produced the same key ID")
	}
}

// An error from the signing path reaches stderr. It must never carry key
// material with it.
func TestLoadSigner_ErrorsCarryNoKeyMaterial(t *testing.T) {
	dir := t.TempDir()

	// A PEM block of the right type whose contents are not a valid key: the
	// path most likely to make a parser quote what it choked on.
	bad := filepath.Join(dir, "bad.key")
	body := "-----BEGIN PRIVATE KEY-----\nU0VOVElORUwtS0VZLU1BVEVSSUFMLTk5OTk5\n-----END PRIVATE KEY-----\n"
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	_, err := LoadSigner(bad)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), "U0VOVElORUw") {
		t.Errorf("error quoted the key material it failed to parse: %v", err)
	}
}

func TestLoadSigner_RejectsNonPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notpem.key")
	os.WriteFile(path, []byte("this is not a key"), 0o600)

	if _, err := LoadSigner(path); err == nil {
		t.Fatal("a non-PEM file was accepted as a signing key")
	}
}

func TestLoadVerifier_RejectsAPrivateKeyFile(t *testing.T) {
	privPath, _ := newKeyFiles(t)

	if _, err := LoadVerifier(privPath); err == nil {
		t.Fatal("a private key file was accepted where a public key was expected")
	}
}

func TestLoadSigner_MissingFile(t *testing.T) {
	if _, err := LoadSigner(filepath.Join(t.TempDir(), "absent.key")); err == nil {
		t.Fatal("expected an error for a missing key file")
	}
}
