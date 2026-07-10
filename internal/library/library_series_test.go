package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	}
	for _, c := range cases {
		season, episode, ok := ParseEpisodeFilename(c.name)
		if ok != c.wantOK || season != c.wantSeason || episode != c.wantEpisode {
			t.Errorf("ParseEpisodeFilename(%q) = (%d, %d, %v), want (%d, %d, %v)",
				c.name, season, episode, ok, c.wantSeason, c.wantEpisode, c.wantOK)
		}
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
		PHash: "phash64/5f:deadbeef", PHashFileSize: 12345, PHashFileMTime: "2026-07-10T00:00:00Z",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "phash64/5f:deadbeef" || got.PHashFileSize != 12345 || got.PHashFileMTime != "2026-07-10T00:00:00Z" {
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

	if err := s.UpdateEpisodePHash(ctx, ep.ID, "phash64/5f:cafe", 999, "2026-07-10T12:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "phash64/5f:cafe" || got.PHashFileSize != 999 || got.PHashFileMTime != "2026-07-10T12:00:00Z" {
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
