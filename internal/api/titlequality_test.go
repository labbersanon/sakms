package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/quality"
)

func TestQualityPrefsRaised(t *testing.T) {
	base := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, MaxResolution: 1080,
	}
	if qualityPrefsRaised(base, base) {
		t.Fatal("identical prefs must not count as raised")
	}
	higherFloor := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"high", "lossless"}, MaxResolution: 1080,
	}
	if !qualityPrefsRaised(base, higherFloor) {
		t.Fatal("raising the floor (drop medium) must count as raised")
	}
	addTop := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high"}, MaxResolution: 1080,
	}
	withLossless := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, MaxResolution: 1080,
	}
	if !qualityPrefsRaised(addTop, withLossless) {
		t.Fatal("adding a higher tier must count as raised")
	}
	uncap := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, MaxResolution: 0,
	}
	if !qualityPrefsRaised(base, uncap) {
		t.Fatal("lifting a finite max-resolution to no-cap must count as raised")
	}
}

func TestFileNeedsQualityUpgrade(t *testing.T) {
	target := quality.Lossless
	if !fileNeedsQualityUpgrade("Show.S01E01.480p.WEB.DL.x264-GRP.mkv", "medium", target) {
		t.Fatal("480p web-dl should need lossless upgrade")
	}
	if fileNeedsQualityUpgrade("Show.S01E01.1080p.BluRay.REMUX.mkv", "medium", target) {
		t.Fatal("remux inferred as lossless must not upgrade")
	}
}

func TestSoftPreferMaxResolution(t *testing.T) {
	cands := []autograb.Candidate{
		{Title: "a", Resolution: 2160},
		{Title: "b", Resolution: 1080},
		{Title: "c", Resolution: 0},
	}
	got := softPreferMaxResolution(cands, 1080)
	if len(got) != 2 || got[0].Title != "b" || got[1].Title != "c" {
		t.Fatalf("got %+v", got)
	}
	if softPreferMaxResolution(cands, 0)[0].Title != "a" {
		t.Fatal("maxRes 0 must keep full list")
	}
}
