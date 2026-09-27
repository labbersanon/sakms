package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/usenet"
)

func dirBytes(dir string) int64 {
	if dir == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// Claude 2026-09-26: Downloads card detail popup mapping + torrent actions.
// Reason: cards stay compact; popup needs catalog identity and protocol telemetry.
// Troubleshooting: popup heading empty — catalogTitle comes from the grab row.
// Review if: a dedicated GET /api/downloads/{gid} replaces list enrichment.

func usenetDetailsFromEngine(d usenet.Download) *apidto.UsenetDownloadDetails {
	statDone, statTotal := int64(0), int64(0)
	if d.Phase == "precheck" {
		statDone, statTotal = d.PhaseDone, d.PhaseTotal
	}
	out := &apidto.UsenetDownloadDetails{
		SegmentDone:     d.SegmentDone,
		SegmentTotal:    d.SegmentTotal,
		CurrentFile:     d.CurrentFile,
		CurrentSeg:      d.CurrentSeg,
		CurrentSegTotal: d.CurrentSegTotal,
		StatDone:        statDone,
		StatTotal:       statTotal,
		WaitReason:      d.WaitReason,
		RepairFile:      d.RepairFile,
		SidecarAgeSec:   d.SidecarAgeSec,
		ActiveConns:     d.ActiveConns,
		MaxConns:        d.MaxConns,
		StagingPath:     d.Dir,
		BytesOnDisk:     dirBytes(d.Dir),
		FailingSegment:  d.FailingSegment,
	}
	if usenetDetailsEmpty(out) {
		return nil
	}
	return out
}

func usenetDetailsEmpty(d *apidto.UsenetDownloadDetails) bool {
	if d == nil {
		return true
	}
	return d.SegmentTotal == 0 && d.CurrentFile == "" && d.StatTotal == 0 &&
		d.WaitReason == "" && d.RepairFile == "" && d.SidecarAgeSec == nil &&
		d.MaxConns == 0 && d.StagingPath == "" && d.BytesOnDisk == 0 &&
		d.FailingSegment == "" && len(d.TriedReleases) == 0
}

func torrentDetailsFromEngine(d downloader.Download) *apidto.TorrentDownloadDetails {
	files := make([]apidto.TorrentFileDetail, 0, len(d.TorrentFiles))
	for _, f := range d.TorrentFiles {
		files = append(files, apidto.TorrentFileDetail{
			Path: f.Path, Length: f.Length, Completed: f.Completed, Priority: f.Priority,
		})
	}
	trackers := make([]apidto.TorrentTrackerDetail, 0, len(d.Trackers))
	for _, tr := range d.Trackers {
		trackers = append(trackers, apidto.TorrentTrackerDetail{
			URL: tr.URL, Status: tr.Status, Message: tr.Message,
		})
	}
	out := &apidto.TorrentDownloadDetails{
		PeerCount:       d.PeerCount,
		Availability:    d.Availability,
		Uploaded:        d.Uploaded,
		Ratio:           d.Ratio,
		SeedRatioGoal:   d.SeedRatioGoal,
		SeedTimeGoalSec: d.SeedTimeGoalSec,
		InfoHash:        d.InfoHash,
		Magnet:          d.Magnet,
		PiecesHave:      d.PiecesHave,
		PiecesTotal:     d.PiecesTotal,
		PieceHeatmap:    d.PieceHeatmap,
		Files:           files,
		Trackers:        trackers,
		SavePath:        d.Dir,
		BytesOnDisk:     dirBytes(d.Dir),
	}
	if torrentDetailsEmpty(out) {
		return nil
	}
	return out
}

func torrentDetailsEmpty(d *apidto.TorrentDownloadDetails) bool {
	if d == nil {
		return true
	}
	return d.PeerCount == 0 && d.Availability == 0 && d.Uploaded == 0 &&
		d.Ratio == 0 && d.SeedRatioGoal == 0 && d.SeedTimeGoalSec == 0 &&
		d.InfoHash == "" && d.Magnet == "" && d.PiecesTotal == 0 &&
		len(d.Files) == 0 && len(d.Trackers) == 0 && d.SavePath == "" &&
		d.BytesOnDisk == 0
}

func catalogTitleFromGrab(g *grabs.Grab) string {
	if g == nil {
		return ""
	}
	title := strings.TrimSpace(g.Title)
	if title == "" {
		return ""
	}
	if g.SeasonSpecified && g.SeasonNumber > 0 && g.EpisodeNumber > 0 {
		return fmt.Sprintf("%s S%02dE%02d", title, g.SeasonNumber, g.EpisodeNumber)
	}
	if g.SeasonSpecified && g.SeasonNumber > 0 {
		return fmt.Sprintf("%s S%02d", title, g.SeasonNumber)
	}
	return title
}

func triedReleasesFromGrab(g *grabs.Grab) []apidto.DownloadTriedRelease {
	if g == nil {
		return nil
	}
	n := grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(g.TriedReleaseKeys))
	reason := strings.TrimSpace(g.RetryReason)
	if n == 0 && reason == "" {
		return nil
	}
	label := "Last park"
	if n > 0 {
		label = fmt.Sprintf("%d prior NZB(s) excluded this episode", n)
	}
	return []apidto.DownloadTriedRelease{{Label: label, Reason: reason}}
}

func enrichDownloadDetails(ctx context.Context, grabsStore *grabs.Store, rows []apidto.Download) {
	if grabsStore == nil {
		return
	}
	for i := range rows {
		g, err := grabsStore.GetByDownloadGID(ctx, rows[i].GID)
		if err != nil || g == nil {
			continue
		}
		rows[i].CatalogTitle = catalogTitleFromGrab(g)
		rows[i].Indexer = strings.TrimSpace(g.Indexer)
		if rows[i].Protocol != apidto.DownloadProtocolUsenet {
			continue
		}
		tried := triedReleasesFromGrab(g)
		if len(tried) == 0 {
			continue
		}
		if rows[i].Usenet == nil {
			rows[i].Usenet = &apidto.UsenetDownloadDetails{}
		}
		rows[i].Usenet.TriedReleases = tried
	}
}

func reannounceDownloadHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		if strings.HasPrefix(gid, "nzb-") {
			http.Error(w, "reannounce is torrent-only", http.StatusBadRequest)
			return
		}
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		if err := dl.Reannounce(gid); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func recheckDownloadHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		if strings.HasPrefix(gid, "nzb-") {
			http.Error(w, "recheck is torrent-only", http.StatusBadRequest)
			return
		}
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		if err := dl.Recheck(gid); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func putDownloadFilePriorityHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid := r.PathValue("gid")
		if strings.HasPrefix(gid, "nzb-") {
			http.Error(w, "file priority is torrent-only", http.StatusBadRequest)
			return
		}
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		var req apidto.DownloadFilePriorityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := dl.SetFilePriority(gid, req.Path, req.Priority); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
