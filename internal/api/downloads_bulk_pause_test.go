package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

func newSettingsStore(t *testing.T) *settings.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return settings.New(sqlDB)
}

// TestBulkCancel_SkipAndContinue proves the bulk-cancel endpoint cancels several
// torrent downloads (deleting their files) and that one bad GID is reported as a
// failure without blocking the rest.
func TestBulkCancel_SkipAndContinue(t *testing.T) {
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)

	f1 := filepath.Join(staging, "one.mkv")
	f2 := filepath.Join(staging, "two.mkv")
	for _, f := range []string{f1, f2} {
		if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	dl.SeedState(downloader.Download{GID: "g1", Status: "active", Dir: staging, Files: []string{f1}})
	dl.SeedState(downloader.Download{GID: "g2", Status: "active", Dir: staging, Files: []string{f2}})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/cancel-batch", bulkCancelHandler(dl, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(apidto.BulkCancelRequest{GIDs: []string{"g1", "missing", "g2"}})
	resp, err := http.Post(srv.URL+"/api/downloads/cancel-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.BulkCancelResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 3 {
		t.Fatalf("expected 3 results, got %d: %+v", len(out.Results), out.Results)
	}
	if !out.Results[0].OK || out.Results[1].OK || !out.Results[2].OK {
		t.Errorf("expected ok,fail,ok, got %+v", out.Results)
	}
	if out.Results[1].Error == "" {
		t.Errorf("the missing GID should carry an error, got %+v", out.Results[1])
	}
	// Both real cancels deleted their files and left the queue.
	for _, f := range []string{f1, f2} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("cancelled file %s should be gone, stat err = %v", f, err)
		}
	}
	if len(dl.List()) != 0 {
		t.Errorf("both cancelled downloads should have left the queue, got %+v", dl.List())
	}
}

// TestPauseGate_BlocksAndAllows proves the global-pause gate at
// dispatchToDownloadClient genuinely blocks a new dispatch with the specific
// paused error while paused, and genuinely allows dispatch again once un-paused.
func TestPauseGate_BlocksAndAllows(t *testing.T) {
	settingsStore := newSettingsStore(t)
	ctx := context.Background()
	dl := downloader.NewForTesting(t.TempDir())
	dl.SetTestNextGID("g1")
	sess := &mode.Session{Downloader: dl}

	// Unpaused (default): a torrent dispatch succeeds.
	_, gid, status, err := dispatchToDownloadClient(ctx, settingsStore, sess, mode.Movies, nil, "torrent", "magnet:x", "Title")
	if err != nil || status != http.StatusOK || gid == "" {
		t.Fatalf("unpaused dispatch should succeed, got gid=%q status=%d err=%v", gid, status, err)
	}
	before := len(dl.List())

	// Paused: dispatch is blocked with the specific error and adds no download.
	if err := settingsStore.SetBool(ctx, downloadsGlobalPausedKey, true); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	_, _, status, err = dispatchToDownloadClient(ctx, settingsStore, sess, mode.Movies, nil, "torrent", "magnet:y", "Title2")
	if err != errDownloadsPaused {
		t.Errorf("paused dispatch should return errDownloadsPaused, got %v", err)
	}
	if status != http.StatusLocked {
		t.Errorf("paused dispatch should return 423 Locked, got %d", status)
	}
	if len(dl.List()) != before {
		t.Errorf("paused dispatch must not add a download, count %d -> %d", before, len(dl.List()))
	}

	// Un-paused: dispatch works again.
	if err := settingsStore.SetBool(ctx, downloadsGlobalPausedKey, false); err != nil {
		t.Fatalf("clear paused: %v", err)
	}
	_, _, status, err = dispatchToDownloadClient(ctx, settingsStore, sess, mode.Movies, nil, "torrent", "magnet:z", "Title3")
	if err != nil || status != http.StatusOK {
		t.Errorf("un-paused dispatch should succeed again, got status=%d err=%v", status, err)
	}
}

// TestPutPauseState_PausesActiveDownloads proves setting the global pause flag
// pauses every currently-active download via the per-item Pause path.
func TestPutPauseState_PausesActiveDownloads(t *testing.T) {
	settingsStore := newSettingsStore(t)
	dl := downloader.NewForTesting(t.TempDir())
	dl.SeedState(downloader.Download{GID: "a", Status: "active"})
	dl.SeedState(downloader.Download{GID: "b", Status: "waiting"})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/downloads/pause-state", getPauseStateHandler(settingsStore))
	mux.HandleFunc("PUT /api/downloads/pause-state", putPauseStateHandler(settingsStore, dl, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(apidto.DownloadPauseState{Paused: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/downloads/pause-state", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	for _, d := range dl.List() {
		if d.Status != "paused" {
			t.Errorf("download %s should be paused, got %q", d.GID, d.Status)
		}
	}

	// GET reflects the persisted flag.
	gresp, err := http.Get(srv.URL + "/api/downloads/pause-state")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer gresp.Body.Close()
	var state apidto.DownloadPauseState
	if err := json.NewDecoder(gresp.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !state.Paused {
		t.Errorf("pause-state should read back true, got %+v", state)
	}
}
