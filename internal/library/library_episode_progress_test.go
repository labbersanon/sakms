package library

import (
	"context"
	"testing"
)

func TestUpsertEpisodeProgress_RoundTripAndSeriesList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series, err := s.UpsertSeries(ctx, Series{TMDBID: 1, Title: "Show", Year: 2020, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.UpsertSeries(ctx, Series{TMDBID: 2, Title: "Other", Year: 2021, RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	otherEp, err := s.UpsertEpisode(ctx, Episode{SeriesID: other.ID, SeasonNumber: 1, EpisodeNumber: 1})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.UpsertEpisodeProgress(ctx, EpisodeProgress{
		EpisodeID: ep.ID, PositionSeconds: 12.5, DurationSeconds: 100, Watched: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt == "" || got.PositionSeconds != 12.5 || got.Watched {
		t.Fatalf("first write: %+v", got)
	}
	if _, err := s.UpsertEpisodeProgress(ctx, EpisodeProgress{
		EpisodeID: otherEp.ID, PositionSeconds: 99, DurationSeconds: 100, Watched: true,
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := s.ListEpisodeProgressForSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[ep.ID].PositionSeconds != 12.5 {
		t.Fatalf("series list leaked sibling or missed row: %+v", listed)
	}

	got, err = s.UpsertEpisodeProgress(ctx, EpisodeProgress{
		EpisodeID: ep.ID, PositionSeconds: 95, DurationSeconds: 100, Watched: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err = s.ListEpisodeProgressForSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !listed[ep.ID].Watched || listed[ep.ID].PositionSeconds != 95 {
		t.Fatalf("update did not replace: %+v", listed[ep.ID])
	}
}

func TestUpsertEpisodeProgress_RejectsMissingEpisode(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpsertEpisodeProgress(context.Background(), EpisodeProgress{EpisodeID: 999999, PositionSeconds: 1})
	if err == nil {
		t.Fatal("expected FK failure for unknown episode")
	}
}
