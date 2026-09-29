package library

import "testing"

func TestParseEpisodeAirDate(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"The.Daily.Show.2024.03.15.720p.WEB.mkv", "2024-03-15", true},
		{"The Daily Show 2024-03-15.mkv", "2024-03-15", true},
		{"Show.2024-3-5.mkv", "2024-03-05", true},
		{"Show.2024.mkv", "", false},
		{"Show.S01E05.mkv", "", false},
		{"Show.2024.13.40.mkv", "", false},
	}
	for _, c := range cases {
		got, ok := ParseEpisodeAirDate(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseEpisodeAirDate(%q) = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestParseAbsoluteEpisode(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"One.Piece.1089.1080p.WEB.mkv", 1089, true},
		{"Show - 157.mkv", 157, true},
		{"Show - 3.mkv", 3, true},
		{"Show - 12 [1080p].mkv", 12, true},
		{"Show.ep12.mkv", 12, true},
		{"Show.S01E05.mkv", 0, false},
		{"The.Daily.Show.2024.03.15.mkv", 0, false},
		{"Show.2024.1080p.mkv", 0, false},
		{"Show.2160p.mkv", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseAbsoluteEpisode(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseAbsoluteEpisode(%q) = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestParseEpisodeNumbers_UnaffectedByDailyAbsolute(t *testing.T) {
	if _, _, ok := ParseEpisodeNumbers("The.Daily.Show.2024.03.15.mkv"); ok {
		t.Fatal("ParseEpisodeNumbers must stay false for a date-only daily name")
	}
	if _, _, ok := ParseEpisodeNumbers("One.Piece.1089.1080p.WEB.mkv"); ok {
		t.Fatal("ParseEpisodeNumbers must stay false for an absolute anime name")
	}
}

func TestStripDailyOrAbsolute(t *testing.T) {
	if got := StripDailyOrAbsolute("The.Daily.Show.2024.03.15.720p.mkv"); got != "The.Daily.Show" {
		t.Fatalf("strip date = %q", got)
	}
	if got := StripDailyOrAbsolute("One.Piece.1089.1080p.mkv"); got != "One.Piece" {
		t.Fatalf("strip abs = %q", got)
	}
}
