package phash

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
)

// probeHWAccel runs ffmpeg -hwaccels once to detect available hardware video
// decoders. Returns the highest-priority match from cuda > vaapi, or "" when
// none are available or ffmpeg is not installed. Called once at New() time.
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

// newRunner returns a runner that extracts frames concurrently (up to 4 at
// once). When hwaccel is non-empty, each frame decode attempts hardware
// acceleration with a transparent CPU retry on any ffmpeg failure.
func newRunner(hwaccel string) runner {
	return func(ctx context.Context, path string, frames int) ([][]byte, error) {
		dur, err := ffprobeDuration(ctx, path)
		if err != nil {
			return nil, err
		}
		if dur <= 0 {
			return nil, fmt.Errorf("phash: %s reports no positive duration", path)
		}

		out := make([][]byte, frames)
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(4)
		for i := 1; i <= frames; i++ {
			i := i
			t := dur * float64(i) / float64(frames+1)
			g.Go(func() error {
				raw, err := extractFrame(gctx, path, t, hwaccel)
				if err != nil {
					return err
				}
				out[i-1] = raw
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
func extractFrame(ctx context.Context, path string, t float64, hwaccel string) ([]byte, error) {
	if hwaccel != "" {
		raw, err := ffmpegFrame(ctx, path, t, hwaccel)
		if err == nil {
			return raw, nil
		}
		log.Printf("phash: hwaccel %q failed at %.3fs in %s, retrying CPU: %v", hwaccel, t, path, err)
	}
	return ffmpegFrame(ctx, path, t, "")
}

// ffprobeDuration probes the video's duration in seconds via ffprobe.
func ffprobeDuration(ctx context.Context, path string) (float64, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	raw, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("phash: ffprobe duration of %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	d, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("phash: parsing duration %q of %s: %w", trimmed, path, err)
	}
	return d, nil
}

// ffmpegFrame extracts one frame at timestamp t from path as PNG bytes.
// hwaccel="" → CPU-only; "cuda" or "vaapi" → hardware-assisted decode.
// The -ss flag precedes -i for fast input-level seeking.
func ffmpegFrame(ctx context.Context, path string, t float64, hwaccel string) ([]byte, error) {
	ts := strconv.FormatFloat(t, 'f', 3, 64)
	args := []string{"-ss", ts}
	if hwaccel != "" {
		args = append(args, "-hwaccel", hwaccel)
	}
	args = append(args,
		"-i", path,
		"-frames:v", "1",
		"-vf", "scale=32:32",
		"-f", "image2",
		"-vcodec", "png",
		"-",
	)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("phash: extracting frame at %.3fs from %s: %w", t, path, err)
	}
	return buf.Bytes(), nil
}
