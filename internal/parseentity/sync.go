package parseentity

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/labbersanon/sakms/internal/stashapi"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// defaultSyncPages is how many pages the initial (cold-start) sync fetches
// from TPDB and stash-box before stopping. Each page is 20 items, so 200
// pages yields up to 4000 scenes worth of studio+performer names. Incremental
// syncs resume from the stored cursor and stop when the source returns an
// empty page.
// DefaultSyncPages is how many pages the initial (cold-start) sync fetches
// from TPDB and stash-box. Exported so the admin trigger handler can reuse
// it without duplicating the constant.
const DefaultSyncPages = 200

const defaultSyncPages = DefaultSyncPages

// SyncFromStash pulls unique studio names out of the local Stash instance's
// scene list and upserts them into the entity cache. Stash has no performer
// browse API, so only studios are synced from this source.
func SyncFromStash(ctx context.Context, store EntityStore, client *stashapi.Client) error {
	files, err := client.LoadAllScenes(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, f := range files {
		if f.Studio == "" || seen[f.Studio] {
			continue
		}
		seen[f.Studio] = true
		if err := store.UpsertStudio(ctx, f.Studio, "stash", ""); err != nil {
			slog.Warn("parseentity: stash studio upsert failed", "name", f.Studio, "err", err)
		}
	}
	slog.Info("parseentity: stash sync complete", "studios", len(seen))
	if err := store.SetSyncCursor(ctx, "stash", "done"); err != nil {
		slog.Warn("parseentity: failed to save stash sync cursor", "err", err)
	}
	return nil
}

// SyncFromTPDB paginates TPDB's scene and performer catalogs and upserts
// every studio name and performer name found. The stored cursor is an integer
// page number so incremental syncs resume from where the previous run stopped.
// maxPages caps how many pages are fetched in a single run (0 = defaultSyncPages).
func SyncFromTPDB(ctx context.Context, store EntityStore, client *tpdbrest.Client, maxPages int) error {
	if maxPages <= 0 {
		maxPages = defaultSyncPages
	}

	cursorStr, _, err := store.GetSyncCursor(ctx, "tpdb")
	if err != nil {
		return err
	}
	startPage := 1
	if cursorStr != "" {
		if p, err := strconv.Atoi(cursorStr); err == nil && p > 0 {
			startPage = p + 1
		}
	}

	// Sync scenes → extract studios + performers.
	sceneStudios := 0
	scenePerformers := 0
	for page := startPage; page < startPage+maxPages; page++ {
		scenes, err := client.BrowseScenes(ctx, page, 20, "")
		if err != nil {
			slog.Warn("parseentity: tpdb scene browse failed", "page", page, "err", err)
			break
		}
		if len(scenes) == 0 {
			break
		}
		for _, sc := range scenes {
			if sc.Site != "" {
				if err := store.UpsertStudio(ctx, sc.Site, "tpdb", ""); err != nil {
					slog.Warn("parseentity: tpdb studio upsert failed", "name", sc.Site, "err", err)
				} else {
					sceneStudios++
				}
			}
			for _, p := range sc.Performers {
				if p == "" {
					continue
				}
				if err := store.UpsertPerformer(ctx, p, "tpdb", ""); err != nil {
					slog.Warn("parseentity: tpdb performer upsert failed", "name", p, "err", err)
				} else {
					scenePerformers++
				}
			}
		}
		if err := store.SetSyncCursor(ctx, "tpdb", strconv.Itoa(page)); err != nil {
			slog.Warn("parseentity: failed to save tpdb cursor", "page", page, "err", err)
		}
		if len(scenes) < 20 {
			break // last page
		}
	}
	slog.Info("parseentity: tpdb sync complete", "studios_seen", sceneStudios, "performers_seen", scenePerformers)
	return nil
}

// SyncFromStashBox paginates a stash-box endpoint (StashDB or FansDB) and
// upserts studios and performers. source is the cursor key ("stashdb" or
// "fansdb"). maxPages works the same as SyncFromTPDB.
//
// Performers and studios use separate cursor keys (source+":performers" and
// source+":studios") so each resumes from the last page it actually fetched,
// independent of the other — matching SyncFromTPDB's per-page save behaviour.
func SyncFromStashBox(ctx context.Context, store EntityStore, client *stashbox.Client, source string, maxPages int) error {
	if maxPages <= 0 {
		maxPages = defaultSyncPages
	}

	// Sync performers.
	perfCursorKey := source + ":performers"
	perfCursorStr, _, err := store.GetSyncCursor(ctx, perfCursorKey)
	if err != nil {
		return err
	}
	perfStartPage := 1
	if perfCursorStr != "" {
		if p, err := strconv.Atoi(perfCursorStr); err == nil && p > 0 {
			perfStartPage = p + 1
		}
	}
	performerCount := 0
	for page := perfStartPage; page < perfStartPage+maxPages; page++ {
		performers, err := client.QueryPerformers(ctx, page, 20)
		if err != nil {
			slog.Warn("parseentity: stashbox performer query failed", "source", source, "page", page, "err", err)
			break
		}
		if len(performers) == 0 {
			break
		}
		for _, p := range performers {
			if err := store.UpsertPerformer(ctx, p.Name, source, p.ID); err != nil {
				slog.Warn("parseentity: stashbox performer upsert failed", "source", source, "name", p.Name, "err", err)
			} else {
				performerCount++
			}
		}
		if err := store.SetSyncCursor(ctx, perfCursorKey, strconv.Itoa(page)); err != nil {
			slog.Warn("parseentity: failed to save stashbox performer cursor", "source", source, "page", page, "err", err)
		}
		if len(performers) < 20 {
			break
		}
	}

	// Sync studios — separate cursor so it resumes independently of performers.
	studioCursorKey := source + ":studios"
	studioCursorStr, _, err := store.GetSyncCursor(ctx, studioCursorKey)
	if err != nil {
		return err
	}
	studioStartPage := 1
	if studioCursorStr != "" {
		if p, err := strconv.Atoi(studioCursorStr); err == nil && p > 0 {
			studioStartPage = p + 1
		}
	}
	studioCount := 0
	for page := studioStartPage; page < studioStartPage+maxPages; page++ {
		studios, err := client.QueryStudios(ctx, page, 20)
		if err != nil {
			slog.Warn("parseentity: stashbox studio query failed", "source", source, "page", page, "err", err)
			break
		}
		if len(studios) == 0 {
			break
		}
		for _, st := range studios {
			if err := store.UpsertStudio(ctx, st.Name, source, st.ID); err != nil {
				slog.Warn("parseentity: stashbox studio upsert failed", "source", source, "name", st.Name, "err", err)
			} else {
				studioCount++
			}
		}
		if err := store.SetSyncCursor(ctx, studioCursorKey, strconv.Itoa(page)); err != nil {
			slog.Warn("parseentity: failed to save stashbox studio cursor", "source", source, "page", page, "err", err)
		}
		if len(studios) < 20 {
			break
		}
	}

	slog.Info("parseentity: stashbox sync complete", "source", source,
		"studios", studioCount, "performers", performerCount)
	return nil
}
