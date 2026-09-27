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

func TestSnapshot_IncludesPosterAndGroups(t *testing.T) {
	m := &Manager{downloads: map[string]*dlState{
		"nzb-a": {
			gid: "nzb-a", status: "active",
			poster: "Jane Doe <jane@example.com>",
			groups: []string{"alt.binaries.movies"},
		},
	}}
	snap := m.readSnapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].Poster != "Jane Doe <jane@example.com>" {
		t.Fatalf("poster=%q", snap[0].Poster)
	}
	if len(snap[0].Groups) != 1 || snap[0].Groups[0] != "alt.binaries.movies" {
		t.Fatalf("groups=%v", snap[0].Groups)
	}
}

func TestSameDownloads_DetectsSegmentProgress(t *testing.T) {
	a := []Download{{GID: "n", Status: "active", SegmentDone: 1, CurrentFile: "a.rar"}}
	b := []Download{{GID: "n", Status: "active", SegmentDone: 2, CurrentFile: "a.rar"}}
	if sameDownloads(a, b) {
		t.Fatal("segmentDone change must not compare equal")
	}
}
