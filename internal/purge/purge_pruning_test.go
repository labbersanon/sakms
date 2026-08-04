package purge

// Claude 2026-08-03: new file — pruning-rule integration coverage for Purge's
// three propose paths (B4, plan §9.4).
// Reason: kept separate from purge_library_test.go / _series_test.go /
// _adult_library_test.go on purpose. Those files' Reason assertions are the
// backward-compatibility signal (§3.2's byte-identical invariant); a diff to
// them would mean the invariant broke, so the new behaviour is asserted here
// instead of by editing them.
// Troubleshooting: TestScanLibrary_TagAndRuleBothMatch_ProducesExactlyOneProposal
// is the important one — two proposals sharing a TrackedID would make the
// second Apply fail confusingly after the first deleted the row.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/pruning"
)

// newTestLibraryStoreDB is newTestLibraryStore plus the *sql.DB behind it, for
// the one test that needs to make an episode query observably fail.
func newTestLibraryStoreDB(t *testing.T) (*library.Store, *sql.DB) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return library.New(sqlDB), sqlDB
}

// ageDaysAgo renders an RFC3339 created_at n days before now, in the same
// shape SQLite's strftime default writes.
func ageDaysAgo(n int) string {
	return time.Now().UTC().Add(-time.Duration(n) * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
}

// setCreatedAt backdates a library row so age conditions have something real
// to compare against — Upsert always writes "now", and CreatedAt is not an
// input to any Upsert.
func setCreatedAt(t *testing.T, sqlDB *sql.DB, table string, id int64, createdAt string) {
	t.Helper()
	if _, err := sqlDB.Exec(fmt.Sprintf("UPDATE %s SET created_at = ? WHERE id = ?", table), createdAt, id); err != nil {
		t.Fatalf("backdating %s %d: %v", table, id, err)
	}
}

func ageRule(name string, days int) pruning.Rule {
	return pruning.Rule{Name: name, Mode: string(mode.Movies), AgeDays: days, Enabled: true}
}

// TestScanLibrary_NilRules_ReasonUnchanged is §3.2's byte-identical
// backward-compatibility invariant: with no rules configured, the emitted
// Reason is exactly the string this package produced before pruning rules
// existed.
func TestScanLibrary_NilRules_ReasonUnchanged(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Flagged Movie", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, item.ID, "junk"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, rules := range [][]pruning.Rule{nil, {}} {
		got, err := ScanLibrary(ctx, libStore, []string{"junk"}, rules)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 proposal, got %d", len(got))
		}
		if got[0].Reason != "matched allowlist tag(s): junk" {
			t.Errorf("Reason = %q, want the unchanged tag-only reason", got[0].Reason)
		}
	}
}

