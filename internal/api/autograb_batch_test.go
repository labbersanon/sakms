package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

// prowlarrStats records what a batch actually did to the indexer: how many
// searches fired in total, and the peak number that were ever in flight at the
// same instant. maxInFlight is the load-bearing proof for hard blocker #1 — a
// sequential loop can never push it above 1, a goroutine fan-out would.
type prowlarrStats struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	total       int
}

func (s *prowlarrStats) snapshot() (total, maxInFlight int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.maxInFlight
}

// fakeProwlarrTracking is a Prowlarr mock that (1) records per-request
// concurrency/count stats and (2) picks its response status+body per request via
// respFor (keyed on the query params, so different titles can return different
// releases, or a deliberate 500 to drive a mid-pipeline per-item error). hold is
// how long each request is held "in flight" — a non-zero hold widens the window
// in which a concurrent handler would overlap requests, so a sequential
// handler's maxInFlight staying 1 is a real observation, not an artifact of
// requests completing too fast to overlap.
func fakeProwlarrTracking(t *testing.T, hold time.Duration, respFor func(url.Values) (int, string)) (*httptest.Server, *prowlarrStats) {
	t.Helper()
	stats := &prowlarrStats{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.mu.Lock()
		stats.total++
		stats.inFlight++
		if stats.inFlight > stats.maxInFlight {
			stats.maxInFlight = stats.inFlight
		}
		stats.mu.Unlock()

		if hold > 0 {
			time.Sleep(hold)
		}
		status, body := respFor(r.URL.Query())

		stats.mu.Lock()
		stats.inFlight--
		stats.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, stats
}

// A healthy 8 GB / x265 / 1080p / 50-seeder release — clears every Low-tier
// floor, so it auto-grabs (same shape the single-endpoint qualified test uses).
const healthyMovieRelease = `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`

// A "1080p" release with an absurd 1-byte size → the mislabel check excludes it,
// so nothing qualifies and the item falls back to the manual pick list.
const tinyMovieRelease = `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"BadIndexer","protocol":"torrent","size":1,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`

// batchTestServer wires a full Mux with a tracking Prowlarr mock and a movie-
// runtime TMDB mock, Movies quality tier Low and root folder /movies configured.
// Returns the running server and the Prowlarr stats for outcome assertions.
func batchTestServer(t *testing.T, dlGID string, hold time.Duration, respFor func(url.Values) (int, string)) (*httptest.Server, *prowlarrStats) {
	t.Helper()
	dl := newTestDownloader(dlGID, t.TempDir())
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 100 min = 6000 s
	prowlarr, stats := fakeProwlarrTracking(t, hold, respFor)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, dl, nil, nil))
	t.Cleanup(srv.Close)
	return srv, stats
}

func postBatch(t *testing.T, url string, req apidto.AutoGrabBatchRequest) (*http.Response, apidto.AutoGrabBatchResponse) {
	t.Helper()
	body, _ := json.Marshal(req)
	resp, err := http.Post(url+"/api/autograb-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	var out apidto.AutoGrabBatchResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			t.Fatalf("decoding response: %v", err)
		}
	}
	return resp, out
}

// TestAutoGrabBatchHandler_SequentialMaxOneInFlight is the hard-blocker #1 proof:
// across a multi-item batch, the Prowlarr mock must never observe more than ONE
// search in flight at a time. A client-side/goroutine fan-out would recreate the
// banned "hundreds of concurrent indexer queries" pattern; the sequential loop
// keeps peak concurrency pinned at 1. Every item is a healthy release, so all
// five auto-grab — proving the sequential guarantee holds on the real grab path,
// not just an error/short-circuit path.
func TestAutoGrabBatchHandler_SequentialMaxOneInFlight(t *testing.T) {
	// A 5ms hold widens the overlap window so a concurrent handler would be
	// caught; a sequential one still peaks at 1.
	srv, stats := batchTestServer(t, "gid-auto", 5*time.Millisecond, func(url.Values) (int, string) {
		return http.StatusOK, healthyMovieRelease
	})

	const n = 5
	items := make([]apidto.AutoGrabBatchItem, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, apidto.AutoGrabBatchItem{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42}})
	}

	resp, out := postBatch(t, srv.URL, apidto.AutoGrabBatchRequest{Items: items})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out.Results) != n {
		t.Fatalf("expected %d results, got %d", n, len(out.Results))
	}
	for i, r := range out.Results {
		if !r.Grabbed || r.Grab == nil {
			t.Fatalf("result %d expected grabbed, got %+v", i, r)
		}
	}

	total, maxInFlight := stats.snapshot()
	if total != n {
		t.Errorf("expected exactly %d Prowlarr searches (one per item), got %d", n, total)
	}
	if maxInFlight != 1 {
		t.Errorf("expected max 1 concurrent Prowlarr search in flight (sequential execution), got %d — the batch is fanning out searches concurrently", maxInFlight)
	}
}

