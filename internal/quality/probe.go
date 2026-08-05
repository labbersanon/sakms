package quality

import (
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/mediainfo"
)

// Claude 2026-08-05: TierFromProbe / Rank / labels for Rename alternate promote
// Reason: deep-interview-rename-alts-ai-fallback — probed tier ladder, not Settings preference alone
// Troubleshooting: promote/demote wrong when only settings tier was used
// Review if: InferTier from release.Info becomes usable for already-on-disk library files

// TierFromProbe maps ffprobe width/height/codec/bitrate onto the same Tier
// vocabulary Settings uses. Source tokens (bluray/web-dl) are unavailable on
// disk, so this is resolution+bitrate+codec heuristics — documented in tests.
func TierFromProbe(p *mediainfo.Probe) Tier {
	if p == nil || (p.Width == 0 && p.Height == 0 && p.BitRate == 0) {
		return ""
	}
	h := p.Height
	if h == 0 {
		h = p.Width // rare portrait / odd container
	}
	codec := strings.ToLower(p.CodecName)
	hevc := strings.Contains(codec, "hevc") || strings.Contains(codec, "h265") || codec == "265"
	br := p.BitRate
	switch {
	case h >= 2160 && hevc && br >= 25_000_000:
		return Lossless
	case h >= 2160:
		return High
	case h >= 1080 && br >= 12_000_000:
		return High
	case h >= 1080:
		return Medium
	case h >= 720:
		return Medium
	case h > 0:
		return Low
	default:
		return ""
	}
}

// Rank orders tiers for promote/demote. Higher wins. Empty/unknown is 0.
func Rank(t Tier) int {
	switch t {
	case Lossless:
		return 4
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	default:
		return 0
	}
}

// RankString is Rank for a stored quality_tier string.
func RankString(s string) int {
	return Rank(Tier(s))
}

// ResolutionLabel formats height as a common release token (2160p/1080p/…).
func ResolutionLabel(height int) string {
	switch {
	case height >= 2160:
		return "2160p"
	case height >= 1440:
		return "1440p"
	case height >= 1080:
		return "1080p"
	case height >= 720:
		return "720p"
	case height >= 480:
		return "480p"
	case height > 0:
		return fmt.Sprintf("%dp", height)
	default:
		return ""
	}
}

// BitrateLabel formats bits/sec as e.g. "5.2Mbps" for alternate filenames.
func BitrateLabel(bitRate int64) string {
	if bitRate <= 0 {
		return ""
	}
	mbps := float64(bitRate) / 1_000_000
	if mbps >= 10 {
		return fmt.Sprintf("%.0fMbps", mbps)
	}
	return fmt.Sprintf("%.1fMbps", mbps)
}

// CodecLabel normalizes ffprobe codec_name for display/filenames.
func CodecLabel(codec string) string {
	c := strings.ToLower(strings.TrimSpace(codec))
	switch c {
	case "hevc", "h265", "265":
		return "HEVC"
	case "h264", "avc", "avc1":
		return "H264"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "":
		return ""
	default:
		return strings.ToUpper(c)
	}
}
