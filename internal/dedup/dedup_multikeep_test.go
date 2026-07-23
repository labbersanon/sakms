package dedup

import (
	"context"
	"os"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// The tests in this file cover Stage 1's multi-keep generalization of the Apply
// delete loops (additionalKeepIndices) across all three modes — the
// .omc/plans/dedup-ux-refine.md AC6/AC10/AC11/AC15 backend behavior. They
// exercise ApplyLibrary* directly (the delete-loop code under test lives here);
// the bulk-apply threading (AC13) and request validation (AC5-structural) are
// exercised through the HTTP handlers in internal/api instead, since that code
// lives there.

// TestApplyLibrary_MultiKeep_AdditionalKeeperSurvivesUntracked_Movies is AC10
// (Movies): an additionalKeepIndices entry's file survives Apply untouched AND
// no new library row is created for it — only the primary is tracked. A plain
// (non-kept) loser is still deleted, proving the OR'd skip is index-scoped, not
// a blanket "keep everything."
func TestApplyLibrary_MultiKeep_AdditionalKeeperSurvivesUntracked_Movies(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)
	keeperPath := writeVideoFile(t, dir, "additional-keeper.mkv", 10)
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	// All three candidates are untracked orphans of the same TMDB id.
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 7, RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, Winner: true},
			{Label: "keeper", Path: keeperPath},
			{Label: "loser", Path: loserPath},
		},
	}
	keep := 0
	id, changes, err := ApplyLibrary(ctx, libStore, p, &keep, []int{1}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a nonzero id for the newly tracked primary winner")
	}

	// Primary winner survives (never in changes), the additional keeper survives
	// UNTOUCHED, and only the plain loser is deleted.
	if _, err := os.Stat(winnerPath); err != nil {
		t.Errorf("expected the primary winner to survive, got %v", err)
	}
	if _, err := os.Stat(keeperPath); err != nil {
		t.Errorf("expected the additional keeper's file to survive untouched, got %v", err)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Errorf("expected the plain loser to be deleted, got %v", err)
	}
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for the loser %q, got %+v", loserPath, changes)
	}

	// Only the primary is tracked — no library row exists for the additional
	// keeper's path (matching keepAll's kept-but-untracked behavior).
	items, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly one tracked item (the primary), got %d: %+v", len(items), items)
	}
	if items[0].FilePath != winnerPath {
		t.Errorf("expected the one tracked item to be the primary winner %q, got %+v", winnerPath, items[0])
	}
	for _, it := range items {
		if it.FilePath == keeperPath {
			t.Errorf("expected NO library row for the additional keeper %q, but one exists: %+v", keeperPath, it)
		}
	}
}

