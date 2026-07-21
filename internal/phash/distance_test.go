package phash

import (
	"math"
	"testing"
)

// TestSimilarityScore_WidthAgnostic proves SimilarityScore derives its
// denominator from the composite's actual byte length (len*8), not a hardcoded
// 64-bit-per-frame assumption, so it is correct at BOTH today's 8-byte/frame
// PHash width and a future 32-byte/frame (256-bit) width — without the function
// knowing which algorithm produced the bytes.
//
// The two sub-cases do different jobs:
//   - 8-byte/frame is the EQUIVALENCE check: with 5 frames the byte-length
//     denominator (40*8 = 320) equals the old frames*64 (5*64 = 320), so the
//     new formula must return the same answer the old code did.
//   - 32-byte/frame is the DISCRIMINATION check: with 5 frames the byte-length
//     denominator (160*8 = 1280) differs from the old frames*64 (320). A
//     correct assertion here fails under the old formula and passes under the
//     new one — which is exactly what proves the fix.
func TestSimilarityScore_WidthAgnostic(t *testing.T) {
	const diffBits = 4 // one 0x0F byte flips exactly 4 bits

	// build returns a scheme-tagged composite of nBytes zero bytes, plus a copy
	// with diffBits bits flipped in the first byte (0x0F).
	build := func(nBytes int) (a, b string) {
		base := make([]byte, nBytes)
		flipped := make([]byte, nBytes)
		flipped[0] = 0x0F
		return encode(Scheme, base), encode(Scheme, flipped)
	}

	approxEqual := func(got, want float64) bool {
		return math.Abs(got-want) < 1e-9
	}

	cases := []struct {
		name          string
		bytesPerFrame int
	}{
		{"8-byte/frame (today's PHash width)", 8},
		{"32-byte/frame (future PDQ width)", 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nBytes := tc.bytesPerFrame * Frames
			a, b := build(nBytes)

			got, err := SimilarityScore(a, b, Frames)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Authoritative denominator: the composite's real bit count.
			wantDenom := float64(nBytes * 8)
			want := 1.0 - float64(diffBits)/wantDenom
			if !approxEqual(got, want) {
				t.Errorf("SimilarityScore = %v, want %v (denominator %d bits)", got, want, nBytes*8)
			}

			// The old hardcoded formula's denominator, for contrast.
			oldDenom := float64(Frames * 64)
			oldScore := 1.0 - float64(diffBits)/oldDenom
			if tc.bytesPerFrame == 8 {
				// Equivalence: new and old denominators coincide at 8 bytes/frame.
				if !approxEqual(want, oldScore) {
					t.Fatalf("test setup: expected 8-byte/frame case to match old formula, want %v old %v", want, oldScore)
				}
			} else {
				// Discrimination: new score must NOT equal what frames*64 gives.
				if approxEqual(got, oldScore) {
					t.Errorf("32-byte/frame score %v matched the old frames*64 result %v — "+
						"the width-agnostic fix is not in effect", got, oldScore)
				}
			}
		})
	}
}

func TestSimilarityWithin_IdenticalCompositesWithinAnyThreshold(t *testing.T) {
	a := encode(Scheme, []byte{0x00, 0xff, 0x0f, 0xaa, 0x55})
	within, err := SimilarityWithin(a, a, 5, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !within {
		t.Error("expected identical composites to be within even a zero threshold")
	}
}

func TestSimilarityWithin_OneBitDiffWithinLooseThreshold(t *testing.T) {
	base := []byte{0x00, 0x00, 0x00, 0x00, 0x00}
	flipped := []byte{0x01, 0x00, 0x00, 0x00, 0x00} // exactly one bit differs
	a := encode(Scheme, base)
	b := encode(Scheme, flipped)

	// perFrameThreshold 1 over 5 frames = a budget of 5 Hamming bits total;
	// one differing bit is comfortably within it.
	within, err := SimilarityWithin(a, b, 5, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !within {
		t.Error("expected a one-bit difference to be within a loose threshold")
	}

	// A zero budget must reject even a single differing bit.
	within, err = SimilarityWithin(a, b, 5, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if within {
		t.Error("expected a one-bit difference to be rejected by a zero threshold")
	}
}

func TestSimilarityWithin_SchemeMismatchIsFalseNotError(t *testing.T) {
	comp := []byte{0x12, 0x34, 0x56, 0x78, 0x9a}
	a := encode(Scheme, comp)
	b := encode("otherscheme/5f", comp)

	within, err := SimilarityWithin(a, b, 5, 64)
	if err != nil {
		t.Fatalf("expected no error on a scheme mismatch, got %v", err)
	}
	if within {
		t.Error("expected a scheme mismatch to report not-within (a stale-scheme cache entry must never assert similarity)")
	}
}

func TestSimilarityWithin_UnequalLengthIsFalseNotError(t *testing.T) {
	a := encode(Scheme, []byte{0x00, 0x00, 0x00, 0x00, 0x00})
	b := encode(Scheme, []byte{0x00}) // shorter composite

	within, err := SimilarityWithin(a, b, 5, 64)
	if err != nil {
		t.Fatalf("expected no error on unequal length, got %v", err)
	}
	if within {
		t.Error("expected unequal-length composites to report not-within")
	}
}

func TestSimilarityWithin_UndecodableInputErrors(t *testing.T) {
	valid := encode(Scheme, []byte{0x00})
	if _, err := SimilarityWithin("no-scheme-separator", valid, 5, 64); err == nil {
		t.Error("expected an error for a structurally undecodable hash")
	}
	if _, err := SimilarityWithin(valid, Scheme+":nothexZZ", 5, 64); err == nil {
		t.Error("expected an error for a non-hex payload")
	}
}
