package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
)

func TestParseImportMode(t *testing.T) {
	if _, err := parseImportMode("adult"); err == nil {
		t.Fatal("adult must be refused")
	}
	if _, err := parseImportMode(""); err == nil {
		t.Fatal("empty mode must be refused")
	}
	m, err := parseImportMode("movies")
	if err != nil || m != mode.Movies {
		t.Fatalf("movies: %v %q", err, m)
	}
}

func TestImportApplyDestRoot_KidsOnlyWhenConfigured(t *testing.T) {
	if got := importApplyDestRoot("/evil", "/media/movies", "/media/kids"); got != "/media/movies" {
		t.Fatalf("untrusted dest = %q", got)
	}
	if got := importApplyDestRoot("/media/kids", "/media/movies", "/media/kids"); got != "/media/kids" {
		t.Fatalf("kids dest = %q", got)
	}
	if got := importApplyDestRoot("", "/media/movies", "/media/kids"); got != "/media/movies" {
		t.Fatalf("empty requested = %q", got)
	}
}

func TestManualImportScan_RejectsPathOutsideRoots(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	body, _ := json.Marshal(manualImportScanRequest{Mode: "movies", Path: "/etc"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/import/scan", bytes.NewReader(body))
	manualImportScanHandler(http.DefaultClient, nil, nil, settingsStore, libStore, nil)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
}

func TestManualImportScan_RejectsAdult(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	body, _ := json.Marshal(manualImportScanRequest{Mode: "adult", Path: "/downloads"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/import/scan", bytes.NewReader(body))
	manualImportScanHandler(http.DefaultClient, nil, nil, settingsStore, libStore, nil)(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Movies and Series") {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
}

func TestManualImportApply_MovesFileIntoLibrary(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	srcFile := filepath.Join(source, "movie.mkv")
	if err := os.WriteFile(srcFile, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, source)

	connStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, dest); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(manualImportApplyRequest{
		Mode: "movies",
		Items: []manualImportItem{{
			SourcePath: srcFile,
			SourceName: "movie.mkv",
			DestRoot:   dest,
			Title:      "The Matrix",
			Year:       1999,
			TMDBID:     603,
			Status:     "pending",
			Mode:       "movies",
		}},
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/import/apply", bytes.NewReader(body))
	manualImportApplyHandler(http.DefaultClient, connStore, nil, settingsStore, libStore, nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp manualImportApplyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("results = %+v", resp.Results)
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move, stat err=%v", err)
	}
	item, err := libStore.GetByTMDBID(ctx, mode.Movies, 603)
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	if item.RootFolderPath != dest {
		t.Fatalf("library root = %q, want %q", item.RootFolderPath, dest)
	}
	if _, err := os.Stat(item.FilePath); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	wantName := naming.MovieFileName(naming.Jellyfin, "The Matrix", 1999, 603, ".mkv")
	if filepath.Base(item.FilePath) != wantName {
		t.Fatalf("dest name = %q, want %q", filepath.Base(item.FilePath), wantName)
	}
}

func TestManualImportApply_RejectsUnmatched(t *testing.T) {
	source := t.TempDir()
	srcFile := filepath.Join(source, "unknown.mkv")
	if err := os.WriteFile(srcFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, source)

	connStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(manualImportApplyRequest{
		Mode: "movies",
		Items: []manualImportItem{{
			SourcePath: srcFile,
			SourceName: "unknown.mkv",
			Status:     "unmatched",
			Mode:       "movies",
		}},
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/import/apply", bytes.NewReader(body))
	manualImportApplyHandler(http.DefaultClient, connStore, nil, settingsStore, libStore, nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp manualImportApplyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].OK {
		t.Fatalf("expected unmatched refusal, got %+v", resp.Results)
	}
	if _, err := os.Stat(srcFile); err != nil {
		t.Fatalf("source should remain: %v", err)
	}
	items, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("library should stay empty, got %+v", items)
	}
}

func TestManualImportApply_IgnoresClientDestOutsideKids(t *testing.T) {
	if got := importApplyDestRoot("/tmp/evil", "/media/movies", ""); got != "/media/movies" {
		t.Fatalf("got %q", got)
	}
}

func TestManualImportItemToProposal_RequiresBrowsableSource(t *testing.T) {
	_, err := importItemToProposal(manualImportItem{
		SourcePath: "/etc/passwd",
		Title:      "X",
		TMDBID:     1,
	}, mode.Movies, "/media/movies")
	if err == nil {
		t.Fatal("expected browsable-path refusal")
	}
}
