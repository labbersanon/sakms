package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

const feedMagnet = "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"

// newAdultDirectGrabServer builds a mux with a real downloader and an Adult root
// folder configured, but DELIBERATELY no Prowlarr connection (sess.Prowlarr is
// nil) and no TMDB — the exact Prowlarr-less install the direct-grab parity
// (C1/D4) must serve. A DownloadURL-bearing item must grab anyway.
func newAdultDirectGrabServer(t *testing.T) *httptest.Server {
	t.Helper()
	dl := newTestDownloader("gid-feed", t.TempDir())
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := settingsStore.Set(context.Background(), adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("setting adult root folder: %v", err)
	}
	mux := NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, dl, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAutoGrabHandler_DirectGrabSkipsProwlarr proves the single endpoint's C1/D4
// direct-grab path: a request carrying a DownloadURL dispatches straight to the
// download client and grabs even with nil Prowlarr — no Prowlarr search, so the
// old sess.Prowlarr==nil guard is correctly relaxed for this case.
func TestAutoGrabHandler_DirectGrabSkipsProwlarr(t *testing.T) {
	srv := newAdultDirectGrabServer(t)

	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: "Feed Scene", DownloadURL: feedMagnet, DownloadProtocol: "torrent"})
	resp, err := http.Post(srv.URL+"/api/modes/adult/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (direct grab succeeds with nil Prowlarr), got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !out.Grabbed || out.Grab == nil {
		t.Fatalf("expected a grab, got %+v", out)
	}
	if out.Grab.RootFolderPath != "/adult" {
		t.Errorf("expected the adult root folder resolved server-side, got %q", out.Grab.RootFolderPath)
	}
	if out.Grab.Indexer != "feed" {
		t.Errorf("expected a feed-sourced grab (Indexer=feed), got %q", out.Grab.Indexer)
	}
}

// TestAutoGrabBatchHandler_DirectGrabSkipsProwlarr is the bulk-path twin: the
// same direct-grab item, in a batch, on a Prowlarr-less install, grabs — proving
// the per-item guard relaxation (Low finding) and grabOneBatchItem's direct
// branch keep single/bulk symmetric (C1). Without both fixes the bulk path would
// reject the item ("prowlarr isn't configured") while the single path grabbed it.
func TestAutoGrabBatchHandler_DirectGrabSkipsProwlarr(t *testing.T) {
	srv := newAdultDirectGrabServer(t)

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Feed Scene", DownloadURL: feedMagnet, DownloadProtocol: "torrent"}},
	}}
	resp, out := postBatch(t, srv.URL, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(out.Results), out.Results)
	}
	r := out.Results[0]
	if !r.Grabbed || r.Grab == nil || r.Error != "" {
		t.Fatalf("expected the direct-grab item to grab with nil Prowlarr in a batch, got %+v", r)
	}
	if r.Grab.RootFolderPath != "/adult" || r.Grab.Indexer != "feed" {
		t.Errorf("expected a feed-sourced grab under /adult, got %+v", r.Grab)
	}
}
