package api

// Claude 2026-08-11: §9.3 Adult autograb/batch tests (A2, A4, A5, A6).
// Reason: persistence feeder, indexer recording, identity gating, A4 retry
// characterisation — none of these paths existed before this task.
// Troubleshooting: if CacheHit tests fail with "fallback", check that
// seedAdultCacheRelease set DurationSeconds and that the title filter passes
// (TitleSimilarity("Some Scene", release.Title) >= 0.2).
// Review if: Adult release TTL window changes (FreshReleasesForScene query) or
// the quality scorer's unknown-runtime handling changes.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/settings"
)

// adultCacheTestIndexer is the indexer name a cached release carries — the
// real indexer from the persistence layer, distinct from "feed" (the plain RSS
// stamp) and "I" (the generic Prowlarr mock label used elsewhere).
const adultCacheTestIndexer = "NZBGeek"

// adultCacheMagnet is the enclosure URL the seeded cache release carries.
// It is distinct from feedMagnet so tests can assert which path won.
const adultCacheMagnet = "magnet:?xt=urn:btih:CACHEDABCDE1234567890abcdef1234567890abc"

// seedAdultCacheRelease persists one qualifying Adult release linked to
// sceneKey in the test ReleaseStore. The release title is crafted so that
// TitleSimilarity(sceneTitle, release.Title) >= titleSimilarityFloor and the
// studio name appears in the title (for the strong-identity tests).
//
// Release shape: 8 GB / x265 / 1080p / 50 seeders — clears every Low-tier
// floor at the 6000 s runtime the caller must set in DurationSeconds.
func seedAdultCacheRelease(t *testing.T, store *adultnewest.ReleaseStore, sceneTitle, sceneKey string) {
	t.Helper()
	ctx := context.Background()
	rel := prowlarr.Release{
		GUID:        "cache-scene-guid",
		Title:       "SomeStudio." + sceneTitle + ".1080p.WEB-DL.x265-GROUP",
		Indexer:     adultCacheTestIndexer,
		Protocol:    prowlarr.Torrent,
		Size:        8_000_000_000,
		Seeders:     50,
		DownloadURL: adultCacheMagnet,
	}
	if err := store.PersistReleases(ctx, sceneTitle, []prowlarr.Release{rel}, []string{sceneKey}); err != nil {
		t.Fatalf("seedAdultCacheRelease: %v", err)
	}
}

// newAdultCacheServer builds a NewMux with a pre-seeded Adult release cache
// and no Prowlarr — the core fixture for "dispatches without Prowlarr" tests.
// Returns the server, the grabsStore for inspection, and the settingsStore.
func newAdultCacheServer(t *testing.T, sceneTitle, sceneKey string) (*httptest.Server, *grabs.Store, *settings.Store, *adultnewest.ReleaseStore) {
	t.Helper()
	dl := newTestDownloader("gid-cache", t.TempDir())
	dl.EnableTestAutoGID()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, releaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	// No Prowlarr configured — proves cache path bypasses it.
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("setting adult root folder: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Adult), string(quality.Low)); err != nil {
		t.Fatalf("setting quality tier: %v", err)
	}
	seedAdultCacheRelease(t, releaseStore, sceneTitle, sceneKey)
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, grabsStore, settingsStore, releaseStore
}

// TestAutoGrabHandler_Adult_CacheHitDispatchesWithoutProwlarr is the §6.1
// "fresh cache dispatch" test: an Adult scene whose releases are already
// persisted dispatches straight to the download client WITHOUT a Prowlarr
// search — the persistence feeder fires, finds a qualifying cached release,
// and grabs it. No Prowlarr is configured; a search-path grab would fail.
//
// This is the §7.3 "no Prowlarr search on cache hit" assertion in handler form.
func TestAutoGrabHandler_Adult_CacheHitDispatchesWithoutProwlarr(t *testing.T) {
	const sceneTitle = "Some Scene"
	sceneKey := adultSceneKey("stashdb", "test-scene-001", sceneTitle)
	srv, grabsStore, _, _ := newAdultCacheServer(t, sceneTitle, sceneKey)

	body, _ := json.Marshal(apidto.AutoGrabRequest{
		Title:           sceneTitle,
		Studio:          "SomeStudio",
		Box:             "stashdb",
		SceneID:         "test-scene-001",
		DurationSeconds: 6000,
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
		t.Fatalf("decoding response: %v", err)
	}
	if !out.Grabbed || out.Grab == nil {
		t.Fatalf("expected a cache-sourced grab (no Prowlarr), got %+v", out)
	}
	if out.Grab.RootFolderPath != "/adult" {
		t.Errorf("expected root folder /adult, got %q", out.Grab.RootFolderPath)
	}
	// Confirm a grabs row was recorded.
	ctx := context.Background()
	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 grab row, got %d", len(list))
	}
}

