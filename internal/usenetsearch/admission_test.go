package usenetsearch

import (
	"testing"

	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestSplitReleaseTitle(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantPrefix string
		wantYear   int
		wantEp     bool
	}{
		{"movie year cut", "Dune.1984.1080p.BluRay.x264-GROUP", "Dune.", 1984, false},
		{"sxxexx prefix cut", "Breaking.Bad.S05E14.1080p.WEB-DL.x264-GROUP", "Breaking.Bad.", 0, true},
		{"year before episode marker captured", "Doctor.Who.2005.S01E01.720p.HDTV.x264-GROUP", "Doctor.Who.", 2005, true},
		{"no year no episode marker", "Dune.1080p.BluRay.x264-GROUP", "Dune.1080p.BluRay.x264-GROUP", 0, false},
		{"nxn episode marker", "Some.Show.1x02.720p.HDTV.x264-GROUP", "Some.Show.", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prefix, year, isEp := splitReleaseTitle(c.in)
			if prefix != c.wantPrefix {
				t.Errorf("prefix = %q, want %q", prefix, c.wantPrefix)
			}
			if year != c.wantYear {
				t.Errorf("year = %d, want %d", year, c.wantYear)
			}
			if isEp != c.wantEp {
				t.Errorf("isEpisode = %v, want %v", isEp, c.wantEp)
			}
		})
	}
}

func TestNormalizeTitleKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"The Dune", "dune"},
		{"Tom & Jerry", "tom and jerry"},
		{"Up", "up"},
		{"Doctor Who", "doctor who"},
		{"  THE   Boys  ", "boys"},
	}
	for _, c := range cases {
		if got := normalizeTitleKey(c.in); got != c.want {
			t.Errorf("normalizeTitleKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassifyMode(t *testing.T) {
	cases := []struct {
		name       string
		isEpisode  bool
		groupModes []string
		want       []string
	}{
		{"episode in movies+series picks series", true, []string{"movies", "series"}, []string{"series"}},
		{"movie in movies+series picks movies", false, []string{"movies", "series"}, []string{"movies"}},
		{"adult-only group, no episode", false, []string{"adult"}, []string{"adult"}},
		{"episode in movies-only group falls back to all", true, []string{"movies"}, []string{"movies"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyMode(c.isEpisode, c.groupModes)
			if len(got) != len(c.want) {
				t.Fatalf("classifyMode() = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("classifyMode() = %v, want %v", got, c.want)
				}
			}
		})
	}
}

func moviesStd(tiers []quality.Tier, minRes, maxRes int) Standard {
	return Standard{Mode: "movies", Policy: "mode", Tiers: tiers, MinRes: minRes, MaxRes: maxRes}
}

func TestResolve_MoviesYearMatch(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{
			"movies": moviesStd([]quality.Tier{quality.Medium}, 0, 0),
		},
		Titles: map[string][]TitleStd{
			"dune": {
				{Mode: "movies", TMDBID: 11, Title: "Dune", Year: 1984, Tiers: []quality.Tier{quality.Low}},
				{Mode: "movies", TMDBID: 22, Title: "Dune", Year: 2021, Tiers: []quality.Tier{quality.High}, MinRes: 1080},
			},
		},
	}

	stds, match := snap.Resolve("alt.binaries.movies", []string{"movies"}, "Dune.1984.1080p.BluRay.x264-GROUP")
	if len(stds) != 1 {
		t.Fatalf("stds = %v, want 1 entry", stds)
	}
	if stds[0].Policy != "title" || stds[0].TMDBID != 11 || stds[0].Match != "exact-year" {
		t.Errorf("1984 release resolved to %+v, want title/11/exact-year", stds[0])
	}
	if match.Year != 1984 {
		t.Errorf("match.Year = %d, want 1984", match.Year)
	}

	stds2, _ := snap.Resolve("alt.binaries.movies", []string{"movies"}, "Dune.2021.2160p.WEB-DL.x265-GROUP")
	if len(stds2) != 1 || stds2[0].TMDBID != 22 || stds2[0].Match != "exact-year" {
		t.Errorf("2021 release resolved to %+v, want title/22/exact-year", stds2)
	}
}

func TestResolve_MovieNoYearFallsBack(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{
			"movies": moviesStd([]quality.Tier{quality.Medium}, 0, 0),
		},
		Titles: map[string][]TitleStd{
			"dune": {
				{Mode: "movies", TMDBID: 11, Title: "Dune", Year: 1984, Tiers: []quality.Tier{quality.Low}},
				{Mode: "movies", TMDBID: 22, Title: "Dune", Year: 2021, Tiers: []quality.Tier{quality.High}, MinRes: 1080},
			},
		},
	}
	stds, _ := snap.Resolve("alt.binaries.movies", []string{"movies"}, "Dune.1080p.BluRay.x264-GROUP")
	if len(stds) != 1 || stds[0].Policy != "mode" || stds[0].Match != "ambiguous" {
		t.Errorf("no-year release resolved to %+v, want mode/ambiguous fallback", stds)
	}
}

