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

---

## 1. Silent non-execution — the job stopped running months ago

The cron entry was removed during a migration. The systemd timer is masked. The
container that ran the backup was rebuilt without its schedule. The credentials
expired and the wrapper script swallowed the error.

**Why it's invisible:** nothing alerts on *absence*. Monitoring watches for
failures, and a job that never runs never fails. The last successful run sits
there looking successful.

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

**How to catch it:** restore and have the application (or the database engine)
validate it. A checksum proves the bytes survived the trip; it says nothing
about whether they were coherent when captured.

## 4. Corruption at rest — bit rot, truncated upload, broken chain

A pack file truncated by a full disk. A block silently corrupted on cheap
storage. An incremental chain broken by a partially-uploaded run.

**Why it's invisible:** partly covered by `restic check --read-data` and
`borg check --verify-data`, which is the good news. The bad news is those
commands are slow, expensive on metered storage, and consequently often
scheduled rarely or never.

**A trap worth knowing:** restic caches tree and index packs locally. With a
warm cache, a repository whose tree pack is corrupt can still restore *cleanly*,
because the damaged pack is never read. On the machine that took the backup —
where the cache is warm — corruption can be invisible even to a real restore
unless caching is explicitly disabled.

## 5. Access loss — the repository is perfect, the passphrase is gone

The key lived on the host that died. The passphrase was in a password manager
nobody else can open. The S3 credentials rotated and only the backup script had
the old ones.

**Why it's invisible:** the repository is completely intact. Every check that
runs *from the machine holding the credentials* passes. The failure only
materialises when someone else, or some other machine, needs to get in.

**How to catch it:** perform the restore from credentials and a machine
independent of the one that made the backup. This is a process problem more than
a tooling one, and it's the one most often discovered during the incident.

## 6. Retention destroyed it — pruning worked exactly as misconfigured

`--keep-daily 7` where someone meant `--keep-daily 7 --keep-monthly 12`. A
retention rule applied to the wrong path. Pruning that ran correctly against a
policy that was wrong.

**Why it's invisible:** it looks like correct behaviour, because it *is* correct
behaviour. The tool did precisely what it was told. Nothing distinguishes
"deliberately pruned" from "deleted the only copy of last quarter".

**How to catch it:** assert that snapshots exist at the ages your policy claims
to keep — and, inside a restored database, that rows exist at the ages your
retention claims to preserve.

## 7. Restores but won't boot — ownership, permissions, SELinux, missing config

Files restored as root that need to be owned by the service account. An
executable that came back without its executable bit. SELinux labels lost. A
config file that lived outside the backed-up directory.

**Why it's invisible:** the restore reports complete success, and it *is*
complete — every file is there. The application then refuses to start, and
you're debugging permissions during an outage.

**How to catch it:** restore and actually start the application, or at minimum
assert on ownership and mode, not just presence. Note that ownership generally
requires restoring as root to reproduce faithfully.

## 8. Cross-component referential breakage — the database and the files disagree

The database was dumped at 02:00 and the blob store synced at 03:00. In between,
users uploaded files. Now the database references rows whose files don't exist,
or files exist that the database has never heard of.

**Why it's invisible:** both backups are individually perfect. Both restore
cleanly. Both pass every check either one could make on its own. The
inconsistency exists only *between* them.

**This is the self-hosting killer** — the Nextcloud, Immich, Paperless class of
application, where the database and the object store must agree. Almost nothing
checks it, because checking it requires understanding both components and the
relationship between them.

**How to catch it:** restore both to a common point in time and run a
referential query — rows whose blobs are missing, blobs with no owning row. It's
the hardest item on this list and the most valuable.

## 9. Version mismatch — a Postgres 16 dump into a Postgres 15 server

The dump was taken from a database that's since been upgraded. The restore
target is the version you had when you wrote the runbook.

**Why it's invisible:** the dump file is perfectly valid. It's a text file full
of correct SQL. It simply will not load into an older major version, and you
find out at restore time, in the worst possible way: a wall of syntax errors on
statements the old parser doesn't recognise.

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

**How to catch it:** measure restore duration and treat it as a first-class
result, with a threshold. An untimed restore drill answers "can we?" but not
"in time?", and only the second question matters during an incident.

## 11. Ransomware reinstated — the restore works and brings the malware back

The encryption started three weeks before anyone noticed. Every backup since is
a faithful copy of a compromised system. The restore works flawlessly and
restores the attacker's foothold along with the data.

**Why it's invisible:** restore *validity* and restore *safety* are different
properties, and every backup tool measures only the first. A backup can be
perfectly restorable and actively harmful.

**How to catch it:** honestly, this one is hard, and anyone claiming to solve it
completely is overselling. Retention long enough to predate an intrusion, plus
some signal about when known-good was — file-age distributions, canary files,
scanning the restored tree — is the practical floor.

---

## The uncomfortable common factor

Nine of these eleven are only detectable by performing a real restore and
inspecting the result. Not by checking exit codes, not by verifying checksums,
not by trusting the backup tool's own report.

The industry's honest position on this is well known and rarely acted on: an
untested backup is a hypothesis, not a backup. The gap isn't that people
disagree — it's that testing restores is tedious, so it happens once during
setup, and then never again.

That's the gap constat is built for: [github.com/mxmchrbrt/constat](https://github.com/mxmchrbrt/constat).

*Corrections and additions welcome — if you've hit a failure mode that isn't
on this list, it belongs here.*
