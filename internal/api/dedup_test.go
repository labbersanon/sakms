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
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
)

func writeTestVideoFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// fakeDedupProber maps a video file path to a canned mediainfo.Probe result
// — Dedup always ffprobes real files directly (see internal/dedup's doc
// comment), so end-to-end tests fake that one boundary rather than needing
// a real ffprobe binary and real encoded video content.
type fakeDedupProber struct {
	byPath map[string]*mediainfo.Probe
}

func (f *fakeDedupProber) Probe(ctx context.Context, path string) (*mediainfo.Probe, error) {
	p, ok := f.byPath[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return p, nil
}

// TestDedupWorkflow_ScanThenApply_EndToEnd exercises the full Dedup loop
// against SAK's real HTTP handlers, a real migrated SQLite database, a
// fake Radarr, and real on-disk files — same rigor as the Rename and Purge
// end-to-end tests.
func TestDedupWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Movies", "Some Movie (2020)")
	orphanDir := filepath.Join(dir, "Movies", "Some.Movie.2020.1080p.BluRay.x264-GROUP")
	trackedFile := writeTestVideoFile(t, trackedDir, "movie.mkv", 10)
	orphanFile := writeTestVideoFile(t, orphanDir, "movie.mkv", 10)

	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + filepath.Join(dir, "Movies") + `","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"Some.Movie.2020.1080p.BluRay.x264-GROUP","path":"` + orphanDir + `","relativePath":"Some.Movie.2020.1080p.BluRay.x264-GROUP"}
			]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			w.Write([]byte(`[{"id":9,"title":"Some Movie","path":"` + trackedDir + `","rootFolderPath":"` + filepath.Join(dir, "Movies") + `","tmdbId":42,"qualityProfileId":4}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			w.Write([]byte(`{"id":55}`))
		case r.URL.Path == "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"Some Movie","year":2020,"tmdbId":42}]`))
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD-1080p"}]`))
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodDelete:
			// The losing tracked candidate gets removed.
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", fakeRadarr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, settingsStore, grabsStore))
	defer srv.Close()

	scanResp, err := http.Post(srv.URL+"/api/modes/movies/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	if len(scanned) != 1 || len(scanned[0].Candidates) != 2 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	listResp, err := http.Get(srv.URL + "/api/modes/movies/dedup/proposals")
	if err != nil {
		t.Fatalf("list proposals failed: %v", err)
	}
	defer listResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(listResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].ID != scanned[0].ID {
		t.Fatalf("expected the dedup queue to reflect what scan staged, got %+v", listed)
	}

	// Apply with no body: auto-resolve by quality (the precomputed winner —
	// the higher-resolution orphan, which isn't tracked yet).
	applyResp, err := http.Post(
		srv.URL+"/api/proposals/"+strconv.FormatInt(scanned[0].ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(applyResp.Body)
		t.Fatalf("expected 200 from apply, got %d: %s", applyResp.StatusCode, body)
	}
	var applied proposals.Proposal
	json.NewDecoder(applyResp.Body).Decode(&applied)
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the proposal to come back Applied, got %+v", applied)
	}
	if _, err := os.Stat(trackedFile); err != nil {
		t.Errorf("expected the losing tracked candidate's local file to remain untouched (removal goes through Radarr's own DeleteTracked API call, never a direct filesystem delete for a tracked candidate): %v", err)
	}
}
