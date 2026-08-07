# Eleven ways a backup that "works" fails to restore

Most backup monitoring answers one question: *did the job run and exit zero?*
That question has a comforting answer almost every time, right up until the
restore. The failures below all produce a green backup job.

This is the taxonomy [constat](https://github.com/mxmchrbrt/constat) is built
against. It's published separately because it's useful whether or not you use
constat — if you take one thing from it, take the habit of asking which of
these your current setup would actually catch.

Each entry says what the failure is, why nothing notices, and what it takes to
detect it. "Detecting" almost always means doing a real restore, which is the
uncomfortable conclusion the whole list points at.

## The axis that matters more than the list: when does it become permanent?

The eleven are grouped by *when the damage is done*, not by when you find out.
The distinction is operational, not academic:

- **Backup-time failures** are already permanent when you discover them. The
  data was never captured, or was captured wrong, or has since been destroyed.
  Finding one during a drill doesn't recover the lost window — it only stops
  you from losing the next one. These deserve the tightest detection loop you
  can afford, because every day of delay is another day of unrecoverable data.
- **Restore-time failures** leave the repository intact. The bytes are fine;
  something *around* the restore is wrong — a lost key, a version gap, a
  permission model, a clock. Caught during a drill, these are all fixable
  before they cost anything. Caught during an incident, they cost everything.

The numbers below are stable identifiers, not a ranking — they're referenced
from constat's code, tests and fixtures, and from anywhere this document has
already been linked. They stay put; the grouping is what carries the meaning.

| Phase | Failure modes |
|---|---|
| **Backup-time** — permanent by the time you look | #1 silent non-execution · #2 coverage drift · #3 inconsistent capture · #8 cross-component referential breakage · #4 corruption at rest · #6 retention destroyed it · #11 ransomware reinstated |
| **Restore-time** — recoverable if a drill finds it first | #5 access loss · #7 restores but won't boot · #9 version mismatch · #10 RTO failure |

---

# Backup-time failures

*The damage is already done. Detection limits future loss; it does not undo
the loss you're detecting.*

## 1. Silent non-execution — the job stopped running months ago

The cron entry was removed during a migration. The systemd timer is masked. The
container that ran the backup was rebuilt without its schedule. The credentials
expired and the wrapper script swallowed the error.

**Why it's invisible:** nothing alerts on *absence*. Monitoring watches for
failures, and a job that never runs never fails. The last successful run sits
there looking successful.

**Permanent because:** every change made during the silent window is gone. The
job restarting tomorrow captures tomorrow's state, not the three months you
lost.

**How to catch it:** check the age of the newest snapshot, not the exit code of
the last run. Any check that requires the job to run in order to report is
blind to this by construction — dead-man's-switch services (Healthchecks.io and
similar) exist precisely for it.

## 2. Coverage drift — a new volume was never added to the include list

The app grew a second data directory. A new database was added. Someone moved
uploads to a separate mount. The backup config was written two years ago and
still backs up exactly what existed then.

**Why it's invisible:** the backup is genuinely healthy. It runs, it succeeds,
it's internally consistent. It is also incomplete, and nothing compares what's
being backed up against what exists.

**Permanent because:** the excluded data has never been in any snapshot. There
is no version of the repository that contains it.

**How to catch it:** assert on expected contents, not just on success — file
counts, specific paths that must exist. It requires knowing what *should* be
there, which is why it can't be fully automatic.

## 3. Inconsistent capture — a hot copy without a snapshot or lock

Files copied while being written. A database directory backed up without a
filesystem snapshot or a proper dump. A multi-file write captured halfway
through.

**Why it's invisible:** it restores cleanly. Every file is present and readable.
The data inside is subtly, silently wrong — a torn write, a half-applied
transaction, an index that doesn't match its table.

**Permanent because:** the incoherent state is what was written to the
repository. No amount of care at restore time reconstructs the transaction that
was mid-flight.

**How to catch it:** restore and have the application (or the database engine)
validate it. A checksum proves the bytes survived the trip; it says nothing
about whether they were coherent when captured.

## 8. Cross-component referential breakage — the database and the files disagree

The database was dumped at 02:00 and the blob store synced at 03:00. In between,
users uploaded files. Now the database references rows whose files don't exist,
or files exist that the database has never heard of.

**Why it's invisible:** both backups are individually perfect. Both restore
cleanly. Both pass every check either one could make on its own. The
inconsistency exists only *between* them.

