package api

// movemode_retire_test.go — the §4.2b half of the cross-mode move's
// safety-critical coverage (.omc/plans/autopilot-impl.md §7, "Additional
// tests" rows "§4.2b source-row retirement" and "step 11 rollback").
//
// §4.2a's zeroing stops Apply from deleting an unrelated file, but on its own
// it leaves the SOURCE mode's tracking row orphaned: the TrackedID == 0 branch
// in internal/dedup means "this file was never tracked", and after a bare
// zeroing that assertion is false. The move must therefore retire the source
// mode's tracking ROWS (never the files), inside MoveMode's own transaction.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// countRowsReferencingPath counts every library tracking row in ANY of the
// three mode-scoped tables that names path. It is how the "no dangling row
// pointing at a removed file" and "no double-tracked path" assertions in
// §4.2b's walkthrough are checked — both are statements about the whole
// library, not about one table, so a per-table lookup would miss exactly the
// cross-table drift the move can cause.
func countRowsReferencingPath(t *testing.T, db *sql.DB, path string) int {
	t.Helper()
	total := 0
	for _, q := range []string{
		`SELECT count(*) FROM library_items WHERE file_path = $1`,
		`SELECT count(*) FROM library_scenes WHERE file_path = $1`,
		`SELECT count(*) FROM library_episodes WHERE file_path = $1`,
	} {
		var n int
		if err := db.QueryRowContext(context.Background(), q, path).Scan(&n); err != nil {
			t.Fatalf("counting rows for %q: %v", path, err)
		}
		total += n
	}
	return total
}

