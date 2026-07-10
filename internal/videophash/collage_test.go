package videophash

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"testing"
)

const epsilon = 1e-9

// solidFrame returns a w×h image filled with a single opaque color.
func solidFrame(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// tagColor gives each frame index a distinct opaque color so a cell can be
// identified by reading back a single canvas pixel.
func tagColor(index int) color.RGBA {
	return color.RGBA{R: uint8(index * 10), G: 0, B: 0, A: 255}
}

func TestCollageTimestamps(t *testing.T) {
	ts := collageTimestamps(100.0)

	if len(ts) != FrameCount {
		t.Fatalf("len = %d, want %d", len(ts), FrameCount)
	}

	// [0] and step are single-operation float64 results — exact equality is valid.
	if ts[0] != 5.0 {
		t.Errorf("ts[0] = %v, want exactly 5.0", ts[0])
	}
	step := (0.9 * 100.0) / float64(FrameCount)
	if step != 3.6 {
		t.Errorf("step = %v, want exactly 3.6", step)
	}

	// Accumulated values (offset + i*step) are NOT guaranteed bit-identical to a
	// separately-computed literal — compare with an epsilon, never ==.
	if got, want := ts[1], 8.6; math.Abs(got-want) >= epsilon {
		t.Errorf("ts[1] = %v, want ~%v", got, want)
	}
	if got, want := ts[24], 91.4; math.Abs(got-want) >= epsilon {
		t.Errorf("ts[24] = %v, want ~%v", got, want)
	}
}

func TestAssembleCollagePasteCoordinates(t *testing.T) {
	const w, h = 4, 2
	frames := make([]image.Image, FrameCount)
	for i := range frames {
		frames[i] = solidFrame(w, h, tagColor(i))
	}

	img, err := assembleCollage(frames)
	if err != nil {
		t.Fatalf("assembleCollage: %v", err)
	}

	if got := img.Bounds().Dx(); got != w*columns {
		t.Errorf("canvas width = %d, want %d", got, w*columns)
	}
	if got := img.Bounds().Dy(); got != h*rows {
		t.Errorf("canvas height = %d, want %d", got, h*rows)
	}

	rgba, ok := img.(*image.RGBA)
	if !ok {
		t.Fatalf("canvas is %T, want *image.RGBA", img)
	}

	// These four indices discriminate row-major from column-major layout.
	cases := []struct {
		index, x, y int
	}{
		{0, 0, 0},          // top-left
		{4, 4 * w, 0},      // top-right (row 1, col 5)
		{5, 0, h},          // row 2, col 1 (row-major wrap)
		{24, 4 * w, 4 * h}, // bottom-right
	}
	for _, c := range cases {
		got := rgba.RGBAAt(c.x, c.y)
		want := tagColor(c.index)
		if got != want {
			t.Errorf("index %d: canvas pixel at (%d,%d) = %v, want %v (proves row-major layout)",
				c.index, c.x, c.y, got, want)
		}
	}
}

func TestAssembleCollageDimensionMismatch(t *testing.T) {
	frames := make([]image.Image, FrameCount)
	for i := range frames {
		frames[i] = solidFrame(4, 2, tagColor(i))
	}
	// One frame with different bounds must be rejected, not silently misaligned.
	frames[7] = solidFrame(8, 2, tagColor(7))

	if _, err := assembleCollage(frames); err == nil {
		t.Fatal("expected error for mismatched frame dimensions, got nil")
	}
}

func TestAssembleCollageWrongFrameCount(t *testing.T) {
	frames := make([]image.Image, FrameCount-1)
	for i := range frames {
		frames[i] = solidFrame(4, 2, tagColor(i))
	}
	if _, err := assembleCollage(frames); err == nil {
		t.Fatalf("expected error for %d frames, got nil", FrameCount-1)
	}
}

func TestFormatUintRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		u       uint64
		wantHex string
		wantLen int
	}{
		{"all-ones", 0xFFFFFFFFFFFFFFFF, "ffffffffffffffff", 16},
		{"high-zero-nibbles", 0x0000000000000001, "1", 1}, // NOT "0000000000000001"
		{"zero", 0, "0", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strconv.FormatUint(c.u, 16)
			if got != c.wantHex {
				t.Errorf("FormatUint(%#x, 16) = %q, want %q", c.u, got, c.wantHex)
			}
			if len(got) != c.wantLen {
				t.Errorf("len(%q) = %d, want %d (unpadded encoding)", got, len(got), c.wantLen)
			}
			back, err := strconv.ParseUint(got, 16, 64)
			if err != nil {
				t.Fatalf("ParseUint(%q): %v", got, err)
			}
			if back != c.u {
				t.Errorf("round-trip %q -> %#x, want %#x", got, back, c.u)
			}
		})
	}
}

func TestHamming(t *testing.T) {
	cases := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0},
		{0xF, 0, 4},
		{0xFFFFFFFFFFFFFFFF, 0, 64},
	}
	for _, c := range cases {
		if got := Hamming(c.a, c.b); got != c.want {
			t.Errorf("Hamming(%#x, %#x) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