// TestAutoGrabBatchHandler_OverCapRejectedBeforeAnySearch is the hard-blocker #2
// proof: a batch larger than MaxBatchGrabItems is rejected with a 400 BEFORE the
// sequential loop starts, so ZERO Prowlarr searches fire (the mock records no
// requests at all) — the rejection can never come "after searching some and then
// bailing".
func TestAutoGrabBatchHandler_OverCapRejectedBeforeAnySearch(t *testing.T) {
	srv, stats := batchTestServer(t, "gid-auto", 0, func(url.Values) (int, string) {
		return http.StatusOK, healthyMovieRelease
	})

	items := make([]apidto.AutoGrabBatchItem, 0, MaxBatchGrabItems+1)
	for i := 0; i < MaxBatchGrabItems+1; i++ {
		items = append(items, apidto.AutoGrabBatchItem{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42}})
	}

	resp, _ := postBatch(t, srv.URL, apidto.AutoGrabBatchRequest{Items: items})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for %d items (> cap %d), got %d", len(items), MaxBatchGrabItems, resp.StatusCode)
	}

	total, _ := stats.snapshot()
	if total != 0 {
		t.Errorf("expected ZERO Prowlarr searches for an over-cap batch (rejected before any search fires), got %d", total)
	}
}

// TestAutoGrabBatchHandler_ThreeStateMixedBatch proves the three-state result
// contract AND skip-and-continue. The batch is [grab, fallback, ERROR, grab]:
// the error is a mid-pipeline Prowlarr 500 (a realistic indexer failure, the
// pre-mortem #1 case) placed BEFORE a still-succeeding item — so the final
// item's grab is what proves the loop continued past the failure rather than
// aborting. The response is always 200; per-item outcomes live in Results.
func TestAutoGrabBatchHandler_ThreeStateMixedBatch(t *testing.T) {
	srv, _ := batchTestServer(t, "gid-auto", 0, func(q url.Values) (int, string) {
		switch q.Get("query") {
		case "Fallback Movie":
			return http.StatusOK, tinyMovieRelease
		case "Error Movie":
			// A deliberate indexer failure mid-batch → autoGrabSearch returns an
			// error → grabOneBatchItem returns an error → this item is recorded
			// as Error and the loop moves on (never a panic — httpx.DoJSON turns
			// a non-2xx into an err return).
			return http.StatusInternalServerError, `{"error":"indexer exploded"}`
		default: // "Qualify Movie" and "Recover Movie"
			return http.StatusOK, healthyMovieRelease
		}
	})

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Qualify Movie", TMDBID: 42}},
		{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Fallback Movie", TMDBID: 43}},
		{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Error Movie", TMDBID: 44}},
		{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Recover Movie", TMDBID: 45}},
	}}

	resp, out := postBatch(t, srv.URL, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (per-item outcomes live in the body), got %d", resp.StatusCode)
	}
	if len(out.Results) != 4 {
		t.Fatalf("expected 4 results, got %d: %+v", len(out.Results), out.Results)
	}

	if r := out.Results[0]; !r.Grabbed || r.Fallback || r.Error != "" || r.Grab == nil {
		t.Errorf("item 0 expected a clean grab, got %+v", r)
	}
	if r := out.Results[1]; r.Grabbed || !r.Fallback || r.Error != "" || len(r.Candidates) != 1 {
		t.Errorf("item 1 expected a fallback with a pick list, got %+v", r)
	}
	if r := out.Results[2]; r.Grabbed || r.Fallback || r.Error == "" {
		t.Errorf("item 2 expected an error outcome, got %+v", r)
	}
	// The load-bearing skip-and-continue assertion: the item AFTER the error
	// still grabbed, so the batch did NOT abort at the failure.
	if r := out.Results[3]; !r.Grabbed || r.Fallback || r.Error != "" || r.Grab == nil {
		t.Errorf("item 3 (after the error) expected a clean grab, proving the batch continued past the failure — got %+v", r)
	}
	// Index is stable and matches submission order.
	for i, r := range out.Results {
		if r.Index != i {
			t.Errorf("result %d has Index %d — index must match submission order", i, r.Index)
		}
	}
}

// TestAutoGrabBatchHandler_CrossModeSessionIsolation is the code-review-flagged
// gap: every other test in this file submits movies-only batches, so none of
// them actually exercise autoGrabBatchHandler's per-mode mode.Session cache
// (sessions map[mode.Mode]*mode.Session) across a genuinely mixed-mode batch —
// the exact property that distinguishes this handler from a naive "one shared
// session for everything" bug.
//
// The proof uses each mode's DIFFERENT seeder floor as a discriminating
// signal, not just "both items happened to grab": Movies/Series require >=5
// seeders, Adult only >=3 (see adultMinSeeders/minSeedersFor). A single
// release shape with EXACTLY 3 seeders is submitted for BOTH a movies item and
// an adult item in the SAME batch. If either item were routed through the
// wrong mode's session/settings (movies root folder leaking into the adult
// item, or the adult item wrongly graded against movies' 5-seeder floor, or
// vice versa), the outcome would flip: the movies item would incorrectly
// qualify, or the adult item would incorrectly fall back. Correct per-mode
// isolation is the only way to get "movies item falls back AND adult item
// grabs" from an otherwise-identical release.
func TestAutoGrabBatchHandler_CrossModeSessionIsolation(t *testing.T) {
	dl := newTestDownloader("gid-auto", t.TempDir())
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 100 min runtime for the movies item
	// Every request gets the identical release: healthy bitrate, exactly 3
	// seeders — below Movies' floor (5), at-or-above Adult's floor (3).
	release := `[{"guid":"1","title":"Cross.Mode.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":3,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`
	prowlarr, stats := fakeProwlarrTracking(t, 0, func(url.Values) (int, string) {
		return http.StatusOK, release
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both modes' settings configured simultaneously — this is what makes a
	// wrong-session-picked-up bug possible to hide movies-only.
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Adult), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, dl, nil, nil))
	defer srv.Close()

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42}},
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Some Scene", Studio: "Some Studio", DurationSeconds: 6000}},
	}}
	resp, out := postBatch(t, srv.URL, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(out.Results), out.Results)
	}

	movies, adult := out.Results[0], out.Results[1]
	if movies.Grabbed || !movies.Fallback {
		t.Errorf("movies item (3 seeders, below the 5-seeder movies floor) should have fallen back, got %+v — a session/settings leak from the adult item would wrongly let this qualify", movies)
	}
	if !adult.Grabbed || adult.Fallback || adult.Grab == nil {
		t.Errorf("adult item (3 seeders, at the adult floor) should have grabbed, got %+v — a session/settings leak from the movies item (5-seeder floor, or the movies root folder) would wrongly fall this back", adult)
	}
	if adult.Grabbed && adult.Grab != nil {
		// Confirm the grab actually landed under the ADULT root folder
		// ("/adult"), not the movies one ("/movies") — the clearest possible
		// sign the two sessions were never conflated.
		if adult.Grab.RootFolderPath != "/adult" {
			t.Errorf("adult grab's RootFolderPath = %q, want \"/adult\" — a session mix-up would leak the movies root folder in here", adult.Grab.RootFolderPath)
		}
	}

	total, _ := stats.snapshot()
	if total != 2 {
		t.Errorf("expected exactly 2 Prowlarr searches (one per item, across both modes), got %d", total)
	}
}

