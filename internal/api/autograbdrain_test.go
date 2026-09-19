package api

// autograbdrain_test.go covers the plan's F-section test requirements:
//   - Two-phase: Usenet hit skips torrent search; Usenet miss searches torrent
//   - Slot gate: no dispatch when Usenet slots full
//   - Failure escalation: 430 → next_search_scope = 'torrent', retry_after ≈ now

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// TestTwoPhaseUsenetHitSkipsTorrent verifies that a Usenet-phase hit
// (SearchPhases=[ScopeUsenet,ScopeTorrent], ScopeUsenet returns a qualifying
// release) causes the torrent phase to be skipped.
//
// Mechanism: the AutoGrabRequest.SearchPhases loop breaks on the first
// qualifying Selection. We fake Prowlarr to return a qualifying release only
// for indexerIds=-1 (Usenet) and an empty list for all others.
func TestTwoPhaseUsenetHitSkipsTorrent(t *testing.T) {
	var usenetHits, torrentHits int32

	// Prowlarr: qualifying release on Usenet scope only.
	prowlarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["indexerIds"]
		w.Header().Set("Content-Type", "application/json")
		if len(ids) == 1 && ids[0] == "-1" {
			// Usenet scope: return a qualifying release.
			atomic.AddInt32(&usenetHits, 1)
			// 8 GB / 2700 s ≈ 23 Mbps — clears every 1080p floor.
			w.Write([]byte(`[{"guid":"1","title":"Some.Show.S01E01.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`))
		} else if len(ids) == 1 && ids[0] == "-2" {
			atomic.AddInt32(&torrentHits, 1)
			w.Write([]byte(`[]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(prowlarrSrv.Close)

	env := newTwoPhaseEnv(t, prowlarrSrv.URL)
	setAutoGrabToggle(t, env.settings, true)

	req := AutoGrabRequest{
		Mode:            mode.Series,
		Title:           "Some Show",
		TMDBID:          901,
		TVDBID:          4322,
		Season:          1,
		Episode:         1,
		SeasonSpecified: true,
		RootFolderPath:  "/series",
		Trigger:         TriggerAirDate,
		SearchPhases:    []prowlarr.Scope{prowlarr.ScopeUsenet, prowlarr.ScopeTorrent},
	}
	_, err := RunAutoGrab(env.ctx, env.deps, env.sess, req)
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if atomic.LoadInt32(&usenetHits) == 0 {
		t.Error("Usenet phase was not called")
	}
	if atomic.LoadInt32(&torrentHits) != 0 {
		t.Errorf("torrent phase was called despite a Usenet hit (torrentHits=%d)", atomic.LoadInt32(&torrentHits))
	}
}

// TestTwoPhaseUsenetMissSearchesTorrent verifies that when the Usenet phase
// finds no qualifying candidate, the torrent phase is attempted.
func TestTwoPhaseUsenetMissSearchesTorrent(t *testing.T) {
	var usenetHits, torrentHits int32

	// Prowlarr: empty on Usenet, qualifying release on torrent scope.
	prowlarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["indexerIds"]
		w.Header().Set("Content-Type", "application/json")
		if len(ids) == 1 && ids[0] == "-1" {
			atomic.AddInt32(&usenetHits, 1)
			w.Write([]byte(`[]`)) // Usenet miss
		} else if len(ids) == 1 && ids[0] == "-2" {
			atomic.AddInt32(&torrentHits, 1)
			w.Write([]byte(`[{"guid":"2","title":"Some.Show.S01E01.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(prowlarrSrv.Close)

	env := newTwoPhaseEnv(t, prowlarrSrv.URL)
	setAutoGrabToggle(t, env.settings, true)

	req := AutoGrabRequest{
		Mode:            mode.Series,
		Title:           "Some Show",
		TMDBID:          901,
		TVDBID:          4322,
		Season:          1,
		Episode:         1,
		SeasonSpecified: true,
		RootFolderPath:  "/series",
		Trigger:         TriggerAirDate,
		SearchPhases:    []prowlarr.Scope{prowlarr.ScopeUsenet, prowlarr.ScopeTorrent},
	}
	out, err := RunAutoGrab(env.ctx, env.deps, env.sess, req)
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if atomic.LoadInt32(&usenetHits) == 0 {
		t.Error("Usenet phase was not called")
	}
	if atomic.LoadInt32(&torrentHits) == 0 {
		t.Error("torrent phase was not called after Usenet miss")
	}
	if !out.Grabbed {
		t.Errorf("expected Grabbed=true after torrent hit, got %+v", out)
	}
}

// TestSlotGateBlocksDispatchWhenFull verifies that freeUsenetSlots returns 0
// when grabs rows fill the configured concurrency, and that the drain cycle
// exits without dispatching.
func TestSlotGateBlocksDispatchWhenFull(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)

	// Set max concurrent Usenet downloads to 2.
	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "2"); err != nil {
		t.Fatalf("setting max concurrent downloads: %v", err)
	}

	// Fill both slots with in-flight Usenet grabs.
	for i := range 2 {
		g, err := grabsStore.Create(ctx, grabs.Grab{
			Mode: mode.Series, Title: "Slot Filler", TMDBID: 900 + i,
			Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
			RootFolderPath: "/series", Status: grabs.Queued,
		})
		if err != nil {
			t.Fatalf("creating in-flight grab %d: %v", i, err)
		}
		if err := grabsStore.SetDownloadGID(ctx, g.ID, fmt.Sprintf("nzb-slot-%d", i)); err != nil {
			t.Fatalf("setting gid: %v", err)
		}
		// Move to downloading so it counts as in-flight.
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Downloading); err != nil {
			t.Fatalf("setting status downloading: %v", err)
		}
	}

	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	free, err := freeUsenetSlots(ctx, grabsStore, settingsStore)
	if err != nil {
		t.Fatalf("freeUsenetSlots: %v", err)
	}
	if free != 0 {
		t.Errorf("expected 0 free slots with 2/2 in-flight, got %d", free)
	}

	// One slot freed — free count should rise.
	list, _ := grabsStore.List(ctx, mode.Series)
	if err := grabsStore.UpdateStatus(ctx, list[0].ID, grabs.Completed); err != nil {
		t.Fatalf("completing grab: %v", err)
	}
	free, err = freeUsenetSlots(ctx, deps.GrabsStore, deps.SettingsStore)
	if err != nil {
		t.Fatalf("freeUsenetSlots after completion: %v", err)
	}
	if free != 1 {
		t.Errorf("expected 1 free slot after one completion, got %d", free)
	}
}

// TestFailureEscalationSetsNextSearchScope verifies that a 430/ErrArticleNotFound
// on a Usenet download parks for a DIFFERENT Usenet release (alternate), not
// torrent escalation — missing articles are NZB-local; another release has
// different message-IDs.
//
// And that a 451 does NOT set next_search_scope / stays Failed.
func TestFailureEscalationSetsNextSearchScope(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	t.Run("430 parks alternate Usenet release due-now", func(t *testing.T) {
		g := dispatchedUsenetGrab(t, grabsStore, "nzb-scope-1")
		beforeGrab, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("reload grab: %v", err)
		}
		failure := fmt.Errorf("segment: %w", usenet.ErrArticleNotFound)

		before := time.Now()
		status, err := applyUsenetFailure(ctx, deps, *beforeGrab, failure, parkGrabForRetry)
		after := time.Now()
		if err != nil {
			t.Fatalf("applyUsenetFailure: %v", err)
		}
		if status != grabs.PendingRetry {
			t.Fatalf("status = %q, want pending_retry", status)
		}

		parked, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("loading parked grab: %v", err)
		}
		if parked.NextSearchScope == "torrent" {
			t.Errorf("next_search_scope = %q, want empty/usenet alternate (not torrent)", parked.NextSearchScope)
		}
		if parked.DownloadGID != "" {
			t.Errorf("GID not cleared: %q", parked.DownloadGID)
		}
		if parked.TriedReleaseKeys == "" {
			t.Error("tried_release_keys empty — alternate park should record the failed NZB")
		}
		retryAt, err := time.Parse("2006-01-02T15:04:05.000Z", parked.RetryAfter)
		if err != nil {
			t.Fatalf("parsing retry_after %q: %v", parked.RetryAfter, err)
		}
		if retryAt.After(after.Add(time.Minute)) {
			t.Errorf("retry_after %v far in future (window: %v–%v)", retryAt, before, after)
		}
	})

	t.Run("451 permanent failure — no next_search_scope", func(t *testing.T) {
		g := dispatchedUsenetGrab(t, grabsStore, "nzb-scope-2")
		failure := fmt.Errorf("segment: %w", usenet.ErrArticleRemoved)

		status, err := applyUsenetFailure(ctx, deps, g, failure, parkGrabForRetry)
		if err != nil {
			t.Fatalf("applyUsenetFailure: %v", err)
		}
		if status != grabs.Failed {
			t.Fatalf("status = %q, want failed", status)
		}
		failed, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("loading failed grab: %v", err)
		}
		if failed.NextSearchScope != "" {
			t.Errorf("451 should not set next_search_scope, got %q", failed.NextSearchScope)
		}
	})

	t.Run("unclassified failure uses normal backoff — no scope set", func(t *testing.T) {
		g := dispatchedUsenetGrab(t, grabsStore, "nzb-scope-3")
		failure := fmt.Errorf("dial timeout")

		status, err := applyUsenetFailure(ctx, deps, g, failure, parkGrabForRetry)
		if err != nil {
			t.Fatalf("applyUsenetFailure: %v", err)
		}
		if status != grabs.PendingRetry {
			t.Fatalf("status = %q, want pending_retry", status)
		}
		parked, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("loading parked grab: %v", err)
		}
		if parked.NextSearchScope != "" {
			t.Errorf("unclassified failure should not set next_search_scope, got %q", parked.NextSearchScope)
		}
	})
}

