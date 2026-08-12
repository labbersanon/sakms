package api

// movemode_validation_test.go — the remaining rows of the cross-mode move's
// §7 "Additional tests" table (.omc/plans/autopilot-impl.md): the rejection
// gates (C-4 root folder, C-5 Adult Apply gate, C-6 unique index, D-2
// already-tracked scene, purge/same-mode/applied/half-pair), the Season-0
// acceptance case, and AC4's structural no-op proof.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
)

// TestMoveMode_RejectsWhenTargetRootFolderUnset is C-4: the target mode's
// library root must be configured before a Rename proposal can be moved into
// it, and the refusal message must not disclose which modes have one.
//
// Deliberately a RENAME proposal: step 10 skips the root-folder resolution
// entirely for Dedup (whose files never relocate, so its existing
// root_folder_path is preserved), and a Dedup fixture here would return 204
// and prove nothing.
func TestMoveMode_RejectsWhenTargetRootFolderUnset(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
			RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"series","tmdbId":42,"title":"A Show"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with no series root folder configured, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := propStore.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("Get after refusal: %v", err)
	}
	if got.Mode != mode.Movies {
		t.Errorf("expected the proposal to be untouched by a refused move, got mode=%q", got.Mode)
	}
}

// TestMoveMode_AdultRequiresBoxAndSceneID is C-5. The two 400 subtests are
// the cheap half; the third is the one that matters, and the plan asks for it
// explicitly: an end-to-end move-to-adult → APPLY proving the moved row can
// really be applied. rename.ApplyLibraryAdult hard-refuses a proposal with an
// empty (box, scene_id) identity (internal/rename/rename_adult_library.go
// :194-197), so a move that failed to write GiveBackBox/GiveBackSceneID would
// produce a row that is 204-clean and permanently un-Appliable. Asserting the
// 204 alone would not catch that.
func TestMoveMode_AdultRequiresBoxAndSceneID(t *testing.T) {
	seedMoviesRename := func(t *testing.T, ctx context.Context, propStore *proposals.Store, sourcePath, root string) int64 {
		t.Helper()
		saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
			{
				Status: proposals.Pending, SourceName: filepath.Base(sourcePath), SourcePath: sourcePath,
				RootFolderPath: root, Title: "Movie A", TMDBID: 1,
			},
		})
		if err != nil {
			t.Fatalf("seeding proposal: %v", err)
		}
		return saved[0].ID
	}

	for _, tc := range []struct{ name, body string }{
		{"MissingBox", `{"targetMode":"adult","sceneId":"abc-123","title":"A Scene"}`},
		{"MissingSceneId", `{"targetMode":"adult","box":"stashdb","title":"A Scene"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
			ctx := context.Background()
			if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/media/adult"); err != nil {
				t.Fatalf("seeding adult root folder: %v", err)
			}
			id := seedMoviesRename(t, ctx, propStore, "/media/movies/Movie A", "/media/movies")

			handler := moveProposalModeHandler(propStore, settingsStore, libStore)
			rec := postMoveMode(t, handler, id, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("MovedRowActuallyApplies", func(t *testing.T) {
		_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
		ctx := context.Background()
		root := t.TempDir()
		adultRoot := filepath.Join(root, "adult")
		if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, adultRoot); err != nil {
			t.Fatalf("seeding adult root folder: %v", err)
		}

		// A real orphan folder with a real video file, so Apply's
		// ResolveVideoFile + os.Rename have something to act on.
		sourceDir := filepath.Join(root, "movies", "Some.Release.2020")
		writeMoveModeFile(t, filepath.Join(sourceDir, "video.mkv"))
		id := seedMoviesRename(t, ctx, propStore, sourceDir, filepath.Join(root, "movies"))

		handler := moveProposalModeHandler(propStore, settingsStore, libStore)
		rec := postMoveMode(t, handler, id,
			`{"targetMode":"adult","box":"stashdb","sceneId":"e2e-scene-1","title":"E2E Scene","studio":"E2E Studio","date":"2020-05-06"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from the move, got %d: %s", rec.Code, rec.Body.String())
		}

		moved, err := propStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after move: %v", err)
		}
		// The dependency being proven, stated directly before it is relied on.
		if moved.GiveBackBox != "stashdb" || moved.GiveBackSceneID != "e2e-scene-1" {
			t.Fatalf("the move must write give_back_box/give_back_scene_id — Apply refuses without them; got box=%q sceneId=%q", moved.GiveBackBox, moved.GiveBackSceneID)
		}
		if moved.RootFolderPath != adultRoot {
			t.Fatalf("expected a Rename move to rewrite root_folder_path to the adult root, got %q", moved.RootFolderPath)
		}

		// sess is used ONLY by submitFingerprintGiveBack, which returns early
		// on an empty PHash (internal/rename/rename.go:78) — and MoveMode
		// clears phash — so an empty session is safe here.
		sceneID, fingerprintSubmitted, changes, err := rename.ApplyLibraryAdult(ctx, &mode.Session{}, libStore, *moved, "high", nil)
		if err != nil {
			t.Fatalf("rename.ApplyLibraryAdult on the moved row: %v", err)
		}
		if fingerprintSubmitted {
			t.Errorf("a moved-to-Adult row carries no phash (R-3), so give-back must not fire")
		}
		if len(changes) == 0 {
			t.Errorf("expected Apply to report the relocation it committed")
		}

		scene, err := libStore.GetScene(ctx, "stashdb", "e2e-scene-1")
		if err != nil {
			t.Fatalf("expected the applied row to be tracked at its (box, scene_id) identity, got %v", err)
		}
		if scene.ID != sceneID {
			t.Errorf("expected Apply to return the new scene row's id %d, got %d", scene.ID, sceneID)
		}
		if filepath.Dir(scene.FilePath) != adultRoot {
			t.Errorf("expected the file to have been relocated under the adult root %q, got %q", adultRoot, scene.FilePath)
		}
		if _, err := os.Stat(scene.FilePath); err != nil {
			t.Errorf("expected the relocated file to exist at %q: %v", scene.FilePath, err)
		}
	})
}

