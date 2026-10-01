package rename

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/nfo"
)

func TestSeriesSidecarAgrees(t *testing.T) {
	if !seriesSidecarAgrees(nfo.SeriesNFO{Title: "Looney Tunes"}, "Looney Toons") {
		t.Fatal("folder spelling Toons should still overlap Tunes via looney")
	}
	if seriesSidecarAgrees(nfo.SeriesNFO{Title: "The Tooney and Russo Show"}, "Looney Toons") {
		t.Fatal("podcast nfo must not agree with Looney Toons")
	}
	if !seriesSidecarAgrees(nfo.SeriesNFO{Title: "Curious George"}, "Curious George") {
		t.Fatal("exact title must agree")
	}
	if !seriesSidecarAgrees(nfo.SeriesNFO{}, "Looney Toons") {
		t.Fatal("empty nfo title cannot disagree")
	}
}

func TestShowTitlesAgree_ToonsTunes(t *testing.T) {
	if !showTitlesAgree("Looney Toons", "Looney Tunes") {
		t.Fatal("Toons folder must agree with Tunes catalog title")
	}
	if showTitlesAgree("Looney Toons", "The Tooney and Russo Show") {
		t.Fatal("podcast title must not agree with Looney Toons")
	}
	if showTitlesAgree("Looney Toons", "") {
		t.Fatal("empty catalog title must not agree")
	}
}

func TestShowFolderSearchQueries_ToonsAlias(t *testing.T) {
	got := showFolderSearchQueries("Looney Toons")
	want := map[string]bool{"Looney Toons": true, "Looney Tunes": true}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, q := range got {
		if !want[q] {
			t.Fatalf("unexpected query %q in %v", q, got)
		}
	}
}

func TestCatalogShowTMDBID_KeepsNegativeAnthologyID(t *testing.T) {
	got := catalogShowTMDBID(context.Background(), &mode.Session{TMDB: nil}, nfo.SeriesNFO{
		TMDBID: -1498833576, TVDBID: 73910, Title: "Laurel & Hardy",
	}, "/tv/Laurel & Hardy (1919)/Season 06/S06E08.mp4")
	if got != -1498833576 {
		t.Fatalf("got %d, want synthetic anthology id", got)
	}
}

func TestSeriesSeasonAcceptable_YearSeason(t *testing.T) {
	if !seriesSeasonAcceptable(nil, nil, 1, 1958) {
		t.Fatal("1958 must skip TMDB SeasonDetails")
	}
	if seriesSeasonAcceptable(nil, nil, 1, 1) {
		t.Fatal("sequential season 1 with no client must fail")
	}
}
