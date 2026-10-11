package api

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-10: import-time bitrate floor (actual size × probed/TMDB runtime).
// Reason: Discover Grab dispatched American.Horror.Story.S13E06.1080p.x265-ELiTE
//   (~294 MiB / 24 min, score 2.74 vs Medium/1080 floor 5). Import then stamped
//   autoGrabTier=medium with no size check, so the library showed a quality pass.
// Troubleshooting: journal "import: below quality floor"; Requests alternate-NZB park.
// Review if: Adult gains a confirmed TPDB runtime and should share this gate.
// Related files: internal/autograb/autograb.go; usenetcontent.go; autograb_shared.go

const contentBelowFloorReason = "the downloaded video is below the quality floor — trying a different release"

func refuseImportBelowFloor(ctx context.Context, libStore *library.Store, settingsStore *settings.Store, sess *mode.Session, g *grabs.Grab, videoPath string, prober dedup.Prober) error {
	if g == nil || g.Mode == mode.Adult {
		return nil
	}
	c, ok := importQualityCandidate(ctx, sess, g, videoPath, prober)
	if !ok {
		return nil
	}
	floor := quality.Lowest(resolveAutoGrabTiersForTitle(ctx, libStore, settingsStore, g.Mode, g.TMDBID))
	gr := autograb.GradeCandidate(c, floor, 0)
	if !gr.BitrateKnown {
		return nil
	}
	switch gr.Status {
	case autograb.StatusBelowFloor, autograb.StatusMislabeled:
		log.Printf("import: below quality floor file=%q title=%q status=%s score=%.2fMbps floor=%.2fMbps implied=%.2fMbps runtime=%.0fs size=%d codec=%s res=%d",
			filepath.Base(videoPath), g.Title, gr.Status, gr.Score, gr.FloorMbps, gr.ImpliedMbps,
			c.RuntimeSeconds, c.SizeBytes, c.Codec, c.Resolution)
		return fmt.Errorf("%w: %w", usenet.ErrContentUnusable, autograb.ErrBelowQualityFloor)
	default:
		return nil
	}
}

func importQualityCandidate(ctx context.Context, sess *mode.Session, g *grabs.Grab, videoPath string, prober dedup.Prober) (autograb.Candidate, bool) {
	size := library.FileSize(videoPath)
	if size <= 0 {
		return autograb.Candidate{}, false
	}
	info := release.Parse(filepath.Base(videoPath))
	c := autograb.Candidate{
		Title:      filepath.Base(videoPath),
		Protocol:   g.Protocol,
		SizeBytes:  size,
		Resolution: info.Resolution,
		Codec:      info.Codec,
		Source:     info.Source,
	}
	if prober != nil {
		if p, err := prober.Probe(ctx, videoPath); err == nil && p != nil {
			if p.Duration > 0 {
				c.RuntimeSeconds = p.Duration
			}
			if p.Height > 0 {
				c.Resolution = snapImportResolution(p.Height)
			}
			if codec := codecFromProbeName(p.CodecName); codec != "" {
				c.Codec = codec
			}
		}
	}
	if c.RuntimeSeconds <= 0 && sess != nil && sess.TMDB != nil && g.TMDBID > 0 {
		switch g.Mode {
		case mode.Series:
			c.RuntimeSeconds = seriesEpisodeRuntimeSeconds(ctx, sess, g.TMDBID, g.SeasonNumber, g.EpisodeNumber)
		case mode.Movies:
			if details, err := sess.TMDB.MovieDetails(ctx, g.TMDBID); err == nil && details.Runtime > 0 {
				c.RuntimeSeconds = float64(details.Runtime) * 60
			}
		}
	}
	if c.SizeBytes <= 0 || c.RuntimeSeconds <= 0 {
		return autograb.Candidate{}, false
	}
	return c, true
}

func snapImportResolution(height int) int {
	switch {
	case height >= 2160:
		return 2160
	case height >= 1080:
		return 1080
	case height >= 720:
		return 720
	case height >= 480:
		return 480
	default:
		return 0
	}
}

func codecFromProbeName(name string) string {
	c := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(c, "hevc"), strings.Contains(c, "h265"), c == "265":
		return "x265"
	case strings.Contains(c, "av1"):
		return "av1"
	case strings.Contains(c, "h264"), strings.Contains(c, "avc"):
		return "x264"
	default:
		return ""
	}
}
