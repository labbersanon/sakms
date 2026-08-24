package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// staleTestStores is testStores' shape for this file: one database, and the
// five stores the stale path actually touches — including excludes, which
// testStores does not build and GET /api/requests requires.
type staleTestStores struct {
	settings *settings.Store
	grabs    *grabs.Store
	library  *library.Store
	excludes *excludes.Store
	conns    *connections.Store
	svcConns *serviceconn.Store
}

func newStaleTestStores(t *testing.T) staleTestStores {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return staleTestStores{
		settings: settings.New(sqlDB),
		grabs:    grabs.New(sqlDB, secretStore),
		library:  library.New(sqlDB),
		excludes: excludes.New(sqlDB),
		conns:    connections.New(sqlDB, secretStore),
		svcConns: serviceconn.NewStore(sqlDB, secretStore),
	}
}

// stalledTorrentGrab sets up the exact state the reaper hands off: a grab in
// flight on the torrent engine, with a real partial file on disk that Cancel
// must delete. Returns the grab and the partial file's path.
func stalledTorrentGrab(t *testing.T, s staleTestStores, dl *downloader.Manager, gid, staging string) (grabs.Grab, string) {
	t.Helper()
	ctx := context.Background()
	g, err := s.grabs.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix",
		RootFolderPath: "/movies", DownloadURL: "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12",
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}
	if err := s.grabs.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("setting download gid: %v", err)
	}
	if err := s.grabs.UpdateStatus(ctx, g.ID, grabs.Downloading); err != nil {
		t.Fatalf("marking downloading: %v", err)
	}

	partial := filepath.Join(staging, "Some.Movie.2023.1080p.mkv")
	if err := os.WriteFile(partial, []byte("partial"), 0o644); err != nil {
		t.Fatalf("writing partial file: %v", err)
	}
	dl.SeedState(downloader.Download{GID: gid, Status: "active", Dir: staging, Files: []string{partial}})
	return g, partial
}

// T-4.6 — the whole point of the feature, end to end: a stalled torrent's grab
// is parked for a re-search AND the dead download is cancelled with its partial
// files deleted. Both halves matter — Manager.Cancel never touches the grabs
// store (they are separate layers), so a cancel with no park would leave the
// row stuck at "downloading" behind a GID that no longer exists.
func TestStaleTorrentHandler_ParksTheGrabAndDeletesThePartialDownload(t *testing.T) {
	ctx := context.Background()
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	g, partial := stalledTorrentGrab(t, s, dl, "gid-stale", staging)

	StaleTorrentHandler(s.settings, s.grabs, dl)("gid-stale")

	got, err := s.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q, want %q", got.Status, grabs.PendingRetry)
	}
	if got.RetryReason != staleTorrentReason {
		t.Errorf("retryReason = %q, want %q", got.RetryReason, staleTorrentReason)
	}
	if got.RetryAfter == "" {
		t.Error("parked without a retry_after — DueForRetry ignores such a row, so it would never be re-searched")
	}
	if got.DownloadGID != "" {
		t.Errorf("parked with the dead GID %q still attached — DueForRetry filters on download_gid = ''", got.DownloadGID)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("the stalled download's partial file survived the auto-cancel (stat err %v)", err)
	}
	if queueHas(dl, "gid-stale") {
		t.Error("the download is still in the engine's queue after an auto-cancel")
	}
}

// T-4.7 — ORDERING (§4.4). Park first, cancel second, and a failed park aborts
// before the cancel.
//
// This is the failure mode the ordering was chosen for: cancelling first and
// then failing to park would destroy the download while leaving the row at
// "downloading" behind a GID that no longer exists — invisible to DueForRetry
// and to every other sweep, i.e. permanently stuck with its files already gone.
// Parking first turns that into the recoverable case instead: the row is
// correctly parked and the operator can still cancel the leftover by hand.
func TestStaleTorrentHandler_ParkFailureLeavesTheDownloadAlone(t *testing.T) {
	ctx := context.Background()
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	g, partial := stalledTorrentGrab(t, s, dl, "gid-stale", staging)

	deps := AutoGrabDeps{SettingsStore: s.settings, GrabsStore: s.grabs}
	parkFailed := func(context.Context, AutoGrabDeps, int64, string) error {
		return errors.New("the database is unavailable")
	}
	handleStaleTorrent(ctx, deps, dl, "gid-stale", parkFailed)

	if !queueHas(dl, "gid-stale") {
		t.Fatal("the download was cancelled even though the park failed")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Errorf("the partial file was deleted even though the park failed: %v", err)
	}
	got, err := s.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.Status != grabs.Downloading || got.DownloadGID != "gid-stale" {
		t.Errorf("the grab row moved despite the failed park: status %q, gid %q", got.Status, got.DownloadGID)
	}
}

