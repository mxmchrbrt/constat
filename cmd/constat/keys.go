package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mxmchrbrt/constat/internal/report"
	"github.com/mxmchrbrt/constat/internal/signing"
)

// keygen writes a new ed25519 keypair. A deliberate subcommand rather than
// something a run does on demand — a job that silently generates a key would
// produce reports signed by a key nobody has seen.
func keygen(args []string) int {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "path for the private key; the public key is written alongside with a .pub suffix")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: constat keygen -out <path>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *out == "" {
		fs.Usage()
		return 1
	}

	privPath := *out
	pubPath := privPath + ".pub"

	priv, pub, err := signing.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	// Written before the private key so that a failure here — a bad path, a
	// full disk — does not leave a private key on disk with no public half to
	// verify against.
	if err := signing.WritePublicKey(pubPath, pub); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if err := signing.WritePrivateKey(privPath, priv); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		// Roll back the public half rather than leaving an orphan that looks
		// like a usable key.
		os.Remove(pubPath)
		return 1
	}

	keyID, err := signing.KeyID(pub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	fmt.Printf("private key: %s (mode 0600 — back this up; losing it makes every report it signed unverifiable)\n", privPath)
	fmt.Printf("public key:  %s (distribute this to whoever needs to verify reports)\n", pubPath)
	fmt.Printf("key id:      %s\n", keyID)
	fmt.Printf("\nAdd to your config:\n\nsigning:\n  key_file: %s\n", filepath.Clean(privPath))

	return 0
}

// verify checks a written report against a public key. Its whole input is a
// report file and a public key — no config, no repository, nothing secret.
func verify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	keyPath := fs.String("key", "", "path to the public key to verify against")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: constat verify -key <public-key> <report.json>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *keyPath == "" || fs.NArg() < 1 {
		fs.Usage()
		return 1
	}

	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading report: %v\n", err)
		return 1
	}

	verifier, err := signing.LoadVerifier(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	if err := report.VerifyEnvelope(raw, verifier); err != nil {
		if errors.Is(err, report.ErrUnsigned) {
			// Distinguished from a bad signature on purpose: an unsigned
			// report is not evidence, but it is also not evidence of
			// tampering, and conflating the two would raise an alarm about
			// the wrong thing.
			fmt.Fprintf(os.Stderr, "UNSIGNED %s: this report carries no signature\n", fs.Arg(0))
			return 1
		}
		fmt.Fprintf(os.Stderr, "INVALID  %s: %v\n", fs.Arg(0), err)
		return 1
	}

	fmt.Printf("OK       %s: signature valid (key %s)\n", fs.Arg(0), verifier.KeyID())
	return 0
}
