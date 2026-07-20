package autograb

import (
	"math"
	"testing"

	"github.com/labbersanon/sakms/internal/quality"
)

// mbps builds a SizeBytes that yields the given raw implied bitrate (Mbps) for
// runtimeSeconds, so fixtures can be written in terms of the bitrate they mean
// to exercise rather than opaque byte counts. impliedMbps = bytes*8/rt/1e6, so
// bytes = mbps*1e6*rt/8.
func bytesForMbps(mbps, runtimeSeconds float64) int64 {
	return int64(mbps * 1e6 * runtimeSeconds / 8)
}

const testRuntime = 6000.0 // 100 min, a plausible feature length

func approx(a, b float64) bool { return math.Abs(a-b) < 0.05 }

func TestImpliedBitrate(t *testing.T) {
	// 5 GB over 6000 s = 5e9*8/6000 = 6.667 Mbps.
	g := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: 5_000_000_000, RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "x264",
	}, quality.High, 0)
	if !g.BitrateKnown {
		t.Fatal("expected bitrate to be known")
	}
	want := 5_000_000_000.0 * 8 / testRuntime / 1e6
	if !approx(g.ImpliedMbps, want) {
		t.Fatalf("implied bitrate = %.4f, want %.4f", g.ImpliedMbps, want)
	}
}

// TestCodecNormalizationDirection is the single most important test: the
// x264-equivalent must go UP for a more efficient codec (equiv = implied /
// multiplier), and each fixture is chosen so ONLY the correct direction
// produces the asserted qualify/reject outcome — an inverted formula fails.
func TestCodecNormalizationDirection(t *testing.T) {
	// High/1080p floor is 10 Mbps. An x265 release at 5 Mbps implied →
	// 5/0.5 = 10 Mbps x264-equivalent. AV1 is exempt from padding, non-AV1
	// isn't, so use x265 with a raw bitrate that survives the ×0.8 padding:
	// pick 6.4 Mbps implied → equiv 12.8 → padded 10.24 ≥ 10 → qualifies.
	// An inverted formula would give 6.4*0.5 = 3.2, nowhere near 10 → reject.
	g := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(6.4, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "x265",
	}, quality.High, 0)
	if !approx(g.X264EquivMbps, 12.8) {
		t.Fatalf("x265 6.4 Mbps → x264-equiv = %.4f, want 12.8 (inverted formula would give ~3.2)", g.X264EquivMbps)
	}
	if !g.Qualified {
		t.Fatalf("x265 at 12.8 Mbps x264-equiv should clear the 10 Mbps High/1080p floor; status=%s score=%.3f", g.Status, g.Score)
	}

	// AV1 multiplier 0.35: 3.5 Mbps implied → 10 Mbps x264-equivalent.
	gav1 := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(3.5, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "av1",
	}, quality.High, 0)
	if !approx(gav1.X264EquivMbps, 10.0) {
		t.Fatalf("av1 3.5 Mbps → x264-equiv = %.4f, want 10.0", gav1.X264EquivMbps)
	}

	// Unknown codec falls back to x264 baseline (1.0): no efficiency credit.
	gunk := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(8, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "xvid",
	}, quality.High, 0)
	if !approx(gunk.X264EquivMbps, 8.0) {
		t.Fatalf("unknown codec → x264-equiv = %.4f, want 8.0 (baseline 1.0)", gunk.X264EquivMbps)
	}
}

// TestNonAV1Padding proves the 25% padding is codec-conditional: a non-AV1
// release that clears the raw floor but fails after ×0.8 is rejected, while an
// AV1 release at the identical x264-equivalent bitrate qualifies.
func TestNonAV1Padding(t *testing.T) {
	// Target 11 Mbps x264-equivalent, just over the 10 Mbps High/1080p floor.
	// Non-AV1 (x264, mult 1.0): padded = 11/1.25 = 8.8 < 10 → below floor.
	x264 := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(11, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "x264",
	}, quality.High, 0)
	if !approx(x264.Score, 8.8) {
		t.Fatalf("x264 padded score = %.4f, want 8.8", x264.Score)
	}
	if x264.Qualified {
		t.Fatalf("x264 at 11 Mbps equiv should FAIL after 25%% padding (8.8 < 10); status=%s", x264.Status)
	}
	if x264.Status != StatusBelowFloor {
		t.Fatalf("x264 padded-out status = %s, want %s", x264.Status, StatusBelowFloor)
	}

	// AV1 at the same 11 Mbps x264-equivalent: no padding, 11 ≥ 10 → qualifies.
	// av1 equiv = implied/0.35, so implied 3.85 → equiv 11.
	av1 := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(3.85, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "av1",
	}, quality.High, 0)
	if !approx(av1.Score, 11.0) {
		t.Fatalf("av1 score = %.4f, want 11.0 (no padding)", av1.Score)
	}
	if !av1.Qualified {
		t.Fatalf("av1 at 11 Mbps equiv should clear the 10 Mbps floor unpadded; status=%s", av1.Status)
	}
}