func TestResolve_ShortTitleYearRequired(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{"movies": moviesStd([]quality.Tier{quality.Medium}, 0, 0)},
		Titles: map[string][]TitleStd{
			"up": {{Mode: "movies", TMDBID: 1, Title: "Up", Year: 2009, Tiers: []quality.Tier{quality.High}}},
		},
	}
	stds, _ := snap.Resolve("alt.binaries.movies", []string{"movies"}, "Up.2009.1080p.BluRay.x264-GROUP")
	if len(stds) != 1 || stds[0].TMDBID != 1 || stds[0].Match != "exact-year" {
		t.Errorf("Up.2009 resolved to %+v, want title/1/exact-year", stds)
	}

	stds2, _ := snap.Resolve("alt.binaries.movies", []string{"movies"}, "Up.1080p.BluRay.x264-GROUP")
	if len(stds2) != 1 || stds2[0].Policy != "mode" {
		t.Errorf("Up without a year resolved to %+v, want mode fallback (no year = no match)", stds2)
	}
}

func TestResolve_SeriesAmbiguityFallsBack(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{"series": {Mode: "series", Policy: "mode", Tiers: []quality.Tier{quality.Medium}}},
		Titles: map[string][]TitleStd{
			"doctor who": {
				{Mode: "series", TMDBID: 100, Title: "Doctor Who", Year: 1963, Tiers: []quality.Tier{quality.Low}},
				{Mode: "series", TMDBID: 200, Title: "Doctor Who", Year: 2005, Tiers: []quality.Tier{quality.High}},
			},
		},
	}
	// No year on the release: ambiguous, falls back.
	stds, _ := snap.Resolve("alt.binaries.tv", []string{"series"}, "Doctor.Who.S01E01.720p.HDTV.x264-GROUP")
	if len(stds) != 1 || stds[0].Policy != "mode" || stds[0].Match != "ambiguous" {
		t.Errorf("ambiguous Doctor Who resolved to %+v, want mode/ambiguous", stds)
	}
	// Year disambiguates to exactly one candidate.
	stds2, _ := snap.Resolve("alt.binaries.tv", []string{"series"}, "Doctor.Who.2005.S01E01.720p.HDTV.x264-GROUP")
	if len(stds2) != 1 || stds2[0].TMDBID != 200 || stds2[0].Match != "exact-year" {
		t.Errorf("Doctor.Who.2005 resolved to %+v, want title/200/exact-year", stds2)
	}
}

func TestResolve_SeriesSingleCandidateMatchesWithoutYear(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{"series": {Mode: "series", Policy: "mode", Tiers: []quality.Tier{quality.Medium}}},
		Titles: map[string][]TitleStd{
			"you": {{Mode: "series", TMDBID: 50, Title: "You", Tiers: []quality.Tier{quality.High}, MinRes: 1080}},
		},
	}
	stds, _ := snap.Resolve("alt.binaries.tv", []string{"series"}, "You.S04E01.1080p.WEB-DL.x265-GROUP")
	if len(stds) != 1 || stds[0].TMDBID != 50 || stds[0].Match != "exact" {
		t.Errorf("You S04E01 resolved to %+v, want title/50/exact", stds)
	}
}

