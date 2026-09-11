package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
