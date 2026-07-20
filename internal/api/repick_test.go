package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// fakeTMDBRepickServer serves /search/movie and /movie/{id} — the latter is
// called by enrichMovieCollection after Apply; a no-collection response is
// returned so the enrichment is a no-op for test movies.
func fakeTMDBRepickServer(t *testing.T, results map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/movie/") {
			// Minimal details response with no belongs_to_collection.
			id := strings.TrimPrefix(r.URL.Path, "/movie/")
			json.NewEncoder(w).Encode(map[string]any{"id": id})
			return
		}
		if r.URL.Path != "/search/movie" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query().Get("query")
		body, ok := results[query]
		if !ok {
			t.Fatalf("unexpected search query %q", query)
		}
		w.Write([]byte(body))
	}))
}

// TestRepickWorkflow_WeakMatchSearchRepickApply_EndToEnd is the full manual-
// override loop this feature exists for: Scan's automatic search returns
// SOME result for a garbled name, but it's a weak match that the confidence
// gate (internal/rename/confidence.go) routes to Unmatched — then the
// operator searches TMDB directly and re-picks the correct title, which
// becomes Applyable exactly like a normal Pending proposal.
func TestRepickWorkflow_WeakMatchSearchRepickApply_EndToEnd(t *testing.T) {
	root := t.TempDir()
	orphanDir := filepath.Join(root, "xyz123")
	if err := os.Mkdir(orphanDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "movie.mkv"), []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fakeTMDB := fakeTMDBRepickServer(t, map[string]string{
		"xyz123":         `{"results":[{"id":999,"title":"Father's Day","release_date":"1997-05-09"}]}`,
		"The Real Movie": `{"results":[{"id":777,"title":"The Real Movie","release_date":"2019-06-01"}]}`,
	})
	defer fakeTMDB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	// 1. Scan: the weak match routes to Unmatched, not silently accepted.
	scanResp, err := http.Post(srv.URL+"/api/modes/movies/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 || scanned[0].Status != proposals.Unmatched {
		t.Fatalf("expected 1 unmatched proposal from the weak match, got %+v", scanned)
	}
	id := scanned[0].ID

	// 2. Search TMDB directly for the correct title.
	searchResp, err := http.Get(srv.URL + "/api/modes/movies/tmdb-search?q=" + "The+Real+Movie")
	if err != nil {
		t.Fatalf("search GET failed: %v", err)
	}
	defer searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from tmdb-search, got %d", searchResp.StatusCode)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(searchResp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding search response: %v", err)
	}
	if len(items) != 1 || items[0].ID != 777 || items[0].Title != "The Real Movie" {
		t.Fatalf("unexpected search results: %+v", items)
	}

	// 3. Re-pick: assign the correct TMDB id.
	repickBody := strings.NewReader(`{"tmdbId":777,"title":"The Real Movie","year":2019}`)
	repickResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/repick", "application/json", repickBody)
	if err != nil {
		t.Fatalf("repick POST failed: %v", err)
	}
	defer repickResp.Body.Close()
	if repickResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from repick, got %d", repickResp.StatusCode)
	}
	var repicked proposals.Proposal
	if err := json.NewDecoder(repickResp.Body).Decode(&repicked); err != nil {
		t.Fatalf("decoding repick response: %v", err)
	}
	if repicked.Status != proposals.Pending || repicked.TMDBID != 777 || repicked.Title != "The Real Movie" || repicked.Year != 2019 {
		t.Fatalf("unexpected repick result: %+v", repicked)
	}

	// 4. Apply: the re-picked proposal is now actionable exactly like a
	// normal Pending one.
	applyResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from apply, got %d", applyResp.StatusCode)
	}
	var applied proposals.Proposal
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the re-picked proposal to apply cleanly, got %+v", applied)
	}

	item, err := libStore.GetByTMDBID(ctx, "movies", 777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.Title != "The Real Movie" {
		t.Errorf("expected the library to record the re-picked title, got %+v", item)
	}
}

