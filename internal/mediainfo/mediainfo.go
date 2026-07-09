// Package mediainfo probes real video files for codec/resolution/bitrate —
// used for both sides of a Dedup comparison. Deliberately not conditional on
// whether a file is already tracked: see internal/dedup's doc comment for
// why SAK always reads the real file itself rather than trusting a
// *arr app's own reported quality for one side of the comparison.
package mediainfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// Probe holds the fields needed to build a place.QualityKey.
type Probe struct {
	CodecName string
	Width     int
	Height    int
	BitRate   int64
}

// runner executes ffprobe and returns its raw JSON stdout. Injected so
// Prober is testable without a real ffprobe binary or media file.
type runner func(ctx context.Context, path string) ([]byte, error)

type Prober struct {
	run     runner
	timeout time.Duration
}

// New returns a Prober backed by the real ffprobe binary.
func New() *Prober {
	return &Prober{run: runFFprobe, timeout: 30 * time.Second}
}

func runFFprobe(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "v:0",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	return out, nil
}

type ffprobeStream struct {
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	BitRate   string `json:"bit_rate"`
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
}

// Probe runs ffprobe against path (bounded by an internal timeout layered
// onto ctx) and returns the first video stream's codec/resolution/bitrate.
// Returns an error if ffprobe fails or the file has no video stream.
func (p *Prober) Probe(ctx context.Context, path string) (*Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.run(ctx, path)
	if err != nil {
		return nil, err
	}

	var out ffprobeOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	if len(out.Streams) == 0 {
		return nil, fmt.Errorf("no video stream found in %s", path)
	}

	s := out.Streams[0]
	var bitRate int64
	if s.BitRate != "" {
		// Best-effort: some containers/codecs don't report a stream-level
		// bit_rate at all — 0 is a fine "unknown" fallback, matching
		// place.QualityKey's existing unknown-value convention.
		bitRate, _ = strconv.ParseInt(s.BitRate, 10, 64)
	}

	return &Probe{
		CodecName: s.CodecName,
		Width:     s.Width,
		Height:    s.Height,
		BitRate:   bitRate,
	}, nil
}
