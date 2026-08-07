#!/bin/sh
# truncated-repo — failure mode #4, corruption at rest.
#
# A pack file was truncated after the fact: a half-finished upload, a full disk,
# bit rot. The snapshot metadata is untouched, so the repository still lists a
# recent snapshot and looks healthy from the outside, and the restore is what
# fails.
#
# `restic check` also catches this one — a truncated pack is a size mismatch
# against the index — so this fixture is not an argument that a restore drill
# replaces a repository check. See orphaned-pack-rot for the damage a restore
# genuinely cannot see, and repository_check for the assertion that does.
. "$(dirname "$0")/../lib.sh"

fixture_init "$@"

fixture_file config.php "$FRESH_MTIME"
fixture_file files/a.txt "$FRESH_MTIME"
fixture_file files/b.txt "$FRESH_MTIME"
fixture_file files/c.txt "$FRESH_MTIME"

fixture_restic backup "$FIXTURE_DATA" >/dev/null

# Truncate the largest pack: that is the tree pack, and it is the one restic
# caches. A truncated data pack is caught even with a warm cache, so it would
# make for a weaker fixture. restic writes packs read-only, hence the chmod.
pack=$(find "$FIXTURE_OUT/repo/data" -type f -printf '%s %p\n' | sort -rn | head -1 | cut -d' ' -f2-)
if [ -z "$pack" ]; then
	echo "no pack file found to truncate" >&2
	exit 1
fi
chmod u+w "$pack"
truncate -s 40 "$pack"
chmod 444 "$pack"

fixture_finish
