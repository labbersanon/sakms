package usenetsearch

import "testing"

func TestLoadPackageSeeds_Valid(t *testing.T) {
	all, err := loadPackageSeeds()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 15 {
		t.Fatalf("got %d seeds, want a curated set", len(all))
	}
	for _, mode := range []string{"movies", "series", "adult"} {
		got := PackagesForMode(mode)
		if len(got) == 0 {
			t.Fatalf("%s: empty packages", mode)
		}
		steps := map[int]bool{}
		for _, g := range got {
			if g.Mode != mode {
				t.Fatalf("cross-mode leak: %+v", g)
			}
			steps[g.Step] = true
		}
		for s := PackageStepMin; s <= PackageStepMax; s++ {
			if !steps[s] {
				t.Fatalf("%s: missing step %d", mode, s)
			}
		}
	}
}

func TestResolveGroupsForStep_Cumulative(t *testing.T) {
	s1 := ResolveGroupsForStep("movies", 1)
	s3 := ResolveGroupsForStep("movies", 3)
	s5 := ResolveGroupsForStep("movies", 5)
	if len(s1) == 0 || len(s3) <= len(s1) || len(s5) <= len(s3) {
		t.Fatalf("want cumulative growth: 1=%d 3=%d 5=%d", len(s1), len(s3), len(s5))
	}
	have := map[string]bool{}
	for _, n := range s3 {
		have[n] = true
	}
	for _, n := range s1 {
		if !have[n] {
			t.Fatalf("step 3 missing step-1 group %s", n)
		}
	}
}

func TestFilterPackagesToProvider(t *testing.T) {
	seeds := []PackageSeed{
		{Mode: "movies", Name: "alt.binaries.movies", Step: 1},
		{Mode: "movies", Name: "alt.binaries.moovee", Step: 1},
	}
	got := FilterPackagesToProvider(seeds, []string{"alt.binaries.movies", "alt.binaries.teevee"})
	if len(got) != 1 || got[0].Name != "alt.binaries.movies" {
		t.Fatalf("got %+v", got)
	}
}

func TestClampPackageStep(t *testing.T) {
	if ClampPackageStep(0) != DefaultPackageStep {
		t.Fatal("0 should default")
	}
	if ClampPackageStep(9) != PackageStepMax {
		t.Fatal("9 should clamp to max")
	}
}

func TestRecommendedForMode_ShimSteps12(t *testing.T) {
	rec := RecommendedForMode("series")
	if len(rec) == 0 {
		t.Fatal("empty shim")
	}
	byName := PackageSeedByName("series")
	for _, r := range rec {
		ps, ok := byName[r.Name]
		if !ok || ps.Step > 2 {
			t.Fatalf("shim leaked step>2: %+v", r)
		}
	}
}
