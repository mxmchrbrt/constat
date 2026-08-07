## Decided

### How an assertion declares what the runner must set up — `Requires()`
Every `Assertion` implements `Requires() Requirements`, currently
`Requirements{Restore bool}`.

Three shapes were on the table. An optional marker interface (`io.WriterTo`
style, core type-asserts) is the idiomatic Go answer and was rejected on
failure shape: an author who forgets the marker gets an empty `RestoreDir` and
a confusing FAIL rather than a compile error, which is exactly wrong for a tool
whose promise is certainty. A `NeedsRestore() bool` was rejected because
session 7 adds a second requirement — a live database container — which would
force widening the interface a second time, breaking every assertion again.

The struct absorbs that: `Requirements` gains a field, `Assertion` does not
change. This is CLAUDE.md's "research broad, ship narrow" applied to the
smallest possible surface — the model is wide, today's implementation is one
bool.

**Revisit when:** a requirement appears that isn't a boolean — something
parameterised, like "a container running *this* image". At that point
`Requirements` stops being a set of flags and the question reopens.

**Update, session 7 — the second field arrived and the design held.**
`Requirements{Restore, Database bool}`. No existing assertion changed, which is
the whole thing this shape was chosen for.

The parameterised case this section anticipated did *not* materialise, and it is
worth recording why rather than counting it as luck. The image is named on the
target (`verify_with.image`), not on the assertion, so what the assertion
declares stays a boolean: "I need the database this target defines". Had the
image been per-assertion, `Requirements` would have needed a value here and the
question would have reopened exactly as predicted. Keeping the parameter on the
target is what avoided it, and that is the thing to preserve.

### What an assertion gets for a database — `Env.DB *sql.DB`
Three shapes were on the table, and this is a fork in the same family as the
`Env`/driver one from session 3.

A `*sql.DB` won. Session 8's assertions want a typed, checked scan and a
statement timeout; both come free, and neither can be had from parsing text.
The container becomes an implementation detail assertions never see.

The alternative worth recording is exec-into-the-container: assertions run psql
inside and parse its output. It is genuinely tempting — zero dependencies, no
published port, the container stays fully sealed — and it was rejected because
every query assertion would then parse text. Text parsing is the failure mode
this project can least afford: it fails by returning a plausible wrong answer,
which is precisely the shape of bug a verification tool must not have.

The costs, stated plainly: a dependency on `github.com/jackc/pgx/v5`, and a port
published on loopback because the connection comes from the host rather than
from inside the container.

**Revisit when:** a second database engine arrives. MySQL means a second driver
and `Env.DB` stops being obviously the right name for one connection to one
engine — that is the moment to ask whether `Env` should carry a small interface
instead of a `*sql.DB`.

### Per-target timeout default — 1 hour
Bounds restore plus every assertion. Chosen over unbounded (a hung restic would
hang the run forever, which is the failure mode #10 the timeout exists to
catch) and over 6 hours (large restores would work unconfigured, weakening the
RTO signal). A 500 GB target must set `timeout:` explicitly, which is the
point: it forces the operator to state their RTO rather than discover it.

### Which timestamp `newest_file_age_max` trusts — mtime, and only mtime
ctime was never really a candidate once measured. The kernel sets ctime on any
inode change and no userspace API can write it, so a restore stamps every file
with the restore's own ctime. Confirmed against restic 0.18.0: a file dated
2025-01-15 came back dated 2025-01-15 (mtime exact), while its ctime was the
moment of the restore. atime is preserved too but means nothing here — reading
a file moves it.