**Permanent because:** this is the clearest case for the whole grouping. The
skew was created at capture time, by two jobs that never agreed on an instant.
Nothing at restore time can invent the missing hour — you are choosing which
component to make wrong. Snapshotting both against a common point in time is
the only real fix, and it has to happen before the backup, not after.

**This is the self-hosting killer** — the Nextcloud, Immich, Paperless class of
application, where the database and the object store must agree. Almost nothing
checks it, because checking it requires understanding both components and the
relationship between them.

**How to catch it:** restore both to a common point in time and run a
referential query — rows whose blobs are missing, blobs with no owning row. It's
the hardest item on this list and the most valuable.

## 4. Corruption at rest — bit rot, truncated upload, broken chain

A pack file truncated by a full disk. A block silently corrupted on cheap
storage. An incremental chain broken by a partially-uploaded run.

**Why it's invisible:** less invisible than it used to be, and it's worth being
precise about the layers, because they cover different things:

- **`restic check` with no flags** verifies the repository's *structure*: that
  the index is consistent, that every blob the trees reference is accounted
  for in some pack, and that the packs are present at the expected sizes. On a
  cloud backend that catches the realistic failures — a truncated or missing
  upload, a broken chain — cheaply, without downloading pack contents. It is
  substantially more useful than its reputation, and there is little excuse for
  not running it often.
- **`restic check --read-data`** (or `--read-data-subset`, for a sampled
  fraction) is what actually reads pack contents back and compares them against
  the recorded hashes. Only this catches a pack that is present, correctly
  sized, and *wrong inside* — the silent bit flip. It's slow and expensive on
  metered storage, which is why it's typically scheduled rarely or never.
  `--read-data-subset=5%` on a rotating basis is the usual compromise.
- **A restore** reads only what that snapshot references. Packs reachable only
  from older snapshots are never touched, so a restore drill is not a substitute
  for either of the above.

**A trap worth knowing:** restic caches tree and index packs locally. With a
warm cache, a repository whose tree pack is corrupt can still restore *cleanly*,
because the damaged pack is never read. On the machine that took the backup —
where the cache is warm — corruption can be invisible even to a real restore
unless caching is explicitly disabled. (constat passes `--no-cache`
unconditionally for this reason.)

**Permanent because:** once the correct bytes are gone from every copy, they're
gone. Which is the argument for **3-2-1** — three copies, on two kinds of
media, one of them offsite — as the mitigation rather than the detection: it
makes it unlikely that corruption hits every copy at once, so verification has
something to fall back to. Detection tells you which copy went bad; only
redundancy gives you another one.

## 6. Retention destroyed it — pruning worked exactly as misconfigured

`--keep-daily 7` where someone meant `--keep-daily 7 --keep-monthly 12`. A
retention rule applied to the wrong path. Pruning that ran correctly against a
policy that was wrong.

**Why it's invisible:** it looks like correct behaviour, because it *is* correct
behaviour. The tool did precisely what it was told. Nothing distinguishes
"deliberately pruned" from "deleted the only copy of last quarter".

**Permanent because:** prune is a destructive operation against the repository
itself. There is no undo, and the fixed policy only protects snapshots that
still exist.

**How to catch it:** assert that snapshots exist at the ages your policy claims
to keep — and, inside a restored database, that rows exist at the ages your
retention claims to preserve.

## 11. Ransomware reinstated — the restore works and brings the malware back

The encryption started three weeks before anyone noticed. Every backup since is
a faithful copy of a compromised system. The restore works flawlessly and
restores the attacker's foothold along with the data.

**Why it's invisible:** restore *validity* and restore *safety* are different
properties, and every backup tool measures only the first. A backup can be
perfectly restorable and actively harmful.

**Permanent because:** what was captured is a compromised system, and no restore
procedure turns it back into a clean one. The only escape is a snapshot that
predates the intrusion — which is a retention decision made long before the
incident, not something you can arrange afterwards.

**How to catch it:** honestly, this one is hard, and anyone claiming to solve it
completely is overselling. Retention long enough to predate an intrusion, plus
some signal about when known-good was — file-age distributions, canary files,
scanning the restored tree — is the practical floor.

---

# Restore-time failures

*The repository is intact. Something around the restore is broken, and a drill
that finds it today costs you an afternoon instead of an outage.*

## 5. Access loss — the repository is perfect, the passphrase is gone

The key lived on the host that died. The passphrase was in a password manager
nobody else can open. The S3 credentials rotated and only the backup script had
the old ones.

**Why it's invisible:** the repository is completely intact. Every check that
runs *from the machine holding the credentials* passes. The failure only
materialises when someone else, or some other machine, needs to get in.

