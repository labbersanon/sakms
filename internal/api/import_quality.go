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

// grabOriginSearchPick is grabs.origin for POST /api/modes/{mode}/search/grab.
// A clicked release that misses the bitrate floor is kept (same as stopgap).
// Mislabeled bait on the on-disk file still rejects — the file post-check
// stays on for every origin. Upgrade-watch still replaces a kept miss.
const grabOriginSearchPick = "search-pick"

// grabOriginQualityStopgap is grabs.origin for an unattended Movies/Series
// auto-grab that kept the best honest below-floor release. Import keeps the
// file (min/max resolution and mislabel still reject). upgrade_watch turns on
// so the sixth retry-cycle pass can replace it with preferred.
const grabOriginQualityStopgap = "quality-stopgap"

func keepBelowFloorOrigin(origin string) bool {
	return origin == grabOriginSearchPick || origin == grabOriginQualityStopgap
}

func refuseImportBelowFloor(ctx context.Context, libStore *library.Store, settingsStore *settings.Store, sess *mode.Session, g *grabs.Grab, videoPath string, prober dedup.Prober) error {
	if g == nil || g.Mode == mode.Adult {
		return nil
	}
	c, ok := importQualityCandidate(ctx, libStore, sess, g, videoPath, prober)
	if !ok {
		return nil
	}
	floors := resolveAutoGrabFloorsForTitle(ctx, libStore, settingsStore, g.Mode, g.TMDBID)
	minRes, maxRes := resolveResolutionBandForTitle(ctx, libStore, settingsStore, g.Mode, g.TMDBID)
	if minRes > 0 && c.Resolution > 0 && c.Resolution < minRes {
		return fmt.Errorf("%w: %w", usenet.ErrContentUnusable, autograb.ErrBelowQualityFloor)
	}
	if maxRes > 0 && c.Resolution > maxRes {
		return fmt.Errorf("%w: %w", usenet.ErrContentUnusable, autograb.ErrBelowQualityFloor)
	}
	floor := autograb.FloorForResolution(floors, c)
	// Claude 2026-10-22: import grades the file without FileFlows padding.
	// Reason: Furious DoVi 5.1 WEB-DL was deleted at 26.24 vs 27.50 after
	//   the 25% non-AV1 pick margin plus HDR/5.1 premiums. Unpadded 32.8
	//   clears that floor. Mislabeled bait and min/max resolution stay.
	// Troubleshooting: journal "import: below quality floor" on a just-
	//   finished HDR 5.1 encode → GradeImportedFile not used. Review if:
	//   FileFlows AV1 re-encode is dropped.
	gr := autograb.GradeImportedFile(c, floor)
	if !gr.BitrateKnown {
		return nil
	}
	// Claude 2026-10-21: keep an honest below-floor file and stop alternate NZBs.
	// Reason: Furious S01E04 grabbed as Qualified (NZB size/runtime), then
	//   import re-graded 26.24 vs 27.50 Mbps and ParkForAlternateRelease
	//   walked CHDWEB → playWEB → TURG → ETHEL → Normal1406. Grab-time
	//   stopgap only stamped origin when SelectBest found nothing Qualified,
	//   so this path never kept the replacement it had already downloaded.
	// Troubleshooting: journal "import: quality-stopgap kept"; grab origin
	//   becomes quality-stopgap so upgrade-watch hunts preferred later.
	// Review if: StatusMislabeled should also keep (it must not — bait NZBs).
	// Related files: grabOriginQualityStopgap; enableUpgradeWatchAfterStopgap;
	//   drainAlternateReleaseRetries.
	if gr.Status == autograb.StatusBelowFloor {
		if !keepBelowFloorOrigin(g.Origin) {
			g.Origin = grabOriginQualityStopgap
		}
		log.Printf("import: quality-stopgap kept file=%q title=%q status=%s score=%.2fMbps floor=%.2fMbps",
			filepath.Base(videoPath), g.Title, gr.Status, gr.Score, gr.FloorMbps)
		return nil
	}
	// Claude 2026-10-22: mislabel still rejects every origin, including search-pick.
	// Reason: search-pick used to return before grading the on-disk file, so a
	//   clicked 1 MiB "2160p" bait skipped the security check. Quality floor
	//   misses stay kept; fake labels are not a preference miss.
	// Troubleshooting: journal "import: search-pick kept" on a sample/bait
	//   size → this early return came back. Review if: operator override
	//   to keep a mislabeled click (then stamp Low, do not skip the probe).
	if gr.Status == autograb.StatusMislabeled {
		log.Printf("import: below quality floor file=%q title=%q status=%s score=%.2fMbps floor=%.2fMbps implied=%.2fMbps runtime=%.0fs size=%d codec=%s res=%d",
			filepath.Base(videoPath), g.Title, gr.Status, gr.Score, gr.FloorMbps, gr.ImpliedMbps,
			c.RuntimeSeconds, c.SizeBytes, c.Codec, c.Resolution)
		return fmt.Errorf("%w: %w", usenet.ErrContentUnusable, autograb.ErrBelowQualityFloor)
	}
	return nil
}

