package dedup

import (
	"testing"

	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestRefineByPHash_TrackedCandidateSelectedRegardlessOfPosition isolates
// refineByPHash's reference-selection logic from any caller's ordering
// convention. Every mode (Movies/Series/Adult) happens to always append its
// tracked candidate first, so a test driven through Scan/ScanLibrary can't
// tell "picked because TrackedID != 0" apart from "picked because it's
// index 0" — this calls refineByPHash directly with the tracked candidate
// placed LAST, proving selection is genuinely TrackedID-driven, not just
// position-driven.
//
// The hash arrangement is deliberately chosen so the two possible reference
// choices produce DIFFERENT survivor sets — a naive "always compare against
// nearHash/refHash, they're close to each other anyway" arrangement would
// pass even with a broken selection (verified: an earlier draft of this test
// did exactly that and passed regardless of which candidate was picked as
// reference, so it proved nothing). Here: orphan-far (farHash, maximally
// distant from everything) sits at index 0; orphan-near (nearHash, close
// ONLY to the tracked candidate's refHash) sits at index 1; the tracked
// candidate (refHash, TrackedID=7) sits last at index 2.
//   - Correct (tracked selected): compare far-vs-ref (far, dropped),
//     near-vs-ref (close, kept) -> survivors = {tracked, near}.
//   - Buggy (index 0 selected): compare near-vs-far (far, dropped),
//     tracked-vs-far (far, dropped) -> survivors = {far itself, alone}.
//
// These sets are disjoint, so the assertions below only pass under the
// correct, TrackedID-driven selection.
func TestRefineByPHash_TrackedCandidateSelectedRegardlessOfPosition(t *testing.T) {
	candidates := []proposals.Candidate{
		{Path: "orphan-far", TrackedID: 0, PHash: farHash},
		{Path: "orphan-near", TrackedID: 0, PHash: nearHash},
		{Path: "tracked", TrackedID: 7, PHash: refHash}, // reference, deliberately last
	}

	got := refineByPHash(candidates, phash.Frames, 2)

	var sawTracked, sawNear, sawFar bool
	for _, c := range got {
		switch c.Path {
		case "tracked":
			sawTracked = true
		case "orphan-near":
			sawNear = true
		case "orphan-far":
			sawFar = true
		}
	}
	if !sawTracked {
		t.Error("expected the TrackedID!=0 candidate to be kept as the reference even though it's last in the slice")
	}
	if !sawNear {
		t.Error("expected the near orphan (close to the tracked reference) to be kept")
	}
	if sawFar {
		t.Error("expected the far orphan (divergent from the tracked reference, and from the near orphan) to be dropped")
	}
	if len(got) != 2 {
		t.Errorf("expected exactly 2 survivors (tracked + near), got %d: %+v", len(got), got)
	}
}
