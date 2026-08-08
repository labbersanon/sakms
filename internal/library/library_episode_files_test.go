package library

import (
	"context"
	"testing"
)

// episodeFileRow is the subset of a library_episode_files row these tests
// assert on. Queried directly rather than through ListEpisodeFiles so a bug in
// the lister can never mask a bug in the writer under test — TestListEpisodeFiles_PrimaryFirst
// is the one test that goes through the lister, on purpose.
type episodeFileRow struct {
	FilePath    string
	IsPrimary   bool
	QualityTier string
	Size        int64
	PHash       string
}

func episodeFileRows(t *testing.T, s *Store, episodeID int64) []episodeFileRow {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT file_path, is_primary, quality_tier, size, phash
		FROM library_episode_files
		WHERE episode_id = ?
		ORDER BY is_primary DESC, id ASC
	`, episodeID)
	if err != nil {
		t.Fatalf("querying episode files: %v", err)
	}
	defer rows.Close()
	out := []episodeFileRow{}
	for rows.Next() {
		var r episodeFileRow
		if err := rows.Scan(&r.FilePath, &r.IsPrimary, &r.QualityTier, &r.Size, &r.PHash); err != nil {
			t.Fatalf("scanning episode file: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating episode files: %v", err)
	}
	return out
}

func newTestSeries(t *testing.T, s *Store, tmdbID int) Series {
	t.Helper()
	series, err := s.UpsertSeries(context.Background(), Series{
		TMDBID: tmdbID, Title: "Some Show", Year: 2020, RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatalf("creating series: %v", err)
	}
	return series
}

// Satisfies plan §9.1's TestUpsertEpisode_SyncsPrimaryFileRow, first half
// ("creates exactly one primary row"); the re-point/demote half is
// TestUpsertEpisode_PathChangeFlipsPrimaryAndKeepsOrphan below.
func TestUpsertEpisode_SyncsPrimaryEpisodeFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 700)

	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot",
		FilePath:    "/tv/Some Show/Season 01/Some Show S01E01 Pilot.mkv",
		QualityTier: "high", Size: 4096, PHash: "abc123",
	})
	if err != nil {
		t.Fatalf("upserting episode: %v", err)
	}

	files := episodeFileRows(t, s, ep.ID)
	if len(files) != 1 {
		t.Fatalf("expected exactly one file row, got %d: %+v", len(files), files)
	}
	want := episodeFileRow{FilePath: ep.FilePath, IsPrimary: true, QualityTier: "high", Size: 4096, PHash: "abc123"}
	if files[0] != want {
		t.Fatalf("primary row mismatch:\n got %+v\nwant %+v", files[0], want)
	}
}

// A fileless catalog row ("TMDB knows about this episode, we don't have it")
// must never mint a file row — the ep.FilePath == "" guard is load-bearing, not
// defensive tidiness, since UpsertEpisode is legitimately called that way.
func TestUpsertEpisode_FilelessEpisodeMintsNoFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 701)

	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Not Aired Yet",
	})
	if err != nil {
		t.Fatalf("upserting episode: %v", err)
	}
	if files := episodeFileRows(t, s, ep.ID); len(files) != 0 {
		t.Fatalf("expected no file rows for a fileless episode, got %+v", files)
	}
}

// Satisfies plan §9.1's TestUpsertEpisode_SyncsPrimaryFileRow, second half
// ("a second UpsertEpisode for the same slot at a different path re-points the
// primary and demotes the old row").
//
// The Dedup shape: the same slot is re-upserted with the surviving winner's
// path. The new path must become primary and the old one must be demoted — the
// exact silent-corruption case the hook exists to prevent. The demoted row
// SURVIVES on purpose (plan §11.11 accepted orphan gap); asserting its survival
// is what stops a future session from "fixing" it with a DELETE.
func TestUpsertEpisode_PathChangeFlipsPrimaryAndKeepsOrphan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 702)

	const loser = "/tv/Some Show/Season 01/loser.mkv"
	const winner = "/tv/Some Show/Season 01/winner.mkv"

	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: loser, QualityTier: "low", Size: 100,
	}); err != nil {
		t.Fatalf("upserting first copy: %v", err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: winner, QualityTier: "high", Size: 900,
	})
	if err != nil {
		t.Fatalf("upserting winner: %v", err)
	}

	files := episodeFileRows(t, s, ep.ID)
	if len(files) != 2 {
		t.Fatalf("expected the demoted row to survive alongside the new primary, got %d: %+v", len(files), files)
	}
	if !files[0].IsPrimary || files[0].FilePath != winner {
		t.Fatalf("expected %q to be primary, got %+v", winner, files[0])
	}
	if files[1].IsPrimary || files[1].FilePath != loser {
		t.Fatalf("expected %q to survive as a non-primary orphan, got %+v", loser, files[1])
	}
}

// Re-syncing an unchanged episode must not flap or duplicate: the demote clause
// deliberately excludes the row it is about to write.
func TestUpsertEpisode_RepeatSyncIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 703)

	in := Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 5,
		FilePath: "/tv/Some Show/Season 02/ep.mkv", QualityTier: "high", Size: 10,
	}
	if _, err := s.UpsertEpisode(ctx, in); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	ep, err := s.UpsertEpisode(ctx, in)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	files := episodeFileRows(t, s, ep.ID)
	if len(files) != 1 || !files[0].IsPrimary {
		t.Fatalf("expected one primary row after a repeat sync, got %+v", files)
	}
}

// Satisfies plan §9.1's TestUpsertEpisodes_SyncsEveryBundledSlot.
//
// The batch sibling is what Rename Apply uses (rename.go:1906), including for a
// logical-episode-split file that backs several slots at one path.
func TestUpsertEpisodes_SyncsEveryPrimaryEpisodeFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 704)

	const rangeFile = "/tv/Some Show/Season 01/Some Show S01E01-E02.mkv"
	out, err := s.UpsertEpisodes(ctx, []Episode{
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: rangeFile, QualityTier: "high", Size: 50},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: rangeFile, QualityTier: "high", Size: 50},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 3},
	})
	if err != nil {
		t.Fatalf("upserting episodes: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 upserted rows, got %d", len(out))
	}
	for _, ep := range out[:2] {
		files := episodeFileRows(t, s, ep.ID)
		if len(files) != 1 || !files[0].IsPrimary || files[0].FilePath != rangeFile {
			t.Fatalf("episode %d: expected one primary row at %q, got %+v", ep.EpisodeNumber, rangeFile, files)
		}
	}
	if files := episodeFileRows(t, s, out[2].ID); len(files) != 0 {
		t.Fatalf("expected no file rows for the fileless episode in the batch, got %+v", files)
	}
}

// newFilelessEpisode mints an episode row with no file, so a test exercising
// UpsertEpisodeFile directly starts from an empty library_episode_files rather
// than from the primary row UpsertEpisode's sync hook would have minted.
func newFilelessEpisode(t *testing.T, s *Store, seriesID int64, season, episode int) Episode {
	t.Helper()
	ep, err := s.UpsertEpisode(context.Background(), Episode{
		SeriesID: seriesID, SeasonNumber: season, EpisodeNumber: episode,
	})
	if err != nil {
		t.Fatalf("creating fileless episode s%02de%02d: %v", season, episode, err)
	}
	return ep
}

// The same (episode_id, file_path) upserts once and then updates in place. The
// ID equality is the discriminating assertion: field equality alone would also
// pass for a delete-then-insert, which the ON CONFLICT clause is there to avoid.
func TestUpsertEpisodeFile_InsertThenUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 705)
	ep := newFilelessEpisode(t, s, series.ID, 1, 1)

	const path = "/tv/Some Show/Season 01/alt.mkv"
	first, err := s.UpsertEpisodeFile(ctx, EpisodeFile{
		EpisodeID: ep.ID, FilePath: path, QualityTier: "low", Size: 100, VideoCodec: "h264",
	})
	if err != nil {
		t.Fatalf("inserting: %v", err)
	}
	second, err := s.UpsertEpisodeFile(ctx, EpisodeFile{
		EpisodeID: ep.ID, FilePath: path, QualityTier: "high", Size: 900, VideoCodec: "hevc",
	})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same row updated in place, got id %d then %d", first.ID, second.ID)
	}
	files := episodeFileRows(t, s, ep.ID)
	if len(files) != 1 {
		t.Fatalf("expected exactly one row after the re-upsert, got %d: %+v", len(files), files)
	}
	want := episodeFileRow{FilePath: path, IsPrimary: false, QualityTier: "high", Size: 900}
	if files[0] != want {
		t.Fatalf("row mismatch:\n got %+v\nwant %+v", files[0], want)
	}
}

// A second IsPrimary write for one episode leaves exactly one primary and does
// not delete the incumbent. Deliberately NOT an idempotency/no-flap assertion:
// UpsertEpisodeFile's demote clause has no `file_path <> ?` conjunct (unlike
// SyncPrimaryEpisodeFile's), so re-promoting the same row demotes then
// re-promotes it — that difference is documented and intentional.
func TestUpsertEpisodeFile_PrimaryDemotesSibling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 706)
	ep := newFilelessEpisode(t, s, series.ID, 1, 1)

	const first = "/tv/Some Show/Season 01/first.mkv"
	const second = "/tv/Some Show/Season 01/second.mkv"
	if _, err := s.UpsertEpisodeFile(ctx, EpisodeFile{EpisodeID: ep.ID, FilePath: first, IsPrimary: true}); err != nil {
		t.Fatalf("inserting first primary: %v", err)
	}
	if _, err := s.UpsertEpisodeFile(ctx, EpisodeFile{EpisodeID: ep.ID, FilePath: second, IsPrimary: true}); err != nil {
		t.Fatalf("inserting second primary: %v", err)
	}

	files := episodeFileRows(t, s, ep.ID)
	if len(files) != 2 {
		t.Fatalf("expected the demoted sibling to survive, got %d rows: %+v", len(files), files)
	}
	primaries := 0
	for _, f := range files {
		if f.IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("expected exactly one primary, got %d: %+v", primaries, files)
	}
	if !files[0].IsPrimary || files[0].FilePath != second {
		t.Fatalf("expected %q to be the surviving primary, got %+v", second, files[0])
	}
	if files[1].IsPrimary || files[1].FilePath != first {
		t.Fatalf("expected %q to survive demoted, got %+v", first, files[1])
	}
}

// The range-file case: one path, two episode_ids, both rows insert. This is the
// test that fails loudly if UNIQUE(episode_id, file_path) is ever "simplified"
// into library_item_files' bare UNIQUE(file_path).
func TestUpsertEpisodeFile_SamePathDifferentEpisodes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 707)
	epA := newFilelessEpisode(t, s, series.ID, 1, 1)
	epB := newFilelessEpisode(t, s, series.ID, 1, 2)

	const rangeFile = "/tv/Some Show/Season 01/Some Show S01E01-E02.mkv"
	rowA, err := s.UpsertEpisodeFile(ctx, EpisodeFile{EpisodeID: epA.ID, FilePath: rangeFile, IsPrimary: true})
	if err != nil {
		t.Fatalf("inserting the E01 half: %v", err)
	}
	rowB, err := s.UpsertEpisodeFile(ctx, EpisodeFile{EpisodeID: epB.ID, FilePath: rangeFile, IsPrimary: true})
	if err != nil {
		t.Fatalf("inserting the E02 half: %v", err)
	}
	if rowA.ID == rowB.ID {
		t.Fatalf("expected two distinct rows for one path across two episodes, both got id %d", rowA.ID)
	}
	for _, ep := range []Episode{epA, epB} {
		files := episodeFileRows(t, s, ep.ID)
		if len(files) != 1 || !files[0].IsPrimary || files[0].FilePath != rangeFile {
			t.Fatalf("episode %d: expected one primary row at %q, got %+v", ep.EpisodeNumber, rangeFile, files)
		}
	}
}

// Ordering. The non-primary rows are seeded BEFORE the primary on purpose: with
// the primary inserted first, `ORDER BY is_primary DESC, id ASC` and a bare
// `ORDER BY id ASC` produce the same list and the test proves nothing.
func TestListEpisodeFiles_PrimaryFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 708)
	ep := newFilelessEpisode(t, s, series.ID, 1, 1)

	const altOne = "/tv/Some Show/Season 01/alt-1.mkv"
	const altTwo = "/tv/Some Show/Season 01/alt-2.mkv"
	const primary = "/tv/Some Show/Season 01/primary.mkv"
	for _, f := range []EpisodeFile{
		{EpisodeID: ep.ID, FilePath: altOne},
		{EpisodeID: ep.ID, FilePath: primary, IsPrimary: true},
		{EpisodeID: ep.ID, FilePath: altTwo},
	} {
		if _, err := s.UpsertEpisodeFile(ctx, f); err != nil {
			t.Fatalf("seeding %q: %v", f.FilePath, err)
		}
	}

	files, err := s.ListEpisodeFiles(ctx, ep.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, f.FilePath)
	}
	want := []string{primary, altOne, altTwo}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
	}
	if !files[0].IsPrimary {
		t.Fatalf("expected the first row to be the primary, got %+v", files[0])
	}
}

// A range file's repeated path appears exactly once (the DISTINCT is
// load-bearing: it is denormalized on two library_episodes rows AND on two
// library_episode_files rows), and an alternate-only path is included.
func TestAllEpisodeFilePaths_UnionAndDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 709)

	const rangeFile = "/tv/Some Show/Season 01/Some Show S01E01-E02.mkv"
	const altOnly = "/tv/Some Show/Season 01/Some Show S01E01 - 720p.mkv"
	out, err := s.UpsertEpisodes(ctx, []Episode{
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: rangeFile},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: rangeFile},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 3},
	})
	if err != nil {
		t.Fatalf("seeding episodes: %v", err)
	}
	if _, err := s.UpsertEpisodeFile(ctx, EpisodeFile{EpisodeID: out[0].ID, FilePath: altOnly}); err != nil {
		t.Fatalf("seeding the alternate: %v", err)
	}

	paths, err := s.AllEpisodeFilePaths(ctx)
	if err != nil {
		t.Fatalf("listing all paths: %v", err)
	}
	seen := map[string]int{}
	for _, p := range paths {
		seen[p]++
	}
	if len(paths) != 2 {
		t.Fatalf("expected exactly 2 distinct paths, got %d: %v", len(paths), paths)
	}
	if seen[rangeFile] != 1 {
		t.Fatalf("expected the range path exactly once, got %d: %v", seen[rangeFile], paths)
	}
	if seen[altOnly] != 1 {
		t.Fatalf("expected the alternate-only path to be included, got %v", paths)
	}
}

// UpdateEpisodePrimaryPath writes only path/tier/size on library_episodes. The
// intact title/air_date is the whole point: it guards against someone reaching
// for UpsertEpisode here, which would blank both when handed a partial Episode.
// It deliberately does NOT touch library_episode_files — the caller owns that.
func TestUpdateEpisodePrimaryPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 710)

	const oldPath = "/tv/Some Show/Season 01/old.mkv"
	const newPath = "/tv/Some Show/Season 01/new.mkv"
	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		Title: "Pilot", AirDate: "2020-01-02",
		FilePath: oldPath, QualityTier: "low", Size: 100,
	})
	if err != nil {
		t.Fatalf("seeding episode: %v", err)
	}

	if err := s.UpdateEpisodePrimaryPath(ctx, ep.ID, newPath, "high", 900); err != nil {
		t.Fatalf("updating primary path: %v", err)
	}

	got, err := s.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("re-reading episode: %v", err)
	}
	if got.FilePath != newPath || got.QualityTier != "high" || got.Size != 900 {
		t.Fatalf("expected path/tier/size updated, got %q/%q/%d", got.FilePath, got.QualityTier, got.Size)
	}
	if got.Title != "Pilot" || got.AirDate != "2020-01-02" {
		t.Fatalf("expected title/air_date left intact, got %q/%q", got.Title, got.AirDate)
	}
}

// The fileless-row guard on the OTHER writer. UpsertEpisodeCatalog writes an
// empty file_path with a raw INSERT that never reaches SyncPrimaryEpisodeFile
// at all, so this is a different code path from
// TestUpsertEpisode_FilelessEpisodeMintsNoFileRow above, not a duplicate of it.
func TestUpsertEpisodeCatalog_MintsNoFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series := newTestSeries(t, s, 711)

	if err := s.UpsertEpisodeCatalog(ctx, series.ID, 1, 4, "Not Aired Yet", "2099-01-01"); err != nil {
		t.Fatalf("upserting catalog row: %v", err)
	}
	ep, err := s.GetEpisode(ctx, series.ID, 1, 4)
	if err != nil {
		t.Fatalf("re-reading catalog row: %v", err)
	}
	if ep.FilePath != "" {
		t.Fatalf("precondition: expected a fileless catalog row, got file_path %q", ep.FilePath)
	}
	if files := episodeFileRows(t, s, ep.ID); len(files) != 0 {
		t.Fatalf("expected library_episode_files to stay empty for a catalog row, got %+v", files)
	}
}
