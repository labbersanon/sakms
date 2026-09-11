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

func TestReconcileInFlightDownloads_UnknownGIDWithURLDoesNotPark(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})

	g := dispatchedUsenetGrab(t, grabsStore, "nzb-cccccccccccccccc")
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
		t.Fatal("expected New() manager without Start to report EngineReady=false")
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
		t.Fatal("parked pending_retry — engine-not-ready must defer, not park")
	}
	if got.DownloadGID != gid {
		t.Fatalf("download GID = %q, want unchanged %q", got.DownloadGID, gid)
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
	if got.Status == grabs.PendingRetry {
		t.Fatal("parked — sample reject must fall through to relaunch, not park")
	}
	if _, err := os.Stat(filepath.Join(dir, "Movie.Sample.mkv")); err != nil {
		t.Fatalf("sample staging was deleted: %v", err)
	}
}

func TestReconcileInFlightDownloads_ResumeSidecarBlocksImport(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	gid := "nzb-eeeeeeeeeeeeeeee"
	dir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.ResumeFileName), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, minUsenetReconcileImportBytes+64)
	if err := os.WriteFile(filepath.Join(dir, "Feature.mkv"), payload, 0o644); err != nil {
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
		t.Fatal("imported while resume sidecar present — mid-download must relaunch, not import")
	}
	if _, err := os.Stat(filepath.Join(dir, "Feature.mkv")); err != nil {
		t.Fatalf("staging wiped despite incomplete resume: %v", err)
	}
}

func TestReconcileInFlightDownloads_OwnedCompleteImportsAndCleans(t *testing.T) {
	ctx := context.Background()
	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	staging := t.TempDir()
	root := t.TempDir()
	nzb := usenet.New(usenet.Config{StagingDir: staging})
	gid := "nzb-ffffffffffffffff"
	dir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, usenet.OwnedMarkerFile), []byte("sakms\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, minUsenetReconcileImportBytes+64)
	if err := os.WriteFile(filepath.Join(dir, "Some.Movie.2020.mkv"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: root, DownloadURL: "https://indexer.example/nzb?id=complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatal(err)
	}

	ReconcileInFlightDownloads(ctx, DownloadReconcileDeps{
		ConnStore: connStore, SettingsStore: settingsStore, GrabsStore: grabsStore, LibStore: libStore, NZB: nzb,
	})
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Imported {
		t.Fatalf("status = %q, want imported", got.Status)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("owned staging dir still present after successful import: %v", err)
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
