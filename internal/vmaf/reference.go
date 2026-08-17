package vmaf

import (
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Claude 2026-08-17: VMAF comparison reference is the largest on-disk file,
// not the Keep/Apply Winner (place.QualityKey).
// Reason: deep-interview-dedup-vmaf-schedule — keep protocol stays quality-
// ranked; VMAF scores everyone else against the biggest file. Lives here
// rather than internal/dedup so cmd/sakms/scanadapter.go can call it without
// tripping TestScanOnlyAllowlist (dedup function refs besides Scan* are banned).
// Troubleshooting: if Keep radio changes VMAF scores, the UI is still sending
// primaryOf as referenceIndex. If eager VMAF still uses Winner, scanadapter
// eagerVMAF is not calling this.
// Review if: VMAF reference is changed back to Winner / keep-primary.

// ReferenceIndex returns the candidate index VMAF should score against.
// Size prefers the stored Candidate.Size; a missing/zero size is filled from
// library.FileSize(path). Ties keep the lowest index (strict >). If every
// resolved size is 0, falls back to the Winner flag. Returns -1 when the
// slice is empty or there is no Winner to fall back to (caller skips).
func ReferenceIndex(candidates []proposals.Candidate) int {
	if len(candidates) == 0 {
		return -1
	}
	best := 0
	var bestSize int64
	for i, c := range candidates {
		sz := c.Size
		if sz <= 0 {
			sz = library.FileSize(c.Path)
		}
		if sz > bestSize {
			best, bestSize = i, sz
		}
	}
	if bestSize > 0 {
		return best
	}
	for i, c := range candidates {
		if c.Winner {
			return i
		}
	}
	return -1
}
