package rename

import (
	"testing"

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

func TestSeriesSeasonAcceptable_YearSeason(t *testing.T) {
	if !seriesSeasonAcceptable(nil, nil, 1, 1958) {
		t.Fatal("1958 must skip TMDB SeasonDetails")
	}
	if seriesSeasonAcceptable(nil, nil, 1, 1) {
		t.Fatal("sequential season 1 with no client must fail")
	}
}
