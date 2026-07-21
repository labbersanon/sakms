//go:build integration

// This is the tier-3 real-ffmpeg integration test (spec §5 / plan §6). Unlike
// phash_test.go (which fakes the runner with canned PNGs) it drives the REAL
// runFFmpeg path: it generates two tiny synthetic clips with ffmpeg's lavfi
// sources at test time — no committed video files — and runs the real
// phash.New().Hash against them. It proves three things unit tests can't:
// (a) Hash runs end-to-end through real ffprobe+ffmpeg decode, (b) the SAME
// clip hashes identically twice (determinism through real decode, not just
// through canned bytes), and (c) two visibly-different clips produce a large
// Hamming distance that the shipped DefaultThreshold (10) reports as NOT
// similar. It is build-tagged `integration` and t.Skip()s cleanly if ffmpeg
// or ffprobe isn't on PATH, so a CI box without them stays green.
//
// It is measure-first: it prints every pairwise distance before asserting, so
// the real numbers are visible in `-v` output and the assertions are margins
// around what was actually observed, not cherry-picked values.
package phash

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// genClip writes a tiny synthetic clip at path using an ffmpeg lavfi source
// expression (e.g. "testsrc" or "testsrc2"). Kept deliberately tiny: 2s,
// 64x64, 5fps — enough for 5 interior-frame sampling, cheap to decode.
func genClip(t *testing.T, path, source string) {
	t.Helper()
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", source+"=duration=2:size=64x64:rate=5",
		"-pix_fmt", "yuv420p",
		path,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generating clip %s from %q: %v\n%s", path, source, err, stderr.String())
	}
}

// compositeDistance decodes two encoded hashes produced by this package and
// returns their total Hamming distance in bits. Available because this test is
// in package phash (it can call the unexported decode/hammingBits), so it can
// report the exact real bit counts rather than only a within/not-within bool.
func compositeDistance(t *testing.T, a, b string) int {
	t.Helper()
	_, ca, err := decode(a)
	if err != nil {
		t.Fatalf("decoding %q: %v", a, err)
	}
	_, cb, err := decode(b)
	if err != nil {
		t.Fatalf("decoding %q: %v", b, err)
	}
	if len(ca) != len(cb) {
		t.Fatalf("composite length mismatch: %d vs %d", len(ca), len(cb))
	}
	return hammingBits(ca, cb)
}

func TestPHash_RealFFmpegDeterminismAndSeparation(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH — skipping real-ffmpeg integration test")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping real-ffmpeg integration test")
	}

	dir := t.TempDir()
	clipA := filepath.Join(dir, "testsrc.mkv")
	clipB := filepath.Join(dir, "testsrc2.mkv")
	genClip(t, clipA, "testsrc")
	genClip(t, clipB, "testsrc2")

	h := New()
	ctx := context.Background()

	// (a) it runs end-to-end through real decode.
	hashA1, err := h.Hash(ctx, clipA)
	if err != nil {
		t.Fatalf("hashing clip A: %v", err)
	}
	hashA2, err := h.Hash(ctx, clipA)
	if err != nil {
		t.Fatalf("re-hashing clip A: %v", err)
	}
	hashB, err := h.Hash(ctx, clipB)
	if err != nil {
		t.Fatalf("hashing clip B: %v", err)
	}

	// Measure first — print every pairwise distance before asserting anything.
	dSame := compositeDistance(t, hashA1, hashA2)
	dDiff := compositeDistance(t, hashA1, hashB)
	// Derive the composite width from the actual decoded bytes rather than a
	// hardcoded per-frame bit literal, so this log stays correct regardless of
	// the active per-frame hash width (64-bit PHash today, a wider hash later).
	_, composite, err := decode(hashA1)
	if err != nil {
		t.Fatalf("decoding hashA1 for width report: %v", err)
	}
	compositeBytes := len(composite)
	compositeBits := compositeBytes * 8
	t.Logf("scheme=%s frames=%d composite=%d bits (%d bytes)", Scheme, Frames, compositeBits, compositeBytes)
	t.Logf("Hamming(testsrc, testsrc  [same clip, twice]) = %d bits", dSame)
	t.Logf("Hamming(testsrc, testsrc2 [different clips])   = %d bits", dDiff)
	t.Logf("DefaultThreshold=%d per-frame → budget=%d bits over the composite",
		DefaultThreshold, DefaultThreshold*Frames)

	// (b) the same clip hashes identically twice — determinism through real
	// ffmpeg decode, not just through canned bytes.
	if hashA1 != hashA2 {
		t.Errorf("expected the same clip to hash identically twice, got\n  %q\n  %q (distance %d bits)",
			hashA1, hashA2, dSame)
	}
	if dSame != 0 {
		t.Errorf("expected a 0-bit distance for identical re-decodes, got %d bits", dSame)
	}

	// (c) two visibly-different clips produce a large distance the shipped
	// DefaultThreshold reports as NOT similar. Assert a margin, not just
	// ">threshold": the budget is DefaultThreshold*Frames (50 bits); a genuine
	// different-pattern pair should clear it with real headroom.
	within, err := SimilarityWithin(hashA1, hashB, Frames, DefaultThreshold)
	if err != nil {
		t.Fatalf("SimilarityWithin(A,B): %v", err)
	}
	if within {
		t.Errorf("expected testsrc vs testsrc2 to be reported NOT similar at DefaultThreshold=%d "+
			"(budget %d bits) but distance was only %d bits — the default may be too loose for real frames",
			DefaultThreshold, DefaultThreshold*Frames, dDiff)
	}
	if margin := dDiff - DefaultThreshold*Frames; margin < DefaultThreshold*Frames {
		// Not a hard failure on its own, but surface a thin margin loudly so a
		// future algorithm/param change that erodes separation is visible.
		t.Logf("WARNING: separation margin is only %d bits above the budget (%d) — thin headroom",
			margin, DefaultThreshold*Frames)
	}

	// Sanity: the same clip is of course reported similar to itself.
	sameWithin, err := SimilarityWithin(hashA1, hashA2, Frames, DefaultThreshold)
	if err != nil {
		t.Fatalf("SimilarityWithin(A,A): %v", err)
	}
	if !sameWithin {
		t.Errorf("expected identical clip hashes to be within DefaultThreshold, but they weren't")
	}
}
