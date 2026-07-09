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

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "qbittorrent", fakeQB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.UpsertWithUsername(ctx, "nzbget", fakeNZB.URL, "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "A Movie", Indexer: "I", Protocol: "torrent", DownloadClient: "qbittorrent", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Series, Title: "A Show", Indexer: "I", Protocol: "usenet", DownloadClient: "nzbget", RootFolderPath: "/tv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
// full completed-download -> relocate -> register loop against a real
// on-disk directory (standing in for qBittorrent's actual download
// directory) and a fake Radarr, the same rigor as Dedup's end-to-end test.
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

	var registered bool
	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			registered = true
			w.Write([]byte(`{"id":77}`))
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fakeRadarr.Close()

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

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "radarr", fakeRadarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
	if !registered {
		t.Error("expected the movie to be registered with the fake Radarr")
	}
	// Relocate moves the whole contentPath directory (preserving its
	// basename) into the root folder, the same generic behavior it already
	// has for a directory-shaped Rename source — so the file lands at
	// <root>/<download-dir-name>/movie.mkv, not directly at <root>/movie.mkv.
	if _, err := os.Stat(filepath.Join(moviesRoot, filepath.Base(downloadDir), "movie.mkv")); err != nil {
		t.Errorf("expected the file to have been relocated into the root folder: %v", err)
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

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
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

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore, grabsStore))
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
