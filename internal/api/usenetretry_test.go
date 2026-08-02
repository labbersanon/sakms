package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/usenet"
)

// staticLookup fakes the usenet engine's FindByGID for exactly one GID.
func staticLookup(gid string, dl *usenet.Download) usenetDownloadLookup {
	return func(want string) (*usenet.Download, error) {
		if want == gid {
			return dl, nil
		}
		return nil, nil
	}
}

// dispatchedUsenetGrab records a grab that is in flight on the usenet engine —
// the shape the GID sweep exists to reconcile.
func dispatchedUsenetGrab(t *testing.T, grabsStore *grabs.Store, gid string) grabs.Grab {
	t.Helper()
	ctx := context.Background()
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=1",
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("setting download gid: %v", err)
	}
	return g
}

// TestSweepUsenetFailuresClassifiesRetrievalErrors is the M3 sweep's core: a
// usenet retrieval failure is asynchronous and arrives long after the HTTP
// response was written, so on a headless instance THIS is the only thing that
// ever moves the grab out of "downloading". The two sentinels must land on
// different states — a 451 takedown retried forever is the exact bug the
// unflattened Download.Err field was added to prevent.
func TestSweepUsenetFailuresClassifiesRetrievalErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failure    error
		wantStatus grabs.Status
		wantParked bool
	}{
		{
			name:       "430 from every subscription is retryable",
			failure:    fmt.Errorf("segment <abc@news>: %w", usenet.ErrArticleNotFound),
			wantStatus: grabs.PendingRetry,
			wantParked: true,
		},
		{
			name:       "451 takedown is permanent and never retried",
			failure:    fmt.Errorf("segment <abc@news>: %w", usenet.ErrArticleRemoved),
			wantStatus: grabs.Failed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
			g := dispatchedUsenetGrab(t, grabsStore, "nzb-1")
			deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

			sweepUsenetFailures(ctx, deps, staticLookup("nzb-1", &usenet.Download{
				GID: "nzb-1", Status: "error", Err: tc.failure,
			}))

			got, err := grabsStore.Get(ctx, g.ID)
			if err != nil {
				t.Fatalf("reloading grab: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if !tc.wantParked {
				// A permanently failed grab must not carry a retry_after, or
				// DueForRetry would resurrect it.
				if got.RetryAfter != "" {
					t.Errorf("a permanently failed grab was parked for retry (retry_after %q)", got.RetryAfter)
				}
				return
			}
			if got.RetryAfter == "" {
				t.Error("parked without a retry_after — DueForRetry ignores such a row, so it would never be retried")
			}
			if got.DownloadGID != "" {
				t.Errorf("parked with a stale GID %q — DueForRetry filters on download_gid = ''", got.DownloadGID)
			}
			if got.RetryCount != 1 {
				t.Errorf("retryCount = %d, want 1 (the attempt must be counted so maxRetryAttempts can cap it)", got.RetryCount)
			}
			if got.RetryReason != articlesUnavailableReason {
				t.Errorf("retryReason = %q, want %q", got.RetryReason, articlesUnavailableReason)
			}
		})
	}
}

// TestSweepUsenetFailuresLeavesHealthyAndUnknownDownloadsAlone covers the three
// skips the sweep must make. The unknown-GID case is the load-bearing one: the
// Manager's queue is in-memory, so after a restart EVERY in-flight usenet grab
// looks unknown — treating that as a failure would silently re-search every
// active download on each boot.
func TestSweepUsenetFailuresLeavesHealthyAndUnknownDownloadsAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gid    string
		lookup func(gid string) usenetDownloadLookup
	}{
		{
			name: "a GID the engine no longer knows (e.g. after a restart)",
			gid:  "nzb-1",
			lookup: func(gid string) usenetDownloadLookup {
				return func(string) (*usenet.Download, error) { return nil, nil }
			},
		},
		{
			name: "a download with no classified failure",
			gid:  "nzb-1",
			lookup: func(gid string) usenetDownloadLookup {
				return staticLookup(gid, &usenet.Download{GID: gid, Status: "active"})
			},
		},
		{
			name: "a torrent GID, which the usenet sweep must not touch",
			gid:  "torrent-1",
			lookup: func(gid string) usenetDownloadLookup {
				return staticLookup(gid, &usenet.Download{
					GID: gid, Status: "error", Err: usenet.ErrArticleRemoved,
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
			g := dispatchedUsenetGrab(t, grabsStore, tc.gid)

			sweepUsenetFailures(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}, tc.lookup(tc.gid))

			got, err := grabsStore.Get(ctx, g.ID)
			if err != nil {
				t.Fatalf("reloading grab: %v", err)
			}
			if got.Status != grabs.Queued || got.DownloadGID != tc.gid || got.RetryAfter != "" {
				t.Fatalf("grab was modified by the sweep: %+v", got)
			}
		})
	}
}