func TestResolve_SharedGroupSeriesAdmittedViaSeries(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{
			"movies": moviesStd([]quality.Tier{quality.High, quality.Lossless}, 1080, 0),
			"series": {Mode: "series", Policy: "mode", Tiers: []quality.Tier{quality.Low, quality.Medium, quality.High, quality.Lossless}},
		},
	}
	name := "Random.Show.S01E01.720p.HDTV.x264-GROUP"
	stds, match := snap.Resolve("alt.binaries.multimedia", []string{"movies", "series"}, name)
	if len(match.CandModes) != 1 || match.CandModes[0] != "series" {
		t.Fatalf("CandModes = %v, want [series] (classifyMode picks series when it's in groupModes)", match.CandModes)
	}
	info := release.Parse(name)
	ok, reason := gate(name, info, stds[0])
	if !ok {
		t.Errorf("gate against series standard = (%v, %q), want ok (series is lenient)", ok, reason)
	}
	// Confirm the movies-strict standard, if it HAD been tried, would reject
	// this release — proving classifyMode's series pick (not luck) is why it
	// was admitted.
	moviesOnly := moviesStd([]quality.Tier{quality.High, quality.Lossless}, 1080, 0)
	if ok, _ := gate(name, info, moviesOnly); ok {
		t.Errorf("movies-strict standard unexpectedly admitted a 720p HDTV release")
	}
}

func TestGate_UnknownSourceNeutral(t *testing.T) {
	info := release.Info{Resolution: 1080}
	std := Standard{Tiers: []quality.Tier{quality.Medium}}
	ok, reason := gate("Some.Release.1080p-GROUP", info, std)
	if !ok {
		t.Errorf("gate() = (%v, %q), want ok=true (unknown source is neutral)", ok, reason)
	}
}

func TestGate_UnknownResolutionStrictReject(t *testing.T) {
	info := release.Info{Source: "webrip"}
	std := Standard{Tiers: []quality.Tier{quality.Low, quality.Medium}, MinRes: 1080}
	ok, reason := gate("Some.Release.WEBRip-GROUP", info, std)
	if ok || reason != reasonResUnknown {
		t.Errorf("gate() = (%v, %q), want (false, %q)", ok, reason, reasonResUnknown)
	}
}

func TestGate_AboveMaxReject(t *testing.T) {
	info := release.Info{Resolution: 2160, Source: "web-dl"}
	std := Standard{Tiers: []quality.Tier{quality.Medium}, MaxRes: 1080}
	ok, reason := gate("Some.Release.2160p.WEB-DL-GROUP", info, std)
	if ok || reason != reasonResAboveMax {
		t.Errorf("gate() = (%v, %q), want (false, %q)", ok, reason, reasonResAboveMax)
	}
}

func TestGate_RemuxOutOfLowMediumBandReject(t *testing.T) {
	info := release.Info{Resolution: 1080, Source: "remux"}
	std := Standard{Tiers: []quality.Tier{quality.Low, quality.Medium}}
	ok, reason := gate("Some.Release.1080p.BluRay.REMUX-GROUP", info, std)
	if ok || reason != reasonTierOutOfBand {
		t.Errorf("gate() = (%v, %q), want (false, %q)", ok, reason, reasonTierOutOfBand)
	}
}

func TestGate_BelowMinReject(t *testing.T) {
	info := release.Info{Resolution: 720, Source: "web-dl"}
	std := Standard{Tiers: []quality.Tier{quality.Medium}, MinRes: 1080}
	ok, reason := gate("Some.Release.720p.WEB-DL-GROUP", info, std)
	if ok || reason != reasonResBelowMin {
		t.Errorf("gate() = (%v, %q), want (false, %q)", ok, reason, reasonResBelowMin)
	}
}

func TestLoosestOf_AnyNoFloorWins(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{
			"movies": moviesStd([]quality.Tier{quality.Medium}, 0, 0), // no floor/cap
		},
		Titles: map[string][]TitleStd{
			"some title": {
				{Mode: "movies", Title: "Some Title", Tiers: []quality.Tier{quality.Low}, MinRes: 1080, MaxRes: 1080},
			},
		},
	}
	std := snap.loosestOf("movies")
	if std.MinRes != 0 || std.MaxRes != 0 {
		t.Errorf("loosestOf = %+v, want MinRes=0 MaxRes=0 (mode default's no-floor/no-cap must win)", std)
	}
	if !tierAllowed(quality.Low, std.Tiers) || !tierAllowed(quality.Medium, std.Tiers) {
		t.Errorf("loosestOf tiers = %v, want union of Low and Medium", std.Tiers)
	}
}