func TestFloorTableIndependentAxes(t *testing.T) {
	// Spot-check the exact plan numbers via qualify/reject at the boundary.
	// Medium/2160p floor is 20 Mbps. AV1 (no padding) at exactly 20 qualifies.
	at20 := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(20*0.35, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 2160, Codec: "av1",
	}, quality.Medium, 0)
	if !at20.Qualified || !approx(at20.FloorMbps, 20) {
		t.Fatalf("Medium/2160p: qualified=%v floor=%.1f, want true/20", at20.Qualified, at20.FloorMbps)
	}
	// Just under: 19.9 Mbps → below floor.
	under := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(19.9*0.35, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 2160, Codec: "av1",
	}, quality.Medium, 0)
	if under.Qualified {
		t.Fatalf("Medium/2160p at 19.9 Mbps should be below floor; status=%s", under.Status)
	}
}

func TestLosslessSourceFlagBypass(t *testing.T) {
	// Lossless/1080p floor is 18 Mbps. A remux at a modest 6 Mbps would fail
	// on bitrate but qualifies on the source flag alone. Keep it above the
	// mislabel line (0.4×2 = 0.8 Mbps for 1080p Low floor) — 6 clears that.
	g := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(6, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "x264", Source: "remux",
	}, quality.Lossless, 0)
	if !g.Qualified {
		t.Fatalf("Lossless remux should qualify on the source flag alone; status=%s", g.Status)
	}
	// Same release WITHOUT the remux/bluray flag, still under 18 → below floor.
	noFlag := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(6, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "x264", Source: "web-dl",
	}, quality.Lossless, 0)
	if noFlag.Qualified {
		t.Fatalf("Lossless web-dl at 6 Mbps should be below the 18 Mbps floor; status=%s", noFlag.Status)
	}
}

func TestMinSeederFloorRejection(t *testing.T) {
	base := Candidate{
		Protocol: "torrent", SizeBytes: bytesForMbps(20, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "av1", // high quality so only seeders can fail it
	}
	// 3 seeders < default 5 → rejected as low-seeders even though quality is great.
	base.Seeders = 3
	low := GradeCandidate(base, quality.High, 0)
	if low.Qualified || low.Status != StatusLowSeeders {
		t.Fatalf("3-seeder torrent: qualified=%v status=%s, want false/%s", low.Qualified, low.Status, StatusLowSeeders)
	}
	// 5 seeders == floor → qualifies.
	base.Seeders = 5
	ok := GradeCandidate(base, quality.High, 0)
	if !ok.Qualified {
		t.Fatalf("5-seeder torrent should meet the floor; status=%s", ok.Status)
	}
	// Tunable floor: raise to 50, a 10-seeder torrent now fails.
	base.Seeders = 10
	tuned := GradeCandidate(base, quality.High, 50)
	if tuned.Status != StatusLowSeeders {
		t.Fatalf("10 seeders vs tunable floor 50: status=%s, want %s", tuned.Status, StatusLowSeeders)
	}
	// Usenet never gets seeder-floored regardless of Seeders==0.
	usenet := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(20, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 1080, Codec: "av1", Seeders: 0,
	}, quality.High, 0)
	if !usenet.Qualified {
		t.Fatalf("usenet should skip the seeder floor; status=%s", usenet.Status)
	}
}

func TestPreGrabMislabelRejection(t *testing.T) {
	// A "2160p" release carrying a 480p-typical ~1 Mbps bitrate. Low/2160p
	// floor is 8 Mbps; mislabel line is 0.4×8 = 3.2 Mbps x264-equiv. x264 at
	// 1 Mbps implied → 1 Mbps equiv < 3.2 → mislabeled.
	g := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(1, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 2160, Codec: "x264",
	}, quality.High, 0)
	if g.Status != StatusMislabeled {
		t.Fatalf("2160p at 1 Mbps should be mislabeled; status=%s equiv=%.3f", g.Status, g.X264EquivMbps)
	}
	if g.Qualified {
		t.Fatal("a mislabeled candidate must not auto-qualify")
	}
}