// dueRetryRow parks a row that is already past its retry_after.
func dueRetryRow(t *testing.T, grabsStore *grabs.Store, m mode.Mode, title string, tmdbID int) grabs.Grab {
	t.Helper()
	g, err := grabsStore.Create(context.Background(), grabs.Grab{
		Mode: m, Title: title, TMDBID: tmdbID, RootFolderPath: "/movies",
		Status:      grabs.PendingRetry,
		RetryAfter:  grabs.FormatTime(time.Now().Add(-time.Hour)),
		RetryReason: noQualifyingCandidateReason,
	})
	if err != nil {
		t.Fatalf("creating pending_retry grab: %v", err)
	}
	return g
}

// TestRetryDueGrabsRelaunchesTheSameRow is THE invariant of the whole retry
// loop: after a retry dispatches successfully, exactly ONE row holds the new
// GID and no row is left pending_retry with a retry_after in the past.
//
// Getting this wrong is not cosmetic. A second row would leave the original
// still due, so every following cycle would grab the same release again — and
// AddNZB mints a fresh GID per call, so ActiveByDownloadGID cannot catch that
// duplicate. DueForRetry's own doc comment states the contract this asserts.
func TestRetryDueGrabsRelaunchesTheSameRow(t *testing.T) {
	ctx := context.Background()
	connStore, _, _, settingsStore, grabsStore, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	dl := newTestDownloader("gid-retry", t.TempDir())
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 6000 s runtime → the release grades
	prowlarrSrv := fakeProwlarr(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)

	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	for _, c := range []struct{ service, url string }{{"tmdb", tmdbSrv.URL}, {"prowlarr", prowlarrSrv.URL}} {
		if err := connStore.Upsert(ctx, c.service, c.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, true)

	g := dueRetryRow(t, grabsStore, mode.Movies, "Some Movie", 42)

	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		build, nil, nil, nil, time.Now())

	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the retry to RE-ARM the existing row, got %d rows: %+v", len(list), list)
	}
	got := list[0]
	if got.ID != g.ID {
		t.Fatalf("the surviving row is a new one (id %d, want %d)", got.ID, g.ID)
	}
	if got.Status != grabs.Queued || got.DownloadGID != "gid-retry" {
		t.Fatalf("row did not rejoin the normal lifecycle: %+v", got)
	}
	if got.RetryAfter != "" || got.RetryReason != "" {
		t.Errorf("retry state survived a successful dispatch (after %q, reason %q) — the row is still due and the Requests screen would render a stale reason", got.RetryAfter, got.RetryReason)
	}
	if got.Indexer != "I" || got.DownloadURL == "" {
		t.Errorf("re-armed row lost its dispatch details: %+v", got)
	}
	if got := len(dl.List()); got != 1 {
		t.Errorf("expected exactly one download-client add, got %d", got)
	}
	due, err := grabsStore.DueForRetry(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due grabs: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("row is STILL due after a successful retry — the next cycle would grab it again: %+v", due)
	}
}

