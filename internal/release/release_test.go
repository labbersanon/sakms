package release

import (
	"testing"
	"time"
)

func TestParse_TableDriven(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  Info
	}{
		{
			name:  "standard web-dl release",
			title: "Some.Movie.2023.1080p.WEB-DL.x264-GROUP",
			want:  Info{Resolution: 1080, Source: "web-dl", Codec: "x264", Group: "GROUP"},
		},
		{
			name:  "bluray x265 release",
			title: "Some.Movie.2023.2160p.BluRay.x265-OTHERGROUP",
			want:  Info{Resolution: 2160, Source: "bluray", Codec: "x265", Group: "OTHERGROUP"},
		},
		{
			name:  "hevc alias normalizes to x265",
			title: "Some.Show.S01E01.720p.HDTV.HEVC-GROUP",
			want:  Info{Resolution: 720, Source: "hdtv", Codec: "x265", Group: "GROUP"},
		},
		{
			name:  "4k without explicit resolution number",
			title: "Some.Movie.2023.4K.WEBRip.x265-GROUP",
			want:  Info{Resolution: 2160, Source: "webrip", Codec: "x265", Group: "GROUP"},
		},
		{
			name:  "bare web distinct from web-dl",
			title: "Some.Movie.2023.720p.WEB.x264-GROUP",
			want:  Info{Resolution: 720, Source: "web", Codec: "x264", Group: "GROUP"},
		},
		{
			name:  "nonstandard name yields zero values",
			title: "a completely nonstandard release name with no markers",
			want:  Info{},
		},
		{
			name:  "remux distinct from plain bluray",
			title: "Some.Movie.2023.2160p.BluRay.REMUX.x265-GROUP",
			want:  Info{Resolution: 2160, Source: "remux", Codec: "x265", Group: "GROUP"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.title)
			if got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.title, got, tc.want)
			}
		})
	}
}

func TestScore_PrefersHigherRankedResolutionAndSource(t *testing.T) {
	prefs := DefaultProfile()

	best := Info{Resolution: 1080, Source: "web-dl", Codec: "x265"}
	worse := Info{Resolution: 480, Source: "dvdrip", Codec: "x264"}

	if Score(best, prefs) <= Score(worse, prefs) {
		t.Errorf("expected best release to outscore worse release: best=%d worse=%d", Score(best, prefs), Score(worse, prefs))
	}
}

func TestScore_UnknownResolutionScoresWorstOfAll(t *testing.T) {
	prefs := DefaultProfile()

	knownWorst := Info{Resolution: 480, Source: "web-dl"}
	unknown := Info{Resolution: 0, Source: "web-dl"}

	if Score(unknown, prefs) >= Score(knownWorst, prefs) {
		t.Errorf("expected an unrecognized resolution to score no better than the worst known one")
	}
}

func TestScore_BlockedGroupScoresVeryLow(t *testing.T) {
	prefs := DefaultProfile()
	prefs.BlockedGroups = []string{"badgroup"}

	blocked := Info{Resolution: 2160, Source: "web-dl", Codec: "x265", Group: "BadGroup"}
	if Score(blocked, prefs) != blockedGroupScore {
		t.Errorf("expected blocked group to score exactly %d, got %d", blockedGroupScore, Score(blocked, prefs))
	}
}

func TestScore_CodecTiebreakPrefersX265(t *testing.T) {
	prefs := DefaultProfile()

	x265 := Info{Resolution: 1080, Source: "web-dl", Codec: "x265"}
	x264 := Info{Resolution: 1080, Source: "web-dl", Codec: "x264"}

	if Score(x265, prefs) <= Score(x264, prefs) {
		t.Errorf("expected x265 to be preferred as a tiebreak over x264 when resolution/source match")
	}
}

func TestScoreCandidate_TorrentHighSeedersBeatsLowSeedersSameTier(t *testing.T) {
	prefs := DefaultProfile()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := Info{Resolution: 1080, Source: "web-dl", Codec: "x264"}

	wellSeeded := ScoreCandidate(Candidate{Info: info, Protocol: "torrent", Seeders: 150}, prefs, now)
	barelySeeded := ScoreCandidate(Candidate{Info: info, Protocol: "torrent", Seeders: 2}, prefs, now)

	if wellSeeded <= barelySeeded {
		t.Errorf("expected a well-seeded torrent to outscore a barely-seeded one of the same quality tier: well=%d barely=%d", wellSeeded, barelySeeded)
	}
}

func TestScoreCandidate_TorrentSeedersCapped(t *testing.T) {
	prefs := DefaultProfile()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := Info{Resolution: 1080, Source: "web-dl"}

	atCap := ScoreCandidate(Candidate{Info: info, Protocol: "torrent", Seeders: torrentSeederCap}, prefs, now)
	wayOverCap := ScoreCandidate(Candidate{Info: info, Protocol: "torrent", Seeders: torrentSeederCap * 10}, prefs, now)

	if atCap != wayOverCap {
		t.Errorf("expected seeder bonus to be capped at %d, got %d vs %d", torrentSeederCap, atCap, wayOverCap)
	}
}

