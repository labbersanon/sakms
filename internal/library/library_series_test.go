package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpsertSeries_CreatesThenUpdatesInPlace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.UpsertSeries(ctx, Series{
		TMDBID: 100, TVDBID: 900, Title: "Some Show", Year: 2020, RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID == 0 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("expected id/timestamps populated, got %+v", created)
	}

	updated, err := s.UpsertSeries(ctx, Series{
		TMDBID: 100, TVDBID: 900, Title: "Some Show (Updated)", Year: 2020, RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.ID != created.ID {
		t.Errorf("expected the same row to be updated (id %d), got id %d", created.ID, updated.ID)
	}
	if updated.Title != "Some Show (Updated)" {
		t.Errorf("expected title to be updated, got %q", updated.Title)
	}

	all, err := s.ListSeries(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected upsert to replace, not duplicate — got %d rows", len(all))
	}
}

func TestGetSeriesByTMDBID_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetSeriesByTMDBID(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpsertEpisode_TracksMissingAndFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 200, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// TMDB knows about episode 1 and 2; only episode 1 is actually on disk.
	if _, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: "/tv/Show/Season 01/Show - S01E01 - Pilot.mkv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Second"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, err := s.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 episodes, got %d", len(all))
	}

	missing, err := s.MissingEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(missing) != 1 || missing[0].EpisodeNumber != 2 {
		t.Fatalf("expected only episode 2 missing, got %+v", missing)
	}

	// Re-upserting episode 2 with a file path now marks it found, not missing.
	if _, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Second", FilePath: "/tv/Show/Season 01/Show - S01E02 - Second.mkv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	missing, err = s.MissingEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no missing episodes once found, got %+v", missing)
	}
}

// TestUpsertEpisodes_AtomicBatch proves the logical-episode-splitting batch
// write: multiple Episode rows (e.g. a "S01E01-E02" file's primary + extra
// bundled number) upsert together, all pointing at the same shared file
// path, and a re-upsert of the same batch updates in place rather than
// duplicating — the same idempotent shape UpsertEpisode already has, just
// batched.
func TestUpsertEpisodes_AtomicBatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 400, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sharedPath := "/tv/Show/Season 01/Show S01E01-E02.mkv"
	upserted, err := s.UpsertEpisodes(ctx, []Episode{
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Part One", FilePath: sharedPath},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part Two", FilePath: sharedPath},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(upserted) != 2 || upserted[0].ID == 0 || upserted[1].ID == 0 {
		t.Fatalf("expected 2 upserted rows with nonzero ids, got %+v", upserted)
	}

	all, err := s.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 || all[0].FilePath != sharedPath || all[1].FilePath != sharedPath {
		t.Fatalf("expected 2 episodes sharing one file path, got %+v", all)
	}

	// Re-upserting the same batch updates in place — no duplicate rows.
	if _, err := s.UpsertEpisodes(ctx, []Episode{
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Part One", FilePath: sharedPath},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part Two", FilePath: sharedPath},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	all, err = s.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected still exactly 2 episodes after re-upserting the same batch, got %d", len(all))
	}
}

func TestDeleteSeries_RemovesEpisodesAndTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 300, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/tv/x.mkv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSeriesTag(ctx, series.ID, "kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.DeleteSeries(ctx, series.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.GetSeriesByTMDBID(ctx, 300); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected series to be gone, got %v", err)
	}
	eps, err := s.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eps) != 0 {
		t.Errorf("expected episodes to be deleted with the series, got %v", eps)
	}
	tags, err := s.SeriesTags(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected tags to be deleted with the series, got %v", tags)
	}
}