func TestScanLibrary_RuleMatchOnly_ProducesProposalWithRuleReason(t *testing.T) {
	libStore, sqlDB := newTestLibraryStoreDB(t)
	ctx := context.Background()

	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Old Movie", RootFolderPath: "/media/Movies",
		Size: 8_804_682_956, QualityTier: "low",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setCreatedAt(t, sqlDB, "library_items", item.ID, ageDaysAgo(412))

	got, err := ScanLibrary(ctx, libStore, nil, []pruning.Rule{{
		Name: "Old low-quality movies", Mode: string(mode.Movies),
		AgeDays: 365, SizeBytes: 1 << 30, QualityTierFloor: "low", Enabled: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d: %+v", len(got), got)
	}
	if got[0].TrackedID != int(item.ID) {
		t.Errorf("TrackedID = %d, want %d", got[0].TrackedID, item.ID)
	}
	// The Reason carries the ACTUAL triggering values, not the rule's
	// thresholds — the property that makes it diagnostic (plan §5.1).
	want := "Matched rule 'Old low-quality movies': 412 days old, 8.2GB, tier: low"
	if got[0].Reason != want {
		t.Errorf("Reason = %q, want %q", got[0].Reason, want)
	}
}

// TestScanLibrary_TagAndRuleBothMatch_ProducesExactlyOneProposal is the
// duplicate-TrackedID guard (§3.2): an item matching both signals is staged
// ONCE with a combined Reason, never twice.
func TestScanLibrary_TagAndRuleBothMatch_ProducesExactlyOneProposal(t *testing.T) {
	libStore, sqlDB := newTestLibraryStoreDB(t)
	ctx := context.Background()

	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Both", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, item.ID, "junk"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setCreatedAt(t, sqlDB, "library_items", item.ID, ageDaysAgo(500))

	got, err := ScanLibrary(ctx, libStore, []string{"junk"}, []pruning.Rule{ageRule("Stale", 365)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal for an item matching BOTH a tag and a rule, got %d: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Reason, "matched allowlist tag(s): junk; Matched rule 'Stale': ") {
		t.Errorf("Reason = %q, want the tag fragment first then the rule fragment", got[0].Reason)
	}
}

func TestScanLibrary_ItemMatchingNeither_ProducesNoProposal(t *testing.T) {
	libStore, sqlDB := newTestLibraryStoreDB(t)
	ctx := context.Background()

	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Fresh", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddTag(ctx, item.ID, "keep"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setCreatedAt(t, sqlDB, "library_items", item.ID, ageDaysAgo(3))

	got, err := ScanLibrary(ctx, libStore, []string{"junk"}, []pruning.Rule{ageRule("Stale", 365)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposals, got %+v", got)
	}
}

func TestScanLibraryAdult_RuleMatch_ProducesProposalWithRuleReason(t *testing.T) {
	libStore, sqlDB := newTestLibraryStoreDB(t)
	ctx := context.Background()

	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-1", Title: "Big Old Scene", RootFolderPath: "/media/Adult",
		Size: 2 << 30, QualityTier: "medium",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setCreatedAt(t, sqlDB, "library_scenes", scene.ID, ageDaysAgo(30))

	got, err := ScanLibraryAdult(ctx, libStore, nil, []pruning.Rule{{
		Name: "Big scenes", Mode: string(mode.Adult), SizeBytes: 1 << 30, Enabled: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d: %+v", len(got), got)
	}
	if got[0].Reason != "Matched rule 'Big scenes': 2GB" {
		t.Errorf("Reason = %q, want the size-only rule fragment", got[0].Reason)
	}
	// SourcePath is load-bearing for ApplyLibraryAdult — a rule-flagged scene
	// must carry it exactly as a tag-flagged one does.
	if got[0].TrackedID != int(scene.ID) {
		t.Errorf("TrackedID = %d, want %d", got[0].TrackedID, scene.ID)
	}
}

// --- Series aggregation (§2.4) -------------------------------------------

// seedSeries creates a series with the given episode (size, tier) pairs.
func seedSeries(t *testing.T, libStore *library.Store, tmdbID int, title string, episodes [][2]any) library.Series {
	t.Helper()
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: tmdbID, Title: title, RootFolderPath: "/media/TV"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, ep := range episodes {
		if _, err := libStore.UpsertEpisode(ctx, library.Episode{
			SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: i + 1,
			FilePath: fmt.Sprintf("/media/TV/%s/s01e%02d.mkv", title, i+1),
			Size:     int64(ep[0].(int)), QualityTier: ep[1].(string),
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	return series
}

func TestScanLibrarySeries_SizeIsSumOfEpisodeSizes(t *testing.T) {
	libStore := newTestLibraryStore(t)
	series := seedSeries(t, libStore, 1, "Show", [][2]any{{600, "high"}, {500, "high"}})

	// Threshold 1100 == the exact sum, so it matches only if the sizes were
	// summed (either episode alone is short of it).
	got, err := ScanLibrarySeries(context.Background(), libStore, nil, []pruning.Rule{{
		Name: "Big shows", Mode: string(mode.Series), SizeBytes: 1100, Enabled: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].TrackedID != int(series.ID) {
		t.Fatalf("expected the series staged on the summed size, got %+v", got)
	}
	if got[0].Reason != "Matched rule 'Big shows': 1.1KB" {
		t.Errorf("Reason = %q, want the summed size", got[0].Reason)
	}
}

// TestScanLibrarySeries_TierIsBestAcrossEpisodes pins the whole point of §2.4:
// one high-quality episode protects the show from a "low or below" rule, even
// though another episode is low.
func TestScanLibrarySeries_TierIsBestAcrossEpisodes(t *testing.T) {
	libStore := newTestLibraryStore(t)
	seedSeries(t, libStore, 1, "Mostly Good Show", [][2]any{{100, "low"}, {100, "lossless"}})
	allLow := seedSeries(t, libStore, 2, "All Low Show", [][2]any{{100, "low"}, {100, "low"}})

	got, err := ScanLibrarySeries(context.Background(), libStore, nil, []pruning.Rule{{
		Name: "Low quality shows", Mode: string(mode.Series), QualityTierFloor: "low", Enabled: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly the all-low show staged, got %d: %+v", len(got), got)
	}
	if got[0].TrackedID != int(allLow.ID) {
		t.Fatalf("staged the wrong series — a single lossless episode must protect the whole show: %+v", got[0])
	}
}

func TestScanLibrarySeries_AllEpisodeTiersUnknown_TierConditionNeverMatches(t *testing.T) {
	libStore := newTestLibraryStore(t)
	seedSeries(t, libStore, 1, "Unbackfilled Show", [][2]any{{100, ""}, {100, library.TierUnknown}})

	// "lossless" is the broadest possible floor — every real tier is at or
	// below it. An un-backfilled series must still not match.
	got, err := ScanLibrarySeries(context.Background(), libStore, nil, []pruning.Rule{{
		Name: "Anything", Mode: string(mode.Series), QualityTierFloor: "lossless", Enabled: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an un-backfilled series matched a tier rule — the fail-closed sentinel rule is broken: %+v", got)
	}
}

func TestScanLibrarySeries_AgeOnlyRuleUsesSeriesCreatedAt(t *testing.T) {
	libStore, sqlDB := newTestLibraryStoreDB(t)
	series := seedSeries(t, libStore, 1, "Old Show", [][2]any{{100, "high"}})
	setCreatedAt(t, sqlDB, "library_series", series.ID, ageDaysAgo(400))

	got, err := ScanLibrarySeries(context.Background(), libStore, nil, []pruning.Rule{{
		Name: "Stale shows", Mode: string(mode.Series), AgeDays: 365, Enabled: true,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Reason != "Matched rule 'Stale shows': 400 days old" {
		t.Fatalf("expected the series staged on its own CreatedAt, got %+v", got)
	}
}

// TestScanLibrarySeries_NoSizeOrTierRules_DoesNotQueryEpisodes is the N+1
// guard (§2.4). It proves the absence of a query by making that query fail:
// with library_episodes renamed out from under the store, a scan that touches
// it errors and a scan that doesn't succeeds.
func TestScanLibrarySeries_NoSizeOrTierRules_DoesNotQueryEpisodes(t *testing.T) {
	libStore, sqlDB := newTestLibraryStoreDB(t)
	ctx := context.Background()
	series := seedSeries(t, libStore, 1, "Show", [][2]any{{100, "high"}})
	setCreatedAt(t, sqlDB, "library_series", series.ID, ageDaysAgo(400))

	if _, err := sqlDB.Exec(`ALTER TABLE library_episodes RENAME TO library_episodes_hidden`); err != nil {
		t.Fatalf("hiding library_episodes: %v", err)
	}

	// No rules at all, and an age-only rule, both answer from the series row.
	for _, rules := range [][]pruning.Rule{
		nil,
		{{Name: "Stale shows", Mode: string(mode.Series), AgeDays: 365, Enabled: true}},
	} {
		if _, err := ScanLibrarySeries(ctx, libStore, []string{"junk"}, rules); err != nil {
			t.Fatalf("a scan needing no size/tier aggregation queried library_episodes: %v", err)
		}
	}

	// The control: a size rule DOES need the aggregate, so it must reach the
	// (now missing) table. Without this half, the assertions above would pass
	// just as well against a store that never queries episodes at all.
	if _, err := ScanLibrarySeries(ctx, libStore, nil, []pruning.Rule{{
		Name: "Big shows", Mode: string(mode.Series), SizeBytes: 1, Enabled: true,
	}}); err == nil {
		t.Fatal("expected a size rule to query library_episodes (the control half of this test is broken)")
	}
}