func TestScoreCandidate_UsenetNewerPostScoresHigherUpToCap(t *testing.T) {
	prefs := DefaultProfile()
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	info := Info{Resolution: 1080, Source: "web-dl"}

	brandNew := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: now.Format(time.RFC3339)}, prefs, now)
	weekOld := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: now.AddDate(0, 0, -7).Format(time.RFC3339)}, prefs, now)
	wayOverCap := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: now.AddDate(0, 0, -365).Format(time.RFC3339)}, prefs, now)
	atCap := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: now.AddDate(0, 0, -usenetAgeCapDays).Format(time.RFC3339)}, prefs, now)

	if brandNew <= weekOld {
		t.Errorf("expected a brand-new usenet post to outscore a week-old one of the same quality: new=%d week=%d", brandNew, weekOld)
	}
	if wayOverCap != atCap {
		t.Errorf("expected the age bonus to be capped at %d days, got %d vs %d", usenetAgeCapDays, atCap, wayOverCap)
	}
}

// Claude 2026-09-22: older-is-safer assertion inverted; kept for history.
// func TestScoreCandidate_UsenetOlderPostScoresHigherUpToCap(t *testing.T) {
// 	prefs := DefaultProfile()
// 	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
// 	info := Info{Resolution: 1080, Source: "web-dl"}
// 	brandNew := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: now.Format(time.RFC3339)}, prefs, now)
// 	weekOld := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: now.AddDate(0, 0, -7).Format(time.RFC3339)}, prefs, now)
// 	if weekOld <= brandNew {
// 		t.Errorf("expected a week-old usenet post to outscore a brand-new one: week=%d new=%d", weekOld, brandNew)
// 	}
// }

func TestScoreCandidate_UsenetAgeDoesNotOverrideResolution(t *testing.T) {
	prefs := DefaultProfile()
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)

	olderBetter := ScoreCandidate(Candidate{
		Info: Info{Resolution: 1080, Source: "web-dl"}, Protocol: "usenet",
		PublishDate: now.AddDate(0, 0, -usenetAgeCapDays).Format(time.RFC3339),
	}, prefs, now)
	newerWorse := ScoreCandidate(Candidate{
		Info: Info{Resolution: 720, Source: "web-dl"}, Protocol: "usenet",
		PublishDate: now.Format(time.RFC3339),
	}, prefs, now)

	if olderBetter <= newerWorse {
		t.Errorf("expected older 1080p to still beat brand-new 720p: older1080=%d new720=%d", olderBetter, newerWorse)
	}
}

func TestScoreCandidate_UsenetUnparseableDateScoresNeutral(t *testing.T) {
	prefs := DefaultProfile()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := Info{Resolution: 1080, Source: "web-dl"}

	base := Score(info, prefs)
	got := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: ""}, prefs, now)
	if got != base {
		t.Errorf("expected an empty publish date to add no bonus, got %d, want %d", got, base)
	}
	gotBadDate := ScoreCandidate(Candidate{Info: info, Protocol: "usenet", PublishDate: "not-a-date"}, prefs, now)
	if gotBadDate != base {
		t.Errorf("expected an unparseable publish date to add no bonus, got %d, want %d", gotBadDate, base)
	}
}

func TestScoreCandidate_IndexerTrustFlagAddsBonus(t *testing.T) {
	prefs := DefaultProfile()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := Info{Resolution: 1080, Source: "web-dl"}

	plain := ScoreCandidate(Candidate{Info: info, Protocol: "torrent"}, prefs, now)
	freeleech := ScoreCandidate(Candidate{Info: info, Protocol: "torrent", IndexerFlags: []string{"freeleech"}}, prefs, now)
	internal := ScoreCandidate(Candidate{Info: info, Protocol: "torrent", IndexerFlags: []string{"Internal"}}, prefs, now)

	if freeleech != plain+indexerTrustBonus {
		t.Errorf("expected freeleech to add exactly %d, got %d vs plain %d", indexerTrustBonus, freeleech, plain)
	}
	if internal != plain+indexerTrustBonus {
		t.Errorf("expected internal (case-insensitive) to add exactly %d, got %d vs plain %d", indexerTrustBonus, internal, plain)
	}
}

func TestScoreCandidate_BlockedGroupStaysBlockedRegardlessOfBonuses(t *testing.T) {
	prefs := DefaultProfile()
	prefs.BlockedGroups = []string{"badgroup"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	blocked := Info{Resolution: 2160, Source: "web-dl", Group: "BadGroup"}

	got := ScoreCandidate(Candidate{
		Info: blocked, Protocol: "torrent", Seeders: 1000, IndexerFlags: []string{"freeleech"},
	}, prefs, now)
	if got != blockedGroupScore {
		t.Errorf("expected a blocked group to stay at %d even with seeder/trust bonuses, got %d", blockedGroupScore, got)
	}
}