// TestMoveMode_ConflictWithLiveTargetProposal is C-6 at the handler level: a
// live proposal already occupying (target mode, workflow, source_path) makes
// the move a 409, and the proposal is left alone.
func TestMoveMode_ConflictWithLiveTargetProposal(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/series"); err != nil {
		t.Fatalf("seeding series root folder: %v", err)
	}
	const shared = "/media/shared/thing.mkv"
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "thing.mkv", SourcePath: shared,
			RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	if _, err := propStore.ReplacePending(ctx, mode.Series, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "thing.mkv", SourcePath: shared,
			RootFolderPath: "/media/series", Title: "Occupying Show", TMDBID: 2,
		},
	}); err != nil {
		t.Fatalf("seeding the conflicting live proposal: %v", err)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"series","tmdbId":42,"title":"A Show"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := propStore.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("Get after conflict: %v", err)
	}
	if got.Mode != mode.Movies {
		t.Errorf("expected the proposal to stay in movies after a 409, got %q", got.Mode)
	}
}

// TestMoveMode_AdultAlreadyTrackedScene is D-2: a move-to-Adult whose (box,
// sceneId) is already tracked is refused with a BARE 409. The
// no-title-in-the-body assertion is not cosmetic — this endpoint is ungated
// for Movies<->Series, so echoing the existing scene's title would turn a
// caller-supplied (box, sceneId) into an Adult-library lookup oracle for a
// PIN-locked session.
func TestMoveMode_AdultAlreadyTrackedScene(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/media/adult"); err != nil {
		t.Fatalf("seeding adult root folder: %v", err)
	}
	const secretTitle = "Very Distinctive Secret Scene Title"
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "taken-1", Title: secretTitle,
		Studio: "Secret Studio", FilePath: "/media/adult/secret.mp4", RootFolderPath: "/media/adult",
	}); err != nil {
		t.Fatalf("seeding the already-tracked scene: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
			RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"adult","box":"stashdb","sceneId":"taken-1","title":"Whatever"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for an already-tracked (box, sceneId), got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, secretTitle) || strings.Contains(body, "Secret Studio") || strings.Contains(body, "secret.mp4") {
		t.Errorf("the 409 body must stay generic — it leaked Adult library metadata: %q", body)
	}
}

