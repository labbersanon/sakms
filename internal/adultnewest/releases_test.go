package adultnewest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
)

func newTestReleaseStore(t *testing.T) *ReleaseStore {
	t.Helper()
	dir := t.TempDir()

	sqlDB, err := db.Open(filepath.Join(dir, "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	return NewReleaseStore(sqlDB)
}

func TestInsertAndList_RoundTripsMatchedRelease(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	m := MatchedRelease{
		RowType:               RowScene,
		EntityID:              "scene-123",
		EntitySource:          "tpdb",
		EntityTitle:           "Some Scene",
		EntityStudio:          "Some Studio",
		EntityImage:           "https://cdn.theporndb.net/scene.jpg",
		EntityDate:            "2026-07-14",
		EntityDurationSeconds: 1800,
		FirstSeenReleaseTitle: "Some.Studio.23.04.22.Performer.Some.Scene.XXX.1080p-GROUP",
		Genres:                []string{"Anal Fetish", "MILF"},
		Performers:            []string{"Jane Doe", "John Roe"},
	}
	if err := s.Insert(ctx, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, err := s.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(list), list)
	}
	got := list[0]
	if got.EntityTitle != "Some Scene" || got.EntitySource != "tpdb" || len(got.Genres) != 2 {
		t.Errorf("unexpected round-tripped release: %+v", got)
	}
	// Regression: a matched entity with no duration silently broke Adult
	// auto-grab (see scan.go's toMatchedRelease doc comment) — confirm the
	// real value survives the cache round trip, not just genres/title/source.
	if got.EntityDurationSeconds != 1800 {
		t.Errorf("EntityDurationSeconds = %d, want 1800", got.EntityDurationSeconds)
	}
	if got.FirstSeenReleaseTitle != "Some.Studio.23.04.22.Performer.Some.Scene.XXX.1080p-GROUP" {
		t.Errorf("FirstSeenReleaseTitle = %q, want the raw release title to survive the round trip", got.FirstSeenReleaseTitle)
	}
	if len(got.Performers) != 2 || got.Performers[0] != "Jane Doe" || got.Performers[1] != "John Roe" {
		t.Errorf("Performers = %v, want the performer list to survive the round trip", got.Performers)
	}
}

// TestInsertAndList_RoundTripsGender proves US-3's new Gender field survives
// the cache round trip, same convention as
// TestInsertAndList_RoundTripsMatchedRelease's other field checks.
func TestInsertAndList_RoundTripsGender(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	m := MatchedRelease{
		RowType: RowPerformer, EntityID: "performer-1", EntitySource: "stashdb",
		EntityTitle: "Riley Reid", Gender: "female", BrowseConfirmed: true,
	}
	if err := s.Insert(ctx, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, err := s.List(ctx, RowPerformer, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Gender != "female" {
		t.Fatalf("expected Gender to round-trip as %q, got %+v", "female", list)
	}
}

// TestInsertAndList_GenderNullReadsBackEmpty proves scanRelease tolerates a
// NULL gender column (the state every pre-existing row is in immediately
// after the 0047 migration, before the backfill drains it) — reading back as
// Go's zero value "", not an error.
func TestInsertAndList_GenderNullReadsBackEmpty(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	m := MatchedRelease{RowType: RowPerformer, EntityID: "performer-null", EntitySource: "stashdb", EntityTitle: "Pre-existing", BrowseConfirmed: true}
	if err := s.Insert(ctx, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Insert always writes a concrete string (possibly ""), never NULL — force
	// the column to NULL directly to simulate a row migrated in before the
	// gender column existed.
	if _, err := s.db.ExecContext(ctx, `UPDATE adult_newest_releases SET gender = NULL WHERE entity_id = 'performer-null'`); err != nil {
		t.Fatalf("forcing gender NULL: %v", err)
	}

	list, err := s.List(ctx, RowPerformer, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Gender != "" {
		t.Fatalf("expected NULL gender to read back as empty string, got %+v", list)
	}
}

// TestInsert_GenderOnConflict proves the ON CONFLICT gender rule (US-3): a
// concrete incoming value overwrites a prior NULL or empty stored value, but
// never overwrites a prior concrete value.
func TestInsert_GenderOnConflict(t *testing.T) {
	ctx := context.Background()

	t.Run("concrete overwrites NULL", func(t *testing.T) {
		s := newTestReleaseStore(t)
		base := MatchedRelease{RowType: RowPerformer, EntityID: "p", EntitySource: "stashdb", EntityTitle: "P", BrowseConfirmed: true}
		if err := s.Insert(ctx, base); err != nil {
			t.Fatalf("seeding: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE adult_newest_releases SET gender = NULL WHERE entity_id = 'p'`); err != nil {
			t.Fatalf("forcing gender NULL: %v", err)
		}

		conflicting := base
		conflicting.Gender = "female"
		if err := s.Insert(ctx, conflicting); err != nil {
			t.Fatalf("re-inserting: %v", err)
		}

		list, err := s.List(ctx, RowPerformer, "", 1, 20)
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(list) != 1 || list[0].Gender != "female" {
			t.Fatalf("expected NULL to be overwritten with the concrete incoming value, got %+v", list)
		}
	})

	t.Run("concrete overwrites empty string", func(t *testing.T) {
		s := newTestReleaseStore(t)
		base := MatchedRelease{RowType: RowPerformer, EntityID: "p", EntitySource: "stashdb", EntityTitle: "P", Gender: "", BrowseConfirmed: true}
		if err := s.Insert(ctx, base); err != nil {
			t.Fatalf("seeding: %v", err)
		}

		conflicting := base
		conflicting.Gender = "male"
		if err := s.Insert(ctx, conflicting); err != nil {
			t.Fatalf("re-inserting: %v", err)
		}

		list, err := s.List(ctx, RowPerformer, "", 1, 20)
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(list) != 1 || list[0].Gender != "male" {
			t.Fatalf("expected empty string to be overwritten with the concrete incoming value, got %+v", list)
		}
	})

	t.Run("incoming value never overwrites an existing concrete value", func(t *testing.T) {
		s := newTestReleaseStore(t)
		base := MatchedRelease{RowType: RowPerformer, EntityID: "p", EntitySource: "stashdb", EntityTitle: "P", Gender: "female", BrowseConfirmed: true}
		if err := s.Insert(ctx, base); err != nil {
			t.Fatalf("seeding: %v", err)
		}

		conflicting := base
		conflicting.Gender = "male"
		if err := s.Insert(ctx, conflicting); err != nil {
			t.Fatalf("re-inserting: %v", err)
		}

		list, err := s.List(ctx, RowPerformer, "", 1, 20)
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(list) != 1 || list[0].Gender != "female" {
			t.Fatalf("expected the existing concrete value to be preserved, got %+v", list)
		}
	})
}

func TestByFeedItemKeys_MapsPresentKeysAndSkipsAbsent(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	// Two feed-sourced rows with distinct enclosure keys, plus a browse-only row
	// (empty feed_item_key) that must never surface for a non-empty key lookup.
	rows := []MatchedRelease{
		{RowType: RowScene, EntityID: "a", EntitySource: "tpdb", EntityTitle: "Scene A",
			EntityStudio: "Studio A", EntityImage: "https://cdn/a.jpg", DownloadURL: "https://x/a.torrent",
			DownloadProtocol: "torrent", FeedID: 1, FeedItemKey: "https://x/a.torrent"},
		{RowType: RowScene, EntityID: "b", EntitySource: "tpdb", EntityTitle: "Scene B",
			EntityStudio: "Studio B", EntityImage: "https://cdn/b.jpg", DownloadURL: "https://x/b.torrent",
			DownloadProtocol: "torrent", FeedID: 1, FeedItemKey: "https://x/b.torrent"},
		{RowType: RowScene, EntityID: "c", EntitySource: "tpdb", EntityTitle: "Browse Only",
			BrowseConfirmed: true, FeedItemKey: ""},
	}
	for _, m := range rows {
		if err := s.Insert(ctx, m); err != nil {
			t.Fatalf("seeding %q: %v", m.EntityID, err)
		}
	}

	got, err := s.ByFeedItemKeys(ctx, []string{"https://x/a.torrent", "https://x/missing.torrent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one match, got %+v", got)
	}
	m, ok := got["https://x/a.torrent"]
	if !ok || m.EntityTitle != "Scene A" || m.EntityStudio != "Studio A" || m.EntityImage != "https://cdn/a.jpg" {
		t.Errorf("unexpected match for key a: %+v", got)
	}
	if _, ok := got["https://x/missing.torrent"]; ok {
		t.Errorf("absent key must not appear in the result map: %+v", got)
	}

	// Empty input is a no-op, non-nil map (mirrors SeenGUIDs' empty-input contract).
	empty, err := s.ByFeedItemKeys(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected empty map for empty keys, got %+v", empty)
	}
}

func TestInsert_DuplicateEntityIsIgnoredNotUpdated(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	// Two different releases resolving to the SAME entity (same row_type +
	// entity_source + entity_id) must collapse to one cache row — this is
	// the real-world case of two different quality rips of the same scene.
	first := MatchedRelease{RowType: RowScene, EntityID: "scene-1", EntitySource: "tpdb", EntityTitle: "First"}
	second := MatchedRelease{RowType: RowScene, EntityID: "scene-1", EntitySource: "tpdb", EntityTitle: "Second"}

	if err := s.Insert(ctx, first); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Insert(ctx, second); err != nil {
		t.Fatalf("unexpected error inserting duplicate entity: %v", err)
	}

	list, err := s.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].EntityTitle != "First" {
		t.Errorf("expected duplicate entity insert to be ignored, keeping the first row; got %+v", list)
	}
}

func TestInsert_SameEntityIDDifferentRowTypeOrSourceIsDistinct(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	// Same entity_id string, but different row_type/source — must NOT
	// collide, since the composite key includes all three.
	releases := []MatchedRelease{
		{RowType: RowScene, EntityID: "123", EntitySource: "tpdb", EntityTitle: "Scene 123"},
		{RowType: RowMovie, EntityID: "123", EntitySource: "tpdb", EntityTitle: "Movie 123"},
		{RowType: RowScene, EntityID: "123", EntitySource: "stashdb", EntityTitle: "StashDB Scene 123"},
	}
	for _, r := range releases {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	sceneResults, err := s.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sceneResults) != 2 {
		t.Fatalf("expected 2 distinct scene entities (different sources), got %d: %+v", len(sceneResults), sceneResults)
	}

	movieResults, err := s.List(ctx, RowMovie, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movieResults) != 1 || movieResults[0].EntityTitle != "Movie 123" {
		t.Errorf("expected 1 distinct movie entity, got %+v", movieResults)
	}
}

func TestList_FiltersByRowTypeAndGenre(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	releases := []MatchedRelease{
		{RowType: RowScene, EntityID: "1", EntitySource: "tpdb", EntityTitle: "Scene A", Genres: []string{"Anal Fetish"}},
		{RowType: RowScene, EntityID: "2", EntitySource: "tpdb", EntityTitle: "Scene B", Genres: []string{"MILF"}},
		{RowType: RowStudio, EntityID: "3", EntitySource: "tpdb", EntityTitle: "Studio A", Genres: []string{"Anal Fetish"}},
	}
	for _, r := range releases {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	sceneResults, err := s.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sceneResults) != 2 {
		t.Errorf("expected 2 scene results, got %d", len(sceneResults))
	}

	analScenes, err := s.List(ctx, RowScene, "Anal Fetish", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(analScenes) != 1 || analScenes[0].EntityTitle != "Scene A" {
		t.Errorf("expected genre filter to isolate Scene A, got %+v", analScenes)
	}

	studioResults, err := s.List(ctx, RowStudio, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(studioResults) != 1 || studioResults[0].EntityTitle != "Studio A" {
		t.Errorf("expected 1 studio result, got %+v", studioResults)
	}
}

func TestSeenGUIDs_TracksMarkSeenIndependentlyOfMatches(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	// A release can be "seen" (already attempted) without ever producing a
	// matched-entity row — the whole point of the separate seen table.
	if err := s.MarkSeen(ctx, "seen-unmatched"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seen, err := s.SeenGUIDs(ctx, []string{"seen-unmatched", "unseen-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seen["seen-unmatched"] || seen["unseen-1"] {
		t.Errorf("unexpected seen map: %+v", seen)
	}
}

func TestMarkSeen_Idempotent(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	if err := s.MarkSeen(ctx, "guid-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.MarkSeen(ctx, "guid-1"); err != nil {
		t.Fatalf("unexpected error marking already-seen guid again: %v", err)
	}
}

func TestSeenGUIDs_EmptyInput(t *testing.T) {
	s := newTestReleaseStore(t)
	seen, err := s.SeenGUIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("expected empty map for empty input, got %+v", seen)
	}
}

func TestDistinctGenres(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	releases := []MatchedRelease{
		{RowType: RowScene, EntityID: "1", EntitySource: "tpdb", Genres: []string{"Anal Fetish", "MILF"}},
		{RowType: RowScene, EntityID: "2", EntitySource: "tpdb", Genres: []string{"MILF", "Goth"}},
	}
	for _, r := range releases {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	genres, err := s.DistinctGenres(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Anal Fetish", "Goth", "MILF"}
	if len(genres) != len(want) {
		t.Fatalf("expected %v, got %v", want, genres)
	}
	for i, g := range want {
		if genres[i] != g {
			t.Errorf("expected sorted distinct genres %v, got %v", want, genres)
			break
		}
	}
}

// TestDistinctPerformerGenders_ReturnsSortedNonEmptyValues covers US-6's
// DistinctPerformerGenders: only RowPerformer rows count, NULL (unbackfilled)
// and '' (reached, no gender on file) are both excluded, and a non-performer
// row's gender (even if somehow non-empty) never contaminates the result.
func TestDistinctPerformerGenders_ReturnsSortedNonEmptyValues(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	releases := []MatchedRelease{
		{RowType: RowPerformer, EntityID: "p1", EntitySource: "tpdb", Gender: "male"},
		{RowType: RowPerformer, EntityID: "p2", EntitySource: "tpdb", Gender: "female"},
		{RowType: RowPerformer, EntityID: "p3", EntitySource: "tpdb", Gender: "male"},
		// Reached-but-no-gender: excluded, not its own bucket.
		{RowType: RowPerformer, EntityID: "p4", EntitySource: "tpdb", Gender: ""},
		// Non-performer row with a (nonsensical but possible) gender value:
		// must never contaminate the performer-only result.
		{RowType: RowStudio, EntityID: "s1", EntitySource: "tpdb", Gender: "male"},
	}
	for _, r := range releases {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	genders, err := s.DistinctPerformerGenders(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"female", "male"}
	if len(genders) != len(want) {
		t.Fatalf("expected %v, got %v", want, genders)
	}
	for i, g := range want {
		if genders[i] != g {
			t.Errorf("expected sorted distinct performer genders %v, got %v", want, genders)
			break
		}
	}
}

// TestListByGender_NarrowsToOneGender covers US-6's read-time gender filter
// backing the Adult Discover dynamic gender-split: a concrete gender value
// narrows to exactly that gender's rows; "" behaves identically to List (no
// filter).
func TestListByGender_NarrowsToOneGender(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	releases := []MatchedRelease{
		{RowType: RowPerformer, EntityID: "p1", EntitySource: "tpdb", EntityTitle: "Alice", Gender: "female"},
		{RowType: RowPerformer, EntityID: "p2", EntitySource: "tpdb", EntityTitle: "Bob", Gender: "male"},
		{RowType: RowPerformer, EntityID: "p3", EntitySource: "tpdb", EntityTitle: "Carol", Gender: "female"},
	}
	for _, r := range releases {
		if err := s.Insert(ctx, r); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	female, err := s.ListByGender(ctx, RowPerformer, "", "female", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(female) != 2 {
		t.Fatalf("expected 2 female performers, got %d: %+v", len(female), female)
	}
	for _, m := range female {
		if m.Gender != "female" {
			t.Errorf("expected only female rows, got gender %q for %q", m.Gender, m.EntityTitle)
		}
	}

	// Omitting the gender filter ("") behaves identically to List.
	all, err := s.ListByGender(ctx, RowPerformer, "", "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected ListByGender with empty gender to return all 3 rows, got %d", len(all))
	}
	plain, err := s.List(ctx, RowPerformer, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plain) != len(all) {
		t.Errorf("expected List and ListByGender(\"\") to agree, got %d vs %d", len(plain), len(all))
	}
}

func TestPurgeStale_RemovesOldEntitiesAndSeenReleases_KeepsRecent(t *testing.T) {
	s := newTestReleaseStore(t)
	ctx := context.Background()

	if err := s.Insert(ctx, MatchedRelease{RowType: RowScene, EntityID: "old", EntitySource: "tpdb", EntityTitle: "Old Scene"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Insert(ctx, MatchedRelease{RowType: RowScene, EntityID: "recent", EntitySource: "tpdb", EntityTitle: "Recent Scene"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.MarkSeen(ctx, "old-guid"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.MarkSeen(ctx, "recent-guid"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Backdate the "old" rows directly — Insert/MarkSeen always stamp "now",
	// so simulating age requires reaching past the store's own API (same
	// package, so s.db is accessible).
	old := time.Now().AddDate(0, -7, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE adult_newest_releases SET first_seen_at = ? WHERE entity_id = 'old'`, old); err != nil {
		t.Fatalf("backdating entity: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE adult_newest_seen SET seen_at = ? WHERE release_guid = 'old-guid'`, old); err != nil {
		t.Fatalf("backdating seen release: %v", err)
	}

	cutoff := time.Now().AddDate(0, -6, 0)
	removed, err := s.PurgeStale(ctx, cutoff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 matched-entity row removed, got %d", removed)
	}

	list, err := s.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].EntityID != "recent" {
		t.Errorf("expected only the recent entity to survive, got %+v", list)
	}

	seen, err := s.SeenGUIDs(ctx, []string{"old-guid", "recent-guid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen["old-guid"] {
		t.Errorf("expected old-guid to be purged from the seen table")
	}
	if !seen["recent-guid"] {
		t.Errorf("expected recent-guid to survive the purge")
	}
}