// TestApplyLibrarySeries_MultiKeep_AdditionalKeeperSurvivesUntracked is AC10
// (Series): identical guarantee for the episode-keyed path — the additional
// keeper's file survives and gets no episode row, only the primary is tracked.
func TestApplyLibrarySeries_MultiKeep_AdditionalKeeperSurvivesUntracked(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)
	keeperPath := writeVideoFile(t, dir, "additional-keeper.mkv", 10)
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show", TMDBID: 9, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, Winner: true},
			{Label: "keeper", Path: keeperPath},
			{Label: "loser", Path: loserPath},
		},
	}
	keep := 0
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, &keep, []int{1}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a nonzero episode id for the newly tracked primary winner")
	}

	if _, err := os.Stat(winnerPath); err != nil {
		t.Errorf("expected the primary winner to survive, got %v", err)
	}
	if _, err := os.Stat(keeperPath); err != nil {
		t.Errorf("expected the additional keeper's file to survive untouched, got %v", err)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Errorf("expected the plain loser to be deleted, got %v", err)
	}
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for the loser %q, got %+v", loserPath, changes)
	}

	// Exactly one series with exactly one episode row (the primary) — no row for
	// the additional keeper.
	allSeries, err := libStore.ListSeries(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(allSeries) != 1 {
		t.Fatalf("expected exactly one series, got %d: %+v", len(allSeries), allSeries)
	}
	episodes, err := libStore.ListEpisodes(ctx, allSeries[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("expected exactly one episode row (the primary), got %d: %+v", len(episodes), episodes)
	}
	if episodes[0].FilePath != winnerPath {
		t.Errorf("expected the one episode row to be the primary winner %q, got %+v", winnerPath, episodes[0])
	}
}

// TestApplyLibraryAdult_MultiKeep_AlreadyTrackedAdditionalKeeperStaysTracked is
// AC10's Adult variant: an Adult dedup group shares ONE (box, scene_id) and
// library_scenes is UNIQUE on it, so at most one candidate is tracked. When the
// operator keeps that already-tracked candidate as an ADDITIONAL keeper and
// picks an untracked orphan as the primary, the tracked scene's ROW must
// persist (it is not deleted) and its file must survive on disk.
//
// Note on identity vs path: the primary winner is untracked, so Apply's
// UpsertScene runs against the SAME (box, scene_id) and, via
// ON CONFLICT(box, scene_id), UPDATEs that existing row's file_path to the
// winner's path — so "stays tracked" means the scene ROW/identity persists
// (GetScene still succeeds, same id), NOT that the row keeps pointing at the
// additional keeper's own path. The additional keeper's FILE is what survives
// untouched on disk; its tracking rides through the on-conflict update of the
// shared identity's single row.
func TestApplyLibraryAdult_MultiKeep_AlreadyTrackedAdditionalKeeperStaysTracked(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)
	trackedKeeperPath := writeVideoFile(t, dir, "tracked-keeper.mkv", 10)
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	tracked, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "Some Scene", Studio: "Studio",
		FilePath: trackedKeeperPath, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene", Studio: "Studio",
		GiveBackBox: "stashdb", GiveBackSceneID: sceneUUIDA, RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, Winner: true},
			{Label: "tracked-keeper", Path: trackedKeeperPath, TrackedID: int(tracked.ID)},
			{Label: "loser", Path: loserPath},
		},
	}
	keep := 0
	id, changes, err := ApplyLibraryAdult(ctx, libStore, p, &keep, []int{1}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The already-tracked additional keeper's FILE survives untouched (skipped
	// by the delete loop), the winner survives, and only the plain loser is
	// deleted.
	if _, err := os.Stat(trackedKeeperPath); err != nil {
		t.Errorf("expected the tracked additional keeper's file to survive untouched, got %v", err)
	}
	if _, err := os.Stat(winnerPath); err != nil {
		t.Errorf("expected the primary winner to survive, got %v", err)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Errorf("expected the plain loser to be deleted, got %v", err)
	}
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for the loser %q, got %+v", loserPath, changes)
	}

	// The scene's identity/row PERSISTS — it was never deleted, and the winner's
	// UpsertScene updated the same row (ON CONFLICT), so GetScene still succeeds
	// and the returned id is the original tracked row's id.
	scene, err := libStore.GetScene(ctx, "stashdb", sceneUUIDA)
	if err != nil {
		t.Fatalf("expected the tracked scene identity to still exist, got err=%v", err)
	}
	if scene.ID != tracked.ID {
		t.Errorf("expected the persisted scene row to keep its original id %d, got %d", tracked.ID, scene.ID)
	}
	if id != tracked.ID {
		t.Errorf("expected Apply to return the persisted scene's id %d, got %d", tracked.ID, id)
	}

	// Exactly one library_scenes row for the shared identity (never a duplicate).
	all, err := libStore.ListScenes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one tracked scene row, got %d: %+v", len(all), all)
	}
}

// TestApplyLibrarySeries_MultiKeep_ComposesWithSharedFileGuard is AC11: the
// Series shared-file guard (CountEpisodesByFilePath) and multi-keep
// (additionalKeepIndices) both feed the SAME OR'd skip-delete condition, so
// they must compose — a candidate is left on disk if it is the primary, an
// additional keeper, OR still referenced by another episode's row. This group
// exercises all three skip reasons plus one genuine delete in a single Apply.
func TestApplyLibrarySeries_MultiKeep_ComposesWithSharedFileGuard(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)
	additionalKeeper := writeVideoFile(t, dir, "additional-keeper.mkv", 10)
	sharedFile := writeVideoFile(t, dir, "Show.S01E01-E02.mkv", 10)
	plainLoser := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Episode 1 (the dedup group's key) and episode 2 both point at the shared
	// split file — so the shared file's refCount is 2 and the guard must protect
	// it even though it loses episode 1's comparison.
	ep1, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Part One", FilePath: sharedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep2Before, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part Two", AirDate: "2020-01-08", FilePath: sharedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Episode 1's dedup group: index0 primary winner, index1 additional keeper,
	// index2 the shared file (tracked loser, guarded), index3 a plain loser.
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, Winner: true},
			{Label: "keeper", Path: additionalKeeper},
			{Label: "tracked-shared", Path: sharedFile, TrackedID: int(ep1.ID)},
			{Label: "loser", Path: plainLoser},
		},
	}
	keep := 0
	_, changes, err := ApplyLibrarySeries(ctx, libStore, p, &keep, []int{1}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Three files survive for three distinct skip reasons; only the plain loser
	// is deleted.
	if _, err := os.Stat(winnerPath); err != nil {
		t.Errorf("expected the primary winner to survive, got %v", err)
	}
	if _, err := os.Stat(additionalKeeper); err != nil {
		t.Errorf("expected the additional keeper to survive (multi-keep skip), got %v", err)
	}
	if _, err := os.Stat(sharedFile); err != nil {
		t.Errorf("expected the shared file to survive (shared-file guard skip), got %v", err)
	}
	if _, err := os.Stat(plainLoser); !os.IsNotExist(err) {
		t.Errorf("expected the plain loser to be deleted, got %v", err)
	}
	if len(changes) != 1 || changes[0].Path != plainLoser || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for the plain loser %q, got %+v", plainLoser, changes)
	}
	// The guarded shared file must never appear as a Deleted change.
	for _, c := range changes {
		if c.Path == sharedFile {
			t.Errorf("expected no PathChange for the still-referenced shared file, got %+v", changes)
		}
	}

	// Episode 2's row is completely untouched — still points at the shared file.
	ep2After, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep2After.ID != ep2Before.ID || ep2After.FilePath != sharedFile || ep2After.Title != "Part Two" {
		t.Errorf("expected episode 2's row untouched, got %+v (was %+v)", ep2After, ep2Before)
	}
	// Episode 1's own resolution still lands on the winner.
	ep1After, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep1After.FilePath != winnerPath {
		t.Errorf("expected episode 1's row updated to the winner path, got %+v", ep1After)
	}
}

