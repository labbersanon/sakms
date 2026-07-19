package videophash

import (
	"context"
	"fmt"
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
