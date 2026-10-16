package usenetsearch

import "testing"

func TestInferPackageFromGroups_EmptyIsDefault(t *testing.T) {
	got := InferPackageFromGroups("movies", nil, nil)
	if got != FormatPackageStep(DefaultPackageStep) {
		t.Fatalf("got %q", got)
	}
}

func TestInferPackageFromGroups_FullListIsDefaultTolerance(t *testing.T) {
	avail := []string{"alt.binaries.moovee", "alt.binaries.movies", "alt.binaries.movies.erotica"}
	got := InferPackageFromGroups("movies", avail, avail)
	if got != FormatPackageStep(DefaultPackageStep) {
		t.Fatalf("got %q want default tolerance", got)
	}
}

func TestInferPackageFromGroups_SeedShortlistIsCustom(t *testing.T) {
	seeds := ResolveAllModeGroups("movies")
	avail := append(append([]string{}, seeds...), "alt.binaries.movies.erotica")
	got := InferPackageFromGroups("movies", seeds, avail)
	if got != PackageCustom {
		t.Fatalf("packages.json shortlist vs full LIST should be custom, got %q", got)
	}
}

func TestInferPackageFromGroups_Custom(t *testing.T) {
	got := InferPackageFromGroups("movies", []string{"alt.binaries.moovee", "totally.fake.group"}, nil)
	if got != PackageCustom {
		t.Fatalf("got %q want custom", got)
	}
}

func TestResolveModeGroups_UsesFullListActive(t *testing.T) {
	avail := []string{"alt.binaries.movies.erotica", "alt.binaries.moovee", "alt.binaries.inner-sanctum"}
	got := ResolveModeGroups("movies", avail)
	if !sameGroupSet(got, avail) {
		t.Fatalf("membership must be the LIST set, got %+v", got)
	}
}

func TestResolveModeGroups_EmptyFallsBackToSeeds(t *testing.T) {
	got := ResolveModeGroups("movies", nil)
	full := ResolveGroupsForStep("movies", PackageStepMax)
	if !sameGroupSet(got, full) {
		t.Fatalf("empty LIST falls back to seeds: got=%d seeds=%d", len(got), len(full))
	}
}

func TestParsePackageValue(t *testing.T) {
	step, custom, ok := ParsePackageValue("3")
	if !ok || custom || step != 3 {
		t.Fatalf("3: %v %v %v", step, custom, ok)
	}
	_, custom, ok = ParsePackageValue("custom")
	if !ok || !custom {
		t.Fatal("custom")
	}
	_, _, ok = ParsePackageValue("nope")
	if ok {
		t.Fatal("nope should fail")
	}
}
