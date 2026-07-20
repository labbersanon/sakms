package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestPurgeWorkflow_AllowlistThenScanThenApply_EndToEnd exercises the full
// Purge loop for Movies: add a tag to the allowlist, Scan matches it against
// libStore's own tagged items (no Radarr involved anymore), and Apply
// deletes exactly the one approved proposal — hitting SAK's real HTTP
// handlers, a real migrated SQLite database, and a real on-disk file.
func TestPurgeWorkflow_AllowlistThenScanThenApply_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	dir := t.TempDir()
	vanillaPath := dir + "/vanilla.mkv"
	flaggedPath := dir + "/flagged.mkv"
	if err := os.WriteFile(vanillaPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(flaggedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vanilla, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Vanilla Movie", FilePath: vanillaPath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, vanilla.ID, "family-friendly"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	flagged, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 2, Title: "Flagged Movie", FilePath: flaggedPath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, flagged.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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
	if len(scanned) != 1 || scanned[0].Title != "Flagged Movie" || scanned[0].TrackedID != int(flagged.ID) {
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
	if _, err := os.Stat(flaggedPath); !os.IsNotExist(err) {
		t.Errorf("expected the flagged movie's file to be deleted, stat returned: %v", err)
	}
	if _, err := os.Stat(vanillaPath); err != nil {
		t.Errorf("expected the vanilla movie's file to survive untouched, got: %v", err)
	}
	if _, err := libStore.Get(ctx, flagged.ID); err != library.ErrNotFound {
		t.Errorf("expected the flagged movie's library record to be deleted, got err=%v", err)
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
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
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

// TestPurgeScanHandler_NoConnectionNeeded proves no mode needs any *arr
// connection for Purge anymore — Movies/Series since their eliminations, and
// Adult since Stage 4's Whisparr elimination (its Scan is now the pure
// library-backed purge.ScanLibraryAdult, returning an empty queue for an
// empty library rather than 400-ing on a missing Whisparr).
func TestPurgeScanHandler_NoConnectionNeeded(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	for _, m := range []string{"movies", "series", "adult"} {
		resp, err := http.Post(srv.URL+"/api/modes/"+m+"/purge/scan", "application/json", nil)
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s with no *arr connection at all, got %d", m, resp.StatusCode)
		}
	}
}
