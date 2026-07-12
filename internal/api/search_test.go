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

	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

func fakeProwlarr(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchHandler_ScoresAndSortsResults(t *testing.T) {
	fake := fakeProwlarr(t, `[
		{"guid":"1","title":"Some.Movie.2023.480p.HDTV.x264-GROUP","indexer":"I","protocol":"torrent","size":1,"seeders":1,"downloadUrl":"http://x/1","publishDate":"2023-01-01"},
		{"guid":"2","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":2,"seeders":2,"downloadUrl":"http://x/2","publishDate":"2023-01-02"}
	]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=Some+Movie")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var results []searchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].GUID != "2" {
		t.Errorf("expected the 1080p WEB-DL release scored first, got %+v", results[0])
	}
}

func TestSearchHandler_RequiresQuery(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a q param, got %d", resp.StatusCode)
	}
}

func TestSearchHandler_ProwlarrNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=x")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when prowlarr isn't configured, got %d", resp.StatusCode)
	}
}

func fakeQBittorrent(t *testing.T, onAdd func(r *http.Request)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
		w.Write([]byte("Ok."))
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if onAdd != nil {
			onAdd(r)
		}
		w.Write([]byte("Ok."))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGrabHandler_Torrent_SendsToQBittorrentAndRecordsGrab(t *testing.T) {
	var gotCategory string
	fakeQB := fakeQBittorrent(t, func(r *http.Request) {
		r.ParseForm()
		gotCategory = r.FormValue("category")
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{
		Title: "Some Movie", TMDBID: 42, Protocol: "torrent",
		DownloadURL: "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12", RootFolderPath: "/movies",
	})
	resp, err := http.Post(srv.URL+"/api/modes/movies/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var g grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if g.DownloadClient != "qbittorrent" || g.ClientRef != "abcdef1234567890abcdef1234567890abcdef12" || g.Status != grabs.Queued {
		t.Errorf("unexpected grab: %+v", g)
	}
	if gotCategory != "movies" {
		t.Errorf("expected category to be the mode name, got %q", gotCategory)
	}
}

// TestGrabHandler_SeasonSpecified_RoundTrips proves seasonSpecified survives
// the POST body -> grabsStore.Create round trip — this flag is what lets
// checkImportHandler tell a deliberate Season 0 (Specials) grab apart from a
// plain series-wide grab with no season picked at all (see search_series_test.go).
func TestGrabHandler_SeasonSpecified_RoundTrips(t *testing.T) {
	fakeQB := fakeQBittorrent(t, func(r *http.Request) {})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{
		Title: "Some Show Special", TMDBID: 555, SeasonNumber: 0, EpisodeNumber: 0, SeasonSpecified: true,
		Protocol: "torrent", DownloadURL: "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12", RootFolderPath: "/tv",
	})
	resp, err := http.Post(srv.URL+"/api/modes/series/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var g grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !g.SeasonSpecified {
		t.Errorf("expected seasonSpecified to round-trip true, got %+v", g)
	}
}

func fakeNZBGet(t *testing.T, nzbID int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/download.nzb", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<nzb/>")) })
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": nzbID, "id": 1})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGrabHandler_Usenet_SendsToNZBGetAndRecordsGrab(t *testing.T) {
	fakeNZB := fakeNZBGet(t, 99)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "nzbget", fakeNZB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{
		Title: "Some Show S01E01", TVDBID: 7, Protocol: "usenet",
		DownloadURL: fakeNZB.URL + "/download.nzb", RootFolderPath: "/tv",
	})
	resp, err := http.Post(srv.URL+"/api/modes/series/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var g grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if g.DownloadClient != "nzbget" || g.ClientRef != "99" {
		t.Errorf("unexpected grab: %+v", g)
	}
}

func TestGrabHandler_UnrecognizedProtocol(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(grabRequest{Title: "X", Protocol: "carrier-pigeon", DownloadURL: "http://x", RootFolderPath: "/movies"})
	resp, err := http.Post(srv.URL+"/api/modes/movies/search/grab", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unrecognized protocol, got %d", resp.StatusCode)
	}
}

func TestListGrabsHandler_ScopedByMode(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "A Movie", Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Series, Title: "A Show", Indexer: "I", Protocol: "usenet", DownloadClient: "nzbget", RootFolderPath: "/tv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/grabs")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var list []grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(list) != 1 || list[0].Title != "A Movie" {
		t.Errorf("expected only the movies grab, got %+v", list)
	}
}

// TestCheckImportHandler_QBittorrentCompleted_PerformsImport exercises the
// full completed-download -> relocate -> record-in-library loop against a
// real on-disk directory (standing in for qBittorrent's actual download
// directory) — no Radarr involved anymore, the same rigor as Dedup's
// end-to-end test.
func TestCheckImportHandler_QBittorrentCompleted_PerformsImport(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Movie.2023.1080p.WEB-DL.x264-GROUP")
	moviesRoot := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "movie.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	fakeQB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","state":"uploading","progress":1,"content_path":"` + downloadDir + `"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer fakeQB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent",
		ClientRef: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}
	// Relocate moves the whole contentPath directory (preserving its
	// basename) into the root folder, the same generic behavior it already
	// has for a directory-shaped Rename source — so the file lands at
	// <root>/<download-dir-name>/movie.mkv, not directly at <root>/movie.mkv.
	wantPath := filepath.Join(moviesRoot, filepath.Base(downloadDir), "movie.mkv")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected the file to have been relocated into the root folder: %v", err)
	}
	item, err := libStore.GetByTMDBID(ctx, mode.Movies, 42)
	if err != nil {
		t.Fatalf("expected the movie to be recorded in the library, got err=%v", err)
	}
	if item.Title != "Some Movie" || item.FilePath != wantPath {
		t.Errorf("unexpected library item: %+v", item)
	}
}

// TestCheckImportHandler_MoviesCompleted_NotifiesJellyfin is Slice 5 end to
// end: a completed grab-import's Relocate lands the file, and
// sess.NotifyPlayers fires exactly one Created PathChange for the resolved
// video file — NOT movedPath itself, since Relocate here moves the whole
// downloadDir (a directory), the same "actual file, not the wrapping
// directory" discipline as rename.go's row 1.
func TestCheckImportHandler_MoviesCompleted_NotifiesJellyfin(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Movie.2023.1080p.WEB-DL.x264-GROUP")
	moviesRoot := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "movie.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	fakeQB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","state":"uploading","progress":1,"content_path":"` + downloadDir + `"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer fakeQB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent",
		ClientRef: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	item, err := libStore.GetByTMDBID(ctx, mode.Movies, 42)
	if err != nil {
		t.Fatalf("expected the movie to be recorded in the library, got err=%v", err)
	}

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	if len(batch) != 1 || batch[0].Path != item.FilePath || batch[0].UpdateType != "Created" {
		t.Errorf("expected a single Created PathChange for the resolved video file %q, got %+v", item.FilePath, batch)
	}
}

// TestCheckImportHandler_RelocateFails_NoNotify proves the plan's explicit
// "if Relocate errors, emit nothing" contract: a failed Relocate (source
// content path vanished) must produce zero notify calls.
func TestCheckImportHandler_RelocateFails_NoNotify(t *testing.T) {
	dir := t.TempDir()
	missingDownloadDir := filepath.Join(dir, "downloads", "gone")
	moviesRoot := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// missingDownloadDir is deliberately never created, so rename.Relocate's
	// os.Rename fails (source doesn't exist).

	fakeQB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","state":"uploading","progress":1,"content_path":"` + missingDownloadDir + `"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer fakeQB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent",
		ClientRef: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a non-200 response surfacing the Relocate failure, got %d", resp.StatusCode)
	}

	if jf.CallCount() != 0 {
		t.Errorf("expected zero notify calls when Relocate fails, got %d: %+v", jf.CallCount(), jf.Batches())
	}
}

// TestCheckImportHandler_JellyfinBestEffort_ImportStillSucceeds is
// Guardrail #1's Slice 5 counterpart: a downstream Jellyfin 500 must never
// fail the grab-import — the file already moved and the library record
// already committed by the time notify runs.
func TestCheckImportHandler_JellyfinBestEffort_ImportStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Movie.2023.1080p.WEB-DL.x264-GROUP")
	moviesRoot := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(moviesRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "movie.mkv"), []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	fakeQB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","state":"uploading","progress":1,"content_path":"` + downloadDir + `"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer fakeQB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(http.StatusInternalServerError)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent",
		ClientRef: "abc123", RootFolderPath: moviesRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 despite the Jellyfin 500, got %d", resp.StatusCode)
	}
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported despite the Jellyfin 500, got %q", updated.Status)
	}
	if jf.CallCount() != 1 {
		t.Errorf("expected the notify attempt to still have been made (and failed), got %d calls", jf.CallCount())
	}
}

// TestCheckImportHandler_AdultCompleted_NotifiesStash proves grab-import
// reaches Adult too (via the mode.Adult branch), and that it notifies Stash
// (not Jellyfin — hardcoded scoping via mode.Build) with movedPath directly.
// Since Whisparr was eliminated (Stage 4) an Adult grab has no scene identity
// at import time, so nothing is UpsertScene'd here — the file is relocated and
// left for the next Rename scan to identify (see the handler's mode.Adult
// branch); Stash's RescanPaths scans the directory tree fine.
func TestCheckImportHandler_AdultCompleted_NotifiesStash(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads", "Some.Scene.mp4")
	adultRoot := filepath.Join(dir, "Adult")
	if err := os.MkdirAll(filepath.Dir(downloadDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(adultRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(downloadDir, []byte("fake video"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	fakeQB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","state":"uploading","progress":1,"content_path":"` + downloadDir + `"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer fakeQB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Adult, Title: "Some Scene",
		Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent",
		ClientRef: "abc123", RootFolderPath: adultRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var updated grabs.Grab
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Status != grabs.Imported {
		t.Errorf("expected status Imported, got %q", updated.Status)
	}

	wantPath := filepath.Join(adultRoot, "Some.Scene.mp4")
	scanCalls := stash.ScanCalls()
	if len(scanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataScan call, got %d: %+v", len(scanCalls), scanCalls)
	}
	scanPaths, _ := scanCalls[0]["paths"].([]any)
	if len(scanPaths) != 1 || scanPaths[0] != wantPath {
		t.Errorf("expected scan of [%q], got %+v", wantPath, scanCalls[0]["paths"])
	}
	if scanCalls[0]["scanGeneratePhashes"] != false {
		t.Errorf("expected phash-free scan (proving RescanPaths not ScanPaths was used), got %v", scanCalls[0]["scanGeneratePhashes"])
	}
	if len(stash.CleanCalls()) != 0 {
		t.Errorf("expected zero metadataClean calls for a Created-only grab-import, got %+v", stash.CleanCalls())
	}
}

func TestCheckImportHandler_StillDownloading_JustUpdatesStatus(t *testing.T) {
	fakeQB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
			w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"hash":"abc123","state":"downloading","progress":0.5}]`))
		}
	}))
	defer fakeQB.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", Indexer: "I", Protocol: "torrent",
		DownloadClient: "qbittorrent", ClientRef: "abc123", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/grabs/"+strconv.FormatInt(g.ID, 10)+"/check-import", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	var updated grabs.Grab
	json.NewDecoder(resp.Body).Decode(&updated)
	if updated.Status != grabs.Downloading {
		t.Errorf("expected status Downloading, got %q", updated.Status)
	}
}
