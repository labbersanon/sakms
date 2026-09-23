package mediainfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Probe holds the fields needed to build a place.QualityKey, plus the video's
// duration in seconds and optional container identity tags.
// Duration is sourced from ffprobe's FORMAT section (not the stream-level
// duration, which MKV/MP4 frequently omit) so it matches the value
// internal/videophash's own duration probe uses for the same file — the two
// must agree because Adult fingerprint give-back stamps this duration and
// rejects a non-positive one. Missing/blank format duration parses to 0.
type Probe struct {
	CodecName string
	Width     int
	Height    int
	BitRate   int64
	Duration  float64
	// Tags are best-effort identity hints from format.tags (title/year/ids).
	// Empty when the container has none — callers must fall through to NFO/filename.
	Tags Tags
}

// Tags holds normalized identity fields read from container metadata.
type Tags struct {
	Title  string
	Year   int
	IMDBID string // tt… when present
	TMDBID int
}

// HasIdentity reports whether any id or title is present.
func (t Tags) HasIdentity() bool {
	return t.TMDBID > 0 || t.IMDBID != "" || strings.TrimSpace(t.Title) != ""
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
		"-show_format",
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

type ffprobeFormat struct {
	Duration string            `json:"duration"`
	Tags     map[string]string `json:"tags"`
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

// Probe runs ffprobe against path (bounded by an internal timeout layered
// onto ctx) and returns the first video stream's codec/resolution/bitrate
// plus any format.tags identity fields.
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

	var duration float64
	if out.Format.Duration != "" {
		// Best-effort like bit_rate: a missing/blank format duration parses to
		// 0, never an error — a file with no reported duration is a valid probe
		// result, just one that can't feed fingerprint give-back.
		duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	}

	return &Probe{
		CodecName: s.CodecName,
		Width:     s.Width,
		Height:    s.Height,
		BitRate:   bitRate,
		Duration:  duration,
		Tags:      ParseFormatTags(out.Format.Tags),
	}, nil
}

var (
	imdbIDRe  = regexp.MustCompile(`(?i)\b(tt\d{7,8})\b`)
	tmdbIDRe  = regexp.MustCompile(`(?i)(?:tmdb[:\s-]*)?(\d{1,8})\b`)
	yearOnlyRe = regexp.MustCompile(`^(19|20)\d{2}$`)
	yearPrefRe = regexp.MustCompile(`^(19|20)\d{2}`)
)

// ParseFormatTags normalizes ffprobe format.tags into Tags.
// Claude 2026-09-23: embedded-tag identity for Movies Rename + library repair.
// Reason: operator C/C/A — tags → NFO → filename; title+year even without ids.
// Troubleshooting: letter tiles / unmatched orphans when container has title tags.
// Review if: additional vendor tag keys need mapping.
func ParseFormatTags(raw map[string]string) Tags {
	if len(raw) == 0 {
		return Tags{}
	}
	norm := map[string]string{}
	for k, v := range raw {
		norm[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}

	var t Tags
	t.Title = firstTag(norm, "title", "show", "movie", "name", "com.apple.quicktime.title")
	t.IMDBID = normalizeIMDB(firstTag(norm, "imdb", "imdb_id", "imdbid", "com.apple.quicktime.imdb"))
	if t.IMDBID == "" {
		// Some muxers stuff the id into a freeform comment/description.
		for _, k := range []string{"comment", "description", "synopsis"} {
			if m := imdbIDRe.FindStringSubmatch(norm[k]); m != nil {
				t.IMDBID = strings.ToLower(m[1])
				break
			}
		}
	}
	t.TMDBID = parseTMDBID(firstTag(norm, "tmdb", "tmdb_id", "tmdbid", "tmdbid"))
	t.Year = parseTagYear(firstTag(norm, "year", "date", "date_released", "release_date", "com.apple.quicktime.year"))
	if t.Year == 0 && t.Title != "" {
		// Title sometimes embeds "(2010)".
		if m := regexp.MustCompile(`\((19|20)\d{2}\)`).FindStringSubmatch(t.Title); m != nil {
			t.Year, _ = strconv.Atoi(m[1])
		}
	}
	return t
}

func firstTag(norm map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(norm[k]); v != "" {
			return v
		}
	}
	return ""
}

func normalizeIMDB(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if m := imdbIDRe.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

func parseTMDBID(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if id, err := strconv.Atoi(s); err == nil && id > 0 {
		return id
	}
	if m := tmdbIDRe.FindStringSubmatch(s); m != nil {
		id, _ := strconv.Atoi(m[1])
		if id > 0 {
			return id
		}
	}
	return 0
}

func parseTagYear(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if yearOnlyRe.MatchString(s) {
		y, _ := strconv.Atoi(s)
		return y
	}
	if m := yearPrefRe.FindString(s); m != "" {
		y, _ := strconv.Atoi(m)
		return y
	}
	return 0
}
