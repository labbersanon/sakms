package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
)

// fakeRadarrHandler serves just enough of Radarr's API for a Scan followed
// by an Apply to succeed end to end.
func fakeRadarrHandler(t *testing.T, addedID int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","path":"/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","relativePath":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"}
			]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"id": addedID})
		case r.URL.Path == "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"A Beautiful Mind","year":2001,"tmdbId":453}]`))
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD-1080p"}]`))
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

// TestRenameWorkflow_ScanThenApply_EndToEnd exercises the full staged-review
// loop the design spec describes: Scan populates the queue, the queue is
// visible via List, and Apply commits exactly the one proposal a human
// approved — hitting Tidyarr's real HTTP handlers, a real migrated SQLite
// database, and a fake Radarr, not any package in isolation.
func TestRenameWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	fakeRadarr := httptest.NewServer(fakeRadarrHandler(t, 55))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", fakeRadarr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
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

	listResp, err := http.Get(srv.URL + "/api/modes/movies/rename/proposals")
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
	if applied.Status != proposals.Applied || applied.TrackedID != 55 {
		t.Fatalf("expected the proposal to come back Applied with trackedId=55, got %+v", applied)
	}
}

func TestDismissProposalHandler_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	saved, err := propStore.ReplacePending(context.Background(), "movies", proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "x", SourcePath: "/x", RootFolderPath: "/media/Movies", Title: "X"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
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
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
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

func TestScanHandler_ModeNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/movies/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when radarr isn't configured yet, got %d", resp.StatusCode)
	}
}