// TestMoveMode_RetiresSourceLibraryRows covers §4.2b's retirement contract:
// after a move, (a) the source mode's tracking rows for the group's
// candidates are gone, (b) NO file was deleted by the move itself, and
// (c) after a subsequent Apply there is no dangling row pointing at a removed
// file and no double-tracked path.
//
// Both outcomes from §4.2b's walkthrough are covered, because they produce
// genuinely different final states:
//   - keep the RETIRED candidate as winner: the retired file survives Apply
//     and must end up tracked exactly ONCE, in the TARGET table (under a bare
//     zeroing it would be double-tracked — old library_scenes row plus a new
//     library_items row).
//   - keep the OTHER candidate as winner: the retired candidate's file is
//     os.Removed by Apply, so its source row must already be gone (under a
//     bare zeroing that row survives and now points at a deleted file).
func TestMoveMode_RetiresSourceLibraryRows(t *testing.T) {
	// seedAdultGroup builds the shared Adult→Movies fixture both keep-variants
	// use: candidate A is TRACKED by a real library_scenes row, candidate B is
	// an untracked orphan, and both files really exist on disk.
	seedAdultGroup := func(t *testing.T, ctx context.Context, propStore *proposals.Store, libStore *library.Store, root string) (id int64, fileA, fileB string, sceneID int64) {
		t.Helper()
		fileA = writeMoveModeFile(t, filepath.Join(root, "adult", "a.mp4"))
		fileB = writeMoveModeFile(t, filepath.Join(root, "adult", "b.mp4"))
		scene, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "retire-src", Title: "Retire Scene",
			FilePath: fileA, RootFolderPath: filepath.Join(root, "adult"),
		})
		if err != nil {
			t.Fatalf("seeding the source scene row: %v", err)
		}
		saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "Retire Scene", Title: "Retire Scene",
				RootFolderPath: filepath.Join(root, "adult"),
				GiveBackBox:    "stashdb", GiveBackSceneID: "retire-src",
				Candidates: []proposals.Candidate{
					{Label: "A-tracked", Path: fileA, TrackedID: int(scene.ID), Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
					{Label: "B-orphan", Path: fileB, Resolution: 720, Codec: "h264", BitRate: 2000},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		return saved[0].ID, fileA, fileB, scene.ID
	}

	t.Run("AdultSource_KeepTheRetiredCandidateAsWinner", func(t *testing.T) {
		connStore, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		id, fileA, fileB, _ := seedAdultGroup(t, ctx, propStore, libStore, root)

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":1234,"title":"Retired Movie"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		// (a) the source row is gone.
		if _, err := libStore.GetScene(ctx, "stashdb", "retire-src"); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("expected the source library_scenes row to be retired by the move, got %v", err)
		}
		// (b) the move deleted no FILES — retirement is rows-only.
		requireFileExists(t, fileA, "the move retires DB rows, never files")
		requireFileExists(t, fileB, "the move retires DB rows, never files")

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		keep := 0 // keep A — the candidate whose source row was just retired.
		if _, _, err := dedup.ApplyLibrary(ctx, libStore, *moved, &keep, nil, false, ""); err != nil {
			t.Fatalf("dedup.ApplyLibrary after the move: %v", err)
		}

		// (c) after Apply: A is tracked exactly ONCE (in the target table),
		// and nothing at all points at the removed loser.
		requireFileExists(t, fileA, "it is the keeper")
		requireFileGone(t, fileB, "it is the loser")
		if n := countRowsReferencingPath(t, connStore.DB(), fileA); n != 1 {
			t.Errorf("expected the kept file to be tracked by exactly 1 library row, got %d — a surviving source row plus the new target row is the double-tracking §4.2b describes", n)
		}
		if n := countRowsReferencingPath(t, connStore.DB(), fileB); n != 0 {
			t.Errorf("expected no library row to point at the removed loser, got %d", n)
		}
	})

	t.Run("AdultSource_KeepTheOtherCandidateAsWinner", func(t *testing.T) {
		connStore, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		id, fileA, fileB, _ := seedAdultGroup(t, ctx, propStore, libStore, root)

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":1234,"title":"Retired Movie"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		if _, err := libStore.GetScene(ctx, "stashdb", "retire-src"); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("expected the source library_scenes row to be retired by the move, got %v", err)
		}
		requireFileExists(t, fileA, "the move retires DB rows, never files")
		requireFileExists(t, fileB, "the move retires DB rows, never files")

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		keep := 1 // keep B — A (the retired candidate) is the loser this time.
		if _, _, err := dedup.ApplyLibrary(ctx, libStore, *moved, &keep, nil, false, ""); err != nil {
			t.Fatalf("dedup.ApplyLibrary after the move: %v", err)
		}

		requireFileGone(t, fileA, "the retired candidate is the loser in this variant")
		requireFileExists(t, fileB, "it is the keeper")
		if n := countRowsReferencingPath(t, connStore.DB(), fileA); n != 0 {
			t.Errorf("expected no library row to point at the removed file, got %d — that is exactly the dangling row §4.2b describes", n)
		}
		if n := countRowsReferencingPath(t, connStore.DB(), fileB); n != 1 {
			t.Errorf("expected the kept file to be tracked by exactly 1 library row, got %d", n)
		}
	})

	// MoviesSource covers the third branch of the handler's retirement switch,
	// `case mode.Movies: DeleteItemTx`. It is the only branch the Adult and
	// Series subtests around it cannot reach, and its absence is not benign:
	// with `case mode.Movies` a no-op, the source library_items row survives
	// pointing at a file Apply then os.Removes — a dangling row — and every
	// other test in this package still passes. Verified by a branch-scoped
	// mutation, not assumed.
	t.Run("MoviesSource_RetiresTheLibraryItemRow", func(t *testing.T) {
		connStore, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		fileA := writeMoveModeFile(t, filepath.Join(root, "movies", "a.mkv"))
		fileB := writeMoveModeFile(t, filepath.Join(root, "movies", "b.mkv"))
		item, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: 7070, Title: "Retire Movie",
			FilePath: fileA, RootFolderPath: filepath.Join(root, "movies"),
		})
		if err != nil {
			t.Fatalf("seeding the source movies row: %v", err)
		}
		saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "Retire Movie", Title: "Retire Movie",
				RootFolderPath: filepath.Join(root, "movies"), TMDBID: 7070,
				Candidates: []proposals.Candidate{
					{Label: "A-tracked", Path: fileA, TrackedID: int(item.ID), Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
					{Label: "B-orphan", Path: fileB, Resolution: 720, Codec: "h264", BitRate: 2000},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, saved[0].ID,
			`{"targetMode":"series","tmdbId":9090,"title":"Now A Show","seasonNumber":1,"episodeNumber":1}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		if _, err := libStore.Get(ctx, item.ID); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("expected the source library_items row to be retired by the move, got %v", err)
		}
		if n := countRowsReferencingPath(t, connStore.DB(), fileA); n != 0 {
			t.Errorf("expected nothing to still track the retired candidate's path, got %d row(s)", n)
		}
		requireFileExists(t, fileA, "the move retires DB rows, never files")
		requireFileExists(t, fileB, "the move retires DB rows, never files")
	})

	// SeriesSource is the direct regression test for the DeleteSeries cascade
	// trap (§4.2b's warning box). internal/library already proves
	// DeleteEpisodeTx itself deletes one row (TestDeleteEpisodeTx_
	// RemovesOnlyTheOneEpisode); the value ADDED here — do not delete this as
	// a duplicate — is that the HANDLER's `case mode.Series` retirement
	// dispatch calls DeleteEpisodeTx and not DeleteSeries. The fixture makes
	// the trap live: the episode's id equals the parent series' id, so a
	// DeleteSeries(episodeID) would wipe the whole series and its siblings.
	t.Run("SeriesSource_RetiresOneEpisodeNotTheSeries", func(t *testing.T) {
		_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 4040, Title: "A Show", RootFolderPath: filepath.Join(root, "tv")})
		if err != nil {
			t.Fatalf("seeding series: %v", err)
		}
		fileEp1 := writeMoveModeFile(t, filepath.Join(root, "tv", "s01e01.mkv"))
		fileEp2 := writeMoveModeFile(t, filepath.Join(root, "tv", "s01e02.mkv"))
		fileOrphan := writeMoveModeFile(t, filepath.Join(root, "tv", "s01e01-dup.mkv"))
		ep1, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: fileEp1})
		if err != nil {
			t.Fatalf("seeding episode 1: %v", err)
		}
		ep2, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: fileEp2})
		if err != nil {
			t.Fatalf("seeding episode 2: %v", err)
		}
		if ep1.ID != series.ID {
			t.Logf("note: episode id %d and series id %d differ in this run; the cascade trap is still covered by the sibling/parent assertions below", ep1.ID, series.ID)
		}

		saved, err := propStore.ReplacePending(ctx, mode.Series, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "A Show S01E01", Title: "A Show",
				RootFolderPath: filepath.Join(root, "tv"), TMDBID: 4040, SeasonNumber: 1, EpisodeNumber: 1,
				Candidates: []proposals.Candidate{
					{Label: "tracked-episode", Path: fileEp1, TrackedID: int(ep1.ID), Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
					{Label: "orphan", Path: fileOrphan, Resolution: 720, Codec: "h264", BitRate: 2000},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"movies","tmdbId":8080,"title":"Now A Movie"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		if _, err := libStore.GetEpisode(ctx, series.ID, 1, 1); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("expected the moved candidate's library_episodes row to be retired, got %v", err)
		}
		remaining, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			t.Fatalf("listing episodes: %v", err)
		}
		if len(remaining) != 1 || remaining[0].ID != ep2.ID {
			t.Fatalf("expected ONLY the sibling episode %d to survive — a DeleteSeries cascade would have taken it too; got %+v", ep2.ID, remaining)
		}
		if _, err := libStore.GetSeries(ctx, series.ID); err != nil {
			t.Fatalf("expected the parent library_series row to survive a single-episode retirement, got %v", err)
		}
		requireFileExists(t, fileEp1, "the move retires DB rows, never files")
		requireFileExists(t, fileEp2, "it was never part of the moved group")
	})

	// The Adult-source case with a REAL tag row. This case is mandatory and
	// its tag-creation step is not optional: library_scene_tags.scene_id ->
	// library_scenes(id) is ON DELETE NO ACTION (0001_baseline.sql:443), so a
	// DeleteSceneTx missing its tag delete only raises 23503 — turning the
	// intended 204 into a bare 500 — when a tag actually exists. Without
	// AddSceneTag below, this test ships green against a broken
	// implementation.
	t.Run("AdultSource_TaggedSceneRetiresWithoutA500", func(t *testing.T) {
		_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		fileA := writeMoveModeFile(t, filepath.Join(root, "adult", "tagged.mp4"))
		scene, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "tagged-src", Title: "Tagged Scene",
			FilePath: fileA, RootFolderPath: filepath.Join(root, "adult"),
		})
		if err != nil {
			t.Fatalf("seeding the source scene row: %v", err)
		}
		if err := libStore.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
			t.Fatalf("seeding the scene tag: %v", err)
		}
		if tags, err := libStore.SceneTags(ctx, scene.ID); err != nil || len(tags) != 1 {
			t.Fatalf("fixture setup failed: the scene must really have a tag before the move (tags=%v err=%v)", tags, err)
		}

		saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "tagged.mp4", SourcePath: fileA,
				RootFolderPath: filepath.Join(root, "adult"), Title: "Tagged Scene",
				GiveBackBox: "stashdb", GiveBackSceneID: "tagged-src",
				TrackedID: int(scene.ID),
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, filepath.Join(root, "movies")); err != nil {
			t.Fatalf("seeding movies root folder: %v", err)
		}

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"movies","tmdbId":2020,"title":"Now A Movie"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 (a 500 here means DeleteSceneTx hit the library_scene_tags 23503), got %d: %s", rec.Code, rec.Body.String())
		}

		if _, err := libStore.GetScene(ctx, "stashdb", "tagged-src"); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("expected the source library_scenes row to be retired, got %v", err)
		}
		tags, err := libStore.SceneTags(ctx, scene.ID)
		if err != nil {
			t.Fatalf("reading scene tags after the move: %v", err)
		}
		if len(tags) != 0 {
			t.Errorf("expected the scene's tag rows to be deleted with it, got %v", tags)
		}
	})
}

// TestMoveMode_ConflictRollsBackRetirement is the step-11 rollback proof: the
// retirement and the mode UPDATE are ONE transaction, not two correctly
// ordered steps. A conflict must therefore leave the source library rows
// fully intact, not destroy them and then report "the move failed".
//
// Reaching MoveMode's own conflict from the HANDLER needs care. Step 10.5's
// fast-path pre-check would 409 first — but it is scoped to
// `Rename && SourcePath != ""`, so a DEDUP proposal with a non-empty
// SourcePath skips it and the partial unique index
// proposals_live_source_path_uidx (mode, workflow, source_path) fires inside
// MoveMode's UPDATE instead. That is what the Conflict subtest does.
//
// "The rows are still present" is ALSO true if retirement simply never ran,
// so the Conflict case is only non-vacuous when paired with a success case
// built from the SAME fixture that proves retirement does happen. Both
// subtests below share sharedFixture for exactly that reason — do not split
// them apart.
func TestMoveMode_ConflictRollsBackRetirement(t *testing.T) {
	const sharedPath = "/media/shared/dupgroup.mp4"

	sharedFixture := func(t *testing.T, ctx context.Context, propStore *proposals.Store, libStore *library.Store, root string) (id int64, sceneID int64) {
		t.Helper()
		fileA := writeMoveModeFile(t, filepath.Join(root, "adult", "a.mp4"))
		fileB := writeMoveModeFile(t, filepath.Join(root, "adult", "b.mp4"))
		scene, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "rollback-src", Title: "Rollback Scene",
			FilePath: fileA, RootFolderPath: filepath.Join(root, "adult"),
		})
		if err != nil {
			t.Fatalf("seeding the source scene row: %v", err)
		}
		if err := libStore.AddSceneTag(ctx, scene.ID, "favorite"); err != nil {
			t.Fatalf("seeding the scene tag: %v", err)
		}
		saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "dupgroup.mp4", SourcePath: sharedPath,
				RootFolderPath: filepath.Join(root, "adult"), Title: "Rollback Scene",
				GiveBackBox: "stashdb", GiveBackSceneID: "rollback-src",
				Candidates: []proposals.Candidate{
					{Label: "A-tracked", Path: fileA, TrackedID: int(scene.ID), Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
					{Label: "B-orphan", Path: fileB, Resolution: 720, Codec: "h264", BitRate: 2000},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		return saved[0].ID, scene.ID
	}

	t.Run("Conflict409LeavesTheSourceRowsIntact", func(t *testing.T) {
		_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		id, sceneID := sharedFixture(t, ctx, propStore, libStore, root)

		// The live row already occupying (movies, dedup, sharedPath) — what
		// the partial unique index collides on when MoveMode writes
		// mode=movies onto the proposal above.
		if _, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "dupgroup.mp4", SourcePath: sharedPath,
				RootFolderPath: filepath.Join(root, "movies"), Title: "Occupying Group", TMDBID: 3,
			},
		}); err != nil {
			t.Fatalf("seeding the conflicting live proposal: %v", err)
		}

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":1234,"title":"Rollback Movie"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 from the conflicting move, got %d: %s", rec.Code, rec.Body.String())
		}

		if _, err := libStore.GetScene(ctx, "stashdb", "rollback-src"); err != nil {
			t.Fatalf("expected the source library_scenes row to SURVIVE a failed move — retirement must roll back with the UPDATE, got %v", err)
		}
		tags, err := libStore.SceneTags(ctx, sceneID)
		if err != nil {
			t.Fatalf("reading scene tags: %v", err)
		}
		if len(tags) != 1 {
			t.Errorf("expected the scene's tag row to survive the rolled-back retirement, got %v", tags)
		}
		got, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after the conflict: %v", err)
		}
		if got.Mode != mode.Adult {
			t.Errorf("expected the proposal's mode to be unchanged after a 409, got %q", got.Mode)
		}
	})

	t.Run("SameFixtureWithoutTheConflictDoesRetire", func(t *testing.T) {
		_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		id, _ := sharedFixture(t, ctx, propStore, libStore, root)

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":1234,"title":"Rollback Movie"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 without a conflicting row, got %d: %s", rec.Code, rec.Body.String())
		}
		if _, err := libStore.GetScene(ctx, "stashdb", "rollback-src"); !errors.Is(err, library.ErrNotFound) {
			t.Fatalf("expected THIS fixture to genuinely retire its source row when the move succeeds — without that, the sibling 409 subtest's assertion is vacuous; got %v", err)
		}
	})
}
