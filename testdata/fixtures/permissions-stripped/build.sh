#!/bin/sh
# permissions-stripped — failure mode #7, restores but won't boot.
#
# Every file is present and current. The entrypoint script came back without its
# executable bit, so the application does not start. Ownership is the other half
# of this failure mode and is not modelled here: setting it requires root, and a
# fixture that only builds as root is a fixture nobody runs.
#
# No assertion catches this yet. That is deliberate and the expectations file
# says so — the corpus is where a known gap is recorded honestly rather than
# left implicit.
. "$(dirname "$0")/../lib.sh"

fixture_init "$@"

fixture_file config.php "$FRESH_MTIME"
fixture_file files/a.txt "$FRESH_MTIME"
fixture_file files/b.txt "$FRESH_MTIME"

# Should be 0755. This is the breakage.
fixture_file bin/entrypoint.sh "$FRESH_MTIME" 644

fixture_restic backup "$FIXTURE_DATA" >/dev/null
fixture_finish