func TestSeriesTags_AddIsIdempotentAndRemoveWorks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 400, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AddSeriesTag(ctx, series.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSeriesTag(ctx, series.ID, "favorite"); err != nil {
		t.Fatalf("adding the same tag twice should be a no-op, got error: %v", err)
	}

	tags, err := s.SeriesTags(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 1 || tags[0] != "favorite" {
		t.Fatalf("expected exactly one tag, got %v", tags)
	}

	if err := s.RemoveSeriesTag(ctx, series.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.RemoveSeriesTag(ctx, series.ID, "not-there"); err != nil {
		t.Fatalf("removing a tag that isn't assigned should be a no-op, got error: %v", err)
	}

	tags, err = s.SeriesTags(ctx, series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags after removal, got %v", tags)
	}
}

func TestSeriesTagVocabulary_DistinctAcrossSeries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.UpsertSeries(ctx, Series{TMDBID: 500, Title: "A", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := s.UpsertSeries(ctx, Series{TMDBID: 501, Title: "B", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AddSeriesTag(ctx, a.ID, "kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSeriesTag(ctx, b.ID, "kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddSeriesTag(ctx, b.ID, "documentary"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vocab, err := s.SeriesTagVocabulary(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vocab) != 2 || vocab[0] != "documentary" || vocab[1] != "kids" {
		t.Fatalf("expected [documentary kids], got %v", vocab)
	}
}

func TestParseEpisodeFilename(t *testing.T) {
	cases := []struct {
		name        string
		wantSeason  int
		wantEpisode int
		wantOK      bool
	}{
		{"Show.Name.S03E05.1080p.mkv", 3, 5, true},
		{"Show Name - 3x05 - Episode Title.mkv", 3, 5, true},
		{"s1e2.mkv", 1, 2, true},
		{"Show Name Complete Season.mkv", 0, 0, false},
		// Real diagnosis data: single-episode projection of "The Path"'s
		// parent-directory shape (the strict single-episode parser already
		// handles this fine on its own — the hash basename inside it does not).
		{"The.Path.S01E02.2160p.WEB.h265-NiXON", 1, 2, true},
	}
	for _, c := range cases {
		season, episode, ok := ParseEpisodeFilename(c.name)
		if ok != c.wantOK || season != c.wantSeason || episode != c.wantEpisode {
			t.Errorf("ParseEpisodeFilename(%q) = (%d, %d, %v), want (%d, %d, %v)",
				c.name, season, episode, ok, c.wantSeason, c.wantEpisode, c.wantOK)
		}
	}
}

func TestParseEpisodeNumbers(t *testing.T) {
	cases := []struct {
		name       string
		wantSeason int
		wantEps    []int
		wantOK     bool
	}{
		// Single-episode — must match ParseEpisodeFilename's existing behavior.
		{"Show.Name.S03E05.1080p.mkv", 3, []int{5}, true},
		{"Show Name - 3x05 - Episode Title.mkv", 3, []int{5}, true},
		{"s1e2.mkv", 1, []int{2}, true},
		{"Show Name Complete Season.mkv", 0, nil, false},
		// Concatenated multi-episode.
		{"Show.Name.S01E01E02E03.mkv", 1, []int{1, 2, 3}, true},
		{"Show.Name.S01E01E02.mkv", 1, []int{1, 2}, true},
		// Dash range (SxxExx form and alt NxNN form), both "-Eyy" and "-yy".
		{"Show.Name.S01E01-E02.mkv", 1, []int{1, 2}, true},
		{"Show.Name.S01E01-02.mkv", 1, []int{1, 2}, true},
		{"Show Name - 01x01-02.mkv", 1, []int{1, 2}, true},
		// Pathological range span is rejected (falls back to single episode).
		{"Show.Name.S01E01-E99.mkv", 1, []int{1}, true},
		// Real diagnosis data (.omc/artifacts/series-parse-failures-20260806.psv):
		// "The Path"'s parent-directory name is a complete scene-release name
		// the strict parser already handles fine on its own — only the
		// opaque hash basename inside it fails (see negative cases below and
		// TestParseEpisodeNumbersLoose).
		{"The.Path.S01E02.2160p.WEB.h265-NiXON", 1, []int{2}, true},
		{"the.path.s03e10.2160p.web.h265-nixon-3b6539", 3, []int{10}, true},
		// Negative — real filenames that must NOT parse (precision over recall).
		{"2ea4ad06efe20501d944b90f3a291e6f.mp4", 0, nil, false},                          // "The Path" opaque hash basename
		{"Laurel & Hardy - Below Zero(Colour)-DVDRip.XviD-DIE-DVD11.mp4", 0, nil, false}, // bare title, no marker at all
		{"RED SKELTON e1524.mp4", 0, nil, false},                                         // ambiguous digit-code, deliberately not parsed
		{"Movie Name (2020).mkv", 0, nil, false},                                         // movie name, bare year only
	}
	for _, c := range cases {
		season, episodes, ok := ParseEpisodeNumbers(c.name)
		if ok != c.wantOK || season != c.wantSeason || !intSlicesEqual(episodes, c.wantEps) {
			t.Errorf("ParseEpisodeNumbers(%q) = (%d, %v, %v), want (%d, %v, %v)",
				c.name, season, episodes, ok, c.wantSeason, c.wantEps, c.wantOK)
		}
	}
}

// TestParseEpisodeNumbersLoose covers the rename-path-only fallback (§2.2 of
// .omc/plans/autopilot-impl.md): "The Path"'s real shape is an opaque hash
// basename inside a parent directory that is itself a complete scene-release
// name.
func TestParseEpisodeNumbersLoose(t *testing.T) {
	cases := []struct {
		name       string
		basename   string
		parentDir  string
		wantSeason int
		wantEps    []int
		wantOK     bool
	}{
		{
			name:       "basename parses on its own — parent dir never consulted",
			basename:   "Show.Name.S03E05.1080p.mkv",
			parentDir:  "/media/Series/Show Name/Season 3",
			wantSeason: 3, wantEps: []int{5}, wantOK: true,
		},
		{
			name:       "real The Path shape — hash basename, parent dir carries the marker",
			basename:   "2ea4ad06efe20501d944b90f3a291e6f.mp4",
			parentDir:  "/media/Media Library/Series/The Path/Season 1/The.Path.S01E02.2160p.WEB.h265-NiXON",
			wantSeason: 1, wantEps: []int{2}, wantOK: true,
		},
		{
			name:       "real The Path shape, lowercase + trailing hash suffix",
			basename:   "6f98fdf03dee126d9fbb2bd08a660104.mp4",
			parentDir:  "/media/Media Library/Series/The Path/Season 3/the.path.s03e10.2160p.web.h265-nixon-3b6539",
			wantSeason: 3, wantEps: []int{10}, wantOK: true,
		},
		{
			name:       "neither basename nor parent dir has a marker — stays unparsed",
			basename:   "Laurel & Hardy - Below Zero(Colour)-DVDRip.XviD-DIE-DVD11.mp4",
			parentDir:  "/media/Media Library/Series/Laurel and Hardy",
			wantSeason: 0, wantEps: nil, wantOK: false,
		},
		{
			// Only the IMMEDIATE parent directory is consulted, never the
			// grandparent or any ancestor beyond it — the grandparent here
			// carries the marker but the immediate parent ("subs") does not,
			// so this must stay unparsed. Distinguishes "checks one level"
			// from "walks to root", which the case above alone does not.
			name:       "grandparent carries the marker, immediate parent does not — must NOT walk up",
			basename:   "2ea4ad06efe20501d944b90f3a291e6f.mp4",
			parentDir:  "/media/Media Library/Series/The Path/Season 1/The.Path.S01E02.2160p.WEB.h265-NiXON/subs",
			wantSeason: 0, wantEps: nil, wantOK: false,
		},
	}
	for _, c := range cases {
		season, episodes, ok := ParseEpisodeNumbersLoose(c.basename, c.parentDir)
		if ok != c.wantOK || season != c.wantSeason || !intSlicesEqual(episodes, c.wantEps) {
			t.Errorf("%s: ParseEpisodeNumbersLoose(%q, %q) = (%d, %v, %v), want (%d, %v, %v)",
				c.name, c.basename, c.parentDir, season, episodes, ok, c.wantSeason, c.wantEps, c.wantOK)
		}
	}

	// Containment proof: ParseEpisodeNumbers itself must still return ok=false
	// for the hash basename that only the loose wrapper can resolve — proving
	// the strict parser (and therefore its other three non-rename call sites)
	// is completely untouched by this fallback's existence.
	if _, _, ok := ParseEpisodeNumbers("2ea4ad06efe20501d944b90f3a291e6f.mp4"); ok {
		t.Error("ParseEpisodeNumbers must still return ok=false for the opaque hash basename — the loose fallback must not have widened the strict parser")
	}
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCountEpisodesByFilePath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 500, Title: "Shared File Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No episode references this path yet.
	count, err := s.CountEpisodesByFilePath(ctx, "/tv/Show/Season 01/Show S01E01-E02.mkv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}

	sharedPath := "/tv/Show/Season 01/Show S01E01-E02.mkv"
	if _, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: sharedPath}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count, err = s.CountEpisodesByFilePath(ctx, sharedPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}

	if _, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: sharedPath}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count, err = s.CountEpisodesByFilePath(ctx, sharedPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 once a second episode shares the same file path, got %d", count)
	}
}

func TestStripEpisodeMarker(t *testing.T) {
	got := StripEpisodeMarker("Show.Name.S03E05.1080p.WEB-DL")
	if got != "Show.Name" {
		t.Errorf("expected %q, got %q", "Show.Name", got)
	}
	got = StripEpisodeMarker("No Marker Here")
	if got != "No Marker Here" {
		t.Errorf("expected the name unchanged when no marker is present, got %q", got)
	}
	// Real diagnosis data: "The Path"'s parent-directory shape strips down to
	// the show title just like any other scene-release name — this is what
	// §2.3's lockstep requirement guards (StripEpisodeMarkerLoose falling
	// back to this same strict function on the parent dir).
	got = StripEpisodeMarker("The.Path.S01E02.2160p.WEB.h265-NiXON")
	if got != "The.Path" {
		t.Errorf("expected %q, got %q", "The.Path", got)
	}
}

// TestStripEpisodeMarkerLoose covers the §2.3 lockstep requirement: the
// search-term extraction at internal/rename/rename.go:818 must fall back to
// the parent directory the same way ParseEpisodeNumbersLoose does, or a
// hash-basename file that now parses season/episode correctly still can't
// find its TMDB series — StripEpisodeMarker(hash) is a no-op, so the search
// term would be the opaque hash itself.
func TestStripEpisodeMarkerLoose(t *testing.T) {
	cases := []struct {
		name      string
		basename  string
		parentDir string
		want      string
	}{
		{
			name:      "basename has the marker — parent dir never consulted",
			basename:  "Show.Name.S03E05.1080p.mkv",
			parentDir: "/media/Series/Show Name/Season 3",
			want:      "Show.Name",
		},
		{
			name:      "real The Path shape — hash basename has nothing to strip, falls back to parent dir",
			basename:  "2ea4ad06efe20501d944b90f3a291e6f.mp4",
			parentDir: "/media/Media Library/Series/The Path/Season 1/The.Path.S01E02.2160p.WEB.h265-NiXON",
			want:      "The.Path",
		},
		{
			name:      "neither has a marker — basename returned unchanged",
			basename:  "No Marker Here",
			parentDir: "/media/Series/Also No Marker",
			want:      "No Marker Here",
		},
		{
			// Same "checks one level only" property as
			// TestParseEpisodeNumbersLoose's grandparent case: the immediate
			// parent ("subs") has nothing to strip even though its own
			// parent carries the marker, so the basename (also unstrippable)
			// is returned unchanged.
			name:      "grandparent carries the marker, immediate parent does not — must NOT walk up",
			basename:  "2ea4ad06efe20501d944b90f3a291e6f.mp4",
			parentDir: "/media/Media Library/Series/The Path/Season 1/The.Path.S01E02.2160p.WEB.h265-NiXON/subs",
			want:      "2ea4ad06efe20501d944b90f3a291e6f.mp4",
		},
	}
	for _, c := range cases {
		got := StripEpisodeMarkerLoose(c.basename, c.parentDir)
		if got != c.want {
			t.Errorf("%s: StripEpisodeMarkerLoose(%q, %q) = %q, want %q", c.name, c.basename, c.parentDir, got, c.want)
		}
	}

	// Containment proof: StripEpisodeMarker itself must still be a no-op on
	// the opaque hash basename — proving the strict function is untouched.
	hash := "2ea4ad06efe20501d944b90f3a291e6f.mp4"
	if got := StripEpisodeMarker(hash); got != hash {
		t.Errorf("StripEpisodeMarker must still no-op on the opaque hash basename, got %q", got)
	}
}

func TestResolveEpisodeVideoFiles_SingleFileAndSeasonPack(t *testing.T) {
	dir := t.TempDir()
	singleFile := filepath.Join(dir, "episode.mkv")
	if err := os.WriteFile(singleFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	got, err := ResolveEpisodeVideoFiles(singleFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != singleFile {
		t.Fatalf("expected just the single file, got %v", got)
	}

	packDir := filepath.Join(dir, "Season Pack")
	if err := os.Mkdir(packDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"S01E01.mkv", "S01E02.mkv", "poster.jpg"} {
		if err := os.WriteFile(filepath.Join(packDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	got, err = ResolveEpisodeVideoFiles(packDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both episode files (not the sidecar), got %v", got)
	}

	nonVideo := filepath.Join(dir, ".plexmatch")
	if err := os.WriteFile(nonVideo, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing plexmatch: %v", err)
	}
	if _, err := ResolveEpisodeVideoFiles(nonVideo); err == nil {
		t.Fatal("expected error for non-video loose file")
	}
}

func TestUpsertEpisode_RoundTripsPHashIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 700, Title: "Cached Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/tv/Cached Show/S01E01.mkv",
		PHash: "pdq256/5f:deadbeef", PHashFileSize: 12345, PHashFileMTime: "2026-07-10T00:00:00Z",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "pdq256/5f:deadbeef" || got.PHashFileSize != 12345 || got.PHashFileMTime != "2026-07-10T00:00:00Z" {
		t.Errorf("expected phash identity to round-trip, got %+v", got)
	}
}

func TestUpdateEpisodePHash_UpdatesInPlaceAndNoOpOnMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 701, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: "/tv/Show/S01E01.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.PHash != "" {
		t.Fatalf("expected an uncached episode to start with an empty phash, got %q", ep.PHash)
	}

	if err := s.UpdateEpisodePHash(ctx, ep.ID, "pdq256/5f:cafe", 999, "2026-07-10T12:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "pdq256/5f:cafe" || got.PHashFileSize != 999 || got.PHashFileMTime != "2026-07-10T12:00:00Z" {
		t.Errorf("expected UpdateEpisodePHash to persist the new hash + identity, got %+v", got)
	}
	// The targeted write must leave the rest of the row intact.
	if got.Title != "Pilot" || got.FilePath != "/tv/Show/S01E01.mkv" {
		t.Errorf("expected UpdateEpisodePHash to leave other columns untouched, got %+v", got)
	}

	if err := s.UpdateEpisodePHash(ctx, 999999, "x", 1, "y"); err != nil {
		t.Errorf("expected updating a nonexistent id to be a no-op, got %v", err)
	}
}

// TestEpisodeTiersBySeries_DedupesSharedFilePathWithDivergentRows pins the
// agreement between this function and storageAllocationQuery's series
// subquery — see TestStorageAllocationDeduplicatesMultiEpisodeFilesWithDivergentRows
// for the same state seeded against the aggregation side. Two episode rows can
// legitimately share one physical file with divergent tiers (UpsertEpisode is
// a single-row writer), and the aggregation collapses them via
// GROUP BY series_id, file_path + MAX(quality_tier). If this function used a
// bare SELECT DISTINCT it would report the series under BOTH tiers, so a
// Dashboard cell for the losing tier would drill down to a list containing a
// series that cell never counted.
func TestEpisodeTiersBySeries_DedupesSharedFilePathWithDivergentRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 10, Title: "A Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	// Two DIFFERENT episode slots, ONE physical file, divergent tiers.
	const sharedPath = "/tv/A Show/A Show S01E01-E02.mkv"
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: sharedPath, Size: 1_000, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding episode 1: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2,
		FilePath: sharedPath, Size: 2_000, QualityTier: "medium",
	}); err != nil {
		t.Fatalf("seeding episode 2: %v", err)
	}

	tiers, err := s.EpisodeTiersBySeries(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// MAX(quality_tier) = 'medium' ('m' > 'h') — exactly the bucket the
	// aggregation counts this series in, and nothing else.
	want := []string{"medium"}
	if !reflect.DeepEqual(tiers[series.ID], want) {
		t.Errorf("expected the shared file collapsed to %v (the aggregation's MAX), got %v", want, tiers[series.ID])
	}
}

// TestEpisodeTiersBySeries_KeepsDistinctTiersAcrossDifferentFiles is the guard
// on the other side of the dedup above: the collapse is per file_path, NOT per
// series. A series whose episodes really do sit at two tiers must still match
// the filter for both — a GROUP BY series_id with one MAX would silently
// flatten every series to a single tier and break the tier filter wholesale.
func TestEpisodeTiersBySeries_KeepsDistinctTiersAcrossDifferentFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 11, Title: "Another Show", Year: 2019, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: "/tv/Another Show/s01e01.mkv", Size: 1_000, QualityTier: "high",
	}); err != nil {
		t.Fatalf("seeding episode 1: %v", err)
	}
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2,
		FilePath: "/tv/Another Show/s01e02.mkv", Size: 2_000, QualityTier: "medium",
	}); err != nil {
		t.Fatalf("seeding episode 2: %v", err)
	}

	tiers, err := s.EpisodeTiersBySeries(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"high", "medium"}
	if !reflect.DeepEqual(tiers[series.ID], want) {
		t.Errorf("expected both tiers (two distinct files) %v, got %v", want, tiers[series.ID])
	}
}

func TestGetSeries_ByIDAndNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.UpsertSeries(ctx, Series{TMDBID: 705, TVDBID: 905, Title: "Show", Year: 2021, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}

	got, err := s.GetSeries(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != created.ID || got.TMDBID != 705 || got.Title != "Show" || got.RootFolderPath != "/tv" {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, created)
	}

	if _, err := s.GetSeries(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown id, got %v", err)
	}
}

// TestDeleteSeries_RemovesSeasonMonitoredRows covers the cascade migration 0056
// adds. SQLite does not enforce the declared foreign keys here (internal/db's
// Open never runs PRAGMA foreign_keys = ON), so every child table has to be
// hand-deleted — a missed one leaks a row per delete and makes
// ListSeasonStates report phantom seasons for a series that no longer exists.
func TestDeleteSeries_RemovesSeasonMonitoredRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 730, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}
	other, err := s.UpsertSeries(ctx, Series{TMDBID: 731, Title: "Other Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding other series: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, series.ID, 1, true); err != nil {
		t.Fatalf("setting monitored: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, series.ID, 2, false); err != nil {
		t.Fatalf("setting monitored: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, other.ID, 1, true); err != nil {
		t.Fatalf("setting monitored on other series: %v", err)
	}

	if err := s.DeleteSeries(ctx, series.ID); err != nil {
		t.Fatalf("deleting series: %v", err)
	}

	// Both rows go, monitored and unmonitored alike — the UNION in
	// ListSeasonStates reads the table regardless of the flag's value.
	states, err := s.ListSeasonStates(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season states: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("expected no phantom seasons after delete, got %+v", states)
	}
	monitored, err := s.MonitoredSeasons(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing monitored seasons: %v", err)
	}
	if len(monitored) != 0 {
		t.Errorf("expected monitored flags to be deleted with the series, got %v", monitored)
	}

	// Scoped to the deleted series only.
	otherMonitored, err := s.MonitoredSeasons(ctx, other.ID)
	if err != nil {
		t.Fatalf("listing other series' monitored seasons: %v", err)
	}
	if !otherMonitored[1] {
		t.Errorf("another series' flags must survive, got %v", otherMonitored)
	}
}

// TestMonitoredSeasons_AbsentRowMeansUnmonitored pins the schema's core
// semantic (migration 0056): there is no tri-state. A season that has never
// been toggled reads exactly the same as one explicitly set false.
func TestMonitoredSeasons_AbsentRowMeansUnmonitored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 710, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}

	monitored, err := s.MonitoredSeasons(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing monitored seasons: %v", err)
	}
	if len(monitored) != 0 {
		t.Fatalf("a series with no rows has no monitored seasons, got %v", monitored)
	}

	if err := s.SetSeasonMonitored(ctx, series.ID, 2, true); err != nil {
		t.Fatalf("setting monitored: %v", err)
	}
	monitored, err = s.MonitoredSeasons(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing monitored seasons: %v", err)
	}
	if !monitored[2] || len(monitored) != 1 {
		t.Fatalf("expected exactly season 2 monitored, got %v", monitored)
	}
	if monitored[1] {
		t.Error("a season with no row must read as unmonitored")
	}

	// Flipping back to false must remove it from the set, not leave a
	// monitored = false row showing up as monitored.
	if err := s.SetSeasonMonitored(ctx, series.ID, 2, false); err != nil {
		t.Fatalf("clearing monitored: %v", err)
	}
	monitored, err = s.MonitoredSeasons(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing monitored seasons: %v", err)
	}
	if len(monitored) != 0 {
		t.Fatalf("an explicit false reads the same as an absent row, got %v", monitored)
	}

	// Scoped per series: another series' flags must not leak in.
	other, err := s.UpsertSeries(ctx, Series{TMDBID: 711, Title: "Other Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding other series: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, other.ID, 5, true); err != nil {
		t.Fatalf("setting monitored on other series: %v", err)
	}
	monitored, err = s.MonitoredSeasons(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing monitored seasons: %v", err)
	}
	if len(monitored) != 0 {
		t.Errorf("another series' monitored seasons must not leak in, got %v", monitored)
	}
}

// TestSeasonMonitorFlags_DistinguishesAbsentFromExplicitFalse is the sibling of
// the test above, pinning the ONE thing MonitoredSeasons deliberately cannot
// express. Both reads are correct for their own consumer: the dispatch filter
// wants absent-means-unmonitored, while the backoff sweep's DESTRUCTIVE reap
// branch must tell an explicit un-monitor apart from a season nothing ever
// touched, or it kills operator-initiated retry rows on every install.
func TestSeasonMonitorFlags_DistinguishesAbsentFromExplicitFalse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 712, Title: "Flag Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}

	flags, err := s.SeasonMonitorFlags(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season monitor flags: %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("a series with no rows has no flags, got %v", flags)
	}

	if err := s.SetSeasonMonitored(ctx, series.ID, 1, true); err != nil {
		t.Fatalf("monitoring season 1: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, series.ID, 2, false); err != nil {
		t.Fatalf("un-monitoring season 2: %v", err)
	}

	flags, err = s.SeasonMonitorFlags(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season monitor flags: %v", err)
	}
	if len(flags) != 2 {
		t.Fatalf("expected both toggled seasons, got %v", flags)
	}
	if monitored, ok := flags[1]; !ok || !monitored {
		t.Errorf("season 1: got (%v, %v), want (true, true)", monitored, ok)
	}
	// The whole point: present-and-false, NOT absent.
	if monitored, ok := flags[2]; !ok || monitored {
		t.Errorf("season 2: got (%v, %v), want (false, true) — an explicit un-monitor must be distinguishable from a never-toggled season", monitored, ok)
	}
	if _, ok := flags[3]; ok {
		t.Errorf("season 3 was never toggled and must be absent, got %v", flags)
	}

	// Scoped per series, same as MonitoredSeasons.
	other, err := s.UpsertSeries(ctx, Series{TMDBID: 713, Title: "Other Flag Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding other series: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, other.ID, 9, true); err != nil {
		t.Fatalf("setting monitored on other series: %v", err)
	}
	flags, err = s.SeasonMonitorFlags(ctx, series.ID)
	if err != nil {
		t.Fatalf("re-listing season monitor flags: %v", err)
	}
	if _, ok := flags[9]; ok {
		t.Errorf("another series' flags must not leak in, got %v", flags)
	}
}

// TestListSeasonStates_UnionsEpisodeAndMonitoredSeasons covers the read the
// per-season UI is built on. Both halves of the union matter: a season known
// only from episode rows (every season on an install predating this feature),
// and a season known only from a monitored row (discovered from TMDB before its
// episodes are synced).
func TestListSeasonStates_UnionsEpisodeAndMonitoredSeasons(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 720, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}

	// Season 1: two episodes on disk, one still missing. No monitored row.
	for _, ep := range []Episode{
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/tv/Show/s01e01.mkv"},
		{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: "/tv/Show/s01e02.mkv"},
	} {
		if _, err := s.UpsertEpisode(ctx, ep); err != nil {
			t.Fatalf("seeding episode: %v", err)
		}
	}
	if err := s.UpsertEpisodeCatalog(ctx, series.ID, 1, 3, "Third", "2024-05-01"); err != nil {
		t.Fatalf("seeding missing episode: %v", err)
	}

	// Season 2: monitored, with no episode rows at all — the discovered-season
	// case a plain query over library_episodes would never see.
	if err := s.SetSeasonMonitored(ctx, series.ID, 2, true); err != nil {
		t.Fatalf("setting monitored: %v", err)
	}

	// Season 3: has episode rows AND an explicit unmonitored row.
	if err := s.UpsertEpisodeCatalog(ctx, series.ID, 3, 1, "S3 Premiere", "2025-01-01"); err != nil {
		t.Fatalf("seeding season 3 episode: %v", err)
	}
	if err := s.SetSeasonMonitored(ctx, series.ID, 3, false); err != nil {
		t.Fatalf("setting season 3 unmonitored: %v", err)
	}

	states, err := s.ListSeasonStates(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season states: %v", err)
	}
	want := []SeasonState{
		{SeasonNumber: 1, EpisodeCount: 3, MissingCount: 1, Monitored: false},
		{SeasonNumber: 2, EpisodeCount: 0, MissingCount: 0, Monitored: true},
		{SeasonNumber: 3, EpisodeCount: 1, MissingCount: 1, Monitored: false},
	}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("season states = %+v, want %+v", states, want)
	}

	// An episode-less season stays listed after being un-monitored — otherwise
	// the operator could never turn it back on.
	if err := s.SetSeasonMonitored(ctx, series.ID, 2, false); err != nil {
		t.Fatalf("un-monitoring season 2: %v", err)
	}
	states, err = s.ListSeasonStates(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season states: %v", err)
	}
	if len(states) != 3 || states[1].SeasonNumber != 2 || states[1].Monitored {
		t.Errorf("an un-monitored episode-less season must stay listed, got %+v", states)
	}
}

