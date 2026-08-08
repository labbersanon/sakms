package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
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

// fakeTMDBManualSlotServer is fakeTMDBSeriesRepickServer's tolerant sibling for
// the manual-slot-assignment tests: an unknown /search/tv query returns an
// empty result set instead of failing the test (Scan searches for whatever it
// scrapes out of an opaque-hash basename, which is by definition unpredictable),
// and /tv/{id}/season/{n} deliberately catalogs ONLY episode 1 — so an operator
// assigning any other episode number is assigning an UNCATALOGUED slot, which
// is exactly the fail-soft path ApplyLibrarySeries has to survive.
func fakeTMDBManualSlotServer(t *testing.T, searchResults map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			if body, ok := searchResults[r.URL.Query().Get("query")]; ok {
				w.Write([]byte(body))
				return
			}
			w.Write([]byte(`{"results":[]}`))
		case strings.HasPrefix(r.URL.Path, "/tv/") && strings.Contains(r.URL.Path, "/season/"):
			w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Pilot","air_date":"2020-01-01"}]}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
}

// newManualSlotServer stages one Series Rename proposal shaped like the real
// gap population (a basename with zero recoverable episode signal), backed by a
// real file on disk and a real store, and returns the live mux plus everything
// the caller needs to drive it.
func newManualSlotServer(t *testing.T, basename string) (srv *httptest.Server, id int64, root string, libStore *library.Store) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, basename), []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fakeTMDB := fakeTMDBManualSlotServer(t, map[string]string{
		"The Path": `{"results":[{"id":777,"name":"The Path","first_air_date":"2016-03-30"}]}`,
	})
	t.Cleanup(fakeTMDB.Close)

	connStore, propStore, allowStore, settingsStore, grabsStore, ls, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	libStore = ls
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", fakeTMDB.URL)
	if err := connStore.Upsert(ctx, "tmdb", fakeTMDB.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv = httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	scanResp, err := http.Post(srv.URL+"/api/modes/series/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 {
		t.Fatalf("expected exactly 1 proposal for %q, got %+v", basename, scanned)
	}
	if scanned[0].Status != proposals.Unmatched {
		t.Fatalf("expected %q to scan as unmatched (no recoverable episode signal), got %q", basename, scanned[0].Status)
	}
	return srv, scanned[0].ID, root, libStore
}

func postRepick(t *testing.T, srv *httptest.Server, id int64, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/repick", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("repick POST failed: %v", err)
	}
	return resp
}

// TestRepickWorkflow_Series_ManualSlotAssignment_EndToEnd is C3's acceptance
// test (AC6 + AC7), driven over the real mux against a real Postgres store and
// a real file on disk — no mock store anywhere.
//
// The file is an opaque hash basename, one of the two real gap populations this
// feature exists for (the other being Candid Camera's raw DVD VIDEO_TS names):
// nothing in it encodes a season or an episode, so no parser and no AI pass can
// recover one and the operator IS the authority.
//
// The assigned slot (S2E16) is deliberately ABSENT from the fake TMDB season
// catalog, which lists only episode 1 — so this simultaneously exercises AC7's
// fail-soft relocation of an uncatalogued slot: ApplyLibrarySeries' tmdbByEp
// lookup misses, the episode title falls through to "", naming.EpisodeFileName
// omits the title segment, and the relocation plus the library_episodes upsert
// both still succeed.
func TestRepickWorkflow_Series_ManualSlotAssignment_EndToEnd(t *testing.T) {
	srv, id, root, libStore := newManualSlotServer(t, "a3f9c2e1b7d84f0e.mkv")
	ctx := context.Background()

	repickResp := postRepick(t, srv, id, `{"tmdbId":777,"title":"The Path","year":2016,"seasonNumber":2,"episodeNumber":16}`)
	defer repickResp.Body.Close()
	if repickResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from the manual slot assignment, got %d", repickResp.StatusCode)
	}
	var repicked proposals.Proposal
	if err := json.NewDecoder(repickResp.Body).Decode(&repicked); err != nil {
		t.Fatalf("decoding repick response: %v", err)
	}
	if repicked.Status != proposals.Pending {
		t.Fatalf("expected the manual assignment to promote to pending, got %q", repicked.Status)
	}
	if repicked.SeasonNumber != 2 || repicked.EpisodeNumber != 16 {
		t.Fatalf("expected the operator's slot on the proposal, got season=%d episode=%d", repicked.SeasonNumber, repicked.EpisodeNumber)
	}
	if len(repicked.ExtraEpisodeNumbers) != 0 {
		t.Errorf("expected no extra episode numbers, got %v", repicked.ExtraEpisodeNumbers)
	}

	applyResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(applyResp.Body)
		t.Fatalf("expected 200 from apply, got %d: %s", applyResp.StatusCode, body)
	}
	var applied proposals.Proposal
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the manually assigned proposal to apply cleanly, got %+v", applied)
	}

	// AC7 — the uncatalogued slot really was relocated and really was recorded.
	series, err := libStore.GetSeriesByTMDBID(ctx, 777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 2, 16)
	if err != nil {
		t.Fatalf("expected a library_episodes row for the manually assigned slot S02E16: %v", err)
	}
	if ep.FilePath == "" {
		t.Fatalf("expected the episode row to record a real path, got %+v", ep)
	}
	if _, serr := os.Stat(ep.FilePath); serr != nil {
		t.Fatalf("expected the file to exist at the recorded path %q: %v", ep.FilePath, serr)
	}
	if _, serr := os.Stat(filepath.Join(root, "a3f9c2e1b7d84f0e.mkv")); !os.IsNotExist(serr) {
		t.Errorf("expected the opaque-hash source file to be gone after relocation, stat err = %v", serr)
	}
	if !strings.Contains(ep.FilePath, "S02E16") {
		t.Errorf("expected the destination name to carry the assigned slot, got %q", ep.FilePath)
	}
	// Evidence for AC7's fail-soft claim rather than an assertion about it: the
	// slot is uncatalogued, so there is no episode title to interpolate and the
	// name is expected to end right after the slot.
	t.Logf("uncatalogued slot relocated to %q", ep.FilePath)
}

