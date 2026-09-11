package api

import (
	"context"
	"testing"

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

// Sole park path: unknown GID + empty DownloadURL. Unknown GID alone must never park.
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

// Forgotten grab with a durable URL stays queued (relaunch attempted), never mass-parked.
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
