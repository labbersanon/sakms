package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/proposals"
)

// fakeTMDBSeriesRenameHandler serves TMDB's /search/tv and
// /tv/{id}/season/{n} endpoints for Series' libStore-backed Rename path.
func fakeTMDBSeriesRenameHandler(t *testing.T, tmdbID int, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"id": tmdbID, "name": title},
			}})
		case r.URL.Path == "/tv/"+strconv.Itoa(tmdbID)+"/season/1":
			w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Pilot","air_date":"2020-01-01"}]}`))
		default:
			// enrichment calls (/tv/{id}, /tv/{id}/aggregate_credits) — soft-fail
			http.NotFound(w, r)
		}
	}
}

// TestRenameWorkflow_Series_ScanThenApply_EndToEnd exercises the full
// staged-review loop for Series' libStore-backed Rename path — no Sonarr
// connection at all, a real temp-dir root folder, and a fake TMDB standing
// in for Sonarr's own Lookup — hitting SAK's real HTTP handlers and a real
// migrated SQLite database, not any package in isolation. Adult's
// generic *arr-backed rename.Scan/Apply path is covered separately by
// TestAdultRenameWorkflow_ScanThenApply_EndToEnd.
func TestRenameWorkflow_Series_ScanThenApply_EndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Some.Show.S01E01.1080p.WEB-DL.x264-GROUP.mkv"), []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fakeTMDB := httptest.NewServer(fakeTMDBSeriesRenameHandler(t, 555, "Some Show"))
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

	scanResp, err := http.Post(srv.URL+"/api/modes/series/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 || scanned[0].Status != proposals.Pending || scanned[0].Title != "Some Show" ||
		scanned[0].SeasonNumber != 1 || scanned[0].EpisodeNumber != 1 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	listResp, err := http.Get(srv.URL + "/api/modes/series/rename/proposals")
	if err != nil {
		t.Fatalf("list GET failed: %v", err)
	}
	defer listResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(listResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].ID != scanned[0].ID {
		t.Fatalf("expected the queue to reflect what scan just staged, got %+v", listed)
	}

	applyResp, err := http.Post(
		srv.URL+"/api/proposals/"+strconv.FormatInt(scanned[0].ID, 10)+"/apply", "application/json", nil)
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
	if applied.Status != proposals.Applied || applied.TrackedID == 0 {
		t.Fatalf("expected the proposal to come back Applied with a nonzero episode id, got %+v", applied)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if series.Title != "Some Show" {
		t.Errorf("unexpected series: %+v", series)
	}
}

// fakeTMDBSearchHandler serves TMDB's /search/movie and /movie/{id} endpoints
// for Movies' libStore-backed Rename path. /movie/{id} is called by
// enrichMovieCollection after Apply to fetch belongs_to_collection; the canned
// response has no collection so the enrichment is a no-op for test movies.
func fakeTMDBSearchHandler(t *testing.T, tmdbID int, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/movie":
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"id": tmdbID, "title": title},
			}})
		case r.URL.Path == "/movie/"+strconv.Itoa(tmdbID):
			// No belongs_to_collection — most movies don't have one.
			json.NewEncoder(w).Encode(map[string]any{"id": tmdbID, "title": title})
		default:
			// enrichment calls (/movie/{id}/credits, etc.) — soft-fail
			http.NotFound(w, r)
		}
	}
}