func TestLoosestOf_MinOfSetFloors(t *testing.T) {
	snap := &PolicySnapshot{
		Modes: map[string]Standard{
			"series": {Mode: "series", Policy: "mode", Tiers: []quality.Tier{quality.High}, MinRes: 1080},
		},
		Titles: map[string][]TitleStd{
			"looney tunes": {
				{Mode: "series", Title: "Looney Tunes", Tiers: []quality.Tier{quality.Low}, MinRes: 480},
			},
		},
	}
	std := snap.loosestOf("series")
	if std.MinRes != 480 {
		t.Errorf("loosestOf MinRes = %d, want 480 (min of the set floors)", std.MinRes)
	}
}

func statResults(vs ...usenet.StatVerdict) []usenet.StatVerdict { return vs }

func TestStatDecision(t *testing.T) {
	cases := []struct {
		name      string
		results   []usenet.StatVerdict
		wantKeep  bool
		wantLabel string
	}{
		{"all present", statResults(usenet.StatPresent, usenet.StatPresent, usenet.StatPresent), true, "ok"},
		{"1 of 3 missing", statResults(usenet.StatPresent, usenet.StatPresent, usenet.StatMissing), true, "partial"},
		{"2 of 3 missing", statResults(usenet.StatPresent, usenet.StatMissing, usenet.StatMissing), false, "missing"},
		{"n=1 all missing", statResults(usenet.StatMissing), false, "missing"},
		{"n=2 all missing", statResults(usenet.StatMissing, usenet.StatMissing), false, "missing"},
		{"missing plus inconclusive stays inconclusive", statResults(usenet.StatPresent, usenet.StatInconclusive, usenet.StatMissing), true, "inconclusive"},
		{"inconclusive only", statResults(usenet.StatInconclusive, usenet.StatInconclusive, usenet.StatPresent), true, "inconclusive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keep, label := statDecision(c.results)
			if keep != c.wantKeep || label != c.wantLabel {
				t.Errorf("statDecision(%v) = (%v, %q), want (%v, %q)", c.results, keep, label, c.wantKeep, c.wantLabel)
			}
		})
	}
}

func TestPickSample_DeterministicFirstMedianLast(t *testing.T) {
	rows := []HeaderRow{
		{Filename: "a", PartN: 5, MsgNum: 105, MsgID: "m105", IsMeta: false},
		{Filename: "a", PartN: 1, MsgNum: 101, MsgID: "m101", IsMeta: false},
		{Filename: "a", PartN: 3, MsgNum: 103, MsgID: "m103", IsMeta: false},
		{Filename: "a", PartN: 2, MsgNum: 102, MsgID: "m102", IsMeta: false},
		{Filename: "a", PartN: 4, MsgNum: 104, MsgID: "m104", IsMeta: false},
	}
	got := pickSample(rows)
	want := []string{"m101", "m103", "m105"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("pickSample() = %v, want %v", got, want)
	}
}

func TestPickSample_FewerThanThreeReturnsAll(t *testing.T) {
	rows := []HeaderRow{
		{Filename: "a", PartN: 1, MsgNum: 1, MsgID: "m1", IsMeta: false},
		{Filename: "a", PartN: 2, MsgNum: 2, MsgID: "m2", IsMeta: false},
	}
	got := pickSample(rows)
	if len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
		t.Errorf("pickSample() = %v, want [m1 m2]", got)
	}
}

func TestPickSample_MetaFallbackWhenNoPayload(t *testing.T) {
	rows := []HeaderRow{
		{Filename: "a.par2", PartN: 2, MsgNum: 2, MsgID: "p2", IsMeta: true},
		{Filename: "a.par2", PartN: 1, MsgNum: 1, MsgID: "p1", IsMeta: true},
	}
	got := pickSample(rows)
	if len(got) != 2 || got[0] != "p1" || got[1] != "p2" {
		t.Errorf("pickSample() = %v, want [p1 p2] (meta fallback, sorted)", got)
	}
}

func TestPickSample_EmptyRows(t *testing.T) {
	if got := pickSample(nil); got != nil {
		t.Errorf("pickSample(nil) = %v, want nil", got)
	}
}
