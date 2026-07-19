// Package videophash computes a StashDB/FansDB-compatible video perceptual hash:
// the single 64-bit DCT "PHASH" that the stash-box network indexes and that a
// local Stash's GraphQL emits. SAK owns this hasher so it can identify and
// dedupe Adult content by talking to StashDB/FansDB/TPDB directly, without a
// live local Stash as a bridge.
//
// The algorithm (VERIFIED against stashapp/stash pkg/hash/videophash/phash.go
// and corona10/goimagehash): sample 25 frames (a 5×5 grid) across the middle 90%
// of the video (5%->95% window at offset+i*stepSize), scale each to width 160
// with proportional height, composite them row-major into one collage image, and
// hand that image to goimagehash.PerceptionHash. SAK implements NONE of the hash
// math (resize→gray→DCT→top-left-8×8→median→threshold→uint64) — that is
// goimagehash's job, verbatim. SAK owns only correct input assembly (the collage)
// and correct output encoding.
//
// ENCODING INVARIANT (load-bearing): the returned string is exactly
// strconv.FormatUint(hashUint64, 16) — lowercase, base-16, UNPADDED. This is
// byte-identical to Stash's models.Fingerprint.Value(), and that byte-identity is
// the entire compatibility premise: local-Stash format == stash-box format == this
// format. A hash with high zero nibbles is therefore SHORTER than 16 characters,
// and that is correct. Do NOT use fmt.Sprintf("%016x", ...) — the zero-padding
// would make the string non-identical to Stash's. Always compare two hashes by
// parsing both to uint64 (strconv.ParseUint(...,16,64)) and taking the Hamming
// distance (see Hamming), never by comparing the encoded strings.
//
// GOIMAGEHASH VERSION PIN (load-bearing): goimagehash is pinned to an exact
// version in go.mod (currently v1.1.0). The "same library → same bits" premise
// rests entirely on its resizer (nfnt/resize, bilinear) and DCT not drifting
// across versions. ANY goimagehash version bump REQUIRES re-running the live
// cross-validation against a reference Stash before trusting the output —
// mirroring internal/phash's Scheme-tag self-invalidation discipline WITHOUT
// adding a scheme tag (a scheme tag would break byte-identity with Stash).
//
// This package is a fully independent sibling of internal/phash and shares ZERO
// code with it: internal/phash is a different, unrelated algorithm (ajdnik/imghash,
// 5 frames hashed separately, 320-bit scheme-tagged composite) for Movies/Series
// Dedup, and stays exactly as-is. No shared types, no shared distance machine, no
// shared Scheme tag — carrying any of that over would break byte-identity with
// Stash. The only thing reused is the shape of the injected-runner test seam.
//
// A real hash requires a real ffmpeg/ffprobe binary; when absent, any runner
// error propagates and Hash returns an error — callers treat any error as "no
// hash", never a partial or fallback hash. That is a permanent boundary.
package videophash

import (
	"context"
	"fmt"
	"image"
	"strconv"
	"time"

	"github.com/corona10/goimagehash"
)

const (
	columns = 5
	rows    = 5
	// FrameCount is the number of frames sampled and composited into the
	// collage — exported for the acceptance-guard and future consumers.
	FrameCount      = columns * rows // 25
	screenshotWidth = 160
)

// durationProber probes the video's duration (seconds). Injected so unit tests
// can supply a fixed duration without a real ffprobe binary or video file.
type durationProber func(ctx context.Context, path string) (float64, error)

// frameRunner extracts one frame per requested timestamp, in order, each already
// decoded to image.Image (scaled to width 160, aspect-preserved). Injected so the
// fake can (a) assert the exact timestamps requested — proving the sampling
// formula — and (b) return canned frames with no PNG encode/decode round-trip.
// Returning decoded image.Image (not [][]byte PNG) keeps SAK's role strictly
// "assemble input": the real runner owns decode.
type frameRunner func(ctx context.Context, path string, timestamps []float64) ([]image.Image, error)

// Hasher computes a StashDB-compatible 64-bit video perceptual hash. It has two
// injected seams (a duration prober and a frame runner) so the sampling formula
// and collage assembly are unit-testable without a real ffmpeg binary.
type Hasher struct {
	probe   durationProber
	run     frameRunner
	timeout time.Duration
}

// New returns a Hasher backed by the real ffmpeg/ffprobe binaries. Hardware
// acceleration is detected once at construction time (cuda > vaapi) and used
// for each frame decode with a transparent CPU fallback on any driver error.
// Frame extractions run concurrently (up to 4 at once) regardless of hardware
// availability.
func New() *Hasher {
	hw := probeHWAccel(context.Background())
	return &Hasher{probe: runFFprobeDuration, run: newRunner(hw), timeout: 2 * time.Minute}
}

// Hash computes the StashDB-compatible perceptual hash of the video at path and
// returns it as unpadded lowercase hex (see the package doc's encoding invariant).
// Flow: probe duration → duration guard → collageTimestamps → run frame runner →
// frame-count guard → assembleCollage → goimagehash.PerceptionHash → encode.
// Bounded by an internal timeout layered onto ctx. Any error (probe failure,
// non-positive duration, wrong frame count, undecodable frame, hash failure)
// returns an error and never a partial hash.
func (h *Hasher) Hash(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	dur, err := h.probe(ctx, path)
	if err != nil {
		return "", err
	}
	if dur <= 0 {
		return "", fmt.Errorf("videophash: %s reports no positive duration", path)
	}

	ts := collageTimestamps(dur)
	frames, err := h.run(ctx, path, ts)
	if err != nil {
		return "", err
	}
	if len(frames) != FrameCount {
		return "", fmt.Errorf("videophash: expected %d frames from %s, got %d", FrameCount, path, len(frames))
	}

	collage, err := assembleCollage(frames)
	if err != nil {
		return "", err
	}

	ih, err := goimagehash.PerceptionHash(collage)
	if err != nil {
		return "", fmt.Errorf("videophash: perception hash of %s: %w", path, err)
	}
	return strconv.FormatUint(ih.GetHash(), 16), nil // UNPADDED lowercase hex — NOT %016x
}
