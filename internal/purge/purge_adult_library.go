package purge

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
)

// CatalogTagHit is one fingerprint-resolved catalog scene's identity plus
// its genre names. ScanLibraryAdult only persists the tags when Box and
// SceneID match the library row, so a rematched phash cannot retag the
// wrong scene.
type CatalogTagHit struct {
	Box     string
	SceneID string
	Tags    []string
}

// CatalogTagSource looks up catalog genre names for identified Adult scenes
// that have no local library_scene_tags yet. Nil on ScanLibraryAdult skips
// the backfill (tests, or identify not configured).
type CatalogTagSource interface {
	TagsByPHash(ctx context.Context, phashes []string) (map[string]CatalogTagHit, error)
	TagsByID(ctx context.Context, box, sceneID string) ([]string, error)
}

// catalogTagBackfillBudget is how long ScanLibraryAdult may spend filling
// empty library_scene_tags from the catalog. Matching runs after this and
// must not share the deadline. Tests shrink it.
var catalogTagBackfillBudget = 90 * time.Second

// ScanLibraryAdult is Purge's Adult-library counterpart to ScanLibrary — used
// once Adult stops requiring Whisparr (see the plan this was built from,
// Stage 2). A scene is a flat one-file-done-once thing like a Movie Item, so
// tags live at the scene level and Purge proposes one row per matched SCENE,
// the direct analogue of ScanLibrary's Movies path. A rule's tags condition is
// matched against each scene's own local tags.
//
// SourcePath is set to the scene's on-disk file: ApplyLibraryAdult trusts it
// for the file removal (there is no GetScene-by-id to re-fetch through, unlike
// Movies' ApplyLibrary), so it is load-bearing, not merely informational.
// rules are evaluated in one per-scene loop (one proposal per scene, with
// every matched rule's fragment joined into its Reason); a Scene carries
// Size/QualityTier/CreatedAt directly, so no aggregation is needed.
//
// Claude 2026-08-14: optional aspect filters ListScenesByAspect.
// Reason: Organize Adult Movies chips (vertical) vs Scenes (horizontal).
//
//	Empty aspect is every row — scheduled scans and pruning preview stay All.
//
// Review if: Organize gains a dedicated Movies workflow.
// Claude 2026-08-14: catalogTags backfills library_scene_tags fill-if-empty.
// Reason: grab-import never wrote MatchResult.Tags, so a tags-only Clean-up
//
//	rule matched nothing against 250 identified scenes. Same class of Scan
//	side-write as Dedup caching phash — GET /tracked never looks up.
//
// Troubleshooting: BDSM rule (Bondage/Bound/Dungeon/Pee/Peeing) scanned [].
// Review if: every grab path writes tags and a one-shot backfill has run.
func ScanLibraryAdult(ctx context.Context, libStore *library.Store, rules []pruning.Rule, aspect string, catalogTags CatalogTagSource) ([]proposals.Proposal, error) {
	scenes, err := libStore.ListScenesFiltered(ctx, aspect)
	if err != nil {
		return nil, fmt.Errorf("loading scenes: %w", err)
	}

	if catalogTags != nil && rulesHaveTags(rules) {
		// Claude 2026-08-14: catalogTagBackfillBudget cap, child ctx.
		// Reason: VPS nginx proxy_read_timeout is 300s; 1s/host identify
		//   throttle × 250 SceneByID overran it (504 + Traefik 499). Matching
		//   stays on ctx so a deadline here cannot fail SceneTags afterwards.
		// Review if: backfill is batched or moved off the request path.
		backfillCtx, cancel := context.WithTimeout(ctx, catalogTagBackfillBudget)
		err := backfillSceneCatalogTags(backfillCtx, libStore, scenes, catalogTags)
		cancel()
		if err != nil {
			if backfillCtx.Err() != nil {
				log.Printf("purge: catalog tag backfill stopped early: %v", err)
			} else {
				return nil, err
			}
		}
	}

	now := time.Now().UTC()

	var out []proposals.Proposal
	for _, sc := range scenes {
		tags, err := libStore.SceneTags(ctx, sc.ID)
		if err != nil {
			return nil, fmt.Errorf("loading tags for %q: %w", sc.Title, err)
		}
		ruleReasons := pruning.MatchAny(rules, pruning.Subject{
			CreatedAt: sc.CreatedAt, SizeBytes: sc.Size, QualityTier: sc.QualityTier, Tags: tags, Rating: sc.Rating,
		}, now)
		if len(ruleReasons) == 0 {
			continue
		}
		out = append(out, proposals.Proposal{
			Mode: mode.Adult, Workflow: proposals.Purge, Status: proposals.Pending,
			SourceName: sc.Title, SourcePath: sc.FilePath, RootFolderPath: sc.RootFolderPath,
			Title: sc.Title, Studio: sc.Studio, Date: sc.Date, TrackedID: int(sc.ID),
			Reason: strings.Join(ruleReasons, "; "),
		})
	}
	return out, nil
}

func rulesHaveTags(rules []pruning.Rule) bool {
	for _, r := range rules {
		if len(r.Tags) > 0 {
			return true
		}
	}
	return false
}

