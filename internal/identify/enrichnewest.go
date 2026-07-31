package identify

import (
	"context"
	"log"
	"strings"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// Tunables for the Adult "newest" drill-down catalog enrichment. Both are
// package-level vars (not consts) matching this package's convention for
// tuned-but-test-overridable knobs (see entityverify.go's threshold block and
// newestScenesOutboundTimeout in internal/api) — nothing mutates them in
// production.
var (
	// catalogPages is how many pages of an entity's NEWEST scene catalog to
	// fetch from its box. 1 keeps enrichment O(1): one resolve + one catalog
	// call per drill. Bump to 2 (a +1 throttled call, still O(1)) if the
	// per-drill observability line shows frequent catalog-window misses.
	catalogPages = 1

	// enrichTitleThreshold is the local scene-title match bar — the SAME 0.4
	// title bar SearchStashBox/SearchTPDB use (boxlookup.go), looser than the
	// 0.6 name bar because scene titles are long/multi-token and matched via
	// containment-aware maxSimilarity.
	enrichTitleThreshold = 0.4
)

// EnrichNewestScenes resolves an entity (kind "studio"|"performer") by name to
// its catalog id in ONE already-known box, fetches that entity's NEWEST scene
// catalog page, and locally matches each raw release title against the fetched
// catalog using this package's own similarity primitives. Returns, keyed by the
// caller's releaseTitles slice index, a *MatchResult for each confident match.
//
// The box is passed in, not discovered: the caller already opened this exact
// entity's drill-down, so the operator has disambiguated it — the pool row that
// backs the drill (adult_newest_releases.entity_source) already records which
// single box originally identified this entity. Resolution therefore mirrors
// PerformerImage/StudioImage's shipped philosophy (entityverify.go): an EXACT
// name match in that one box, first candidate with a real id wins — no fuzzy
// threshold, no ambiguity/near-tie handling. This is how every other
// performer/studio in this app is already resolved.
//
// O(1) upstream calls (one resolve + one catalog fetch), every one routed
// through id.Throttle. Best-effort: an empty box, an entity the box can't
// exact-match (unreachable or genuinely absent), a box error, a throttle-abort,
// or no confident title match yields no entry for that release (never an error
// that should fail the caller). Local matching is zero-cost (CPU only). A
// release with no confident catalog match is simply absent from the returned
// map, so the caller can drop it cleanly.
func (id *Identifier) EnrichNewestScenes(ctx context.Context, kind, name, box string, releaseTitles []string) (map[int]*MatchResult, error) {
	result := make(map[int]*MatchResult)
	if id.Boxes == nil || box == "" || len(releaseTitles) == 0 {
		return result, nil
	}

	entityID := id.resolveEntityInBox(ctx, box, kind, name)
	if entityID == "" {
		// Firm observability requirement (mis-resolution detection): log every
		// drill's box + whether it resolved, even the no-match case.
		log.Printf("adult newest enrichment: kind=%q name=%q box=%q — no exact-name match, enriched 0/%d items",
			kind, name, box, len(releaseTitles))
		return result, nil
	}

	catalog := id.fetchCatalog(ctx, box, kind, entityID)
	result = matchReleaseTitles(releaseTitles, catalog)
	log.Printf("adult newest enrichment: kind=%q name=%q box=%q — resolved id=%q, enriched %d/%d items",
		kind, name, box, entityID, len(result), len(releaseTitles))
	return result, nil
}

// resolveEntityInBox resolves the drill entity to a catalog id in exactly ONE
// box via an EXACT name match, mirroring PerformerImage/StudioImage
// (entityverify.go) — the only difference is testing for a non-empty id instead
// of a non-empty image. No fuzzy threshold, no near-tie logic, no box iteration
// (the box is already known). Every upstream call is throttled first. Returns ""
// when the box isn't configured, the throttle wait is cancelled, the box errors,
// or no exact-name candidate exists.
func (id *Identifier) resolveEntityInBox(ctx context.Context, box, kind, name string) string {
	switch box {
	case "stashdb", "fansdb":
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			return ""
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return ""
		}
		if kind == "studio" {
			// FindStudio is itself an exact-name query (no candidate list to
			// scan), exactly as StudioImage uses it.
			studio, err := client.FindStudio(ctx, name)
			if err == nil && studio != nil && studio.ID != "" {
				return studio.ID
			}
			return ""
		}
		// performer: first candidate whose name exactly equals the drill name,
		// same p.Name == name test PerformerImage uses.
		candidates, err := client.SearchPerformer(ctx, name, 5)
		if err == nil {
			for _, p := range candidates {
				if p.Name == name && p.ID != "" {
					return p.ID
				}
			}
		}
		return ""

	case "tpdb":
		if id.Boxes.tpdb == nil {
			return ""
		}
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return ""
		}
		if kind == "studio" {
			if sites, err := id.Boxes.tpdb.SearchSites(ctx, name); err == nil {
				for _, s := range sites {
					if s.Name == name && s.ID != "" {
						return s.ID
					}
				}
			}
			return ""
		}
		if performers, err := id.Boxes.tpdb.SearchPerformers(ctx, name); err == nil {
			for _, p := range performers {
				if p.Name == name && p.ID != "" {
					return p.ID
				}
			}
		}
		return ""
	}
	return ""
}

