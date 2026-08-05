package quality

import (
	"testing"

	"github.com/labbersanon/sakms/internal/mediainfo"
)

func TestTierFromProbe_ResolutionLadder(t *testing.T) {
	cases := []struct {
		name string
		p    *mediainfo.Probe
		want Tier
	}{
		{"nil", nil, ""},
		{"empty", &mediainfo.Probe{}, ""},
		{"4k hevc high bitrate", &mediainfo.Probe{Height: 2160, CodecName: "hevc", BitRate: 40_000_000}, Lossless},
		{"4k h264", &mediainfo.Probe{Height: 2160, CodecName: "h264", BitRate: 20_000_000}, High},
		{"1080 high bitrate", &mediainfo.Probe{Height: 1080, CodecName: "h264", BitRate: 15_000_000}, High},
		{"1080 typical", &mediainfo.Probe{Height: 1080, CodecName: "h264", BitRate: 5_000_000}, Medium},
		{"720", &mediainfo.Probe{Height: 720, CodecName: "h264", BitRate: 2_000_000}, Medium},
		{"480", &mediainfo.Probe{Height: 480, CodecName: "mpeg4", BitRate: 1_000_000}, Low},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TierFromProbe(tc.p)
			if got != tc.want {
				t.Errorf("TierFromProbe = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRank_Order(t *testing.T) {
	if Rank(Lossless) <= Rank(High) || Rank(High) <= Rank(Medium) || Rank(Medium) <= Rank(Low) || Rank(Low) <= Rank("") {
		t.Fatal("expected lossless > high > medium > low > unknown")
	}
}
