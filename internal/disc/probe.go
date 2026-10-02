package disc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type ffprobeOut struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		Duration string `json:"duration"`
	} `json:"streams"`
	Chapters []struct {
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	} `json:"chapters"`
}

func probeTitles(ctx context.Context, src string) ([]Title, error) {
	probe, err := lookPath("ffprobe")
	if err != nil {
		return nil, fmt.Errorf("%w: ffprobe", ErrToolMissing)
	}
	var titles []Title
	streak := 0
	for n := 1; n <= maxTitles; n++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		t, err := probeTitle(ctx, probe, src, n)
		if err != nil {
			streak++
			if n == 1 {
				return nil, fmt.Errorf("%w: title 1: %v", ErrNoTitles, err)
			}
			if streak >= failStreak {
				break
			}
			continue
		}
		streak = 0
		titles = append(titles, t)
	}
	if len(titles) == 0 {
		return nil, ErrNoTitles
	}
	return titles, nil
}

func probeTitle(ctx context.Context, bin, src string, n int) (Title, error) {
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := runCmd(pctx, bin,
		"-hide_banner", "-f", "dvdvideo", "-title", strconv.Itoa(n),
		"-print_format", "json", "-show_format", "-show_streams", "-show_chapters",
		"-i", src,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Title{}, fmt.Errorf("ffprobe title %d: %v: %s", n, err, trimErr(out))
	}
	var parsed ffprobeOut
	if err := json.Unmarshal(jsonFromMixed(out), &parsed); err != nil {
		return Title{}, fmt.Errorf("ffprobe title %d json: %w", n, err)
	}
	dur := parseFloat(parsed.Format.Duration)
	if dur <= 0 {
		for _, s := range parsed.Streams {
			if d := parseFloat(s.Duration); d > dur {
				dur = d
			}
		}
	}
	if dur <= 0 {
		return Title{}, fmt.Errorf("title %d has no duration", n)
	}
	return Title{N: n, DurationS: dur, Chapters: len(parsed.Chapters)}, nil
}

func jsonFromMixed(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	i := strings.Index(s, "{")
	if i < 0 {
		return b
	}
	return []byte(s[i:])
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func trimErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		return s[len(s)-240:]
	}
	return s
}
