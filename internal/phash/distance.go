package phash

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// DefaultThreshold is the starting per-frame average Hamming distance under
// which two composite hashes are treated as the same content. It is a
// STARTING DEFAULT and an algorithm-sanity regression guard — NOT a
// real-world-validated constant. It is exposed as a per-mode tunable
// (GET/PUT /api/modes/{mode}/phash-threshold); real-world confidence comes
// from the build-tagged integration test and the manual live walkthrough
// against actual movie files, not from this value being provably correct on
// arbitrary movie frames (see calibrate_test.go's doc comment).
const DefaultThreshold = 10

// DefaultMoviesThreshold is the factory default for Movies mode's phash-primary
// Dedup scan. More permissive than DefaultThreshold (25 vs 10) because there
// is no within-show shared-intro false-positive risk for Movies. Corresponds
// to approximately 60% similarity over 5 × 64-bit frames.
const DefaultMoviesThreshold = 25

// encode returns the DB/candidate-JSON storage form of a composite hash:
// "<scheme>:<hex>", e.g. "phash64/5f:1a2b...". The scheme tag makes a hash
// self-describing, so a value cached under an OLD algorithm or frame count is
// detectably incomparable to a freshly computed one.
func encode(scheme string, composite []byte) string {
	return scheme + ":" + hex.EncodeToString(composite)
}

// decode splits an encoded hash back into its scheme tag and raw composite
// bytes. Returns an error for a malformed string (no scheme separator or a
// non-hex payload).
func decode(s string) (scheme string, composite []byte, err error) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", nil, fmt.Errorf("phash: malformed encoded hash %q (no scheme separator)", s)
	}
	composite, err = hex.DecodeString(s[i+1:])
	if err != nil {
		return "", nil, fmt.Errorf("phash: decoding hash payload of %q: %w", s, err)
	}
	return s[:i], composite, nil
}

// SimilarityScore returns the normalised similarity of a and b as a value in
// [0.0, 1.0]: 1.0 means bit-for-bit identical, 0.0 means maximally dissimilar.
// It returns (0, nil) — NOT an error — for scheme/length mismatches (same
// stale-entry safety as SimilarityWithin). The composite byte length from the
// decoded hash is the authoritative bit count (len(composite)*8), so the score
// is correct regardless of per-frame hash width — 64-bit PHash or a wider hash.
// frames is retained for signature/caller compatibility and no longer bounds
// the denominator.
func SimilarityScore(a, b string, frames int) (float64, error) {
	schemeA, compositeA, err := decode(a)
	if err != nil {
		return 0, err
	}
	schemeB, compositeB, err := decode(b)
	if err != nil {
		return 0, err
	}
	if schemeA != schemeB || len(compositeA) != len(compositeB) {
		return 0, nil
	}
	totalBits := len(compositeA) * 8
	if totalBits <= 0 {
		return 0, nil
	}
	return 1.0 - float64(hammingBits(compositeA, compositeB))/float64(totalBits), nil
}

// SimilarityWithin reports whether a and b are within perFrameThreshold average
// Hamming bits per frame. It returns (false, nil) — NOT an error — when the two
// hashes have different schemes or unequal lengths, so a stale-scheme cache
// entry can never wrongly assert similarity; it returns an error only when a
// value is structurally undecodable. Expressing the threshold as a per-frame
// average keeps the tunable a clean 0–64 number independent of frame count.
func SimilarityWithin(a, b string, frames, perFrameThreshold int) (bool, error) {
	schemeA, compositeA, err := decode(a)
	if err != nil {
		return false, err
	}
	schemeB, compositeB, err := decode(b)
	if err != nil {
		return false, err
	}
	if schemeA != schemeB || len(compositeA) != len(compositeB) {
		return false, nil
	}
	return hammingBits(compositeA, compositeB) <= perFrameThreshold*frames, nil
}
