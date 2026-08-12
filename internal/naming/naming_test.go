package naming

import (
	"path/filepath"
	"testing"
)

func TestValid(t *testing.T) {
	if !Valid(Jellyfin) || !Valid(Legacy) {
		t.Error("expected both Jellyfin and Legacy to be valid presets")
	}
	if Valid(Preset("bogus")) {
		t.Error("expected an unrecognized preset to be invalid")
	}
}

func TestMovieFolderName(t *testing.T) {
	cases := []struct {
		preset       Preset
		title        string
		year, tmdbID int
		want         string
	}{
		{Jellyfin, "Some Movie", 2020, 42, "Some Movie (2020) [tmdbid-42]"},
		{Legacy, "Some Movie", 2020, 42, "Some Movie (2020)"},
		{Jellyfin, "Some Movie", 0, 42, "Some Movie [tmdbid-42]"},
		{Jellyfin, "Some Movie", 2020, 0, "Some Movie (2020)"},
		{Jellyfin, "Some Movie", 0, 0, "Some Movie"},
		{Jellyfin, "Web Special", 1982, -42, "Web Special (1982)"},
		{Jellyfin, "9/11: Truth, Lies and Conspiracies", 2016, 496063,
			"9-11: Truth, Lies and Conspiracies (2016) [tmdbid-496063]"},
	}
	for _, c := range cases {
		if got := MovieFolderName(c.preset, c.title, c.year, c.tmdbID); got != c.want {
			t.Errorf("MovieFolderName(%v, %q, %d, %d) = %q, want %q", c.preset, c.title, c.year, c.tmdbID, got, c.want)
		}
	}
}

func TestSafePathComponent(t *testing.T) {
	if got := SafePathComponent(`a/b\c` + "\x00" + "d"); got != "a-b-c_d" {
		t.Errorf("got %q", got)
	}
}

func TestMovieFileName(t *testing.T) {
	if got := MovieFileName(Jellyfin, "Some Movie", 2020, 42, ".mkv"); got != "Some Movie (2020) [tmdbid-42].mkv" {
		t.Errorf("unexpected file name: %q", got)
	}
	if got := MovieFileName(Legacy, "Some Movie", 2020, 42, ".mkv"); got != "Some Movie (2020).mkv" {
		t.Errorf("unexpected legacy file name: %q", got)
	}
}

func TestSeriesFolderName(t *testing.T) {
	if got := SeriesFolderName(Jellyfin, "Some Show", 2019, 555); got != "Some Show (2019) [tmdbid-555]" {
		t.Errorf("unexpected jellyfin series folder: %q", got)
	}
	if got := SeriesFolderName(Legacy, "Some Show", 2019, 555); got != "Some Show" {
		t.Errorf("expected legacy series folder to stay a bare title, got %q", got)
	}
}

func TestSeasonDirName(t *testing.T) {
	if got := SeasonDirName(3); got != "Season 03" {
		t.Errorf("expected %q, got %q", "Season 03", got)
	}
}

func TestAdultFileName(t *testing.T) {
	cases := []struct {
		name                       string
		studio, title, date, phash string
		ext                        string
		want                       string
	}{
		{"all fields", "Brazzers", "Scene Title", "2021-03-04", "abc123", ".mp4",
			"Brazzers - Scene Title (2021-03-04) [phash-abc123].mp4"},
		{"missing studio drops the prefix", "", "Scene Title", "2021-03-04", "abc123", ".mkv",
			"Scene Title (2021-03-04) [phash-abc123].mkv"},
		{"missing date drops the segment", "Brazzers", "Scene Title", "", "abc123", ".mp4",
			"Brazzers - Scene Title [phash-abc123].mp4"},
		{"missing phash drops the tag", "Brazzers", "Scene Title", "2021-03-04", "", ".mp4",
			"Brazzers - Scene Title (2021-03-04).mp4"},
		{"title only", "", "Scene Title", "", "", ".avi", "Scene Title.avi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AdultFileName(c.studio, c.title, c.date, c.phash, c.ext); got != c.want {
				t.Errorf("AdultFileName(%q, %q, %q, %q, %q) = %q, want %q", c.studio, c.title, c.date, c.phash, c.ext, got, c.want)
			}
		})
	}
}

