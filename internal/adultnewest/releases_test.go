package adultnewest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/db"
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