// backfillSceneCatalogTags writes catalog genres onto identified scenes that
// have no local tags. Fingerprint batch first (every live identified scene
// already has a phash); SceneByID only for leftovers whose phash did not
// resolve to the same (box, scene_id). Local scenes and lookup failures are
// skipped — fail-closed, same as Match's empty-tags handling.
func backfillSceneCatalogTags(ctx context.Context, libStore *library.Store, scenes []library.Scene, src CatalogTagSource) error {
	var need []library.Scene
	for _, sc := range scenes {
		if library.IsLocalScene(sc.Box) || sc.Box == "" || sc.SceneID == "" {
			continue
		}
		tags, err := libStore.SceneTags(ctx, sc.ID)
		if err != nil {
			return fmt.Errorf("loading tags for %q: %w", sc.Title, err)
		}
		if len(tags) > 0 {
			continue
		}
		need = append(need, sc)
	}
	if len(need) == 0 {
		return nil
	}

	phashes := make([]string, 0, len(need))
	seenHash := map[string]bool{}
	for _, sc := range need {
		if sc.PHash == "" || seenHash[sc.PHash] {
			continue
		}
		seenHash[sc.PHash] = true
		phashes = append(phashes, sc.PHash)
	}

	var hits map[string]CatalogTagHit
	if len(phashes) > 0 {
		var err error
		hits, err = src.TagsByPHash(ctx, phashes)
		if err != nil {
			log.Printf("purge: catalog tag fingerprint lookup failed: %v", err)
			hits = nil
		}
	}

	filled := map[int64]bool{}
	for _, sc := range need {
		if sc.PHash == "" {
			continue
		}
		hit, ok := hits[sc.PHash]
		if !ok || len(hit.Tags) == 0 {
			continue
		}
		if !strings.EqualFold(hit.Box, sc.Box) || hit.SceneID != sc.SceneID {
			continue
		}
		if err := libStore.ApplyCatalogTags(ctx, sc.ID, strings.Join(hit.Tags, ",")); err != nil {
			return fmt.Errorf("persisting catalog tags for %q: %w", sc.Title, err)
		}
		filled[sc.ID] = true
	}

	for _, sc := range need {
		if filled[sc.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			log.Printf("purge: catalog tag backfill stopped after fingerprint fill (%d scenes): %v", len(filled), err)
			break
		}
		tags, err := src.TagsByID(ctx, sc.Box, sc.SceneID)
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("purge: catalog tag backfill stopped: %v", err)
				break
			}
			log.Printf("purge: catalog tag lookup for %s/%s failed: %v", sc.Box, sc.SceneID, err)
			continue
		}
		if len(tags) == 0 {
			continue
		}
		if err := libStore.ApplyCatalogTags(ctx, sc.ID, strings.Join(tags, ",")); err != nil {
			return fmt.Errorf("persisting catalog tags for %q: %w", sc.Title, err)
		}
	}
	return nil
}

// Claude 2026-08-14: previous ScanLibraryAdult (no catalogTags param) is
// replaced by the signature above. Commented out rather than deleted.
// Reason: grab-import never persisted catalog tags; Scan now backfills them.
// Review if: every grab path writes tags and the one-shot backfill has run.
//
// func ScanLibraryAdult(ctx context.Context, libStore *library.Store, rules []pruning.Rule, aspect string) ([]proposals.Proposal, error) {
// 	scenes, err := libStore.ListScenesFiltered(ctx, aspect)
// 	...
// }

// ApplyLibraryAdult is Purge's Adult-library counterpart to ApplyLibrary —
// removes the scene's file directly (no *arr app to ask) and deletes its
// record from libStore. p must be Pending and carry a TrackedID from
// ScanLibraryAdult (the scene's own id, following the same field convention
// the other purge paths established).
//
// Unlike ApplyLibrary (Movies), the file path is taken from p.SourcePath
// rather than re-fetched from the store: a scene has no GetScene-by-id lookup,
// only GetScene(box, sceneID), so the path captured at scan time is what
// Apply acts on.
//
// changes is a named return so a post-delete failure (libStore.DeleteScene)
// still reports the committed removal to the caller for
// Session.NotifyPlayers. p.SourcePath can legitimately be "" (a tracked scene
// with no file) — the Deleted PathChange is only ever appended inside the
// non-empty guard, and an already-gone file (os.IsNotExist) is not an error.
func ApplyLibraryAdult(ctx context.Context, libStore *library.Store, p proposals.Proposal) (changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}
	if p.TrackedID == 0 {
		return nil, fmt.Errorf("proposal %d has no scene id to delete", p.ID)
	}

	if p.SourcePath != "" {
		if err := os.Remove(p.SourcePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("deleting %q: %w", p.SourcePath, err)
		}
		changes = append(changes, mode.PathChange{Path: p.SourcePath, Kind: mode.Deleted})
	}
	if err := libStore.DeleteScene(ctx, int64(p.TrackedID)); err != nil {
		return changes, err
	}
	return changes, nil
}
