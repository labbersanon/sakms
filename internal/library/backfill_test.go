package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

// Every test here writes REAL files via t.TempDir() + os.WriteFile rather
// than mocking a filesystem: the size assertions are against actual bytes,
// which is the only way they prove os.Stat is really being consulted.

// writeVideo creates path (and its parent directories) with deterministic
// contents and returns the byte count os.Stat should report.
func writeVideo(t *testing.T, path string, size int) int64 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return int64(size)
}

// readSizeTier reads the raw stored columns, deliberately not going through
// Get/GetEpisode/GetScene: several assertions here turn on telling the
// empty-string sentinel apart from the literal "unknown", so the test reads
// exactly what is on disk.
func readSizeTier(t *testing.T, s *Store, table string, id int64) (int64, string) {
	t.Helper()
	var (
		size int64
		tier string
	)
	if err := s.db.QueryRow(`SELECT size, quality_tier FROM `+table+` WHERE id = ?`, id).Scan(&size, &tier); err != nil {
		t.Fatalf("reading %s row %d: %v", table, id, err)
	}
	return size, tier
}

func TestBackfillFilenameInference(t *testing.T) {
	// The real population for filename inference: a grab-imported file that
	// has not had a Rename Apply run against it yet, so rename.Relocate's
	// verbatim scene-release name (tokens intact) is still on disk.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	path := filepath.Join(root, "Some.Movie.2024.2160p.BluRay.REMUX-GRP.mkv")
	want := writeVideo(t, path, 4096)

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Some Movie", Year: 2024,
		FilePath: path, RootFolderPath: root,
	})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}

	summary, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if summary.Scanned != 1 || summary.SizedOK != 1 || summary.SizeFailed != 0 {
		t.Errorf("summary = %+v, want Scanned 1 / SizedOK 1 / SizeFailed 0", summary)
	}
	if summary.ByTier["lossless"] != 1 {
		t.Errorf("ByTier = %v, want one lossless", summary.ByTier)
	}

	size, tier := readSizeTier(t, s, "library_items", item.ID)
	if size != want {
		t.Errorf("size = %d, want the file's real %d bytes", size, want)
	}
	if tier != "lossless" {
		t.Errorf("quality_tier = %q, want %q (a REMUX release)", tier, "lossless")
	}
}

func TestBackfillFilenameInferenceFromParentDir(t *testing.T) {
	// Two things under test at once, both named so neither is implicit:
	//
	//  1. The parse is over the path RELATIVE TO THE ROOT FOLDER, not the
	//     basename. "Show.S01E01.mkv" alone carries no source token — every
	//     quality token lives on the season-pack PARENT DIRECTORY. If this
	//     only parsed the basename the result would be "unknown".
	//     The root folder is deliberately named "remux-archive": if the root
	//     prefix were NOT trimmed first, release.Parse would match "remux"
	//     (checked before every other source pattern) and infer "lossless",
	//     so this test fails loudly rather than passing by accident.
	//  2. The library_episodes -> library_series JOIN. Episodes have no
	//     root_folder_path column of their own; it lives on the parent series
	//     row, which is why a library_series parent is explicitly seeded here.
	s := newTestStore(t)
	ctx := context.Background()

	root := filepath.Join(t.TempDir(), "remux-archive")
	path := filepath.Join(root, "Show.S01.1080p.WEB-DL-GRP", "Show.S01E01.mkv")
	want := writeVideo(t, path, 2048)

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 55, Title: "Show", Year: 2021, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: path,
	})
	if err != nil {
		t.Fatalf("seeding episode: %v", err)
	}

	if _, err := s.BackfillSizeAndTier(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	size, tier := readSizeTier(t, s, "library_episodes", ep.ID)
	if size != want {
		t.Errorf("size = %d, want the file's real %d bytes", size, want)
	}
	if tier != "medium" {
		t.Errorf("quality_tier = %q, want %q — %q means the root folder was not trimmed, %q means only the basename was parsed",
			tier, "medium", "lossless", TierUnknown)
	}
}