// TestAutoGrabBatchHandler_CapBoundaries covers the two non-blocker cap edges:
// an empty batch is a 400 (mirrors apply-batch's empty-batch rejection), and a
// batch of EXACTLY MaxBatchGrabItems is NOT rejected — it runs and returns a
// result per item.
func TestAutoGrabBatchHandler_CapBoundaries(t *testing.T) {
	srv, _ := batchTestServer(t, "gid-auto", 0, func(url.Values) (int, string) {
		// Tiny release → every item falls back (no grabs), keeping the 20-item
		// run light while still proving it wasn't rejected.
		return http.StatusOK, tinyMovieRelease
	})

	t.Run("empty batch rejected", func(t *testing.T) {
		resp, _ := postBatch(t, srv.URL, apidto.AutoGrabBatchRequest{Items: nil})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for an empty batch, got %d", resp.StatusCode)
		}
	})

	t.Run("exactly cap not rejected", func(t *testing.T) {
		items := make([]apidto.AutoGrabBatchItem, 0, MaxBatchGrabItems)
		for i := 0; i < MaxBatchGrabItems; i++ {
			items = append(items, apidto.AutoGrabBatchItem{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42}})
		}
		resp, out := postBatch(t, srv.URL, apidto.AutoGrabBatchRequest{Items: items})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for exactly %d items (at the cap, not over it), got %d", MaxBatchGrabItems, resp.StatusCode)
		}
		if len(out.Results) != MaxBatchGrabItems {
			t.Fatalf("expected %d results, got %d", MaxBatchGrabItems, len(out.Results))
		}
		for i, r := range out.Results {
			if !r.Fallback {
				t.Errorf("item %d expected fallback, got %+v", i, r)
			}
		}
	})
}