// TestRepick_SeasonZeroAccepted is the single most important handler test in
// C3: season 0 is Specials, and a falsy guard anywhere between the JSON body
// and the UPDATE would silently drop it and assign the wrong slot. That is the
// whole reason the DTO fields are *int rather than int.
func TestRepick_SeasonZeroAccepted(t *testing.T) {
	srv, id, _, _ := newManualSlotServer(t, "VIDEO_TS.VOB")

	resp := postRepick(t, srv, id, `{"tmdbId":777,"title":"The Path","year":2016,"seasonNumber":0,"episodeNumber":3}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for an explicit season 0, got %d: %s", resp.StatusCode, body)
	}
	var got proposals.Proposal
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding repick response: %v", err)
	}
	if got.SeasonNumber != 0 || got.EpisodeNumber != 3 {
		t.Fatalf("expected season 0 / episode 3 to persist verbatim, got season=%d episode=%d", got.SeasonNumber, got.EpisodeNumber)
	}
	if got.Status != proposals.Pending {
		t.Errorf("expected pending, got %q", got.Status)
	}
}

func TestRepick_SeasonEpisodePairValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"season without episode", `{"tmdbId":777,"title":"The Path","seasonNumber":2}`},
		{"episode without season", `{"tmdbId":777,"title":"The Path","episodeNumber":16}`},
		{"episode zero rejected", `{"tmdbId":777,"title":"The Path","seasonNumber":2,"episodeNumber":0}`},
		{"negative season rejected", `{"tmdbId":777,"title":"The Path","seasonNumber":-1,"episodeNumber":3}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, id, _, _ := newManualSlotServer(t, "a3f9c2e1b7d84f0e.mkv")
			resp := postRepick(t, srv, id, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", resp.StatusCode)
			}
		})
	}
}

// TestRepick_ShowLevelUnchanged pins the dispatch's else arm: a body with
// neither field behaves exactly as it did before this feature — Repick, not
// RepickEpisode, so the proposal's existing season/episode are left alone.
func TestRepick_ShowLevelUnchanged(t *testing.T) {
	srv, id, _, _ := newManualSlotServer(t, "a3f9c2e1b7d84f0e.mkv")

	resp := postRepick(t, srv, id, `{"tmdbId":777,"title":"The Path","year":2016}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got proposals.Proposal
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding repick response: %v", err)
	}
	if got.Status != proposals.Pending || got.TMDBID != 777 || got.Title != "The Path" {
		t.Fatalf("unexpected show-level repick result: %+v", got)
	}
	if got.SeasonNumber != 0 || got.EpisodeNumber != 0 {
		t.Errorf("expected the show-level path to leave the slot untouched, got season=%d episode=%d", got.SeasonNumber, got.EpisodeNumber)
	}
}

// TestRepick_MoviesWithSeasonEpisode — season/episode is Series-only, and the
// Movies re-pick path is structurally untouched by this feature.
func TestRepick_MoviesWithSeasonEpisode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Unmatched, SourceName: "gibberish", SourcePath: "/media/Movies/gibberish", RootFolderPath: "/media/Movies", Reason: "no match"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp := postRepick(t, srv, saved[0].ID, `{"tmdbId":777,"title":"The Real Movie","seasonNumber":1,"episodeNumber":2}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for season/episode on a movies proposal, got %d", resp.StatusCode)
	}
}
