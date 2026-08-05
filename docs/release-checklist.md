# Release checklist — v0.1

Working notes for the first public release. Everything here was prepared by
session 12's audit; the items marked **author** are ones nobody else can or
should do.

## Blockers — do not tag until these are resolved

### 1. Report signing is not implemented (**author**)

The product's one-line description is a *signed*, dated statement of fact.
Today constat produces an unsigned one. The format, the canonical
serialisation, the envelope, `Write`, `VerifyEnvelope` and the `Signer` /
`Verifier` interfaces are all built and tested against stubs — what is missing
is any implementation of `Signer`.

`CLAUDE.md` keeps this with the author regardless of the v0 working-agreement
pivot: key generation, loading, storage, and the ed25519 call itself. The
remaining work is four bounded steps, spelled out in `ARCHITECTURE.md` under
"Where the signing boundary sits".

Two honest options, and this is a product call, not a technical one:

- **Implement it before tagging.** The description stays true and v0.1 is the
  thing the brief describes.
- **Ship v0.1 unsigned and say so.** README now states plainly that reports
  are unsigned. This is defensible — the operational check is genuinely useful
  without signatures — but the "evidence" positioning has to wait for v0.2, and
  the announcement copy must not claim it.

The README currently reflects the second option. If the first is chosen, that
section needs updating back.

### 2. Name reservation (**author**)

Per the brief §12, before announcing anything:

- GitHub org / repo name
- Domains
- EUIPO and INPI, classes 9 and 42
- `go get` collision

**Checked in this session:** `github.com/mxmchrbrt/constat` is unclaimed on the
Go module proxy (404 — the repository is not public yet). No module currently
occupies that path, so there is no import-path collision to design around.

**Not checked, and deliberately left alone:** trademark classes and domains.
Those are registrations with legal and financial consequences, and judgment
calls about scope that belong to the author.

Note that the README's `go install github.com/mxmchrbrt/constat/cmd/constat@latest`
only works once the repository is public *and* a version tag exists. It is
correct for the post-release state and wrong until then.

## Verified in this session

- `go build ./...`, `go vet ./...` — clean.
- `staticcheck ./...` — clean. Two SA5001 findings were reviewed and annotated
  rather than suppressed blindly: both are deliberate `defer Close()` calls
  armed before their error check, which is what guarantees a half-started
  container is still torn down.
- `go test -race ./...` — see "Known flake" below.
- Secret-leak audit across every error path (below).
- The Docker image builds and runs, with restic present and the version ldflag
  threaded through (session 11).
- Every command in the README's worked example, run verbatim (session 11).
- `constat.yaml.example` parses and validates against the real binary
  (session 11).

## Secret-leak audit

Every error path in non-test code was enumerated and read. Findings:

**Repository passphrase** — never read by constat at all. It is passed to
restic as a `--password-file` *path*; the contents are never loaded into
constat's memory, so there is nothing to leak. Pinned by
`TestReport_NeverContainsTheRepositoryPassphrase`, which runs a genuinely
failing restic invocation and greps the rendered report.

**Generated container credential** — random per run, passed via `--env-file`
so it never appears in `ps`, and redacted from errors this package wraps by
`redactDSN`.

**A gap found and closed:** errors raised from *inside* `internal/assert` — a
query failing because the container died mid-run — have no password in scope to
redact against, and rely on pgx redacting its own connection string. It does:
verified empirically, pgx reports ``failed to connect to `user=constat
database=constat` `` with no password. That is a dependency guarantee constat
silently depends on, so it is now pinned by
`TestPgxConnectionErrorsDoNotCarryThePassword` — a pgx upgrade that changed it
would otherwise leak into a durable report unnoticed.

**Webhook URL** — can itself be a secret (a Discord token, a private ntfy
topic). Redacted to scheme+host, by unwrapping `*url.Error` rather than string
replacement (session 10).

**Unbounded subprocess output — found and fixed in this session.** See below.

## Fixed in this session

**Restic's output was carried into errors unbounded.** Measured against a real
truncated repository of 400 files: `restic restore` emitted ~276 KB, and that
went verbatim into `report.json` and the webhook body. A real repository has
orders of magnitude more files.

The failure compounded in the worst direction — the more broken the backup, the
larger the payload, until the alert reporting the breakage would itself be
rejected for size. Now bounded to 2000 characters with a visible truncation
marker, matching what `internal/container` already did. Verified end to end: the
same corrupt repository now produces a 2.7 KB report instead of ~276 KB, with
the verdict unchanged.

## Known flake

`internal/container` intermittently fails to start a container with
`pasta failed ... Failed to bind port N`, under rootless podman. Seen once
across the session's runs.

constat's own behaviour is correct — it reports ERROR (the tool could not run),
not FAIL (the backup is broken), which is the right side of that distinction.
The flake is in the test's assumption that the container always starts.

Not fixed here, deliberately: adding retry logic to container startup at
release time is a change to the lifecycle code that session 7 mutation-tested,
and the failure mode is both rare and correctly classified. Worth revisiting if
it recurs, or if an operator reports spurious ERROR verdicts.

## Still open from earlier sessions

- **`ci.yml` runs the full suite on every push**, including a ~300 MB image
  pull. Whether it should be `-short` plus a separate slow job was flagged in
  session 7 and is still undecided. `release.yml` already uses `-short`.
- **`.goreleaser.yaml` has never been dry-run** — the goreleaser module fetch
  timed out twice in session 11. Run `goreleaser release --snapshot --clean`
  once before the first real tag.

## Sequence, once the blockers are cleared

1. Decide the signing question above.
2. Reserve the name.
3. `goreleaser release --snapshot --clean` — confirm the artifacts look right.
4. Tag `v0.1.0` and push it; the release workflow drafts a GitHub release.
5. Review the draft, then publish.
6. Announce: Show HN, r/selfhosted, Lobsters, awesome-selfhosted PR, and
   publish `docs/failure-modes.md` alongside — per the brief, it is the better
   of the two marketing assets.