func TestBackfillUnknownForRenamedFile(t *testing.T) {
	// The honest-expectations test. naming.MovieFileName strips every quality
	// token, so a file SAK has already Renamed genuinely has no tier signal
	// left on disk — the app working correctly, not a parse failure. The
	// backfill must record the LITERAL "unknown", never "" (which means
	// "never processed" and would make the run non-idempotent).
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	path := filepath.Join(root, "The Movie (2024) [tmdbid-99]", "The Movie (2024) [tmdbid-99].mkv")
	want := writeVideo(t, path, 1024)

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 99, Title: "The Movie", Year: 2024,
		FilePath: path, RootFolderPath: root,
	})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}

	summary, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if summary.ByTier[TierUnknown] != 1 {
		t.Errorf("ByTier = %v, want one %q", summary.ByTier, TierUnknown)
	}

	size, tier := readSizeTier(t, s, "library_items", item.ID)
	if size != want {
		t.Errorf("size = %d, want the file's real %d bytes (size is still captured for an unknown-tier row)", size, want)
	}
	if tier != TierUnknown {
		t.Errorf("quality_tier = %q, want exactly %q — an empty string would mean 'never processed' and would be re-scanned forever", tier, TierUnknown)
	}
}

func TestBackfillGrabRecordDoesNotResolveTier(t *testing.T) {
	// A grabs row matching this item's (tmdb_id, title) contributes NOTHING
	// to the tier. This is deliberate and permanent, not an unimplemented
	// step: grabs.quality_profile_id is dead — its only write comes from a
	// request field zero frontend callers ever send, so it is 0 on every row
	// in every install, and even a nonzero value would be a Servarr profile
	// id with no defined mapping onto a quality.Tier. grabs.title is the
	// MEDIA title, not the release title, so it carries no tokens to parse
	// either. The spec's original grab-record-match step therefore had
	// nothing to read, and the chain is 2 steps (path inference -> unknown),
	// not 3.
	//
	// If a future session re-adds a grab-record lookup thinking this was an
	// oversight, this test is what should stop them.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	path := filepath.Join(root, "The Movie (2024) [tmdbid-99]", "The Movie (2024) [tmdbid-99].mkv")
	writeVideo(t, path, 512)

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 99, Title: "The Movie", Year: 2024,
		FilePath: path, RootFolderPath: root,
	})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO grabs (mode, title, tmdb_id, quality_profile_id, indexer, protocol, download_client, root_folder_path)
		VALUES ('movies', 'The Movie', 99, 0, 'someindexer', 'torrent', 'qbittorrent', ?)
	`, root); err != nil {
		t.Fatalf("seeding grab: %v", err)
	}

	if _, err := s.BackfillSizeAndTier(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if _, tier := readSizeTier(t, s, "library_items", item.ID); tier != TierUnknown {
		t.Errorf("quality_tier = %q, want %q — a grabs row must contribute nothing to the inferred tier", tier, TierUnknown)
	}
}

func TestBackfillSkipsMissingEpisodeRows(t *testing.T) {
	// library_episodes deliberately holds rows for episodes TMDB knows about
	// that are not on disk (file_path == ""), which is what makes "missing
	// episodes" a plain query. Those rows must be left completely untouched
	// and must not inflate Scanned — otherwise the Dashboard's Series row
	// would report hundreds of phantom 0-byte Unknown items.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	onDisk := filepath.Join(root, "Show.S01.1080p.WEB-DL-GRP", "Show.S01E01.mkv")
	writeVideo(t, onDisk, 128)

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 7, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	present, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: onDisk})
	if err != nil {
		t.Fatalf("seeding present episode: %v", err)
	}
	missing, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Not grabbed yet"})
	if err != nil {
		t.Fatalf("seeding missing episode: %v", err)
	}

	summary, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if summary.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1 — the file_path='' episode must not be counted", summary.Scanned)
	}

	if size, tier := readSizeTier(t, s, "library_episodes", missing.ID); size != 0 || tier != "" {
		t.Errorf("missing episode row = (size %d, tier %q), want (0, \"\") — it must be left completely untouched", size, tier)
	}
	if size, tier := readSizeTier(t, s, "library_episodes", present.ID); size == 0 || tier == "" {
		t.Errorf("on-disk episode row = (size %d, tier %q), want it processed", size, tier)
	}
}

func TestBackfillIdempotent(t *testing.T) {
	// Safe to call on every boot forever: once a row is captured it is not
	// re-read, so a second run is a cheap no-op.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	pathA := filepath.Join(root, "A.Movie.2020.1080p.WEB-DL-GRP.mkv")
	pathB := filepath.Join(root, "B.Movie.2021.2160p.BluRay.REMUX-GRP.mkv")
	wantA := writeVideo(t, pathA, 700)
	wantB := writeVideo(t, pathB, 900)

	a, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 1, Title: "A Movie", FilePath: pathA, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding A: %v", err)
	}
	b, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 2, Title: "B Movie", FilePath: pathB, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding B: %v", err)
	}

	first, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if first.Scanned != 2 {
		t.Fatalf("first run Scanned = %d, want 2", first.Scanned)
	}

	var updatedAtA, updatedAtB string
	if err := s.db.QueryRow(`SELECT updated_at FROM library_items WHERE id = ?`, a.ID).Scan(&updatedAtA); err != nil {
		t.Fatalf("reading A updated_at: %v", err)
	}
	if err := s.db.QueryRow(`SELECT updated_at FROM library_items WHERE id = ?`, b.ID).Scan(&updatedAtB); err != nil {
		t.Fatalf("reading B updated_at: %v", err)
	}

	second, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.Scanned != 0 {
		t.Errorf("second run Scanned = %d, want 0 — captured rows must not be re-read", second.Scanned)
	}
	if size, tier := readSizeTier(t, s, "library_items", a.ID); size != wantA || tier != "medium" {
		t.Errorf("A after second run = (%d, %q), want (%d, %q) — unchanged", size, tier, wantA, "medium")
	}
	if size, tier := readSizeTier(t, s, "library_items", b.ID); size != wantB || tier != "lossless" {
		t.Errorf("B after second run = (%d, %q), want (%d, %q) — unchanged", size, tier, wantB, "lossless")
	}

	// Reset A to the UNCAPTURED state. "size = 0" alone is not that state:
	// the read predicate and the UPDATE guard are the same condition
	// (size = 0 AND quality_tier = ''), so a refresh means clearing both.
	// This is also the only supported re-run mechanism — a deliberate DB
	// edit, not a UI action.
	if _, err := s.db.ExecContext(ctx, `UPDATE library_items SET size = 0, quality_tier = '' WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("resetting A: %v", err)
	}

	third, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("third backfill: %v", err)
	}
	if third.Scanned != 1 {
		t.Errorf("third run Scanned = %d, want 1 — only the reset row reprocesses", third.Scanned)
	}
	if size, tier := readSizeTier(t, s, "library_items", a.ID); size != wantA || tier != "medium" {
		t.Errorf("A after reprocessing = (%d, %q), want (%d, %q)", size, tier, wantA, "medium")
	}
	var afterB string
	if err := s.db.QueryRow(`SELECT updated_at FROM library_items WHERE id = ?`, b.ID).Scan(&afterB); err != nil {
		t.Fatalf("re-reading B updated_at: %v", err)
	}
	if afterB != updatedAtB {
		t.Errorf("B's updated_at changed (%q -> %q) — the untouched row must not be rewritten", updatedAtB, afterB)
	}
}

