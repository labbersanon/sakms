package phash

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// DefaultThreshold is the Series-mode per-frame Hamming distance (out of 256
// PDQ bits per frame) under which two composite hashes are treated as the same
// content. It is a STARTING DEFAULT and an algorithm-sanity regression guard —
// NOT a real-world-validated constant. It is exposed as a per-mode tunable
// (GET/PUT /api/modes/{mode}/phash-threshold); real-world confidence comes
// from the build-tagged integration test and the manual live walkthrough
// against actual movie files, not from this value being provably correct on
// arbitrary movie frames (see calibrate_test.go's doc comment).
//
// Calibrated for PDQ (Stage 4): the calibrate_test harness measured a
// perturbed-duplicate max of 12 and a distinct-content min of 98 per-frame
// Hamming bits. 40 sits inside that [12, 98] gap with wide margin (28 bits
// clear of the duplicate class, 58 clear of the distinct class), held at the
// conservative Series hypothesis because Series carries a within-show
// shared-intro/opening-credits false-positive risk that Movies does not, so it
// stays the stricter (less permissive) of the two defaults.
const DefaultThreshold = 40

// DefaultMoviesThreshold is the factory default for Movies mode's phash-primary
// Dedup scan. More permissive than DefaultThreshold (64 vs 40) because there
// is no within-show shared-intro false-positive risk for Movies.
//
// Calibrated for PDQ (Stage 4): with the harness's distinct-content min at 98
// per-frame Hamming bits, the naive linear-scale Movies point (100, from the
// old 25/64) is REJECTED — 100 >= 98 would place the cut at or above where
// genuinely distinct content begins, risking a destructive false merge. 64 is
// the shipped value: more permissive than Series (40), yet still 34 bits clear
// below the measured distinct-content floor and 52 bits above the
// perturbed-duplicate max (12).
const DefaultMoviesThreshold = 64

// encode returns the DB/candidate-JSON storage form of a composite hash:
// "<scheme>:<hex>", e.g. "pdq256/5f:1a2b...". The scheme tag makes a hash
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
// average keeps the tunable a clean 0–PerFrameBits number (0–256 for PDQ)
// independent of frame count.
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