func TestEpisodeFileName(t *testing.T) {
	if got := EpisodeFileName(Jellyfin, "Show Name", 3, 5, "Episode Title", ".mkv"); got != "Show Name S03E05 Episode Title.mkv" {
		t.Errorf("unexpected jellyfin file name: %q", got)
	}
	if got := EpisodeFileName(Jellyfin, "Show Name", 3, 5, "", ".mkv"); got != "Show Name S03E05.mkv" {
		t.Errorf("unexpected jellyfin file name with no episode title: %q", got)
	}
	if got := EpisodeFileName(Legacy, "Show Name", 3, 5, "Episode Title", ".mkv"); got != "Show Name - S03E05 - Episode Title.mkv" {
		t.Errorf("unexpected legacy file name: %q", got)
	}
	if got := EpisodeFileName(Legacy, "Show Name", 3, 5, "", ".mkv"); got != "Show Name - S03E05.mkv" {
		t.Errorf("unexpected legacy file name with no episode title: %q", got)
	}
}

func TestEpisodeRangeFileName(t *testing.T) {
	if got := EpisodeRangeFileName(Jellyfin, "Show Name", 3, []int{5, 6}, "", ".mkv"); got != "Show Name S03E05-E06.mkv" {
		t.Errorf("unexpected jellyfin range file name: %q", got)
	}
	if got := EpisodeRangeFileName(Legacy, "Show Name", 3, []int{5, 6}, "", ".mkv"); got != "Show Name - S03E05-E06.mkv" {
		t.Errorf("unexpected legacy range file name: %q", got)
	}
	// A 3+ episode range uses first/last, not every number in between.
	if got := EpisodeRangeFileName(Jellyfin, "Show Name", 1, []int{1, 2, 3}, "", ".mkv"); got != "Show Name S01E01-E03.mkv" {
		t.Errorf("unexpected 3-episode range file name: %q", got)
	}
	if got := EpisodeRangeFileName(Jellyfin, "Show Name", 3, []int{5, 6}, "Episode Title", ".mkv"); got != "Show Name S03E05-E06 Episode Title.mkv" {
		t.Errorf("unexpected jellyfin range file name with episode title: %q", got)
	}
	// Fewer than 2 numbers falls straight through to EpisodeFileName's
	// ordinary single-episode rendering — no separate branch needed by
	// callers.
	if got := EpisodeRangeFileName(Jellyfin, "Show Name", 3, []int{5}, "", ".mkv"); got != "Show Name S03E05.mkv" {
		t.Errorf("expected single-episode fallback, got %q", got)
	}
	if got := EpisodeRangeFileName(Jellyfin, "Show Name", 3, nil, "", ".mkv"); got != "Show Name S03E00.mkv" {
		t.Errorf("expected empty-slice fallback to episode 0, got %q", got)
	}
}

// episodeAlternateCase is shared by TestEpisodeAlternateFileName and
// TestEpisodeAlternateFileName_MatchesSeriesSchema on purpose: a case added for
// the byte-exact rendering is automatically also checked for schema
// conformance, which is what keeps the second test a real regression guard
// rather than a hand-maintained second list that drifts out of sync.
type episodeAlternateCase struct {
	name                string
	preset              Preset
	seriesTitle         string
	seasonNumber        int
	episodeNumbers      []int
	episodeTitle        string
	res, codec, bitrate string
	ext                 string
	want                string
}

