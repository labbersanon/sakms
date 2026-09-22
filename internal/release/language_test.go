package release

import "testing"

func TestFindLanguageTags(t *testing.T) {
	cases := []struct {
		title string
		want  []string
	}{
		{"Some.Movie.2020.1080p.BluRay.x264-GROUP", nil},
		{"Some.Movie.2020.GERMAN.1080p-GROUP", []string{"german"}},
		{"Some.Movie.2020.MULTI.1080p-GROUP", nil},
		{"Some.Movie.2020.ENGLISH.GERMAN.1080p", []string{"english", "german"}},
		{"FrenchConnection.2020.1080p-GROUP", nil},
	}
	for _, tc := range cases {
		got := FindLanguageTags(tc.title)
		if len(got) != len(tc.want) {
			t.Errorf("FindLanguageTags(%q)=%v want %v", tc.title, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("FindLanguageTags(%q)=%v want %v", tc.title, got, tc.want)
				break
			}
		}
	}
}

func TestTitleLanguageAllowed(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		preferred []string
		want      bool
	}{
		{"unmarked empty pref", "Show.S01E01.720p.WEB-DL.x264-GROUP", nil, true},
		{"german empty pref", "Show.S01E01.GERMAN.720p.WEB-DL.x264-TVP", nil, false},
		{"multi empty pref", "Show.S01E01.MULTI.720p-GROUP", nil, true},
		{"english tag empty pref", "Show.S01E01.ENGLISH.720p-GROUP", nil, true},
		{"german preferred german", "Show.S01E01.GERMAN.DUBBED.720p-TVP", []string{"german"}, true},
		{"unmarked preferred german", "Show.S01E01.720p.WEB-DL.x264-GROUP", []string{"german"}, true},
		{"french preferred german", "Show.S01E01.FRENCH.720p-GROUP", []string{"german"}, false},
		{"german+french preferred german", "Show.S01E01.GERMAN.FRENCH.720p", []string{"german"}, true},
		{"english preferred german", "Show.S01E01.ENGLISH.720p-GROUP", []string{"german"}, false},
		{"vostfr empty pref", "Show.S01E01.VOSTFR.720p", nil, false},
		{"preferred case/space", "Show.GERMAN.720p", []string{" German "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TitleLanguageAllowed(tc.title, tc.preferred); got != tc.want {
				t.Fatalf("TitleLanguageAllowed(%q, %v)=%v want %v", tc.title, tc.preferred, got, tc.want)
			}
		})
	}
}