// TestAutoGrabHandler_Adult_CacheGrabRecordsRealIndexer proves §7.3/A2: a grab
// sourced from the release cache records the cached release's own Prowlarr
// indexer (e.g. "NZBGeek"), NOT the plain-RSS stamp "feed". The indexer column
// on the grabs row is what operators see in the Grabs view and what the retry
// scheduler uses to surface origin information.
func TestAutoGrabHandler_Adult_CacheGrabRecordsRealIndexer(t *testing.T) {
	const sceneTitle = "Some Scene"
	sceneKey := adultSceneKey("stashdb", "test-scene-002", sceneTitle)
	srv, grabsStore, _, _ := newAdultCacheServer(t, sceneTitle, sceneKey)

	body, _ := json.Marshal(apidto.AutoGrabRequest{
		Title:           sceneTitle,
		Studio:          "SomeStudio",
		Box:             "stashdb",
		SceneID:         "test-scene-002",
		DurationSeconds: 6000,
	})
	resp, err := http.Post(srv.URL+"/api/modes/adult/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !out.Grabbed || out.Grab == nil {
		t.Fatalf("expected a cache-sourced grab, got %+v", out)
	}
	if out.Grab.Indexer == "feed" {
		t.Errorf("cache-sourced grab must record the real indexer (%q), not %q", adultCacheTestIndexer, out.Grab.Indexer)
	}
	if out.Grab.Indexer != adultCacheTestIndexer {
		t.Errorf("expected indexer %q, got %q", adultCacheTestIndexer, out.Grab.Indexer)
	}

	ctx := context.Background()
	row, err := grabsStore.Get(ctx, out.Grab.ID)
	if err != nil {
		t.Fatalf("loading grab row: %v", err)
	}
	if row.Indexer != adultCacheTestIndexer {
		t.Errorf("grabs row indexer = %q, want %q", row.Indexer, adultCacheTestIndexer)
	}
}

// TestAutoGrabHandler_Adult_CardEnclosureWinsOverCache proves §7.3: when a
// request carries its own DownloadURL (a plain RSS/feed card), the persistence
// feeder is bypassed entirely — the enclosure URL is used directly and Indexer
// is stamped "feed" (no cache, no indexer lookup). Even if a cached release
// exists for the same scene, the explicit DownloadURL takes precedence.
func TestAutoGrabHandler_Adult_CardEnclosureWinsOverCache(t *testing.T) {
	const sceneTitle = "Some Scene"
	sceneKey := adultSceneKey("stashdb", "test-scene-003", sceneTitle)
	// Seed a cache release — if the feeder fires incorrectly, the test would
	// grab the cache release and record adultCacheTestIndexer instead of "feed".
	srv, grabsStore, _, _ := newAdultCacheServer(t, sceneTitle, sceneKey)

	body, _ := json.Marshal(apidto.AutoGrabRequest{
		Title:            sceneTitle,
		Studio:           "SomeStudio",
		Box:              "stashdb",
		SceneID:          "test-scene-003",
		DurationSeconds:  6000,
		DownloadURL:      feedMagnet, // explicit enclosure — feeder must NOT fire
		DownloadProtocol: "torrent",
	})
	resp, err := http.Post(srv.URL+"/api/modes/adult/autograb", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	var out apidto.AutoGrabResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !out.Grabbed || out.Grab == nil {
		t.Fatalf("expected the direct-enclosure grab to succeed, got %+v", out)
	}
	if out.Grab.Indexer != "feed" {
		t.Errorf("a request carrying its own DownloadURL must stamp Indexer=\"feed\", got %q", out.Grab.Indexer)
	}

	ctx := context.Background()
	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 grab, got %d", len(list))
	}
	if list[0].Indexer != "feed" {
		t.Errorf("grabs row must record Indexer=\"feed\" for an RSS-enclosure grab, got %q", list[0].Indexer)
	}
}

