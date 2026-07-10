package videophash

import (
	"fmt"
	"image"
	"image/draw"
)

// collageTimestamps returns the FrameCount timestamps (seconds) at which frames
// are sampled, matching Stash's videophash frame window exactly (VERIFIED
// against stashapp/stash pkg/hash/videophash/phash.go): skip the first 5%, then
// space FrameCount frames evenly across the middle 90% (5%->95% window).
//
// Frame 0 is at exactly offset (5% of duration); there is NO half-step
// centering — that half-step off-by-one is the classic mistake this formula is
// pinned against. Frame i is offset + i*step, i from 0.
func collageTimestamps(duration float64) []float64 {
	offset := 0.05 * duration
	step := (0.9 * duration) / float64(FrameCount)
	ts := make([]float64, FrameCount)
	for i := range ts {
		ts[i] = offset + float64(i)*step
	}
	return ts
}

// assembleCollage composites the FrameCount frames into a single row-major
// columns×rows montage, matching Stash's videophash collage layout exactly
// (VERIFIED against stashapp/stash pkg/hash/videophash/phash.go): the cell size
// is frame[0]'s bounds (width 160, proportional height — NOT 160×160); the
// canvas is width*columns × height*rows; placement is row-major
// (x = width*(i%columns), y = height*(i/rows), integer division == floor).
//
// Stash uses imaging.New(color.NRGBA{}) + imaging.Paste; stdlib image.NewRGBA +
// draw.Draw is pixel-equivalent here because every cell is fully overwritten and
// goimagehash resizes the montage to 64×64 grayscale before hashing, so the
// RGBA-vs-NRGBA backing type is provably irrelevant to the resulting bits. Not a
// fidelity risk.
//
// All frames must share dimensions (they do when scaled to a fixed width); a
// mismatch is an error rather than a silently-misaligned montage.
func assembleCollage(frames []image.Image) (image.Image, error) {
	if len(frames) != FrameCount {
		return nil, fmt.Errorf("videophash: assembleCollage needs %d frames, got %d", FrameCount, len(frames))
	}
	b := frames[0].Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("videophash: frame 0 has zero dimension")
	}
	for i, f := range frames {
		fb := f.Bounds()
		if fb.Dx() != w || fb.Dy() != h {
			return nil, fmt.Errorf("videophash: frame %d dims %dx%d differ from %dx%d", i, fb.Dx(), fb.Dy(), w, h)
		}
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w*columns, h*rows))
	for i, f := range frames {
		x := w * (i % columns)
		y := h * (i / rows) // integer division == floor(i/rows)
		draw.Draw(canvas, image.Rect(x, y, x+w, y+h), f, f.Bounds().Min, draw.Src)
	}
	return canvas, nil
}
