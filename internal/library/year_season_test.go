package library

import (
	"path/filepath"
	"testing"
)

func TestParseYearSeasonNumbers(t *testing.T) {
	cases := []struct {
		name       string
		wantSeason int
		wantEps    []int
		wantOK     bool
	}{
		{"Looney.Tunes.S1958E14.Fistic.Mystic.mkv", 1958, []int{14}, true},
		{"Looney Tunes - 1958x01 - Title.mkv", 1958, []int{1}, true},
		{"Show.S1958E01-E02.mkv", 1958, []int{1, 2}, true},
		{"Show.S1928E01.mkv", 1928, []int{1}, true},
		{"Show.S1999E05.mkv", 1999, []int{5}, true},
		{"Show.S2000E01.mkv", 0, nil, false},
		{"Show.S2024E05.mkv", 0, nil, false},
		{"Show.S1927E01.mkv", 0, nil, false},
		{"Show.Name.S03E05.mkv", 0, nil, false},
		{"Movie Name (2020).mkv", 0, nil, false},
	}
	for _, c := range cases {
		season, eps, ok := ParseYearSeasonNumbers(c.name)
		if ok != c.wantOK || season != c.wantSeason || !intSlicesEqual(eps, c.wantEps) {
			t.Errorf("ParseYearSeasonNumbers(%q) = (%d, %v, %v), want (%d, %v, %v)",
				c.name, season, eps, ok, c.wantSeason, c.wantEps, c.wantOK)
		}
	}
}

func TestParseEpisodeNumbers_UnaffectedByYearSeason(t *testing.T) {
	if season, _, ok := ParseEpisodeNumbers("Looney.Tunes.S1958E14.mkv"); ok {
		t.Fatalf("ParseEpisodeNumbers must not accept S1958E14, got season %d", season)
	}
}

func TestYearSeasonFolder(t *testing.T) {
	if y, ok := YearSeasonFolder("1958"); !ok || y != 1958 {
		t.Fatalf("1958: got %d %v", y, ok)
	}
	if y, ok := YearSeasonFolder("Season 1958"); !ok || y != 1958 {
		t.Fatalf("Season 1958: got %d %v", y, ok)
	}
	if _, ok := YearSeasonFolder("Season 01"); ok {
		t.Fatal("Season 01 must not be a year-season folder")
	}
	if _, ok := YearSeasonFolder("Season 2005"); ok {
		t.Fatal("Season 2005 is after the SxxExx-standard cutoff")
	}
}

func TestParseEpisodeNumbersNested_ShortsTree(t *testing.T) {
	root := "/media/Series (Kids)"
	video := filepath.Join(root, "Looney Toons", "1958", "Disc 1", "E14 Fistic Mystic.mkv")
	season, eps, ok := ParseEpisodeNumbersNested(video, root)
	if !ok || season != 1958 || !intSlicesEqual(eps, []int{14}) {
		t.Fatalf("nested disc = (%d, %v, %v)", season, eps, ok)
	}
}

func TestParseEpisodeNumbersNested_DoesNotWalkSxxExxGrandparent(t *testing.T) {
	root := "/media/Series"
	video := filepath.Join(root, "The Path", "Season 1", "The.Path.S01E02.2160p.WEB.h265-NiXON", "subs", "hash.mp4")
	if _, _, ok := ParseEpisodeNumbersNested(video, root); ok {
		t.Fatal("must not take S01E02 from a grandparent through a non-skippable folder")
	}
}

func TestStripYearSeasonMarker(t *testing.T) {
	got := StripYearSeasonMarker("Looney.Tunes.S1958E14.Fistic.Mystic.mkv")
	if got != "Looney.Tunes" {
		t.Fatalf("got %q", got)
	}
}