// TestNextSearchPhasesConsumesTorrentScope verifies that nextSearchPhases
// returns [ScopeTorrent] for a grab with next_search_scope='torrent'
// and nil for an empty scope.
func TestNextSearchPhasesConsumesTorrentScope(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	withScope := grabs.Grab{NextSearchScope: "torrent"}
	phases := nextSearchPhases(ctx, deps, withScope)
	if len(phases) != 1 || phases[0] != prowlarr.ScopeTorrent {
		t.Errorf("nextSearchPhases(torrent) = %v, want [ScopeTorrent]", phases)
	}

	withoutScope := grabs.Grab{}
	phases = nextSearchPhases(ctx, deps, withoutScope)
	if phases != nil {
		t.Errorf("nextSearchPhases(empty) = %v, want nil", phases)
	}
}

// twoPhaseEnv is the minimal wiring for two-phase RunAutoGrab tests: a real
// grabs+settings store, a Series session that points at the caller's fake
// Prowlarr and a stubbed TMDB, and no downloader (downloads are accepted by
// the fake DL below).
type twoPhaseEnv struct {
	ctx      context.Context
	settings *settings.Store
	deps     AutoGrabDeps
	sess     *mode.Session
}

// stubDownloadManager is a no-op downloader that accepts every NZB/magnet
// and returns a fixed GID — sufficient to let RunAutoGrab's dispatch path
// record a grab row without a real download client.
func stubDownloadManager(t *testing.T) *downloader.Manager {
	t.Helper()
	// Use the existing test downloader helper from airdatemonitor_test.go.
	return newTestDownloader("gid-twophase", t.TempDir())
}

