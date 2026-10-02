// Package disc probes DVD ISO/IMG images and extracts MPEG-2 titles.
//
// Claude 2026-10-02: Organize Discs unpack for library disc images.
// Reason: .iso stays in VideoExts (Jellyfin) but is not a Rename video;
//
//	IFO titles/chapters are the episode map and must be persisted first.
//
// Troubleshooting: container ffmpeg must list demuxer dvdvideo (BtbN GPL).
// Review if: Usenet finalize grows the same hook (Phase 3, not this PR).
package disc

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
)

const (
	RolePlayAll  = "playall"
	RoleFeature  = "feature"
	RoleExtra    = "extra"
	RoleMenu     = "menu"
	sidecarExt   = ".disc.json"
	mapVersion   = 1
	maxTitles    = 99
	failStreak   = 3
	menuMaxS     = 60
	shortMaxS    = 20 * 60
	extraMinS    = 15 * 60
	chSplitMinN  = 20
	chSplitMinS  = 90
	chSplitMaxS  = 10 * 60
	minOutputB   = 1024
	titleTimeout = 30 * 60 // seconds per ffmpeg title, applied via context
)

var (
	ErrNotDisc     = errors.New("disc: not an ISO or IMG image")
	ErrToolMissing = errors.New("disc: ffmpeg or ffprobe is not available")
	ErrNoTitles    = errors.New("disc: no DVD titles found")
	ErrNoFeatures  = errors.New("disc: no feature titles to extract")
	ErrIncomplete  = errors.New("disc: extract incomplete; ISO was kept")
)

// Title is one DVD title from ffmpeg dvdvideo.
type Title struct {
	N             int     `json:"n"`
	DurationS     float64 `json:"durationS"`
	Chapters      int     `json:"chapters"`
	Role          string  `json:"role"`
	SplitChapters bool    `json:"splitChapters,omitempty"`
}

// Output is one extracted MKV written next to the source image.
type Output struct {
	Path    string `json:"path"`
	Title   int    `json:"title"`
	Chapter int    `json:"chapter,omitempty"`
	Role    string `json:"role"`
}

// Map is the sidecar persisted before the ISO is deleted.
type Map struct {
	Version int      `json:"version"`
	Volume  string   `json:"volume"`
	Source  string   `json:"source"`
	Titles  []Title  `json:"titles"`
	Outputs []Output `json:"outputs,omitempty"`
	Deleted bool     `json:"deletedSource,omitempty"`
}

// Work is one ffmpeg extract (whole title or one chapter).
type Work struct {
	Title        int
	ChapterStart int
	ChapterEnd   int
	Name         string
	Role         string
}

// Result is Unpack's return.
type Result struct {
	Map           *Map
	Outputs       []string
	DeletedSource bool
}

// IsDiscImage reports whether path is an .iso or .img file.
func IsDiscImage(path string) bool {
	return config.IsDiscImage(path)
}

// SidecarPath is the JSON map next to src (stem.disc.json).
func SidecarPath(src string) string {
	ext := filepath.Ext(src)
	return strings.TrimSuffix(src, ext) + sidecarExt
}
