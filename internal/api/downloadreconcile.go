package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-09-11: ARR-parity in-flight download reconcile (built-in engines)
// Reason: usenet/torrent managers are memory-only; after restart GIDs look
//         unknown and queued grabs were stranded forever (sweep skips unknown)
// Troubleshooting: journal "download reconcile:"; never parks solely on unknown GID
// Review if: torrent metainfo column lands so relaunch does not need DownloadURL
// Related: usenet.RelaunchNZB, sweepUsenetFailures, UsenetCompleteImporter

// restoreMissingURLReason is parked only when a forgotten in-flight grab has no
// durable DownloadURL to relaunch from. Detail-free: rendered on Requests.
const restoreMissingURLReason = "the in-flight download could not be restored after a restart — it will be re-searched"

// DownloadReconcileDeps is everything ReconcileInFlightDownloads needs to
// import completed staging or re-attach forgotten engine jobs. Nil managers
// / stores are skipped safely so tests and partial boots stay quiet.
type DownloadReconcileDeps struct {
	HTTPClient    *http.Client
	ConnStore     *connections.Store
	SCStore       *serviceconn.Store
	SettingsStore *settings.Store
	GrabsStore    *grabs.Store
	LibStore      *library.Store
	Prober        dedup.Prober
	VideoHasher   rename.PHasher
	DL            *downloader.Manager
	NZB           *usenet.Manager
}

// ReconcileInFlightDownloads is the ARR-style queue sync for sakms's built-in
// downloaders. For every queued/downloading grab whose live engine no longer
// knows the GID:
//
//  1. Usenet staging already has an importable video → import (same path as
//     check-import's disk fallback) and clear owned staging.
//  2. Torrent → re-AddTorrent from the durable DownloadURL (anacrolix reuses
//     pieces under StagingDir).
//  3. Usenet incomplete/empty → RelaunchNZB into the same nzb-* directory.
//  4. Missing DownloadURL → park pending_retry (the only park this pass does).
//
// An unknown GID alone NEVER parks — that was the boot-storm trap the failure
// sweep correctly avoids, and this pass exists to restore rather than give up.
func ReconcileInFlightDownloads(ctx context.Context, deps DownloadReconcileDeps) {
	if deps.GrabsStore == nil {
		return
	}
	for _, m := range usenetRetryModes {
		list, err := deps.GrabsStore.List(ctx, m)
		if err != nil {
			log.Printf("download reconcile: listing %s grabs: %v", m, err)
			continue
		}
		for i := range list {
			reconcileOneInFlight(ctx, deps, &list[i])
		}
	}
}

func reconcileOneInFlight(ctx context.Context, deps DownloadReconcileDeps, g *grabs.Grab) {
	if g.Status != grabs.Queued && g.Status != grabs.Downloading {
		return
	}
	if g.DownloadGID == "" {
		return
	}

	if strings.HasPrefix(g.DownloadGID, usenetGIDPrefix) {
		reconcileUsenetInFlight(ctx, deps, g)
		return
	}
	reconcileTorrentInFlight(ctx, deps, g)
}

func reconcileUsenetInFlight(ctx context.Context, deps DownloadReconcileDeps, g *grabs.Grab) {
	if deps.NZB == nil {
		return
	}
	live, err := deps.NZB.FindByGID(g.DownloadGID)
	if err != nil {
		log.Printf("download reconcile: usenet lookup grab %d gid %s: %v", g.ID, g.DownloadGID, err)
		return
	}
	if live != nil {
		return // engine still owns it — failure sweep / completion path apply
	}

	stagingPath := filepath.Join(deps.NZB.StagingDir(), g.DownloadGID)
	// Claude 2026-09-11: force-full skips import; otherwise require owned+complete staging
	// Reason: force-full promises re-download; ResolveVideoFile alone still false-completes
	// Troubleshooting: journal "force-full" / "staging not import-ready"; then relaunch
	// Review if: force-full becomes a one-shot that auto-clears after relaunch
	_, forceFull := deps.NZB.ResumePolicy()
	if forceFull {
		if err := usenet.ClearResumeArtifacts(stagingPath); err != nil {
			log.Printf("download reconcile: clear resume artifacts %s: %v", stagingPath, err)
		}
		deps.NZB.ClearResumeMirror(g.DownloadGID)
		log.Printf("download reconcile: grab %d force-full — skipping staging import, will relaunch", g.ID)
	} else if ok, why := usenetStagingReadyForImport(deps.NZB.StagingDir(), stagingPath); ok {
		if err := reconcileImportUsenet(ctx, deps, g, stagingPath); err != nil {
			log.Printf("download reconcile: importing usenet staging for grab %d: %v — will relaunch", g.ID, err)
		} else {
			log.Printf("download reconcile: grab %d (%s) imported from staging after engine forget", g.ID, g.Title)
			return
		}
	} else if why != "" {
		log.Printf("download reconcile: grab %d staging not import-ready (%s) — will relaunch", g.ID, why)
	}

	if strings.TrimSpace(g.DownloadURL) == "" {
		if err := parkGrabForRetry(ctx, AutoGrabDeps{SettingsStore: deps.SettingsStore, GrabsStore: deps.GrabsStore}, g.ID, restoreMissingURLReason); err != nil {
			log.Printf("download reconcile: parking grab %d (missing URL): %v", g.ID, err)
			return
		}
		log.Printf("download reconcile: grab %d parked — no durable download URL to relaunch", g.ID)
		return
	}

	if err := deps.NZB.RelaunchNZB(ctx, g.DownloadGID, g.DownloadURL, g.Title); err != nil {
		log.Printf("download reconcile: relaunching usenet grab %d gid %s: %v", g.ID, g.DownloadGID, err)
		return
	}
	log.Printf("download reconcile: grab %d (%s) relaunched into %s", g.ID, g.Title, g.DownloadGID)
}

