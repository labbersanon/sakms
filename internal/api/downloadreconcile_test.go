package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/usenet"
)

// TestReconcileInFlightDownloads_SkipsLiveEngineGID ensures a grab whose usenet
// engine still knows the GID is left alone — restore is only for forgotten jobs.
func TestReconcileInFlightDownloads_SkipsLiveEngineGID(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	gid := "nzb-aaaaaaaaaaaaaaaa"
	nzb.InjectDownloadForTest(gid)

	g := dispatchedUsenetGrab(t, grabsStore, gid)
	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Queued && got.Status != grabs.Downloading {
		t.Fatalf("status = %q, want still in-flight (not parked)", got.Status)
	}
	if got.DownloadGID != gid {
		t.Fatalf("download GID changed to %q", got.DownloadGID)
	}
}

// TestReconcileInFlightDownloads_ParksOnlyWhenURLMissing is the sole park path:
// unknown GID + empty DownloadURL. Unknown GID alone must never park.
func TestReconcileInFlightDownloads_ParksOnlyWhenURLMissing(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Forgotten", TMDBID: 7,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	gid := "nzb-bbbbbbbbbbbbbbbb"
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatal(err)
	}

	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q, want pending_retry", got.Status)
	}
	if got.DownloadGID != "" {
		t.Fatalf("download GID = %q, want cleared by park", got.DownloadGID)
	}
}

// TestReconcileInFlightDownloads_UnknownGIDWithURLDoesNotPark guards the
// anti-boot-storm rule: a forgotten grab that still has a durable URL stays
// queued (relaunch attempted) rather than mass-parking on restart.
func TestReconcileInFlightDownloads_UnknownGIDWithURLDoesNotPark(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})

	g := dispatchedUsenetGrab(t, grabsStore, "nzb-cccccccccccccccc")
	// Engine does not know the GID; URL is present (dispatchedUsenetGrab sets one).
	// Relaunch will fail (no NZB HTTP), but must NOT park.
	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == grabs.PendingRetry {
		t.Fatalf("parked pending_retry — unknown GID with URL must not park")
	}
	if got.DownloadGID == "" {
		t.Fatal("download GID cleared — relaunch failure must not park")
	}
}

func TestReconcileInFlightDownloads_TorrentDefersWhenEngineNotReady(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := downloader.New(downloader.Config{StagingDir: t.TempDir()}, http.DefaultClient)
	if dl.EngineReady() {
		t.Fatal("expected New() without Start to report EngineReady=false")
	}
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Torrent Wait", TMDBID: 9,
		Indexer: "I", Protocol: "torrent", DownloadClient: "torrent",
		RootFolderPath: "/movies", DownloadURL: "https://example.invalid/a.torrent",
	})
	if err != nil {
		t.Fatal(err)
	}
	gid := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatal(err)
	}
	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, DL: dl,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == grabs.PendingRetry {
		t.Fatal("parked — engine-not-ready must defer, not park")
	}
	if got.DownloadGID != gid {
		t.Fatalf("download GID = %q, want unchanged", got.DownloadGID)
	}
}

