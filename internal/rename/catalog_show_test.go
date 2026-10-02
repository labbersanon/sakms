package rename

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/nfo"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tmdb"
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

func TestStrongFolderTokens(t *testing.T) {
	got := strongFolderTokens("Looney Toons")
	if len(got) != 2 || got[0] != "looney" || got[1] != "toons" {
		t.Fatalf("got %v", got)
	}
	if tok := strongFolderTokens("Popeye"); len(tok) != 0 {
		t.Fatalf("single-word title must not re-query itself, got %v", tok)
	}
	if tok := strongFolderTokens("Up"); len(tok) != 0 {
		t.Fatalf("short tokens are not strong, got %v", tok)
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

func TestFillTMDBEpisodeTitle(t *testing.T) {
	sess := &mode.Session{TMDB: fakeTMDBSeriesServer(t, nil, nil)}

	p := proposals.Proposal{TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1}
	fillTMDBEpisodeTitle(context.Background(), sess, &p)
	if p.EpisodeTitle != "Pilot" {
		t.Fatalf("S01E01 title = %q, want Pilot", p.EpisodeTitle)
	}

	kept := proposals.Proposal{TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1, EpisodeTitle: "Keep Me"}
	fillTMDBEpisodeTitle(context.Background(), sess, &kept)
	if kept.EpisodeTitle != "Keep Me" {
		t.Fatalf("already-set title overwritten: %q", kept.EpisodeTitle)
	}

	miss := proposals.Proposal{TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 16}
	fillTMDBEpisodeTitle(context.Background(), sess, &miss)
	if miss.EpisodeTitle != "" {
		t.Fatalf("E16 miss should stay empty, got %q", miss.EpisodeTitle)
	}

	synth := proposals.Proposal{TMDBID: -1498833576, SeasonNumber: 1, EpisodeNumber: 1}
	fillTMDBEpisodeTitle(context.Background(), sess, &synth)
	if synth.EpisodeTitle != "" {
		t.Fatalf("synthetic TMDB id must no-op, got %q", synth.EpisodeTitle)
	}

	phineas := &mode.Session{TMDB: fakeTMDBEpisodeTitleServer(t, 1877, "Phineas and Ferb", map[int][]tmdb.SeasonEpisode{
		1: {{EpisodeNumber: 16, Name: "Get That Bigfoot!", AirDate: "2008-02-01"}},
	}, -1, nil)}
	slot := proposals.Proposal{TMDBID: 1877, SeasonNumber: 1, EpisodeNumber: 16}
	fillTMDBEpisodeTitle(context.Background(), phineas, &slot)
	if slot.EpisodeTitle != "Get That Bigfoot!" {
		t.Fatalf("Phineas E16 title = %q", slot.EpisodeTitle)
	}

	placeholder := &mode.Session{TMDB: fakeTMDBEpisodeTitleServer(t, 1877, "Phineas and Ferb", map[int][]tmdb.SeasonEpisode{
		1: {{EpisodeNumber: 16, Name: "Episode 16", AirDate: "2008-02-01"}},
	}, -1, nil)}
	blank := proposals.Proposal{TMDBID: 1877, SeasonNumber: 1, EpisodeNumber: 16}
	fillTMDBEpisodeTitle(context.Background(), placeholder, &blank)
	if blank.EpisodeTitle != "" {
		t.Fatalf("placeholder Episode 16 must stay empty, got %q", blank.EpisodeTitle)
	}
}

func TestResolveSeriesTVDBID(t *testing.T) {
	ctx := context.Background()
	if got := resolveSeriesTVDBID(ctx, nil, 850, 7266); got != 7266 {
		t.Fatalf("known id = %d", got)
	}
	if got := resolveSeriesTVDBID(ctx, nil, 850, 0); got != 0 {
		t.Fatalf("nil client = %d", got)
	}
}

func TestFillTMDBEpisodeTitle_TVDBFallback(t *testing.T) {
	tvdbClient := fakeTVDBEpisodesServer(t, []fakeTVDBEpisode{
		{ID: 1, SeriesID: 7266, Name: "A Hare Grows in Manhattan", Number: 5, SeasonNumber: 1947, Aired: "1947-03-22"},
	})

	placeholder := &mode.Session{
		TMDB: fakeTMDBEpisodeTitleServer(t, 850, "Looney Tunes", map[int][]tmdb.SeasonEpisode{
			1947: {{EpisodeNumber: 5, Name: "Episode 5", AirDate: "1947-03-22"}},
		}, -1, nil),
		TVDB: tvdbClient,
	}
	p := proposals.Proposal{TMDBID: 850, TVDBID: 7266, SeasonNumber: 1947, EpisodeNumber: 5}
	fillTMDBEpisodeTitle(context.Background(), placeholder, &p)
	if p.EpisodeTitle != "A Hare Grows in Manhattan" {
		t.Fatalf("placeholder TMDB title = %q, want TVDB cartoon name", p.EpisodeTitle)
	}
	if p.TVDBID != 7266 {
		t.Fatalf("TVDBID = %d, want 7266 persisted on the proposal", p.TVDBID)
	}

	miss := &mode.Session{
		TMDB: fakeTMDBEpisodeTitleServer(t, 850, "Looney Tunes", map[int][]tmdb.SeasonEpisode{
			1947: {{EpisodeNumber: 1, Name: "Other Short", AirDate: "1947-01-01"}},
		}, -1, nil),
		TVDB: tvdbClient,
	}
	empty := proposals.Proposal{TMDBID: 850, TVDBID: 7266, SeasonNumber: 1947, EpisodeNumber: 5}
	fillTMDBEpisodeTitle(context.Background(), miss, &empty)
	if empty.EpisodeTitle != "A Hare Grows in Manhattan" {
		t.Fatalf("TMDB miss title = %q, want TVDB cartoon name", empty.EpisodeTitle)
	}

	hits, n := countingTVDBServer(t)
	keep := &mode.Session{TMDB: fakeTMDBSeriesServer(t, nil, nil), TVDB: hits}
	pilot := proposals.Proposal{TMDBID: 555, TVDBID: 999, SeasonNumber: 1, EpisodeNumber: 1}
	fillTMDBEpisodeTitle(context.Background(), keep, &pilot)
	if pilot.EpisodeTitle != "Pilot" {
		t.Fatalf("real TMDB title overwritten: %q", pilot.EpisodeTitle)
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("TVDB hits = %d, want 0 when TMDB already named the slot", got)
	}
}
