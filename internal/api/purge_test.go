package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/proposals"
)

// fakeRadarrTagsHandler serves just enough of Radarr's API for a Purge Scan
// followed by an Apply to succeed end to end: tracked items with tags, a
// tag catalog to resolve labels, and a DELETE endpoint that records what it
// was asked to remove.
func fakeRadarrTagsHandler(t *testing.T, deletedPaths *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			w.Write([]byte(`[
				{"id":1,"title":"Vanilla Movie","path":"/media/Movies/Vanilla Movie","rootFolderPath":"/media/Movies","tags":[9]},
				{"id":2,"title":"Flagged Movie","path":"/media/Movies/Flagged Movie","rootFolderPath":"/media/Movies","tags":[1]}
			]`))
		case r.URL.Path == "/api/v3/tag":
			w.Write([]byte(`[{"id":1,"label":"BDSM"},{"id":9,"label":"family-friendly"}]`))
		case r.URL.Path == "/api/v3/movie/2" && r.Method == http.MethodDelete:
			*deletedPaths = append(*deletedPaths, r.URL.String())
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

// TestPurgeWorkflow_AllowlistThenScanThenApply_EndToEnd exercises the full
// Purge loop: add a tag to the allowlist, Scan matches it against a live
// Radarr's tracked items and tags, and Apply deletes exactly the one
// approved proposal — hitting SAK's real HTTP handlers and a real
// migrated SQLite database throughout.
func TestPurgeWorkflow_AllowlistThenScanThenApply_EndToEnd(t *testing.T) {
	var deletedPaths []string
	fakeRadarr := httptest.NewServer(fakeRadarrTagsHandler(t, &deletedPaths))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", fakeRadarr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	// Add a tag to the allowlist via the API, not directly on the store.
	addBody, _ := json.Marshal(addAllowlistTagRequest{Tag: "BDSM"})
	addResp, err := http.Post(srv.URL+"/api/modes/movies/purge/allowlist", "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("add allowlist tag failed: %v", err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 adding an allowlist tag, got %d", addResp.StatusCode)
	}

	listAllowResp, err := http.Get(srv.URL + "/api/modes/movies/purge/allowlist")
	if err != nil {
		t.Fatalf("list allowlist failed: %v", err)
	}
	defer listAllowResp.Body.Close()
	var allowed []string
	json.NewDecoder(listAllowResp.Body).Decode(&allowed)
	if len(allowed) != 1 || allowed[0] != "BDSM" {
		t.Fatalf("expected allowlist to contain BDSM, got %v", allowed)
	}

	scanResp, err := http.Post(srv.URL+"/api/modes/movies/purge/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	if len(scanned) != 1 || scanned[0].Title != "Flagged Movie" || scanned[0].TrackedID != 2 {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}

	listResp, err := http.Get(srv.URL + "/api/modes/movies/purge/proposals")
	if err != nil {
		t.Fatalf("list proposals failed: %v", err)
	}
	defer listResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(listResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].ID != scanned[0].ID {
		t.Fatalf("expected the purge queue to reflect what scan staged, got %+v", listed)
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
	json.NewDecoder(applyResp.Body).Decode(&applied)
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the proposal to come back Applied, got %+v", applied)
	}
	if len(deletedPaths) != 1 || deletedPaths[0] != "/api/v3/movie/2?deleteFiles=true" {
		t.Fatalf("expected exactly one delete call for the approved item, got %v", deletedPaths)
	}

	// Remove the tag again via the API.
	delReq, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/api/modes/movies/purge/allowlist/"+url.PathEscape("BDSM"), nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("remove allowlist tag failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 removing an allowlist tag, got %d", delResp.StatusCode)
	}
	afterAllowResp, err := http.Get(srv.URL + "/api/modes/movies/purge/allowlist")
	if err != nil {
		t.Fatalf("list allowlist failed: %v", err)
	}
	defer afterAllowResp.Body.Close()
	var afterAllowed []string
	json.NewDecoder(afterAllowResp.Body).Decode(&afterAllowed)
	if len(afterAllowed) != 0 {
		t.Fatalf("expected empty allowlist after removal, got %v", afterAllowed)
	}
}

func TestAddAllowlistTagHandler_RequiresTag(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	body, _ := json.Marshal(addAllowlistTagRequest{})
	resp, err := http.Post(srv.URL+"/api/modes/movies/purge/allowlist", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing tag, got %d", resp.StatusCode)
	}
}

func TestPurgeScanHandler_ModeNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/movies/purge/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when radarr isn't configured yet, got %d", resp.StatusCode)
	}
}
