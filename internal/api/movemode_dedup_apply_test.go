package api

// movemode_dedup_apply_test.go — the §4.2a half of the cross-mode move's
// safety-critical coverage (.omc/plans/autopilot-impl.md §7, "Additional
// tests" rows "§4.2a TrackedID (CRITICAL)", "§4.2a winner path", and
// "→Series direction"). Every test here goes MOVE → APPLY, because the
// defect these guard against is invisible at the move itself: the move
// writes a perfectly reasonable-looking row, and the damage lands later,
// inside internal/dedup, when a mode-scoped foreign key is dereferenced
// against the WRONG table.
//
// Apply is driven by calling internal/dedup's ApplyLibrary* directly rather
// than through POST /api/proposals/{id}/apply. That is deliberate: it is the
// exact function applyByWorkflow dispatches to (internal/api/proposals.go
// :430-443), it exercises the real TrackedID branches and the real winner
// early-return, and it returns the resulting tracked id — which is itself an
// assertion target for the winner-registration tests below.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// postMoveMode invokes moveProposalModeHandler the way the mux does, with the
// {id} path value set. Shared by every move-mode test file in this package.
func postMoveMode(t *testing.T, h http.HandlerFunc, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/proposals/"+strconv.FormatInt(id, 10)+"/move-mode", strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	h(rec, req)
	return rec
}

// writeMoveModeFile creates path (and its parents) with some bytes in it, so
// an os.Remove during Apply is a real filesystem mutation this test can
// observe. The existing move-mode smoke tests use /media/... string literals
// that never touch disk; every test in this file Applies, so it needs real
// files.
func writeMoveModeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("video bytes"), 0o644); err != nil {
		t.Fatalf("writing %q: %v", path, err)
	}
	return path
}

func requireFileExists(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to still exist (%s), stat: %v", path, why, err)
	}
}

func requireFileGone(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s to have been removed (%s), but it is still there", path, why)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat %s: %v", path, err)
	}
}

// TestMoveMode_ZeroesCandidateTrackedIDs is the §4.2a CRITICAL invariant at
// the HANDLER level. internal/proposals has a same-named store-level test,
// and this is NOT a duplicate of it: that one hands MoveMode a candidate
// slice the TEST already zeroed, so it proves the store persists a zeroed
// slice. This one proves the thing that actually ships — that
// moveProposalModeHandler itself builds the zeroed copy (step 10.6) — while
// every other candidate field and the ordering survive byte-identically.
func TestMoveMode_ZeroesCandidateTrackedIDs(t *testing.T) {
	_, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Dup Group", Title: "Dup Group",
			RootFolderPath: "/media/adult",
			TrackedID:      42,
			Candidates: []proposals.Candidate{
				{Label: "tracked", Path: "/media/adult/a.mp4", TrackedID: 42, Resolution: 1080, Codec: "h264", BitRate: 5000, PHash: "p1", Winner: true},
				{Label: "orphan", Path: "/media/adult/b.mp4", TrackedID: 0, Resolution: 720, Codec: "av1", BitRate: 2500, PHash: "p2", Winner: false},
				{Label: "tracked-too", Path: "/media/adult/c.mp4", TrackedID: 99, Resolution: 480, Codec: "hevc", BitRate: 1200, PHash: "p3", Winner: false},
			},
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID
	original := saved[0].Candidates

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":7,"title":"Dup Group Movie"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.TrackedID != 0 {
		t.Errorf("expected the top-level tracked_id to be zeroed by the move, got %d", got.TrackedID)
	}
	if len(got.Candidates) != len(original) {
		t.Fatalf("expected %d candidates to survive the move, got %d: %+v", len(original), len(got.Candidates), got.Candidates)
	}
	for i, c := range got.Candidates {
		want := original[i]
		if c.TrackedID != 0 {
			t.Errorf("candidate %d (%s): expected TrackedID zeroed by the handler, got %d", i, c.Label, c.TrackedID)
		}
		if c.Label != want.Label || c.Path != want.Path || c.Resolution != want.Resolution ||
			c.Codec != want.Codec || c.BitRate != want.BitRate || c.PHash != want.PHash || c.Winner != want.Winner {
			t.Errorf("candidate %d: file metadata/ordering changed across the move — got %+v, want (same but TrackedID=0) %+v", i, c, want)
		}
	}
}

