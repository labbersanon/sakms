// Tests for the Adult monitor dispatch pass (monitorAdultEntities) and the
// un-monitor cleanup (cancelMonitorRetries / monitorOriginated).
//
// These are unit-level tests that call the internal functions directly — the
// same approach airdatemonitor_test.go uses for its dispatch and cancel
// functions. No HTTP server is started here; HTTP-level coverage lives in
// adult_monitor_handler_test.go.
package api

import (
	"context"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// --- monitorOriginated predicate ---

func TestMonitorOriginated_TrueForNeverDispatchedMonitorGrab(t *testing.T) {
	g := grabs.Grab{
		Mode:             mode.Adult,
		Status:           grabs.PendingRetry,
		MonitorEntityKey: "performer\x1ftpdb\x1fent-1",
		Indexer:          "", // never dispatched
		DownloadURL:      "",
	}
	if !monitorOriginated(g) {
		t.Error("expected monitorOriginated=true for a never-dispatched monitor grab")
	}
}

func TestMonitorOriginated_FalseWhenDispatched(t *testing.T) {
	g := grabs.Grab{
		Mode:             mode.Adult,
		Status:           grabs.PendingRetry,
		MonitorEntityKey: "performer\x1ftpdb\x1fent-1",
		Indexer:          "SomeIndexer", // was dispatched
		DownloadURL:      "http://example.com/release.nzb",
	}
	if monitorOriginated(g) {
		t.Error("expected monitorOriginated=false for a dispatched (then-retried) grab")
	}
}

func TestMonitorOriginated_FalseForNonAdultMode(t *testing.T) {
	g := grabs.Grab{
		Mode:             mode.Movies,
		Status:           grabs.PendingRetry,
		MonitorEntityKey: "performer\x1ftpdb\x1fent-1",
		Indexer:          "",
		DownloadURL:      "",
	}
	if monitorOriginated(g) {
		t.Error("expected monitorOriginated=false for non-Adult mode")
	}
}

func TestMonitorOriginated_FalseWhenKeyEmpty(t *testing.T) {
	g := grabs.Grab{
		Mode:             mode.Adult,
		Status:           grabs.PendingRetry,
		MonitorEntityKey: "",
		Indexer:          "",
		DownloadURL:      "",
	}
	if monitorOriginated(g) {
		t.Error("expected monitorOriginated=false when MonitorEntityKey is empty")
	}
}

func TestMonitorOriginated_FalseWhenNotPendingRetry(t *testing.T) {
	g := grabs.Grab{
		Mode:             mode.Adult,
		Status:           grabs.Queued,
		MonitorEntityKey: "performer\x1ftpdb\x1fent-1",
		Indexer:          "",
		DownloadURL:      "",
	}
	if monitorOriginated(g) {
		t.Error("expected monitorOriginated=false for queued (non-pending_retry) status")
	}
}

// --- cancelMonitorRetries ---

func TestCancelMonitorRetries_CancelsNeverDispatchedGrabs(t *testing.T) {
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	grabsStore := grabs.New(sqlDB, secretStore)
	ctx := context.Background()

	key := adultnewest.FormatMonitorKey("performer", "tpdb", "ent-99")

	// Create a never-dispatched monitor pending_retry grab.
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Adult, Title: "Monitor Scene",
		Indexer: "", Protocol: "", DownloadClient: "", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := grabsStore.SetMonitorEntityKey(ctx, g.ID, key); err != nil {
		t.Fatalf("SetMonitorEntityKey: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	cancelMonitorRetries(ctx, grabsStore, key)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != grabs.Failed {
		t.Errorf("expected Failed after cancelMonitorRetries; got %q", got.Status)
	}
}

func TestCancelMonitorRetries_DoesNotCancelDispatchedGrabs(t *testing.T) {
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	grabsStore := grabs.New(sqlDB, secretStore)
	ctx := context.Background()

	key := adultnewest.FormatMonitorKey("performer", "tpdb", "ent-dispatched")

	// Create a dispatched monitor grab (has indexer + download URL set).
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Adult, Title: "Dispatched Scene",
		Indexer: "SomeIndexer", Protocol: "usenet", DownloadClient: "nntp",
		DownloadURL: "http://example.com/scene.nzb",
		RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := grabsStore.SetMonitorEntityKey(ctx, g.ID, key); err != nil {
		t.Fatalf("SetMonitorEntityKey: %v", err)
	}
	// Put it in PendingRetry (simulating a dispatched grab that failed and is retrying).
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	cancelMonitorRetries(ctx, grabsStore, key)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Must remain PendingRetry — it was dispatched, so the un-monitor predicate
	// must NOT cancel it.
	if got.Status != grabs.PendingRetry {
		t.Errorf("dispatched grab status changed unexpectedly: got %q; want PendingRetry", got.Status)
	}
}

func TestCancelMonitorRetries_DifferentKeyNotCancelled(t *testing.T) {
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	grabsStore := grabs.New(sqlDB, secretStore)
	ctx := context.Background()

	keyA := adultnewest.FormatMonitorKey("performer", "tpdb", "ent-A")
	keyB := adultnewest.FormatMonitorKey("performer", "tpdb", "ent-B")

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Adult, Title: "Scene B",
		Indexer: "", Protocol: "", DownloadClient: "", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := grabsStore.SetMonitorEntityKey(ctx, g.ID, keyB); err != nil {
		t.Fatalf("SetMonitorEntityKey: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Cancel retries for key A — keyB grab must remain untouched.
	cancelMonitorRetries(ctx, grabsStore, keyA)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Errorf("keyB grab was cancelled when cancelling keyA; status=%q", got.Status)
	}
}

// --- monitorAdultEntities watermark ---

func TestMonitorAdultEntities_WatermarkSkipsOldScenes(t *testing.T) {
	// Verify that scenes whose first_seen_at <= monitored_since are skipped.
	// We set monitored_since to a future time so all pool scenes are "old".
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}

	ctx := context.Background()
	grabsStore := grabs.New(sqlDB, secretStore)
	releaseStore := adultnewest.NewReleaseStore(sqlDB)
	monitoredStore := adultnewest.NewMonitoredStore(sqlDB)
	settingsStore := settings.New(sqlDB)
	connStore := connections.New(sqlDB, secretStore)
	scStore := serviceconn.NewStore(sqlDB, secretStore)

	// Seed one scene in the pool.
	err = releaseStore.Insert(ctx, adultnewest.MatchedRelease{
		RowType:     adultnewest.RowScene,
		EntityID:    "scene-001",
		EntitySource: "tpdb",
		EntityTitle: "Old Scene",
		EntityStudio: "SomeStudio",
		Performers:  []string{"Alice"},
	})
	if err != nil {
		t.Fatalf("Insert scene: %v", err)
	}

	// Monitor Alice with a future monitored_since (after the scene was added).
	futureSince := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	_, err = monitoredStore.UpsertOnMonitor(ctx, "performer", "tpdb", "perf-alice", "Alice", "", futureSince)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}

	setAutoGrabToggle(t, settingsStore, true)

	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), nil, m)
	}

	monitorAdultEntities(ctx, deps, build, nil, monitoredStore, releaseStore, nil, time.Now())

	// No grabs should have been created — the scene is "older" than monitored_since.
	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("List grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 grabs (scene pre-dates monitored_since); got %d: %+v", len(list), list)
	}
}

