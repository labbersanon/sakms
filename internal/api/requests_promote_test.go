package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

// TestPromoteRequestHandler_BumpsRetryAfterToNow — AllowsReleasedMovie proof:
// a Movies row with a past digital release promotes (204) and becomes due for retry.
func TestPromoteRequestHandler_BumpsRetryAfterToNow(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42, RootFolderPath: "/movies",
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(now.Add(30 * 24 * time.Hour)),
		RetryReason: "no candidate cleared the quality floor",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Supply a released TMDB client so the gate allows the promote.
	tmdbSrv := fakeTMDBMovieRuntime(t, 100)
	connStore, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("tmdb upsert: %v", err)
	}
	_ = settingsStore

	body, _ := json.Marshal(apidto.PromoteRequestRequest{GrabID: g.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore, connStore, tmdbSrv.Client())(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %q, want 204", rr.Code, rr.Body.String())
	}
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RetryAfter != grabs.FormatTime(now) && got.RetryAfter > grabs.FormatTime(now.Add(time.Minute)) {
		t.Fatalf("retry_after = %q, want near now (%q)", got.RetryAfter, grabs.FormatTime(now))
	}
	due, err := grabsStore.DueForRetry(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}
	if len(due) != 1 || due[0].ID != g.ID {
		t.Fatalf("promoted row should be due, got %+v", due)
	}
}

func TestPromoteRequestHandler_RequiresGrabID(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	body, _ := json.Marshal(apidto.PromoteRequestRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore, nil, nil)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestPromote_RefusesUnreleasedMovie — the gate blocks promoting a Movies row
// when TMDB has only a theatrical US release.
func TestPromote_RefusesUnreleasedMovie(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	ctx := context.Background()
	now := time.Now()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Unreleased Film", TMDBID: 99, RootFolderPath: "/movies",
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(now.Add(time.Hour)),
		RetryReason: "held until its release date",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Theatrical-only TMDB server (type 3 only).
	theatricalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"iso_3166_1": "US",
				"release_dates": []map[string]any{
					{"type": 3, "release_date": "2025-01-01T00:00:00.000Z"},
				},
			}},
		})
	}))
	defer theatricalSrv.Close()

	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	overrideFixedURL(t, "tmdb", theatricalSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", theatricalSrv.URL, "key"); err != nil {
		t.Fatalf("tmdb upsert: %v", err)
	}

	body, _ := json.Marshal(apidto.PromoteRequestRequest{GrabID: g.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore, connStore, theatricalSrv.Client())(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body %q, want 409 (gate blocked)", rr.Code, rr.Body.String())
	}

	// Row must be unchanged.
	still, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if still.RetryAfter != g.RetryAfter {
		t.Errorf("retry_after changed: %q → %q (should be unchanged)", g.RetryAfter, still.RetryAfter)
	}
}

// TestPromote_AllowsSeriesRow — Series rows bypass the gate entirely.
func TestPromote_AllowsSeriesRow(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	ctx := context.Background()
	now := time.Now()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Series, Title: "Some Show", TMDBID: 77, RootFolderPath: "/series",
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)),
		RetryReason: "no candidate",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// No TMDB configured — gate must not be called for Series.
	body, _ := json.Marshal(apidto.PromoteRequestRequest{GrabID: g.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore, nil, nil)(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %q, want 204 for Series", rr.Code, rr.Body.String())
	}
}

// TestPromote_AllowsReleasedMovie — a past digital release allows promote.
func TestPromote_AllowsReleasedMovie(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	ctx := context.Background()
	now := time.Now()

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Released Film", TMDBID: 55, RootFolderPath: "/movies",
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)),
		RetryReason: "no candidate",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	releasedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/release_dates") {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{
					"iso_3166_1": "US",
					"release_dates": []map[string]any{
						{"type": 4, "release_date": "2020-01-01T00:00:00.000Z"},
					},
				}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer releasedSrv.Close()

	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	overrideFixedURL(t, "tmdb", releasedSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", releasedSrv.URL, "key"); err != nil {
		t.Fatalf("tmdb upsert: %v", err)
	}

	body, _ := json.Marshal(apidto.PromoteRequestRequest{GrabID: g.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore, connStore, releasedSrv.Client())(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %q, want 204 for released movie", rr.Code, rr.Body.String())
	}
}