// TestRetryDueGrabsCountsAFailedAttempt guards the hot-loop hazard. A row parked
// by the toggle-ON Search hook carries TMDB id 0, so RunAutoGrab's internal
// search cannot resolve it and errors every single time. Left unparked, that row
// stays due forever and re-runs the same broken search every cycle on a tight
// loop — retry_count is tracked for observability, but (2026-08-01: retry cap
// removed) nothing ever caps it; the interval-based park below is what prevents
// the hot loop, not a give-up threshold.
func TestRetryDueGrabsCountsAFailedAttempt(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	setAutoGrabToggle(t, settingsStore, true)
	g := dueRetryRow(t, grabsStore, mode.Movies, "Some Movie", 0)

	failing := func(context.Context, mode.Mode) (*mode.Session, error) {
		return nil, fmt.Errorf("prowlarr isn't configured")
	}
	runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		failing, nil, nil, nil, time.Now())

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.RetryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", got.RetryCount)
	}
	if got.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want %q", got.Status, grabs.PendingRetry)
	}
	// The reason is deliberately classified, not the cause's text: the cause is
	// routinely a *url.Error carrying an api_key, and this string is stored in
	// plaintext and rendered in the browser. See retrySearchFailedReason and
	// TestReparkFailedRetryDoesNotLeakTheCause.
	if got.RetryReason != retrySearchFailedReason {
		t.Errorf("retryReason = %q, want %q", got.RetryReason, retrySearchFailedReason)
	}
	if strings.Contains(got.RetryReason, "prowlarr isn't configured") {
		t.Errorf("retryReason embeds the underlying failure again: %q", got.RetryReason)
	}
	due, err := grabsStore.DueForRetry(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due grabs: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("row is still due immediately after a failed attempt — that is the hot loop this test exists to prevent")
	}
}

// TestRetryDueGrabsNeverTerminatesAPermanentlyUngradeableRow covers the
// 2026-08-01 decision to remove the retry-attempt cap: for a request that can
// NEVER succeed — a Series season pack, a duration-less Adult scene, or (as
// here) a row parked by the Search hook with no TMDB id for the internal
// search to resolve — the scheduler now retries indefinitely rather than
// terminating. This is a deliberate, explicit product decision (auto-grab
// should keep trying until a match is found), not an oversight; the previous
// version of this test asserted the opposite (termination after
// maxRetryAttempts) before that cap was removed.
//
// The clock is advanced per cycle because each failed attempt pushes
// retry_after a full interval out — a test that called the cycle repeatedly
// at the same instant would pass while proving nothing.
func TestRetryDueGrabsNeverTerminatesAPermanentlyUngradeableRow(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	setAutoGrabToggle(t, settingsStore, true)
	g := dueRetryRow(t, grabsStore, mode.Movies, "Some Movie", 0)

	alwaysFails := func(context.Context, mode.Mode) (*mode.Session, error) {
		return nil, fmt.Errorf("prowlarr isn't configured")
	}
	// Well past what was previously the give-up cap; each tick 25 h later so
	// the row is due again.
	const cyclesWellPastTheOldCap = 12
	for i := 0; i <= cyclesWellPastTheOldCap; i++ {
		runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
			alwaysFails, nil, nil, nil, time.Now().Add(time.Duration(i)*25*time.Hour))
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q after %d cycles, want %q — retries are no longer capped", got.Status, cyclesWellPastTheOldCap, grabs.PendingRetry)
	}
	if got.RetryCount != cyclesWellPastTheOldCap+1 {
		t.Errorf("retryCount = %d, want %d — still counted for observability even though nothing caps on it", got.RetryCount, cyclesWellPastTheOldCap+1)
	}
	if got.RetryAfter == "" {
		t.Errorf("a still-retrying row must stay parked for its next attempt, got empty RetryAfter")
	}
	due, err := grabsStore.DueForRetry(ctx, time.Now().Add(30*24*time.Hour))
	if err != nil {
		t.Fatalf("listing due grabs: %v", err)
	}
	if len(due) != 1 || due[0].ID != g.ID {
		t.Errorf("a row well past the old cap must still come back due, got %+v", due)
	}
}