// searchPickLibraryTier is the library quality_tier for one imported video.
// Search-screen picks and quality-stopgap grabs that miss the title floor are
// stamped with the highest tier they actually meet (Low when they meet none),
// so upgrade-watch can replace them. Every other grab keeps fallback, which
// is the mode floor.
func searchPickLibraryTier(ctx context.Context, libStore *library.Store, settingsStore *settings.Store, sess *mode.Session, g *grabs.Grab, videoPath string, prober dedup.Prober, fallback string) string {
	if g == nil || !keepBelowFloorOrigin(g.Origin) || g.Mode == mode.Adult {
		return fallback
	}
	c, ok := importQualityCandidate(ctx, libStore, sess, g, videoPath, prober)
	if !ok {
		return fallback
	}
	floors := resolveAutoGrabFloorsForTitle(ctx, libStore, settingsStore, g.Mode, g.TMDBID)
	floor := autograb.FloorForResolution(floors, c)
	gr := autograb.GradeImportedFile(c, floor)
	if !gr.BitrateKnown || gr.Qualified {
		return fallback
	}
	met := quality.Low
	for i := len(quality.AllTiers) - 1; i >= 0; i-- {
		if autograb.GradeImportedFile(c, quality.AllTiers[i]).Qualified {
			met = quality.AllTiers[i]
			break
		}
	}
	// Claude 2026-10-18: stamp the met tier, not the mode floor.
	// Reason: import used to record Medium on a file that missed Medium, so
	//   upgrade-watch treated the kept Search pick as already good enough.
	// Troubleshooting: library quality_tier stays "low" while the floor is
	//   medium; upgrade-watch then lists the episode.
	// Review if: episode rows store probed Mbps and the watch reads that.
	// Related files: fileNeedsQualityUpgrade; grabOriginSearchPick.
	log.Printf("import: %s kept file=%q title=%q status=%s score=%.2fMbps floor=%.2fMbps stamped=%s",
		g.Origin, filepath.Base(videoPath), g.Title, gr.Status, gr.Score, gr.FloorMbps, met)
	return string(met)
}

func enableUpgradeWatchAfterStopgap(ctx context.Context, libStore *library.Store, g *grabs.Grab, movieID, seriesID int64) {
	if libStore == nil || g == nil || g.Origin != grabOriginQualityStopgap {
		return
	}
	switch g.Mode {
	case mode.Movies:
		if movieID == 0 {
			return
		}
		if err := libStore.SetItemUpgradeWatch(ctx, movieID, true); err != nil {
			log.Printf("import: enabling upgrade-watch on movie %d: %v", movieID, err)
		}
	case mode.Series:
		if seriesID == 0 {
			return
		}
		if err := libStore.SetSeriesUpgradeWatch(ctx, seriesID, true); err != nil {
			log.Printf("import: enabling upgrade-watch on series %d: %v", seriesID, err)
		}
	}
}

func importQualityCandidate(ctx context.Context, libStore *library.Store, sess *mode.Session, g *grabs.Grab, videoPath string, prober dedup.Prober) (autograb.Candidate, bool) {
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
		HDR:        info.HDR,
		Channels:   info.Channels,
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
			c.HDR = p.HDR
			c.Channels = release.ChannelsFromCount(p.AudioChannels)
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
	// Claude 2026-10-13: import re-grade must see Animation for 480p Low floor.
	// Reason: pre-grab SelectBest uses applyTitleAnimation; import GradeCandidate
	//   would otherwise keep live Medium@480 and refuse the NZB we just grabbed.
	// Troubleshooting: journal "import: below quality floor" on cartoon DVDRips.
	// Review if: 720p Animation shares the 480p discount.
	c.Animation = titleIsAnimation(ctx, libStore, sess, g.Mode, g.TMDBID, g.TVDBID)
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