func TestUnknownInputNeutral(t *testing.T) {
	// Size==0 → unknown bitrate, neutral: NOT mislabeled, NOT qualified.
	zeroSize := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: 0, RuntimeSeconds: testRuntime,
		Resolution: 2160, Codec: "x264",
	}, quality.High, 0)
	if zeroSize.Status != StatusUnknownBitrate {
		t.Fatalf("Size==0 status=%s, want %s", zeroSize.Status, StatusUnknownBitrate)
	}
	if zeroSize.Status == StatusMislabeled || zeroSize.Qualified {
		t.Fatal("unknown bitrate must be neutral — never mislabeled, never qualified")
	}

	// runtime==0 → same neutral outcome (and no divide-by-zero panic).
	zeroRuntime := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: 5_000_000_000, RuntimeSeconds: 0,
		Resolution: 1080, Codec: "x264",
	}, quality.High, 0)
	if zeroRuntime.Status != StatusUnknownBitrate || zeroRuntime.Qualified {
		t.Fatalf("runtime==0 status=%s qualified=%v, want %s/false", zeroRuntime.Status, zeroRuntime.Qualified, StatusUnknownBitrate)
	}
	if zeroRuntime.ImpliedMbps != 0 {
		t.Fatalf("unknown-runtime implied bitrate = %.3f, want 0", zeroRuntime.ImpliedMbps)
	}

	// Bitrate known but resolution unrecognized (e.g. 540p) → also neutral.
	oddRes := GradeCandidate(Candidate{
		Protocol: "usenet", SizeBytes: bytesForMbps(5, testRuntime), RuntimeSeconds: testRuntime,
		Resolution: 540, Codec: "x264",
	}, quality.High, 0)
	if oddRes.Status != StatusUnknownResolution || oddRes.Qualified {
		t.Fatalf("unknown resolution status=%s qualified=%v, want %s/false", oddRes.Status, oddRes.Qualified, StatusUnknownResolution)
	}
}

func TestSelectPicksHighestQualified(t *testing.T) {
	cands := []Candidate{
		// [0] below floor (x264 at 11 → padded 8.8 < 10).
		{Title: "low", Protocol: "usenet", SizeBytes: bytesForMbps(11, testRuntime), RuntimeSeconds: testRuntime, Resolution: 1080, Codec: "x264"},
		// [1] qualified av1 at 15 Mbps equiv.
		{Title: "good", Protocol: "usenet", SizeBytes: bytesForMbps(15*0.35, testRuntime), RuntimeSeconds: testRuntime, Resolution: 1080, Codec: "av1"},
		// [2] qualified av1 at 25 Mbps equiv — the best.
		{Title: "best", Protocol: "usenet", SizeBytes: bytesForMbps(25*0.35, testRuntime), RuntimeSeconds: testRuntime, Resolution: 1080, Codec: "av1"},
	}
	sel := Select(cands, quality.High, 0)
	if sel.Fallback {
		t.Fatal("expected a qualified pick, got fallback")
	}
	if sel.PickIndex != 2 {
		t.Fatalf("PickIndex = %d, want 2 (highest score)", sel.PickIndex)
	}
	// Ranked lists best score first.
	if sel.Ranked[0] != 2 || sel.Ranked[len(sel.Ranked)-1] != 0 {
		t.Fatalf("Ranked = %v, want best (2) first and below-floor (0) last", sel.Ranked)
	}
}

