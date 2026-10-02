package disc

// Claude 2026-10-02: Inspect probes + writes sidecar without extracting.
// Reason: Organize Discs identifies after ISO pick; extract is a later POST.
// Troubleshooting: ErrToolMissing — container ffmpeg/ffprobe not on PATH.
// Review if: identify skips sidecar write until extract succeeds.

import (
	"context"
	"fmt"
	"os"
)

// Inspect probes a disc image and returns the classified map without extracting.
func Inspect(ctx context.Context, src string) (*Map, error) {
	if !IsDiscImage(src) {
		return nil, ErrNotDisc
	}
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, ErrNotDisc
	}
	if _, err := lookPath("ffprobe"); err != nil {
		return nil, fmt.Errorf("%w: ffprobe", ErrToolMissing)
	}
	volume := volumeOf(ctx, src)
	titles, err := probeTitles(ctx, src)
	if err != nil {
		return nil, err
	}
	titles = AssignRoles(titles)
	m := &Map{
		Version: mapVersion,
		Volume:  volume,
		Source:  src,
		Titles:  titles,
	}
	if err := writeSidecar(m); err != nil {
		return nil, fmt.Errorf("writing sidecar: %w", err)
	}
	return m, nil
}
