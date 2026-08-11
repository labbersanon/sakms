package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
)

// newAdultDedupGrabServer is newAdultDirectGrabServer with a handle to the
// downloader returned, so a test can change the GID AddTorrent hands back
// between grabs — the difference between the download client re-deduping the
// same infohash to ONE GID (a duplicate grab) and two genuinely distinct
// downloads (two GIDs).
func newAdultDedupGrabServer(t *testing.T) (*httptest.Server, *downloader.Manager) {
	t.Helper()
	dl := newTestDownloader("gid-A", t.TempDir())
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := settingsStore.Set(context.Background(), adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("setting adult root folder: %v", err)
	}
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dl
}

func postDirectGrab(t *testing.T, url, title, magnet string) apidto.AutoGrabResponse {
	t.Helper()
	body, _ := json.Marshal(apidto.AutoGrabRequest{Title: title, DownloadURL: magnet, DownloadProtocol: "torrent"})
	resp, err := http.Post(url+"/api/modes/adult/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return out
}

func adultGrabCount(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url + "/api/modes/adult/grabs")
	if err != nil {
		t.Fatalf("GET grabs failed: %v", err)
	}
	defer resp.Body.Close()
	var list []struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding grabs: %v", err)
	}
	return len(list)
}

// TestAutoGrabHandler_DuplicateDownloadGIDDoesNotDuplicateRow proves the
// idempotency guard: two grabs of the SAME release — the download client dedupes
// by infohash, so AddTorrent returns the SAME GID both times — record exactly
// ONE grabs row. The second grab reports "already grabbing this release" instead
// of creating a duplicate that would strand at 'queued' forever.
func TestAutoGrabHandler_DuplicateDownloadGIDDoesNotDuplicateRow(t *testing.T) {
	srv, _ := newAdultDedupGrabServer(t)

	first := postDirectGrab(t, srv.URL, "Feed Scene", feedMagnet)
	if !first.Grabbed || first.Grab == nil {
		t.Fatalf("expected the first grab to succeed, got %+v", first)
	}

	second := postDirectGrab(t, srv.URL, "Feed Scene", feedMagnet)
	if second.Grabbed {
		t.Errorf("expected the second (duplicate) grab NOT to be reported as a fresh grab")
	}
	if second.Message != "already grabbing this release" {
		t.Errorf("expected an 'already grabbing' message, got %q", second.Message)
	}

	if n := adultGrabCount(t, srv.URL); n != 1 {
		t.Errorf("expected exactly 1 grabs row after a duplicate grab, got %d", n)
	}
}

// TestAutoGrabBatch_DuplicateItemReportedAsAlreadyGrabbing proves the batch
// path gives a duplicate its own distinct three-state-honest outcome: the
// second item (same release, same GID) comes back AlreadyGrabbing=true, NOT
// counted as a fresh Grabbed, and records no duplicate row.
func TestAutoGrabBatch_DuplicateItemReportedAsAlreadyGrabbing(t *testing.T) {
	srv, _ := newAdultDedupGrabServer(t)

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Feed Scene", DownloadURL: feedMagnet, DownloadProtocol: "torrent"}},
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Feed Scene", DownloadURL: feedMagnet, DownloadProtocol: "torrent"}},
	}}
	resp, out := postBatch(t, srv.URL, req)
	defer resp.Body.Close()

	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(out.Results), out.Results)
	}
	if !out.Results[0].Grabbed {
		t.Errorf("expected the first item to grab, got %+v", out.Results[0])
	}
	if out.Results[1].Grabbed {
		t.Errorf("expected the second (duplicate) item NOT counted as a fresh grab, got %+v", out.Results[1])
	}
	if !out.Results[1].AlreadyGrabbing {
		t.Errorf("expected the second item AlreadyGrabbing=true, got %+v", out.Results[1])
	}
	if n := adultGrabCount(t, srv.URL); n != 1 {
		t.Errorf("expected exactly 1 grabs row after a duplicate batch item, got %d", n)
	}
}

// TestAutoGrabHandler_DistinctDownloadGIDsBothCreated proves the guard does not
// over-block: two genuinely distinct downloads (different GIDs) each record their
// own grabs row.
func TestAutoGrabHandler_DistinctDownloadGIDsBothCreated(t *testing.T) {
	srv, dl := newAdultDedupGrabServer(t)

	dl.SetTestNextGID("gid-A")
	if out := postDirectGrab(t, srv.URL, "Scene A", feedMagnet); !out.Grabbed {
		t.Fatalf("expected Scene A to grab, got %+v", out)
	}

	dl.SetTestNextGID("gid-B")
	if out := postDirectGrab(t, srv.URL, "Scene B", "magnet:?xt=urn:btih:00000000000000000000000000000000000000ff"); !out.Grabbed {
		t.Fatalf("expected Scene B (a distinct download) to grab, got %+v", out)
	}

	if n := adultGrabCount(t, srv.URL); n != 2 {
		t.Errorf("expected 2 distinct grabs rows for 2 distinct downloads, got %d", n)
	}
}
