package phash

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"
)

// TestCalibration_DefaultThresholdSeparatesPerturbedFromDistinct is a
// regression guard on ALGORITHM SANITY, not a real-world-validated claim that
// the per-mode defaults (Series 40, Movies 64) are the correct per-frame
// Hamming cuts for actual movie frames. It only proves that the shipped
// algorithm (imghash PDQ, 256 bits/frame) distinguishes a synthetically
// PERTURBED copy of an image
// (JPEG re-encode / brightness shift — modelling "same content, different
// release/codec/res") from a structurally DISTINCT generated image (modelling
// a wrong-TMDB match, a different cut, or an extras file) with the default cut
// sitting cleanly between the two classes, with margin on each side.
//
// Treat the defaults as STARTING values, exposed as a per-mode tunable
// (GET/PUT /api/modes/{mode}/phash-threshold), NOT proven constants. Real
// content is not shipped in this repo (no copyrighted movie frames), so
// real-world confidence comes from the build-tagged integration test and the
// manual live walkthrough against actual files — never from this synthetic
// test. The test asserts a MARGIN (not merely "separated") so a future
// algorithm or parameter change that erodes the separation fails loudly here
// before it fully collapses in production.
func TestCalibration_DefaultThresholdSeparatesPerturbedFromDistinct(t *testing.T) {
	const (
		size  = 128
		bases = 8
	)
	algo, err := newAlgo()
	if err != nil {
		t.Fatalf("constructing algo: %v", err)
	}
	hash := func(img image.Image) []byte {
		h, _ := hashFrame(algo, img)
		return h
	}

	baseImgs := make([]image.Image, bases)
	baseHash := make([][]byte, bases)
	for k := 0; k < bases; k++ {
		baseImgs[k] = broadbandImage(k, size)
		baseHash[k] = hash(baseImgs[k])
	}

	// Duplicate class: each base vs. mild perturbations of itself.
	dupMax := 0
	for k := 0; k < bases; k++ {
		for _, pert := range []image.Image{
			jpegRoundTrip(t, baseImgs[k], 40),
			brightnessShift(baseImgs[k], 30),
			brightnessShift(baseImgs[k], -30),
			jpegRoundTrip(t, brightnessShift(baseImgs[k], 20), 50),
		} {
			if d := hammingBits(baseHash[k], hash(pert)); d > dupMax {
				dupMax = d
			}
		}
	}

	// Different class: every distinct pair of unrelated bases.
	diffMin := 1 << 30
	for i := 0; i < bases; i++ {
		for j := i + 1; j < bases; j++ {
			if d := hammingBits(baseHash[i], baseHash[j]); d < diffMin {
				diffMin = d
			}
		}
	}

	t.Logf("perturbed-duplicate max per-frame Hamming = %d; distinct-content min = %d; Series cut = %d; Movies cut = %d",
		dupMax, diffMin, DefaultThreshold, DefaultMoviesThreshold)

	// Margins: over PDQ's 256-bit-per-frame scale the measured separation is
	// wide (harness: dupMax≈12, diffMin≈98). Each shipped per-mode default must
	// sit inside the [dupMax, diffMin] gap with at least a guardBand of headroom
	// on BOTH sides, so a future algorithm/parameter change that erodes the
	// separation fails loudly here before it collapses in production. Both
	// bounds are computed from the run's actual dupMax/diffMin (not hardcoded),
	// keeping this a real regression guard rather than a snapshot of one run.
	const guardBand = 10 // per-frame Hamming bits of required clearance each side
	assertInGap := func(name string, cut int) {
		if cut-dupMax < guardBand {
			t.Errorf("%s cut %d is within %d bits of the perturbed-duplicate max %d — margin eroded (need >= %d clearance)",
				name, cut, cut-dupMax, dupMax, guardBand)
		}
		if diffMin-cut < guardBand {
			t.Errorf("%s cut %d is within %d bits of the distinct-content min %d — margin eroded (need >= %d clearance)",
				name, cut, diffMin-cut, diffMin, guardBand)
		}
		if !(dupMax < cut && cut < diffMin) {
			t.Fatalf("%s cut %d does not sit strictly between the duplicate max (%d) and distinct min (%d)",
				name, cut, dupMax, diffMin)
		}
	}
	assertInGap("Series (DefaultThreshold)", DefaultThreshold)
	assertInGap("Movies (DefaultMoviesThreshold)", DefaultMoviesThreshold)

	// Design intent: Movies is more permissive (higher cut) than Series.
	if DefaultMoviesThreshold <= DefaultThreshold {
		t.Errorf("Movies cut %d must be strictly more permissive than Series cut %d",
			DefaultMoviesThreshold, DefaultThreshold)
	}
}

// broadbandImage renders a deterministic image with rich, natural-spectrum-like
// structure: a sum of many sinusoids at seed-varied frequencies, amplitudes,
// and phases. Broadband content keeps PHash's DCT coefficients well clear of
// their comparison mean, so the hash is STABLE under a mild perturbation yet
// DISTINCT across seeds — unlike a smooth gradient or a single grating, whose
// near-threshold coefficients make PHash flip many bits on tiny changes.
func broadbandImage(seed, size int) image.Image {
	rng := rand.New(rand.NewSource(int64(seed) + 1))
	type comp struct{ fx, fy, ph, a float64 }
	comps := make([]comp, 14)
	for i := range comps {
		comps[i] = comp{
			fx: 0.5 + rng.Float64()*5.5,
			fy: 0.5 + rng.Float64()*5.5,
			ph: rng.Float64() * 2 * math.Pi,
			a:  rng.Float64()*2 - 1,
		}
	}
	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fxn := float64(x) / float64(size)
			fyn := float64(y) / float64(size)
			var s float64
			for _, c := range comps {
				s += c.a * math.Sin(2*math.Pi*(c.fx*fxn+c.fy*fyn)+c.ph)
			}
			img.SetGray(x, y, color.Gray{Y: clampByte(128 + 45*s)})
		}
	}
	return img
}

func brightnessShift(src image.Image, delta int) image.Image {
	b := src.Bounds()
	out := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g := color.GrayModel.Convert(src.At(x, y)).(color.Gray)
			out.SetGray(x, y, color.Gray{Y: clampByte(float64(int(g.Y) + delta))})
		}
	}
	return out
}

func jpegRoundTrip(t *testing.T, src image.Image, quality int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	dec, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	return dec
}

func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
