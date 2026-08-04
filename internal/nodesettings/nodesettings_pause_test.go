package nodesettings_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/nodesettings"
)

// TestSetPauseDispatch_NoPriorRow_GetOkTrue pins the load-bearing chain the
// reconnect-seeding path depends on: SetPauseDispatch on a node with no prior
// max_jobs/path-mapping row must INSERT a node_max_jobs row (max_jobs seeded 0)
// so a subsequent Get reports ok=true — otherwise seedNodePause would never
// fire for a node whose only persisted state is a pause.
func TestSetPauseDispatch_NoPriorRow_GetOkTrue(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.SetPauseDispatch(ctx, "node-a", true); err != nil {
		t.Fatalf("SetPauseDispatch: %v", err)
	}

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after SetPauseDispatch (a lone pause bit is persisted state)")
	}
	if !got.PauseDispatch {
		t.Error("PauseDispatch: got false, want true")
	}
	if got.MaxJobs != 0 {
		t.Errorf("MaxJobs: got %d, want 0 (fresh-row default seed)", got.MaxJobs)
	}
}

// TestSetPauseDispatch_FlipsOnlyPause_PreservesMaxJobsAndPathMap proves the
// column-scoped upsert touches only pause_dispatch: a full Set (MaxJobs +
// path mappings) followed by SetPauseDispatch leaves MaxJobs and every path
// mapping intact, provable by a Get round-trip.
func TestSetPauseDispatch_FlipsOnlyPause_PreservesMaxJobsAndPathMap(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: "movies_library_root_folder", NodePath: "/mnt/movies"},
			{LibraryPathKey: "series_library_root_folder", NodePath: "/mnt/series"},
		},
		MaxJobs: 4,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.SetPauseDispatch(ctx, "node-a", true); err != nil {
		t.Fatalf("SetPauseDispatch: %v", err)
	}

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !got.PauseDispatch {
		t.Error("PauseDispatch: got false, want true")
	}
	if got.MaxJobs != 4 {
		t.Errorf("MaxJobs: got %d, want 4 (unchanged by pause write)", got.MaxJobs)
	}
	if len(got.PathMappings) != 2 {
		t.Fatalf("path mappings: got %d, want 2 (unchanged by pause write): %+v", len(got.PathMappings), got.PathMappings)
	}
	byKey := make(map[string]string, len(got.PathMappings))
	for _, e := range got.PathMappings {
		byKey[e.LibraryPathKey] = e.NodePath
	}
	if byKey["movies_library_root_folder"] != "/mnt/movies" || byKey["series_library_root_folder"] != "/mnt/series" {
		t.Errorf("path mappings changed by pause write: %+v", byKey)
	}
}

// TestSet_DoesNotChangePauseDispatch proves the reverse direction: an unrelated
// Set (the MaxJobs/PathMap writer) never touches pause_dispatch after a pause
// has been set. This is the other half of the parallel-write footgun guard.
func TestSet_DoesNotChangePauseDispatch(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.SetPauseDispatch(ctx, "node-a", true); err != nil {
		t.Fatalf("SetPauseDispatch: %v", err)
	}

	// A later MaxJobs save must not clear the pause.
	if err := store.Set(ctx, "node-a", nodesettings.Settings{MaxJobs: 7}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, _, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.PauseDispatch {
		t.Error("PauseDispatch was cleared by an unrelated Set — the two writers interfered")
	}
	if got.MaxJobs != 7 {
		t.Errorf("MaxJobs: got %d, want 7", got.MaxJobs)
	}

	// And a SetPauseDispatch(false) must leave that MaxJobs untouched.
	if err := store.SetPauseDispatch(ctx, "node-a", false); err != nil {
		t.Fatalf("SetPauseDispatch(false): %v", err)
	}
	got, _, err = store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PauseDispatch {
		t.Error("PauseDispatch: got true, want false after unpause")
	}
	if got.MaxJobs != 7 {
		t.Errorf("MaxJobs: got %d, want 7 (unchanged by pause write)", got.MaxJobs)
	}
}

// TestPauseDispatch_PersistsAcrossReopen proves server-restart durability: the
// pause bit survives closing and reopening the store on the same Postgres schema.
func TestPauseDispatch_PersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	schema := dbtest.SharedSchema(t)

	sqlDB := dbtest.OpenSchema(t, schema)
	if err := nodesettings.New(sqlDB).SetPauseDispatch(ctx, "node-a", true); err != nil {
		t.Fatalf("SetPauseDispatch: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// Reopen the same schema — simulates a server restart.
	sqlDB2 := dbtest.OpenSchema(t, schema)

	got, ok, err := nodesettings.New(sqlDB2).Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after reopen")
	}
	if !got.PauseDispatch {
		t.Error("PauseDispatch did not survive a store reopen")
	}
}
