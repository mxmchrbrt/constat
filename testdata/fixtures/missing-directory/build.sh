#!/bin/sh
# missing-directory — failure mode #2, coverage drift.
#
# uploads/ was added to the application and never added to the backup's include
# list. The backup is healthy, runs nightly, and is incomplete. Modelled with
# --exclude, which is what a stale include list amounts to.
. "$(dirname "$0")/../lib.sh"

fixture_init "$@"

fixture_file config.php "$FRESH_MTIME"
fixture_file files/a.txt "$FRESH_MTIME"
fixture_file files/b.txt "$FRESH_MTIME"
fixture_file files/c.txt "$FRESH_MTIME"

# Present on disk, absent from the backup.
fixture_file uploads/photo-01.bin "$FRESH_MTIME"
fixture_file uploads/photo-02.bin "$FRESH_MTIME"

fixture_restic backup --exclude "$FIXTURE_DATA/uploads" "$FIXTURE_DATA" >/dev/null
fixture_finish
