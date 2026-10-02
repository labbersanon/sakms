package disc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func outputPath(dir, volume, name string) string {
	return filepath.Join(dir, volume+" - "+name+".mkv")
}

func extractWork(ctx context.Context, src string, w Work, dest string, volume string) error {
	if st, err := os.Stat(dest); err == nil && st.Size() >= minOutputB {
		return nil
	}
	bin, err := lookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("%w: ffmpeg", ErrToolMissing)
	}
	ectx, cancel := context.WithTimeout(ctx, time.Duration(titleTimeout)*time.Second)
	defer cancel()
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-f", "dvdvideo", "-title", strconv.Itoa(w.Title),
	}
	if w.ChapterStart > 0 {
		args = append(args,
			"-chapter_start", strconv.Itoa(w.ChapterStart),
			"-chapter_end", strconv.Itoa(w.ChapterEnd),
		)
	}
	args = append(args,
		"-i", src,
		"-c", "copy", "-map", "0",
		"-metadata", "title="+volume+" "+w.Name,
		"-metadata", "comment=disc-volume="+volume,
		dest,
	)
	cmd := runCmd(ectx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("ffmpeg %s: %v: %s", w.Name, err, trimErr(out))
	}
	st, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dest, err)
	}
	if st.Size() < minOutputB {
		_ = os.Remove(dest)
		return fmt.Errorf("%s is empty after extract", dest)
	}
	return nil
}
