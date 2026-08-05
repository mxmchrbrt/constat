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

## Open questions (deferred, with reasons)

### Assertion paths carry the backup's original absolute path — unresolved
restic stores absolute paths and reproduces them under the restore target, so a
snapshot of `/home/user/constat-lab/data` restored into a temp directory lands
at `<restoredir>/home/user/constat-lab/data`. Every file assertion's `path:`
must therefore be written as `home/user/constat-lab/data/config.php`, not
`config.php`.

This is bad on three counts: it is unguessable from the config alone, it
silently breaks when a machine's layout changes, and a wrong prefix reads as a
legitimate FAIL rather than as a config error — the exact failure shape the
`Requires()` decision above was designed to avoid elsewhere. It bit a live
regression run during session 5: the lab's snapshot recorded a path from before
the directory was moved, and four assertions failed convincingly for the wrong
reason.

Three routes, none taken yet:

- `restic restore latest:<path> --target dir` flattens that subtree to the
  restore root. Verified working on 0.18.0. Cheapest fix, but it is a
  restic-specific escape hatch and CLAUDE.md forbids restic leaking into core.
- A `restore.strip_prefix:` or `restore.root:` config field, driver-agnostic
  but one more thing the operator has to get right.
- The runner detects the single common prefix of the restored tree and points
  `Env.RestoreDir` at it. Best config ergonomics, most magic, and ambiguous the
  moment a snapshot covers two unrelated paths.

**Revisit when:** session 11 writes the install documentation, or earlier if
session 6's fixture corpus makes the prefix handling painful to express. It is
a config-schema decision, so it is the author's.

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
