package api

// movemode_test.go — handler coverage for POST /api/proposals/{id}/move-mode
// (plan §4.4): a basic 204 success case, and D-1a's Adult section-lock gate
// proven in all three directions (locked move OUT of Adult, locked move INTO
// Adult, and an unrelated Movies<->Series move staying ungated while Adult is
// locked). The REST of the plan's §7/§8 "Additional tests" table — D-2, the
// remaining AC1-AC11 rejection/acceptance gates, and §4.2a/§4.2b's
// TrackedID/retirement coverage — lives in movemode_validation_test.go,
// movemode_dedup_apply_test.go, and movemode_retire_test.go respectively.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// TestMoveMode_MoviesToSeriesSucceeds is the basic 204 success case: a
// Movies Rename proposal moved to Series, ungated (D-1), writes the target
// mode and metadata and returns no body.
func TestMoveMode_MoviesToSeriesSucceeds(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

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

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)

	body := `{"targetMode":"series","tmdbId":42,"title":"A Show","year":2021}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proposals/"+strconv.FormatInt(id, 10)+"/move-mode", strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body on 204, got %q", rec.Body.String())
	}

	got, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.Mode != mode.Series {
		t.Errorf("expected mode=series, got %q", got.Mode)
	}
	if got.Title != "A Show" || got.TMDBID != 42 || got.Year != 2021 {
		t.Errorf("expected target metadata written, got title=%q tmdbId=%d year=%d", got.Title, got.TMDBID, got.Year)
	}
	if got.RootFolderPath != "/media/series" {
		t.Errorf("expected root_folder_path to move with the mode, got %q", got.RootFolderPath)
	}
}

// TestMoveMode_AdultLockedMoveOutOfAdultReturns403 is the D-1a 403 case:
// with adult-content locked, a move whose SOURCE mode is Adult is refused —
// even though the target (Movies) is not itself gated by anything else.
func TestMoveMode_AdultLockedMoveOutOfAdultReturns403(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/media/movies"); err != nil {
		t.Fatalf("seeding movies root folder: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Scene A", SourcePath: "/media/adult/Scene A",
			RootFolderPath: "/media/adult", Title: "Scene A",
			GiveBackBox: "stashdb", GiveBackSceneID: "abc-123", Studio: "Studio A", Date: "2020-01-01",
			ForeignID: "abc-123", ItemType: "scene",
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)

	body := `{"targetMode":"movies","tmdbId":1,"title":"A Movie"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proposals/"+strconv.FormatInt(id, 10)+"/move-mode", strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	locked := sectionlock.Decision{
		Enforcing: true,
		Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
	}
	req = req.WithContext(sectionlock.WithDecision(req.Context(), locked))

	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 while adult-content is locked, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding 403 body: %v", err)
	}
	if resp["code"] != "section_locked" {
		t.Errorf("expected code=section_locked, got %+v", resp)
	}
	if resp["section"] != sectionlock.SectionAdultContent {
		t.Errorf("expected section=%q, got %+v", sectionlock.SectionAdultContent, resp)
	}

	// The proposal must be untouched — the gate ran BEFORE any write.
	got, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after refused move: %v", err)
	}
	if got.Mode != mode.Adult {
		t.Errorf("expected mode to stay adult after a refused move, got %q", got.Mode)
	}
}

// TestMoveMode_AdultLockedMoveIntoAdultReturns403 is D-1a's other gated
// direction: with adult-content locked, a move whose TARGET mode is Adult is
// refused, even though the source (Movies) is not itself gated by anything
// else. Together with TestMoveMode_AdultLockedMoveOutOfAdultReturns403 this
// proves the gate fires in BOTH directions.
func TestMoveMode_AdultLockedMoveIntoAdultReturns403(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/media/adult"); err != nil {
		t.Fatalf("seeding adult root folder: %v", err)
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

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)

	body := `{"targetMode":"adult","box":"stashdb","sceneId":"xyz-999","title":"Scene X"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proposals/"+strconv.FormatInt(id, 10)+"/move-mode", strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	locked := sectionlock.Decision{
		Enforcing: true,
		Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
	}
	req = req.WithContext(sectionlock.WithDecision(req.Context(), locked))

	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a move INTO adult while adult-content is locked, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding 403 body: %v", err)
	}
	if resp["code"] != "section_locked" {
		t.Errorf("expected code=section_locked, got %+v", resp)
	}

	// The proposal must be untouched — the gate ran BEFORE any write, and
	// BEFORE the D-2 already-tracked-scene lookup too.
	got, err := propStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after refused move: %v", err)
	}
	if got.Mode != mode.Movies {
		t.Errorf("expected mode to stay movies after a refused move, got %q", got.Mode)
	}
}

// TestMoveMode_AdultLockedMoviesToSeriesSucceeds is the OTHER half of D-1a:
// a move that touches neither the source NOR the target Adult mode must
// stay fully ungated even while adult-content is locked.
func TestMoveMode_AdultLockedMoviesToSeriesSucceeds(t *testing.T) {
	_, propStore, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

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

	handler := moveProposalModeHandler(propStore, settingsStore, libStore)

	body := `{"targetMode":"series","tmdbId":42,"title":"A Show","year":2021}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proposals/"+strconv.FormatInt(id, 10)+"/move-mode", strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	locked := sectionlock.Decision{
		Enforcing: true,
		Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
	}
	req = req.WithContext(sectionlock.WithDecision(req.Context(), locked))

	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a Movies->Series move while only adult-content is locked, got %d: %s", rec.Code, rec.Body.String())
	}
}
