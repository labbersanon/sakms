package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestCatalogTitleFromGrab(t *testing.T) {
	if got := catalogTitleFromGrab(&grabs.Grab{Title: " Movie "}); got != "Movie" {
		t.Fatalf("title = %q", got)
	}
	if got := catalogTitleFromGrab(&grabs.Grab{
		Title: "Show", SeasonSpecified: true, SeasonNumber: 2, EpisodeNumber: 4,
	}); got != "Show S02E04" {
		t.Fatalf("episode title = %q", got)
	}
	if got := catalogTitleFromGrab(&grabs.Grab{
		Title: "Show", SeasonSpecified: true, SeasonNumber: 2,
	}); got != "Show S02" {
		t.Fatalf("season title = %q", got)
	}
	if got := catalogTitleFromGrab(&grabs.Grab{
		Title: "Show", SeasonSpecified: true, SeasonNumber: 0, EpisodeNumber: 3,
	}); got != "Show S00E03" {
		t.Fatalf("specials title = %q", got)
	}
	if got := catalogTitleFromGrab(&grabs.Grab{
		Title: "Furious", SeasonSpecified: true, SeasonNumber: 1, EpisodeNumber: 1,
		EpisodeTitle: "The Gorgon",
	}); got != "Furious S01E01 · The Gorgon" {
		t.Fatalf("named episode = %q", got)
	}
}

func TestTriedReleasesFromGrab(t *testing.T) {
	if got := triedReleasesFromGrab(&grabs.Grab{}); len(got) != 0 {
		t.Fatalf("empty grab should have no parks, got %#v", got)
	}
	got := triedReleasesFromGrab(&grabs.Grab{
		TriedReleaseKeys: "u:aaaaaaaaaaaaaaaa\nt:bbbbbbbbbbbbbbbb",
		RetryReason:      "articles missing",
	})
	if len(got) != 1 || !strings.Contains(got[0].Label, "1 prior") || got[0].Reason != "articles missing" {
		t.Fatalf("tried = %#v", got)
	}
}

func TestToUsenetDTODownload_MapsPopupDetails(t *testing.T) {
	age := int64(12)
	got := toUsenetDTODownload(usenet.Download{
		GID: "nzb-a", Status: "active", Filename: "ep.mkv",
		Dir: "/staging/nzb-a", Phase: "precheck", PhaseDone: 4, PhaseTotal: 20,
		SegmentDone: 3, SegmentTotal: 10, CurrentFile: "part028.rar",
		CurrentSeg: 59, CurrentSegTotal: 79, WaitReason: "slot full",
		SidecarAgeSec: &age, ActiveConns: 8, MaxConns: 50,
		FailingSegment: "segment 12: 430",
	})
	if got.Usenet == nil {
		t.Fatal("Usenet details missing")
	}
	if got.Usenet.StatDone != 4 || got.Usenet.StatTotal != 20 {
		t.Fatalf("STAT = %d/%d", got.Usenet.StatDone, got.Usenet.StatTotal)
	}
	if got.Usenet.CurrentFile != "part028.rar" || got.Usenet.CurrentSeg != 59 {
		t.Fatalf("current = %s %d", got.Usenet.CurrentFile, got.Usenet.CurrentSeg)
	}
	if got.Usenet.StagingPath != "/staging/nzb-a" {
		t.Fatalf("staging = %q", got.Usenet.StagingPath)
	}
}

func TestToDTODownload_MapsTorrentPopupDetails(t *testing.T) {
	got := toDTODownload(downloader.Download{
		GID: "g1", Status: "active", Filename: "movie.mkv", Dir: "/staging/g1",
		PeerCount: 6, Availability: 0.8, Uploaded: 100, Ratio: 0.5,
		InfoHash: "abc", Magnet: "magnet:?xt=urn:btih:abc",
		PiecesHave: 2, PiecesTotal: 4, PieceHeatmap: "09",
		TorrentFiles: []downloader.TorrentFile{{Path: "a.mkv", Length: 10, Completed: 5, Priority: "normal"}},
		Trackers:     []downloader.TrackerStatus{{URL: "udp://t", Status: "working"}},
	})
	if got.Torrent == nil {
		t.Fatal("Torrent details missing")
	}
	if got.Torrent.PeerCount != 6 || got.Torrent.InfoHash != "abc" {
		t.Fatalf("torrent details = %#v", got.Torrent)
	}
	if len(got.Torrent.Files) != 1 || got.Torrent.Files[0].Path != "a.mkv" {
		t.Fatalf("files = %#v", got.Torrent.Files)
	}
}

func TestReannounce_UsenetGIDRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/{gid}/reannounce", reannounceDownloadHandler(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/downloads/nzb-abc/reannounce", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRecheck_UsenetGIDRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/{gid}/recheck", recheckDownloadHandler(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/downloads/nzb-abc/recheck", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestFilePriority_UsenetGIDRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/downloads/{gid}/files", putDownloadFilePriorityHandler(nil))
	req := httptest.NewRequest(http.MethodPut, "/api/downloads/nzb-abc/files", strings.NewReader(`{"path":"a","priority":"skip"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUsenetDetailsEmpty_OmitsZero(t *testing.T) {
	got := toUsenetDTODownload(usenet.Download{GID: "g", Status: "active", Filename: "x"})
	if got.Usenet != nil {
		t.Fatalf("expected nil Usenet on empty engine row, got %#v", got.Usenet)
	}
	_ = apidto.DownloadProtocolUsenet
}

func TestUsenetDetailsFromEngine_SkipsDiskWalk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), []byte("hello-world"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := usenetDetailsFromEngine(usenet.Download{
		GID:      "nzb-1",
		Filename: "Show.mkv",
		Dir:      dir,
	})
	if got == nil {
		t.Fatal("want usenet details")
	}
	if got.BytesOnDisk != 0 {
		t.Fatalf("BytesOnDisk=%d want 0 — list/SSE must not WalkDir", got.BytesOnDisk)
	}
	if got.StagingPath != dir {
		t.Fatalf("StagingPath=%q want %q", got.StagingPath, dir)
	}
	if n := dirBytes(dir); n != 11 {
		t.Fatalf("fixture WalkDir=%d want 11 — mapper must not have used this", n)
	}
}

func TestTorrentDetailsFromEngine_SkipsDiskWalk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), []byte("hello-world"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := torrentDetailsFromEngine(downloader.Download{
		GID: "g1",
		Dir: dir,
	})
	if got == nil {
		t.Fatal("want torrent details")
	}
	if got.BytesOnDisk != 0 {
		t.Fatalf("BytesOnDisk=%d want 0 — list/SSE must not WalkDir", got.BytesOnDisk)
	}
	if got.SavePath != dir {
		t.Fatalf("SavePath=%q want %q", got.SavePath, dir)
	}
	if n := dirBytes(dir); n != 11 {
		t.Fatalf("fixture WalkDir=%d want 11 — mapper must not have used this", n)
	}
}