func TestListSeasonStates_EmptyForUnknownSeries(t *testing.T) {
	s := newTestStore(t)
	states, err := s.ListSeasonStates(context.Background(), 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if states == nil || len(states) != 0 {
		t.Errorf("expected an empty, non-nil slice, got %#v", states)
	}
}

// TestUpsertEpisodeCatalog_NeverTouchesFileColumns is the library-corruption
// guard, not a coverage test. UpsertEpisodeCatalog runs unattended from the
// auto-grab cycle over every tracked series, writing rows for episodes it knows
// nothing about on disk. The two failure modes it must structurally prevent:
//
//	(a) overwriting a tracked episode's file_path/size/quality_tier/phash
//	    triple — which would turn downloaded episodes into "missing" ones,
//	    orphan real files, and auto-grab duplicates of them next cycle;
//	(b) blanking a stored non-empty title/air_date with an empty incoming one —
//	    TMDB legitimately returns an empty name/air_date for unannounced or
//	    placeholder episodes, and an episode whose air_date got blanked becomes
//	    PERMANENTLY ineligible for air-date detection, silently.
//
// (b) is what the COALESCE(NULLIF(...)) in the ON CONFLICT clause buys; a plain
// `title = excluded.title` passes case (a) and fails here.
func TestUpsertEpisodeCatalog_NeverTouchesFileColumns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 700, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}

	// A fully-tracked episode: real file, real size/tier, populated phash cache.
	tracked := Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 5,
		Title: "Real Title", AirDate: "2024-03-01",
		FilePath: "/tv/Show/Season 02/s02e05.mkv", Size: 4_200_000_000, QualityTier: "high",
		PHash: "v1:abcdef0123456789", PHashFileSize: 4_200_000_000, PHashFileMTime: "2024-03-02T10:00:00Z",
	}
	if _, err := s.UpsertEpisode(ctx, tracked); err != nil {
		t.Fatalf("seeding tracked episode: %v", err)
	}

	// Case (a): a catalog write with fresh title/air_date updates exactly those
	// two columns and leaves every file/phash column byte-identical.
	if err := s.UpsertEpisodeCatalog(ctx, series.ID, 2, 5, "Corrected Title", "2024-03-04"); err != nil {
		t.Fatalf("catalog upsert: %v", err)
	}
	got, err := s.GetEpisode(ctx, series.ID, 2, 5)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got.Title != "Corrected Title" || got.AirDate != "2024-03-04" {
		t.Errorf("catalog fields should update, got title %q air date %q", got.Title, got.AirDate)
	}
	if got.FilePath != tracked.FilePath {
		t.Errorf("file_path must never be touched: got %q, want %q", got.FilePath, tracked.FilePath)
	}
	if got.Size != tracked.Size {
		t.Errorf("size must never be touched: got %d, want %d", got.Size, tracked.Size)
	}
	if got.QualityTier != tracked.QualityTier {
		t.Errorf("quality_tier must never be touched: got %q, want %q", got.QualityTier, tracked.QualityTier)
	}
	if got.PHash != tracked.PHash || got.PHashFileSize != tracked.PHashFileSize || got.PHashFileMTime != tracked.PHashFileMTime {
		t.Errorf("the phash triple must never be touched: got (%q, %d, %q), want (%q, %d, %q)",
			got.PHash, got.PHashFileSize, got.PHashFileMTime,
			tracked.PHash, tracked.PHashFileSize, tracked.PHashFileMTime)
	}

	// Case (b): TMDB returns an empty name/air_date for this episode on a later
	// cycle. Neither stored value may be blanked.
	if err := s.UpsertEpisodeCatalog(ctx, series.ID, 2, 5, "", ""); err != nil {
		t.Fatalf("catalog upsert with empty values: %v", err)
	}
	got, err = s.GetEpisode(ctx, series.ID, 2, 5)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got.Title != "Corrected Title" {
		t.Errorf("an empty incoming title must not blank a stored one, got %q", got.Title)
	}
	if got.AirDate != "2024-03-04" {
		t.Errorf("an empty incoming air date must not blank a stored one, got %q", got.AirDate)
	}
	if got.FilePath != tracked.FilePath {
		t.Errorf("file_path must still be intact after the empty write, got %q", got.FilePath)
	}
}

