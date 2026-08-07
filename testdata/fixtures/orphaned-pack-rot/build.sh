#!/bin/sh
# orphaned-pack-rot — failure mode #4, corruption at rest, in the one place a
# restore drill structurally cannot look.
#
# The repository holds two snapshots. Everything the newest one references is
# intact, so the restore succeeds and every file assertion passes. The damage
# is in a pack only the older snapshot points at — an archive that was backed
# up last year and deleted since — which a restore of the latest snapshot never
# reads and therefore never notices.
#
# This is the fixture behind the claim that `repository_check` is not
# redundant with a restore: the restore is green here, and the repository is
# damaged.
. "$(dirname "$0")/../lib.sh"

fixture_init "$@"

# A file large enough to land in its own pack, with deterministic content:
# same bytes on every build, so the fixture means the same thing every time.
seq 1 400000 >"$FIXTURE_DATA/archive-2024.bin"

fixture_restic backup "$FIXTURE_DATA" >/dev/null

# Every pack written by that first backup. Recorded rather than picked by size:
# the point is to damage exactly what the second snapshot does not reference,
# and after this backup that is all of them.
find "$FIXTURE_OUT/repo/data" -type f >"$FIXTURE_OUT/first-packs"

# The second backup shares no content with the first — the archive is gone and
# the files that replace it are new. Nothing in the packs above is reachable
# from the snapshot a restore will pick.
rm "$FIXTURE_DATA/archive-2024.bin"
fixture_file config.php "$FRESH_MTIME"
fixture_file files/a.txt "$FRESH_MTIME"

fixture_restic backup "$FIXTURE_DATA" >/dev/null

# restic writes packs read-only, hence the chmod either side.
while read -r pack; do
	chmod u+w "$pack"
	truncate -s 40 "$pack"
	chmod 444 "$pack"
done <"$FIXTURE_OUT/first-packs"

fixture_finish
