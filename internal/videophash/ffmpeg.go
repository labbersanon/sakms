package videophash

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder image.Decode uses on ffmpeg output
	"os/exec"
	"strconv"
	"strings"
)

// runFFprobeDuration probes the video's duration in seconds via ffprobe. This is
// an OWN COPY of internal/phash's ffprobeDuration shape (copied deliberately, NOT
// imported — this package shares zero code with internal/phash), so the two
// hashers stay fully independent.
func runFFprobeDuration(ctx context.Context, path string) (float64, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	raw, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("videophash: ffprobe duration of %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	d, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("videophash: parsing duration %q of %s: %w", trimmed, path, err)
	}
	return d, nil
}

// runFFmpegFrames extracts one frame per requested timestamp, in order, each
// scaled to width 160 with proportional (even) height and decoded to an
// image.Image. Per frame it shells out `ffmpeg -ss <t> -i <path> -frames:v 1
// -vf scale=160:-2 -f image2 -vcodec png -` with fast seek (-ss BEFORE -i,
// matching Stash's default slowSeek=false). Any single frame error aborts the
// whole hash — never a partial montage.
//
// scale=160:-2 (proportional, even height) is THE byte-fidelity variable vs
// Stash's transcoder Width:160. It is intentionally not gold-plated: goimagehash's
// 64×64 re-resize dominates, so a ±1px height or seek-mode delta yields at most a
// small Hamming distance well inside StashDB tolerance. Pin Stash's exact
// seek+scale ONLY if a live cross-check exceeds tolerance.
func runFFmpegFrames(ctx context.Context, path string, timestamps []float64) ([]image.Image, error) {
	scaleFilter := fmt.Sprintf("scale=%d:-2", screenshotWidth)
	out := make([]image.Image, 0, len(timestamps))
	for _, t := range timestamps {
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-ss", strconv.FormatFloat(t, 'f', 3, 64),
			"-i", path,
			"-frames:v", "1",
			"-vf", scaleFilter,
			"-f", "image2",
			"-vcodec", "png",
			"-",
		)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("videophash: extracting frame at %.3fs from %s: %w", t, path, err)
		}
		img, _, err := image.Decode(&buf)
		if err != nil {
			return nil, fmt.Errorf("videophash: decoding frame at %.3fs from %s: %w", t, path, err)
		}
		out = append(out, img)
	}
	return out, nil
}
