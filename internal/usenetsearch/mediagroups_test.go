package usenetsearch

import (
	"testing"
)

func TestMediaWildmats_KnownModes(t *testing.T) {
	for _, mode := range []string{"movies", "series", "adult", "Movies"} {
		if got := MediaWildmats(mode); len(got) == 0 {
			t.Fatalf("MediaWildmats(%q) empty", mode)
		}
	}
	if got := MediaWildmats("other"); got != nil {
		t.Fatalf("want nil for unknown mode, got %v", got)
	}
}

func TestIsMediaGroup(t *testing.T) {
	if !IsMediaGroup("alt.binaries.movies") {
		t.Fatal("movies should pass")
	}
	if IsMediaGroup("control.cancel") {
		t.Fatal("control should fail")
	}
	if IsMediaGroup("") {
		t.Fatal("empty should fail")
	}
}

func TestMergeGroups_Dedupes(t *testing.T) {
	got := MergeGroups(
		[]string{"a", "b"},
		[]string{"b", "c"},
		[]string{"", "a"},
	)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestParseGroups_AndFormatRoundTrip(t *testing.T) {
	raw := "alt.binaries.movies\nalt.binaries.tv, alt.binaries.movies"
	got := ParseGroups(raw)
	if len(got) != 2 || got[0] != "alt.binaries.movies" || got[1] != "alt.binaries.tv" {
		t.Fatalf("ParseGroups = %v", got)
	}
	if FormatGroups(got) != "alt.binaries.movies\nalt.binaries.tv" {
		t.Fatalf("FormatGroups = %q", FormatGroups(got))
	}
}
