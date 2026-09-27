package usenet

import (
	"strings"
	"testing"
)

func TestFailingSegmentFromError(t *testing.T) {
	if got := failingSegmentFromError("dial tcp timeout"); got != "" {
		t.Fatalf("non-segment error should be empty, got %q", got)
	}
	if got := failingSegmentFromError(`"part028.rar": segment 59: article 430`); !strings.Contains(got, "segment 59") {
		t.Fatalf("got %q", got)
	}
}

func TestSameDownloads_DetectsSegmentProgress(t *testing.T) {
	a := []Download{{GID: "n", Status: "active", SegmentDone: 1, CurrentFile: "a.rar"}}
	b := []Download{{GID: "n", Status: "active", SegmentDone: 2, CurrentFile: "a.rar"}}
	if sameDownloads(a, b) {
		t.Fatal("segmentDone change must not compare equal")
	}
}
