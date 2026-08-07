package rename

import "testing"

// TestEpisodeTitleMatches is the P1-P5 predicate table from the plan's §5.2
// — pure function tests, no fake TMDB server and no fixture data needed.
func TestEpisodeTitleMatches(t *testing.T) {
	cases := []struct {
		name        string
		basename    string
		showTitle   string
		episodeName string
		want        bool
	}{
		{
			// P1: an episode titled the same as its own show must NOT match
			// every file in that show's folder — the residual after
			// subtracting the show's own tokens is empty.
			name:        "P1 episode title equals show title — residual empty after show-token subtraction",
			basename:    "The Path S-less file.mkv",
			showTitle:   "The Path",
			episodeName: "The Path",
			want:        false,
		},
		{
			// P2: TMDB's un-titled-episode placeholder is rejected outright,
			// even though "episode" alone is a >=4-rune "strong" token that
			// would otherwise survive the residual/containment rule.
			name:        "P2 TMDB placeholder episode name rejected",
			basename:    "Red Skelton Episode 12.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "Episode 12",
			want:        false,
		},
		{
			// P3: the motivating shape — same strings as web_authority_test.go's
			// HasTitleTokenOverlap fixture. Directional containment (every
			// discriminating episode token appears in the file title) is what
			// makes this match despite the file title carrying more tokens
			// than the episode name (exact equality would reject this).
			name:        "P3 motivating shape — file title is a superset of the episode title",
			basename:    "Red Skelton More Funny Faces.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "More Funny Faces",
			want:        true,
		},
		{
			// P4: one shared token is enough for HasTitleTokenOverlap (step 1)
			// but the residual isn't fully CONTAINED in the file's own tokens
			// ("different" appears nowhere in the file title) — must reject.
			name:        "P4 one shared token, residual not contained — no match",
			basename:    "Red Skelton Something Else.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "Something Different",
			want:        false,
		},
		{
			// P5: the Red Skelton digit-code population (§0.2/§1.5's worked
			// trace) must decline to zero, not one wrong, matches — a digit
			// code shares no token with any real episode title.
			name:        "P5 digit-code basename shares no token with a real episode title",
			basename:    "Red Skelton e1524.mp4",
			showTitle:   "The Red Skelton Show",
			episodeName: "More Funny Faces",
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := episodeTitleMatches(tc.basename, tc.showTitle, tc.episodeName)
			if got != tc.want {
				t.Errorf("episodeTitleMatches(%q, %q, %q) = %v, want %v", tc.basename, tc.showTitle, tc.episodeName, got, tc.want)
			}
		})
	}
}

func TestIsPlaceholderEpisodeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"Episode N", "Episode 12", true},
		{"Ep. N", "Ep. 3", true},
		{"Chapter N", "Chapter 4", true},
		{"Part N", "Part 2", true},
		{"real title", "More Funny Faces", false},
		{"real title containing a digit", "Skelton's Scrapbook", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPlaceholderEpisodeName(tc.in); got != tc.want {
				t.Errorf("isPlaceholderEpisodeName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