// The plan's §4.3 matrix in full: Jellyfin + Legacy × (all tokens / partial
// tokens / no tokens) × (single / range) × (with title / without), plus §4.3's
// own four rendered examples verbatim as named cases.
var episodeAlternateCases = []episodeAlternateCase{
	// --- Jellyfin, single episode ---
	{"jellyfin single with-title all-tokens", Jellyfin, "Some Show", 6, []int{14}, "Funny Faces", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show S06E14 Funny Faces - 1080p h264 5 Mbps.mkv"},
	{"jellyfin single with-title partial-tokens", Jellyfin, "Some Show", 6, []int{14}, "Funny Faces", "720p", "", "", ".mkv",
		"Some Show S06E14 Funny Faces - 720p.mkv"},
	{"jellyfin single with-title no-tokens", Jellyfin, "Some Show", 6, []int{14}, "Funny Faces", "", "", "", ".mkv",
		"Some Show S06E14 Funny Faces - alternate.mkv"},
	{"jellyfin single no-title all-tokens", Jellyfin, "Some Show", 6, []int{14}, "", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show S06E14 - 1080p h264 5 Mbps.mkv"},
	{"jellyfin single no-title partial-tokens", Jellyfin, "Some Show", 6, []int{14}, "", "720p", "", "", ".mkv",
		"Some Show S06E14 - 720p.mkv"},
	{"jellyfin single no-title no-tokens", Jellyfin, "Some Show", 6, []int{14}, "", "", "", "", ".mkv",
		"Some Show S06E14 - alternate.mkv"},

	// --- Jellyfin, range (logical-episode-split) ---
	{"jellyfin range with-title all-tokens", Jellyfin, "Some Show", 6, []int{14, 15}, "Funny Faces", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show S06E14-E15 Funny Faces - 1080p h264 5 Mbps.mkv"},
	{"jellyfin range with-title partial-tokens", Jellyfin, "Some Show", 6, []int{14, 15}, "Funny Faces", "720p", "", "", ".mkv",
		"Some Show S06E14-E15 Funny Faces - 720p.mkv"},
	{"jellyfin range with-title no-tokens", Jellyfin, "Some Show", 6, []int{14, 15}, "Funny Faces", "", "", "", ".mkv",
		"Some Show S06E14-E15 Funny Faces - alternate.mkv"},
	{"jellyfin range no-title all-tokens", Jellyfin, "Some Show", 6, []int{14, 15}, "", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show S06E14-E15 - 1080p h264 5 Mbps.mkv"},
	{"jellyfin range no-title partial-tokens", Jellyfin, "Some Show", 6, []int{14, 15}, "", "720p", "", "", ".mkv",
		"Some Show S06E14-E15 - 720p.mkv"},
	{"jellyfin range no-title no-tokens", Jellyfin, "Some Show", 6, []int{14, 15}, "", "", "", "", ".mkv",
		"Some Show S06E14-E15 - alternate.mkv"},

	// --- Legacy, single episode ---
	{"legacy single with-title all-tokens", Legacy, "Some Show", 6, []int{14}, "Funny Faces", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show - S06E14 - Funny Faces - 1080p h264 5 Mbps.mkv"},
	{"legacy single with-title partial-tokens", Legacy, "Some Show", 6, []int{14}, "Funny Faces", "720p", "", "", ".mkv",
		"Some Show - S06E14 - Funny Faces - 720p.mkv"},
	{"legacy single with-title no-tokens", Legacy, "Some Show", 6, []int{14}, "Funny Faces", "", "", "", ".mkv",
		"Some Show - S06E14 - Funny Faces - alternate.mkv"},
	{"legacy single no-title all-tokens", Legacy, "Some Show", 6, []int{14}, "", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show - S06E14 - 1080p h264 5 Mbps.mkv"},
	{"legacy single no-title partial-tokens", Legacy, "Some Show", 6, []int{14}, "", "720p", "", "", ".mkv",
		"Some Show - S06E14 - 720p.mkv"},
	{"legacy single no-title no-tokens", Legacy, "Some Show", 6, []int{14}, "", "", "", "", ".mkv",
		"Some Show - S06E14 - alternate.mkv"},

	// --- Legacy, range (logical-episode-split) ---
	{"legacy range with-title all-tokens", Legacy, "Some Show", 6, []int{14, 15}, "Funny Faces", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show - S06E14-E15 - Funny Faces - 1080p h264 5 Mbps.mkv"},
	{"legacy range with-title partial-tokens", Legacy, "Some Show", 6, []int{14, 15}, "Funny Faces", "720p", "", "", ".mkv",
		"Some Show - S06E14-E15 - Funny Faces - 720p.mkv"},
	{"legacy range with-title no-tokens", Legacy, "Some Show", 6, []int{14, 15}, "Funny Faces", "", "", "", ".mkv",
		"Some Show - S06E14-E15 - Funny Faces - alternate.mkv"},
	{"legacy range no-title all-tokens", Legacy, "Some Show", 6, []int{14, 15}, "", "1080p", "h264", "5 Mbps", ".mkv",
		"Some Show - S06E14-E15 - 1080p h264 5 Mbps.mkv"},
	{"legacy range no-title partial-tokens", Legacy, "Some Show", 6, []int{14, 15}, "", "720p", "", "", ".mkv",
		"Some Show - S06E14-E15 - 720p.mkv"},
	{"legacy range no-title no-tokens", Legacy, "Some Show", 6, []int{14, 15}, "", "", "", "", ".mkv",
		"Some Show - S06E14-E15 - alternate.mkv"},

	// --- Plan §4.3's four rendered examples, verbatim ---
	{"§4.3 jellyfin all tokens", Jellyfin, "The Red Skelton Hour", 6, []int{14}, "Funny Faces", "1080p", "h264", "5 Mbps", ".mkv",
		"The Red Skelton Hour S06E14 Funny Faces - 1080p h264 5 Mbps.mkv"},
	{"§4.3 legacy all tokens", Legacy, "The Red Skelton Hour", 6, []int{14}, "Funny Faces", "1080p", "h264", "5 Mbps", ".mkv",
		"The Red Skelton Hour - S06E14 - Funny Faces - 1080p h264 5 Mbps.mkv"},
	{"§4.3 jellyfin no title no tokens", Jellyfin, "The Red Skelton Hour", 6, []int{14}, "", "", "", "", ".mkv",
		"The Red Skelton Hour S06E14 - alternate.mkv"},
	{"§4.3 jellyfin range partial tokens", Jellyfin, "Show", 1, []int{1, 2}, "Pilot", "720p", "", "", ".mkv",
		"Show S01E01-E02 Pilot - 720p.mkv"},
}

func TestEpisodeAlternateFileName(t *testing.T) {
	for _, c := range episodeAlternateCases {
		t.Run(c.name, func(t *testing.T) {
			got := EpisodeAlternateFileName(c.preset, c.seriesTitle, c.seasonNumber, c.episodeNumbers,
				c.episodeTitle, c.res, c.codec, c.bitrate, c.ext)
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// Every alternate name must be accepted by MatchesSeriesSchema under its own
// preset, or Rename re-proposes the alternate on every subsequent Scan —
// forever, with no error and no log line. Nothing else in the suite would catch
// a change to the " - " suffix separator.
//
// MatchesSeriesSchema does no filesystem access (pure filepath.Base/Dir plus a
// regex), so a synthetic path is sufficient — no temp dir or real file needed.
// The Jellyfin grandparent must itself be schema-conformant; Legacy's series
// folder has no fixed shape and is not checked, so one path shape serves both.
func TestEpisodeAlternateFileName_MatchesSeriesSchema(t *testing.T) {
	for _, c := range episodeAlternateCases {
		t.Run(c.name, func(t *testing.T) {
			name := EpisodeAlternateFileName(c.preset, c.seriesTitle, c.seasonNumber, c.episodeNumbers,
				c.episodeTitle, c.res, c.codec, c.bitrate, c.ext)
			path := filepath.Join("/tv",
				SeriesFolderName(Jellyfin, c.seriesTitle, 2019, 555),
				SeasonDirName(c.seasonNumber),
				name)
			if !MatchesSeriesSchema(path, c.preset) {
				t.Errorf("alternate %q is not %s-schema-conformant; it would be re-proposed on every Scan", path, c.preset)
			}
		})
	}
}

// adultAlternateCase is shared by TestAdultAlternateFileName and
// TestAdultAlternateFileName_MatchesAdultSchema_WithPhash so that cases added
// for byte-exact rendering are automatically checked for schema conformance too.
type adultAlternateCase struct {
	name                            string
	studio, title, date, phash      string
	res, codec, bitrate             string
	ext                             string
	want                            string
}

var adultAlternateCases = []adultAlternateCase{
	// Plan §7.1 named example verbatim
	{"all fields all tokens", "Studio", "Scene Title", "2024-01-02", "abc123", "1080p", "h264", "8 Mbps", ".mkv",
		"Studio - Scene Title (2024-01-02) [phash-abc123] - 1080p h264 8 Mbps.mkv"},
	{"partial tokens res-only", "Studio", "Scene Title", "2024-01-02", "abc123", "720p", "", "", ".mkv",
		"Studio - Scene Title (2024-01-02) [phash-abc123] - 720p.mkv"},
	{"no tokens falls back to alternate", "Studio", "Scene Title", "2024-01-02", "abc123", "", "", "", ".mkv",
		"Studio - Scene Title (2024-01-02) [phash-abc123] - alternate.mkv"},
	// Empty phash drops the tag — alternate has its own hash, not the primary's
	{"empty phash drops tag", "Studio", "Scene Title", "2024-01-02", "", "1080p", "h264", "8 Mbps", ".mkv",
		"Studio - Scene Title (2024-01-02) - 1080p h264 8 Mbps.mkv"},
	{"empty phash no tokens", "Studio", "Scene Title", "2024-01-02", "", "", "", "", ".mkv",
		"Studio - Scene Title (2024-01-02) - alternate.mkv"},
	// Optional fields omitted
	{"no studio", "", "Scene Title", "2024-01-02", "def456", "1080p", "h264", "8 Mbps", ".mkv",
		"Scene Title (2024-01-02) [phash-def456] - 1080p h264 8 Mbps.mkv"},
	{"no date", "Studio", "Scene Title", "", "def456", "1080p", "h264", "8 Mbps", ".mkv",
		"Studio - Scene Title [phash-def456] - 1080p h264 8 Mbps.mkv"},
	{"title only no tokens", "", "Scene Title", "", "", "", "", "", ".avi",
		"Scene Title - alternate.avi"},
}

func TestAdultAlternateFileName(t *testing.T) {
	for _, c := range adultAlternateCases {
		t.Run(c.name, func(t *testing.T) {
			got := AdultAlternateFileName(c.studio, c.title, c.date, c.phash, c.res, c.codec, c.bitrate, c.ext)
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// An alternate name with a phash must be accepted by MatchesAdultSchema so the
// known-map keeps it from being re-proposed on every Scan. An alternate without
// a phash (empty hash) renders without the [phash-…] tag and MatchesAdultSchema
// returns false — the known-map from AllScenePaths is the real guard in that case.
func TestAdultAlternateFileName_MatchesAdultSchema_WithPhash(t *testing.T) {
	for _, c := range adultAlternateCases {
		if c.phash == "" {
			continue // no-phash alternate has no [phash-…] tag by design; schema check not applicable
		}
		t.Run(c.name, func(t *testing.T) {
			name := AdultAlternateFileName(c.studio, c.title, c.date, c.phash, c.res, c.codec, c.bitrate, c.ext)
			path := filepath.Join("/adult", name)
			if !MatchesAdultSchema(path) {
				t.Errorf("alternate %q (with phash) is not MatchesAdultSchema-conformant; it would be re-proposed on every Scan", path)
			}
		})
	}
}

// An alternate without a phash must NOT match AdultSchema — MatchesAdultSchema
// checks for [phash-…] and a name without it returns false. This pins the
// known-map dependency documented in AdultAlternateFileName's doc comment.
func TestAdultAlternateFileName_NoPhash_DoesNotMatchAdultSchema(t *testing.T) {
	name := AdultAlternateFileName("Studio", "Scene Title", "2024-01-02", "", "1080p", "h264", "8 Mbps", ".mkv")
	path := filepath.Join("/adult", name)
	if MatchesAdultSchema(path) {
		t.Errorf("alternate %q (no phash) should not match MatchesAdultSchema — AllScenePaths is the guard", path)
	}
}

// The result must never be byte-equal to AdultFileName for the same inputs,
// so the alternate cannot silently collide with the primary on disk.
func TestAdultAlternateFileName_NeverEqualToAdultFileName(t *testing.T) {
	primary := AdultFileName("Studio", "Scene Title", "2024-01-02", "abc123", ".mkv")
	alt := AdultAlternateFileName("Studio", "Scene Title", "2024-01-02", "abc123", "1080p", "h264", "8 Mbps", ".mkv")
	if primary == alt {
		t.Errorf("AdultAlternateFileName must not equal AdultFileName for the same inputs: %q", primary)
	}
	altNoTokens := AdultAlternateFileName("Studio", "Scene Title", "2024-01-02", "abc123", "", "", "", ".mkv")
	if primary == altNoTokens {
		t.Errorf("AdultAlternateFileName (no tokens) must not equal AdultFileName: %q", primary)
	}
}