// fetchCatalog fetches up to catalogPages of the entity's NEWEST scenes for one
// box and converts each to a *MatchResult using the same field construction the
// box-lookup paths use (boxlookup.go). Every page fetch is throttled. No
// resolveTPDBDuration re-fetch — the catalog scene's Duration is accepted as-is
// (0 = unknown).
func (id *Identifier) fetchCatalog(ctx context.Context, box, kind, entityID string) []*MatchResult {
	var out []*MatchResult
	for page := 1; page <= catalogPages; page++ {
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return out
		}
		switch box {
		case "stashdb", "fansdb":
			client := id.Boxes.stashBoxes[box]
			if client == nil {
				return out
			}
			var scenes []stashbox.Scene
			var err error
			if kind == "studio" {
				scenes, err = client.QueryScenesByStudio(ctx, entityID, page, 0)
			} else {
				scenes, err = client.QueryScenesByPerformer(ctx, entityID, page, 0)
			}
			if err != nil {
				return out
			}
			for _, s := range scenes {
				out = append(out, stashboxSceneToMatch(box, s))
			}
		case "tpdb":
			if id.Boxes.tpdb == nil {
				return out
			}
			var scenes []tpdbrest.Scene
			var err error
			if kind == "studio" {
				scenes, err = id.Boxes.tpdb.ScenesBySite(ctx, entityID, page, 0)
			} else {
				scenes, err = id.Boxes.tpdb.ScenesByPerformer(ctx, entityID, page, 0)
			}
			if err != nil {
				return out
			}
			for _, s := range scenes {
				out = append(out, tpdbSceneToMatch(s))
			}
		}
	}
	return out
}

// stashboxSceneToMatch mirrors SearchStashBox's Scene → MatchResult
// construction (boxlookup.go:74-78). Tags is empty for the catalog path: the
// browse query (queryScenesQuery) doesn't request tags, so s.Tags is nil — the
// same empty-Genres outcome as today's raw fallback, by design.
func stashboxSceneToMatch(box string, s stashbox.Scene) *MatchResult {
	return &MatchResult{
		Title: s.Title, Studio: s.StudioName, Date: s.ReleaseDate,
		Type: "scene", Source: box + "_catalog", SceneID: s.ID, Box: box,
		Image: s.ImageURL, Tags: strings.Join(s.Tags, ","), RuntimeSeconds: s.Duration,
	}
}

// tpdbSceneToMatch mirrors SearchTPDB's Scene → MatchResult construction
// (boxlookup.go:127-133) — Tags/Performers comma-joined with no space — but
// with RuntimeSeconds taken directly from the catalog scene (no
// resolveTPDBDuration re-fetch, which would be an O(N) unthrottled call).
func tpdbSceneToMatch(s tpdbrest.Scene) *MatchResult {
	tagNames := make([]string, len(s.Tags))
	for i, t := range s.Tags {
		tagNames[i] = t.Name
	}
	return &MatchResult{
		Title: s.Title, Studio: s.Site, Date: s.Date,
		Type: "scene", Source: "tpdb_catalog", SceneID: s.ID, Box: "tpdb",
		Image: s.Image, Tags: strings.Join(tagNames, ","),
		Performers:     strings.Join(s.Performers, ","),
		RuntimeSeconds: s.Duration,
	}
}

// MatchReleaseTitlesToCatalog is the exported entry point to this package's
// local, zero-upstream-cost scene-title matcher (matchReleaseTitles) for
// callers outside the entity-scoped EnrichNewestScenes path — e.g. Adult
// catalog Search grouping its one Prowlarr search's raw releases against the
// RSS pool's already-identified scenes (a caller-supplied catalog of
// *MatchResult), pure-CPU, before any bounded box lookup. Reuses the SAME
// primitive EnrichNewestScenes does (normalizeForSearch + maxSimilarity +
// enrichTitleThreshold) rather than forking a second matcher. Returns, keyed by
// releaseTitles index, the best confident catalog match per release.
func MatchReleaseTitlesToCatalog(releaseTitles []string, catalog []*MatchResult) map[int]*MatchResult {
	return matchReleaseTitles(releaseTitles, catalog)
}

// matchReleaseTitles matches each release title against the box's catalog using
// the asymmetry-safe maxSimilarity (so release cruft matches a clean catalog
// title via containment), keeping the best catalog scene per release index if
// it clears enrichTitleThreshold. Zero upstream cost — CPU only.
func matchReleaseTitles(releaseTitles []string, catalog []*MatchResult) map[int]*MatchResult {
	out := make(map[int]*MatchResult)
	for i, rt := range releaseTitles {
		norm := normalizeForSearch(rt)
		var best *MatchResult
		bestScore := 0.0
		for _, mr := range catalog {
			if s := maxSimilarity(norm, mr.Title); s > bestScore {
				bestScore = s
				best = mr
			}
		}
		if best != nil && bestScore >= enrichTitleThreshold {
			out[i] = best
		}
	}
	return out
}