// TestUpsertEpisodeCatalog_InsertsTheFilelessRow proves the INSERT half: an
// episode TMDB knows about but that is not on disk gets a row with file_path
// "" — which is exactly what makes it visible to MissingEpisodes. Every other
// column falls to its schema-level default, which is why the INSERT names only
// the columns this writer actually sets (§1.2, m-1).
func TestUpsertEpisodeCatalog_InsertsTheFilelessRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 701, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("seeding series: %v", err)
	}

	if err := s.UpsertEpisodeCatalog(ctx, series.ID, 1, 1, "Pilot", "2024-01-05"); err != nil {
		t.Fatalf("catalog upsert: %v", err)
	}

	missing, err := s.MissingEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatalf("listing missing: %v", err)
	}
	if len(missing) != 1 {
		t.Fatalf("expected exactly one fileless row, got %d", len(missing))
	}
	ep := missing[0]
	if ep.Title != "Pilot" || ep.AirDate != "2024-01-05" {
		t.Errorf("catalog metadata should persist, got title %q air date %q", ep.Title, ep.AirDate)
	}
	if ep.FilePath != "" || ep.Size != 0 || ep.QualityTier != "" {
		t.Errorf("an inserted catalog row is fileless with defaulted columns, got %+v", ep)
	}
	if ep.PHash != "" || ep.PHashFileSize != 0 || ep.PHashFileMTime != "" {
		t.Errorf("phash columns must default to their empty sentinels, got (%q, %d, %q)",
			ep.PHash, ep.PHashFileSize, ep.PHashFileMTime)
	}
}