func TestReconcileInFlightDownloads_SampleVideoDoesNotImport(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	gid := "nzb-dddddddddddddddd"
	dir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, minUsenetReconcileImportBytes+64)
	if err := os.WriteFile(filepath.Join(dir, "Movie.Sample.mkv"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	g := dispatchedUsenetGrab(t, grabsStore, gid)
	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, LibStore: libStore, NZB: nzb,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == grabs.Imported {
		t.Fatal("imported sample video — completeness gate must reject samples")
	}
	if _, err := os.Stat(filepath.Join(dir, "Movie.Sample.mkv")); err != nil {
		t.Fatalf("sample staging was deleted: %v", err)
	}
}

func TestUsenetStagingReadyForImport_Gates(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "other-client-dir")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, why := usenetStagingReadyForImport(root, foreign); ok || why != "not owned staging" {
		t.Fatalf("unowned = (%v, %q)", ok, why)
	}
	gid := "nzb-1111111111111111"
	dir := filepath.Join(root, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, why := usenetStagingReadyForImport(root, dir); ok || why != "no video" {
		t.Fatalf("owned empty = (%v, %q)", ok, why)
	}
}

func TestReconcileInFlightDownloads_ForceFullSkipsImport(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	nzb.SetForceFullForTest(true)

	gid := "nzb-ffffffffffffffff"
	dir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, minUsenetReconcileImportBytes+64)
	for i := range payload {
		payload[i] = byte('A' + (i % 26))
	}
	if err := os.WriteFile(filepath.Join(dir, "Movie.mkv"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.ResumeFileName), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := dispatchedUsenetGrab(t, grabsStore, gid)
	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, LibStore: libStore, NZB: nzb,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == grabs.Imported {
		t.Fatal("force-full must skip staging import")
	}
	if _, err := os.Stat(filepath.Join(dir, "Movie.mkv")); err != nil {
		t.Fatalf("payload should remain for relaunch path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, usenet.ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("force-full reconcile should clear resume sidecar")
	}
}

// nzbXMLOneSeg is a minimal one-segment NZB for relaunch throttle tests.
const nzbXMLOneSeg = `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1" subject="a.mkv">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="100" number="1">seg1@test</segment>
    </segments>
  </file>
</nzb>`

func startNZBFixture(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		_, _ = w.Write([]byte(nzbXMLOneSeg))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// forgottenUsenetGrab records an in-flight usenet grab the engine no longer
// knows about, with owned (but empty) staging so reconcile takes the relaunch
// path rather than importing.
func forgottenUsenetGrab(t *testing.T, grabsStore *grabs.Store, staging, gid, nzbURL string) grabs.Grab {
	t.Helper()
	dir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Throttle " + gid, TMDBID: 42,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: nzbURL,
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("setting download gid: %v", err)
	}
	return g
}

// TestUsenetRelaunchSlots_CountsPipelineOnly: relaunch capacity tracks
// precheck|waiting, not BODY downloading (precheck-ahead).
func TestUsenetRelaunchSlots_CountsPipelineOnly(t *testing.T) {
	resetUsenetReconcileDrainForTest()
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 2})
	if got := usenetRelaunchSlots(nzb); got != 2 {
		t.Fatalf("empty manager slots = %d, want 2", got)
	}
	nzb.InjectDownloadForTest("nzb-aaaaaaaaaaaaaaaa") // downloading — does not fill pipeline
	if got := usenetRelaunchSlots(nzb); got != 2 {
		t.Fatalf("one downloading slots = %d, want 2 (pipeline free)", got)
	}
	nzb.InjectDownloadPhaseForTest("nzb-bbbbbbbbbbbbbbbb", "waiting")
	if got := usenetRelaunchSlots(nzb); got != 1 {
		t.Fatalf("one waiting slots = %d, want 1", got)
	}
	nzb.InjectDownloadPhaseForTest("nzb-cccccccccccccccc", "precheck")
	if got := usenetRelaunchSlots(nzb); got != 0 {
		t.Fatalf("waiting+precheck slots = %d, want 0", got)
	}
}

// TestReconcileInFlightDownloads_ThrottlesUsenetRelaunches caps kickoffs to
// MaxConcurrentDownloads so boot does not stampede precheck + runDownload.
func TestReconcileInFlightDownloads_ThrottlesUsenetRelaunches(t *testing.T) {
	resetUsenetReconcileDrainForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	srv := startNZBFixture(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{
		StagingDir: staging, HTTPClient: srv.Client(), MaxConcurrentDownloads: 1,
	})
	go nzb.Start(ctx)

	for _, gid := range []string{"nzb-1111111111111111", "nzb-2222222222222222", "nzb-3333333333333333"} {
		forgottenUsenetGrab(t, grabsStore, staging, gid, srv.URL)
	}

	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb,
	})

	// A relaunch starts then fails immediately (no NNTP pools), so count every
	// engine entry rather than active ones — the gate is kickoffs, not survivors.
	if got := len(nzb.List()); got != 1 {
		t.Fatalf("engine downloads = %d, want 1 (throttled to MaxConcurrentDownloads); list=%v", got, nzb.List())
	}
}

