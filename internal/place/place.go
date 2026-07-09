// Package place holds pure decision logic for comparing two candidate copies
// of the same movie/episode and deciding which is the better quality — the
// comparison Dedup's compare card surfaces instead of leaving it silent —
// plus UniquePath, the filename-collision logic Rename's Kids relocation
// uses when physically moving a file into a different root folder.
package place

import (
	"fmt"
	"path/filepath"
	"strings"
)

// QualityKey ranks two copies of the same content so a dedupe pass can decide
// which one to keep. Compared most-significant-field-first: Resolution, then
// SourceRank, then a modern-codec preference, then BitRate as a final
// tiebreak.
//
// SourceRank is best-effort: for a file already tracked by Sonarr/Radarr, it
// could in principle come from their own reported quality.quality.source
// ranking, but SAK doesn't fetch that (see internal/dedup's doc comment
// for why) — every candidate, tracked or not, gets its Resolution/Codec/
// BitRate from SAK's own ffprobe read of the real file, and SourceRank
// stays 0 (unknown) for all of them today. The field exists so a future pass
// that does wire up the authoritative source ranking doesn't need to touch
// this comparison logic at all.
type QualityKey struct {
	Resolution int   // pixel height; higher is better
	SourceRank int   // 0 = unknown; higher is better
	IsAV1      bool  // AV1 preferred over other codecs at equal resolution/source
	BitRate    int64 // final tiebreak
}

// Greater reports whether k should be kept over other in a dedupe decision.
func (k QualityKey) Greater(other QualityKey) bool {
	if k.Resolution != other.Resolution {
		return k.Resolution > other.Resolution
	}
	if k.SourceRank != other.SourceRank {
		return k.SourceRank > other.SourceRank
	}
	if k.IsAV1 != other.IsAV1 {
		return k.IsAV1
	}
	return k.BitRate > other.BitRate
}

// NewQualityKey builds a QualityKey from raw probed info. codec is matched
// case-insensitively.
func NewQualityKey(resolution, sourceRank int, codec string, bitRate int64) QualityKey {
	return QualityKey{
		Resolution: resolution,
		SourceRank: sourceRank,
		IsAV1:      strings.EqualFold(codec, "av1"),
		BitRate:    bitRate,
	}
}

// Exists reports whether a path is already occupied — the caller supplies
// this (rather than UniquePath calling os.Stat itself) so it stays testable
// without a real filesystem.
type Exists func(path string) bool

// UniquePath returns target if it's free, or the first "target.N<ext>"
// variant (N starting at 2) that is — the same collision-avoidance a
// physical file move needs when its destination folder might already
// contain something with the same name.
func UniquePath(target string, exists Exists) (string, error) {
	if !exists(target) {
		return target, nil
	}
	ext := filepath.Ext(target)
	stem := strings.TrimSuffix(target, ext)
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s.%d%s", stem, i, ext)
		if !exists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a unique path for %s", target)
}