// --- RunAutoGrab weak/strong identity tests (A6) ----------------------------

// TestRunAutoGrab_Adult_StrongIdentityDispatches is the A6 positive path:
// when the studio name appears in the release title, adultIdentityWeak returns
// false and RunAutoGrab dispatches the qualifying release to the download
// client — no staging, no pending_retry row.
func TestRunAutoGrab_Adult_StrongIdentityDispatches(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := newTestDownloader("gid-strong", t.TempDir())
	setAutoGrabToggle(t, settingsStore, true)
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("setting root folder: %v", err)
	}

	// Release title contains the studio name → strong identity.
	rel := prowlarr.Release{
		GUID: "1", Title: "HotStudios.Hot.Scene.1080p.WEB-DL.x265-GROUP", Indexer: "I",
		Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 50,
		DownloadURL: "magnet:?xt=urn:btih:STRONG1234567890abcdef1234567890abcdef12",
	}
	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		&mode.Session{Mode: mode.Adult, Downloader: dl},
		AutoGrabRequest{
			Mode: mode.Adult, Title: "Hot Scene", Studio: "HotStudios",
			RootFolderPath: "/adult", Trigger: TriggerRequest,
			Releases:       []prowlarr.Release{rel},
			RuntimeSeconds: 6000,
		})
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if out.NoMatch || !out.Grabbed {
		t.Fatalf("strong identity should dispatch, got NoMatch=%v Grabbed=%v", out.NoMatch, out.Grabbed)
	}
	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 || list[0].Status == grabs.PendingRetry {
		t.Fatalf("expected one dispatched (non-retry) grab row, got %+v", list)
	}
}

// TestRunAutoGrab_Adult_WeakIdentityStages is the A6/A5 staging test: when
// studio AND performers are empty (nothing to check against), adultIdentityWeak
// returns true and RunAutoGrab parks a pending_retry row regardless of whether
// the release cleared the quality floor. No dispatch fires.
//
// This also covers A4 for TriggerRequest: a retry with no studio/performers is
// always weak. TestRetryDueGrabs_Adult_AlwaysStagesNeverDispatches covers the
// same invariant at TriggerRetry level.
func TestRunAutoGrab_Adult_WeakIdentityStages(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := newTestDownloader("gid-weak", t.TempDir())
	setAutoGrabToggle(t, settingsStore, true)

	rel := prowlarr.Release{
		GUID: "1", Title: "Some.Studio.Hot.Scene.1080p.WEB-DL.x265-GROUP", Indexer: "I",
		Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 50,
		DownloadURL: "magnet:?xt=urn:btih:WEAK1234567890abcdef1234567890abcdef1234",
	}
	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		&mode.Session{Mode: mode.Adult, Downloader: dl},
		AutoGrabRequest{
			Mode: mode.Adult, Title: "Hot Scene",
			// Studio and Performers intentionally empty → adultIdentityWeak = true.
			RootFolderPath: "/adult", Trigger: TriggerRequest,
			Releases:       []prowlarr.Release{rel},
			RuntimeSeconds: 6000,
		})
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if !out.NoMatch || out.Grabbed {
		t.Fatalf("weak identity must stage (NoMatch), not dispatch: %+v", out)
	}
	if out.GrabID == 0 {
		t.Fatal("weak identity must park a pending_retry row (GrabID must be set)")
	}
	if out.RetryStatus != grabs.PendingRetry {
		t.Errorf("expected PendingRetry status, got %q", out.RetryStatus)
	}
	if out.RetryReason != weakIdentityReason {
		t.Errorf("expected reason %q, got %q", weakIdentityReason, out.RetryReason)
	}
	if got := len(dl.List()); got != 0 {
		t.Errorf("no dispatch should fire on a weak-identity stage, got %d", got)
	}
	// Confirm the row is visible to the retry cycle.
	row, err := grabsStore.Get(ctx, out.GrabID)
	if err != nil {
		t.Fatalf("loading parked row: %v", err)
	}
	if row.RetryAfter == "" {
		t.Error("weak-identity parked row has no retry_after — DueForRetry will never find it")
	}
}

