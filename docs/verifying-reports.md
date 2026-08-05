# Verifying a constat report

A constat report is a statement that a backup was restorable at a moment in
time. A *signed* report is that statement plus evidence of who made it and that
nobody has edited it since.

This document is the specification of what constat signs and how to check it.
It is deliberately complete enough to verify a report **without using constat**
— evidence that can only be checked by the tool that produced it is not
evidence.

## The quick way

```bash
constat verify -key signing.key.pub report.json
```

```
OK       report.json: signature valid (key 5c6632db…)
```

Exit code 0 means valid. Non-zero means one of three distinct things, and the
message says which:

| Output | Meaning |
|---|---|
| `OK` | The report is intact and was signed by this key. |
| `INVALID … has been modified` | The bytes changed after signing, or a different key signed it. |
| `INVALID … signed with key X, but the public key provided is Y` | You have the wrong public key. Common, and not an attack. |
| `UNSIGNED` | The report carries no signature at all. Not evidence — but also not evidence of tampering. |

## The format

A written report is a JSON envelope with exactly two fields:

```json
{
  "report": { "schema_version": 1, "generated_at": "...", ... },
  "signature": {
    "algorithm": "ed25519",
    "key_id": "5c6632db...",
    "value": "UuSi7n1zdf6w...base64..."
  }
}
```

`signature` is absent entirely when the report is unsigned.

**The signed bytes are the exact bytes of the `report` value as they appear in
the file** — not a re-serialisation of it. This is the single most important
property to get right when verifying independently. If you parse the envelope
into your language's object model and then re-encode the `report` field, you
will almost certainly produce different bytes (key order, whitespace, unicode
escaping) and the signature will appear invalid on a perfectly good report.

Extract the raw byte range of the `report` value and verify over that.

- **Algorithm:** Ed25519 (RFC 8032). The message is signed directly; Ed25519
  hashes internally, so there is no separate digest step.
- **`value`:** the 64-byte signature, base64 (standard encoding, as Go's
  `encoding/json` renders `[]byte`).
- **`key_id`:** SHA-256 over the DER PKIX encoding of the public key, hex.
  Recorded so a verifier holding several public keys knows which to use. It is
  derived from public material only.

## Verifying without constat

The public key is a standard PKIX PEM file, so ordinary tooling works. The one
thing to get right is extracting the signed bytes without re-encoding them.

Use a JSON parser that reports **byte offsets** — Python's
`JSONDecoder.raw_decode`, Go's `json.Decoder.InputOffset` — rather than one
that hands you an object you then re-serialise:

```python
import base64, json

raw = open("report.json", "rb").read()

# The exact byte range of the report value, never a re-encoding of it.
key = b'"report":'
start = raw.index(key) + len(key)
_, end = json.JSONDecoder().raw_decode(raw.decode(), start)
signed = raw[start:end]

open("signed-bytes.bin", "wb").write(signed)
open("signature.bin", "wb").write(
    base64.b64decode(json.loads(raw)["signature"]["value"]))
```

Then verify with openssl:

```bash
openssl pkeyutl -verify \
  -pubin -inkey signing.key.pub \
  -rawin -in signed-bytes.bin \
  -sigfile signature.bin
```

```
Signature Verified Successfully
```

Both steps above were run against a real signed report while writing this
document, including confirming that a report with a single flipped verdict
fails with `Signature Verification Failure`. An independent implementation
agreeing is the point — it is what makes this evidence rather than constat
vouching for itself.

**Avoid `jq -c '.report'` for this.** It re-serialises. It happens to reproduce
constat's canonical form today, because that form is already compact with keys
in encounter order — but that is two encoders coincidentally agreeing, not a
guarantee, and a future change to either breaks verification in a way that
looks like tampering.

If you prefer a pure-Python check, `cryptography` verifies the same bytes
directly:

```python
from cryptography.hazmat.primitives.serialization import load_pem_public_key
pub = load_pem_public_key(open("signing.key.pub", "rb").read())
pub.verify(signature, signed)   # raises InvalidSignature if it does not match
```

## What a valid signature does and does not tell you

**It tells you:** these exact report bytes were signed by the holder of that
private key, and have not changed since.

**It does not tell you:**

- That the backup is *currently* restorable. A report is a statement about a
  moment; `generated_at` is that moment, and it is inside the signed bytes for
  exactly this reason.
- That the key belongs to who you think. Ed25519 gives you integrity and
  authenticity relative to a key, not identity. Establishing that a given
  public key belongs to a given organisation is a trust problem this tool does
  not solve and does not pretend to.
- That the machine producing reports was not itself compromised. A signature
  proves the report was not edited *in transit or at rest*. An attacker with
  the private key signs whatever they like.

The last point is the reason the key permission check exists: constat refuses
to load a signing key that is group- or world-readable, because a key the whole
host can read makes every signature it produces meaningless.

## Key handling

```bash
constat keygen -out /etc/constat/signing.key
```

Writes the private key mode 0600 and the public key alongside as
`signing.key.pub`, mode 0644. It refuses to overwrite existing files: losing a
private key makes every report it ever signed permanently unverifiable.

Then in `constat.yaml`:

```yaml
signing:
  key_file: /etc/constat/signing.key
```

- **Back up the private key** — somewhere other than the backups constat is
  verifying, for reasons that should be obvious.
- **Distribute the public key** to whoever needs to check reports. That is what
  it is for.
- If a configured key is missing or has permissions constat will not accept,
  the run **fails** rather than falling back to an unsigned report. An operator
  who asked for evidence and silently received none would not find out until
  someone tried to verify it.

## Rotation

Not implemented. `algorithm` and `key_id` are recorded per signature precisely
so that a second key or scheme can be introduced without making existing
reports ambiguous, but there is no rotation tooling and no key history.

Today, rotating means generating a new key and keeping the old public key
around to verify old reports. That is a real gap for anyone retaining reports
as long-term evidence, and it is honest to call it one.
