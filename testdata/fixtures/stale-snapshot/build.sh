#!/bin/sh
# stale-snapshot — failure mode #1, silent non-execution.
#
# The repository is intact and every file is present. The job simply stopped
# running: the newest snapshot, and everything in it, is from May.
. "$(dirname "$0")/../lib.sh"

fixture_init "$@"

fixture_file config.php "$STALE_DATE"
fixture_file files/a.txt "$STALE_DATE"
fixture_file files/b.txt "$STALE_DATE"
fixture_file files/c.txt "$STALE_DATE"

fixture_restic backup --time "$STALE_DATE" "$FIXTURE_DATA" >/dev/null
fixture_finish