// TestMoveMode_DedupGroupMoveThenApply_DeletesOnlyItsOwnFiles is THE
// regression test for the delete-an-unrelated-file defect (§4.2a).
//
// library_items, library_episodes and library_scenes are three INDEPENDENT
// identity sequences, so the same integer names a different, unrelated row in
// each. Both directions below therefore build a group whose candidate
// TrackedID numerically COLLIDES with a real pre-existing row in the TARGET
// mode's table — the fixture asserts the collision explicitly, because
// without it the test proves nothing.
//
// The two directions fail DIFFERENTLY under the bug, and both assertions are
// kept deliberately:
//   - Adult→Movies: dedup.removeLibraryCandidate resolves the id through
//     libStore.Get and os.Removes THAT row's FilePath — an unrelated movie
//     file is destroyed on disk.
//   - Movies→Adult: dedup.removeSceneCandidate removes c.Path (its own file,
//     so the file check cannot discriminate here) and then DeleteScene(id) —
//     an unrelated scene's tracking ROW is destroyed. The row assertion is
//     the discriminator in that direction.
func TestMoveMode_DedupGroupMoveThenApply_DeletesOnlyItsOwnFiles(t *testing.T) {
	t.Run("AdultToMovies", func(t *testing.T) {
		_, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		// The unrelated, pre-existing MOVIES row + its file — the collateral
		// damage under the bug. Created first so it takes id 1 in
		// library_items.
		bystanderFile := writeMoveModeFile(t, filepath.Join(root, "movies", "Unrelated Movie.mkv"))
		bystander, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: 555, Title: "Unrelated Movie",
			FilePath: bystanderFile, RootFolderPath: filepath.Join(root, "movies"),
		})
		if err != nil {
			t.Fatalf("seeding the bystander movies row: %v", err)
		}

		// The Adult group's own files, and the SOURCE-mode tracking row for
		// the loser. First library_scenes row, so it takes id 1 too.
		loserFile := writeMoveModeFile(t, filepath.Join(root, "adult", "group-loser.mp4"))
		winnerFile := writeMoveModeFile(t, filepath.Join(root, "adult", "group-winner.mp4"))
		sourceScene, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "group-src-1", Title: "Group Scene",
			FilePath: loserFile, RootFolderPath: filepath.Join(root, "adult"),
		})
		if err != nil {
			t.Fatalf("seeding the source adult scene row: %v", err)
		}
		if sourceScene.ID != bystander.ID {
			t.Fatalf("fixture: the source scene id (%d) and the target-mode item id (%d) did not collide — this test proves nothing unless they do", sourceScene.ID, bystander.ID)
		}

		saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "Group Scene", Title: "Group Scene",
				RootFolderPath: filepath.Join(root, "adult"),
				GiveBackBox:    "stashdb", GiveBackSceneID: "group-src-1",
				Candidates: []proposals.Candidate{
					{Label: "loser", Path: loserFile, TrackedID: int(sourceScene.ID), Resolution: 720, Codec: "h264", BitRate: 2000},
					{Label: "winner", Path: winnerFile, Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		id := saved[0].ID

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		// tmdbId 777 deliberately differs from the bystander's 555:
		// library_items is keyed ON CONFLICT(mode, tmdb_id), so a shared id
		// would make the winner's Upsert MUTATE the bystander row instead of
		// creating a new one, corrupting both assertions below.
		rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":777,"title":"Moved Group"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		if _, _, err := dedup.ApplyLibrary(ctx, libStore, *moved, nil, nil, false, ""); err != nil {
			t.Fatalf("dedup.ApplyLibrary after the move: %v", err)
		}

		// (a) only the group's OWN candidate file was removed.
		requireFileGone(t, loserFile, "it is the group's own losing candidate")
		requireFileExists(t, winnerFile, "it is the group's winner")
		// (b) the colliding target-mode row and ITS file are untouched.
		requireFileExists(t, bystanderFile, "it belongs to an unrelated movies row that merely shares an id with the moved group's candidate")
		still, err := libStore.Get(ctx, bystander.ID)
		if err != nil {
			t.Fatalf("expected the colliding library_items row %d to survive the move+apply, got %v", bystander.ID, err)
		}
		if still.FilePath != bystanderFile || still.Title != "Unrelated Movie" {
			t.Errorf("the colliding library_items row was mutated: %+v", still)
		}
	})

	t.Run("MoviesToAdult", func(t *testing.T) {
		_, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, filepath.Join(root, "adult")); err != nil {
			t.Fatalf("seeding adult root folder: %v", err)
		}

		// The unrelated, pre-existing ADULT row + its file. Its (box,
		// sceneId) deliberately differs from the move request's, or step 9's
		// D-2 already-tracked check would 409 before Apply is ever reached.
		bystanderFile := writeMoveModeFile(t, filepath.Join(root, "adult", "Unrelated Scene.mp4"))
		bystander, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "bystander-scene", Title: "Unrelated Scene",
			FilePath: bystanderFile, RootFolderPath: filepath.Join(root, "adult"),
		})
		if err != nil {
			t.Fatalf("seeding the bystander scene row: %v", err)
		}

		loserFile := writeMoveModeFile(t, filepath.Join(root, "movies", "group-loser.mkv"))
		winnerFile := writeMoveModeFile(t, filepath.Join(root, "movies", "group-winner.mkv"))
		sourceItem, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: 900, Title: "Group Movie",
			FilePath: loserFile, RootFolderPath: filepath.Join(root, "movies"),
		})
		if err != nil {
			t.Fatalf("seeding the source movies row: %v", err)
		}
		if sourceItem.ID != bystander.ID {
			t.Fatalf("fixture: the source item id (%d) and the target-mode scene id (%d) did not collide — this test proves nothing unless they do", sourceItem.ID, bystander.ID)
		}

		saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "Group Movie", Title: "Group Movie",
				RootFolderPath: filepath.Join(root, "movies"), TMDBID: 900,
				Candidates: []proposals.Candidate{
					{Label: "loser", Path: loserFile, TrackedID: int(sourceItem.ID), Resolution: 720, Codec: "h264", BitRate: 2000},
					{Label: "winner", Path: winnerFile, Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		id := saved[0].ID

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"adult","box":"stashdb","sceneId":"moved-scene-1","title":"Moved Scene","studio":"S","date":"2020-01-01"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		if _, _, err := dedup.ApplyLibraryAdult(ctx, libStore, *moved, nil, nil, false, ""); err != nil {
			t.Fatalf("dedup.ApplyLibraryAdult after the move: %v", err)
		}

		requireFileGone(t, loserFile, "it is the group's own losing candidate")
		requireFileExists(t, winnerFile, "it is the group's winner")
		requireFileExists(t, bystanderFile, "it belongs to an unrelated scene row that merely shares an id with the moved group's candidate")
		// The discriminating assertion for this direction: under the bug
		// DeleteScene(TrackedID) destroys this row.
		if _, err := libStore.GetScene(ctx, "stashdb", "bystander-scene"); err != nil {
			t.Fatalf("expected the colliding library_scenes row %d to survive the move+apply, got %v", bystander.ID, err)
		}
	})
}

