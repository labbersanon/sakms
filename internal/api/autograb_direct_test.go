package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

const feedMagnet = "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"

// minimalNZB is just enough NZB 1.1 XML for usenet.AddNZB to parse and start a
// (harmless, since no NNTP server is wired) background download — the direct-
// enclosure test below only needs AddNZB to accept and assign a GID, never to
// actually complete a retrieval.
const minimalNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file subject="a.release.name" date="1700000000" groups="alt.binaries.test">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1000" number="1">abc123@example</segment></segments>
  </file>
</nzb>`

// newAdultDirectGrabUsenetServer is newAdultDirectGrabServer's usenet twin: a
// real (zero-NNTP-server) *usenet.Manager wired into NewMux as nzb, plus the
// grabsStore returned directly so tests can inspect fields the wire DTO never
// exposes (DownloadURL is json:"-" — see grabs.Grab's doc comment).
func newAdultDirectGrabUsenetServer(t *testing.T) (*httptest.Server, *grabs.Store, *settings.Store) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := settingsStore.Set(context.Background(), adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("setting adult root folder: %v", err)
	}
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir(), HTTPClient: testHTTPClient()})
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nzb, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, grabsStore, settingsStore
}

// TestAutoGrabHandler_DirectGrabUsenet_SharesRetrievalAndRecordsDownloadURL is
// BE-8's shared-path test (spec AC 16 / traceability table): an RSS/feed-
// sourced item carrying a usenet enclosure reaches the exact same
// dispatchToDownloadClient -> usenet.Manager.AddNZB retrieval a Request-path
// grab uses (internal/api/search.go's grabHandler and RunAutoGrab both call
// the identical dispatchToDownloadClient) — there is no separate feed-only
// usenet code path and no per-feed subscription selection anywhere in the
// call chain (AddNZB takes no subscription argument; internal/usenet.Manager
// fans a download out across every configured pool itself).
//
// It also guards the retry-participation half of the AC: the direct-enclosure
// branch (grabDirectEnclosure) must record DownloadURL so a later ASYNCHRONOUS
// retrieval failure (parkGrabForRetry, reached from checkImportHandler for ANY
// nzb-prefixed grab regardless of origin) parks a pending_retry row a future
// retry cycle can actually resubmit from — a feed item has no TMDB/Prowlarr
// identity to re-search with, so its enclosure URL is its only provenance.
func TestAutoGrabHandler_DirectGrabUsenet_SharesRetrievalAndRecordsDownloadURL(t *testing.T) {
	nzbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(minimalNZB))
	}))
	defer nzbSrv.Close()

	srv, grabsStore, settingsStore := newAdultDirectGrabUsenetServer(t)

	body, _ := json.Marshal(apidto.AutoGrabRequest{
		Title: "Feed Scene", DownloadURL: nzbSrv.URL, DownloadProtocol: "usenet",
	})
	resp, err := http.Post(srv.URL+"/api/modes/adult/autograb", "application/json", bytes.NewReader(body))
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
	if !out.Grabbed || out.Grab == nil {
		t.Fatalf("expected a grab, got %+v", out)
	}
	if out.Grab.DownloadClient != "nntp" {
		t.Errorf("expected the shared usenet engine (nntp), got %q", out.Grab.DownloadClient)
	}

	// DownloadGID and DownloadURL are deliberately not on the wire DTO
	// (json:"-" on DownloadURL; DownloadGID isn't mapped by toDTOGrab at all),
	// so inspect the persisted grabs.Grab row directly.
	ctx := context.Background()
	created, err := grabsStore.Get(ctx, out.Grab.ID)
	if err != nil {
		t.Fatalf("loading created grab: %v", err)
	}
	if !strings.HasPrefix(created.DownloadGID, "nzb-") {
		t.Errorf("expected a usenet.Manager-assigned GID, got %q", created.DownloadGID)
	}
	if created.DownloadURL != nzbSrv.URL {
		t.Fatalf("grabDirectEnclosure must record DownloadURL so a retry can resubmit it; got %q, want %q", created.DownloadURL, nzbSrv.URL)
	}

	// Simulate the async retrieval failure path (checkImportHandler's
	// nzb-prefixed branch) and confirm the parked row keeps its DownloadURL —
	// exactly what a future retry cycle needs.
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	if err := parkGrabForRetry(ctx, deps, out.Grab.ID, articlesUnavailableReason); err != nil {
		t.Fatalf("parking for retry: %v", err)
	}

	parked, err := grabsStore.Get(ctx, out.Grab.ID)
	if err != nil {
		t.Fatalf("loading parked grab: %v", err)
	}
	if parked.Status != grabs.PendingRetry {
		t.Fatalf("expected pending_retry, got %q", parked.Status)
	}
	if parked.RetryAfter == "" {
		t.Error("expected RetryAfter to be set so DueForRetry can find this row")
	}
	if parked.DownloadURL != nzbSrv.URL {
		t.Errorf("parking for retry must not lose the feed item's DownloadURL — the only provenance a retry can resubmit from; got %q", parked.DownloadURL)
	}
}

// TestAutoGrabHandler_DirectGrabUsenet_NoManagerConfigured proves the feed
// item's usenet protocol actually routes through dispatchToDownloadClient's
// Usenet branch (not some separate, feed-only path that would silently
// succeed or fail differently): with nzb == nil, it gets the exact same "add
// a Usenet subscription" 400 any usenet grab gets when no engine is running.
func TestAutoGrabHandler_DirectGrabUsenet_NoManagerConfigured(t *testing.T) {
	srv := newAdultDirectGrabServer(t) // nzb == nil in this helper

	body, _ := json.Marshal(apidto.AutoGrabRequest{
		Title: "Feed Scene", DownloadURL: "http://example.invalid/release.nzb", DownloadProtocol: "usenet",
	})
	resp, err := http.Post(srv.URL+"/api/modes/adult/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 (no usenet engine configured), got %d", resp.StatusCode)
	}
}

// TestRssFeedItemDTO_NoSubscriptionField is the AC's "grep assertion": spec
// Goal 8 requires RSS usenet items to use the shared multi-subscription
// manager with NO per-feed subscription picker anywhere in RSS admin. This
// asserts the wire DTO a resolved feed item sends to the frontend carries no
// field that would let one be chosen — a structural regression guard cheaper
// than a real grep, and one that survives a field rename a text grep would
// miss.
func TestRssFeedItemDTO_NoSubscriptionField(t *testing.T) {
	typ := reflect.TypeOf(apidto.RssFeedItem{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "subscription") || strings.Contains(name, "pool") || strings.Contains(name, "server") {
			t.Errorf("apidto.RssFeedItem has field %q — RSS items must not carry a per-feed subscription/pool selection (spec Goal 8)", typ.Field(i).Name)
		}
	}
}

// TestGrabHandler_RssResolvedUsenetItem_SharesRetrieval is BE-8's actual
// shared-path test for the internal/rssfeeds RSS admin surface (rss_feeds.go's
// resolveRssFeedHandler): the frontend's RssFeedCard.tsx grabs a resolved feed
// item via manualGrab -> POST /api/modes/{mode}/search/grab, NOT via
// /autograb's direct-enclosure branch — the plan's BE-8 section cites the
// wrong call chain (autograb.go:133/grabDirectEnclosure) for this surface.
// grabHandler (search.go) is the SAME endpoint the auto-grab manual pick list
// posts to (see api/grab.ts's ManualGrabBody comment) and calls the identical
// dispatchToDownloadClient -> usenet.Manager.AddNZB used by every other grab
// path, so a feed item posted here proves "the same retrieval code as a
// Request" without needing a picker of any kind — AddNZB takes no
// subscription argument, and internal/usenet.Manager fans a download out
// across every configured pool internally.
func TestGrabHandler_RssResolvedUsenetItem_SharesRetrieval(t *testing.T) {
	nzbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(minimalNZB))
	}))
	defer nzbSrv.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir(), HTTPClient: testHTTPClient()})
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nzb, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Field set mirrors exactly what RssFeedCard.tsx's manualGrab sends: title,
	// indexer (the feed's own Title, per resolveRssFeedHandler's doc comment),
	// protocol (the feed's admin-set rssfeeds.Usenet, same string value as
	// prowlarr.Usenet), the item's enclosure URL, and a root folder resolved
	// client-side via libraryRootFolder.
	body, _ := json.Marshal(grabRequest{
		Title: "Some RSS Release", Indexer: "My Usenet Feed", Protocol: "usenet",
		DownloadURL: nzbSrv.URL, RootFolderPath: "/tv",
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
	if g.DownloadClient != "nntp" {
		t.Errorf("expected the shared usenet engine (nntp), got %q", g.DownloadClient)
	}
	if !strings.HasPrefix(g.DownloadGID, "nzb-") {
		t.Errorf("expected a usenet.Manager-assigned GID, got %q", g.DownloadGID)
	}
	if g.Indexer != "My Usenet Feed" {
		t.Errorf("expected the feed's own title as Indexer, got %q", g.Indexer)
	}

	// KNOWN GAP, not fixed here (search.go is out of this task's scope — see
	// the executor's final report): grabHandler's Create call
	// (search.go:368-373) does not persist DownloadURL, unlike RunAutoGrab's
	// (autograb_shared.go:255-261) and grabDirectEnclosure's (autograb.go,
	// fixed by this same change). So an RSS-admin grab that later needs an
	// async retry (checkImportHandler -> parkGrabForRetry) will park with an
	// empty download_url_encrypted — nothing for a future retry cycle to
	// resubmit. Confirmed via direct store inspection below; intentionally not
	// asserted as a failure so this test documents, rather than blocks on, a
	// bug that belongs to search.go's owner.
	created, err := grabsStore.Get(context.Background(), g.ID)
	if err != nil {
		t.Fatalf("loading created grab: %v", err)
	}
	if created.DownloadURL != "" {
		t.Logf("grabHandler now records DownloadURL (%q) — the known gap noted above appears to be fixed; update this test's comment/assert accordingly", created.DownloadURL)
	}
}

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
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil)
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
