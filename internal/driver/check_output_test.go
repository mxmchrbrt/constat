package driver

import (
	"strings"
	"testing"
)

// Verbatim output from restic 0.18 checking a repository with two truncated
// packs. The point of the test is what survives: this text becomes a report
// line and a webhook body, and the sentence naming the damaged pack has to be
// findable in it.
const truncatedPackOutput = `create exclusive lock for repository
load indexes
check all packs
pack 9dd3c138c2e6bb3ff55be09c19aea76cf38e9113c80a9c85d034cc0f398e64f0: unexpected file size: got 40, expected 1574
pack 101d638f9802034f2c25ba419e388880a42c4e88cd985674e0639449ad7fce46: unexpected file size: got 40, expected 151816
check snapshots, trees and blobs
Load(<data/9dd3c138c2>, 247, 500) failed: file is too short
Load(<data/9dd3c138c2>, 247, 500) failed: file is too short
error for tree 49e9170e:
  ReadFull(<data/9dd3c138c2>): file is too short
[0:00] 100.00%  2 / 2 snapshots

The repository contains damaged pack files. These damaged files must be removed to repair the repository. This can be done using the following commands. Please read the troubleshooting guide at https://restic.readthedocs.io/en/stable/077_troubleshooting.html first.

restic repair packs 9dd3c138c2e6bb3ff55be09c19aea76cf38e9113c80a9c85d034cc0f398e64f0 101d638f9802034f2c25ba419e388880a42c4e88cd985674e0639449ad7fce46
restic repair snapshots --forget

Damaged pack files can be caused by backend problems, hardware problems or bugs in restic. Please open an issue at https://github.com/restic/restic/issues/new/choose for further troubleshooting!
Fatal: repository contains errors`

func TestSummariseCheck(t *testing.T) {
	got := summariseCheck([]byte(truncatedPackOutput))

	mustKeep := []string{
		"pack 9dd3c138c2e6bb3ff55be09c19aea76cf38e9113c80a9c85d034cc0f398e64f0: unexpected file size",
		"pack 101d638f9802034f2c25ba419e388880a42c4e88cd985674e0639449ad7fce46: unexpected file size",
		"error for tree 49e9170e",
		"Fatal: repository contains errors",
	}
	for _, want := range mustKeep {
		if !strings.Contains(got, want) {
			t.Errorf("summary dropped a decisive line: %q\ngot: %s", want, got)
		}
	}

	mustDrop := []string{
		"create exclusive lock",
		"load indexes",
		"check all packs",
		"100.00%",
		"troubleshooting guide",
		"restic repair packs",
		"open an issue",
	}
	for _, unwanted := range mustDrop {
		if strings.Contains(got, unwanted) {
			t.Errorf("summary kept noise: %q\ngot: %s", unwanted, got)
		}
	}

	// The same unreadable pack is reported once per read attempt.
	if n := strings.Count(got, "Load(<data/9dd3c138c2>, 247, 500) failed"); n != 1 {
		t.Errorf("repeated line kept %d times, want 1", n)
	}

	if len(got) >= len(truncatedPackOutput) {
		t.Errorf("summary (%d bytes) did not shorten the original (%d bytes)", len(got), len(truncatedPackOutput))
	}
}

// An output this code does not recognise must come through verbose rather
// than empty: something made restic exit non-zero, and swallowing it would
// report a damaged repository with no reason attached.
func TestSummariseCheck_UnrecognisedOutputSurvives(t *testing.T) {
	got := summariseCheck([]byte("something restic has not said before"))

	if !strings.Contains(got, "something restic has not said before") {
		t.Errorf("unrecognised output was lost: %q", got)
	}
}

func TestSummariseCheck_AllNoise(t *testing.T) {
	got := summariseCheck([]byte("create exclusive lock for repository\nload indexes\n"))

	if got == "" {
		t.Error("an all-noise output summarised to nothing; the failure would have no explanation")
	}
}
