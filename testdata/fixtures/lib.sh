# Shared helpers for the fixture build scripts. Sourced, not executed.
#
# Every fixture builds a restic repository from scratch into a directory given
# on the command line, so nothing binary is committed and a fixture can be
# rebuilt and inspected by hand at any time:
#
#     testdata/fixtures/stale-snapshot/build.sh /tmp/look-at-this
#     restic -r /tmp/look-at-this/repo --password-file /tmp/look-at-this/pass snapshots
#
# Determinism means same content, same structure, same timestamps on every
# build. It does not mean byte-identical repositories: restic encrypts with a
# fresh key per repository, so pack file names and IDs differ every time. What
# the tests assert on is the verdict each fixture produces, and that is stable.

set -eu

# STALE_DATE is the fixed clock for fixtures that must stay stale. An absolute
# date rather than "90 days ago" so a fixture does not quietly change meaning
# between runs — it only gets more stale, which is the direction that keeps the
# expected verdict correct.
STALE_DATE='2026-05-05 03:00:00'

# FRESH_MTIME is for fixtures that must stay current. These cannot use a fixed
# date: "the snapshot is recent" is only true relative to now.
FRESH_MTIME='1 hour ago'

FIXTURE_PASSPHRASE='fixture-passphrase'

# fixture_init prepares $1 as a fixture directory with an initialised
# repository, and sets FIXTURE_OUT and FIXTURE_DATA for the rest of the script.
fixture_init() {
	if [ $# -ne 1 ]; then
		echo "usage: $0 <output-directory>" >&2
		exit 2
	fi

	FIXTURE_OUT=$1
	mkdir -p "$FIXTURE_OUT"
	# Resolve, because restic records the path it resolved and the tests
	# compare against it.
	FIXTURE_OUT=$(cd "$FIXTURE_OUT" && pwd -P)
	FIXTURE_DATA="$FIXTURE_OUT/data"

	mkdir -p "$FIXTURE_DATA"
	printf '%s\n' "$FIXTURE_PASSPHRASE" >"$FIXTURE_OUT/pass"
	chmod 600 "$FIXTURE_OUT/pass"

	fixture_restic init >/dev/null
}

# --cache-dir keeps a fixture build out of the user's own restic cache, so a
# fixture that breaks the repository on disk is not quietly repaired by a warm
# cache belonging to something else.
fixture_restic() {
	restic -r "$FIXTURE_OUT/repo" --password-file "$FIXTURE_OUT/pass" --cache-dir "$FIXTURE_OUT/cache" "$@"
}

# fixture_file writes a file with fixed content and a fixed mtime.
# Usage: fixture_file <relative-path> <mtime> [mode]
fixture_file() {
	path="$FIXTURE_DATA/$1"
	mkdir -p "$(dirname "$path")"
	# Content derived from the path, so it is stable across builds and
	# different between files without any randomness.
	printf 'constat fixture payload for %s\n' "$1" >"$path"
	touch -d "$2" "$path"
	if [ $# -ge 3 ]; then
		chmod "$3" "$path"
	fi
}

# fixture_finish records the absolute path the backup captured. The tests read
# it to set restore.strip_prefix, so fixtures work from any directory.
fixture_finish() {
	printf '%s\n' "$FIXTURE_DATA" >"$FIXTURE_OUT/backup-path"
}
