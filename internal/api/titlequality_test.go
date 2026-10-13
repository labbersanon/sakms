package api

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
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
	sameRes := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, Floor: "medium", MinResolution: 0,
		ResolutionFloors: map[string]string{"480": "low", "720": "medium", "1080": "medium", "2160": "medium"},
	}
	raised480 := apidto.TitleQualityPrefsResponse{
		Tiers: []string{"medium", "high", "lossless"}, Floor: "medium", MinResolution: 0,
		ResolutionFloors: map[string]string{"480": "medium", "720": "medium", "1080": "medium", "2160": "medium"},
	}
	if qualityPrefsRaised(sameRes, sameRes) {
		t.Fatal("identical per-rung floors must not count as raised")
	}
	if !qualityPrefsRaised(sameRes, raised480) {
		t.Fatal("raising the 480p floor must count as raised")
	}
}

func TestResolveAutoGrabFloorsForTitle_OverridePerRung(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	if err := settingsStore.Set(ctx, qualityResolutionFloorsKey(mode.Series), `{"480":"medium","720":"medium","1080":"medium","2160":"medium"}`); err != nil {
		t.Fatal(err)
	}
	inherited := resolveAutoGrabFloorsForTitle(ctx, libStore, settingsStore, mode.Series, 333432)
	if inherited[480] != quality.Medium || inherited[1080] != quality.Medium {
		t.Fatalf("inherited floors=%v, want medium on 480 and 1080", inherited)
	}

	floors := map[int]quality.Tier{480: quality.Low, 720: quality.Medium, 1080: quality.Medium, 2160: quality.Medium}
	if err := libStore.SetTitleQualityPrefs(ctx, library.TitleQualityPrefs{
		Mode: mode.Series, TMDBID: 333432, Tiers: quality.TiersAtOrAbove(quality.Low),
		MinResolutionSet: true, ResolutionFloors: floors,
	}); err != nil {
		t.Fatal(err)
	}
	got := resolveAutoGrabFloorsForTitle(ctx, libStore, settingsStore, mode.Series, 333432)
	if got[480] != quality.Low || got[1080] != quality.Medium {
		t.Fatalf("override floors=%v, want 480=low 1080=medium", got)
	}
	accepted := resolveAutoGrabAcceptedByResolution(ctx, libStore, settingsStore, mode.Series, 333432)
	if quality.Lowest(accepted[480]) != quality.Low {
		t.Fatalf("480 accepted=%v, want Low as floor", accepted[480])
	}
	if quality.Lowest(accepted[1080]) != quality.Medium {
		t.Fatalf("1080 accepted=%v, want Medium as floor", accepted[1080])
	}
}

func TestFileNeedsQualityUpgrade(t *testing.T) {
	floor := quality.High
	if !fileNeedsQualityUpgrade("Show.S01E01.480p.WEB.DL.x264-GRP.mkv", "medium", floor, 0, false) {
		t.Fatal("480p web-dl should need high-floor upgrade")
	}
	if !fileNeedsQualityUpgrade("Show.S01E01.720p.WEB.DL.x264-GRP.mkv", "medium", floor, 1080, false) {
		t.Fatal("720p should need upgrade when min resolution is 1080")
	}
	if fileNeedsQualityUpgrade("Show.S01E01.1080p.BluRay.REMUX.mkv", "medium", floor, 1080, false) {
		t.Fatal("1080p remux at/above floor must not upgrade")
	}
	if !fileNeedsQualityUpgrade("Movie.mkv", "high", floor, 1080, false) {
		t.Fatal("unknown height with min 1080 must need upgrade")
	}
	if fileNeedsQualityUpgrade("Movie.mkv", "high", floor, 0, false) {
		t.Fatal("unknown height without a min resolution must not force upgrade")
	}
	if !fileNeedsQualityUpgrade("Show.S01E01.1080p.BluRay.REMUX.mkv", "medium", floor, 1080, true) {
		t.Fatal("SDR remux should need upgrade when feature-upgrade is on")
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
