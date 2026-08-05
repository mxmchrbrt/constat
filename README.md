# constat

Prove your backups actually restore. constat takes an existing restic
repository, restores it into a disposable environment, runs assertions
against the result, and emits a signed, dated report — then alerts if
anything's wrong.

It never stores your backup data. It doesn't replace restic, Borg, or
`pg_dump`; it's the layer above them that most self-hosted setups don't have:
someone actually checking the restore works, on a schedule, instead of
finding out during an incident.

## What's real today, and what isn't

This is v0. Being precise about the boundary matters more than sounding
finished — a tool whose job is telling you the truth about your backups has
no business overstating itself.

**Works:** restic repositories, file assertions, Postgres dump verification
against a real disposable container, JSON and HTML reports, webhook alerting,
and ed25519-signed reports — verifiable with `constat verify` or with ordinary
tooling like openssl, per
[`docs/verifying-reports.md`](docs/verifying-reports.md).

**Not built yet:**

- **Borg.** restic is the only `source.kind`. If you saw a Borg claim
  elsewhere, it was wrong; this is the correct statement.
- **MySQL and other databases.** Postgres only.
- **Key rotation.** `algorithm` and `key_id` are recorded per signature so a
  second key can be introduced without making old reports ambiguous, but there
  is no rotation tooling. Rotating today means keeping the old public key to
  verify old reports.

## Five minutes to a first verification

You'll need a restic repository and its password file. If you don't have one
handy, the last section below builds a disposable one to try this against.

**1. Install.**

```bash
go install github.com/mxmchrbrt/constat/cmd/constat@latest
```

Or grab a binary from [Releases](https://github.com/mxmchrbrt/constat/releases)
(linux/amd64 and linux/arm64) and put it on your `PATH`. Or:

```bash
docker build -t constat .
```

**2. Write a config.** Copy [`constat.yaml.example`](constat.yaml.example) and
point it at your repository:

```yaml
targets:
  - name: my-backup
    source:
      kind: restic
      repo: /path/to/your/restic/repo
      password_file: /path/to/your/restic/password
    assert:
      - newest_snapshot_age_max: 24h
```

That single assertion already catches the most common real-world failure:
the backup job silently stopped running and nothing noticed.

**3. Run it.**

```bash
constat constat.yaml
```

```
PASS  my-backup: newest snapshot a1b2c3d4 is 3h12m old (max 24h0m0s)
```

Exit code is 0 on pass, 1 on fail or error — wire it into whatever already
pages you.

**4. Ask more of it.** `newest_snapshot_age_max` only proves the repository
metadata looks healthy. To prove the *files* restore, add assertions that
need the actual data — constat then restores the snapshot into a temp
directory before running them:

```yaml
    assert:
      - newest_snapshot_age_max: 24h
      - path_exists: important-file.txt
      - file_count_min:
          min: 10
```

See `constat.yaml.example` for the full picture, including verifying a
Postgres dump against a real, disposable Postgres container — not a checksum,
an actual restore.

**5. Get a report and an alert.**

```bash
constat -report report.json -html report.html constat.yaml
```

To make the report verifiable evidence rather than just a file, generate a
signing key and point the config at it:

```bash
constat keygen -out /etc/constat/signing.key
```

```yaml
signing:
  key_file: /etc/constat/signing.key
```

Anyone with the public half can then check it, without needing constat's
config or anything secret:

```bash
constat verify -key /etc/constat/signing.key.pub report.json
```

**6. Alert on it.**

```yaml
webhook:
  url: https://ntfy.sh/your-topic
  format: ntfy   # or: healthchecks, discord, generic
```

Fires on pass and on fail, deliberately — an alert that only fires on
failure can't tell "healthy" from "stopped running three weeks ago".

**7. Run it on a schedule.** constat ships a scheduler *example*, not a
scheduler — plug it into whatever you already use. A systemd timer is in
[`examples/systemd/`](examples/systemd/):

```bash
sudo cp examples/systemd/constat.* /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now constat.timer
```

## Assertions

| Assertion | Catches |
|---|---|
| `newest_snapshot_age_max` | The backup job stopped running |
| `newest_file_age_max` | The job still runs, but stopped capturing new data |
| `path_exists` | A restore that succeeds but the app won't boot |
| `file_count_min` | A directory silently dropped from the backup's scope |
| `query_min` | A table that should have rows and doesn't (bad retention, wrong dump flags) |
| `query_newer_than` | The database dump loads fine and the data inside it is stale |

Each one exists because it catches a specific, named failure — see
`CLAUDE.md`'s failure taxonomy for the reasoning behind each, and
`ARCHITECTURE.md` for the design decisions and why each one was made.

## Running the tests

```bash
go build ./... && go vet ./... && go test ./...
```

Some tests need a real `restic` binary and a container runtime (Docker or
Podman) to exercise the restore lifecycle and the database path end to end —
they skip cleanly, and under `-short`, when either is unavailable.

## Trying it without a real backup

Build a disposable restic repository to point constat at:

```bash
mkdir -p /tmp/constat-demo/{data,repo}
echo hello > /tmp/constat-demo/data/hello.txt

export RESTIC_REPOSITORY=/tmp/constat-demo/repo
export RESTIC_PASSWORD=demo
restic init
restic backup /tmp/constat-demo/data

echo demo > /tmp/constat-demo/pass
```

```yaml
targets:
  - name: demo
    source:
      kind: restic
      repo: /tmp/constat-demo/repo
      password_file: /tmp/constat-demo/pass
    restore:
      strip_prefix: /tmp/constat-demo/data
    assert:
      - newest_snapshot_age_max: 24h
      - path_exists: hello.txt
```

```bash
constat demo-constat.yaml
```

## License

AGPL-3.0. See [`LICENSE`](LICENSE).
