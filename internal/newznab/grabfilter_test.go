package newznab

import "testing"

func TestFilterGrabReleases_TokenMatchPrefixDupExact(t *testing.T) {
	rels := []Release{
		{Title: "Unrelated.Show.2020.1080p.WEB-DL.x264-X", Size: 1, DownloadURL: "http://a/nzb"},
		{Title: "Grab.Movie.2021.720p.WEB-DL.x264-GRP", Size: 10_000_000, DownloadURL: "http://b/nzb"},
		{Title: "Grab.Movie.2021.720p.WEB-DL.x264-GRP", Size: 10_100_000, DownloadURL: "http://c/nzb"},
		{Title: "Grab.Movie", Size: 9_000_000, DownloadURL: "http://d/nzb"},
	}
	got := FilterGrabReleases("Grab Movie", rels)
	if len(got) != 2 {
		t.Fatalf("got=%+v want exact Grab.Movie plus one 720p (dup dropped, unrelated dropped)", got)
	}
	if got[0].Title != "Grab.Movie" {
		t.Fatalf("exact title first: %+v", got)
	}
	if got[1].Title != "Grab.Movie.2021.720p.WEB-DL.x264-GRP" {
		t.Fatalf("prefix match second: %+v", got)
	}
}

func TestFilterGrabReleases_ExcludeTerm(t *testing.T) {
	rels := []Release{
		{Title: "Show.S01E01.GERMAN.720p.HDTV.x264-X", Size: 1, DownloadURL: "http://a/nzb"},
		{Title: "Show.S01E01.720p.HDTV.x264-GRP", Size: 2, DownloadURL: "http://b/nzb"},
	}
	got := FilterGrabReleases("Show S01E01 -GERMAN", rels)
	if len(got) != 1 || got[0].Title != "Show.S01E01.720p.HDTV.x264-GRP" {
		t.Fatalf("got=%+v", got)
	}
}

func TestFilterGrabReleases_Prefix1080(t *testing.T) {
	rels := []Release{
		{Title: "Movie.2021.1080p.BluRay.x264-GRP", Size: 1, DownloadURL: "http://a/nzb"},
		{Title: "Movie.2021.720p.BluRay.x264-GRP", Size: 2, DownloadURL: "http://b/nzb"},
	}
	got := FilterGrabReleases("Movie 2021 1080", rels)
	if len(got) != 1 || got[0].Title != "Movie.2021.1080p.BluRay.x264-GRP" {
		t.Fatalf("1080 must prefix-match 1080p: %+v", got)
	}
}

func TestFilterGrabReleases_FallbackWhenNothingMatches(t *testing.T) {
	rels := []Release{
		{Title: "Totally.Other.2020", Size: 1, DownloadURL: "http://a/nzb"},
		{Title: "Totally.Other.2020", Size: 1, DownloadURL: "http://b/nzb"},
	}
	got := FilterGrabReleases("Wanted Movie", rels)
	if len(got) != 1 {
		t.Fatalf("fallback still dedups: %+v", got)
	}
}

func TestSanitizeGrabQuery(t *testing.T) {
	if got := sanitizeGrabQuery("Grab Movie 2021"); got != "grab.movie.2021" {
		t.Fatalf("got %q", got)
	}
}
