package nodesettings_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/nodesettings"
)

func newTestStore(t *testing.T) *nodesettings.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return nodesettings.New(sqlDB)
}

func TestGet_NeverSaved_OkFalse(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a node that was never saved")
	}
	if len(got.PathMappings) != 0 {
		t.Fatalf("expected no path mappings, got %+v", got.PathMappings)
	}
}

func TestSetThenGet_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	want := nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: "movies_library_root_folder", NodePath: "/mnt/movies"},
			{LibraryPathKey: "series_library_root_folder", NodePath: "/mnt/series"},
		},
		MaxJobs: 4,
	}
	if err := store.Set(ctx, "node-a", want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after Set")
	}
	if got.MaxJobs != want.MaxJobs {
		t.Errorf("MaxJobs: got %d, want %d", got.MaxJobs, want.MaxJobs)
	}
	if len(got.PathMappings) != 2 {
		t.Fatalf("expected 2 path mappings, got %d: %+v", len(got.PathMappings), got.PathMappings)
	}
	byKey := make(map[string]string, len(got.PathMappings))
	for _, e := range got.PathMappings {
		byKey[e.LibraryPathKey] = e.NodePath
	}
	if byKey["movies_library_root_folder"] != "/mnt/movies" {
		t.Errorf("movies mapping: got %q, want /mnt/movies", byKey["movies_library_root_folder"])
	}
	if byKey["series_library_root_folder"] != "/mnt/series" {
		t.Errorf("series mapping: got %q, want /mnt/series", byKey["series_library_root_folder"])
	}
}

// TestSet_PartialUpdate_DoesNotDeleteOtherKeys confirms Set never implicitly
// deletes a previously-saved key it wasn't given this time — the same
// "leave untouched" guarantee mergePathMap enforces node-side, enforced here
// on the server-side persisted record.
func TestSet_PartialUpdate_DoesNotDeleteOtherKeys(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: "movies_library_root_folder", NodePath: "/mnt/movies"},
			{LibraryPathKey: "series_library_root_folder", NodePath: "/mnt/series"},
		},
		MaxJobs: 2,
	}); err != nil {
		t.Fatalf("first Set: %v", err)
	}

	// Second save only touches movies (e.g. the operator only edited that
	// one row this time).
	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: "movies_library_root_folder", NodePath: "/mnt/movies-v2"},
		},
		MaxJobs: 3,
	}); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(got.PathMappings) != 2 {
		t.Fatalf("expected series' row to survive the partial update, got %d rows: %+v", len(got.PathMappings), got.PathMappings)
	}
	byKey := make(map[string]string, len(got.PathMappings))
	for _, e := range got.PathMappings {
		byKey[e.LibraryPathKey] = e.NodePath
	}
	if byKey["movies_library_root_folder"] != "/mnt/movies-v2" {
		t.Errorf("movies mapping not updated: got %q", byKey["movies_library_root_folder"])
	}
	if byKey["series_library_root_folder"] != "/mnt/series" {
		t.Errorf("series mapping was wrongly deleted/changed: got %q, want /mnt/series (unchanged)", byKey["series_library_root_folder"])
	}
	if got.MaxJobs != 3 {
		t.Errorf("MaxJobs: got %d, want 3 (updated)", got.MaxJobs)
	}
}

