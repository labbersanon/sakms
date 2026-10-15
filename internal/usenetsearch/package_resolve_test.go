package usenetsearch

import "testing"

func TestInferPackageFromGroups_EmptyIsDefault(t *testing.T) {
	got := InferPackageFromGroups("movies", nil, nil)
	if got != FormatPackageStep(DefaultPackageStep) {
		t.Fatalf("got %q", got)
	}
}

func TestInferPackageFromGroups_ExactStep(t *testing.T) {
	step2 := ResolveGroupsForStep("movies", 2)
	got := InferPackageFromGroups("movies", step2, step2)
	if got != "2" {
		t.Fatalf("got %q want 2", got)
	}
}

func TestInferPackageFromGroups_Custom(t *testing.T) {
	got := InferPackageFromGroups("movies", []string{"alt.binaries.moovee", "totally.fake.group"}, nil)
	if got != PackageCustom {
		t.Fatalf("got %q want custom", got)
	}
}

func TestResolvePackageGroups_IntersectsProvider(t *testing.T) {
	all := ResolveGroupsForStep("movies", 5)
	if len(all) < 2 {
		t.Fatal("need curated movies")
	}
	only := []string{all[0]}
	got := ResolvePackageGroups("movies", 5, only)
	if len(got) != 1 || got[0] != all[0] {
		t.Fatalf("got %+v", got)
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
