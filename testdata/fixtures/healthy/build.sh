#!/bin/sh
# healthy — the control. Nothing is wrong with this backup.
#
# A corpus of broken fixtures cannot tell you whether the assertions work; it
# only tells you they fire. This one proves they stay quiet when they should,
# which is the half that catches an assertion that fails against everything.
. "$(dirname "$0")/../lib.sh"

fixture_init "$@"

fixture_file config.php "$FRESH_MTIME"
fixture_file files/a.txt "$FRESH_MTIME"
fixture_file files/b.txt "$FRESH_MTIME"
fixture_file files/c.txt "$FRESH_MTIME"

fixture_restic backup "$FIXTURE_DATA" >/dev/null
fixture_finish
