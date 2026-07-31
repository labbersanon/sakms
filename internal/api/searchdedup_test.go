package api

import "testing"

// item is a minimal release-like type for exercising dedupeReleases in isolation
// (the generic works over any T given a keyOf adapter). The fields mirror the
// grab-bearing fields the real call sites carry, so the grab-safety case can
// assert whole-item selection.
type item struct {
	downloadURL  string
	title        string
	seeders      int
	releaseTitle string
	protocol     string
	sizeBytes    int64
}

func keyOf(it item) releaseDedupKey {
	return releaseDedupKey{downloadURL: it.downloadURL, normalizedTitle: it.title, seeders: it.seeders}
}

// TestDedupeReleases_RootCauseGap is the confirmed root cause: two items with an
// identical normalized title but DIFFERENT non-empty DownloadURLs (the same
// release cross-posted to two indexers) must collapse to exactly one survivor —
// the higher-seeder one. Today's pre-fix logic keeps both.
func TestDedupeReleases_RootCauseGap(t *testing.T) {
	in := []item{
		{downloadURL: "http://a/1", title: "same title", seeders: 3},
		{downloadURL: "http://b/2", title: "same title", seeders: 9},
	}
	out := dedupeReleases(in, keyOf)
	if len(out) != 1 {
		t.Fatalf("expected 1 survivor, got %d (%+v)", len(out), out)
	}
	if out[0].seeders != 9 {
		t.Errorf("expected the higher-seeder (9) survivor, got %+v", out[0])
	}
}

// TestDedupeReleases_URLMatch: two items with the same non-empty DownloadURL but
// different titles collapse to the higher-seeder one.
func TestDedupeReleases_URLMatch(t *testing.T) {
	in := []item{
		{downloadURL: "http://x/same", title: "title A", seeders: 2},
		{downloadURL: "http://x/same", title: "title B", seeders: 5},
	}
	out := dedupeReleases(in, keyOf)
	if len(out) != 1 {
		t.Fatalf("expected 1 survivor, got %d (%+v)", len(out), out)
	}
	if out[0].seeders != 5 {
		t.Errorf("expected the higher-seeder (5) survivor, got %+v", out[0])
	}
}

// TestDedupeReleases_DistinctQualityVariantsNotCollapsed: two items with
// DIFFERENT normalized titles (distinct resolution/source/codec tokens) and
// different URLs both survive — the spec's quality-variant invariant.
func TestDedupeReleases_DistinctQualityVariantsNotCollapsed(t *testing.T) {
	in := []item{
		{downloadURL: "http://a/1", title: "scene name 1080p WEB DL", seeders: 3},
		{downloadURL: "http://b/2", title: "scene name 2160p WEB DL", seeders: 3},
	}
	out := dedupeReleases(in, keyOf)
	if len(out) != 2 {
		t.Fatalf("expected BOTH distinct-quality variants to survive, got %d (%+v)", len(out), out)
	}
}

// TestDedupeReleases_EmptyURLNonCollapse is the CONCERN 3 guard: two items with
// BOTH empty DownloadURL and DIFFERENT titles must both survive — proving the URL
// criterion is guarded on non-empty and does not collapse everything under "".
func TestDedupeReleases_EmptyURLNonCollapse(t *testing.T) {
	in := []item{
		{downloadURL: "", title: "title A", seeders: 1},
		{downloadURL: "", title: "title B", seeders: 1},
	}
	out := dedupeReleases(in, keyOf)
	if len(out) != 2 {
		t.Fatalf("expected both empty-URL distinct-title items to survive, got %d (%+v)", len(out), out)
	}
}

// TestDedupeReleases_TieKeepsFirst: equal seeders → the first occurrence survives
// and order is preserved.
func TestDedupeReleases_TieKeepsFirst(t *testing.T) {
	in := []item{
		{downloadURL: "http://a/1", title: "dup", seeders: 4, releaseTitle: "FIRST"},
		{downloadURL: "http://b/2", title: "dup", seeders: 4, releaseTitle: "SECOND"},
		{downloadURL: "http://c/3", title: "other", seeders: 1, releaseTitle: "THIRD"},
	}
	out := dedupeReleases(in, keyOf)
	if len(out) != 2 {
		t.Fatalf("expected 2 survivors, got %d (%+v)", len(out), out)
	}
	if out[0].releaseTitle != "FIRST" {
		t.Errorf("expected the first tied occurrence to survive, got %q", out[0].releaseTitle)
	}
	if out[1].releaseTitle != "THIRD" {
		t.Errorf("expected stable order (THIRD second), got %q", out[1].releaseTitle)
	}
}

// TestDedupeReleases_GrabSafety proves the function selects whole items and never
// mutates a survivor's fields: the surviving item's ReleaseTitle/DownloadURL/
// protocol/sizeBytes equal the input winner's byte-for-byte.
func TestDedupeReleases_GrabSafety(t *testing.T) {
	winner := item{downloadURL: "http://b/2", title: "same title", seeders: 9,
		releaseTitle: "Real.Release.2160p-GRP", protocol: "torrent", sizeBytes: 123456789}
	in := []item{
		{downloadURL: "http://a/1", title: "same title", seeders: 3,
			releaseTitle: "Loser.Release-GRP", protocol: "usenet", sizeBytes: 1},
		winner,
	}
	out := dedupeReleases(in, keyOf)
	if len(out) != 1 {
		t.Fatalf("expected 1 survivor, got %d (%+v)", len(out), out)
	}
	if out[0] != winner {
		t.Errorf("expected the survivor to be the input winner byte-for-byte, got %+v want %+v", out[0], winner)
	}
}

// TestDedupeReleases_NonNilEmptyReturn: nil / empty input returns a non-nil empty
// slice (so it JSON-encodes as [] not null).
func TestDedupeReleases_NonNilEmptyReturn(t *testing.T) {
	out := dedupeReleases[item](nil, keyOf)
	if out == nil {
		t.Fatal("expected a non-nil slice for nil input")
	}
	if len(out) != 0 {
		t.Errorf("expected an empty slice, got %+v", out)
	}
}
