package videophash

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder image.Decode uses on ffmpeg output
	"log"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
)

// probeHWAccel runs ffmpeg -hwaccels once to detect available hardware video
// decoders. Returns the highest-priority match from cuda > vaapi, or "" when
// none are available or ffmpeg is not installed. Called once at New() time.
// An own copy of internal/phash's probeHWAccel — this package shares zero
// code with internal/phash by design.
func probeHWAccel(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hwaccels", "-v", "quiet")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseHWAccels(string(out))
}

// parseHWAccels parses ffmpeg's -hwaccels output and returns the first match
// from the preference list cuda > vaapi, or "" if neither is present.
func parseHWAccels(text string) string {
	lower := strings.ToLower(text)
	for _, hw := range []string{"cuda", "vaapi"} {
		if strings.Contains(lower, hw) {
			return hw
		}
	}
	return ""
}

// newRunner returns a frameRunner that extracts frames concurrently (up to 4
// at once). When hwaccel is non-empty, each frame decode attempts hardware
// acceleration with a transparent CPU retry on any ffmpeg failure.
func newRunner(hwaccel string) frameRunner {
	return func(ctx context.Context, path string, timestamps []float64) ([]image.Image, error) {
		out := make([]image.Image, len(timestamps))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(4)
		for i, t := range timestamps {
			i, t := i, t
			g.Go(func() error {
				img, err := extractFrame(gctx, path, t, hwaccel)
				if err != nil {
					return err
				}
				out[i] = img
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		return out, nil
	}
}

// extractFrame extracts one frame at timestamp t. When hwaccel is non-empty
// it tries hardware decode first; any ffmpeg error silently retries on CPU.
func extractFrame(ctx context.Context, path string, t float64, hwaccel string) (image.Image, error) {
	if hwaccel != "" {
		img, err := ffmpegFrameImage(ctx, path, t, hwaccel)
		if err == nil {
			return img, nil
		}
		log.Printf("videophash: hwaccel %q failed at %.3fs in %s, retrying CPU: %v", hwaccel, t, path, err)
	}
	return ffmpegFrameImage(ctx, path, t, "")
}

// ffmpegFrameImage extracts one frame at timestamp t from path, scaled to
// screenshotWidth with proportional height, and returns it as a decoded
// image.Image. hwaccel="" → CPU-only; "cuda" or "vaapi" → hardware-assisted
// decode. The -ss flag precedes -i for fast input-level seeking.
func ffmpegFrameImage(ctx context.Context, path string, t float64, hwaccel string) (image.Image, error) {
	ts := strconv.FormatFloat(t, 'f', 3, 64)
	scaleFilter := fmt.Sprintf("scale=%d:-2", screenshotWidth)
	args := []string{"-ss", ts}
	if hwaccel != "" {
		args = append(args, "-hwaccel", hwaccel)
	}
	args = append(args,
		"-i", path,
		"-frames:v", "1",
		"-vf", scaleFilter,
		"-f", "image2",
		"-vcodec", "png",
		"-",
	)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("videophash: extracting frame at %.3fs from %s: %w", t, path, err)
	}
	img, _, err := image.Decode(&buf)
	if err != nil {
		return nil, fmt.Errorf("videophash: decoding frame at %.3fs from %s: %w", t, path, err)
	}
	return img, nil
}
