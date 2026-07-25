package adultnewest

import (
	"context"
	"testing"
	"time"
)

// findByID returns the single cached row matching entityID for rowType, or fails.
func findByID(t *testing.T, s *ReleaseStore, rowType RowType, entityID string) MatchedRelease {
	t.Helper()
	list, err := s.List(context.Background(), rowType, "", 1, 100)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, m := range list {
		if m.EntityID == entityID {
			return m
		}
	}
	t.Fatalf("no cached row with entity id %q found in %+v", entityID, list)
	return MatchedRelease{}
}

func browseRow(id string) MatchedRelease {
	return MatchedRelease{RowType: RowScene, EntityID: id, EntitySource: "tpdb", EntityTitle: "Browse " + id, BrowseConfirmed: true}
}

func feedRow(id string, feedID int64, url string, now int64) MatchedRelease {
	return MatchedRelease{
		RowType: RowScene, EntityID: id, EntitySource: "tpdb", EntityTitle: "Feed " + id,
		BrowseConfirmed: false, DownloadURL: url, DownloadProtocol: "torrent", SizeBytes: 111,
		FeedID: feedID, FeedItemKey: url, LastConfirmedSeen: now,
	}
}

// TestInsert_CaseMerge_BrowseThenFeed: a feed match on a pre-cached browse row
// upgrades the grab path (adopts the enclosure) while keeping browse_confirmed=1
// (visibility preserved). This is the M1 / BLOCKING fix.
func TestInsert_CaseMerge_BrowseThenFeed(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	if err := s.Insert(ctx, browseRow("e1")); err != nil {
		t.Fatalf("browse insert: %v", err)
	}
	if err := s.Insert(ctx, feedRow("e1", 5, "http://feed/e1.torrent", now)); err != nil {
		t.Fatalf("feed insert: %v", err)
	}

	got := findByID(t, s, RowScene, "e1")
	if !got.BrowseConfirmed {
		t.Error("browse_confirmed must stay true after a feed match")
	}
	if got.DownloadURL != "http://feed/e1.torrent" || got.FeedID != 5 || got.DownloadProtocol != "torrent" || got.LastConfirmedSeen != now {
		t.Errorf("expected the feed enclosure adopted onto the browse row, got %+v", got)
	}
	if got.EntityTitle != "Browse e1" {
		t.Errorf("identity-stable metadata must be preserved (first writer wins), got title %q", got.EntityTitle)
	}
}

// TestInsert_CaseMerge_FeedThenBrowse: a browse match on a pre-cached feed row
// raises browse_confirmed to 1 without downgrading the feed enclosure.
func TestInsert_CaseMerge_FeedThenBrowse(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	if err := s.Insert(ctx, feedRow("e2", 5, "http://feed/e2.torrent", now)); err != nil {
		t.Fatalf("feed insert: %v", err)
	}
	if err := s.Insert(ctx, browseRow("e2")); err != nil {
		t.Fatalf("browse insert: %v", err)
	}

	got := findByID(t, s, RowScene, "e2")
	if !got.BrowseConfirmed {
		t.Error("browse_confirmed must be raised to true by the browse match")
	}
	if got.DownloadURL != "http://feed/e2.torrent" || got.FeedID != 5 || got.LastConfirmedSeen != now {
		t.Errorf("the feed enclosure must NOT be downgraded by a browse insert, got %+v", got)
	}
}

// TestInsert_CaseMerge_FeedThenFeed: first-feed-wins among feeds — a second
// feed never overwrites the first feed's enclosure, and browse_confirmed stays 0.
func TestInsert_CaseMerge_FeedThenFeed(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	if err := s.Insert(ctx, feedRow("e3", 5, "http://feedA/e3.torrent", now)); err != nil {
		t.Fatalf("feed A insert: %v", err)
	}
	if err := s.Insert(ctx, feedRow("e3", 9, "http://feedB/e3.torrent", now+1000)); err != nil {
		t.Fatalf("feed B insert: %v", err)
	}

	got := findByID(t, s, RowScene, "e3")
	if got.BrowseConfirmed {
		t.Error("browse_confirmed must stay false for a feed-only entity")
	}
	if got.DownloadURL != "http://feedA/e3.torrent" || got.FeedID != 5 {
		t.Errorf("first-feed-wins: the second feed must not overwrite the first's enclosure, got %+v", got)
	}
}