func reconcileTorrentInFlight(ctx context.Context, deps DownloadReconcileDeps, g *grabs.Grab) {
	if deps.DL == nil {
		return
	}
	live, err := deps.DL.FindByGID(g.DownloadGID)
	if err != nil {
		log.Printf("download reconcile: torrent lookup grab %d gid %s: %v", g.ID, g.DownloadGID, err)
		return
	}
	if live != nil {
		return
	}

	if strings.TrimSpace(g.DownloadURL) == "" {
		if err := parkGrabForRetry(ctx, AutoGrabDeps{SettingsStore: deps.SettingsStore, GrabsStore: deps.GrabsStore}, g.ID, restoreMissingURLReason); err != nil {
			log.Printf("download reconcile: parking torrent grab %d (missing URL): %v", g.ID, err)
			return
		}
		log.Printf("download reconcile: torrent grab %d parked — no durable download URL to re-add", g.ID)
		return
	}

	// Claude 2026-09-11: defer torrent restore until the engine is up
	// Reason: critic — boot reconcile raced Start(); AddTorrent failed with
	//         "engine not running" and usenet-retry may be off
	// Troubleshooting: journal "engine not ready"; main WaitForEngine before reconcile
	// Review if: Start exposes a ready channel instead of polling
	if !deps.DL.EngineReady() {
		log.Printf("download reconcile: torrent grab %d — engine not ready, deferring restore", g.ID)
		return
	}

	newGID, err := deps.DL.AddTorrent(ctx, g.DownloadURL)
	if err != nil {
		log.Printf("download reconcile: re-adding torrent grab %d: %v", g.ID, err)
		return
	}
	if newGID != "" && newGID != g.DownloadGID {
		if err := deps.GrabsStore.SetDownloadGID(ctx, g.ID, newGID); err != nil {
			log.Printf("download reconcile: relinking grab %d to torrent gid %s: %v", g.ID, newGID, err)
			return
		}
	}
	log.Printf("download reconcile: grab %d (%s) torrent re-added as %s", g.ID, g.Title, newGID)
}

// reconcileImportUsenet is the non-HTTP twin of importUsenetFromDisk / the
// UsenetCompleteImporter success path: import, mark imported, clear owned staging.
func reconcileImportUsenet(ctx context.Context, deps DownloadReconcileDeps, g *grabs.Grab, contentPath string) error {
	if deps.LibStore == nil || deps.SettingsStore == nil {
		return fmt.Errorf("library/settings store unavailable for reconcile import")
	}
	sess, err := mode.Build(ctx, deps.ConnStore, deps.SCStore, deps.SettingsStore, deps.HTTPClient, deps.DL, g.Mode)
	if err != nil {
		return err
	}
	changes, err := importGrabContent(ctx, deps.LibStore, g, contentPath, string(autoGrabTier(ctx, deps.SettingsStore, g.Mode)), deps.SettingsStore, sess, deps.VideoHasher, deps.Prober)
	if err != nil {
		return err
	}
	postGrabRuntimeReview(ctx, deps.Prober, deps.GrabsStore, sess, g, changes)
	sess.NotifyPlayers(ctx, changes)
	_ = deps.GrabsStore.SetDownloadStatus(ctx, g.ID, "complete", contentPath)
	if err := deps.GrabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
		return err
	}
	clearOwnedUsenetStaging(deps.NZB, g.DownloadGID)
	return nil
}

// minUsenetReconcileImportBytes rejects hollow/tiny ResolveVideoFile hits
// (samples, truncates) that are not a finished feature release.
const minUsenetReconcileImportBytes = 1 << 20 // 1 MiB

// usenetStagingReadyForImport gates reconcile import: owned staging, non-sample
// video, and a minimum size floor.
func usenetStagingReadyForImport(stagingRoot, stagingPath string) (ok bool, reason string) {
	if !usenet.IsOwnedStagingPath(stagingRoot, stagingPath) {
		return false, "not owned staging"
	}
	if _, err := os.Stat(filepath.Join(stagingPath, usenet.ResumeFileName)); err == nil {
		return false, "resume sidecar present"
	} else if err != nil && !os.IsNotExist(err) {
		return false, "resume sidecar stat failed"
	}
	video, err := library.ResolveVideoFile(stagingPath)
	if err != nil || video == "" {
		return false, "no video"
	}
	if searchterm.IsSampleVideo(video) {
		return false, "sample video"
	}
	fi, err := os.Stat(video)
	if err != nil {
		return false, "video stat failed"
	}
	if fi.Size() < minUsenetReconcileImportBytes {
		return false, "video too small"
	}
	return true, ""
}
