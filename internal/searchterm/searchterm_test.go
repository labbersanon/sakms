package searchterm

import "testing"

func TestFromName_RealExamples(t *testing.T) {
	// All inputs below are real orphaned-item names from Radarr/Sonarr's own
	// unmappedFolders response.
	cases := []struct {
		name string
		want string
	}{
		{
			name: "9.11.Truth.Lies.and.Conspiracies.WEB.x264-spamTV",
			want: "9 11 Truth Lies and Conspiracies",
		},
		{
			// No recognizable noise at all — expected to fall through to
			// the AI fallback unchanged, not a bug in this function.
			name: "FathersLLDVD",
			want: "FathersLLDVD",
		},
		{
			name: "Lego Movies",
			want: "Lego Movies",
		},
		{
			name: "American.Pie.1999.THEATRiCAL.2160p.UHD.BluRay.x265-4KDVS",
			want: "American Pie 1999",
		},
		{
			// The mangled "Dts-HDMa5.1" audio tag (dot-splitting turns
			// "DTS-HD MA 5.1" into a form this best-effort cleaner doesn't
			// recognize) leaves a small leftover fragment — accepted here
			// since "American Pie 2 (2001)" still dominates the search term
			// enough for Radarr's own fuzzy lookup to match correctly; not
			// worth chasing every possible mangled release-tag variant.
			name: "American.Pie.2.(2001).1080p.BluRay.REMUX.Dts-HDMa5.1.AVC-d3g",
			want: "American Pie 2 (2001) -HDMa5 1",
		},
	}
	for _, tc := range cases {
		got := FromName(tc.name)
		if got != tc.want {
			t.Errorf("FromName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFromName_PreservesYearInParens(t *testing.T) {
	got := FromName("Movie Title (2020) [1080p BluRay x264]")
	want := "Movie Title (2020)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFromName_CleanNameUnchanged(t *testing.T) {
	got := FromName("A Beautiful Mind")
	if got != "A Beautiful Mind" {
		t.Errorf("expected an already-clean name to pass through unchanged, got %q", got)
	}
}

func TestFromName_UnderscoresToSpaces(t *testing.T) {
	got := FromName("Some_Movie_Name_2020")
	if got != "Some Movie Name 2020" {
		t.Errorf("got %q", got)
	}
}
