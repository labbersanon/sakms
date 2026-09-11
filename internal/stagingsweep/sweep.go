// Package stagingsweep deletes sakms-owned usenet staging directories that are
// safe to remove: leftovers after import, and aged orphans with no grab row.
//
// Ownership is decided by usenet.IsOwnedStagingPath — paths outside the
// configured staging root, or not sakms-minted GIDs, are never touched.
package stagingsweep

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-09-11: auto-sweep stale sakms staging (imported + aged orphans)
// Reason: ~122G of orphan RAR dirs left after failed/abandoned grabs; import
//         also left PAR2 behind. User: delete whole owned staging folder;
//         orphans after 7d; only if sakms owns the path.
// Troubleshooting: journal "stagingsweep:"; settings keys below; ownership in usenet/ownership.go
// Review if: staging root setting key changes or GID naming changes
// Related: usenet.RemoveOwnedStagingDir, api.importUsenetFromDisk

const (
	// OrphanDaysKey: days before an unreferenced owned staging dir is deleted.
	// Default 7. 0 disables orphan deletion (imported leftovers still cleaned).
	OrphanDaysKey = "usenet_stale_staging_orphan_days"
	// SweepSecondsKey: sweeper cadence. Default 3600. 0 disables the loop.
	SweepSecondsKey = "usenet_stale_staging_sweep_seconds"

	defaultOrphanDays  = 7
	defaultSweepSecs   = 3600
)

// GrabLookup resolves a download GID to a grab, or grabs.ErrNotFound.
type GrabLookup interface {
	GetByDownloadGID(ctx context.Context, gid string) (*grabs.Grab, error)
}

// LoadOrphanAge returns the orphan age threshold.
func LoadOrphanAge(ctx context.Context, store *settings.Store) time.Duration {
	days := defaultOrphanDays
	if store != nil {
		v, err := store.Get(ctx, OrphanDaysKey)
		if err == nil && strings.TrimSpace(v) != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				days = n
			}
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// LoadInterval returns the sweeper tick interval, or 0 to disable.
func LoadInterval(ctx context.Context, store *settings.Store) time.Duration {
	secs := defaultSweepSecs
	if store != nil {
		v, err := store.Get(ctx, SweepSecondsKey)
		if err == nil && strings.TrimSpace(v) != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				secs = n
			}
		}
	}
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// Run ticks until ctx is cancelled. interval<=0 means off (immediate return).
func Run(ctx context.Context, interval time.Duration, stagingRoot string, grabsStore GrabLookup, settingsStore *settings.Store) {
	if interval <= 0 {
		return
	}
	if stagingRoot == "" {
		log.Printf("stagingsweep: empty staging root — disabled")
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// One pass at boot so existing leftovers don't wait a full interval.
	runCycle(ctx, stagingRoot, grabsStore, settingsStore)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if settingsStore != nil {
				if next := LoadInterval(ctx, settingsStore); next <= 0 {
					log.Printf("stagingsweep: disabled via %s", SweepSecondsKey)
					return
				} else if next != interval {
					ticker.Reset(next)
					interval = next
				}
			}
			runCycle(ctx, stagingRoot, grabsStore, settingsStore)
		}
	}
}

func runCycle(ctx context.Context, stagingRoot string, grabsStore GrabLookup, settingsStore *settings.Store) {
	orphanAge := LoadOrphanAge(ctx, settingsStore)
	n, err := Sweep(ctx, time.Now(), stagingRoot, orphanAge, grabsStore)
	if err != nil {
		log.Printf("stagingsweep: cycle error: %v", err)
		return
	}
	if n > 0 {
		log.Printf("stagingsweep: removed %d owned staging dir(s)", n)
	}
}

// Sweep removes eligible owned staging directories. Returns how many were deleted.
func Sweep(ctx context.Context, now time.Time, stagingRoot string, orphanAge time.Duration, grabsStore GrabLookup) (int, error) {
	root := filepath.Clean(stagingRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !usenet.IsOwnedStagingPath(root, dir) {
			continue
		}
		gid := e.Name()
		shouldDelete, reason := decide(ctx, now, dir, gid, orphanAge, grabsStore)
		if !shouldDelete {
			continue
		}
		if err := usenet.RemoveOwnedStagingDir(root, dir); err != nil {
			log.Printf("stagingsweep: delete %s (%s): %v", dir, reason, err)
			continue
		}
		log.Printf("stagingsweep: deleted %s (%s)", dir, reason)
		removed++
	}
	return removed, nil
}

func decide(ctx context.Context, now time.Time, dir, gid string, orphanAge time.Duration, grabsStore GrabLookup) (bool, string) {
	if grabsStore == nil {
		return false, ""
	}
	g, err := grabsStore.GetByDownloadGID(ctx, gid)
	if err != nil {
		if !errors.Is(err, grabs.ErrNotFound) {
			// Ambiguous — do not delete if we cannot read grab state.
			log.Printf("stagingsweep: lookup %s: %v — skip", gid, err)
			return false, ""
		}
		// Orphan: no grab owns this GID.
		if orphanAge <= 0 {
			return false, ""
		}
		fi, err := os.Stat(dir)
		if err != nil {
			return false, ""
		}
		if now.Sub(fi.ModTime()) < orphanAge {
			return false, ""
		}
		return true, "orphan age exceeded"
	}
	if g.Status == grabs.Imported {
		return true, "grab imported"
	}
	// In-flight or failed-but-still-referenced — sakms may still need the files.
	return false, ""
}
