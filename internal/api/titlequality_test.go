package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/quality"
)

func TestQualityPrefsRaised(t *testing.T) {
	base := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, Floor: "medium", MinResolution: 0,
	}
	if qualityPrefsRaised(base, base) {
		t.Fatal("identical prefs must not count as raised")
	}
	higherFloor := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"high", "lossless"}, Floor: "high", MinResolution: 0,
	}
	if !qualityPrefsRaised(base, higherFloor) {
		t.Fatal("raising the quality floor must count as raised")
	}
	min1080 := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, Floor: "medium", MinResolution: 1080,
	}
	if !qualityPrefsRaised(base, min1080) {
		t.Fatal("raising min resolution must count as raised")
	}
}

func TestFileNeedsQualityUpgrade(t *testing.T) {
	floor := quality.High
	if !fileNeedsQualityUpgrade("Show.S01E01.480p.WEB.DL.x264-GRP.mkv", "medium", floor, 0) {
		t.Fatal("480p web-dl should need high-floor upgrade")
	}
	if !fileNeedsQualityUpgrade("Show.S01E01.720p.WEB.DL.x264-GRP.mkv", "medium", floor, 1080) {
		t.Fatal("720p should need upgrade when min resolution is 1080")
	}
	if fileNeedsQualityUpgrade("Show.S01E01.1080p.BluRay.REMUX.mkv", "medium", floor, 1080) {
		t.Fatal("1080p remux at/above floor must not upgrade")
	}
}

func TestRequireMinResolution(t *testing.T) {
	cands := []autograb.Candidate{
		{Title: "a", Resolution: 480},
		{Title: "b", Resolution: 1080},
		{Title: "c", Resolution: 2160},
		{Title: "d", Resolution: 0},
	}
	got := requireMinResolution(cands, 1080)
	if len(got) != 3 || got[0].Title != "b" || got[1].Title != "c" || got[2].Title != "d" {
		t.Fatalf("got %+v", got)
	}
	if len(requireMinResolution(cands, 0)) != 4 {
		t.Fatal("minRes 0 must keep full list")
	}
	// Hard floor: no fallback when everything is below.
	onlySD := []autograb.Candidate{{Title: "sd", Resolution: 480}}
	if len(requireMinResolution(onlySD, 1080)) != 0 {
		t.Fatal("hard min must not fall back to below-floor releases")
	}
}

func TestTitlePrefsTiersFromRequest_FloorExpandsUp(t *testing.T) {
	got := titlePrefsTiersFromRequest(apidto.TitleQualityPrefsRequest{Floor: "high"})
	if len(got) != 2 || got[0] != quality.High || got[1] != quality.Lossless {
		t.Fatalf("got %v", got)
	}
}