// TestMoveMode_DedupWinnerRegisteredInTargetTable is the OTHER half of the
// §4.2a defect. A non-zero winner TrackedID hits an EARLY RETURN in the dedup
// apply code (internal/dedup/dedup.go:245 for Movies, :408 for Series,
// internal/dedup/dedup_adult_library.go:305 for Adult) — the winner is never
// registered in the target mode's table, and markAppliedResilient then stamps
// the stale cross-table id onto the proposal. With TrackedID zeroed by the
// move, the winner falls through to the target table's Upsert/UpsertScene
// instead, which is what these assert.
//
// Each direction seeds a filler row in the TARGET table first so the new
// row's id cannot coincidentally equal the pre-move id — otherwise
// "a NEW tracked id" would be unfalsifiable.
func TestMoveMode_DedupWinnerRegisteredInTargetTable(t *testing.T) {
	t.Run("AdultToMovies", func(t *testing.T) {
		_, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		// Filler, so the next library_items id is 2 while the source scene
		// id is 1 — a genuinely different number.
		if _, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: 1, Title: "Filler",
			FilePath:       writeMoveModeFile(t, filepath.Join(root, "movies", "filler.mkv")),
			RootFolderPath: filepath.Join(root, "movies"),
		}); err != nil {
			t.Fatalf("seeding filler movies row: %v", err)
		}

		winnerFile := writeMoveModeFile(t, filepath.Join(root, "adult", "winner.mp4"))
		loserFile := writeMoveModeFile(t, filepath.Join(root, "adult", "loser.mp4"))
		sourceScene, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "winner-src", Title: "Winner Scene",
			FilePath: winnerFile, RootFolderPath: filepath.Join(root, "adult"),
		})
		if err != nil {
			t.Fatalf("seeding the source scene row: %v", err)
		}

		saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "Winner Scene", Title: "Winner Scene",
				RootFolderPath: filepath.Join(root, "adult"),
				GiveBackBox:    "stashdb", GiveBackSceneID: "winner-src",
				Candidates: []proposals.Candidate{
					{Label: "winner", Path: winnerFile, TrackedID: int(sourceScene.ID), Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
					{Label: "loser", Path: loserFile, Resolution: 720, Codec: "h264", BitRate: 2000},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		id := saved[0].ID

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"movies","tmdbId":4242,"title":"Now A Movie"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		itemID, _, err := dedup.ApplyLibrary(ctx, libStore, *moved, nil, nil, false, "")
		if err != nil {
			t.Fatalf("dedup.ApplyLibrary after the move: %v", err)
		}

		if itemID == sourceScene.ID {
			t.Fatalf("Apply returned the PRE-MOVE tracked id %d — the winner hit dedup.go's non-zero-TrackedID early return and was never registered in library_items", itemID)
		}
		registered, err := libStore.Get(ctx, itemID)
		if err != nil {
			t.Fatalf("expected a NEW library_items row for the winner, got %v", err)
		}
		if registered.FilePath != winnerFile {
			t.Errorf("expected the new library_items row to point at the winner's file %q, got %q", winnerFile, registered.FilePath)
		}
		if registered.TMDBID != 4242 || registered.Mode != mode.Movies {
			t.Errorf("expected the new row to carry the TARGET mode's identity (movies/4242), got mode=%q tmdbId=%d", registered.Mode, registered.TMDBID)
		}
	})

	t.Run("MoviesToAdult", func(t *testing.T) {
		_, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()

		if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, filepath.Join(root, "adult")); err != nil {
			t.Fatalf("seeding adult root folder: %v", err)
		}
		// Filler so the next library_scenes id differs from the source item id.
		if _, err := libStore.UpsertScene(ctx, library.Scene{
			Box: "stashdb", SceneID: "filler-scene", Title: "Filler",
			FilePath:       writeMoveModeFile(t, filepath.Join(root, "adult", "filler.mp4")),
			RootFolderPath: filepath.Join(root, "adult"),
		}); err != nil {
			t.Fatalf("seeding filler scene row: %v", err)
		}

		winnerFile := writeMoveModeFile(t, filepath.Join(root, "movies", "winner.mkv"))
		loserFile := writeMoveModeFile(t, filepath.Join(root, "movies", "loser.mkv"))
		sourceItem, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: 321, Title: "Winner Movie",
			FilePath: winnerFile, RootFolderPath: filepath.Join(root, "movies"),
		})
		if err != nil {
			t.Fatalf("seeding the source movies row: %v", err)
		}

		saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: "Winner Movie", Title: "Winner Movie",
				RootFolderPath: filepath.Join(root, "movies"), TMDBID: 321,
				Candidates: []proposals.Candidate{
					{Label: "winner", Path: winnerFile, TrackedID: int(sourceItem.ID), Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
					{Label: "loser", Path: loserFile, Resolution: 720, Codec: "h264", BitRate: 2000},
				},
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		id := saved[0].ID

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id, `{"targetMode":"adult","box":"stashdb","sceneId":"now-a-scene","title":"Now A Scene","studio":"Studio","date":"2021-02-03"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		sceneID, _, err := dedup.ApplyLibraryAdult(ctx, libStore, *moved, nil, nil, false, "")
		if err != nil {
			t.Fatalf("dedup.ApplyLibraryAdult after the move: %v", err)
		}

		if sceneID == sourceItem.ID {
			t.Fatalf("Apply returned the PRE-MOVE tracked id %d — the winner hit dedup_adult_library.go's non-zero-TrackedID early return and was never registered in library_scenes", sceneID)
		}
		registered, err := libStore.GetScene(ctx, "stashdb", "now-a-scene")
		if err != nil {
			t.Fatalf("expected a NEW library_scenes row for the winner at the target identity, got %v", err)
		}
		if registered.ID != sceneID {
			t.Errorf("expected Apply to return the new scene row's id %d, got %d", registered.ID, sceneID)
		}
		if registered.FilePath != winnerFile {
			t.Errorf("expected the new library_scenes row to point at the winner's file %q, got %q", winnerFile, registered.FilePath)
		}
	})
}

// TestMoveMode_DedupMoveToSeries_WinnerRegistered is the ONLY coverage the
// →Series direction gets, and the plan says so explicitly (§7, "→Series
// direction (currently uncovered)"). Series Dedup removes losers BY PATH via
// CountEpisodesByFilePath (internal/dedup/dedup.go:389), never via TrackedID,
// so every loser-side assertion in this file is a structural no-op there —
// the winner early-return at dedup.go:408 is the only way a stale TrackedID
// can manifest for a →Series move. Without this test the whole direction
// ships untested.
func TestMoveMode_DedupMoveToSeries_WinnerRegistered(t *testing.T) {
	cases := []struct {
		name       string
		sourceMode mode.Mode
		// seed returns the winner's file, the loser's file, and the winner's
		// SOURCE-mode tracked id.
		seed func(t *testing.T, ctx context.Context, libStore *library.Store, root string) (string, string, int)
		prop func(winner, loser string, trackedID int, root string) proposals.Proposal
	}{
		{
			name: "MoviesToSeries", sourceMode: mode.Movies,
			seed: func(t *testing.T, ctx context.Context, libStore *library.Store, root string) (string, string, int) {
				winner := writeMoveModeFile(t, filepath.Join(root, "movies", "winner.mkv"))
				loser := writeMoveModeFile(t, filepath.Join(root, "movies", "loser.mkv"))
				item, err := libStore.Upsert(ctx, library.Item{
					Mode: mode.Movies, TMDBID: 111, Title: "Was A Movie",
					FilePath: winner, RootFolderPath: filepath.Join(root, "movies"),
				})
				if err != nil {
					t.Fatalf("seeding the source movies row: %v", err)
				}
				return winner, loser, int(item.ID)
			},
			prop: func(winner, loser string, trackedID int, root string) proposals.Proposal {
				return proposals.Proposal{
					Status: proposals.Pending, SourceName: "Was A Movie", Title: "Was A Movie",
					RootFolderPath: filepath.Join(root, "movies"), TMDBID: 111,
					Candidates: []proposals.Candidate{
						{Label: "winner", Path: winner, TrackedID: trackedID, Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
						{Label: "loser", Path: loser, Resolution: 720, Codec: "h264", BitRate: 2000},
					},
				}
			},
		},
		{
			name: "AdultToSeries", sourceMode: mode.Adult,
			seed: func(t *testing.T, ctx context.Context, libStore *library.Store, root string) (string, string, int) {
				winner := writeMoveModeFile(t, filepath.Join(root, "adult", "winner.mp4"))
				loser := writeMoveModeFile(t, filepath.Join(root, "adult", "loser.mp4"))
				scene, err := libStore.UpsertScene(ctx, library.Scene{
					Box: "stashdb", SceneID: "was-a-scene", Title: "Was A Scene",
					FilePath: winner, RootFolderPath: filepath.Join(root, "adult"),
				})
				if err != nil {
					t.Fatalf("seeding the source scene row: %v", err)
				}
				return winner, loser, int(scene.ID)
			},
			prop: func(winner, loser string, trackedID int, root string) proposals.Proposal {
				return proposals.Proposal{
					Status: proposals.Pending, SourceName: "Was A Scene", Title: "Was A Scene",
					RootFolderPath:  filepath.Join(root, "adult"),
					GiveBackBox:     "stashdb",
					GiveBackSceneID: "was-a-scene",
					Candidates: []proposals.Candidate{
						{Label: "winner", Path: winner, TrackedID: trackedID, Resolution: 1080, Codec: "h264", BitRate: 8000, Winner: true},
						{Label: "loser", Path: loser, Resolution: 720, Codec: "h264", BitRate: 2000},
					},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
			ctx := context.Background()
			root := t.TempDir()

			// Filler series+episode in the TARGET table, so the episode row
			// this Apply creates cannot coincidentally take the same id as
			// the source-mode row the winner was tracked by. Without it,
			// library_items/library_scenes and library_episodes both hand out
			// id 1 and the "a NEW tracked id" assertion below is
			// unfalsifiable — verified: this test fails for that reason if
			// the filler is removed.
			filler, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 5959, Title: "Filler Show", RootFolderPath: filepath.Join(root, "tv")})
			if err != nil {
				t.Fatalf("seeding filler series: %v", err)
			}
			if _, err := libStore.UpsertEpisode(ctx, library.Episode{
				SeriesID: filler.ID, SeasonNumber: 1, EpisodeNumber: 1,
				FilePath: writeMoveModeFile(t, filepath.Join(root, "tv", "filler.mkv")),
			}); err != nil {
				t.Fatalf("seeding filler episode: %v", err)
			}

			winnerFile, loserFile, trackedID := tc.seed(t, ctx, libStore, root)
			saved, err := propStore.ReplacePending(ctx, tc.sourceMode, proposals.Dedup,
				[]proposals.Proposal{tc.prop(winnerFile, loserFile, trackedID, root)})
			if err != nil {
				t.Fatalf("seeding proposal: %v", err)
			}
			id := saved[0].ID

			handler := moveProposalModeHandler(propStore, settingsStore, libStore)
			rec := postMoveMode(t, handler, id,
				`{"targetMode":"series","tmdbId":6060,"title":"A Show","year":2022,"seasonNumber":1,"episodeNumber":3}`)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
			}

			moved, err := propStore.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get after move: %v", err)
			}
			episodeID, _, err := dedup.ApplyLibrarySeries(ctx, libStore, *moved, nil, nil, false, "")
			if err != nil {
				t.Fatalf("dedup.ApplyLibrarySeries after the move: %v", err)
			}
			if episodeID == int64(trackedID) {
				t.Fatalf("Apply returned the PRE-MOVE tracked id %d — the winner hit dedup.go:408's non-zero-TrackedID early return, so UpsertSeries/UpsertEpisode never ran", episodeID)
			}

			series, err := libStore.GetSeriesByTMDBID(ctx, 6060)
			if err != nil {
				t.Fatalf("expected UpsertSeries to have created the target series row, got %v", err)
			}
			ep, err := libStore.GetEpisode(ctx, series.ID, 1, 3)
			if err != nil {
				t.Fatalf("expected UpsertEpisode to have created S01E03, got %v", err)
			}
			if ep.ID != episodeID {
				t.Errorf("expected Apply to return the new episode row's id %d, got %d", ep.ID, episodeID)
			}
			if ep.FilePath != winnerFile {
				t.Errorf("expected the new episode row to point at the winner's file %q, got %q", winnerFile, ep.FilePath)
			}
			requireFileExists(t, winnerFile, "it is the group's winner")
			requireFileGone(t, loserFile, "Series Dedup removes losers by path")
		})
	}
}
