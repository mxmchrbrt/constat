# The fixture corpus

Backups that are broken in named, deliberate ways, one per failure mode from
`CLAUDE.md`. Unit tests say the code does what the code does; these say the tool
reaches the right conclusion about a real, specific failure. That is what makes
the assertions provably correct rather than plausible, and nobody else has this
corpus.

## Layout

Each fixture is a directory with two files:

| File | What it is |
|---|---|
| `build.sh` | Builds the broken repository from scratch. Takes one argument: the output directory. |
| `expect.yaml` | The failure mode it represents, the assertions to run against it, and the verdict each one must return. |

`expect.yaml` is both the documentation and the test input — deliberately one
file rather than a README beside it, because a prose copy of the expected
verdicts is a copy that goes stale without anything noticing.

`lib.sh` holds the shared helpers and is sourced, not executed.

## Running one by hand

```sh
testdata/fixtures/stale-snapshot/build.sh /tmp/look-at-this
restic -r /tmp/look-at-this/repo --password-file /tmp/look-at-this/pass snapshots
```

The whole corpus runs as part of the test suite:

```sh
go test ./internal/run/ -run FixtureCorpus -v
```

It skips cleanly when restic is not installed, and under `-short`.

## Built, not committed

No repository binaries live in git. Every fixture is rebuilt from its script, so
a fixture can be inspected, modified, and rebuilt without a binary diff that
nobody can review.

Deterministic here means same content, same structure, same timestamps on every
build. It does not mean byte-identical repositories: restic generates a fresh
key per repository, so pack names and IDs differ every time. What is stable is
the verdict each fixture produces, which is what the tests assert.

Fixtures that must stay stale use a fixed absolute date (`STALE_DATE`), so they
only get more stale and never change meaning. Fixtures that must look current
use a relative mtime, because "recent" is only true relative to now.

## Current fixtures

| Fixture | Failure mode | What it proves |
|---|---|---|
| `healthy` | — | The control. Assertions stay quiet when nothing is wrong. |
| `stale-snapshot` | #1 silent non-execution | Both age checks fail; content checks pass, because nothing is missing. |
| `missing-directory` | #2 coverage drift | Every age check passes. Only an assertion naming the missing directory finds it. |
| `truncated-repo` | #4 corruption at rest | The snapshot still lists fine; the restore is what fails. |
| `permissions-stripped` | #7 restores but won't boot | Nothing catches it yet. See below. |

## `permissions-stripped` expects a clean run, on purpose

constat has no assertion that reads a file mode, so an entrypoint script that
came back without its executable bit is currently invisible to it. That fixture
therefore expects every assertion to pass.

That is the finding, recorded rather than implied. The day a `file_mode`
assertion lands, this fixture's `expect.yaml` changes and the fixture becomes
the test that proves the new assertion works.

## What `truncated-repo` found

It was written expecting an error and got a PASS. The cause was not the fixture:
restic caches tree and index packs, and constat was shelling out to plain
`restic restore`, so a warm cache served the trees and the truncated pack was
never read. On the customer's own machine — which is where this tool is
documented to run, and usually the machine that took the backup — the cache is
warm exactly where it does the most damage.

The driver now passes `--no-cache`. The fixture that found it is the reason the
corpus exists.