// TestReconcileInFlightDownloads_DrainResumesWhenSlotFrees ensures deferred
// relaunches are not stuck until the 24h retry tick. Pipeline full (Waiting)
// blocks further relaunches; freeing it lets the drain proceed.
func TestReconcileInFlightDownloads_DrainResumesWhenSlotFrees(t *testing.T) {
	resetUsenetReconcileDrainForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	srv := startNZBFixture(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{
		StagingDir: staging, HTTPClient: srv.Client(), MaxConcurrentDownloads: 1,
	})
	go nzb.Start(ctx)

	oldInterval := usenetReconcileDrainInterval
	usenetReconcileDrainInterval = 20 * time.Millisecond
	t.Cleanup(func() { usenetReconcileDrainInterval = oldInterval })

	gids := []string{"nzb-4444444444444444", "nzb-5555555555555555"}
	for _, gid := range gids {
		forgottenUsenetGrab(t, grabsStore, staging, gid, srv.URL)
	}

	// Fill the precheck pipeline so relaunches defer (BODY can still be free).
	nzb.InjectDownloadPhaseForTest("nzb-occupiedoccupied", "waiting")

	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb,
	})
	if got := len(nzb.List()); got != 1 {
		t.Fatalf("after throttled pass List len = %d, want 1 (only injected Waiting)", got)
	}

	// Free the pipeline slot; drain should relaunch one deferred grab.
	if err := nzb.Cancel("nzb-occupiedoccupied"); err != nil {
		t.Fatal(err)
	}

	// Claude 2026-09-23: 10s, not 2s.
	// Reason: go-test runs with -p 4; a 2s wait missed the 20ms drain tick on
	//   a loaded GitHub runner (TestReconcileInFlightDownloads_DrainResumesWhenSlotFrees).
	// Troubleshooting: "drain did not relaunch" with an empty list means the
	//   drain never ran; with only the injected GID, Cancel did not free the slot.
	// Review if: usenetReconcileDrainInterval is no longer shortened in this test.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, d := range nzb.List() {
			if d.GID == gids[0] || d.GID == gids[1] {
				return // drain relaunched at least one deferred grab
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("drain did not relaunch a deferred grab after slot freed; list=%v", nzb.List())
}

// TestReconcileInFlightDownloads_PrefersResumeSidecar: with one slot, the grab
// that already has .sakms-resume.json relaunches before an empty staging peer.
func TestReconcileInFlightDownloads_PrefersResumeSidecar(t *testing.T) {
	resetUsenetReconcileDrainForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	srv := startNZBFixture(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{
		StagingDir: staging, HTTPClient: srv.Client(), MaxConcurrentDownloads: 1,
	})
	go nzb.Start(ctx)

	emptyGID := "nzb-aaaaaaaaaaaaaaaa"
	resumeGID := "nzb-bbbbbbbbbbbbbbbb"
	_ = forgottenUsenetGrab(t, grabsStore, staging, emptyGID, srv.URL)
	_ = forgottenUsenetGrab(t, grabsStore, staging, resumeGID, srv.URL)
	if err := os.WriteFile(filepath.Join(staging, resumeGID, usenet.ResumeFileName), []byte(`{"v":3}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb,
	})

	list := nzb.List()
	if len(list) != 1 {
		t.Fatalf("engine downloads = %d, want 1; list=%v", len(list), list)
	}
	if list[0].GID != resumeGID {
		t.Fatalf("relaunched %q, want resume-sidecar grab %q", list[0].GID, resumeGID)
	}
}

// TestSortUsenetReconcilePriority_OrdersSidecarFirst pins the pure sort helper.
func TestSortUsenetReconcilePriority_OrdersSidecarFirst(t *testing.T) {
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	sidecarGID := "nzb-sidecar00000001"
	emptyGID := "nzb-empty0000000002"
	for _, gid := range []string{sidecarGID, emptyGID} {
		dir := filepath.Join(staging, gid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, sidecarGID, usenet.ResumeFileName), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	list := []grabs.Grab{
		{ID: 2, Status: grabs.Queued, DownloadGID: emptyGID},
		{ID: 1, Status: grabs.Queued, DownloadGID: sidecarGID},
	}
	sortUsenetReconcilePriority(nzb, list)
	if list[0].DownloadGID != sidecarGID {
		t.Fatalf("first = %q, want sidecar %q", list[0].DownloadGID, sidecarGID)
	}
}
