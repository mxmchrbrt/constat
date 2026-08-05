// Package signing implements report.Signer and report.Verifier over ed25519.
//
// This is the half of the report that turns an operational check into
// evidence. A report says a backup restored; a signature says *this* machine,
// holding *this* key, asserted it, and that the bytes have not been edited
// since. Without it a report is a text file anyone can retype.
//
// Kept in its own package rather than inside internal/report so the boundary
// stays visible: report defines what a signature is and how the envelope holds
// it, and knows nothing about keys. Only this package ever touches key
// material.
//
// Deliberately stdlib-only — crypto/ed25519, crypto/x509, encoding/pem. A
// dependency in the signing path is a dependency that can change what a
// signature means.
package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mxmchrbrt/constat/internal/report"
)

// Algorithm is the only scheme this package implements. It is recorded in
// every signature so that adding a second one later cannot make old reports
// ambiguous about which was used.
const Algorithm = "ed25519"

const (
	privatePEMType = "PRIVATE KEY" // PKCS#8, per RFC 7468
	publicPEMType  = "PUBLIC KEY"  // PKIX, per RFC 7468
)

// Signer holds an ed25519 private key and implements report.Signer.
type Signer struct {
	key   ed25519.PrivateKey
	keyID string
}

// Verifier holds an ed25519 public key and implements report.Verifier.
//
// Separate type from Signer, and constructed from a separate file, so that a
// verifying tool is never handed a private key. Verification is the operation
// a third party performs — an auditor, a customer, the person who needs to
// believe the report — and none of them should need anything secret to do it.
type Verifier struct {
	key   ed25519.PublicKey
	keyID string
}

// Generate creates a new keypair. The private key is returned alongside its
// public half so the caller can write both without ever re-deriving one from
// the other on disk.
func Generate() (priv ed25519.PrivateKey, pub ed25519.PublicKey, err error) {
	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating ed25519 key: %w", err)
	}
	return priv, pub, nil
}

// KeyID is the fingerprint recorded in a signature so a verifier can tell
// which public key to check against.
//
// SHA-256 over the PKIX encoding of the *public* key. Derived only from public
// material, deliberately: a key identifier that leaked anything about the
// private half would be published in every report constat ever wrote.
func KeyID(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("encoding public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// WritePrivateKey writes priv to path as PKCS#8 PEM, mode 0600.
//
// Created with O_EXCL: writing a signing key must never silently overwrite an
// existing one. Losing a private key means every report signed with it becomes
// unverifiable, and a keygen command that clobbers on a re-run is one
// mistyped path away from doing exactly that.
func WritePrivateKey(path string, priv ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("encoding private key: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating private key file: %w", err)
	}
	defer f.Close()

	if err := pem.Encode(f, &pem.Block{Type: privatePEMType, Bytes: der}); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}
	return f.Close()
}

// WritePublicKey writes pub to path as PKIX PEM, mode 0644. The public key is
// meant to be distributed — that is the whole point of it.
func WritePublicKey(path string, pub ed25519.PublicKey) error {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("encoding public key: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("creating public key file: %w", err)
	}
	defer f.Close()

	if err := pem.Encode(f, &pem.Block{Type: publicPEMType, Bytes: der}); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}
	return f.Close()
}

// LoadSigner reads a private key from path.
//
// The file's permissions are checked and a key readable by anyone other than
// its owner is refused outright rather than warned about. A signing key that
// the whole host can read is not a signing key — anything it signs proves
// nothing about who signed it — and a warning printed at 3am into a log nobody
// reads is not a control.
func LoadSigner(path string) (*Signer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}
	if err := checkKeyPermissions(path, info.Mode()); err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("signing key %s is not PEM-encoded", path)
	}
	if block.Type != privatePEMType {
		return nil, fmt.Errorf("signing key %s contains a %q block, want %q", path, block.Type, privatePEMType)
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Deliberately not wrapping the parse error's detail: x509 parse
		// failures can quote fragments of the material they choked on, and
		// this material is a private key.
		return nil, fmt.Errorf("signing key %s could not be parsed as a PKCS#8 private key", path)
	}

	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key %s is a %T, want an ed25519 key", path, parsed)
	}

	keyID, err := KeyID(priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}

	return &Signer{key: priv, keyID: keyID}, nil
}

// checkKeyPermissions refuses a private key that is group- or world-readable.
func checkKeyPermissions(path string, mode fs.FileMode) error {
	if mode&0o077 != 0 {
		return fmt.Errorf(
			"signing key %s has mode %04o: it is readable by users other than its owner, so a signature made with it proves nothing; run chmod 600 %s",
			path, mode.Perm(), filepath.Clean(path))
	}
	return nil
}

// LoadVerifier reads a public key from path. No permission check: a public key
// is meant to be readable, and refusing to verify because the public half was
// world-readable would be theatre.
func LoadVerifier(path string) (*Verifier, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading public key: %w", err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("public key %s is not PEM-encoded", path)
	}
	if block.Type != publicPEMType {
		return nil, fmt.Errorf("public key %s contains a %q block, want %q", path, block.Type, publicPEMType)
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public key %s could not be parsed: %w", path, err)
	}

	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key %s is a %T, want an ed25519 key", path, parsed)
	}

	keyID, err := KeyID(pub)
	if err != nil {
		return nil, err
	}

	return &Verifier{key: pub, keyID: keyID}, nil
}

// KeyID implements report.Signer.
func (s *Signer) KeyID() string { return s.keyID }

// Algorithm implements report.Signer.
func (s *Signer) Algorithm() string { return Algorithm }

// Sign implements report.Signer over exactly the bytes it is given.
//
// ed25519.Sign panics on a malformed key rather than returning an error, so
// the key is validated at load time; by the time it reaches here it is known
// good. The length check is belt and braces against a Signer constructed
// outside LoadSigner.
func (s *Signer) Sign(canonical []byte) ([]byte, error) {
	if len(s.key) != ed25519.PrivateKeySize {
		return nil, errors.New("signing key is not initialised")
	}
	return ed25519.Sign(s.key, canonical), nil
}

// KeyID reports the fingerprint of the public key this verifier holds.
func (v *Verifier) KeyID() string { return v.keyID }

// Verify implements report.Verifier.
//
// Checks the algorithm and the key ID before the signature itself. Verifying a
// report against the wrong key would otherwise fail with a bare "signature
// does not match", which reads as tampering when it usually means the operator
// grabbed the wrong public key — two very different things to tell someone.
func (v *Verifier) Verify(canonical, signature []byte, keyID, algorithm string) error {
	if algorithm != Algorithm {
		return fmt.Errorf("report is signed with %q, and this build only verifies %q", algorithm, Algorithm)
	}
	if keyID != v.keyID {
		return fmt.Errorf("report was signed with key %s, but the public key provided is %s", keyID, v.keyID)
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(signature), ed25519.SignatureSize)
	}
	if !ed25519.Verify(v.key, canonical, signature) {
		return errors.New("signature does not match the report: the report has been modified since it was signed, or it was not signed by this key")
	}
	return nil
}

// Compile-time proof that these satisfy the contracts report declares. If
// either interface changes, this fails at build time rather than at the moment
// someone tries to sign a report.
var (
	_ report.Signer   = (*Signer)(nil)
	_ report.Verifier = (*Verifier)(nil)
)