That leaves mtime carrying the whole assertion, which is uncomfortable, because
the failure is asymmetric. If a backend does *not* preserve mtime, every
restored file looks brand new, the newest-file age is ~0, and the check passes
forever — silently, on exactly the failure mode (#1) it exists to catch. A
verification tool that fails towards PASS is worse than no check.

So two guards, both errors rather than verdicts:

- an mtime ahead of wall-clock now (clock skew), mirroring the call made for
  `newest_snapshot_age_max` in session 1;
- an mtime newer than the snapshot it was restored from, beyond a 5-minute
  tolerance for files still being written while the backup ran.

The second guard costs this file assertion a `Driver.Latest` call, which is why
it was the author's decision rather than an implementation detail: a file
assertion now reaches for repository metadata. Accepted because the call is
cheap by construction (that is what the `Latest`/`Restore` split is for) and
because without it the headline assertion can be vacuously green.

**Revisit when:** a backend appears whose restore genuinely cannot preserve
mtime — a `pg_dump` file restored into a container has no per-file mtime worth
reading, and the object-storage tarball case may be similar. At that point the
answer is probably a driver-declared capability rather than a per-assertion
guard, and this section reopens together with the two-implementation extraction
below.

### Where assertion paths are rooted — `restore.strip_prefix`
Backups store absolute paths and a restore reproduces them, so a snapshot of
`/home/app/data` lands at `<restoredir>/home/app/data`. Without something, every
assertion has to spell that prefix out: `path_exists:
home/app/data/config.php`. Unguessable from the config, and it breaks whenever
the source machine's layout changes.

Set the prefix once per target; assertions are written relative to it.
Three routes were on the table:

- restic's `restore latest:<path>` flattens that subtree to the restore root.
  Verified working on 0.18.0 and the least code, rejected because it pins core
  to restic, which CLAUDE.md forbids.
- Infer the common prefix from the restored tree. Best ergonomics, rejected
  because a snapshot can cover two unrelated paths and a wrong guess moves the
  root of every assertion silently — the worst available failure shape.
- An explicit config field. Chosen: one more thing to get right, but wrong in a
  way that is loud.

Resolved in the runner rather than the driver, so the restore still produces the
full tree and only `Env.RestoreDir` moves. `driver.Driver` stays untouched,
which matters given it has already changed twice.

**A prefix not present in the restored tree is an error, never a verdict.** This
is the whole point. A wrong prefix roots every assertion at an empty directory,
and the target then reports a list of entirely convincing failures about a
backup that is fine. That happened in a live regression run during session 5 —
the lab's snapshot recorded a path from before the directory was moved — which
is what turned this from a papercut into a decision. The error names `restic
snapshots --json` as where the right value comes from.

Resolution goes through `safepath`, so a symlink in the restored tree cannot
point the assertion root outside the restore directory. That shared use is why
`safepath` became its own package rather than staying unexported inside
`assert`.

**Revisit when:** a driver appears whose restore has no meaningful path prefix
at all — a `pg_dump` loaded into a container is the obvious one. The field
should then be documented as file-path-driver-only rather than quietly ignored.

### Every restic invocation passes `--no-cache`
restic caches tree and index packs in `~/.cache/restic`. With a warm cache a
repository whose tree pack has been truncated restores cleanly and reports
success: the trees never come off disk and the damaged pack is never read. Data
packs are not cached, so corruption there is caught either way — but half of
failure mode #4 being invisible is not a guarantee anyone can rely on.

This is not hypothetical and was not reasoned out in advance. The
`truncated-repo` fixture was written expecting an ERROR, and the tool said PASS.

The deployment makes it worse rather than better: constat runs on the customer's
own machine, which is usually the machine that took the backup, so the cache is
warm exactly where it does the most damage.

The cost is re-reading the index on every invocation, which is real for a large
remote repository and pushes against failure mode #10 (RTO). Accepted: a
verification that might be reading a cache is not a verification. A scratch
`--cache-dir` per run was the alternative — cold every run, but with reuse
within a run — and was not taken because it adds a lifecycle to clean up for a
benefit that is currently one extra index read.

The general point is larger than the flag. Shelling out to the customer's own
tool is not neutral: restic's defaults are tuned for backing up quickly, and
verification wants the opposite of several of them. `--no-cache` is unlikely to
be the last one.

**Revisit when:** a remote-repository user reports the index read dominating
their run, or a second flag joins it — at which point this stops being one
constant and becomes a deliberate "verification profile" for the driver.

### `repository_check` exists because a restore drill is not a repository check
A restore reads only the packs the restored snapshot references. That is the
whole coverage of a drill, and it leaves a real gap: a pack reachable only from
an older snapshot can be truncated or rotted and every run stays green, right
up until the day someone needs that older snapshot.

`restic check` walks the whole repository, so it is the assertion that covers
what the restore structurally cannot. The default is structure-only —
deliberately, because the argument for it is that it is cheap enough to leave
on. It verifies index consistency, that every referenced blob is accounted for,
and that packs are present at the size they claim, which is the realistic
failure set for a cloud backend (truncated upload, missing object, broken
chain) without downloading pack contents.

`read_data_subset` is opt-in for the opposite reason. It is what actually reads
packs back and compares them against recorded hashes, so it is the only thing
that catches a pack that is present, correctly sized and wrong inside — and it
costs bandwidth proportional to the fraction read. Defaulting it on would make
the assertion expensive enough that people stop running it, which trades a
partial check for no check.

The value is validated at config load, not left to restic. A typo in
`read_data_subset` makes restic exit non-zero, and this assertion reads a
non-zero exit as a damaged repository — so an unvalidated typo would page
someone about corruption that does not exist. That is the one false alarm a
verification tool cannot afford.

`RepositoryChecker` is an optional interface rather than a method on `Driver`:
a driver that cannot verify a whole repository should fail to build the
assertion with a clear message, not carry a method that returns "unsupported"
at run time.

**Revisit when:** a second driver implements it, or an operator wants the
subset to rotate automatically rather than being a fixed fraction per run.
Rotation belongs to the scheduler, which constat deliberately does not own.

### Which database failures are verdicts, and which are broken runs
The load step is the first place where a failure could honestly be reported
either way, so the boundary was drawn explicitly:

| Situation | Reported as | Why |
|---|---|---|
| The dump will not load | FAIL | Taxonomy #9 and #4 surface exactly here. This is the failure being hunted. |
| The dump is absent from the backup | FAIL | Taxonomy #2 with a database attached. |
| No `verify_with` block | ERROR | The config is wrong, not the backup. |
| No container runtime | ERROR | The tool cannot run. |
| Image missing or won't start | ERROR | Same. |

The risk accepted on the FAIL side is that a genuinely broken config — wrong
image, a `load:` pointing at the wrong file — reads as a failed backup and
alerts. That was judged the better error: the opposite mistake is a dump that
genuinely cannot be restored, reported as tool breakage, and quietly not
alerted on.

When the database is unavailable, database assertions are skipped rather than
reported, and the single FAIL or ERROR line is their explanation. File
assertions still run: one broken thing must not abort the others.

**Revisit when:** a real operator hits the wrong-config-reads-as-FAIL case and
finds it confusing. The fix then is a clearer message, not a reclassification —
moving load failures to ERROR would take the alert away from the case that
needs it most.

### The version check is a pre-load gate, not an assertion
The plan had `dump_version_matches` in the `assert:` list. It could not work
there, and the reason is worth keeping: a 16 dump into a 15 server fails during
the load, so an assertion running afterwards never executes for the one case it
exists for. What the operator would actually see is a wall of psql syntax
errors.

So the check runs between "server is up" and "psql starts". It reads the
version pg_dump writes into the header, compares major versions against the
running server, and on a mismatch reports a `LoadError` — which the runner
already treats as a FAIL, because a dump that cannot load into the operator's
own image is a verdict about the backup.

Three properties chosen deliberately:

- **Only newer-into-older is rejected.** Older into newer is supported by
  Postgres and is what an upgrade looks like. Flagging it would fail runs that
  are working exactly as intended.
- **Minor versions are ignored.** 16.2 into 16.14 is fine, and comparing them
  would generate noise indistinguishable from the real signal.
- **An unreadable header is not an error.** Custom-format dumps and
  hand-written SQL have no such line, and they are loaded anyway. The gate
  improves the message where it can; refusing what it does not understand would
  turn a diagnostic into an obstacle.

**Revisit when:** custom-format dumps (`pg_dump -Fc`) are supported. Their
version lives in the archive header rather than a comment, so the parser gains a
second shape — and `pg_restore`, not psql, becomes the loader.

### Query assertions: what is a verdict, what is the operator's mistake
The queries come from the operator's own config, so SQL injection is not the
threat model — an operator who wants to run arbitrary SQL against their own
restored backup may. The real risks are a query that hangs and a query that
returns something other than what the assertion expects.

Both are handled once, in `query_support.go`, rather than in each assertion:

- A context deadline **and** a server-side `statement_timeout`. The first stops
  constat waiting; the second stops the server working. Without the second, a
  cancelled query keeps burning time inside a container about to be destroyed.
- A scan that checks exactly one row and exactly one column instead of assuming
  either. A wrong-shaped query would otherwise produce a confident wrong answer,
  which is the failure this project can least afford.
- A **read-only transaction**. A verification must not be able to modify the
  data it is verifying, however the operator writes the SQL.

The boundary between verdict and error:

| Situation | Reported as | Why |
|---|---|---|
| Count below `min`, or newest row too old | FAIL | The finding. |
| Table does not exist (SQLSTATE 42P01) | FAIL | Taxonomy #2 — a table that should be there and is not. |
| Any other SQL error | ERROR | A typo in a column name is the operator's mistake, not the backup's. |
| `query_newer_than` gets NULL or no rows | FAIL | An empty table is how MAX() answers "nothing to date". |
| `query_min` gets NULL or no rows | ERROR | A count query cannot do that, so the query is not a count. |
| Timestamp in the future | ERROR | Same call as sessions 1 and 5: an untrustworthy answer must never read as a pass. |

The asymmetry in the middle two rows is deliberate and is the part most likely
to look like an inconsistency later. `query_newer_than` asks "how fresh is the
data", and an empty table answers that question — badly, which is the verdict.
`query_min` asks for a number, and NULL is not a number, so the query is wrong.

Splitting 42P01 from other SQL errors costs a `pgconn` import inside
`internal/assert`, which is the first Postgres-specific thing in the assertion
model. **Revisit when:** a second engine arrives — that import and `Env.DB` are
the two places that will need generalising, and they should move together.

### What makes the report canonical, and why each choice was forced
A signature over bytes that are not reproducible proves nothing, so every
decision here was made to remove a source of variation rather than for taste.

- **No maps anywhere in the model.** Go randomises map iteration; struct fields
  serialise in declaration order. The model is structs the whole way down
  specifically so there is no ordering question to answer.
- **Durations are integer milliseconds.** A Go duration string (`"1.5s"`) is a
  parsing problem for every consumer that is not Go, and its formatting has
  changed across Go releases.
- **`GeneratedAt` is UTC, truncated to the second.** This costs resolution. It
  buys a timestamp that survives a JSON round trip unchanged, which is what
  lets a verifier re-derive the exact signed bytes from a file it just read.
  Sub-second precision is the usual way this quietly breaks.
- **HTML escaping off.** Otherwise a message containing `<` serialises
  differently depending on encoder settings, and messages come out of backups.
- **No indentation.** One less thing for two writers to disagree about. `jq .`
  is how a human reads it.
- **Empty target list, never null.** A consumer should not have to handle two
  spellings of "nothing here".

**The envelope holds the report as `json.RawMessage`, not as a struct.** This is
the load-bearing part and the easiest to undo by accident. If the file were
produced by re-serialising a `Report`, the bytes in the file could differ from
the bytes that were signed — through indentation, a future Go release changing
some formatting detail, anything — and the signature would fail to verify
against the very document it sits in. `VerifyEnvelope` reads the bytes from the
file for the same reason: verifying a re-serialisation checks what this code
believes the report says, not what the file says.

**Revisit when:** the schema changes. `schema_version` exists for that, and it
goes first in the serialised form so a consumer can dispatch on it before
parsing the rest. A field added to the middle of a struct changes the canonical
bytes of every future report but not of past ones, which is fine — old
signatures cover old bytes.

### Where the signing boundary sits, and what is left to do
`CLAUDE.md` keeps key generation, loading, storage and the ed25519 call with the
author, regardless of the v0 pivot. Session 9 built everything up to that line
and stopped at it deliberately.

Written: the report model, `Canonical`, the `Envelope`, `Write`,
`VerifyEnvelope`, and the `Signer`/`Verifier` interfaces with their contracts
documented in `internal/report/signing.go`.

Not written, and intentionally so: any implementation of `Signer`. The test
stubs hold no key and perform no cryptography.

What remains is bounded:

1. A type implementing `Signer` holding an ed25519 private key.
2. Loading that key from a path, with the file's permissions checked — a signing
   key readable by anyone on the host is not a signing key.
3. A config field naming it, and passing the signer into `writeReport` in
   `cmd/constat/main.go`, where `nil` is passed today.
4. A `Verifier` for the public half, and probably a `constat verify` subcommand,
   since evidence nobody can check is not evidence.

The contract `Signer` must meet is written out in full in that file so the
implementation has no design decisions left in it — only the parts that must be
the author's.

**Revisit when:** a second algorithm is wanted. `Signature.Algorithm` is
recorded per report precisely so adding one does not make old reports ambiguous.

### Webhook config lives at the top level, not per-target
A signed report is one run's outcome across every target; alerting is about
that run, not about any single target succeeding or failing in isolation. Per-
target webhooks would mean deciding how to dedupe five notifications from one
`constat run`, and there was no case that needed it.

**Revisit when:** an operator wants different destinations for different
targets — a "app-db is critical, alert PagerDuty; lab-files is not, alert
Slack" split. That's a real future need, not an imagined one, and the answer is
probably `webhook:` becoming allowed at both levels with target overriding
top-level, not replacing the top-level block.

### Webhook format is a config choice, never sniffed from the URL
"Looks like ntfy.sh" is exactly the guess that stops being right the day an
operator self-hosts ntfy under their own domain, or points Healthchecks.io
through a reverse proxy. `format:` is explicit, defaults to `generic`, and is
validated where the webhook is actually sent (`cmd/constat/main.go`) rather than
at config load — same reason `source.kind` is validated in `internal/run`, not
`internal/config`: config has no dependency on the package that knows which
values are real.

### A webhook URL is redacted the way an error, not appended
`net/http`'s own errors are `*url.Error`, and `Error()` embeds the complete
request URL. A webhook URL is operator-controlled and can carry its own secret
in the path — a Discord webhook token, a private ntfy topic — so a naive
`fmt.Errorf("%v (redacted)", err)` still leaks: the leak is inside `err`, not
appended after it. `redactURL` unwraps `*url.Error` and discards its URL half
outright, keeping only the underlying cause and a host-only destination string
built separately from the parsed URL. Found while writing the redaction, not
assumed correct — the first version was wrong and a test caught it.

### The release job's own tests run `-short`
`ci.yml` runs the full suite, container tests and all, on every push — that was
never revisited from session 6/7's flag that it might be worth a separate slow
job, and it still isn't resolved here. `release.yml` is narrower: it only needs
to know the tagged commit is safe to ship, and that commit already passed the
full suite to get merged. Making the release job depend on a fresh container
pull succeeding on the runner would let a flaky image registry block a release
of code that is already known-good.

**Revisit when:** the `ci.yml` question above is finally decided — the two are
the same shape of trade-off (fast and slightly less thorough vs. slow and
fully thorough) and should probably be resolved together rather than
separately.

### Subprocess output is bounded before it reaches an error
Both `internal/driver` and `internal/container` cap how much of a subprocess's
output they interpolate into an error, at 2000 characters with a visible
truncation marker.

This is a correctness bound, not tidiness, and the reasoning is worth keeping
because the naive version looks harmless. Those errors do not stay local: an
error becomes a `report.Assertion` message, which is written verbatim into
`report.json` and posted as the webhook body. restic emits roughly one line per
affected file on a failed restore — measured at ~276 KB for a repository of
only 400 files.

Unbounded, the failure compounds in the worst available direction: the more
broken the backup, the larger the payload, until the webhook carrying the alert
is rejected for being oversized. The alert fails exactly when it matters.

The two implementations are deliberately separate small copies rather than a
shared package — each is a handful of lines with no logic to get wrong, and
neither package otherwise depends on the other. This is the opposite call from
`safepath`, which *was* extracted, and the difference is that safepath is a
security guard where divergence is silent and dangerous, while a truncation
cap that drifts by a few hundred characters is merely untidy.

**Revisit when:** a third caller appears. That is the point where copies stop
being cheaper than a package.

### The secret-leak story has one link constat does not own
Every path where a secret could reach durable output was enumerated in session
12. The repository passphrase is never read by constat at all — it is handed to
restic as a path. The generated container credential is redacted by `redactDSN`
in the errors `internal/container` wraps, and the webhook URL by `redactURL`.

The link constat does not own: errors raised from inside `internal/assert` —
a query failing because the container died mid-run — have no password in scope
to redact against, and depend on **pgx redacting its own connection string**.
It does, verified empirically rather than assumed: pgx reports ``failed to
connect to `user=constat database=constat` `` with no password in it.

That is an external guarantee, so it is pinned by a test
(`TestPgxConnectionErrorsDoNotCarryThePassword`). A pgx upgrade that changed
the behaviour would otherwise leak into a signed report and be discovered by
whoever read it.

**Revisit when:** a second database engine arrives with a different driver —
the same question has to be asked of it, and the answer will not be inherited.

## Open questions (deferred, with reasons)

### What the report deliberately does not record
Assertion *parameters* — the SQL of a `query_min`, the `max_age` of a file check
— are not in the report. Only the name, the verdict, the human message, and the
duration.

The argument for including them is real: a report that says which query ran is
better evidence than one that says a query ran. The argument against, which won
for v0, is that a query is operator-supplied text and is the most plausible
place for a credential to end up — a connection string in a comment, a password
in a `WHERE` clause. Everything else in the report is generated by this code
and can be reasoned about; parameters cannot.

The messages already carry the numbers that matter ("query returned 500 (min
1)"), so the loss is smaller than it looks.

**Revisit when:** someone needs the report to prove *what* was checked rather
than *that* it was checked — a compliance reader, most likely. The answer then
is probably to record parameters for the assertions that have no free-text
fields, and keep queries out.

### Container hardening stops short of read-only and dropped capabilities
The container gets no host network, a loopback-only published port, memory and
pids caps, `no-new-privileges`, and no mounts. It does not get `--read-only`
(Postgres needs a writable data directory, which means a tmpfs and a size to
guess at) or `--cap-drop=ALL` (untested against the Postgres entrypoint, which
does own its uid/gid setup).

Both were skipped for the same reason: adding a hardening flag that breaks the
image on some host, in a check that runs unattended at 3am, trades a real
availability failure for a speculative isolation gain. The container is already
unreachable from outside loopback and lives seconds.

**Revisit when:** session 12's audit pass, with time to actually test each flag
against the image rather than assume. `--read-only` with an explicit tmpfs for
`/var/lib/postgresql/data` is the more valuable of the two.

### The generated database credential sits next to an author-only rule
`CLAUDE.md` keeps anything touching passwords or signing keys in the author's
hands. This is a randomly generated, single-run credential for a loopback-only
container — not the customer's repository passphrase and not a signing key — so
it was treated as in scope, but it is close enough to the line to be worth
flagging rather than assuming.

What it does: 24 random bytes from `crypto/rand`, written to a file mode 0600
and passed with `--env-file` so it never appears in `ps`, redacted out of any
error before that error can reach stdout.

**Revisit when:** the author reviews it. If the rule is meant to cover any
credential at all rather than the repository's own secrets, this is the piece to
rewrite, and the surrounding code does not change.

### Severity on `Result` — deferred
`Result` carries `Duration` but not `Severity`. Duration is passive: it gets
recorded and printed, and nothing else in the system has to interpret it. It
also feeds failure mode #10 (RTO), though the number that matters there is
restore time, measured in the driver, not per-assertion time.

Severity is active — a third verdict state forces decisions across the whole
system: exit code when only warnings fired, whether the webhook fires, whether
the author or the user picks the level (if the user, every assert entry stops
being a one-key map and the config format changes), and what "passed with
warnings" means in a signed evidence report.

Deferred because there is no assertion today where "warn" is obviously right.
**Revisit when:** the first check appears that shouldn't fail a run — then
design against a real case, and decide at that point whether severity belongs
to the assertion author or the user.

Note the tension to resolve later: a three-state verdict helps the operator
running drills and hurts the compliance artifact, which wants a flat statement
of fact. Those are different customers (v0 vs phase 2).

### Driver interface shape — deferred until two implementations exist
`source.kind` is the dispatch point, mirroring the assertion registry (name →
factory becomes kind → driver). The dispatch is settled; the interface is not.

Working shape, two methods, kept separate deliberately:

    Restore(ctx, dest) error          // actual bytes on disk
    Latest(ctx) (*Snapshot, error)    // metadata only, cheap

The split matters as a product property, not just tidiness: failure mode #1
(silent non-execution) is the headline check and runs off metadata alone, so it
must not require restoring 500 GB.

`Snapshot` is the risky type. Restic's notion (ID, time, paths, hostname) may
not survive contact with other backends — a plain `pg_dump` file has no repo and
no snapshots, only an mtime, and a tarball on S3 is closer to that than to
restic. Keep `Snapshot` minimal (a time, an identifier); let richer drivers
expose extras separately.

**Revisit when:** the Borg driver is written. Extract the interface from two
working implementations rather than from one plus imagination. Section 3 of the
brief wants the model wide enough that adding MySQL is ~100 lines — the reliable
route to that is generalising from real cases.

**Update, porting `snapshot_age`:** written earlier than planned. `Env` (what
an `Assertion.Check` sees) had nowhere to reach repo metadata from —
`RestoreDir` assumes every check runs post-restore, and snapshot age is
metadata-only by design, the whole point of the `Latest`/`Restore` split
above. So `Env` needed to carry a driver, which meant `Driver` needed to
exist. Still only one implementation (restic) behind it — the two-backend
generalisation this section calls for is still owed, just delayed to the
first day a Borg target shows up rather than paid today.

### Signing: ed25519, stdlib only, and a permission check with teeth
Implemented in session 12 after the author waived `CLAUDE.md`'s signing-key
carve-out for this one piece (the waiver, and its cost, are recorded there).

**Ed25519, `crypto/ed25519` only.** No third-party crypto: a dependency in the
signing path is a dependency that can change what a signature means. PKCS#8 PEM
for the private key, PKIX PEM for the public half — both stdlib, both readable
by `openssl` without constat present, which is the point.

**The key ID is SHA-256 over the *public* key's DER.** It is published in every
report constat writes, so it must be derived from public material alone.

**A group- or world-readable private key is refused, not warned about.** A key
the whole host can read makes every signature it produces meaningless — the
signature no longer says *who*. A warning printed at 3am into a log nobody
reads is not a control; refusing to run is.

**A configured-but-unusable key is fatal, not a fallback to unsigned.** An
operator who asked for evidence and silently received an unsigned report would
not discover it until someone tried to verify — which is exactly when it is too
late to re-run. The key is therefore loaded *before* any restore work begins,
so the failure comes in the first second rather than after an hour of restores
with nothing to re-sign from.

**`keygen` refuses to overwrite.** Losing a private key makes every report ever
signed with it permanently unverifiable, and a keygen that clobbers on a re-run
is one mistyped path away from doing that.

Verified against an independent implementation, not just its own tests:
`openssl pkeyutl -verify` accepts a real signed report and rejects one with a
single flipped verdict. `docs/verifying-reports.md` is written as a
specification so a third party can do the same.

**Revisit when:** key rotation is needed. `algorithm` and `key_id` are recorded
per signature precisely so a second key or scheme can arrive without making old
reports ambiguous — but there is no rotation tooling and no key history, and
that is a real gap for anyone retaining reports as long-term evidence.

### Container startup retries a lost port race, and nothing else
The published port is kernel-assigned, and another process can take it between
the kernel choosing it and the runtime binding it — observed once as rootless
podman's `pasta failed ... Failed to bind port N`.

`Start` makes up to three attempts, each a *completely fresh* container: new
name, new scratch directory, new port. The failed attempt is torn down before
the next begins, so a retry never inherits state — which is what keeps session
7's mutation-tested teardown guarantee intact.

Retrying is worth it because the alternative is a spurious ERROR on a backup
that is fine. constat classifies that correctly (the tool could not run, not
the backup is broken), but an operator woken by it still has to work that out,
and alert fatigue is how real failures come to be ignored.

The classification is deliberately **narrow**: only port-binding messages are
retried. Matching broadly would turn a missing image or a dead daemon — fast,
clear failures — into slow ones. It matches on message text because shelling
out to a CLI leaves no error code to read, which is the honest cost of that
decision.

Three attempts, not more: a collision that survives three fresh ports is not a
collision, it is something that will not fix itself by waiting.