// fakeTMDBSeriesRepickServer serves /search/tv (keyed by query string, like
// fakeTMDBRepickServer) and /tv/{id}/season/{n} (always succeeds) — Series'
// counterpart to fakeTMDBRepickServer, needed because proposeOneEpisodeLibrary
// confirms the season via TMDB before accepting a match.
func fakeTMDBSeriesRepickServer(t *testing.T, searchResults map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			query := r.URL.Query().Get("query")
			body, ok := searchResults[query]
			if !ok {
				t.Fatalf("unexpected search query %q", query)
			}
			w.Write([]byte(body))
		case strings.HasPrefix(r.URL.Path, "/tv/") && strings.Contains(r.URL.Path, "/season/"):
			w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Pilot","air_date":"2020-01-01"}]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

// TestRepickWorkflow_Series_WeakMatchSearchRepickApply_EndToEnd is Series'
// counterpart to the Movies end-to-end test above — the confidence gate and
// the repick handler are separate call sites for Series (proposeOneEpisodeLibrary,
// SearchTV) from Movies (proposeOneLibrary, SearchMovies), so this needs its
// own direct coverage, not just an assumption that the Movies path generalizes.
func TestRepickWorkflow_Series_WeakMatchSearchRepickApply_EndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xyz123.S01E01.mkv"), []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fakeTMDB := fakeTMDBSeriesRepickServer(t, map[string]string{
		"xyz123":        `{"results":[{"id":999,"name":"Completely Unrelated Show"}]}`,
		"The Real Show": `{"results":[{"id":777,"name":"The Real Show","first_air_date":"2019-06-01"}]}`,
	})
	defer fakeTMDB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	// 1. Scan: the weak match routes to Unmatched.
	scanResp, err := http.Post(srv.URL+"/api/modes/series/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 || scanned[0].Status != proposals.Unmatched {
		t.Fatalf("expected 1 unmatched proposal from the weak match, got %+v", scanned)
	}
	id := scanned[0].ID

	// 2. Search TMDB directly (SearchTV) for the correct show.
	searchResp, err := http.Get(srv.URL + "/api/modes/series/tmdb-search?q=The+Real+Show")
	if err != nil {
		t.Fatalf("search GET failed: %v", err)
	}
	defer searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from tmdb-search, got %d", searchResp.StatusCode)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(searchResp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding search response: %v", err)
	}
	if len(items) != 1 || items[0].ID != 777 || items[0].Title != "The Real Show" {
		t.Fatalf("unexpected search results: %+v", items)
	}

	// 3. Re-pick.
	repickBody := strings.NewReader(`{"tmdbId":777,"title":"The Real Show","year":2019}`)
	repickResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/repick", "application/json", repickBody)
	if err != nil {
		t.Fatalf("repick POST failed: %v", err)
	}
	defer repickResp.Body.Close()
	if repickResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from repick, got %d", repickResp.StatusCode)
	}
	var repicked proposals.Proposal
	if err := json.NewDecoder(repickResp.Body).Decode(&repicked); err != nil {
		t.Fatalf("decoding repick response: %v", err)
	}
	if repicked.Status != proposals.Pending || repicked.TMDBID != 777 || repicked.Title != "The Real Show" {
		t.Fatalf("unexpected repick result: %+v", repicked)
	}

	// 4. Apply.
	applyResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from apply, got %d", applyResp.StatusCode)
	}
	var applied proposals.Proposal
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the re-picked proposal to apply cleanly, got %+v", applied)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, 777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if series.Title != "The Real Show" {
		t.Errorf("expected the library to record the re-picked title, got %+v", series)
	}
}

func TestTMDBSearchHandler_RejectsAdultMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/tmdb-search?q=anything")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for adult mode, got %d", resp.StatusCode)
	}
}

func TestRepickProposalHandler_UnknownID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body := strings.NewReader(`{"tmdbId":1,"title":"Anything"}`)
	resp, err := http.Post(srv.URL+"/api/proposals/999/repick", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown proposal id, got %d", resp.StatusCode)
	}
}

func TestRepickProposalHandler_RejectsMissingFields(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	saved, err := propStore.ReplacePending(context.Background(), "movies", proposals.Rename, []proposals.Proposal{
		{Status: proposals.Unmatched, SourceName: "x", SourcePath: "/x", RootFolderPath: "/media/Movies", Reason: "no match"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body := strings.NewReader(`{"title":"Missing TMDB id"}`)
	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/repick", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing tmdbId, got %d", resp.StatusCode)
	}
}

func TestRepickProposalHandler_RejectsAlreadyAppliedProposal(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	saved, err := propStore.ReplacePending(ctx, "movies", proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "x", SourcePath: "/x", RootFolderPath: "/media/Movies", Title: "X", TMDBID: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := propStore.MarkApplied(ctx, saved[0].ID, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body := strings.NewReader(`{"tmdbId":2,"title":"Something Else"}`)
	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/repick", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an already-applied proposal, got %d", resp.StatusCode)
	}
}

func TestRepickProposalHandler_RejectsNonRenameWorkflow(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	saved, err := propStore.ReplacePending(context.Background(), "movies", proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "x", SourcePath: "/x", RootFolderPath: "/media/Movies", Title: "X", TrackedID: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body := strings.NewReader(`{"tmdbId":1,"title":"Anything"}`)
	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/repick", "application/json", body)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-Rename workflow proposal, got %d", resp.StatusCode)
	}
}