func TestMonitorAdultEntities_NilStoreSkipsGracefully(t *testing.T) {
	// Passing nil for monitoredStore must not panic.
	ctx := context.Background()
	deps := AutoGrabDeps{}
	monitorAdultEntities(ctx, deps, nil, nil, nil, nil, nil, time.Now())
}

func TestMonitorAdultEntities_CapAtMaxGrabsPerCycle(t *testing.T) {
	// Verify that the pass caps at maxMonitorGrabsPerCycle across all entities.
	// We set up maxMonitorGrabsPerCycle+5 scenes but expect only maxMonitorGrabsPerCycle grabs.
	// Since no Prowlarr is configured, RunAutoGrab will park them as PendingRetry (no match).
	// We just check no panic and that List never exceeds the cap.
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	ctx := context.Background()
	grabsStore := grabs.New(sqlDB, secretStore)
	releaseStore := adultnewest.NewReleaseStore(sqlDB)
	monitoredStore := adultnewest.NewMonitoredStore(sqlDB)
	settingsStore := settings.New(sqlDB)
	connStore := connections.New(sqlDB, secretStore)
	scStore := serviceconn.NewStore(sqlDB, secretStore)

	// monitored_since in the past so all scenes pass the watermark.
	pastSince := "2000-01-01T00:00:00.000Z"
	_, err = monitoredStore.UpsertOnMonitor(ctx, "performer", "tpdb", "perf-cap", "Cap Test", "", pastSince)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}

	// Seed maxMonitorGrabsPerCycle+5 scenes linked to "Cap Test".
	for i := 0; i < maxMonitorGrabsPerCycle+5; i++ {
		if err := releaseStore.Insert(ctx, adultnewest.MatchedRelease{
			RowType:     adultnewest.RowScene,
			EntityID:    string(rune('A' + i%26)),
			EntitySource: "tpdb",
			EntityTitle: "Cap Scene",
			EntityStudio: "",
			Performers:  []string{"Cap Test"},
		}); err != nil {
			t.Fatalf("Insert scene %d: %v", i, err)
		}
	}

	setAutoGrabToggle(t, settingsStore, true)
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/adult"); err != nil {
		t.Fatalf("set adult root: %v", err)
	}

	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), nil, m)
	}

	monitorAdultEntities(ctx, deps, build, nil, monitoredStore, releaseStore, nil, time.Now())

	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("List grabs: %v", err)
	}
	if len(list) > maxMonitorGrabsPerCycle {
		t.Errorf("expected at most %d grabs per cycle (cap); got %d", maxMonitorGrabsPerCycle, len(list))
	}
}