// TestApplyLibrary_KeepAllVsCheckAll_DifferentUpsertOutcomes is AC15: "Keep All"
// and "check every box" are distinct actions with documented different Upsert
// outcomes. Using an all-untracked group so no early-return-on-tracked-id muddies
// the comparison: Keep All tracks NOTHING (no row), while check-all-boxes (a
// primary plus every other index as an additional keeper) tracks the PRIMARY
// (exactly one row). Neither deletes anything.
func TestApplyLibrary_KeepAllVsCheckAll_DifferentUpsertOutcomes(t *testing.T) {
	ctx := context.Background()

	// --- Keep All: tracks nothing, deletes nothing. ---
	t.Run("KeepAll tracks nothing", func(t *testing.T) {
		dir := t.TempDir()
		a := writeVideoFile(t, dir, "a.mkv", 10)
		b := writeVideoFile(t, dir, "b.mkv", 10)
		libStore := newTestLibraryStore(t)

		p := proposals.Proposal{
			ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 11, RootFolderPath: dir,
			Candidates: []proposals.Candidate{
				{Label: "a", Path: a, Winner: true},
				{Label: "b", Path: b},
			},
		}
		id, changes, err := ApplyLibrary(ctx, libStore, p, nil, nil, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != 0 {
			t.Errorf("expected keepAll on an all-untracked group to track nothing (id 0), got %d", id)
		}
		if len(changes) != 0 {
			t.Errorf("expected keepAll to delete nothing, got %+v", changes)
		}
		if _, err := os.Stat(a); err != nil {
			t.Errorf("expected file a to survive, got %v", err)
		}
		if _, err := os.Stat(b); err != nil {
			t.Errorf("expected file b to survive, got %v", err)
		}
		items, err := libStore.List(ctx, mode.Movies)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("expected keepAll to create NO library row, got %d: %+v", len(items), items)
		}
	})

	// --- Check every box: tracks the primary, deletes nothing. ---
	t.Run("check-all tracks the primary", func(t *testing.T) {
		dir := t.TempDir()
		a := writeVideoFile(t, dir, "a.mkv", 10)
		b := writeVideoFile(t, dir, "b.mkv", 10)
		libStore := newTestLibraryStore(t)

		p := proposals.Proposal{
			ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 11, RootFolderPath: dir,
			Candidates: []proposals.Candidate{
				{Label: "a", Path: a, Winner: true},
				{Label: "b", Path: b},
			},
		}
		keep := 0
		id, changes, err := ApplyLibrary(ctx, libStore, p, &keep, []int{1}, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id == 0 {
			t.Error("expected check-all to track the primary (nonzero id), got 0")
		}
		if len(changes) != 0 {
			t.Errorf("expected check-all to delete nothing, got %+v", changes)
		}
		if _, err := os.Stat(a); err != nil {
			t.Errorf("expected file a to survive, got %v", err)
		}
		if _, err := os.Stat(b); err != nil {
			t.Errorf("expected file b to survive, got %v", err)
		}
		items, err := libStore.List(ctx, mode.Movies)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("expected check-all to track exactly the primary (1 row), got %d: %+v", len(items), items)
		}
		if items[0].FilePath != a {
			t.Errorf("expected the one tracked row to be the primary %q, got %+v", a, items[0])
		}
	})
}