// TestMinScoreFloorFallback proves the fallback-to-manual-pick-list signal:
// when nothing clears the floor, Fallback is true, PickIndex is -1, and Ranked
// is ordered by the SAME bitrate score (not release.ScoreCandidate).
func TestMinScoreFloorFallback(t *testing.T) {
	cands := []Candidate{
		// All below the 10 Mbps High/1080p floor after padding, different scores.
		{Title: "a", Protocol: "usenet", SizeBytes: bytesForMbps(8, testRuntime), RuntimeSeconds: testRuntime, Resolution: 1080, Codec: "x264"},  // padded 6.4
		{Title: "b", Protocol: "usenet", SizeBytes: bytesForMbps(11, testRuntime), RuntimeSeconds: testRuntime, Resolution: 1080, Codec: "x264"}, // padded 8.8
		{Title: "c", Protocol: "usenet", SizeBytes: bytesForMbps(5, testRuntime), RuntimeSeconds: testRuntime, Resolution: 1080, Codec: "x264"},  // padded 4.0
	}
	sel := Select(cands, quality.High, 0)
	if !sel.Fallback || sel.PickIndex != -1 {
		t.Fatalf("expected fallback (nothing qualifies): fallback=%v pick=%d", sel.Fallback, sel.PickIndex)
	}
	// Ranked by bitrate score desc: b (8.8) > a (6.4) > c (4.0).
	want := []int{1, 0, 2}
	for i, idx := range want {
		if sel.Ranked[i] != idx {
			t.Fatalf("Ranked = %v, want %v (by bitrate score, not ScoreCandidate)", sel.Ranked, want)
		}
	}
}

func TestRuntimeMismatch(t *testing.T) {
	tests := []struct {
		name             string
		probed, expected float64
		wantMismatch     bool
		wantChecked      bool
	}{
		{"gross short (sample file)", 300, 6000, true, true},    // ratio 0.05
		{"gross long (wrong content)", 12000, 6000, true, true}, // ratio 2.0
		{"within band exact", 6000, 6000, false, true},
		{"within band extended cut", 7200, 6000, false, true},   // ratio 1.2, legit
		{"within band trimmed", 4400, 6000, false, true},        // ratio ~0.73, legit
		{"zero probed duration → skip", 0, 6000, false, false},  // ffprobe omitted duration
		{"zero expected runtime → skip", 6000, 0, false, false}, // TMDB runtime unset
		{"both zero → skip", 0, 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMismatch, gotChecked := RuntimeMismatch(tc.probed, tc.expected)
			if gotMismatch != tc.wantMismatch || gotChecked != tc.wantChecked {
				t.Fatalf("RuntimeMismatch(%.0f,%.0f) = (mismatch=%v, checked=%v), want (%v, %v)",
					tc.probed, tc.expected, gotMismatch, gotChecked, tc.wantMismatch, tc.wantChecked)
			}
		})
	}
}

// TestFloorTable480pRatiosConsistentWithExistingRows locks in the plan's
// explicit design intent for the new 480p floorTable row: its cross-tier step
// ratios (Low->Medium, Medium->High, High->Lossless) should track the SAME
// spacing the three pre-existing resolution rows (720/1080/2160) already
// share — not exact equality (480p's proposed numbers are flagged as tunable
// starting points, not derived by precise division — see floorTable's doc
// comment), but within a generous tolerance. A future retune of these numbers
// is expected to keep passing this test as long as it preserves that same
// relative spacing; loosening the *tolerance* to force a pass instead of
// fixing the numbers would defeat the point of this test.
func TestFloorTable480pRatiosConsistentWithExistingRows(t *testing.T) {
	const tolerance = 0.15 // 15% — generous margin around the ~2.5x/2x/1.8x steps

	steps := []struct {
		name          string
		lower, higher quality.Tier
	}{
		{"Low->Medium", quality.Low, quality.Medium},
		{"Medium->High", quality.Medium, quality.High},
		{"High->Lossless", quality.High, quality.Lossless},
	}

	for _, resolution := range []int{720, 1080, 2160} {
		for _, step := range steps {
			refRatio := floorTable[step.higher][resolution] / floorTable[step.lower][resolution]
			gotRatio := floorTable[step.higher][480] / floorTable[step.lower][480]
			diff := math.Abs(gotRatio-refRatio) / refRatio
			if diff > tolerance {
				t.Errorf("480p %s ratio = %.3f, reference (%dp) = %.3f, diff %.1f%% exceeds %.0f%% tolerance",
					step.name, gotRatio, resolution, refRatio, diff*100, tolerance*100)
			}
		}
	}
}

// TestFloorTable480pRowPresentForEveryTier is a basic sanity check that the
// new row exists (no missing-key zero-value silently grading every 480p
// candidate as StatusUnknownResolution again).
func TestFloorTable480pRowPresentForEveryTier(t *testing.T) {
	for _, tier := range []quality.Tier{quality.Low, quality.Medium, quality.High, quality.Lossless} {
		floor, ok := floorTable[tier][480]
		if !ok || floor <= 0 {
			t.Errorf("floorTable[%s][480] missing or non-positive: %v (ok=%v)", tier, floor, ok)
		}
	}
}
