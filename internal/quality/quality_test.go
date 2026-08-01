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

func TestInferTier(t *testing.T) {
	// The full reverse-mapping contract. Note bluray's two rows: a bare
	// bluray is NOT Lossless (High's own source list contains bluray too, so
	// the source alone cannot discriminate a remux-quality rip from a
	// re-encode) — High is the deliberate conservative default there.
	cases := []struct {
		name     string
		info     release.Info
		wantTier Tier
		wantOK   bool
	}{
		{"remux is unambiguously lossless", release.Info{Source: "remux"}, Lossless, true},
		{"bluray+x265 reads as a lossless-intent encode", release.Info{Source: "bluray", Codec: "x265"}, Lossless, true},
		{"bare bluray is ambiguous and defaults to high", release.Info{Source: "bluray"}, High, true},
		{"bluray+x264 stays high and ignores resolution", release.Info{Source: "bluray", Codec: "x264", Resolution: 720}, High, true},
		{"webrip+x265 is low", release.Info{Source: "webrip", Codec: "x265"}, Low, true},
		{"webrip+x264 is medium", release.Info{Source: "webrip", Codec: "x264"}, Medium, true},
		{"hdtv+x265 is low", release.Info{Source: "hdtv", Codec: "x265"}, Low, true},
		{"hdtv without a codec is medium", release.Info{Source: "hdtv"}, Medium, true},
		{"web-dl is medium", release.Info{Source: "web-dl"}, Medium, true},
		{"web is medium", release.Info{Source: "web"}, Medium, true},
		{"dvdrip is low", release.Info{Source: "dvdrip"}, Low, true},
		{"an unrecognized source is not inferable", release.Info{}, "", false},
		{"resolution alone never infers a tier", release.Info{Resolution: 2160}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTier, gotOK := InferTier(tc.info)
			if gotOK != tc.wantOK {
				t.Fatalf("InferTier(%+v) ok = %v, want %v", tc.info, gotOK, tc.wantOK)
			}
			if gotOK && gotTier != tc.wantTier {
				t.Errorf("InferTier(%+v) = %q, want %q", tc.info, gotTier, tc.wantTier)
			}
		})
	}
}

func TestInferTierNeverConsultsResolution(t *testing.T) {
	// Tier and resolution are independent settings (see this package's doc
	// comment — conflating them was a mistake). Every resolution at a fixed
	// source/codec must infer the identical tier.
	for _, res := range []int{0, 480, 720, 1080, 2160} {
		for _, src := range []string{"remux", "bluray", "webrip", "hdtv", "web-dl", "web", "dvdrip"} {
			base, baseOK := InferTier(release.Info{Source: src})
			got, gotOK := InferTier(release.Info{Source: src, Resolution: res})
			if got != base || gotOK != baseOK {
				t.Errorf("source %q: resolution %d changed the inferred tier (%q,%v -> %q,%v)", src, res, base, baseOK, got, gotOK)
			}
		}
	}
}

func TestInferTierNeverReturnsHighForNonBlurayInput(t *testing.T) {
	// High is reachable from InferTier ONLY via the bluray+non-x265 path.
	// That is a deliberate property, not an oversight: High's own source list
	// is release.DefaultProfile().PreferredSources — the full unfiltered
	// default list, which discriminates nothing — so it is the conservative
	// fallback for the one genuinely ambiguous source, never a general
	// bucket. Pinned in a test rather than a comment so a future edit to the
	// mapping table cannot silently make High common (or unreachable).
	otherSources := []string{"remux", "webrip", "hdtv", "web-dl", "web", "dvdrip", ""}
	codecs := []string{"x264", "x265", ""}
	for _, src := range otherSources {
		for _, codec := range codecs {
			info := release.Info{Source: src, Codec: codec}
			if tier, ok := InferTier(info); ok && tier == High {
				t.Errorf("InferTier(%+v) returned High; High must be reachable only via bluray+non-x265", info)
			}
		}
	}

	// ...and the bluray path really does reach it, so the sweep above is
	// asserting a boundary rather than a vacuous truth.
	if tier, ok := InferTier(release.Info{Source: "bluray"}); !ok || tier != High {
		t.Fatalf("expected bare bluray to reach High, got (%q, %v)", tier, ok)
	}
}