// TestInsert_CaseMerge_BrowseThenBrowse: two browse inserts keep
// browse_confirmed=1, no enclosure, and preserve the first writer's metadata.
func TestInsert_CaseMerge_BrowseThenBrowse(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	first := browseRow("e4")
	first.EntityTitle = "First"
	second := browseRow("e4")
	second.EntityTitle = "Second"
	if err := s.Insert(ctx, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := s.Insert(ctx, second); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	got := findByID(t, s, RowScene, "e4")
	if !got.BrowseConfirmed || got.DownloadURL != "" || got.FeedID != 0 {
		t.Errorf("expected browse_confirmed=1, no enclosure, got %+v", got)
	}
	if got.EntityTitle != "First" {
		t.Errorf("first-writer-wins metadata expected, got %q", got.EntityTitle)
	}
}

// TestRefreshLastConfirmedSeen_UpdatesOnlyGivenFeedKeys asserts writer (ii): the
// full-window bulk refresh advances last_confirmed_seen only for the named
// feed's named keys, leaving other feeds' rows untouched.
func TestRefreshLastConfirmedSeen_UpdatesOnlyGivenFeedKeys(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	if err := s.Insert(ctx, feedRow("a", 5, "http://feed5/a", 100)); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if err := s.Insert(ctx, feedRow("b", 9, "http://feed9/b", 100)); err != nil {
		t.Fatalf("insert b: %v", err)
	}

	if err := s.RefreshLastConfirmedSeen(ctx, 5, []string{"http://feed5/a"}, 5000); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := findByID(t, s, RowScene, "a"); got.LastConfirmedSeen != 5000 {
		t.Errorf("expected feed 5's row refreshed to 5000, got %d", got.LastConfirmedSeen)
	}
	if got := findByID(t, s, RowScene, "b"); got.LastConfirmedSeen != 100 {
		t.Errorf("expected feed 9's row untouched at 100, got %d", got.LastConfirmedSeen)
	}
}

func TestSearchScenes_TitleLikeAcrossSceneAndMovie(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	rows := []MatchedRelease{
		{RowType: RowScene, EntityID: "s1", EntitySource: "tpdb", EntityTitle: "Sunny Afternoon", BrowseConfirmed: true},
		{RowType: RowMovie, EntityID: "m1", EntitySource: "stashdb", EntityTitle: "SUNNY Compilation", BrowseConfirmed: true},
		{RowType: RowScene, EntityID: "s2", EntitySource: "tpdb", EntityTitle: "Rainy Day", BrowseConfirmed: true},
		{RowType: RowStudio, EntityID: "st1", EntitySource: "tpdb", EntityTitle: "Sunny Studios", BrowseConfirmed: true},
	}
	for _, r := range rows {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("insert %q: %v", r.EntityID, err)
		}
	}

	got, err := s.SearchScenes(ctx, "sunny", 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// Case-insensitive title match over scene+movie rows only — the studio is
	// excluded even though its title contains "Sunny".
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.EntityID] = true
	}
	if len(got) != 2 || !ids["s1"] || !ids["m1"] {
		t.Errorf("expected scene s1 and movie m1 (no studio, no non-match), got %+v", got)
	}
}

func TestListRecentScenes_OrdersByDateDescending(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	rows := []MatchedRelease{
		{RowType: RowScene, EntityID: "old", EntitySource: "tpdb", EntityTitle: "Old", EntityDate: "2024-01-01", BrowseConfirmed: true},
		{RowType: RowScene, EntityID: "new", EntitySource: "tpdb", EntityTitle: "New", EntityDate: "2024-12-31", BrowseConfirmed: true},
		{RowType: RowStudio, EntityID: "st", EntitySource: "tpdb", EntityTitle: "Studio", EntityDate: "2025-01-01", BrowseConfirmed: true},
	}
	for _, r := range rows {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("insert %q: %v", r.EntityID, err)
		}
	}

	got, err := s.ListRecentScenes(ctx, 1)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(got) != 2 || got[0].EntityID != "new" || got[1].EntityID != "old" {
		t.Errorf("expected [new, old] scene/movie rows newest-date-first (studio excluded), got %+v", got)
	}
}