// TestRunAutoGrab_Adult_WeakIdentityOperatorAlsoStages covers A5: even when
// the trigger is TriggerOperator (the one-click Discover grab), weak identity
// parks a pending_retry row rather than returning a plain pick list. The
// handler wraps this outcome as Fallback=true so the operator sees the
// candidate, but the pending_retry row is what lets the retry cycle pick it
// up if the operator does nothing.
func TestRunAutoGrab_Adult_WeakIdentityOperatorAlsoStages(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := newTestDownloader("gid-op-weak", t.TempDir())

	rel := prowlarr.Release{
		GUID: "1", Title: "Hot.Scene.1080p.WEB-DL.x265-GROUP", Indexer: "I",
		Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 50,
		DownloadURL: "magnet:?xt=urn:btih:OPWEAK1234567890abcdef1234567890abcdef12",
	}
	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		&mode.Session{Mode: mode.Adult, Downloader: dl},
		AutoGrabRequest{
			Mode: mode.Adult, Title: "Hot Scene",
			// No studio or performers — always weak, even for TriggerOperator.
			RootFolderPath: "/adult", Trigger: TriggerOperator,
			Releases:       []prowlarr.Release{rel},
			RuntimeSeconds: 6000,
		})
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if !out.NoMatch || out.Grabbed {
		t.Fatalf("TriggerOperator + weak identity must stage (NoMatch), not dispatch: %+v", out)
	}
	if out.GrabID == 0 {
		t.Fatal("TriggerOperator + weak identity must park a pending_retry row (A5)")
	}
	if out.RetryReason != weakIdentityReason {
		t.Errorf("expected reason %q, got %q", weakIdentityReason, out.RetryReason)
	}
	if got := len(dl.List()); got != 0 {
		t.Errorf("no dispatch on weak TriggerOperator, got %d", got)
	}
}

// TestRetryDueGrabs_Adult_AlwaysStagesNeverDispatches is A4's characterisation
// test: Adult TriggerRetry ALWAYS stages because the grabs row carries neither
// studio nor performers (they were not known at the original dispatch time and
// the row schema has no columns for them). adultIdentityWeak returns true when
// both are empty, so EVERY retry cycle parks another pending_retry row rather
// than dispatching — even when the release's scorer-grade would qualify.
//
// This characterisation is intentional (A4): operators who want a specific Adult
// release must grab it manually from Requests or Discover. The retry cycle is
// not a substitute for human identity validation.
func TestRetryDueGrabs_Adult_AlwaysStagesNeverDispatches(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := newTestDownloader("gid-retry", t.TempDir())
	setAutoGrabToggle(t, settingsStore, true)

	rel := prowlarr.Release{
		GUID: "1", Title: "Studio.Hot.Scene.1080p.WEB-DL.x265-GROUP", Indexer: "I",
		Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 50,
		DownloadURL: "magnet:?xt=urn:btih:RETRY1234567890abcdef1234567890abcdef1234",
	}
	// TriggerRetry carries no Studio or Performers (A4) — same as a real retry
	// cycle that reads from the grabs row which never had those fields.
	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		&mode.Session{Mode: mode.Adult, Downloader: dl},
		AutoGrabRequest{
			Mode: mode.Adult, Title: "Hot Scene",
			Studio: "", Performers: nil, // always empty on a real retry
			RootFolderPath: "/adult", Trigger: TriggerRetry,
			Releases:       []prowlarr.Release{rel},
			RuntimeSeconds: 6000,
		})
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if !out.NoMatch || out.Grabbed {
		t.Fatalf("Adult TriggerRetry (A4) must ALWAYS stage, never dispatch — got Grabbed=%v NoMatch=%v", out.Grabbed, out.NoMatch)
	}
	if out.GrabID == 0 {
		t.Fatal("Adult TriggerRetry must always park a pending_retry row (A4)")
	}
	if out.RetryReason != weakIdentityReason {
		t.Errorf("expected reason %q, got %q", weakIdentityReason, out.RetryReason)
	}
	if got := len(dl.List()); got != 0 {
		t.Errorf("Adult TriggerRetry must never dispatch: got %d dispatch(es)", got)
	}
}

// --- Batch path tests (A6/batch) --------------------------------------------

