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

## Open questions (deferred, with reasons)

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
