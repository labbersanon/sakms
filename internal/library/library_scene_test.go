package library

import (
	"context"
	"errors"
	"testing"
)

func TestUpsertScene_CreatesThenUpdatesInPlace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-1", Title: "Some Scene", Studio: "Studio A", Date: "2024-01-02",
		FilePath: "/adult/Studio A - Some Scene (2024-01-02).mkv", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID == 0 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("expected id/timestamps populated, got %+v", created)
	}

	updated, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-1", Title: "Some Scene (Updated)", Studio: "Studio A", Date: "2024-01-02",
		FilePath: "/adult/Studio A - Some Scene (2024-01-02).mkv", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.ID != created.ID {
		t.Errorf("expected the same row to be updated (id %d), got id %d", created.ID, updated.ID)
	}
	if updated.Title != "Some Scene (Updated)" {
		t.Errorf("expected title to be updated, got %q", updated.Title)
	}

	all, err := s.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected upsert to replace, not duplicate — got %d rows", len(all))
	}
}

func TestUpsertScene_SameSceneIDDifferentBoxAreDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A StashDB match and a FansDB match can carry the same raw UUID — the
	// (box, scene_id) key must keep them as two separate tracked scenes.
	if _, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "same-uuid", Title: "A", RootFolderPath: "/adult"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.UpsertScene(ctx, Scene{Box: "fansdb", SceneID: "same-uuid", Title: "B", RootFolderPath: "/adult"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, err := s.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected the same uuid in two different boxes to be two rows, got %d", len(all))
	}
}

func TestGetScene_RoundTripAndNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.GetScene(ctx, "stashdb", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	created, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-2", Title: "Round Trip", Studio: "Studio B", Date: "2023-05-06",
		FilePath: "/adult/x.mkv", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.GetScene(ctx, "stashdb", "uuid-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != created.ID || got.Title != "Round Trip" || got.Studio != "Studio B" || got.Date != "2023-05-06" || got.FilePath != "/adult/x.mkv" {
		t.Errorf("expected the scene to round-trip, got %+v", got)
	}
}

func TestDeleteScene_RemovesTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	scene, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "uuid-3", Title: "Scene", RootFolderPath: "/adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.DeleteScene(ctx, scene.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.GetScene(ctx, "stashdb", "uuid-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected scene to be gone, got %v", err)
	}
	tags, err := s.SceneTags(ctx, scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected tags to be deleted with the scene, got %v", tags)
	}
}

func TestDeleteScene_NotFoundIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteScene(context.Background(), 999); err != nil {
		t.Fatalf("expected deleting a nonexistent id to be a no-op, got %v", err)
	}
}

func TestSceneTags_AddIsIdempotentAndRemoveWorks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	scene, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "uuid-4", Title: "Scene", RootFolderPath: "/adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("adding the same tag twice should be a no-op, got error: %v", err)
	}

	tags, err := s.SceneTags(ctx, scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 1 || tags[0] != "favorite" {
		t.Fatalf("expected exactly one tag, got %v", tags)
	}

	if err := s.RemoveSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.RemoveSceneTag(ctx, scene.ID, "not-there"); err != nil {
		t.Fatalf("removing a tag that isn't assigned should be a no-op, got error: %v", err)
	}

	tags, err = s.SceneTags(ctx, scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags after removal, got %v", tags)
	}
}

func TestSceneTagVocabulary_DistinctAcrossScenes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "uuid-5", Title: "A", RootFolderPath: "/adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "uuid-6", Title: "B", RootFolderPath: "/adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AddSceneTag(ctx, a.ID, "amateur"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSceneTag(ctx, b.ID, "amateur"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSceneTag(ctx, b.ID, "vintage"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vocab, err := s.SceneTagVocabulary(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vocab) != 2 || vocab[0] != "amateur" || vocab[1] != "vintage" {
		t.Fatalf("expected [amateur vintage], got %v", vocab)
	}
}

func TestUpsertScene_RoundTripsPHashIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-7", Title: "Cached Scene", RootFolderPath: "/adult",
		FilePath: "/adult/Cached Scene.mkv",
		PHash:    "phash64/5f:deadbeef", PHashFileSize: 12345, PHashFileMTime: "2026-07-10T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.GetScene(ctx, "stashdb", "uuid-7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != created.ID || got.PHash != "phash64/5f:deadbeef" || got.PHashFileSize != 12345 || got.PHashFileMTime != "2026-07-10T00:00:00Z" {
		t.Errorf("expected phash identity to round-trip, got %+v", got)
	}
}

func TestUpdateScenePHash_UpdatesInPlaceAndNoOpOnMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	scene, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-8", Title: "Scene", RootFolderPath: "/adult",
		FilePath: "/adult/Scene.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scene.PHash != "" {
		t.Fatalf("expected an uncached scene to start with an empty phash, got %q", scene.PHash)
	}

	if err := s.UpdateScenePHash(ctx, scene.ID, "phash64/5f:cafe", 999, "2026-07-10T12:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.GetScene(ctx, "stashdb", "uuid-8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "phash64/5f:cafe" || got.PHashFileSize != 999 || got.PHashFileMTime != "2026-07-10T12:00:00Z" {
		t.Errorf("expected UpdateScenePHash to persist the new hash + identity, got %+v", got)
	}
	// The targeted write must leave the rest of the row intact.
	if got.Title != "Scene" || got.FilePath != "/adult/Scene.mkv" {
		t.Errorf("expected UpdateScenePHash to leave other columns untouched, got %+v", got)
	}

	if err := s.UpdateScenePHash(ctx, 999999, "x", 1, "y"); err != nil {
		t.Errorf("expected updating a nonexistent id to be a no-op, got %v", err)
	}
}

func TestListScenes_EmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)
	got, err := s.ListScenes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Error("expected an empty slice, not nil, so it serializes as [] not null")
	}
}