// TestSetThenGet_CPUCapPercent_RoundTrip proves the new cpu_cap_percent column
// is written by Set and read back by Get, including an explicit 0 (unlimited/
// clear) that must be preserved distinctly rather than lost — the same discipline
// MaxJobs already follows. CPUCapPercent is operator-owned and rides the shared
// Set write path alongside MaxJobs (unlike PauseDispatch's own column-scoped
// method), so it coexists with both on the one node_max_jobs row.
func TestSetThenGet_CPUCapPercent_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Write a non-zero cap alongside MaxJobs and a pause bit.
	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		MaxJobs:       4,
		CPUCapPercent: 50,
	}); err != nil {
		t.Fatalf("Set (cap=50): %v", err)
	}
	if err := store.SetPauseDispatch(ctx, "node-a", true); err != nil {
		t.Fatalf("SetPauseDispatch: %v", err)
	}

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after Set")
	}
	if got.CPUCapPercent != 50 {
		t.Errorf("CPUCapPercent: got %d, want 50", got.CPUCapPercent)
	}
	if got.MaxJobs != 4 {
		t.Errorf("MaxJobs coexistence: got %d, want 4", got.MaxJobs)
	}
	if !got.PauseDispatch {
		t.Error("PauseDispatch coexistence: got false, want true (the shared row must carry all three)")
	}

	// An explicit 0 clears the cap and is read back as 0 (distinct from unset).
	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		MaxJobs:       4,
		CPUCapPercent: 0,
	}); err != nil {
		t.Fatalf("Set (cap=0): %v", err)
	}
	got, _, err = store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if got.CPUCapPercent != 0 {
		t.Errorf("CPUCapPercent after clear: got %d, want 0", got.CPUCapPercent)
	}
	// The pause bit (its own column-scoped writer) is untouched by the cap write.
	if !got.PauseDispatch {
		t.Error("a CPUCapPercent write must not reset the independently-owned PauseDispatch")
	}
}

func TestSetThenGet_DifferentNodesAreIsolated(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{{LibraryPathKey: "movies_library_root_folder", NodePath: "/a/movies"}},
		MaxJobs:      1,
	}); err != nil {
		t.Fatalf("Set node-a: %v", err)
	}
	if err := store.Set(ctx, "node-b", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{{LibraryPathKey: "movies_library_root_folder", NodePath: "/b/movies"}},
		MaxJobs:      2,
	}); err != nil {
		t.Fatalf("Set node-b: %v", err)
	}

	a, _, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get node-a: %v", err)
	}
	b, _, err := store.Get(ctx, "node-b")
	if err != nil {
		t.Fatalf("Get node-b: %v", err)
	}
	if a.PathMappings[0].NodePath != "/a/movies" || a.MaxJobs != 1 {
		t.Errorf("node-a: got %+v", a)
	}
	if b.PathMappings[0].NodePath != "/b/movies" || b.MaxJobs != 2 {
		t.Errorf("node-b: got %+v", b)
	}
}

func TestSetThenGet_VerificationStatusRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{
				LibraryPathKey:     "movies_library_root_folder",
				NodePath:           "/mnt/movies",
				VerificationStatus: nodesettings.VerificationVerified,
				VerifiedAt:         &now,
			},
			{
				LibraryPathKey:     "series_library_root_folder",
				NodePath:           "/mnt/series",
				VerificationStatus: nodesettings.VerificationUnverifiedBootstrap,
			},
			{
				LibraryPathKey:     "adult_library_root_folder",
				NodePath:           "/mnt/adult",
				VerificationStatus: nodesettings.VerificationUnverifiedApproval,
			},
		},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	byKey := make(map[string]nodesettings.PathMappingEntry, len(got.PathMappings))
	for _, e := range got.PathMappings {
		byKey[e.LibraryPathKey] = e
	}

	movies := byKey["movies_library_root_folder"]
	if movies.VerificationStatus != nodesettings.VerificationVerified {
		t.Errorf("movies status: got %q, want verified", movies.VerificationStatus)
	}
	if movies.VerifiedAt == nil || !movies.VerifiedAt.Equal(now) {
		t.Errorf("movies verifiedAt: got %v, want %v", movies.VerifiedAt, now)
	}

	series := byKey["series_library_root_folder"]
	if series.VerificationStatus != nodesettings.VerificationUnverifiedBootstrap {
		t.Errorf("series status: got %q, want unverified_bootstrap", series.VerificationStatus)
	}
	if series.VerifiedAt != nil {
		t.Errorf("series verifiedAt: got %v, want nil", series.VerifiedAt)
	}

	adult := byKey["adult_library_root_folder"]
	if adult.VerificationStatus != nodesettings.VerificationUnverifiedApproval {
		t.Errorf("adult status: got %q, want unverified_approval", adult.VerificationStatus)
	}
}
