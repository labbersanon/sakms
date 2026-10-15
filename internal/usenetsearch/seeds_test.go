package usenetsearch

import "testing"

func TestLoadRecommendedSeeds_Valid(t *testing.T) {
	all, err := loadRecommendedSeeds()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 10 {
		t.Fatalf("got %d seeds, want a curated set", len(all))
	}
	movies := RecommendedForMode("movies")
	series := RecommendedForMode("series")
	adult := RecommendedForMode("adult")
	if len(movies) == 0 || len(series) == 0 || len(adult) == 0 {
		t.Fatalf("movies=%d series=%d adult=%d", len(movies), len(series), len(adult))
	}
	for _, s := range movies {
		if s.Mode != "movies" {
			t.Fatalf("cross-mode leak: %+v", s)
		}
	}
}

func TestFilterSeedsToProvider(t *testing.T) {
	seeds := []RecommendedSeed{
		{Mode: "movies", Name: "alt.binaries.movies", Tier: SeedTierCore},
		{Mode: "movies", Name: "alt.binaries.moovee", Tier: SeedTierCore},
	}
	got := FilterSeedsToProvider(seeds, []string{"alt.binaries.movies", "alt.binaries.teevee"})
	if len(got) != 1 || got[0].Name != "alt.binaries.movies" {
		t.Fatalf("got %+v", got)
	}
}
