package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

// buildCensusFixture inserts one row per bucket and returns the expected
// census counts. All rows are pending_retry unless otherwise noted.
//
// Buckets:
//  1. malformed retry_after (non-empty, unparseable)
//  2. origin="e2e" (TestOrigin)
//  3. awaiting resume overdue (GID+retry_after older than strandedResumeGrace)
//  4. due now (retry_after <= now)
//  5. due within 1h
//  6. parked within 24h
//  7. parked far (> 7d)
//  8. held pre-release (HoldUntil > now)
//  9. air-date shaped (Series, TMDBID>0, SeasonSpecified, EpisodeNumber>0)
func buildCensusFixture(t *testing.T, grabsStore *grabs.Store, now time.Time) parkCensusResponse {
	t.Helper()
	ctx := context.Background()

	var want parkCensusResponse
	want.GeneratedAt = grabs.FormatTime(now)

	createPending := func(g grabs.Grab) grabs.Grab {
		t.Helper()
		created, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: g.Mode, Title: g.Title, TMDBID: g.TMDBID,
			TVDBID: g.TVDBID, SeasonNumber: g.SeasonNumber, EpisodeNumber: g.EpisodeNumber,
			SeasonSpecified: g.SeasonSpecified,
			RootFolderPath:  "/movies",
			Status:          grabs.PendingRetry,
			RetryAfter:      g.RetryAfter,
			RetryReason:     g.RetryReason,
			HoldUntil:       g.HoldUntil,
			Origin:          g.Origin,
			DownloadURL:     g.DownloadURL,
		})
		if err != nil {
			t.Fatalf("create pending grab %q: %v", g.Title, err)
		}
		if g.DownloadGID != "" {
			if err := grabsStore.SetDownloadGID(ctx, created.ID, g.DownloadGID); err != nil {
				t.Fatalf("SetDownloadGID %q: %v", g.Title, err)
			}
			// Now re-park as transport so the GID sticks.
			after := g.RetryAfter
			if after == "" {
				after = grabs.FormatTime(now)
			}
			retryT, _ := grabs.ParseTime(after)
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

	// 1. Malformed schedule.
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Malformed", TMDBID: 1001,
		RetryAfter: "NOT-A-TIMESTAMP", RetryReason: "corrupt"})
	want.Total++
	want.MalformedSchedule++

	// 2. origin="e2e" (TestOrigin).
	e2eRow := createPending(grabs.Grab{Mode: mode.Movies, Title: "E2E Row", TMDBID: 1002,
		RetryAfter: grabs.FormatTime(now.Add(2 * 24 * time.Hour))})
	if err := grabsStore.SetOrigin(ctx, e2eRow.ID, "e2e"); err != nil {
		t.Fatalf("SetOrigin e2e: %v", err)
	}
	want.Total++
	want.TestOrigin++
	want.ParkedWithin7d++

	// 3. awaiting resume overdue (GID + retry_after more than strandedResumeGrace in the past).
	overdueTime := now.Add(-(strandedResumeGrace + 1*time.Hour))
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Overdue Resume", TMDBID: 1003,
		DownloadGID: "nzb-overdue-census", DownloadURL: "https://idx.example/nzb?id=overdue",
		RetryAfter: grabs.FormatTime(overdueTime)})
	want.Total++
	want.AwaitingResume++
	want.AwaitingResumeOverdue++

	// 4. due now.
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Due Now", TMDBID: 1004,
		RetryAfter: grabs.FormatTime(now.Add(-1 * time.Minute))})
	want.Total++
	want.DueNow++

	// 5. due within 1h (but not now).
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Due Soon", TMDBID: 1005,
		RetryAfter: grabs.FormatTime(now.Add(30 * time.Minute))})
	want.Total++
	want.DueWithin1h++

	// 6. parked within 24h (>1h, ≤24h).
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Parked 4h", TMDBID: 1006,
		RetryAfter: grabs.FormatTime(now.Add(4 * time.Hour))})
	want.Total++
	want.ParkedWithin24h++

	// 7. parked far (>7d).
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Far Out", TMDBID: 1007,
		RetryAfter: grabs.FormatTime(now.Add(30 * 24 * time.Hour))})
	want.Total++
	want.ParkedFar++

	// 8. held pre-release.
	createPending(grabs.Grab{Mode: mode.Movies, Title: "Pre-release", TMDBID: 1008,
		HoldUntil: grabs.FormatTime(now.Add(7 * 24 * time.Hour))})
	want.Total++
	want.HeldPreRelease++
	// No retry_after → not in distance buckets.

	// 9. air-date shaped (Series, TMDBID>0, SeasonSpecified, EpisodeNumber>0).
	createPending(grabs.Grab{Mode: mode.Series, Title: "Air Date Row", TMDBID: 1009,
		TVDBID: 5001, SeasonNumber: 1, EpisodeNumber: 3, SeasonSpecified: true,
		RetryAfter: grabs.FormatTime(now.Add(48 * time.Hour))})
	want.Total++
	want.AirDateShaped++
	want.ParkedWithin7d++

	return want
}

