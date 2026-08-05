package container

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Taxonomy #9, version mismatch: a dump taken from Postgres 16 will not load
// into 15, and it only ever surfaces at restore time — which is to say, on the
// day it matters.
//
// Checked before psql runs rather than as an assertion afterwards. An assertion
// could never fire for the case it exists for: the load fails first, and what
// the operator gets is a wall of psql errors about unrecognised syntax rather
// than the one sentence that explains it.
//
// The gate only ever improves the message. A dump whose header it cannot read —
// custom-format output, hand-written SQL, anything not from pg_dump — is loaded
// anyway. Refusing to load what it does not understand would turn a diagnostic
// into an obstacle.

// dumpHeaderScanLimit bounds how much of a dump is read looking for the version
// line. pg_dump writes it within the first few lines; a dump can be hundreds of
// gigabytes, and this must never be the thing that reads them.
const dumpHeaderScanLimit = 64 * 1024

// dumpVersionMarker is what pg_dump writes into a plain-format dump:
//
//	-- Dumped from database version 16.14
const dumpVersionMarker = "-- Dumped from database version "

// parseDumpVersion reads the declared server version from the head of a plain
// pg_dump file. ok is false when there is no such header, which is not an
// error: it means this check has nothing to say about that dump.
func parseDumpVersion(r io.Reader) (major int, raw string, ok bool) {
	scanner := bufio.NewScanner(io.LimitReader(r, dumpHeaderScanLimit))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, dumpVersionMarker) {
			continue
		}
		raw = strings.TrimSpace(strings.TrimPrefix(line, dumpVersionMarker))
		major, ok = majorVersion(raw)
		if !ok {
			return 0, raw, false
		}
		return major, raw, true
	}
	return 0, "", false
}

// majorVersion takes the leading integer of a Postgres version string. "16.14"
// is 16; so is "16.14 (Debian 16.14-1)". Only the major number decides
// compatibility — 16.2 loads into 16.14 and the reverse is fine too.
func majorVersion(v string) (int, bool) {
	v = strings.TrimSpace(v)
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(v[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// checkVersionCompatible reports why a dump cannot be loaded into this server,
// or nil when it can be or when there is not enough information to say.
//
// Only newer-into-older is rejected. Loading an older dump into a newer server
// is supported by Postgres and is what an upgrade looks like, so flagging it
// would fail runs that are working exactly as intended.
func checkVersionCompatible(dumpVersion string, dumpMajor int, serverVersion string) error {
	serverMajor, ok := majorVersion(serverVersion)
	if !ok {
		return nil
	}
	if dumpMajor <= serverMajor {
		return nil
	}
	return fmt.Errorf(
		"dump was taken from PostgreSQL %s but verify_with.image runs %s: a dump cannot be loaded into an older major version, so this backup could not be restored onto that server",
		dumpVersion, serverVersion)
}