func TestBackfillTolerantOfMissingFile(t *testing.T) {
	// A dead path (a dropped CIFS/NFS mount is the common real cause) is not
	// a fatal error: size stays at its 0 sentinel, the tier is still recorded
	// from the path, the function returns nil, and every other row in the
	// batch still processes.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	ghost := filepath.Join(root, "Ghost.2019.1080p.BluRay.x265-GRP.mkv") // deliberately never created
	realPath := filepath.Join(root, "Real.2020.1080p.WEB-DL-GRP.mkv")
	wantReal := writeVideo(t, realPath, 333)

	ghostItem, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 10, Title: "Ghost", FilePath: ghost, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding ghost: %v", err)
	}
	realItem, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 11, Title: "Real", FilePath: realPath, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding real: %v", err)
	}

	summary, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("backfill must not fail on an unstattable file, got: %v", err)
	}
	if summary.Scanned != 2 || summary.SizedOK != 1 || summary.SizeFailed != 1 {
		t.Errorf("summary = %+v, want Scanned 2 / SizedOK 1 / SizeFailed 1", summary)
	}

	if size, tier := readSizeTier(t, s, "library_items", ghostItem.ID); size != 0 || tier != "lossless" {
		t.Errorf("ghost row = (size %d, tier %q), want (0, %q) — the tier is still inferable from the path", size, tier, "lossless")
	}
	if size, tier := readSizeTier(t, s, "library_items", realItem.ID); size != wantReal || tier != "medium" {
		t.Errorf("real row = (size %d, tier %q), want (%d, %q) — one bad row must not abort the batch", size, tier, wantReal, "medium")
	}
}