// TestMoveMode_RejectsPurgeWorkflow — step 5. A Purge proposal has no
// identification to move.
func TestMoveMode_RejectsPurgeWorkflow(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/series"); err != nil {
		t.Fatalf("seeding series root folder: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Old Movie", SourcePath: "/media/movies/Old Movie",
			RootFolderPath: "/media/movies", Title: "Old Movie", TMDBID: 1, TrackedID: 5,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"series","tmdbId":42,"title":"A Show"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a purge proposal, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMoveMode_RejectsSameMode — step 7.
func TestMoveMode_RejectsSameMode(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
			RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"movies","tmdbId":42,"title":"Another Movie"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a same-mode move, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMoveMode_RejectsAppliedProposal — step 6. Only Pending/Unmatched rows
// can be moved; an already-Applied or Dismissed one cannot.
func TestMoveMode_RejectsAppliedProposal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(t *testing.T, ctx context.Context, propStore *proposals.Store, id int64)
	}{
		{"Applied", func(t *testing.T, ctx context.Context, propStore *proposals.Store, id int64) {
			if err := propStore.MarkApplied(ctx, id, 7); err != nil {
				t.Fatalf("marking applied: %v", err)
			}
		}},
		{"Dismissed", func(t *testing.T, ctx context.Context, propStore *proposals.Store, id int64) {
			if err := propStore.Dismiss(ctx, id); err != nil {
				t.Fatalf("dismissing: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
			ctx := context.Background()
			if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/series"); err != nil {
				t.Fatalf("seeding series root folder: %v", err)
			}

			saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
				{
					Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
					RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
				},
			})
			if err != nil {
				t.Fatalf("seeding proposal: %v", err)
			}
			tc.close(t, ctx, propStore, saved[0].ID)

			handler := moveProposalModeHandler(propStore, settingsStore, libStore)
			rec := postMoveMode(t, handler, saved[0].ID, `{"targetMode":"series","tmdbId":42,"title":"A Show"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for a closed proposal, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestMoveMode_UnmatchedProposalMovesPromotesAndApplies is the positive-case
// sibling of TestMoveMode_RejectsAppliedProposal directly above: step 6's
// status gate admits Unmatched exactly as it admits Pending (both checked in
// the same `if`), and Store.MoveMode's UPDATE unconditionally writes
// status = Pending regardless of which of the two admitted statuses the
// proposal arrived with. Neither behavior had a test pinning it before this
// one — a future refactor narrowing the gate to Pending-only, or dropping the
// Pending promotion, would compile and pass every other test in this file.
// Unmatched is the feature's headline use case (a stuck, unidentified group
// is exactly what an operator most needs to move), so this closes the full
// loop: Unmatched -> moved -> Pending -> Applied, not just the move step in
// isolation.
func TestMoveMode_UnmatchedProposalMovesPromotesAndApplies(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, filepath.Join(root, "series")); err != nil {
		t.Fatalf("seeding series root folder: %v", err)
	}

	sourceFile := writeMoveModeFile(t, filepath.Join(root, "movies", "Movie A.mkv"))
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Unmatched, SourceName: "Movie A.mkv", SourcePath: sourceFile,
			RootFolderPath: filepath.Join(root, "movies"), Reason: "no confident match",
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID
	if saved[0].Status != proposals.Unmatched {
		t.Fatalf("fixture: expected the seeded proposal to be Unmatched, got %q — this test proves nothing unless it starts Unmatched", saved[0].Status)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, id,
		`{"targetMode":"series","tmdbId":42,"title":"A Show","year":2021,"seasonNumber":1,"episodeNumber":1}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 moving an Unmatched proposal, got %d: %s", rec.Code, rec.Body.String())
	}

	moved, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if moved.Status != proposals.Pending {
		t.Fatalf("expected the move to promote Unmatched to Pending, got %q", moved.Status)
	}
	if moved.Mode != mode.Series {
		t.Errorf("expected mode=series after the move, got %q", moved.Mode)
	}
	if moved.Title != "A Show" || moved.TMDBID != 42 || moved.Year != 2021 {
		t.Errorf("expected the target mode's write to land (title/tmdbId/year), got title=%q tmdbId=%d year=%d", moved.Title, moved.TMDBID, moved.Year)
	}
	if moved.SeasonNumber != 1 || moved.EpisodeNumber != 1 {
		t.Errorf("expected season/episode from the move request to land, got season=%d episode=%d", moved.SeasonNumber, moved.EpisodeNumber)
	}
	if moved.Reason != "" {
		t.Errorf("expected the move to clear the stale Unmatched reason, got %q", moved.Reason)
	}

	// Close the loop: the promoted-to-Pending row must be genuinely
	// Applicable, not just readable as Pending. rename.ApplyLibrarySeries
	// itself refuses anything but Pending (rename.go:1864-1866), so a
	// non-nil error here alone would already refute a bad promotion.
	episodeID, _, err := rename.ApplyLibrarySeries(ctx, libStore, nil, nil, *moved, naming.Jellyfin, "medium", nil)
	if err != nil {
		t.Fatalf("Apply of the moved (now-Pending) proposal: %v", err)
	}
	if episodeID == 0 {
		t.Errorf("expected Apply to return a real episode id")
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, 42)
	if err != nil {
		t.Fatalf("expected UpsertSeries to have created the target series row, got %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("expected UpsertEpisode to have created S01E01, got %v", err)
	}
	if ep.ID != episodeID {
		t.Errorf("expected Apply to return the new episode row's id %d, got %d", ep.ID, episodeID)
	}
}

// TestMoveMode_SeriesHalfPairRejected — supplying only one of
// seasonNumber/episodeNumber is a client bug, not a show-level move.
func TestMoveMode_SeriesHalfPairRejected(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"SeasonOnly", `{"targetMode":"series","tmdbId":42,"title":"A Show","seasonNumber":2}`},
		{"EpisodeOnly", `{"targetMode":"series","tmdbId":42,"title":"A Show","episodeNumber":7}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
			ctx := context.Background()
			if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/series"); err != nil {
				t.Fatalf("seeding series root folder: %v", err)
			}
			saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
				{
					Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
					RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
				},
			})
			if err != nil {
				t.Fatalf("seeding proposal: %v", err)
			}

			handler := moveProposalModeHandler(propStore, settingsStore, libStore)
			rec := postMoveMode(t, handler, saved[0].ID, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for a half-supplied season/episode pair, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestMoveMode_SeasonZeroAccepted proves season 0 (Specials) is a real value,
// not "unset". The source proposal is seeded with season 5 on purpose: 0 is
// Go's zero value, so "the move wrote season 0" is indistinguishable from
// "the move wrote nothing" unless the column held something else first.
func TestMoveMode_SeasonZeroAccepted(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/series"); err != nil {
		t.Fatalf("seeding series root folder: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
			RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1,
			SeasonNumber: 5, EpisodeNumber: 9,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	if saved[0].SeasonNumber != 5 {
		t.Fatalf("fixture: the seeded season must be non-zero for this test to discriminate, got %d", saved[0].SeasonNumber)
	}

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)
	rec := postMoveMode(t, handler, saved[0].ID,
		`{"targetMode":"series","tmdbId":42,"title":"A Show","seasonNumber":0,"episodeNumber":1}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 — season 0 is Specials, not 'unset'; got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := propStore.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.SeasonNumber != 0 {
		t.Errorf("expected season 0 to be written over the seeded 5, got %d", got.SeasonNumber)
	}
	if got.EpisodeNumber != 1 {
		t.Errorf("expected episode 1, got %d", got.EpisodeNumber)
	}
}

// TestMoveMode_NoWriteWithoutCommit is AC4: opening the move panel and
// searching a target catalog — then cancelling — must be a STRUCTURAL no-op.
// Only the two search endpoints the panel calls are exercised, and the whole
// Proposal struct is compared with reflect.DeepEqual afterwards, so a write
// to any column (not just the ones a hand-written field list happens to name)
// fails this test.
func TestMoveMode_NoWriteWithoutCommit(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	// A local TMDB fake rather than fakeTMDBRepickServer: that one only
	// serves /search/movie, and the panel's Series target searches /search/tv.
	fakeTMDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/tv" {
			t.Errorf("unexpected TMDB request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":42,"name":"A Show","first_air_date":"2021-01-01"}]}`))
	}))
	defer fakeTMDB.Close()
	// A stash-box fake, so scene-search is exercised for real rather than
	// short-circuiting on "no adult metadata database is configured" — and so
	// nothing in this test reaches the public StashDB endpoint.
	fakeStashDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[{"id":"sb-1","title":"A Scene","release_date":"2020-01-01"}]}}`))
	}))
	defer fakeStashDB.Close()

	for _, c := range []struct{ service, url string }{
		{"tmdb", fakeTMDB.URL},
		{"stashdb", fakeStashDB.URL},
	} {
		if err := connStore.Upsert(ctx, c.service, c.url, "test-key"); err != nil {
			t.Fatalf("seeding %s connection: %v", c.service, err)
		}
		overrideFixedURL(t, c.service, c.url)
	}
	// buildIdentifier reads the endpoint from the registry (migration 0061),
	// so overrideFixedURL alone would leave the real public endpoint in play.
	retargetStashBoxDatabase(t, connStore.DB(), "stashdb", fakeStashDB.URL)
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/media/series"); err != nil {
		t.Fatalf("seeding series root folder: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Movie A", SourcePath: "/media/movies/Movie A",
			RootFolderPath: "/media/movies", Title: "Movie A", TMDBID: 1, Year: 2020,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID

	before, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("snapshotting the proposal: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	// wantSubstring keeps each search honest: a 200 carrying an empty result
	// set would satisfy a bare status check while proving the endpoint was
	// never really exercised.
	for _, tc := range []struct{ u, wantSubstring string }{
		{"/api/modes/series/tmdb-search?q=A+Show", `"A Show"`},
		{"/api/modes/adult/scene-search?q=A+Scene", `"sb-1"`},
	} {
		u := tc.u
		resp, err := http.Get(srv.URL + u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatalf("reading %s: %v", u, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 from %s, got %d: %s", u, resp.StatusCode, raw)
		}
		if !strings.Contains(string(raw), tc.wantSubstring) {
			t.Fatalf("expected %s to return a real result containing %s, got %s", u, tc.wantSubstring, raw)
		}
	}

	after, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("re-reading the proposal: %v", err)
	}
	if !reflect.DeepEqual(*before, *after) {
		t.Fatalf("searching a target catalog wrote to the proposal — cancel/search-only must be a structural no-op.\nbefore: %+v\nafter:  %+v", *before, *after)
	}
}
