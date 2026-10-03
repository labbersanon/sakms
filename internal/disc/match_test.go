package disc

import "testing"

func TestMatchUniqueSlots_OneObviousDuration(t *testing.T) {
	got := MatchUniqueSlots(
		map[string]float64{"t02": 433, "t03": 1200},
		[]CatalogEpisode{
			{Season: 1, Episode: 4, Title: "Short", RuntimeMin: 7},
			{Season: 1, Episode: 5, Title: "Long", RuntimeMin: 22},
		},
	)
	if len(got) != 1 || got["t02"].Episode != 4 || got["t02"].Title != "Short" {
		t.Fatalf("got %+v, want t02 → S01E04 Short", got)
	}
}

func TestMatchUniqueSlots_SharedRuntimeIsNotUnique(t *testing.T) {
	got := MatchUniqueSlots(
		map[string]float64{"t02": 433, "t03": 420},
		[]CatalogEpisode{
			{Season: 1, Episode: 1, Title: "A", RuntimeMin: 7},
			{Season: 1, Episode: 2, Title: "B", RuntimeMin: 7},
		},
	)
	if len(got) != 0 {
		t.Fatalf("shared 7m runtime must not assign, got %+v", got)
	}
}

func TestMatchUniqueSlots_TwoWorksOneEpisode(t *testing.T) {
	got := MatchUniqueSlots(
		map[string]float64{"t02": 433, "t03": 410},
		[]CatalogEpisode{
			{Season: 1, Episode: 1, Title: "Only", RuntimeMin: 7},
		},
	)
	if len(got) != 0 {
		t.Fatalf("two works claiming one episode must not assign, got %+v", got)
	}
}

func TestMatchUniqueSlots_ZeroRuntimeIgnored(t *testing.T) {
	got := MatchUniqueSlots(
		map[string]float64{"t01": 5400},
		[]CatalogEpisode{
			{Season: 1, Episode: 1, Title: "Unknown", RuntimeMin: 0},
		},
	)
	if len(got) != 0 {
		t.Fatalf("runtime 0 must not match, got %+v", got)
	}
}

func TestMatchUniqueSlots_DoesNotPairByCount(t *testing.T) {
	got := MatchUniqueSlots(
		map[string]float64{"t02": 400, "t03": 410, "t04": 420},
		[]CatalogEpisode{
			{Season: 1, Episode: 1, RuntimeMin: 7},
			{Season: 1, Episode: 2, RuntimeMin: 7},
			{Season: 1, Episode: 3, RuntimeMin: 7},
		},
	)
	if len(got) != 0 {
		t.Fatalf("1:1 count must not assign, got %+v", got)
	}
}

func TestMatchUniqueTitles_NamesShorts(t *testing.T) {
	got := MatchUniqueTitles(
		map[string]string{"t02": "14 Carrot Rabbit", "t03": "Ali Baba Bunny"},
		[]CatalogEpisode{
			{Season: 1, Episode: 12, Title: "Ali Baba Bunny", RuntimeMin: 7},
			{Season: 1, Episode: 4, Title: "14 Carrot Rabbit", RuntimeMin: 7},
			{Season: 1, Episode: 5, Title: "Other", RuntimeMin: 7},
		},
	)
	if got["t02"].Episode != 4 || got["t03"].Episode != 12 {
		t.Fatalf("got %+v", got)
	}
}

func TestUniqueNamedHit_OneExactTitle(t *testing.T) {
	idx, ok := UniqueNamedHit("The Daffy Doc", []string{
		"Looney Tunes Golden Collection, Vol. 5",
		"The Daffy Doc",
		"Other",
	})
	if !ok || idx != 1 {
		t.Fatalf("idx=%d ok=%v, want 1 true", idx, ok)
	}
}

func TestUniqueNamedHit_RemakesAreNotUnique(t *testing.T) {
	_, ok := UniqueNamedHit("Duck Soup", []string{"Duck Soup", "Duck Soup"})
	if ok {
		t.Fatal("two same-title remakes must not pick a year")
	}
}

func TestMergeCatalogByTitle_OverrideWins(t *testing.T) {
	got := MergeCatalogByTitle(
		[]CatalogEpisode{{Season: 1, Episode: 12, Title: "The Daffy Doc", RuntimeMin: 7}},
		[]CatalogEpisode{{Season: 1938, Episode: 36, Title: "The Daffy Doc", RuntimeMin: 8}},
	)
	if len(got) != 1 || got[0].Season != 1938 || got[0].Episode != 36 {
		t.Fatalf("got %+v, want TVDB year-season", got)
	}
}

func TestMatchUniqueTitles_DuplicateCatalogTitleDropped(t *testing.T) {
	got := MatchUniqueTitles(
		map[string]string{"t02": "Short"},
		[]CatalogEpisode{
			{Season: 1, Episode: 1, Title: "Short"},
			{Season: 1, Episode: 2, Title: "Short"},
		},
	)
	if len(got) != 0 {
		t.Fatalf("duplicate catalog title must not assign, got %+v", got)
	}
}