// TestRetryDueGrabsSkipsExcludedTitles: excluding a request from the worklist is
// an explicit "stop working on this". A background loop that keeps re-searching
// it anyway is the scheduler overriding a human decision. Skipped, not failed —
// exclusion is reversible.
func TestRetryDueGrabsSkipsExcludedTitles(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	setAutoGrabToggle(t, settingsStore, true)
	g := dueRetryRow(t, grabsStore, mode.Movies, "Some Movie", 42)

	build := func(context.Context, mode.Mode) (*mode.Session, error) {
		t.Fatal("an excluded title must never reach a session build, let alone a live search")
		return nil, nil
	}
	excluded := map[string]bool{excludes.Key(string(mode.Movies), 42, "Some Movie"): true}
	runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		build, nil, nil, excluded, time.Now())

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.RetryCount != 0 || got.Status != grabs.PendingRetry {
		t.Fatalf("an excluded row was modified: %+v", got)
	}
}

// TestLoadUsenetRetryIntervalIsOffByDefault: the opt-in gate. 0 here must stay
// 0 — deliberately unlike usenetRetryInterval, which degrades to 24h so a
// freshly parked row isn't due the instant it is written.
func TestLoadUsenetRetryIntervalIsOffByDefault(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)

	if got := LoadUsenetRetryInterval(ctx, settingsStore); got != 0 {
		t.Fatalf("interval = %s on a blank install, want 0 (off)", got)
	}
	for _, v := range []string{"0", "-5", "not a number"} {
		if err := settingsStore.Set(ctx, usenetRetryIntervalSecondsKey, v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := LoadUsenetRetryInterval(ctx, settingsStore); got != 0 {
			t.Errorf("interval for stored %q = %s, want 0 (off)", v, got)
		}
	}
	if err := settingsStore.Set(ctx, usenetRetryIntervalSecondsKey, "86400"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := LoadUsenetRetryInterval(ctx, settingsStore); got != 24*time.Hour {
		t.Errorf("interval = %s, want 24h", got)
	}
}

// TestPutUsenetAutoGrabEnabledCouplesTheInterval: the toggle owns the cadence.
// Two independently settable switches could only ever disagree, and a client
// that forgot the second write would leave auto-grab on with the retry loop
// dead — stranding every pending_retry row forever.
func TestPutUsenetAutoGrabEnabledCouplesTheInterval(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)

	put := func(enabled bool) {
		t.Helper()
		body, _ := json.Marshal(usenetAutoGrabEnabledRequest{Enabled: enabled})
		rec := httptest.NewRecorder()
		putUsenetAutoGrabEnabledHandler(settingsStore)(rec, httptest.NewRequest(http.MethodPut, "/api/settings/usenet-autograb-enabled", strings.NewReader(string(body))))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("PUT enabled=%v: status %d, body %q", enabled, rec.Code, rec.Body.String())
		}
	}
	get := func() usenetAutoGrabEnabledResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		getUsenetAutoGrabEnabledHandler(settingsStore)(rec, httptest.NewRequest(http.MethodGet, "/api/settings/usenet-autograb-enabled", nil))
		var out usenetAutoGrabEnabledResponse
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		return out
	}

	if get().Enabled {
		t.Fatal("auto-grab defaults to ON — it must be off until an operator opts in")
	}
	if got := LoadUsenetRetryInterval(ctx, settingsStore); got != 0 {
		t.Fatalf("retry interval = %s before the toggle was ever set, want 0", got)
	}

	put(true)
	if !get().Enabled {
		t.Error("toggle did not persist")
	}
	if got := LoadUsenetRetryInterval(ctx, settingsStore); got != 24*time.Hour {
		t.Errorf("retry interval = %s after switching auto-grab on, want 24h", got)
	}

	put(false)
	if get().Enabled {
		t.Error("toggle did not persist off")
	}
	if got := LoadUsenetRetryInterval(ctx, settingsStore); got != 0 {
		t.Errorf("retry interval = %s after switching auto-grab off, want 0 (the loop must stop)", got)
	}
}
