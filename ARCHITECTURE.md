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

## Open questions (deferred, with reasons)

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