// T-4.8 — a GID SAK did not initiate (a torrent added out of band, or a grab
// already deleted) is a silent no-op, exactly as DownloadCompleteImporter
// treats the same case. It must not cancel a download it does not own.
func TestStaleTorrentHandler_UnknownGIDIsASilentNoOp(t *testing.T) {
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	dl.SeedState(downloader.Download{GID: "gid-not-ours", Status: "active", Dir: staging})

	StaleTorrentHandler(s.settings, s.grabs, dl)("gid-not-ours")

	if !queueHas(dl, "gid-not-ours") {
		t.Fatal("a download with no grab row was cancelled")
	}
	if list, err := s.grabs.List(context.Background(), mode.Movies); err != nil {
		t.Fatalf("listing grabs: %v", err)
	} else if len(list) != 0 {
		t.Errorf("an unknown gid created grab rows: %+v", list)
	}
}

// T-4.9 — THE REGRESSION GUARD FOR THE 2026-08-01 RETRY-CAP REMOVAL. A grab
// well past what used to be the give-up threshold (retry_count 7 vs. the old
// cap of 5) must still park normally rather than being retired.
//
// Under the old cap, SetPendingRetry decided in SQL that this row had had
// enough and wrote status 'failed' with an empty retry_after — and because
// isActiveGrab excludes Failed, the title would have been auto-cancelled, had
// its files deleted, and then VANISHED from /api/requests with no visible
// trace. Asserting the row is still there, still "Pending Retry", is what makes
// a reintroduced cap fail loudly here instead of silently in production.
func TestStaleTorrentHandler_UncappedRetry_HighRetryCountStillRequeues(t *testing.T) {
	ctx := context.Background()
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	g, _ := stalledTorrentGrab(t, s, dl, "gid-stale", staging)

	// Drive retry_count to 7 the only way production can: real park cycles.
	const priorAttempts = 7
	for i := 0; i < priorAttempts; i++ {
		if err := s.grabs.SetPendingRetry(ctx, g.ID, time.Now().Add(time.Hour), "an earlier attempt"); err != nil {
			t.Fatalf("seeding retry attempt %d: %v", i+1, err)
		}
	}
	// SetPendingRetry clears the GID, so put the row back in flight.
	if err := s.grabs.UpdateStatus(ctx, g.ID, grabs.Downloading); err != nil {
		t.Fatalf("marking downloading: %v", err)
	}
	if err := s.grabs.SetDownloadGID(ctx, g.ID, "gid-stale"); err != nil {
		t.Fatalf("re-attaching the gid: %v", err)
	}

	StaleTorrentHandler(s.settings, s.grabs, dl)("gid-stale")

	got, err := s.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q at retry_count %d, want %q — a retry cap has been reintroduced", got.Status, got.RetryCount, grabs.PendingRetry)
	}
	if got.RetryCount != priorAttempts+1 {
		t.Errorf("retryCount = %d, want %d — still counted, purely for observability", got.RetryCount, priorAttempts+1)
	}
	if got.RetryAfter == "" {
		t.Error("a still-retrying row must stay parked for its next attempt, got an empty retry_after")
	}
	if got.RetryReason != staleTorrentReason {
		t.Errorf("retryReason = %q, want %q (and never a 'gave up after N attempts' wording)", got.RetryReason, staleTorrentReason)
	}
	if queueHas(dl, "gid-stale") {
		t.Error("the stalled download was not cancelled")
	}

	// The title must still be on the worklist. This is the half that silently
	// broke under the old cap.
	items := fetchRequests(t, s)
	item, ok := findRequestItem(items, "Some Movie")
	if !ok {
		t.Fatalf("the title vanished from /api/requests after a park at retry_count %d: %+v", got.RetryCount, items)
	}
	if item.Status != "Pending Retry" {
		t.Errorf("requests status = %q, want %q", item.Status, "Pending Retry")
	}
}

