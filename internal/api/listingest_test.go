package api

import "testing"

func TestParseSettingLines(t *testing.T) {
	got := parseSettingLines("123\n# comment\n  456, 789\n")
	want := []string{"123", "456", "789"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseTMDBListID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"12345", "12345"},
		{"https://www.themoviedb.org/list/7075775-favorites", "7075775"},
		{"https://www.themoviedb.org/list/10?language=en", "10"},
		{"not-a-list", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseTMDBListID(tc.in); got != tc.want {
			t.Errorf("parseTMDBListID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseIMDbListRef(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ls123456789", "ls123456789"},
		{"https://www.imdb.com/list/ls000123456/", "ls000123456"},
		{"ur987654321", "ur987654321"},
		{"https://www.imdb.com/user/ur1234567/watchlist", "ur1234567"},
		{"nope", ""},
	}
	for _, tc := range cases {
		if got := parseIMDbListRef(tc.in); got != tc.want {
			t.Errorf("parseIMDbListRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractIMDbTitleID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://www.imdb.com/title/tt0111161/", "tt0111161"},
		{"tt0944947", "tt0944947"},
		{"see tt12 later tt1234567", "tt1234567"},
		{"nothing", ""},
	}
	for _, tc := range cases {
		if got := extractIMDbTitleID(tc.in); got != tc.want {
			t.Errorf("extractIMDbTitleID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIMDbRSSURL(t *testing.T) {
	if got := imdbRSSURL("ls123"); got != "https://rss.imdb.com/list/ls123/" {
		t.Fatalf("list url = %q", got)
	}
	if got := imdbRSSURL("ur123"); got != "https://rss.imdb.com/user/ur123/watchlist" {
		t.Fatalf("watchlist url = %q", got)
	}
}
