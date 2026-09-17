package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

// hygieneDeps builds deps + grabsStore for hygiene handler tests.
func hygieneDeps(t *testing.T) (AutoGrabDeps, *grabs.Store) {
	t.Helper()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	return AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}, grabsStore
}

// createHygieneGrab creates a pending_retry grab with given fields.
// Note: Create does not insert the origin column, so Origin is set via
// SetOrigin after creation.
func createHygieneGrab(t *testing.T, grabsStore *grabs.Store, g grabs.Grab) grabs.Grab {
	t.Helper()
	ctx := context.Background()
	created, err := grabsStore.Create(ctx, grabs.Grab{
		Mode:           g.Mode,
		Title:          g.Title,
		TMDBID:         g.TMDBID,
		RootFolderPath: "/movies",
		Status:         grabs.PendingRetry,
		RetryAfter:     g.RetryAfter,
		RetryReason:    g.RetryReason,
		DownloadURL:    g.DownloadURL,
		HoldUntil:      g.HoldUntil,
	})
	if err != nil {
		t.Fatalf("create grab %q: %v", g.Title, err)
	}
	if g.Origin != "" {
		if err := grabsStore.SetOrigin(ctx, created.ID, g.Origin); err != nil {
			t.Fatalf("SetOrigin %q: %v", g.Title, err)
		}
	}
	if g.DownloadGID != "" {
		if err := grabsStore.SetDownloadGID(ctx, created.ID, g.DownloadGID); err != nil {
			t.Fatalf("SetDownloadGID %q: %v", g.Title, err)
		}
		retryT, parseErr := grabs.ParseTime(g.RetryAfter)
		if parseErr != nil {
			retryT = time.Now()
		}
		if parkErr := grabsStore.ParkForTransportResume(ctx, created.ID, retryT, "transport"); parkErr != nil {
			t.Fatalf("ParkForTransportResume %q: %v", g.Title, parkErr)
		}
	}
	got, err := grabsStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get %q: %v", g.Title, err)
	}
	return *got
}

// TestRunParkHygiene_StrandedResume_ClearsGID: a transport-parked row with a
// parseable retry_after more than strandedResumeGrace in the past has its GID
// cleared and joins the days-ladder re-search.
func TestRunParkHygiene_StrandedResume_ClearsGID(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := hygieneDeps(t)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	overdue := now.Add(-(strandedResumeGrace + 1*time.Hour))

	g := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "Stranded Grab", TMDBID: 2001,
		DownloadGID: "nzb-stranded-1", DownloadURL: "https://idx.example/nzb?id=st1",
		RetryAfter: grabs.FormatTime(overdue),
	})
	if g.DownloadGID == "" {
		t.Fatal("pre-condition: grab should have a GID after transport park")
	}

	runParkHygiene(ctx, deps, now)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after hygiene: %v", err)
	}
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (stranded recovery must clear GID)", got.DownloadGID)
	}
	if got.RetryReason != retrievalFailedReason {
		t.Errorf("retry_reason = %q, want %q", got.RetryReason, retrievalFailedReason)
	}
}

// TestRunParkHygiene_MalformedRow_Repaired: a pending_retry row with an
// unparseable retry_after (and no GID) gets rescheduled to RetryBackoff(count+1)
// from now.
func TestRunParkHygiene_MalformedRow_Repaired(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := hygieneDeps(t)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	g := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "Malformed Grab", TMDBID: 2002,
		RetryAfter: "HH24-CORRUPT-TIMESTAMP", RetryReason: "corrupt",
	})

	runParkHygiene(ctx, deps, now)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after hygiene: %v", err)
	}
	if _, err := grabs.ParseTime(got.RetryAfter); err != nil {
		t.Errorf("retry_after %q still malformed after repair: %v", got.RetryAfter, err)
	}
	// The new retry_after should be ≈ now+RetryBackoff(count+1).
	repaired, _ := grabs.ParseTime(got.RetryAfter)
	wantDelay := grabs.RetryBackoff(g.RetryCount + 1)
	diff := repaired.Sub(now)
	if diff < wantDelay-5*time.Second || diff > wantDelay+5*time.Second {
		t.Errorf("repaired retry_after is %s from now, want ≈ %s", diff, wantDelay)
	}
}

// TestRunParkHygiene_FreshTransportPark_NotTouched: a transport-parked row
// whose retry_after is only 2 minutes out (not yet stranded) must not be touched.
func TestRunParkHygiene_FreshTransportPark_NotTouched(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := hygieneDeps(t)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(2 * time.Minute)

	g := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "Fresh Transport", TMDBID: 2003,
		DownloadGID: "nzb-fresh-1", DownloadURL: "https://idx.example/nzb?id=fr1",
		RetryAfter: grabs.FormatTime(fresh),
	})

	runParkHygiene(ctx, deps, now)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after hygiene: %v", err)
	}
	if got.DownloadGID != g.DownloadGID {
		t.Errorf("fresh transport GID changed from %q to %q — hygiene must not touch it", g.DownloadGID, got.DownloadGID)
	}
	if got.RetryAfter != g.RetryAfter {
		t.Errorf("fresh transport retry_after changed from %q to %q", g.RetryAfter, got.RetryAfter)
	}
}

// --- parkHygieneHandler tests ---