// T-5.1 — AC6's first half: the stale title visibly re-enters the Requests
// worklist with an honest reason, rather than disappearing when its download is
// cancelled.
func TestStaleTorrentHandler_TitleReEntersRequests(t *testing.T) {
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	stalledTorrentGrab(t, s, dl, "gid-stale", staging)

	StaleTorrentHandler(s.settings, s.grabs, dl)("gid-stale")

	items := fetchRequests(t, s)
	item, ok := findRequestItem(items, "Some Movie")
	if !ok {
		t.Fatalf("the stale title is missing from /api/requests: %+v", items)
	}
	if item.Status != "Pending Retry" {
		t.Errorf("status = %q, want %q", item.Status, "Pending Retry")
	}
	if item.RetryReason != staleTorrentReason {
		t.Errorf("retryReason = %q, want %q", item.RetryReason, staleTorrentReason)
	}
	if item.RetryAfter == "" {
		t.Error("the worklist row carries no retry_after, so the screen cannot say when the re-search runs")
	}
}

// T-5.2 — AC6's second half: a stale-parked row is genuinely retried. The retry
// re-arms THAT SAME grabs row (same id, fresh GID) through TriggerRetry +
// ExistingGrabID rather than creating a second one — a second row would leave
// the original still due and every following cycle would grab the same release
// again.
//
// Nothing new is wired for this: the handler only parks, and the ALREADY
// EXISTING retryDueGrabs sweep does the rest. That is the whole "new parker,
// not new trigger" claim, tested rather than asserted.
func TestStaleTorrentHandler_ParkedRowIsRetriedOnTheNormalCycle(t *testing.T) {
	ctx := context.Background()
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	g, _ := stalledTorrentGrab(t, s, dl, "gid-stale", staging)
	seedRetryableSearch(t, s)
	setAutoGrabToggle(t, s.settings, true)
	dl.SetTestNextGID("gid-after-retry")

	StaleTorrentHandler(s.settings, s.grabs, dl)("gid-stale")

	// The park sets retry_after to now + the 24h interval, so drive the cycle
	// with a clock past that rather than rewriting the row.
	runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: s.settings, GrabsStore: s.grabs},
		staleSessionBuilder(s, dl), nil, nil, nil, nil, nil, time.Now().Add(25*time.Hour))

	list, err := s.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("the retry must RE-ARM the existing row, got %d rows: %+v", len(list), list)
	}
	got := list[0]
	if got.ID != g.ID {
		t.Fatalf("the surviving row is a new one (id %d, want %d)", got.ID, g.ID)
	}
	if got.Status != grabs.Queued {
		t.Fatalf("row did not rejoin the normal lifecycle: %+v", got)
	}
	if got.DownloadGID != "gid-after-retry" {
		t.Errorf("downloadGID = %q, want the freshly dispatched %q", got.DownloadGID, "gid-after-retry")
	}
	if got.RetryAfter != "" || got.RetryReason != "" {
		t.Errorf("stale retry state survived a successful re-dispatch (after %q, reason %q)", got.RetryAfter, got.RetryReason)
	}
}

// T-5.3 — the gate is honored. Auto-cancelling a dead download is cleanup and
// runs ungated; RE-GRABBING is a new download decision and stays behind
// usenet_autograb_enabled. With the toggle off the row must simply sit in
// pending_retry with nothing dispatched — proving the stale path parks into the
// shared mechanism rather than routing around its single gate.
func TestStaleTorrentHandler_RequeueHonorsTheAutoGrabGate(t *testing.T) {
	ctx := context.Background()
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	g, _ := stalledTorrentGrab(t, s, dl, "gid-stale", staging)
	seedRetryableSearch(t, s)
	setAutoGrabToggle(t, s.settings, false)
	dl.SetTestNextGID("gid-after-retry")

	StaleTorrentHandler(s.settings, s.grabs, dl)("gid-stale")
	runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: s.settings, GrabsStore: s.grabs},
		staleSessionBuilder(s, dl), nil, nil, nil, nil, nil, time.Now().Add(25*time.Hour))

	got, err := s.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q with auto-grab OFF, want %q — the stale requeue routed around the gate", got.Status, grabs.PendingRetry)
	}
	if got.DownloadGID != "" {
		t.Fatalf("a release was dispatched with auto-grab OFF (gid %q)", got.DownloadGID)
	}
	if queueHas(dl, "gid-after-retry") {
		t.Error("the download engine received a dispatch with auto-grab OFF")
	}
}

