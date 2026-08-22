package vmaf

import "github.com/labbersanon/sakms/internal/phash"

// Claude 2026-08-21: VMAF runs only for a pair that already matched on phash.
// Reason: Dedup also groups by TMDB identity (crop vs open-matte Last Crusade
// is TMDB 89 at ~48% PDQ). VMAF is quality scoring after duplicate detection,
// not a substitute for it — ffmpeg on dissimilar pictures is wasted and
// misleading. Lives here so cmd/sakms/scanadapter.go can call it without
// tripping TestScanOnlyAllowlist (dedup function refs besides Scan* are banned).
// Troubleshooting: identity-only Dedup tiles still showing VMAF… means the
// on-demand handler or eagerVMAF is not calling this.
// Review if: Dedup stops grouping by TMDB identity, or VMAF is used as a
// duplicate detector (it must not be).

// ShouldScoreVMAF reports whether candidate vs reference is eligible for VMAF.
// A missing hash, or one that fails to decode or uses a different scheme, is
// not a match.
func ShouldScoreVMAF(candidatePHash, referencePHash string, perFrameThreshold int) bool {
	if candidatePHash == "" || referencePHash == "" {
		return false
	}
	within, err := phash.SimilarityWithin(candidatePHash, referencePHash, phash.Frames, perFrameThreshold)
	return err == nil && within
}