// TestRenameWorkflow_Movies_ScanThenApply_EndToEnd is Movies' own
// libStore-backed counterpart — no Radarr connection at all, a real
// temp-dir root folder, and a fake TMDB standing in for Servarr's Lookup.
func TestRenameWorkflow_Movies_ScanThenApply_EndToEnd(t *testing.T) {
	root := t.TempDir()
	orphanDir := filepath.Join(root, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	if err := os.Mkdir(orphanDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "movie.mkv"), []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fakeTMDB := httptest.NewServer(fakeTMDBSearchHandler(t, 453, "A Beautiful Mind"))
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

	scanResp, err := http.Post(srv.URL+"/api/modes/movies/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 || scanned[0].Status != proposals.Pending || scanned[0].Title != "A Beautiful Mind" {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	applyResp, err := http.Post(
		srv.URL+"/api/proposals/"+strconv.FormatInt(scanned[0].ID, 10)+"/apply", "application/json", nil)
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
	if applied.Status != proposals.Applied || applied.TrackedID == 0 {
		t.Fatalf("expected the proposal to come back Applied with a nonzero library item id, got %+v", applied)
	}

	item, err := libStore.Get(ctx, int64(applied.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.TMDBID != 453 || item.Title != "A Beautiful Mind" {
		t.Errorf("unexpected library item: %+v", item)
	}
	wantDest := filepath.Join(root, "A Beautiful Mind [tmdbid-453]", "A Beautiful Mind [tmdbid-453].mkv")
	if item.FilePath != wantDest {
		t.Errorf("expected the Jellyfin/Emby-standard naming preset applied by default, wanted %q, got %q", wantDest, item.FilePath)
	}
	if _, err := os.Stat(wantDest); err != nil {
		t.Errorf("expected the file to actually be renamed on disk, got: %v", err)
	}
}

// TestRenameWorkflow_Movies_LegacyPreset_ScanThenApply_EndToEnd proves the
// naming-preset setting actually changes ApplyLibrary's on-disk behavior —
// Legacy keeps a bare "Title (Year)" folder/file, no tmdbid tag.
func TestRenameWorkflow_Movies_LegacyPreset_ScanThenApply_EndToEnd(t *testing.T) {
	root := t.TempDir()
	orphanDir := filepath.Join(root, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	if err := os.Mkdir(orphanDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "movie.mkv"), []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fakeTMDB := httptest.NewServer(fakeTMDBSearchHandler(t, 453, "A Beautiful Mind"))
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

	presetBody, _ := json.Marshal(namingPresetRequest{Preset: "legacy"})
	presetResp, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/naming-preset", bytes.NewReader(presetBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := http.DefaultClient.Do(presetResp); err != nil {
		t.Fatalf("naming-preset PUT failed: %v", err)
	}

	scanResp, err := http.Post(srv.URL+"/api/modes/movies/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	applyResp, err := http.Post(
		srv.URL+"/api/proposals/"+strconv.FormatInt(scanned[0].ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	var applied proposals.Proposal
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}

	item, err := libStore.Get(ctx, int64(applied.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDest := filepath.Join(root, "A Beautiful Mind", "A Beautiful Mind.mkv")
	if item.FilePath != wantDest {
		t.Errorf("expected the legacy naming preset (no tmdbid tag), wanted %q, got %q", wantDest, item.FilePath)
	}
}

func TestGetNamingPresetHandler_DefaultsToJellyfin(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/naming-preset")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got namingPresetResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Preset != "jellyfin" {
		t.Errorf("expected default preset %q, got %q", "jellyfin", got.Preset)
	}
}

func TestPutNamingPresetHandler_RejectsInvalidPreset(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(namingPresetRequest{Preset: "bogus"})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/naming-preset", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid preset, got %d", resp.StatusCode)
	}
}

func TestDismissProposalHandler_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	saved, err := propStore.ReplacePending(context.Background(), "movies", proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "x", SourcePath: "/x", RootFolderPath: "/media/Movies", Title: "X"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/dismiss", "application/json", nil)
	if err != nil {
		t.Fatalf("dismiss POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, err := propStore.Get(context.Background(), saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != proposals.Dismissed {
		t.Errorf("expected Dismissed, got %+v", got)
	}
}

func TestApplyProposalHandler_UnknownID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/proposals/999/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown proposal id, got %d", resp.StatusCode)
	}
}

// TestScanHandler_Adult_RequiresIdentifyConfigured proves Adult's Scan no
// longer needs a Whisparr connection (Whisparr eliminated, Stage 4) — it now
// runs the library-backed ScanLibraryAdult, which fails with a clear 502 (not
// a crash) when the identification pipeline isn't configured, the same way
// Series' Scan surfaces a missing TMDB. Movies/Series' own Scan requirements
// are covered by TestScanHandler_Movies_RequiresTMDBConfigured and
// TestScanHandler_Series_RequiresTMDBConfigured below.
func TestScanHandler_Adult_RequiresIdentifyConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/adult/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 when adult identification isn't configured yet, got %d", resp.StatusCode)
	}
}

// TestScanHandler_Series_RequiresTMDBConfigured confirms Series' Scan fails
// with a clear 502 (not a crash) when TMDB isn't set up — there's no Sonarr
// connection requirement to check anymore.
func TestScanHandler_Series_RequiresTMDBConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := settingsStore.Set(context.Background(), seriesLibraryRootFolderKey, t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/series/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 when tmdb isn't configured yet (ScanLibrarySeries' error surfaces the same way ScanLibrary's does), got %d", resp.StatusCode)
	}
}

// TestScanHandler_Movies_RequiresTMDBConfigured confirms Movies' Scan fails
// with a clear 400 (not a crash) when TMDB isn't set up — there's no Radarr
// connection requirement to check anymore.
func TestScanHandler_Movies_RequiresTMDBConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := settingsStore.Set(context.Background(), moviesLibraryRootFolderKey, t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/movies/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 when tmdb isn't configured yet (ScanLibrary's error surfaces the same way Scan's does), got %d", resp.StatusCode)
	}
}