// T-4.2 (global-pause half; the engine half lives in internal/downloader's
// TestIsStale_NeverFiresForAPausedTorrent). Global pause shields every torrent
// from the reaper through two mechanisms, and BOTH are needed:
//
//   - Existing downloads: the toggle drives each one through the same per-item
//     Pause the predicate's status clause already excludes.
//   - New downloads: dispatchToDownloadClient — the ONLY production caller of
//     Manager.AddTorrent — refuses with 423 Locked while the flag is set, so no
//     entry is ever created in a non-paused status during a global pause.
//
// Without the second half, a torrent added under global pause would sit at zero
// bytes with zero peers in a status the predicate does NOT shield, and the
// reaper would delete it after the threshold. That is why this asserts the
// refusal rather than assuming it.
func TestGlobalPause_ShieldsEveryTorrentFromTheStaleReaper(t *testing.T) {
	ctx := context.Background()
	s := newStaleTestStores(t)
	staging := t.TempDir()
	dl := downloader.NewForTesting(staging)
	dl.SetTestNextGID("gid-new")
	dl.SeedState(downloader.Download{GID: "gid-active", Status: "active", Dir: staging})
	dl.SeedState(downloader.Download{GID: "gid-waiting", Status: "waiting", Dir: staging})

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/downloads/pause-state", putPauseStateHandler(s.settings, dl, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(apidto.DownloadPauseState{Paused: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/downloads/pause-state", strings.NewReader(string(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT pause-state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT pause-state: status %d", resp.StatusCode)
	}

	for _, d := range dl.List() {
		if d.Status != "paused" {
			t.Errorf("download %s is %q after a global pause — the stale predicate only shields %q entries", d.GID, d.Status, "paused")
		}
	}

	// And nothing new can join the queue in an unshielded status.
	before := len(dl.List())
	sess := &mode.Session{Downloader: dl}
	_, gid, status, err := dispatchToDownloadClient(ctx, s.settings, sess, mode.Movies, nil, "torrent", "magnet:x", "New Title")
	if status != http.StatusLocked || !errors.Is(err, errDownloadsPaused) {
		t.Fatalf("a dispatch under global pause returned status %d, err %v — want 423 and errDownloadsPaused", status, err)
	}
	if gid != "" || len(dl.List()) != before {
		t.Fatalf("a torrent was added under global pause (gid %q, queue %d -> %d): it would sit unshielded at zero bytes and be reaped", gid, before, len(dl.List()))
	}
}

// seedRetryableSearch wires the connections and quality tier a retry re-search
// needs to find and grade a qualifying release: a TMDB runtime (the bitrate
// scorer's denominator) and a Prowlarr result that clears the floor.
func seedRetryableSearch(t *testing.T, s staleTestStores) {
	t.Helper()
	ctx := context.Background()
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 6000 s
	prowlarrSrv := fakeProwlarr(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	for _, c := range []struct{ service, url string }{{"tmdb", tmdbSrv.URL}, {"prowlarr", prowlarrSrv.URL}} {
		if err := s.conns.Upsert(ctx, c.service, c.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", c.service, err)
		}
	}
	if err := s.settings.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("setting quality tier: %v", err)
	}
}

func staleSessionBuilder(s staleTestStores, dl *downloader.Manager) sessionBuilderFunc {
	return func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, s.conns, s.svcConns, s.settings, testHTTPClient(), dl, m)
	}
}

func fetchRequests(t *testing.T, s staleTestStores) []apidto.RequestStatusItem {
	t.Helper()
	srv := httptest.NewServer(NewRequestsMux(s.grabs, s.library, s.excludes))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/requests")
	if err != nil {
		t.Fatalf("GET /api/requests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/requests: status %d", resp.StatusCode)
	}
	var out apidto.RequestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding /api/requests: %v", err)
	}
	return out.Items
}

// queueHas reports whether gid is still in the engine's queue. FindByGID
// returns (nil, nil) for an unknown gid rather than an error, so a bare error
// check would silently pass either way.
func queueHas(dl *downloader.Manager, gid string) bool {
	for _, d := range dl.List() {
		if d.GID == gid {
			return true
		}
	}
	return false
}

func findRequestItem(items []apidto.RequestStatusItem, title string) (apidto.RequestStatusItem, bool) {
	for _, it := range items {
		if it.Title == title {
			return it, true
		}
	}
	return apidto.RequestStatusItem{}, false
}