// TestComputeParkCensus_FixtureBuckets verifies every bucket in computeParkCensus
// using the canonical fixture set.
func TestComputeParkCensus_FixtureBuckets(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	want := buildCensusFixture(t, grabsStore, now)
	got := computeParkCensus(context.Background(), grabsStore, now)

	if got.Total != want.Total {
		t.Errorf("Total = %d, want %d", got.Total, want.Total)
	}
	if got.DueNow != want.DueNow {
		t.Errorf("DueNow = %d, want %d", got.DueNow, want.DueNow)
	}
	if got.DueWithin1h != want.DueWithin1h {
		t.Errorf("DueWithin1h = %d, want %d", got.DueWithin1h, want.DueWithin1h)
	}
	if got.ParkedWithin24h != want.ParkedWithin24h {
		t.Errorf("ParkedWithin24h = %d, want %d", got.ParkedWithin24h, want.ParkedWithin24h)
	}
	if got.ParkedWithin7d != want.ParkedWithin7d {
		t.Errorf("ParkedWithin7d = %d, want %d", got.ParkedWithin7d, want.ParkedWithin7d)
	}
	if got.ParkedFar != want.ParkedFar {
		t.Errorf("ParkedFar = %d, want %d", got.ParkedFar, want.ParkedFar)
	}
	if got.AwaitingResume != want.AwaitingResume {
		t.Errorf("AwaitingResume = %d, want %d", got.AwaitingResume, want.AwaitingResume)
	}
	if got.AwaitingResumeOverdue != want.AwaitingResumeOverdue {
		t.Errorf("AwaitingResumeOverdue = %d, want %d", got.AwaitingResumeOverdue, want.AwaitingResumeOverdue)
	}
	if got.HeldPreRelease != want.HeldPreRelease {
		t.Errorf("HeldPreRelease = %d, want %d", got.HeldPreRelease, want.HeldPreRelease)
	}
	if got.AirDateShaped != want.AirDateShaped {
		t.Errorf("AirDateShaped = %d, want %d", got.AirDateShaped, want.AirDateShaped)
	}
	if got.TestOrigin != want.TestOrigin {
		t.Errorf("TestOrigin = %d, want %d", got.TestOrigin, want.TestOrigin)
	}
	if got.MalformedSchedule != want.MalformedSchedule {
		t.Errorf("MalformedSchedule = %d, want %d", got.MalformedSchedule, want.MalformedSchedule)
	}
}

// TestComputeParkCensus_EmptyStore returns all-zero counts on a fresh store.
func TestComputeParkCensus_EmptyStore(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	got := computeParkCensus(context.Background(), grabsStore, now)
	if got.Total != 0 {
		t.Errorf("Total = %d, want 0 on empty store", got.Total)
	}
}

// TestParkCensusHandler_Returns200JSON verifies the HTTP handler returns 200
// with well-formed JSON.
func TestParkCensusHandler_Returns200JSON(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)

	handler := parkCensusHandler(grabsStore)
	req := httptest.NewRequest(http.MethodGet, "/api/requests/park-census", nil)
	rw := httptest.NewRecorder()
	handler(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	var resp parkCensusResponse
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.GeneratedAt == "" {
		t.Error("generatedAt is empty")
	}
}