// TestAutoGrabBatch_Adult_WeakIdentityReturnsFallback proves the batch-path
// A6 behaviour: a batch Adult item with weak identity (empty studio and
// performers) returns a fallback pick list instead of dispatching. The grab
// result is Fallback=true with the candidate list; no download-client add fires
// and no grabs row is created — consistent with the "operator picks explicitly"
// three-state honesty convention.
func TestAutoGrabBatch_Adult_WeakIdentityReturnsFallback(t *testing.T) {
	const n = 1
	// A healthy release that qualifies on quality but not on identity.
	releasesJSON := `[{"guid":"1","title":"Some.Adult.Scene.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ADULTWEAK1234567890abcdef1234567890abcdef"}]`

	prowlarrSrv := fakeProwlarr(t, releasesJSON)
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Adult), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dl := newTestDownloader("gid-batch-weak", t.TempDir())
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{
			Title:           "Some Adult Scene",
			Studio:          "",   // empty → weak identity
			Performers:      nil,  // empty → weak identity
			DurationSeconds: 6000,
		}},
	}}
	resp, out := postBatch(t, srv.URL, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out.Results) != n {
		t.Fatalf("expected %d result, got %d", n, len(out.Results))
	}
	r := out.Results[0]
	if r.Grabbed || r.Error != "" {
		t.Fatalf("weak-identity batch item must not dispatch: %+v", r)
	}
	if !r.Fallback {
		t.Fatalf("weak-identity batch item must return Fallback=true, got %+v", r)
	}
	if len(r.Candidates) == 0 {
		t.Error("weak-identity fallback must carry at least one candidate for operator review")
	}

	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("weak-identity batch item must write no grabs row, got %d", len(list))
	}
	if got := len(dl.List()); got != 0 {
		t.Errorf("weak-identity batch item must fire no dispatch, got %d", got)
	}
}

// TestAutoGrabBatch_Adult_StrongIdentityGrabs proves that a batch Adult item
// with a studio name matching the release title dispatches normally — the batch
// weak-identity guard must not block grabs that should succeed.
func TestAutoGrabBatch_Adult_StrongIdentityGrabs(t *testing.T) {
	// Release title contains "HotStudios" — strong identity when Studio = "HotStudios".
	releasesJSON := `[{"guid":"1","title":"HotStudios.Hot.Scene.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ADULTSTRONG1234567890abcdef1234567890abc"}]`

	prowlarrSrv := fakeProwlarr(t, releasesJSON)
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Adult), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dl := newTestDownloader("gid-batch-strong", t.TempDir())
	dl.EnableTestAutoGID()
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{
			Title:           "Hot Scene",
			Studio:          "HotStudios", // appears in release title → strong
			DurationSeconds: 6000,
		}},
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
	if !r.Grabbed || r.Fallback || r.Error != "" {
		t.Fatalf("strong-identity Adult batch item should dispatch, got %+v", r)
	}

	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 grab row, got %d", len(list))
	}
}

// TestAutoGrabBatch_Adult_CacheHitDispatchesWithoutProwlarr is the batch-path
// twin of the single-handler cache test: a batch Adult item for a scene whose
// releases are cached grabs from cache without searching Prowlarr.
func TestAutoGrabBatch_Adult_CacheHitDispatchesWithoutProwlarr(t *testing.T) {
	const sceneTitle = "Some Scene"
	sceneKey := adultSceneKey("stashdb", "batch-cache-001", sceneTitle)

	dl := newTestDownloader("gid-bcache", t.TempDir())
	dl.EnableTestAutoGID()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, releaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	// No Prowlarr — a search-path grab would fail.
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("setting root folder: %v", err)
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Adult), string(quality.Low)); err != nil {
		t.Fatalf("setting quality tier: %v", err)
	}
	seedAdultCacheRelease(t, releaseStore, sceneTitle, sceneKey)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, dl, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	req := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{
			Title:           sceneTitle,
			Studio:          "SomeStudio", // strong identity — appears in seeded title
			Box:             "stashdb",
			SceneID:         "batch-cache-001",
			DurationSeconds: 6000,
		}},
	}}
	resp, out := postBatch(t, srv.URL, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
	r := out.Results[0]
	if !r.Grabbed || r.Fallback || r.Error != "" {
		t.Fatalf("cache-sourced batch item should dispatch without Prowlarr, got %+v", r)
	}
	if r.Grab == nil || r.Grab.Indexer != adultCacheTestIndexer {
		t.Errorf("cache-sourced batch grab must record indexer %q, got %q", adultCacheTestIndexer, func() string {
			if r.Grab == nil {
				return "<nil>"
			}
			return r.Grab.Indexer
		}())
	}

	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 grab row, got %d", len(list))
	}
}
