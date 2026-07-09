package release

import "testing"

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