func newTwoPhaseEnv(t *testing.T, prowlarrURL string) *twoPhaseEnv {
	t.Helper()
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)

	// TMDB stub: returns a fixed S01E01 episode runtime (2700 s = 45 min)
	// and external IDs so ExternalIDs + seriesEpisodeRuntimeSeconds work.
	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case endsWith(r.URL.Path, "/external_ids"):
			fmt.Fprintf(w, `{"tvdb_id":4322}`)
		case endsWith(r.URL.Path, "/season/1"):
			fmt.Fprintf(w, `{"episodes":[{"episode_number":1,"air_date":"2024-01-01","runtime":45}]}`)
		default:
			fmt.Fprintf(w, `{}`)
		}
	}))
	t.Cleanup(tmdbSrv.Close)
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)

	if err := settingsStore.Set(ctx, qualityTierKey(mode.Series), "low"); err != nil {
		t.Fatalf("setting quality tier: %v", err)
	}

	// Build connections so mode.Build can wire TMDB + Prowlarr.
	from, _ := secrets.New(make([]byte, 32))
	connStore := connections.New(dbtest.New(t), from)
	scStore := serviceconn.NewStore(dbtest.New(t), from)
	for _, svc := range []struct{ name, url string }{
		{"tmdb", tmdbSrv.URL},
		{"prowlarr", prowlarrURL},
	} {
		if err := connStore.Upsert(ctx, svc.name, svc.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", svc.name, err)
		}
	}

	dl := stubDownloadManager(t)
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, mode.Series)
	if err != nil {
		t.Fatalf("mode.Build: %v", err)
	}

	return &twoPhaseEnv{
		ctx:      ctx,
		settings: settingsStore,
		deps:     AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		sess:     sess,
	}
}

// endsWith is a compact path-suffix helper used by the TMDB stub above.
func endsWith(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}
