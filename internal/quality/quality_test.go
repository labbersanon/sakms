package quality

import (
	"reflect"
	"testing"

	"github.com/labbersanon/sakms/internal/release"
)

func TestProfileFor_High_NoCap_MatchesDefaultProfile(t *testing.T) {
	if got, want := ProfileFor(High, 0), release.DefaultProfile(); !reflect.DeepEqual(got, want) {
		t.Errorf("expected High with no resolution cap to match release.DefaultProfile() exactly (no behavior change for existing installs), got %+v, want %+v", got, want)
	}
}

func TestProfileFor_UnrecognizedTierFallsBackToHigh(t *testing.T) {
	got := ProfileFor(Tier("bogus"), 0)
	want := ProfileFor(High, 0)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected an unrecognized tier to fall back to High, got %+v, want %+v", got, want)
	}
}

func TestProfileFor_TierNeverAffectsResolutionOrdering(t *testing.T) {
	// The whole point of the redesign: tier is a source/codec (bitrate)
	// preference, never a resolution one — every tier at the same
	// maxResolution must produce the identical PreferredResolutions.
	tiers := []Tier{Low, Medium, High, Lossless}
	for _, maxRes := range []int{0, 2160, 1080, 720, 480} {
		var first []int
		for i, tier := range tiers {
			p := ProfileFor(tier, maxRes)
			if i == 0 {
				first = p.PreferredResolutions
				continue
			}
			if !reflect.DeepEqual(p.PreferredResolutions, first) {
				t.Errorf("maxRes=%d: tier %q produced different resolutions (%v) than tier %q (%v)", maxRes, tier, p.PreferredResolutions, tiers[0], first)
			}
		}
	}
}

func TestProfileFor_MaxResolutionCapsAndOrdersDescending(t *testing.T) {
	cases := []struct {
		maxRes int
		want   []int
	}{
		{1080, []int{1080, 720, 480}},
		{720, []int{720, 480}},
		{480, []int{480}},
		{2160, []int{2160, 1080, 720, 480}},
	}
	for _, tc := range cases {
		got := ProfileFor(High, tc.maxRes).PreferredResolutions
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("maxRes=%d: got %v, want %v", tc.maxRes, got, tc.want)
		}
	}
}

func TestProfileFor_ZeroMaxResolutionPrefers1080Over2160(t *testing.T) {
	got := ProfileFor(High, 0).PreferredResolutions
	if len(got) == 0 || got[0] != 1080 {
		t.Errorf("expected the zero-config default to prefer 1080p first, got %v", got)
	}
}

func TestProfileFor_LosslessPrefersRemuxAndHasNoCodecPreference(t *testing.T) {
	p := ProfileFor(Lossless, 0)
	if len(p.PreferredSources) == 0 || p.PreferredSources[0] != "remux" {
		t.Errorf("expected Lossless to prefer remux first, got %v", p.PreferredSources)
	}
	if p.PreferredCodecs != nil {
		t.Errorf("expected Lossless to express no codec preference, got %v", p.PreferredCodecs)
	}
}

func TestProfileFor_LowPrefersSmallerSourcesAndEfficientCodec(t *testing.T) {
	p := ProfileFor(Low, 0)
	if len(p.PreferredSources) == 0 || p.PreferredSources[0] != "webrip" {
		t.Errorf("expected Low to prefer webrip first, got %v", p.PreferredSources)
	}
	if len(p.PreferredCodecs) == 0 || p.PreferredCodecs[0] != "x265" {
		t.Errorf("expected Low to prefer the more efficient x265 codec, got %v", p.PreferredCodecs)
	}
}

func TestProfileFor_LowVsLosslessScoreDifferentlyAtSameResolution(t *testing.T) {
	// The scenario the redesign exists for: two releases at the SAME
	// resolution (so PreferredResolutions can't distinguish them), one a
	// small WEBRip, one an uncompressed remux — Low should prefer the
	// WEBRip, Lossless should prefer the remux, even though neither tier's
	// resolution preference differs at all.
	webrip := release.Info{Resolution: 1080, Source: "webrip", Codec: "x265"}
	remux := release.Info{Resolution: 1080, Source: "remux"}

	lowPrefs := ProfileFor(Low, 1080)
	if release.Score(webrip, lowPrefs) <= release.Score(remux, lowPrefs) {
		t.Error("expected Low to prefer the smaller WEBRip release over the remux")
	}

	losslessPrefs := ProfileFor(Lossless, 1080)
	if release.Score(remux, losslessPrefs) <= release.Score(webrip, losslessPrefs) {
		t.Error("expected Lossless to prefer the remux release over the WEBRip")
	}
}