**Recoverable while:** at least one party can still open the repository. Caught
during a drill, this is a key-escrow chore. Caught after the last holder is
gone, it's indistinguishable from having no backup at all — this is the
restore-time failure that turns permanent fastest.

**How to catch it:** perform the restore from credentials and a machine
independent of the one that made the backup. This is a process problem more than
a tooling one, and it's the one most often discovered during the incident.

## 7. Restores but won't boot — ownership, permissions, SELinux, missing config

Files restored as root that need to be owned by the service account. An
executable that came back without its executable bit. SELinux labels lost. A
config file that lived outside the backed-up directory.

**Why it's invisible:** the restore reports complete success, and it *is*
complete — every file is there. The application then refuses to start, and
you're debugging permissions during an outage.

**Recoverable because:** the data is present and correct. What's missing is a
procedure — the chown, the relabel, the config file that needs backing up too.
Every one of those is knowable in advance and free to fix, if a drill surfaces
it in advance.

**How to catch it:** restore and actually start the application, or at minimum
assert on ownership and mode, not just presence. Note that ownership generally
requires restoring as root to reproduce faithfully.

## 9. Version mismatch — a Postgres 16 dump into a Postgres 15 server

The dump was taken from a database that's since been upgraded. The restore
target is the version you had when you wrote the runbook.

**Why it's invisible:** the dump file is perfectly valid. It's a text file full
of correct SQL. It simply will not load into an older major version, and you
find out at restore time, in the worst possible way: a wall of syntax errors on
statements the old parser doesn't recognise.

**Recoverable because:** the dump is fine. You need the right server version,
which is a provisioning problem, not a data problem — annoying at 3am, trivial
at any other hour.

**Prevent it:** pin the source version in Infrastructure-as-Code, so the version
the database was running when the dump was taken is a recorded fact rather than
institutional memory, and the runbook can reference it instead of guessing. This
is the cheap, correct fix and it should be the default.

**But IaC only prevents drift going forward** — it can't validate a runbook
written before the pinning existed, and it doesn't catch the more general
problem it's a special case of: *runbook bitrot*. The document describes a
system that has since changed underneath it. The only known cure is periodically
executing the runbook, which is tedious enough that in practice it happens where
a compliance control forces it and almost nowhere else. Automating the execution
is the point of the exercise.

**How to catch it:** load the dump into the version you'd actually restore onto.
Compare the dump's declared version against the target *before* attempting the
load, so the failure is one clear sentence rather than a thousand parser errors.

## 10. RTO failure — it restores perfectly, in forty hours

Cold storage with retrieval delays. A repository so fragmented that restoring
means millions of small reads. A 4 TB dataset over a connection that can move
100 GB a day.

**Why it's invisible:** every check passes. The backup is complete, uncorrupted,
and fully restorable. It's a technical success and a commercial disaster,
because the business needed to be back in four hours.

**Recoverable because:** it's an architecture decision with a known cost —
storage class, chunking, bandwidth, a warm standby. All of it is purchasable
in advance and none of it is purchasable during the incident.

**How to catch it:** measure restore duration and treat it as a first-class
result, with a threshold. An untimed restore drill answers "can we?" but not
"in time?", and only the second question matters during an incident.

---

## The uncomfortable common factor

Nine of these eleven are only detectable by performing a real restore and
inspecting the result. The two exceptions are #1, which is a metadata check, and
#4, which `restic check` covers structurally — and even there, the silent bit
flip needs `--read-data` and the correlated-corruption case needs a second copy.
Not exit codes, not checksums, not the backup tool's own report.

The industry's honest position on this is well known and rarely acted on: an
untested backup is a hypothesis, not a backup. The gap isn't that people
disagree — it's that testing restores is tedious, so it happens once during
setup, and then never again.

That's the gap constat is built for: [github.com/mxmchrbrt/constat](https://github.com/mxmchrbrt/constat).

*Corrections and additions welcome — if you've hit a failure mode that isn't
on this list, it belongs here.*

## Changelog

- **2026-08-07** — Regrouped the eleven by backup-time versus restore-time,
  after [MichaelEischer pointed out](https://forum.restic.net/t/eleven-ways-a-backup-that-works-fails-to-restore-a-taxonomy-plus-a-tool-built-against-it/10953/6)
  that #8 is a capture-time failure sitting in a restore-time list, and that the
  distinction changes what you can still do about it. Same thread: corrected #4
  to state what plain `restic check` actually covers and added 3-2-1 as the
  mitigation, and added IaC version pinning to #9. Numbering is unchanged and
  stays stable.