func TestBackfillDoesNotClobberConcurrentWrite(t *testing.T) {
	// The read-then-write is not atomic. A live Apply/Dedup capture-at-write
	// can land on a row after the backfill has read it but before it writes,
	// and when it does the backfill's inferred value is the STALE one — the
	// live write knows the operator's actual configured tier and the real
	// current file. The per-row guard (WHERE id = ? AND size = 0 AND
	// quality_tier = '') is what makes that safe.
	//
	// Driven deterministically rather than with real goroutines: the test
	// takes the same two steps BackfillSizeAndTier takes internally, in the
	// same order, landing the concurrent write between them. It calls the
	// production read and the production guarded write, so the actual SQL is
	// under test rather than a copy of it.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	path := filepath.Join(root, "Some.Movie.2024.1080p.WEBRip.x265-GRP.mkv")
	writeVideo(t, path, 600)

	item, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: path, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}

	// Step 1: the backfill reads the row while it is still uncaptured.
	pending, err := s.pendingSizeTierRows(ctx)
	if err != nil {
		t.Fatalf("reading pending rows: %v", err)
	}
	if len(pending) != 1 || pending[0].id != item.ID {
		t.Fatalf("expected exactly the seeded row to be pending, got %+v", pending)
	}

	// Step 2: a live capture-at-write lands first, with real values.
	const (
		liveSize = int64(999999)
		liveTier = "lossless"
	)
	if _, err := s.db.ExecContext(ctx, `UPDATE library_items SET size = ?, quality_tier = ? WHERE id = ?`,
		liveSize, liveTier, item.ID); err != nil {
		t.Fatalf("simulating concurrent capture-at-write: %v", err)
	}

	// Step 3: the backfill's own (now stale) write fires. It must be a no-op,
	// and must not report an error — zero rows affected is the correct
	// outcome here, not a failure.
	if err := s.writeBackfilledSizeAndTier(ctx, pending[0].table, pending[0].id, 600, "low"); err != nil {
		t.Fatalf("guarded write returned an error: %v", err)
	}

	size, tier := readSizeTier(t, s, "library_items", item.ID)
	if size != liveSize || tier != liveTier {
		t.Errorf("row = (size %d, tier %q), want the fresher live values (%d, %q) — the backfill clobbered a concurrent capture-at-write",
			size, tier, liveSize, liveTier)
	}

	// And a full run afterwards leaves it alone too: the read predicate is
	// the same condition as the guard, so the row is no longer pending.
	summary, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if summary.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 — a captured row must not be re-read", summary.Scanned)
	}
	if size, tier := readSizeTier(t, s, "library_items", item.ID); size != liveSize || tier != liveTier {
		t.Errorf("row = (size %d, tier %q) after a full run, want the live values (%d, %q) preserved", size, tier, liveSize, liveTier)
	}
}

func TestBackfillCoversAllThreeTrackedTables(t *testing.T) {
	// library_scenes is the third tracked table and is easy to forget: Adult
	// has its own table rather than sharing library_items. One seeded row of
	// each proves all three branches of the read run.
	s := newTestStore(t)
	ctx := context.Background()

	root := t.TempDir()
	moviePath := filepath.Join(root, "A.Movie.2020.1080p.WEB-DL-GRP.mkv")
	epPath := filepath.Join(root, "Show.S01.2160p.BluRay.REMUX-GRP", "Show.S01E01.mkv")
	scenePath := filepath.Join(root, "Studio.Scene.2022.1080p.WEBRip.x265-GRP.mp4")
	writeVideo(t, moviePath, 11)
	writeVideo(t, epPath, 22)
	writeVideo(t, scenePath, 33)

	item, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 1, Title: "A Movie", FilePath: moviePath, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}
	series, err := s.UpsertSeries(ctx, Series{TMDBID: 2, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: epPath})
	if err != nil {
		t.Fatalf("seeding episode: %v", err)
	}
	scene, err := s.UpsertScene(ctx, Scene{Box: "stashdb", SceneID: "abc", Title: "Scene", FilePath: scenePath, RootFolderPath: root})
	if err != nil {
		t.Fatalf("seeding scene: %v", err)
	}

	summary, err := s.BackfillSizeAndTier(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if summary.Scanned != 3 || summary.SizedOK != 3 {
		t.Errorf("summary = %+v, want Scanned 3 / SizedOK 3", summary)
	}

	for _, tc := range []struct {
		table    string
		id       int64
		wantSize int64
		wantTier string
	}{
		{"library_items", item.ID, 11, "medium"},
		{"library_episodes", ep.ID, 22, "lossless"},
		{"library_scenes", scene.ID, 33, "low"},
	} {
		if size, tier := readSizeTier(t, s, tc.table, tc.id); size != tc.wantSize || tier != tc.wantTier {
			t.Errorf("%s = (size %d, tier %q), want (%d, %q)", tc.table, size, tier, tc.wantSize, tc.wantTier)
		}
	}
}
