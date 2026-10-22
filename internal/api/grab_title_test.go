package api

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func TestAttachEpisodeTitles(t *testing.T) {
	_, _, _, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 1413, Title: "American Horror Story", RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 13, EpisodeNumber: 9, Title: "The Butcher's Children",
	}); err != nil {
		t.Fatal(err)
	}

	list := []grabs.Grab{
		{Mode: mode.Series, Title: "American Horror Story", TMDBID: 1413, SeasonSpecified: true, SeasonNumber: 13, EpisodeNumber: 9},
		{Mode: mode.Series, Title: "American Horror Story", TMDBID: 1413, SeasonSpecified: true, SeasonNumber: 13, EpisodeNumber: 10},
		{Mode: mode.Movies, Title: "A Movie"},
	}
	attachEpisodeTitles(ctx, libStore, list)
	if list[0].EpisodeTitle != "The Butcher's Children" {
		t.Fatalf("episode 9 title = %q", list[0].EpisodeTitle)
	}
	if list[1].EpisodeTitle != "" {
		t.Fatalf("missing catalog episode should stay empty, got %q", list[1].EpisodeTitle)
	}
	if list[2].EpisodeTitle != "" {
		t.Fatalf("movie grab picked up an episode title %q", list[2].EpisodeTitle)
	}
}

func TestFormatGrabCatalogTitle_Empty(t *testing.T) {
	if got := formatGrabCatalogTitle("  ", true, 1, 1, "Pilot"); got != "" {
		t.Fatalf("empty title = %q", got)
	}
}
