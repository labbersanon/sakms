package usenetsearch

import "testing"

func TestInferPackageFromGroups_EmptyIsDefault(t *testing.T) {
	got := InferPackageFromGroups("movies", nil, nil)
	if got != FormatPackageStep(DefaultPackageStep) {
		t.Fatalf("got %q", got)
	}
}

func TestInferPackageFromGroups_FullSetIsDefaultTolerance(t *testing.T) {
	full := ResolveAllModeGroups("movies")
	got := InferPackageFromGroups("movies", full, full)
	if got != FormatPackageStep(DefaultPackageStep) {
		t.Fatalf("got %q want default tolerance", got)
	}
}

func TestInferPackageFromGroups_PartialIsCustom(t *testing.T) {
	step2 := ResolveGroupsForStep("movies", 2)
	full := ResolveAllModeGroups("movies")
	if sameGroupSet(step2, full) {
		t.Fatal("need step2 ⊂ full for this assertion")
	}
	got := InferPackageFromGroups("movies", step2, nil)
	if got != PackageCustom {
		t.Fatalf("partial curated set should be custom, got %q", got)
	}
}

func TestInferPackageFromGroups_Custom(t *testing.T) {
	got := InferPackageFromGroups("movies", []string{"alt.binaries.moovee", "totally.fake.group"}, nil)
	if got != PackageCustom {
		t.Fatalf("got %q want custom", got)
	}
}

func TestResolveModeGroups_IntersectsProvider(t *testing.T) {
	all := ResolveAllModeGroups("movies")
	if len(all) < 2 {
		t.Fatal("need curated movies")
	}
	only := []string{all[0]}
	got := ResolveModeGroups("movies", only)
	if len(got) != 1 || got[0] != all[0] {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveModeGroups_UnfilteredIsFullCuratedSet(t *testing.T) {
	got := ResolveModeGroups("movies", nil)
	full := ResolveGroupsForStep("movies", PackageStepMax)
	if !sameGroupSet(got, full) {
		t.Fatalf("membership must be full curated set: got=%d full=%d", len(got), len(full))
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
