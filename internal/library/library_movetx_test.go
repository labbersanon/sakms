package library

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

// This file covers the three tx-scoped retirement methods added for the
// cross-mode "move to another section" feature's source-row retirement
// (.omc/plans/autopilot-impl.md §4.2b): DeleteItemTx, DeleteSceneTx, and
// DeleteEpisodeTx. All three exist specifically so Store.MoveMode (owned by
// a sibling wave) can retire a moved candidate's source-mode tracking row
// inside its own transaction — these tests prove (a) DeleteEpisodeTx never
// behaves like DeleteSeries, (b) DeleteSceneTx's tag delete actually fires
// when a tag exists, and (c) none of the three silently open their own
// transaction.

// TestDeleteEpisodeTx_RemovesOnlyTheOneEpisode is the direct regression
// test for the DeleteSeries cascade trap documented in §4.2b: a Series-
// sourced Dedup Candidate.TrackedID is an episode id, and passing it to
// DeleteSeries would cascade-delete an unrelated series' entire tracking
// history. This asserts the targeted episode is gone while its sibling
// episode AND the parent library_series row both survive.
func TestDeleteEpisodeTx_RemovesOnlyTheOneEpisode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 900, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep1, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/tv/s01e01.mkv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep2, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: "/tv/s01e02.mkv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.DeleteEpisodeTx(ctx, tx, ep1.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	remaining, err := s.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != ep2.ID {
		t.Fatalf("expected only sibling episode %d to remain, got %+v", ep2.ID, remaining)
	}

	if _, err := s.GetSeries(ctx, series.ID); err != nil {
		t.Fatalf("expected parent series to survive DeleteEpisodeTx, got %v", err)
	}
}

// TestDeleteSceneTx_RemovesTaggedSceneAndItsTags is the ON DELETE NO ACTION
// regression test called out in §4.2b: library_scene_tags.scene_id ->
// library_scenes(id) does NOT cascade. This test is a no-op / silently
// green even against a broken DeleteSceneTx unless a tag row genuinely
// exists first, so it explicitly creates one via AddSceneTag before
// deleting.
func TestDeleteSceneTx_RemovesTaggedSceneAndItsTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	scene, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "uuid-movetx-1", Title: "Scene", RootFolderPath: "/adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tagsBefore, err := s.SceneTags(ctx, scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tagsBefore) != 1 {
		t.Fatalf("fixture setup failed: expected the scene to have 1 tag before delete, got %v", tagsBefore)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.DeleteSceneTx(ctx, tx, scene.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.GetScene(ctx, "stashdb", "uuid-movetx-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected scene to be gone, got %v", err)
	}
	tagsAfter, err := s.SceneTags(ctx, scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tagsAfter) != 0 {
		t.Errorf("expected the tag row created by AddSceneTag to be deleted with the scene, got %v", tagsAfter)
	}
}

// TestRetirementTx_RollbackLeavesAllRowsUntouched proves DeleteItemTx,
// DeleteSceneTx, and DeleteEpisodeTx are genuinely scoped to the
// caller-supplied transaction and never open one of their own — the
// property §4.2b's design (MoveMode owning a single transaction around
// retirement + the mode UPDATE) depends on. If any of the three quietly
// committed its own work, this test would see the row gone despite the
// rollback.
func TestRetirementTx_RollbackLeavesAllRowsUntouched(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 901, Title: "Rollback Movie", Year: 2020,
		FilePath: "/movies/rollback.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	scene, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "uuid-movetx-rollback", Title: "Scene", RootFolderPath: "/adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 902, Title: "Rollback Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/tv/rollback.mkv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.DeleteItemTx(ctx, tx, item.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.DeleteSceneTx(ctx, tx, scene.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.DeleteEpisodeTx(ctx, tx, ep.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.Get(ctx, item.ID); err != nil {
		t.Fatalf("expected library item to survive rollback, got %v", err)
	}
	if _, err := s.GetScene(ctx, "stashdb", "uuid-movetx-rollback"); err != nil {
		t.Fatalf("expected scene to survive rollback, got %v", err)
	}
	tags, err := s.SceneTags(ctx, scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 1 {
		t.Errorf("expected the scene's tag to survive rollback, got %v", tags)
	}
	eps, err := s.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != ep.ID {
		t.Fatalf("expected episode to survive rollback, got %+v", eps)
	}
}