func hygieneHandlerRequest(t *testing.T, grabsStore *grabs.Store, body parkHygieneRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/requests/park-hygiene", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	parkHygieneHandler(grabsStore)(rw, req)
	return rw
}

// TestParkHygieneHandler_DryRun_ReportsMatchesMutatesNothing: apply=false
// returns matched ids but leaves all rows unchanged.
func TestParkHygieneHandler_DryRun_ReportsMatchesMutatesNothing(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	now := time.Now()

	g1 := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "Dry Run Target", TMDBID: 3001,
		RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)),
		Origin: "e2e",
	})

	rw := hygieneHandlerRequest(t, grabsStore, parkHygieneRequest{
		Action: "reap", Origin: "e2e", Apply: false,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	var resp parkHygieneResponse
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Matched != 1 {
		t.Errorf("matched = %d, want 1", resp.Matched)
	}
	if resp.Applied {
		t.Error("applied = true in dry-run response, want false")
	}
	if len(resp.IDs) != 1 || resp.IDs[0] != g1.ID {
		t.Errorf("ids = %v, want [%d]", resp.IDs, g1.ID)
	}

	// Row must be unchanged.
	got, _ := grabsStore.Get(ctx, g1.ID)
	if got.Status != grabs.PendingRetry {
		t.Errorf("status changed to %q in dry-run", got.Status)
	}
}

// TestParkHygieneHandler_Reap_FlipsToFailed: apply=true reap with origin="e2e"
// flips all tagged pending_retry rows to Failed.
func TestParkHygieneHandler_Reap_FlipsToFailed(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	now := time.Now()

	g1 := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "E2E Row 1", TMDBID: 3002,
		RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)),
		Origin: "e2e",
	})
	g2 := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "E2E Row 2", TMDBID: 3003,
		RetryAfter: grabs.FormatTime(now.Add(48 * time.Hour)),
		Origin: "e2e",
	})
	// This one has a different origin — must not be reaped.
	gOther := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "Non-E2E Row", TMDBID: 3004,
		RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)),
		Origin: "",
	})

	rw := hygieneHandlerRequest(t, grabsStore, parkHygieneRequest{
		Action: "reap", Origin: "e2e", Apply: true,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	var resp parkHygieneResponse
	json.NewDecoder(rw.Body).Decode(&resp)
	if resp.Matched != 2 {
		t.Errorf("matched = %d, want 2", resp.Matched)
	}
	if !resp.Applied {
		t.Error("applied = false, want true")
	}

	// E2E rows → Failed.
	for _, id := range []int64{g1.ID, g2.ID} {
		got, err := grabsStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		if got.Status != grabs.Failed {
			t.Errorf("grab %d status = %q, want failed", id, got.Status)
		}
		if got.RetryReason != testParkReapedReason {
			t.Errorf("grab %d retry_reason = %q, want %q", id, got.RetryReason, testParkReapedReason)
		}
	}
	// Non-E2E row must be untouched.
	gotOther, _ := grabsStore.Get(ctx, gOther.ID)
	if gotOther.Status != grabs.PendingRetry {
		t.Errorf("non-e2e grab %d status = %q, want pending_retry (must not be reaped)", gOther.ID, gotOther.Status)
	}
}

// TestParkHygieneHandler_OriginEmpty_NotTouched: a row with origin="" is never
// included in a reap targeting origin="e2e".
func TestParkHygieneHandler_OriginEmpty_NotTouched(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	now := time.Now()

	g := createHygieneGrab(t, grabsStore, grabs.Grab{
		Mode: mode.Movies, Title: "No Origin", TMDBID: 3005,
		RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)),
	})

	rw := hygieneHandlerRequest(t, grabsStore, parkHygieneRequest{
		Action: "reap", Origin: "e2e", Apply: true,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}

	got, _ := grabsStore.Get(context.Background(), g.ID)
	if got.Status != grabs.PendingRetry {
		t.Errorf("empty-origin grab was reaped: status = %q", got.Status)
	}
}

// TestParkHygieneHandler_NonPendingRetry_NeverTouched: a non-pending_retry row
// (e.g. Queued) is never matched regardless of origin.
func TestParkHygieneHandler_NonPendingRetry_NeverTouched(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	// Create a Queued grab with origin="e2e".
	queued, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Queued E2E", TMDBID: 3006,
		RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := grabsStore.SetOrigin(ctx, queued.ID, "e2e"); err != nil {
		t.Fatalf("SetOrigin: %v", err)
	}

	rw := hygieneHandlerRequest(t, grabsStore, parkHygieneRequest{
		Action: "reap", Origin: "e2e", Apply: true,
	})
	json.NewDecoder(rw.Body).Decode(new(parkHygieneResponse))

	got, _ := grabsStore.Get(ctx, queued.ID)
	if got.Status != grabs.Queued {
		t.Errorf("queued grab status = %q, want queued (non-pending_retry must never be touched)", got.Status)
	}
}

// TestParkHygieneHandler_BadOrigin_Returns400: an origin not in the allowlist
// returns 400.
func TestParkHygieneHandler_BadOrigin_Returns400(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)

	rw := hygieneHandlerRequest(t, grabsStore, parkHygieneRequest{
		Action: "reap", Origin: "invalid-origin", Apply: true,
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for bad origin", rw.Code)
	}
}
